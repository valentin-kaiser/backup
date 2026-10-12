package login

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/auth"
)

const password = "correct horse battery"

// open creates a store whose password file holds the password, or no file when it is empty.
func open(t *testing.T, content string) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "password")
	if content != "" {
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(dir, auth.New(func() string { return file }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, file
}

func TestWithoutAPasswordNobodyCanSignIn(t *testing.T) {
	s, _ := open(t, "")
	if _, err := s.Login(password, "1.1.1.1"); err == nil || err.Error() != ErrNoPassword.Error() {
		t.Fatalf("err = %v", err)
	}
}

func TestSignInWithThePassword(t *testing.T) {
	s, _ := open(t, password+"\n")
	if _, err := s.Login("wrong wrong wrong", "1.1.1.1"); err == nil || err.Error() != ErrInvalidLogin.Error() {
		t.Fatalf("wrong password: %v", err)
	}
	token, err := s.Login(password, "1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Valid(token) {
		t.Error("the session must be valid")
	}
	s.Logout(token)
	if s.Valid(token) {
		t.Error("the session must end with the sign-out")
	}
	if s.Valid("") || s.Valid("not a token") {
		t.Error("strangers are not signed in")
	}
}

func TestReplacingThePasswordEndsEverySession(t *testing.T) {
	s, file := open(t, password)
	token, err := s.Login(password, "1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("another long password"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	_ = os.Chtimes(file, future, future)
	if s.Valid(token) {
		t.Error("a session of the old password must end")
	}
	if _, err := s.Login(password, "1.1.1.1"); err == nil {
		t.Error("the old password must stop working")
	}
	if _, err := s.Login("another long password", "1.1.1.1"); err != nil {
		t.Errorf("the new password: %v", err)
	}
}

func TestLockoutIsPerAddress(t *testing.T) {
	s, _ := open(t, password)
	for i := 0; i < auth.MaxFailures; i++ {
		_, _ = s.Login("wrong wrong wrong", "1.1.1.1")
	}
	if _, err := s.Login(password, "1.1.1.1"); err == nil || err.Error() != ErrLocked.Error() {
		t.Fatalf("locked address: %v", err)
	}
	if _, err := s.Login(password, "2.2.2.2"); err != nil {
		t.Errorf("another address: %v", err)
	}
}

func TestSessionExpiry(t *testing.T) {
	s, _ := open(t, password)
	now := time.Now()
	s.now = func() time.Time { return now }
	token, _ := s.Login(password, "1.1.1.1")

	s.now = func() time.Time { return now.Add(SessionIdle - time.Minute) }
	if !s.Valid(token) {
		t.Fatal("still active")
	}
	s.now = func() time.Time { return now.Add(2*SessionIdle - 2*time.Minute) }
	if !s.Valid(token) {
		t.Fatal("activity extends the session")
	}
	s.now = func() time.Time { return now.Add(3 * SessionIdle) }
	if s.Valid(token) {
		t.Error("idle for too long")
	}

	s.now = func() time.Time { return now }
	token, _ = s.Login(password, "1.1.1.1")
	for at := time.Hour; at < SessionLifetime; at += time.Hour {
		s.now = func() time.Time { return now.Add(at) }
		if !s.Valid(token) {
			t.Fatalf("expired early at %v", at)
		}
	}
	s.now = func() time.Time { return now.Add(SessionLifetime + time.Second) }
	if s.Valid(token) {
		t.Error("lifetime over")
	}
}

func TestSessionsSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "password")
	_ = os.WriteFile(file, []byte(password), 0o600)
	pw := func() *auth.Password { return auth.New(func() string { return file }) }
	s, _ := Open(dir, pw())
	token, _ := s.Login(password, "1.1.1.1")
	_ = s.Close()
	s, err := Open(dir, pw())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.Valid(token) {
		t.Error("the session must survive a restart")
	}
}

func TestEndAll(t *testing.T) {
	s, _ := open(t, password)
	token, _ := s.Login(password, "1.1.1.1")
	s.EndAll()
	if s.Valid(token) {
		t.Error("every session must end")
	}
}
