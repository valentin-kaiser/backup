package runner

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Valentin-Kaiser/backup/pkg/config"
)

func TestSplitArgs(t *testing.T) {
	tests := []struct {
		in      string
		want    []string
		wantErr bool
	}{
		{"", nil, false},
		{"--a  --b", []string{"--a", "--b"}, false},
		{`--exclude "a b" 'c d'`, []string{"--exclude", "a b", "c d"}, false},
		{`a\ b`, []string{"a b"}, false},
		{`--x=$(id) ;ls`, []string{"--x=$(id)", ";ls"}, false},
		{`"open`, nil, true},
		{`trail\`, nil, true},
		{`""`, []string{""}, false},
	}
	for _, tt := range tests {
		got, err := SplitArgs(tt.in)
		if (err != nil) != tt.wantErr {
			t.Fatalf("%q: err = %v", tt.in, err)
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q: got %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestArgsBackup(t *testing.T) {
	c := *config.Default()
	c.Source = "/src"
	c.ReadConcurrency = 4
	c.PackSize = 32
	c.OneFileSystem = true
	job := config.Job{Type: config.TypeBackup, Args: "--exclude-caches"}
	got, err := Args(c, job)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"backup", "--retry-lock", "5m", "--pack-size", "32", "--read-concurrency", "4", "--one-file-system", "--exclude-caches", "--", "/src"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestArgsForget(t *testing.T) {
	c := *config.Default()
	got, _ := Args(c, config.Job{Type: config.TypeForget, Args: "--prune --keep-last 3"})
	if strings.Join(got, " ") != "forget --retry-lock 5m --prune --keep-last 3" {
		t.Errorf("got %q", got)
	}
	if _, err := Args(c, config.Job{Type: "nope"}); err == nil {
		t.Error("expected error for unknown type")
	}
}

// TestHelperProcess is re-executed as a fake restic.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_FAKE_RESTIC") != "1" {
		return
	}
	if os.Getenv("GO_FAKE_ECHO") == "1" {
		// Report what restic would see: the password file, the file of the new password and the arguments.
		file := os.Getenv("RESTIC_PASSWORD_FILE")
		data, err := os.ReadFile(file)
		os.Stdout.WriteString("file=" + file + "\n")
		if err == nil {
			os.Stdout.WriteString("pw=" + string(data) + "\n")
			if fi, e := os.Stat(file); e == nil {
				os.Stdout.WriteString("mode=" + fi.Mode().Perm().String() + "\n")
			}
		}
		for i, a := range os.Args {
			if a == "--new-password-file" && i+1 < len(os.Args) {
				nd, _ := os.ReadFile(os.Args[i+1])
				os.Stdout.WriteString("newfile=" + os.Args[i+1] + "\nnew=" + string(nd) + "\n")
			}
		}
		os.Exit(0)
	}
	os.Stdout.WriteString("to stdout\n")
	os.Stderr.WriteString("to stderr\n")
	if os.Getenv("GO_FAKE_EXIT") == "3" {
		os.Exit(3)
	}
	os.Exit(0)
}

func fake() Runner {
	return Runner{Binary: os.Args[0], Prefix: []string{"-test.run=TestHelperProcess", "--"}}
}

func TestExecCapturesBothStreamsAndExitCode(t *testing.T) {
	t.Setenv("GO_FAKE_RESTIC", "1")
	t.Setenv("GO_FAKE_EXIT", "3")
	c := *config.Default()
	c.Nice = 0
	var lines []string
	res, err := fake().Exec(context.Background(), c, func(l string) { lines = append(lines, l) }, "backup")
	if err == nil || res.ExitCode != 3 {
		t.Fatalf("exit = %d, err = %v", res.ExitCode, err)
	}
	joined := strings.Join(lines, "|")
	if !strings.Contains(joined, "to stdout") || !strings.Contains(joined, "to stderr") {
		t.Errorf("missing output: %q", joined)
	}
}

func TestExecSuccess(t *testing.T) {
	t.Setenv("GO_FAKE_RESTIC", "1")
	c := *config.Default()
	c.Nice = 0
	if _, err := fake().Exec(context.Background(), c, nil, "check"); err != nil {
		t.Fatal(err)
	}
}

// echo runs the fake restic and returns what it reported as key=value pairs.
func echo(t *testing.T, ctx context.Context, args ...string) map[string]string {
	t.Helper()
	t.Setenv("GO_FAKE_RESTIC", "1")
	t.Setenv("GO_FAKE_ECHO", "1")
	c := *config.Default()
	c.Nice = 0
	c.PasswordFile = "/from/the/config"
	out := map[string]string{}
	if _, err := fake().Exec(ctx, c, func(l string) {
		if k, v, ok := strings.Cut(l, "="); ok {
			out[k] = v
		}
	}, args...); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestThePasswordOfTheContextReachesResticInAPrivateFile(t *testing.T) {
	got := echo(t, WithPassword(context.Background(), "s3cret value"), "snapshots")
	if got["pw"] != "s3cret value" {
		t.Fatalf("restic saw %v", got)
	}
	if got["file"] == "/from/the/config" {
		t.Error("the secret must replace the password file of the configuration")
	}
	if got["mode"] != "" && os.PathSeparator == '/' && got["mode"] != "-rw-------" {
		t.Errorf("the file must be private: %s", got["mode"])
	}
	if _, err := os.Stat(got["file"]); err == nil {
		t.Error("the password file must be removed after the run")
	}
	if _, err := os.Stat(filepath.Dir(got["file"])); err == nil {
		t.Error("the temporary directory must be removed too")
	}
}

func TestWithoutASecretTheConfiguredPasswordFileIsUsed(t *testing.T) {
	got := echo(t, context.Background(), "snapshots")
	if got["file"] != "/from/the/config" {
		t.Errorf("restic saw %v", got)
	}
}

func TestTheNewPasswordReplacesThePlaceholder(t *testing.T) {
	ctx := WithNewPassword(WithPassword(context.Background(), "old"), "brand new")
	got := echo(t, ctx, "key", "passwd", "--new-password-file", NewPasswordFile)
	if got["pw"] != "old" || got["new"] != "brand new" {
		t.Fatalf("restic saw %v", got)
	}
	if _, err := os.Stat(got["newfile"]); err == nil {
		t.Error("the new password file must be removed")
	}
}

func TestAPlaceholderWithoutANewPasswordFailsBeforeResticRuns(t *testing.T) {
	t.Setenv("GO_FAKE_RESTIC", "1")
	c := *config.Default()
	c.Nice = 0
	ctx := WithPassword(context.Background(), "old")
	if _, err := fake().Exec(ctx, c, nil, "key", "passwd", "--new-password-file", NewPasswordFile); err == nil {
		t.Fatal("expected an error")
	}
}

func TestOutputUsesThePasswordToo(t *testing.T) {
	t.Setenv("GO_FAKE_RESTIC", "1")
	t.Setenv("GO_FAKE_ECHO", "1")
	c := *config.Default()
	c.Nice = 0
	out, err := fake().Output(WithPassword(context.Background(), "via output"), c, "snapshots")
	if err != nil || !strings.Contains(string(out), "pw=via output") {
		t.Fatalf("out = %q, %v", out, err)
	}
}
