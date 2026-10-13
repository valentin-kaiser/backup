package web

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/audit"
	api "github.com/Valentin-Kaiser/backup/worker/portal/pkg/web/api/v1"
	"github.com/gorilla/websocket"
	"github.com/valentin-kaiser/go-core/logging/log"
)

// Access tells whether the request carries a valid session. The proxy asks again for every websocket frame, so a
// session that ended (password changed, logout) cuts the connection at the next call.
type Access func(r *http.Request) bool

const (
	subprotocol        = "xrpc.json"
	maxFrame           = 1 << 20
	maxBody            = 64 << 10
	recheckEvery       = 30 * time.Second
	codeForbidden      = -32001
	codeInvalidRequest = -32600
	handshakeLimit     = 10 * time.Second
	// daemonPath is the endpoint of the daemon API, whatever path the browser used on the portal.
	daemonPath = "/api/v1/rpc"
)

// Auditor records the calls that change something on a daemon. A nil Auditor records nothing.
type Auditor struct {
	Log *audit.Log
}

// frame is a JSON-RPC request or response, only what the portal needs to look at.
type frame struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (f frame) key() string { return strings.TrimSpace(string(f.ID)) }

// parseFrames reads a request or a batch of requests.
func parseFrames(data []byte) ([]frame, bool) {
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '[' {
		var frames []frame
		return frames, json.Unmarshal(data, &frames) == nil && len(frames) > 0
	}
	var f frame
	if json.Unmarshal(data, &f) != nil {
		return nil, false
	}
	return []frame{f}, true
}

// duplicateID reports an id used twice in a batch. The answers could not be told apart, and the audit entry of one of the
// calls would be lost or given the result of the other.
func duplicateID(frames []frame) (string, bool) {
	seen := map[string]bool{}
	for _, f := range frames {
		k := f.key()
		if k == "" || k == "null" {
			continue
		}
		if seen[k] {
			return k, true
		}
		seen[k] = true
	}
	return "", false
}

// check refuses requests with a method that is not part of the control API.
func check(frames []frame) (string, bool) {
	for _, f := range frames {
		if _, ok := known(f.Method); !ok {
			return f.Method, false
		}
	}
	return "", true
}

// pending are the audited calls that wait for the answer of the daemon, by the id of the request. A browser may reuse an id
// over a websocket, so the entries of one id are answered in the order they were sent.
type pending struct {
	mu    sync.Mutex
	calls map[string][]audit.Entry
}

func (p *pending) add(id string, e audit.Entry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls == nil {
		p.calls = map[string][]audit.Entry{}
	}
	p.calls[id] = append(p.calls[id], e)
}

func (p *pending) take(id string) (audit.Entry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	q := p.calls[id]
	if len(q) == 0 {
		return audit.Entry{}, false
	}
	if len(q) == 1 {
		delete(p.calls, id)
	} else {
		p.calls[id] = q[1:]
	}
	return q[0], true
}

func (p *pending) any() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls) > 0
}

func (p *pending) drain() []audit.Entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []audit.Entry
	for _, q := range p.calls {
		out = append(out, q...)
	}
	p.calls = nil
	return out
}

// plan notes the calls of the frames that change something. They are finished when the answer arrives. A call
// without an id gets no answer and is recorded right away, as unknown.
func (a *Auditor) plan(frames []frame, ip string, p *pending) {
	if a == nil || a.Log == nil {
		return
	}
	for _, f := range frames {
		name, _ := known(f.Method)
		s, ok := changes[name]
		if !ok {
			continue
		}
		target, detail := s.describe(f.Params)
		e := audit.Entry{IP: ip, Action: s.action, Target: target, Detail: detail}
		if f.key() == "" || f.key() == "null" {
			e.Result = audit.Unknown
			e.Detail = strings.TrimPrefix(e.Detail+", sent without an id, no answer", ", ")
			a.Log.Add(e)
			continue
		}
		p.add(f.key(), e)
	}
}

// answer finishes the call that belongs to a response.
func (a *Auditor) answer(f frame, p *pending) {
	if a == nil || a.Log == nil {
		return
	}
	e, ok := p.take(f.key())
	if !ok {
		return
	}
	if f.Error != nil {
		e.Result = audit.Failed
		e.Detail = strings.TrimPrefix(e.Detail+": "+audit.Redact(f.Error.Message), ": ")
	} else {
		e.Result = audit.OK
	}
	a.Log.Add(e)
}

// abandon records the calls that never got an answer.
func (a *Auditor) abandon(p *pending, why string) {
	if a == nil || a.Log == nil {
		return
	}
	for _, e := range p.drain() {
		e.Result = audit.Unknown
		e.Detail = strings.TrimPrefix(e.Detail+": "+why, ": ")
		a.Log.Add(e)
	}
}

// deny records a refused request.
func (a *Auditor) deny(ip, method, why string) {
	if a == nil || a.Log == nil {
		return
	}
	a.Log.Add(audit.Entry{IP: ip, Action: "call_refused", Target: audit.Redact(method), Result: audit.Denied, Detail: why})
}

// capture keeps the beginning of a response while it passes through.
type capture struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (c *capture) WriteHeader(code int) {
	c.status = code
	c.ResponseWriter.WriteHeader(code)
}

func (c *capture) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if room := maxBody - c.body.Len(); room > 0 {
		c.body.Write(b[:min(len(b), room)])
	}
	return c.ResponseWriter.Write(b)
}

func (c *capture) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// finishHTTP matches the answer of the daemon with the calls that were sent.
func (a *Auditor) finishHTTP(c *capture, p *pending) {
	if a == nil || a.Log == nil {
		return
	}
	if c.status != http.StatusOK {
		a.abandon(p, fmt.Sprintf("the daemon answered %d", c.status))
		return
	}
	body := bytes.TrimSpace(c.body.Bytes())
	var answers []frame
	if len(body) > 0 && body[0] == '[' {
		_ = json.Unmarshal(body, &answers)
	} else {
		var f frame
		if json.Unmarshal(body, &f) == nil {
			answers = []frame{f}
		}
	}
	for _, f := range answers {
		a.answer(f, p)
	}
	a.abandon(p, "no answer")
}

// Proxy forwards the API calls and the websocket of the browser to the control daemon. The portal holds the
// credentials for the daemon, the browser never sees them.
//
// Only calls of the control API are forwarded. Browsers send an Origin on websockets and the daemon only accepts its
// own, so the proxy enforces the same-origin rule for the browser side and presents the daemon its own origin.
// tlsConfig verifies the daemon (CA or pinned fingerprint) and may carry a client certificate.
func Proxy(target *url.URL, tlsConfig *tls.Config, token string, access Access) http.Handler {
	return newProxy(context.Background(), target, tlsConfig, token, access, nil)
}

// newProxy is Proxy with a context that ends the open websockets when it is cancelled, and an auditor.
func newProxy(ctx context.Context, target *url.URL, tlsConfig *tls.Config, token string, access Access, aud *Auditor) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.URL.Path, r.Out.URL.RawPath = daemonPath, ""
			r.Out.Host = target.Host
			// Whatever the browser sent is dropped, the portal authenticates itself.
			r.Out.Header.Del("Authorization")
			r.Out.Header.Del("Cookie")
			if token != "" {
				r.Out.Header.Set("Authorization", "Bearer "+token)
			}
			if r.In.Header.Get("Origin") != "" {
				r.Out.Header.Set("Origin", target.Scheme+"://"+target.Host)
			}
		},
		Transport: &http.Transport{
			Proxy:           http.ProxyFromEnvironment,
			TLSClientConfig: tlsConfig,
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			log.Warn().Err(err).Msg("the control daemon is not reachable")
			http.Error(w, "the control daemon is not reachable", http.StatusBadGateway)
		},
	}
	upgrader := websocket.Upgrader{Subprotocols: []string{subprotocol}, CheckOrigin: func(*http.Request) bool { return true }}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			http.Error(w, "cross-origin requests are not allowed", http.StatusForbidden)
			return
		}
		if !access(r) {
			http.Error(w, "sign in required", http.StatusUnauthorized)
			return
		}
		if websocket.IsWebSocketUpgrade(r) {
			relay(ctx, w, r, &upgrader, target, tlsConfig, token, access, aud)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		frames, ok := parseFrames(body)
		if !ok {
			aud.deny(api.ClientIP(r), "", "the request is not JSON-RPC")
			http.Error(w, "this call is not allowed", http.StatusForbidden)
			return
		}
		if method, ok := check(frames); !ok {
			log.Warn().Field("method", method).Msg("a call to a method that is not part of the API was refused")
			aud.deny(api.ClientIP(r), method, "not a method of the control API")
			http.Error(w, "this call is not allowed", http.StatusForbidden)
			return
		}
		if id, dup := duplicateID(frames); dup {
			aud.deny(api.ClientIP(r), "", "the id "+audit.Redact(id)+" is used twice in one batch")
			http.Error(w, "request ids in a batch must be unique", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var p pending
		aud.plan(frames, api.ClientIP(r), &p)
		c := &capture{ResponseWriter: w}
		proxy.ServeHTTP(c, r)
		aud.finishHTTP(c, &p)
	})
}

// sameOrigin allows requests without an Origin (not from a browser) and those from the page's own origin.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}

// relay terminates the websocket of the browser and opens one to the daemon, so every frame can be checked.
func relay(ctx context.Context, w http.ResponseWriter, r *http.Request, up *websocket.Upgrader, target *url.URL, tlsConfig *tls.Config, token string, access Access, aud *Auditor) {
	daemon := *target
	daemon.Scheme = map[string]string{"https": "wss"}[target.Scheme]
	if daemon.Scheme == "" {
		daemon.Scheme = "ws"
	}
	daemon.Path, daemon.RawPath, daemon.RawQuery = daemonPath, "", ""

	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	header.Set("Origin", target.Scheme+"://"+target.Host)
	dialer := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		TLSClientConfig:  tlsConfig,
		HandshakeTimeout: handshakeLimit,
		Subprotocols:     []string{subprotocol},
	}
	upstream, _, err := dialer.Dial(daemon.String(), header)
	if err != nil {
		log.Warn().Err(err).Msg("the control daemon is not reachable")
		http.Error(w, "the control daemon is not reachable", http.StatusBadGateway)
		return
	}
	defer upstream.Close()
	browser, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer browser.Close()
	browser.SetReadLimit(maxFrame)
	upstream.SetReadLimit(maxFrame * 8)
	ip := api.ClientIP(r)

	var wmu sync.Mutex // gorilla allows one writer per connection
	reply := func(kind int, data []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		return browser.WriteMessage(kind, data)
	}
	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done); _ = browser.Close(); _ = upstream.Close() }) }

	var calls pending
	go func() { // daemon -> browser
		defer stop()
		for {
			kind, data, err := upstream.ReadMessage()
			if err != nil {
				return
			}
			// Only the answers to audited calls are looked at, the stream of events is passed on untouched.
			if kind == websocket.TextMessage && calls.any() {
				var f frame
				if json.Unmarshal(data, &f) == nil && f.Method == "" && f.key() != "" {
					aud.answer(f, &calls)
				}
			}
			if reply(kind, data) != nil {
				return
			}
		}
	}()
	go func() { // the session is rechecked while a stream is quiet
		t := time.NewTicker(recheckEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				stop()
				return
			case <-t.C:
				if !access(r) {
					stop()
					return
				}
			}
		}
	}()

	defer stop()
	defer func() { aud.abandon(&calls, "the connection ended before the daemon answered") }()
	for { // browser -> daemon
		kind, data, err := browser.ReadMessage()
		if err != nil {
			return
		}
		if !access(r) || kind != websocket.TextMessage {
			return
		}
		frames, ok := parseFrames(data)
		if !ok {
			return
		}
		if method, ok := check(frames); !ok {
			log.Warn().Field("method", method).Msg("a call to a method that is not part of the API was refused")
			aud.deny(ip, method, "not a method of the control API")
			if len(frames) == 1 && frames[0].key() != "" {
				msg, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"id":      frames[0].ID,
					"error":   map[string]any{"code": codeForbidden, "message": "this call is not allowed"},
				})
				if reply(websocket.TextMessage, msg) != nil {
					return
				}
			}
			continue
		}
		if id, dup := duplicateID(frames); dup {
			aud.deny(ip, "", "the id "+audit.Redact(id)+" is used twice in one batch")
			msg, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      nil,
				"error":   map[string]any{"code": codeInvalidRequest, "message": "request ids in a batch must be unique"},
			})
			if reply(websocket.TextMessage, msg) != nil {
				return
			}
			continue
		}
		aud.plan(frames, ip, &calls)
		if err := upstream.WriteMessage(kind, data); err != nil {
			return
		}
	}
}
