package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/auth"
	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/audit"
	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/config"
	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/login"
	api "github.com/Valentin-Kaiser/backup/worker/portal/pkg/web/api/v1"
	"github.com/gorilla/websocket"
)

const password = "correct horse battery"

// apiPath is the daemon API as the browser sees it.
const apiPath = daemonRoute

// portal serves the handlers of the portal in front of a daemon that records what reaches it.
type portal struct {
	srv    *httptest.Server
	store  *login.Store
	audit  *audit.Log
	client *http.Client
	daemon *Daemon
	// settings are the settings of the daemon, change them to point the portal elsewhere.
	settings config.Daemon
	// passwordFile holds the password of the portal and the daemon.
	passwordFile string

	mu   sync.Mutex
	seen []string
	// tokens are the Authorization headers of the calls over HTTP.
	tokens []string
	// errors makes the daemon answer a method with an error, by the name of the method.
	errors map[string]string
	// silent makes the daemon not answer a method at all.
	silent map[string]bool
}

func (p *portal) lastToken() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.tokens) == 0 {
		return ""
	}
	return p.tokens[len(p.tokens)-1]
}

func (p *portal) saw() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

// answer is what the daemon says to a request.
func (p *portal) answer(f frame) []byte {
	name, _ := known(f.Method)
	p.mu.Lock()
	msg, bad := p.errors[name]
	silent := p.silent[name]
	p.mu.Unlock()
	if silent {
		return nil
	}
	resp := map[string]any{"jsonrpc": "2.0", "id": f.ID}
	if bad {
		resp["error"] = map[string]any{"code": -32000, "message": msg}
	} else {
		resp["result"] = map[string]any{}
	}
	b, _ := json.Marshal(resp)
	return b
}

var dbCounter int

func newPortal(t *testing.T) *portal {
	t.Helper()
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte(password+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pw := auth.New(func() string { return passwordFile })
	store, err := login.Open(t.TempDir(), pw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	p := &portal{store: store, errors: map[string]string{}, silent: map[string]bool{}}

	rec := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			up := websocket.Upgrader{Subprotocols: []string{"xrpc.json"}}
			conn, err := up.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			for {
				_, msg, err := conn.ReadMessage()
				if err != nil {
					return
				}
				p.mu.Lock()
				p.seen = append(p.seen, string(msg))
				p.mu.Unlock()
				var f frame
				_ = json.Unmarshal(msg, &f)
				if out := p.answer(f); out != nil {
					_ = conn.WriteMessage(websocket.TextMessage, out)
				}
			}
		}
		b, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.tokens = append(p.tokens, r.Header.Get("Authorization"))
		p.seen = append(p.seen, string(b))
		p.mu.Unlock()
		frames, _ := parseFrames(b)
		var out [][]byte
		for _, f := range frames {
			if a := p.answer(f); a != nil {
				out = append(out, a)
			}
		}
		if len(out) == 1 && !strings.HasPrefix(strings.TrimSpace(string(b)), "[") {
			_, _ = w.Write(out[0])
			return
		}
		_, _ = w.Write(append(append([]byte("["), bytes.Join(out, []byte(","))...), ']'))
	}))
	t.Cleanup(rec.Close)

	p.passwordFile = passwordFile
	p.settings = config.Daemon{URL: rec.URL}
	if p.audit, err = audit.New(store.DB(), 0); err != nil {
		t.Fatal(err)
	}

	a := api.NewAuth(store, false, p.audit)
	mux := http.NewServeMux()
	p.daemon = NewDaemon(func() config.Daemon { return p.settings }, pw, a.Access, p.audit)
	mux.Handle(api.Endpoint, api.Handler(a, api.NewAuditLog(a, p.audit)))
	mux.Handle(daemonRoute, p.daemon)
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	jar, _ := cookiejar.New(nil)
	p.client = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return p
}

// do sends a request with the cookie jar. Headers come in pairs, an empty value removes the header.
func (p *portal) do(t *testing.T, method, path, body string, headers ...string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, p.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Origin", p.srv.URL)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	resp, err := p.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func loginCall(password string) string {
	return `{"jsonrpc":"2.0","id":1,"method":"Auth.Login","params":{"password":"` + password + `"}}`
}

// call sends an rpc to the services of the portal. The parameters use field names.
func (p *portal) call(t *testing.T, method, params string, headers ...string) (int, string) {
	t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"%s","params":%s}`, method, params)
	return p.do(t, "POST", api.Endpoint, body, append([]string{api.FieldNamesHeader, "true"}, headers...)...)
}

func (p *portal) login(t *testing.T) {
	t.Helper()
	if code, body := p.call(t, "Auth.Login", `{"password":"`+password+`"}`); code != 200 {
		t.Fatalf("login: %d %s", code, body)
	}
}

// dial opens the websocket of the main control with the session of the cookie jar.
func (p *portal) dial(t *testing.T) *websocket.Conn {
	t.Helper()
	u, _ := url.Parse(p.srv.URL)
	header := http.Header{"Origin": {p.srv.URL}}
	for _, c := range p.client.Jar.Cookies(u) {
		header.Add("Cookie", c.String())
	}
	conn, _, err := (&websocket.Dialer{Subprotocols: []string{"xrpc.json"}}).Dial("ws"+strings.TrimPrefix(p.srv.URL, "http")+apiPath, header)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	return conn
}

func (p *portal) entries(t *testing.T) []audit.Entry {
	t.Helper()
	got, err := p.audit.Query(audit.Query{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// entry returns the newest entry of an action.
func (p *portal) entry(t *testing.T, action string) audit.Entry {
	t.Helper()
	for _, e := range p.entries(t) {
		if e.Action == action {
			return e
		}
	}
	t.Fatalf("no %q entry in %+v", action, p.entries(t))
	return audit.Entry{}
}

func rpc(id int, method, params string) string {
	if params == "" {
		params = "{}"
	}
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"Control.%s","params":%s}`, id, method, params)
}

func TestAPIRequiresASession(t *testing.T) {
	p := newPortal(t)
	if code, _ := p.do(t, "POST", apiPath, call); code != http.StatusUnauthorized {
		t.Errorf("rpc without session: %d", code)
	}
	for _, method := range []string{"Auth.GetSession", "AuditLog.ListAuditLog", "AuditLog.ExportAuditLog"} {
		if code, _ := p.call(t, method, `{}`); code != http.StatusUnauthorized {
			t.Errorf("%s without session: %d", method, code)
		}
	}
	dialer := websocket.Dialer{Subprotocols: []string{"xrpc.json"}}
	_, resp, err := dialer.Dial("ws"+strings.TrimPrefix(p.srv.URL, "http")+apiPath, http.Header{"Origin": {p.srv.URL}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("websocket without session: %v %v", err, resp)
	}
	if len(p.saw()) != 0 {
		t.Error("nothing may reach the daemon")
	}
}

func TestLoginSetsAHardenedCookie(t *testing.T) {
	p := newPortal(t)
	req, _ := http.NewRequest("POST", p.srv.URL+api.Endpoint, strings.NewReader(loginCall(password)))
	req.Header.Set("Origin", p.srv.URL)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(api.FieldNamesHeader, "true")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var c *http.Cookie
	for _, k := range resp.Cookies() {
		if k.Name == api.CookieName {
			c = k
		}
	}
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Value == "" {
		t.Fatalf("cookie = %+v", c)
	}
}

func TestWrongPasswordAndLockout(t *testing.T) {
	p := newPortal(t)
	for i := 0; i < auth.MaxFailures; i++ {
		if code, _ := p.call(t, "Auth.Login", `{"password":"wrong wrong wrong"}`); code != 401 {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	if code, _ := p.call(t, "Auth.Login", `{"password":"`+password+`"}`); code != http.StatusTooManyRequests {
		t.Errorf("locked address: %d", code)
	}
	if code, _ := p.call(t, "Auth.GetSession", `{}`); code != 401 {
		t.Errorf("a locked address must not get a session: %d", code)
	}
}

func TestAPortalWithoutAPasswordRefusesEveryone(t *testing.T) {
	p := newPortal(t)
	_ = os.Remove(p.passwordFile)
	if code, body := p.call(t, "Auth.Login", `{"password":"`+password+`"}`); code != http.StatusServiceUnavailable || !strings.Contains(body, "password-file") {
		t.Errorf("login: %d %s", code, body)
	}
}

func TestCallsNeedSameOrigin(t *testing.T) {
	p := newPortal(t)
	if code, _ := p.call(t, "Auth.Login", `{"password":"`+password+`"}`, "Origin", "http://evil.example"); code != 403 {
		t.Errorf("cross-origin login: %d", code)
	}
	if code, _ := p.call(t, "Auth.Login", `{"password":"`+password+`"}`, "Origin", ""); code != 403 {
		t.Errorf("login without origin: %d", code)
	}
	if code, _ := p.call(t, "Auth.Reboot", `{}`); code != 404 {
		t.Errorf("unknown method: %d", code)
	}
}

func TestSessionEndsWithLogout(t *testing.T) {
	p := newPortal(t)
	p.login(t)
	if code, _ := p.call(t, "Auth.GetSession", `{}`); code != 200 {
		t.Fatalf("session: %d", code)
	}
	if code, _ := p.call(t, "Auth.Logout", `{}`); code != 200 {
		t.Fatalf("logout: %d", code)
	}
	if code, _ := p.call(t, "Auth.GetSession", `{}`); code != 401 {
		t.Errorf("after logout: %d", code)
	}
}

func TestReplacingThePasswordEndsTheSession(t *testing.T) {
	p := newPortal(t)
	p.login(t)
	if err := os.WriteFile(p.passwordFile, []byte("another long password"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	_ = os.Chtimes(p.passwordFile, future, future)
	if code, _ := p.call(t, "Auth.GetSession", `{}`); code != 401 {
		t.Errorf("sessions must end with the old password: %d", code)
	}
	if code, _ := p.call(t, "Auth.Login", `{"password":"`+password+`"}`); code != 401 {
		t.Errorf("the old password: %d", code)
	}
	if code, _ := p.call(t, "Auth.Login", `{"password":"another long password"}`); code != 200 {
		t.Errorf("the new password: %d", code)
	}
}

func TestOnlyCallsOfTheControlAPIAreForwarded(t *testing.T) {
	p := newPortal(t)
	p.login(t)
	for name, body := range map[string]string{
		"odd spelling":  `{"jsonrpc":"2.0","id":1,"method":"control.getstatus"}`,
		"other service": `{"jsonrpc":"2.0","id":1,"method":"Other.GetStatus"}`,
		"unknown":       rpc(1, "Reboot", ""),
		"no method":     `{"id":1}`,
		"batch":         `[` + call + `,` + rpc(2, "Reboot", "") + `]`,
		"garbage":       `not json`,
		"empty batch":   `[]`,
	} {
		if code, _ := p.do(t, "POST", apiPath, body); code != 403 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if len(p.saw()) != 0 {
		t.Fatalf("a refused call reached the daemon: %v", p.saw())
	}
	for _, method := range []string{"GetStatus", "StartJob", "SetSecret", "ListSecrets", "DeleteSecret", "Restore"} {
		if code, _ := p.do(t, "POST", apiPath, rpc(1, method, "")); code != 200 {
			t.Errorf("%s must be forwarded: %d", method, code)
		}
	}
}

func TestEveryMethodOfTheAPIIsKnown(t *testing.T) {
	for _, m := range []string{
		"GetStatus", "ListRuns", "GetRun", "ListSnapshots", "ListFiles", "GetStats", "Watch", "ListSecrets",
		"StartJob", "CancelJob", "PauseJob", "ResumeJob", "Restore", "CreateJob", "UpdateJob", "DeleteJob",
		"SetSecret", "ChangeRepositoryPassword", "DeleteSecret",
	} {
		for _, prefix := range prefixes {
			if _, ok := known(prefix + m); !ok {
				t.Errorf("%s%s must be known", prefix, m)
			}
		}
	}
	if _, ok := known("xrpc.cancel"); !ok {
		t.Error("xrpc.cancel ends a stream and has to pass")
	}
	if _, ok := known("xrpc.message"); ok {
		t.Error("xrpc.message is for the server only")
	}
}

func TestWebsocketRefusesUnknownMethodsAndAnswersTheCaller(t *testing.T) {
	p := newPortal(t)
	p.login(t)
	conn := p.dial(t)
	_ = conn.WriteMessage(websocket.TextMessage, []byte(rpc(7, "Reboot", "")))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		ID    int `json:"id"`
		Error struct{ Code int }
	}
	if err := json.Unmarshal(msg, &out); err != nil || out.ID != 7 || out.Error.Code != codeForbidden {
		t.Fatalf("a refused frame must be answered with an error, got %s", msg)
	}
	if len(p.saw()) != 0 {
		t.Fatal("the refused frame reached the daemon")
	}
	_ = conn.WriteMessage(websocket.TextMessage, []byte(call))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	if got := p.saw(); len(got) != 1 || got[0] != call {
		t.Errorf("the daemon saw %q", got)
	}
}

func TestAnEndedSessionCutsTheWebsocket(t *testing.T) {
	p := newPortal(t)
	p.login(t)
	conn := p.dial(t)
	_ = conn.WriteMessage(websocket.TextMessage, []byte(call))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	p.store.EndAll()
	_ = conn.WriteMessage(websocket.TextMessage, []byte(call))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("the connection must end with the session")
	}
}

// ── audit ───────────────────────────────────────────────────────────────────

func TestSignInsAreAudited(t *testing.T) {
	p := newPortal(t)
	_, _ = p.call(t, "Auth.Login", `{"password":"wrong wrong wrong"}`)
	p.login(t)
	_, _ = p.call(t, "Auth.Logout", `{}`)

	got := map[string]audit.Entry{}
	for _, e := range p.entries(t) {
		if _, seen := got[e.Action]; !seen {
			got[e.Action] = e
		}
	}
	if e := got[audit.LoginFailed]; e.Result != audit.Denied || e.IP == "" {
		t.Errorf("failed login: %+v", e)
	}
	if e := got[audit.LoginOK]; e.Result != audit.OK {
		t.Errorf("login: %+v", e)
	}
	if e := got[audit.Logout]; e.Result != audit.OK {
		t.Errorf("logout: %+v", e)
	}
	for _, e := range p.entries(t) {
		if strings.Contains(e.Detail+e.Target, "wrong wrong") || strings.Contains(e.Detail+e.Target, password) {
			t.Errorf("a password in the log: %+v", e)
		}
	}
}

func TestCallsThatChangeThingsAreAuditedWithTheirOutcome(t *testing.T) {
	p := newPortal(t)
	p.login(t)
	p.errors["CancelJob"] = "job is not running"

	_, _ = p.do(t, "POST", apiPath, rpc(1, "StartJob", `{"1":"photos"}`))
	_, _ = p.do(t, "POST", apiPath, rpc(2, "CancelJob", `{"1":"photos"}`))
	_, _ = p.do(t, "POST", apiPath, rpc(3, "Restore", `{"1":"abc123","2":"/restore/x","3":["/a","/b"],"4":"photos"}`))
	_, _ = p.do(t, "POST", apiPath, rpc(4, "PauseJob", `{"1":"photos"}`))

	if e := p.entry(t, "start_job"); e.Result != audit.OK || e.Target != "photos" || e.IP == "" {
		t.Errorf("start: %+v", e)
	}
	if e := p.entry(t, "cancel_job"); e.Result != audit.Failed || !strings.Contains(e.Detail, "not running") {
		t.Errorf("cancel: %+v", e)
	}
	if e := p.entry(t, "restore"); e.Target != "photos" || !strings.Contains(e.Detail, "snapshot=abc123") ||
		!strings.Contains(e.Detail, "to=/restore/x") || !strings.Contains(e.Detail, "paths=2") {
		t.Errorf("restore: %+v", e)
	}
	if e := p.entry(t, "pause_job"); e.Target != "photos" {
		t.Errorf("pause: %+v", e)
	}
}

func TestReadsAreNotAudited(t *testing.T) {
	p := newPortal(t)
	p.login(t)
	for _, m := range []string{"GetStatus", "ListRuns", "ListSnapshots", "ListFiles", "ListSecrets", "GetStats"} {
		_, _ = p.do(t, "POST", apiPath, rpc(1, m, ""))
	}
	for _, e := range p.entries(t) {
		if e.Action != audit.LoginOK {
			t.Errorf("a read was recorded: %+v", e)
		}
	}
}

func TestSecretsNeverReachTheAuditLog(t *testing.T) {
	p := newPortal(t)
	p.login(t)
	_, _ = p.do(t, "POST", apiPath, rpc(1, "SetSecret", `{"1":"photos","2":"hunter2-the-value"}`))
	_, _ = p.do(t, "POST", apiPath, rpc(2, "SetSecret", `{"1":"fresh","3":true}`))
	_, _ = p.do(t, "POST", apiPath, rpc(3, "ChangeRepositoryPassword", `{"1":"photos","2":"another-secret-value"}`))
	// A job with credentials in the repository address and in the restic arguments.
	_, _ = p.do(t, "POST", apiPath, rpc(4, "CreateJob",
		`{"1":"offsite","2":"backup","4":"0 2 * * *","5":"-o s3.secret=ARGS-SECRET","9":"rest:https://backup:REPO-SECRET@host/repo","12":"photos"}`))

	var all strings.Builder
	for _, e := range p.entries(t) {
		fmt.Fprintf(&all, "%+v\n", e)
	}
	for _, secret := range []string{"hunter2-the-value", "another-secret-value", "ARGS-SECRET", "REPO-SECRET"} {
		if strings.Contains(all.String(), secret) {
			t.Errorf("%q is in the audit log:\n%s", secret, all.String())
		}
	}
	if e := p.entry(t, "set_secret"); e.Target == "" {
		t.Errorf("the name of the secret belongs in the log: %+v", e)
	}
	create := p.entry(t, "create_job")
	if create.Target != "offsite" || !strings.Contains(create.Detail, "rest:https://***@host/repo") || !strings.Contains(create.Detail, "password secret=photos") {
		t.Errorf("create_job: %+v", create)
	}
}

func TestABatchOfChangesIsAuditedCallByCall(t *testing.T) {
	p := newPortal(t)
	p.login(t)
	p.errors["PauseJob"] = "no such job"
	_, _ = p.do(t, "POST", apiPath, `[`+rpc(1, "StartJob", `{"1":"a"}`)+`,`+rpc(2, "PauseJob", `{"1":"b"}`)+`,`+rpc(3, "GetStatus", "")+`]`)
	if e := p.entry(t, "start_job"); e.Target != "a" || e.Result != audit.OK {
		t.Errorf("start: %+v", e)
	}
	if e := p.entry(t, "pause_job"); e.Target != "b" || e.Result != audit.Failed {
		t.Errorf("pause: %+v", e)
	}
}

func TestWebsocketCallsAreAuditedToo(t *testing.T) {
	p := newPortal(t)
	p.login(t)
	p.errors["DeleteJob"] = "job is running"
	conn := p.dial(t)
	_ = conn.WriteMessage(websocket.TextMessage, []byte(rpc(5, "StartJob", `{"1":"photos"}`)))
	_, _, _ = conn.ReadMessage()
	_ = conn.WriteMessage(websocket.TextMessage, []byte(rpc(6, "DeleteJob", `{"1":"photos"}`)))
	_, _, _ = conn.ReadMessage()
	if e := p.entry(t, "start_job"); e.Result != audit.OK || e.Target != "photos" {
		t.Errorf("start: %+v", e)
	}
	if e := p.entry(t, "delete_job"); e.Result != audit.Failed || !strings.Contains(e.Detail, "running") {
		t.Errorf("delete: %+v", e)
	}
}

func TestACallThatGetsNoAnswerIsRecordedAsUnknown(t *testing.T) {
	p := newPortal(t)
	p.login(t)
	p.silent["Restore"] = true
	conn := p.dial(t)
	_ = conn.WriteMessage(websocket.TextMessage, []byte(rpc(9, "Restore", `{"4":"photos","2":"/restore/x"}`)))
	// Wait until the daemon has it, then drop the connection.
	deadline := time.Now().Add(5 * time.Second)
	for len(p.saw()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	conn.Close()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range p.entries(t) {
			if e.Action == "restore" {
				if e.Result != audit.Unknown || !strings.Contains(e.Detail, "before the daemon answered") {
					t.Fatalf("restore: %+v", e)
				}
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the unanswered call was never recorded")
}

func TestRefusedCallsAreAudited(t *testing.T) {
	p := newPortal(t)
	p.login(t)
	_, _ = p.do(t, "POST", apiPath, rpc(1, "Reboot", ""))
	e := p.entry(t, "call_refused")
	if e.Result != audit.Denied || !strings.Contains(e.Target, "Reboot") {
		t.Errorf("refused: %+v", e)
	}
}

func TestAnUnreachableDaemonIsRecordedAsFailed(t *testing.T) {
	p := newPortal(t)
	p.login(t)
	p.settings.URL = "http://127.0.0.1:1"
	if code, _ := p.do(t, "POST", apiPath, rpc(1, "StartJob", `{"1":"photos"}`)); code != http.StatusBadGateway {
		t.Fatalf("status %d", code)
	}
	if e := p.entry(t, "start_job"); e.Result != audit.Unknown || !strings.Contains(e.Detail, "502") {
		t.Errorf("start: %+v", e)
	}
}

func TestReadingAndExportingTheAuditLog(t *testing.T) {
	p := newPortal(t)
	p.login(t)
	_, _ = p.do(t, "POST", apiPath, rpc(1, "StartJob", `{"1":"photos"}`))
	_, _ = p.do(t, "POST", apiPath, rpc(2, "StartJob", `{"1":"docs"}`))

	var list struct {
		Result struct {
			Entries []struct{ Target string }
			NextID  json.Number `json:"nextId"`
		}
	}
	code, body := p.call(t, "AuditLog.ListAuditLog", `{"filter":{"action":"start_job"},"limit":1}`)
	if code != 200 || json.Unmarshal([]byte(body), &list) != nil || len(list.Result.Entries) != 1 || list.Result.Entries[0].Target != "docs" || list.Result.NextID == "" {
		t.Fatalf("list = %d %s", code, body)
	}
	code, body = p.call(t, "AuditLog.ListAuditLog", fmt.Sprintf(`{"filter":{"action":"start_job"},"before_id":%s}`, list.Result.NextID))
	if code != 200 || !strings.Contains(body, "photos") || strings.Contains(body, "docs") {
		t.Errorf("page 2 = %d %s", code, body)
	}
	if code, body := p.call(t, "AuditLog.ListAuditLog", `{"filter":{"text":"photos"}}`); code != 200 || strings.Contains(body, "docs") {
		t.Errorf("search = %d %s", code, body)
	}
	for name, params := range map[string]string{"negative limit": `{"limit":-1}`, "negative before": `{"before_id":-1}`} {
		if code, _ := p.call(t, "AuditLog.ListAuditLog", params); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if code, _ := p.call(t, "AuditLog.ExportAuditLog", `{"format":"xml"}`); code != 400 {
		t.Errorf("unknown format: %d", code)
	}

	var export struct {
		Result struct {
			Filename    string
			ContentType string `json:"contentType"`
			Data        []byte
		}
	}
	code, body = p.call(t, "AuditLog.ExportAuditLog", `{"format":"csv"}`)
	if code != 200 || json.Unmarshal([]byte(body), &export) != nil {
		t.Fatalf("export = %d %s", code, body)
	}
	csv := string(export.Result.Data)
	if !strings.HasPrefix(csv, "id,time,ip,action") || !strings.Contains(csv, "start_job") || !strings.HasSuffix(export.Result.Filename, ".csv") {
		t.Fatalf("csv = %q %+v", csv, export.Result)
	}
	if e := p.entry(t, "audit_export"); e.Detail != "csv" {
		t.Errorf("an export must be recorded: %+v", e)
	}
}
