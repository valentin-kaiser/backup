package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Valentin-Kaiser/backup/pkg/secure"
)

func newKey(t *testing.T) secure.Key {
	t.Helper()
	k, err := secure.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSetGetListDelete(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, newKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if created, err := s.Set("photos", "hunter2 hunter2"); err != nil || !created {
		t.Fatalf("set = %v, %v", created, err)
	}
	if created, err := s.Set("photos", "another value"); err != nil || created {
		t.Fatalf("replace = %v, %v", created, err)
	}
	_, _ = s.Set("docs", "x")
	if v, err := s.Get("photos"); err != nil || v != "another value" {
		t.Fatalf("get = %q, %v", v, err)
	}
	list := s.List()
	if len(list) != 2 || list[0].Name != "docs" || list[1].Created.After(list[1].Updated) {
		t.Fatalf("list = %+v", list)
	}
	if err := s.Delete("docs"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("docs"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
	if err := s.Delete("docs"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete again: %v", err)
	}
}

func TestOnDiskTheValuesAreEncrypted(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, newKey(t))
	_, _ = s.Set("photos", "super-secret-password")
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "super-secret-password") {
		t.Fatal("the secret file holds the plaintext")
	}
	if fi, _ := os.Stat(filepath.Join(dir, FileName)); fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("the file must be private: %v", fi.Mode())
	}
}

func TestSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	key := newKey(t)
	s, _ := Open(dir, key)
	_, _ = s.Set("photos", "value")
	again, err := Open(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := again.Get("photos"); v != "value" {
		t.Fatalf("got %q", v)
	}
}

func TestAWrongKeyIsReportedAtStart(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, newKey(t))
	_, _ = s.Set("photos", "value")
	if _, err := Open(dir, newKey(t)); err == nil || !strings.Contains(err.Error(), "photos") {
		t.Fatalf("err = %v", err)
	}
}

func TestAValueCannotBeMovedToAnotherName(t *testing.T) {
	dir := t.TempDir()
	key := newKey(t)
	s, _ := Open(dir, key)
	_, _ = s.Set("photos", "value")
	s.mu.Lock()
	s.items["docs"] = s.items["photos"]
	s.mu.Unlock()
	if _, err := s.Get("docs"); err == nil {
		t.Fatal("a sealed value bound to photos must not open as docs")
	}
}

func TestValidation(t *testing.T) {
	s, _ := Open(t.TempDir(), newKey(t))
	for name, fn := range map[string]func() error{
		"bad name":   func() error { _, err := s.Set("Bad Name", "x"); return err },
		"empty name": func() error { _, err := s.Set("", "x"); return err },
		"empty":      func() error { _, err := s.Set("a", ""); return err },
		"newline":    func() error { _, err := s.Set("a", "abc\ndef"); return err },
		"nul":        func() error { _, err := s.Set("a", "abc\x00"); return err },
		"too long":   func() error { _, err := s.Set("a", strings.Repeat("x", MaxLength+1)); return err },
	} {
		if fn() == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if len(s.List()) != 0 {
		t.Error("nothing may have been stored")
	}
}

func TestRekey(t *testing.T) {
	dir := t.TempDir()
	oldKey, newK := newKey(t), newKey(t)
	s, _ := Open(dir, oldKey)
	_, _ = s.Set("photos", "value")
	if err := Rekey(dir, oldKey, newK); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, oldKey); err == nil {
		t.Error("the old key must no longer open the file")
	}
	again, err := Open(dir, newK)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := again.Get("photos"); v != "value" {
		t.Errorf("got %q", v)
	}
}
