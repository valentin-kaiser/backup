package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestTheDaemonSeesThePortalTokenNotTheBrowser(t *testing.T) {
	p := newPortal(t)
	var got string
	d := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization") + " " + r.URL.Path
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer d.Close()
	p.login(t)
	p.settings.URL = d.URL
	if code, _ := p.do(t, "POST", apiPath, call, "Authorization", "Bearer browser"); code != 200 {
		t.Fatalf("call: %d", code)
	}
	if got != "Bearer correct horse battery /api/v1/rpc" {
		t.Errorf("daemon saw %q", got)
	}
}

func TestOnlyTheDaemonRouteIsServed(t *testing.T) {
	p := newPortal(t)
	if code, _ := p.do(t, "POST", apiPath, call); code != 401 {
		t.Errorf("a stranger: %d", code)
	}
	p.login(t)
	for _, path := range []string{daemonRoute + "/more", "/api/v1/controls/main/rpc"} {
		if code, _ := p.do(t, "POST", path, call); code != 404 {
			t.Errorf("%s: %d", path, code)
		}
	}
}

func TestUnusableSettingsAreReportedAndFixedWithoutARestart(t *testing.T) {
	p := newPortal(t)
	p.login(t)
	good := p.settings

	p.passwordFile = ""
	p.settings = good
	p.settings.CAFile = "/does/not/exist"
	if code, _ := p.do(t, "POST", apiPath, call); code != http.StatusBadGateway {
		t.Errorf("a missing CA file: %d", code)
	}
	p.settings = good
	if code, _ := p.do(t, "POST", apiPath, call); code != 200 {
		t.Errorf("fixed settings: %d", code)
	}
}

func TestAReplacedPasswordIsPickedUp(t *testing.T) {
	p := newPortal(t)
	p.login(t)
	if code, _ := p.do(t, "POST", apiPath, call); code != 200 {
		t.Fatalf("first call: %d", code)
	}
	if err := os.WriteFile(p.passwordFile, []byte("new long password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	_ = os.Chtimes(p.passwordFile, future, future)
	// The old session ended with the old password, the daemon sees the new one after signing in again.
	if code, _ := p.call(t, "Auth.Login", `{"password":"new long password"}`); code != 200 {
		t.Fatalf("sign in with the new password: %d", code)
	}
	if code, _ := p.do(t, "POST", apiPath, call); code != 200 {
		t.Fatalf("second call: %d", code)
	}
	seen := p.saw()
	if len(seen) != 2 {
		t.Fatalf("daemon saw %v", seen)
	}
	if got := p.lastToken(); got != "Bearer new long password" {
		t.Errorf("the daemon saw %q", got)
	}
}

func TestChangingTheSettingsEndsOpenWebsockets(t *testing.T) {
	p := newPortal(t)
	p.login(t)
	u, _ := url.Parse(p.srv.URL)
	header := http.Header{"Origin": {p.srv.URL}}
	for _, c := range p.client.Jar.Cookies(u) {
		header.Add("Cookie", c.String())
	}
	conn, _, err := (&websocket.Dialer{Subprotocols: []string{"xrpc.json"}}).Dial("ws"+strings.TrimPrefix(p.srv.URL, "http")+apiPath, header)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.WriteMessage(websocket.TextMessage, []byte(call))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	p.settings.Fingerprint = strings.Repeat("ab", 32)
	// The next call notices the change and drops the old proxy and with it the websocket.
	_, _ = p.do(t, "POST", apiPath, call)
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("the websocket must end when the settings change")
	}
}
