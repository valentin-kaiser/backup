package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const secret = "correct horse battery"

func file(t *testing.T, content string) (string, *Password) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "password")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path, New(func() string { return path })
}

func TestCheckComparesWithTheFile(t *testing.T) {
	_, p := file(t, secret+"\r\n")
	if ok, err := p.Check(secret); err != nil || !ok {
		t.Fatalf("right password: %v %v", ok, err)
	}
	for _, wrong := range []string{"", "wrong wrong wrong", secret + "x", secret + "\n"} {
		if ok, err := p.Check(wrong); err != nil || ok {
			t.Errorf("%q: %v %v", wrong, ok, err)
		}
	}
	if v, err := p.Value(); err != nil || v != secret {
		t.Errorf("value = %q, %v", v, err)
	}
}

func TestWithoutAUsableFileNobodyGetsIn(t *testing.T) {
	for name, content := range map[string]string{"missing": "", "short": "short", "blank": "   \n"} {
		_, p := file(t, content)
		if ok, err := p.Check(content); ok || err == nil {
			t.Errorf("%s: %v %v", name, ok, err)
		}
		if _, err := p.Value(); err == nil {
			t.Errorf("%s: value must fail", name)
		}
	}
	if _, err := New(func() string { return "" }).Value(); !errors.Is(err, ErrNoPassword) {
		t.Errorf("no file configured: %v", err)
	}
}

func TestAReplacedFileAppliesAtOnce(t *testing.T) {
	path, p := file(t, secret)
	if ok, _ := p.Check(secret); !ok {
		t.Fatal("first password")
	}
	before := p.Version()
	if err := os.WriteFile(path, []byte("another long password"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	_ = os.Chtimes(path, future, future)
	if ok, _ := p.Check(secret); ok {
		t.Error("the old password must stop working")
	}
	if ok, _ := p.Check("another long password"); !ok {
		t.Error("the new password must work")
	}
	if p.Version() == before {
		t.Error("the version must change")
	}
}

func TestLimiterLocksAfterTooManyFailures(t *testing.T) {
	l := NewLimiter()
	now := time.Now()
	l.now = func() time.Time { return now }
	for i := 0; i < MaxFailures-1; i++ {
		l.Fail("1.1.1.1")
	}
	if l.Locked("1.1.1.1") {
		t.Fatal("locked too early")
	}
	l.Fail("1.1.1.1")
	if !l.Locked("1.1.1.1") || l.Locked("2.2.2.2") {
		t.Fatal("only the failing address is locked")
	}
	now = now.Add(LockDuration + time.Second)
	if l.Locked("1.1.1.1") {
		t.Error("the lock must end")
	}
	l.Fail("3.3.3.3")
	l.Clear("3.3.3.3")
	for i := 0; i < MaxFailures-1; i++ {
		l.Fail("3.3.3.3")
	}
	if l.Locked("3.3.3.3") {
		t.Error("a success resets the count")
	}
}

func TestMiddleware(t *testing.T) {
	_, p := file(t, secret)
	l := NewLimiter()
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), p, l)
	call := func(authorization string) int {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.RemoteAddr = "10.0.0.1:1234"
		if authorization != "" {
			r.Header.Set("Authorization", authorization)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if code := call(""); code != http.StatusUnauthorized {
		t.Errorf("no credentials: %d", code)
	}
	if code := call("Bearer " + secret); code != http.StatusNoContent {
		t.Errorf("right password: %d", code)
	}
	if code := call("Basic " + secret); code != http.StatusUnauthorized {
		t.Errorf("another scheme: %d", code)
	}
	for i := 0; i < MaxFailures; i++ {
		call("Bearer wrong wrong wrong")
	}
	if code := call("Bearer " + secret); code != http.StatusTooManyRequests {
		t.Errorf("locked address: %d", code)
	}
}

func TestMiddlewareRefusesEveryoneWithoutAPassword(t *testing.T) {
	_, p := file(t, "")
	h := Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), p, NewLimiter())
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d", w.Code)
	}
}

func TestEnsureCreatesOnlyAMissingPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "portal_password")
	created, err := Ensure(path)
	if err != nil || !created {
		t.Fatalf("created = %v, err = %v", created, err)
	}
	first, err := Read(path)
	if err != nil {
		t.Fatalf("the generated password is not usable: %v", err)
	}
	if created, err := Ensure(path); err != nil || created {
		t.Fatalf("an existing password must be kept, created = %v, err = %v", created, err)
	}
	if again, _ := Read(path); again != first {
		t.Error("the password changed")
	}
	// An empty file counts as not set, a short password is the admin's to fix.
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if created, err := Ensure(path); err != nil || !created {
		t.Fatalf("empty: created = %v, err = %v", created, err)
	}
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if created, _ := Ensure(path); created {
		t.Error("a short password must not be replaced")
	}
}
