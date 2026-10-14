package syncserve

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Valentin-Kaiser/backup/pkg/config"
)

func TestArgsKeepThePasswordOffTheCommandLine(t *testing.T) {
	pw := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pw, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := config.SyncServe{Path: "/sync", Address: "0.0.0.0", Port: 9081, Username: "sync", PasswordFile: pw}
	args := Args(s)
	if strings.Contains(strings.Join(args, " "), "secret") {
		t.Errorf("password on the command line: %q", args)
	}
	for _, want := range []string{"serve", "webdav", "/sync", "0.0.0.0:9081", "sync"} {
		if !slices.Contains(args, want) {
			t.Errorf("%q is missing in %q", want, args)
		}
	}
	if slices.Contains(args, "--cert") {
		t.Error("no certificate configured")
	}
	env, err := Env(s)
	if err != nil || len(env) != 1 || env[0] != "RCLONE_PASS=secret" {
		t.Errorf("env = %q, err = %v", env, err)
	}
	s.CertificateFile, s.KeyFile = "/c.pem", "/k.pem"
	if !slices.Contains(Args(s), "--cert") || !slices.Contains(Args(s), "--key") {
		t.Error("certificate and key expected")
	}
}
