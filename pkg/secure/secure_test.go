package secure

import (
	"bytes"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testKey(t *testing.T, id string) Key {
	t.Helper()
	k, err := NewKey(id, bytes.Repeat([]byte{byte(len(id))}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	k := testKey(t, "k1")
	sealed, err := k.Seal([]byte("hunter2"), "secret:1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "hunter2") {
		t.Fatal("plaintext leaked into the sealed value")
	}
	got, err := k.Open(sealed, "secret:1")
	if err != nil || string(got) != "hunter2" {
		t.Fatalf("open = %q, %v", got, err)
	}
}

func TestOpenRejectsWrongContextKeyAndTampering(t *testing.T) {
	k := testKey(t, "k1")
	sealed, _ := k.Seal([]byte("x"), "a")
	if _, err := k.Open(sealed, "b"); err == nil {
		t.Fatal("a different context must not open")
	}
	if _, err := testKey(t, "k22").Open(sealed, "a"); err == nil {
		t.Fatal("a different key must not open")
	}
	if _, err := k.Open(sealed[:len(sealed)-4]+"AAAA", "a"); err == nil {
		t.Fatal("a tampered value must not open")
	}
	if _, err := k.Open("garbage", "a"); err == nil {
		t.Fatal("garbage must not open")
	}
}

func TestSealUsesFreshNonce(t *testing.T) {
	k := testKey(t, "k1")
	a, _ := k.Seal([]byte("x"), "")
	b, _ := k.Seal([]byte("x"), "")
	if a == b {
		t.Fatal("two seals of the same value must differ")
	}
}

func TestRekey(t *testing.T) {
	oldKey, newKey := testKey(t, "old"), testKey(t, "newer")
	sealed, _ := oldKey.Seal([]byte("pw"), "c")
	moved, err := newKey.Rekey(oldKey, sealed, "c")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := newKey.Open(moved, "c"); err != nil || string(got) != "pw" {
		t.Fatalf("open after rekey = %q, %v", got, err)
	}
}

func TestLoadOrCreateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	first, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := first.Seal([]byte("x"), "")
	second, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Open(sealed, ""); err != nil {
		t.Fatalf("a reloaded key must open earlier values: %v", err)
	}
	if _, err := NewKey("k", []byte("short")); err == nil {
		t.Fatal("a short key must be refused")
	}
}

func TestToken(t *testing.T) {
	token, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 64 || token == hash {
		t.Fatalf("unexpected token %q", token)
	}
	if HashToken(token) != hash || HashToken(token+"0") == hash {
		t.Fatal("the hash must belong to the token")
	}
}

func TestServerTLSSelfSignedAndPinning(t *testing.T) {
	cfg, err := ServerTLS{CommonName: "localhost"}.Config()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.TLS = cfg
	srv.StartTLS()
	defer srv.Close()

	fp := Fingerprint(srv.Certificate())
	get := func(c ClientTLS) error {
		cc, err := c.Config()
		if err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: cc}}
		resp, err := client.Get(srv.URL)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if err := get(ClientTLS{}); err == nil {
		t.Fatal("an unknown self-signed certificate must be refused")
	}
	if err := get(ClientTLS{Fingerprint: fp}); err != nil {
		t.Fatalf("the pinned certificate must be accepted: %v", err)
	}
	other := strings.Repeat("00", 32)
	if err := get(ClientTLS{Fingerprint: other}); err == nil {
		t.Fatal("a different pin must be refused")
	}
	if err := get(ClientTLS{SkipVerify: true}); err != nil {
		t.Fatalf("skip verify: %v", err)
	}
	if _, err := (ClientTLS{Fingerprint: "zz"}).Config(); err == nil {
		t.Fatal("a malformed pin must be an error")
	}
}

func TestServerTLSRequiresClientCA(t *testing.T) {
	if _, err := (ServerTLS{RequireClientCert: true}).Config(); err == nil {
		t.Fatal("requiring client certificates without a CA must be an error")
	}
	cfg, err := ServerTLS{CommonName: "x"}.Config()
	if err != nil || cfg.ClientAuth != tls.NoClientCert || cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("unexpected defaults: %v %v", cfg, err)
	}
}

func TestKeyIDIsDerivedFromTheKey(t *testing.T) {
	a, _ := GenerateKey()
	b, _ := GenerateKey()
	if a.ID == "" || a.ID == b.ID {
		t.Fatalf("ids = %q, %q", a.ID, b.ID)
	}
	sealed, _ := a.Seal([]byte("x"), "c")
	if !strings.HasPrefix(sealed, a.ID+":") {
		t.Errorf("the sealed value must carry the key id: %q", sealed)
	}
	if _, err := b.Open(sealed, "c"); err == nil {
		t.Error("another key must not open it")
	}
}

func TestRotate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	old, _ := LoadOrCreateKey(path)
	sealed, _ := old.Seal([]byte("secret"), "c")
	var resealed string
	err := Rotate(path, func(from, to Key) error {
		plain, err := from.Open(sealed, "c")
		if err != nil {
			return err
		}
		resealed, err = to.Seal(plain, "c")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	now, _ := LoadOrCreateKey(path)
	if now.ID == old.ID {
		t.Fatal("the key file must hold the new key")
	}
	if got, err := now.Open(resealed, "c"); err != nil || string(got) != "secret" {
		t.Fatalf("the new key must open the resealed value: %q, %v", got, err)
	}
	if kept, _ := os.ReadFile(path + ".old"); strings.TrimSpace(string(kept)) != old.Encode() {
		t.Error("the old key must be kept as .old")
	}
	if _, err := os.Stat(path + ".new"); err == nil {
		t.Error(".new must be gone")
	}
}

func TestRotateChangesNothingWhenResealFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	old, _ := LoadOrCreateKey(path)
	err := Rotate(path, func(Key, Key) error { return errors.New("disk full") })
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("err = %v", err)
	}
	if now, _ := LoadOrCreateKey(path); now.ID != old.ID {
		t.Error("the key must be unchanged")
	}
	if _, err := os.Stat(path + ".new"); err == nil {
		t.Error(".new must be removed")
	}
}

func TestServerTLSWritesTheGeneratedCertificateOnce(t *testing.T) {
	dir := t.TempDir()
	s := ServerTLS{Dir: dir, CertificateFile: "certs/server.cert", KeyFile: "certs/server.key", CommonName: "localhost"}
	first, err := s.Config()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"certs/server.cert", "certs/server.key"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("%s must be written: %v", f, err)
		}
	}
	second, err := s.Config()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Certificates[0].Certificate[0], second.Certificates[0].Certificate[0]) {
		t.Error("the certificate must stay the same across restarts")
	}
	// A broken certificate is an error, it is never overwritten.
	if err := os.WriteFile(filepath.Join(dir, "certs/server.cert"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Config(); err == nil {
		t.Error("a broken certificate must be reported")
	}
}

func TestResolve(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "x.cert")
	for _, tc := range []struct{ dir, path, want string }{
		{"/data", "certs/a.cert", filepath.Join("/data", "certs/a.cert")},
		{"/data", abs, abs},
		{"", "a.cert", "a.cert"},
		{"/data", "", ""},
	} {
		if got := Resolve(tc.dir, tc.path); got != tc.want {
			t.Errorf("Resolve(%q, %q) = %q, want %q", tc.dir, tc.path, got, tc.want)
		}
	}
}
