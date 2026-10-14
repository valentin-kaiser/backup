package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/audit"
	"github.com/gorilla/websocket"
)

// daemon answers like the control API: it only accepts its own origin and echoes what it saw.
func daemon(t *testing.T) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{Subprotocols: []string{"xrpc.json"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			conn, err := up.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			_ = conn.WriteMessage(websocket.TextMessage, []byte(string(msg)+"|"+r.Header.Get("Sec-WebSocket-Protocol")))
			return
		}
		_, _ = io.WriteString(w, r.Method+" "+r.URL.Path+" host="+r.Host)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func front(t *testing.T, target string) *httptest.Server {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Proxy(u, nil, "", admin))
	t.Cleanup(srv.Close)
	return srv
}

func TestProxyForwardsHTTP(t *testing.T) {
	d := daemon(t)
	f := front(t, d.URL)
	resp, err := http.Post(f.URL+"/api/v1/rpc", "application/json", strings.NewReader(call))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if want := "POST /api/v1/rpc host=" + strings.TrimPrefix(d.URL, "http://"); string(body) != want {
		t.Errorf("got %q, want %q", body, want)
	}
}

func TestProxyForwardsWebsocket(t *testing.T) {
	d := daemon(t)
	f := front(t, d.URL)

	dialer := websocket.Dialer{Subprotocols: []string{"xrpc.json"}}
	header := http.Header{"Origin": {f.URL}}
	conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(f.URL, "http")+"/api/v1/rpc", header)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if conn.Subprotocol() != "xrpc.json" {
		t.Errorf("negotiated %q", conn.Subprotocol())
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte(call)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if string(msg) != call+"|xrpc.json" {
		t.Errorf("the daemon saw %q", msg)
	}
}

func TestProxyRefusesCrossOriginWebsockets(t *testing.T) {
	d := daemon(t)
	f := front(t, d.URL)
	dialer := websocket.Dialer{Subprotocols: []string{"xrpc.json"}}
	_, resp, err := dialer.Dial("ws"+strings.TrimPrefix(f.URL, "http")+"/api/v1/rpc", http.Header{"Origin": {"http://evil.example"}})
	if err == nil {
		t.Fatal("a cross-origin websocket must be refused")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("response = %v", resp)
	}
}

func TestProxyReportsAnUnreachableDaemon(t *testing.T) {
	d := daemon(t)
	url := d.URL
	d.Close()
	f := front(t, url)
	resp, err := http.Post(f.URL+"/api/v1/rpc", "application/json", strings.NewReader(call))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status %d", resp.StatusCode)
	}
}

func TestProxyAuthenticatesWithItsOwnToken(t *testing.T) {
	var got string
	d := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
	}))
	defer d.Close()
	u, _ := url.Parse(d.URL)
	f := httptest.NewServer(Proxy(u, nil, "secret", admin))
	defer f.Close()

	req, _ := http.NewRequest(http.MethodPost, f.URL+"/api/v1/rpc", strings.NewReader(call))
	req.Header.Set("Authorization", "Bearer from-the-browser")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got != "Bearer secret" {
		t.Errorf("daemon saw %q, the browser credentials must be replaced by the portal token", got)
	}
}

const call = `{"jsonrpc":"2.0","id":1,"method":"Control.GetStatus"}`

// admin lets every request in, these tests are about forwarding.
func admin(*http.Request) bool { return true }

func TestDuplicateIDsInABatchAreFound(t *testing.T) {
	if _, dup := duplicateID([]frame{{ID: json.RawMessage(`1`)}, {ID: json.RawMessage(`2`)}, {}, {}}); dup {
		t.Fatal("distinct ids and notifications are fine")
	}
	if id, dup := duplicateID([]frame{{ID: json.RawMessage(`7`)}, {ID: json.RawMessage(` 7`)}}); !dup || id != "7" {
		t.Fatalf("got %q %v", id, dup)
	}
}

func TestPendingAnswersSameIDInOrder(t *testing.T) {
	var p pending
	p.add("1", audit.Entry{Action: "a"})
	p.add("1", audit.Entry{Action: "b"})
	for _, want := range []string{"a", "b"} {
		if e, ok := p.take("1"); !ok || e.Action != want {
			t.Fatalf("want %s, got %v %v", want, e.Action, ok)
		}
	}
	if p.any() {
		t.Fatal("nothing should be pending")
	}
}
