package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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
		_, _ = os.Stdout.WriteString("file=" + file + "\n")
		if err == nil {
			_, _ = os.Stdout.WriteString("pw=" + string(data) + "\n")
			if fi, e := os.Stat(file); e == nil {
				_, _ = os.Stdout.WriteString("mode=" + fi.Mode().Perm().String() + "\n")
			}
		}
		for i, a := range os.Args {
			if a == "--new-password-file" && i+1 < len(os.Args) {
				nd, _ := os.ReadFile(os.Args[i+1])
				_, _ = os.Stdout.WriteString("newfile=" + os.Args[i+1] + "\nnew=" + string(nd) + "\n")
			}
		}
		os.Exit(0)
	}
	_, _ = os.Stdout.WriteString("to stdout\n")
	_, _ = os.Stderr.WriteString("to stderr\n")
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

func TestArgsSync(t *testing.T) {
	c := *config.Default()
	c.SyncDir = t.TempDir()
	job := config.Job{Name: "mirror", Type: config.TypeSync, Path1: "/sync/a", Path2: "/sync/b", Args: "--exclude *.tmp"}

	// No state yet: the first run has to build the listings.
	got, err := Args(c, job)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"bisync", "/sync/a", "/sync/b", "--workdir", filepath.Join(c.SyncDir, "mirror"),
		"--conflict-resolve", "newer", "--conflict-loser", "num", "--max-delete", "50",
		"--resilient", "--recover", "--create-empty-src-dirs", "--resync", "--exclude", "*.tmp",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}

	// With listings no resync, and the settings of the job replace the defaults.
	state := filepath.Join(c.SyncDir, "mirror")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "a..b.path1.lst"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	job.Force = true
	job.Path2, job.Conflict, job.ConflictLoser, job.MaxDeletePct, job.CheckAccess = "peer:west/data", config.ConflictNone, config.LoserDelete, 10, true
	got, _ = Args(c, job)
	line := strings.Join(got, " ")
	for _, s := range []string{"peer_west:data", "--conflict-resolve none", "--conflict-loser delete", "--max-delete 10", "--check-access", "--force"} {
		if !strings.Contains(line, s) {
			t.Errorf("%q is missing in %q", s, line)
		}
	}
	if strings.Contains(line, "--resync") {
		t.Errorf("an existing state must not be resynced: %q", line)
	}

	if err := ResetSync(c, "mirror"); err != nil {
		t.Fatal(err)
	}
	if !NeedsResync(c, "mirror") {
		t.Error("a reset job must resync")
	}
}

func TestSyncRunsRcloneWithPeerRemote(t *testing.T) {
	dir := t.TempDir()
	pw := filepath.Join(dir, "pw")
	if err := os.WriteFile(pw, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := *config.Default()
	c.Nice = 0
	c.Peers = []config.Peer{{Name: "west", URL: "https://west:9081", Username: "sync", PasswordFile: pw}}
	t.Setenv("GO_FAKE_RCLONE", "1")
	r := Runner{SyncBinary: os.Args[0], SyncPrefix: []string{"-test.run=TestRcloneHelper", "--"}}
	var lines []string
	res, err := r.Exec(context.Background(), c, func(l string) { lines = append(lines, l) }, SyncCommand, "/a", "peer_west:")
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	out := strings.Join(lines, "\n")
	for _, s := range []string{"args=bisync /a peer_west:", "type=webdav", "url=https://west:9081", "user=sync", "pass=obscured(secret)"} {
		if !strings.Contains(out, s) {
			t.Errorf("%q is missing in %q", s, out)
		}
	}
}

// TestRcloneHelper is re-executed as a fake rclone.
func TestRcloneHelper(t *testing.T) {
	if os.Getenv("GO_FAKE_RCLONE") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) > 0 && args[0] == "obscure" {
		in, _ := io.ReadAll(os.Stdin)
		_, _ = os.Stdout.WriteString("obscured(" + string(in) + ")\n")
		os.Exit(0)
	}
	fmt.Println("args=" + strings.Join(args, " "))
	fmt.Println("type=" + os.Getenv("RCLONE_CONFIG_PEER_WEST_TYPE"))
	fmt.Println("url=" + os.Getenv("RCLONE_CONFIG_PEER_WEST_URL"))
	fmt.Println("user=" + os.Getenv("RCLONE_CONFIG_PEER_WEST_USER"))
	fmt.Println("pass=" + os.Getenv("RCLONE_CONFIG_PEER_WEST_PASS"))
	os.Exit(0)
}

func TestArgsUnison(t *testing.T) {
	c := *config.Default()
	c.SyncDir = t.TempDir()
	job := config.Job{Name: "mirror", Type: config.TypeSync, Engine: config.EngineUnison, Path1: "/sync/a", Path2: "/sync/b", Args: "-ignore 'Name *.tmp'"}
	got, err := Args(c, job)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(got, " ")
	for _, s := range []string{
		UnisonCommand + " " + filepath.Join(c.SyncDir, "mirror") + " /sync/a /sync/b",
		"-batch", "-ignorelocks", "-confirmbigdel", "-prefer newer", "-copyonconflict", "-ignore Name *.tmp",
	} {
		if !strings.Contains(line, s) {
			t.Errorf("%q is missing in %q", s, line)
		}
	}
	for _, s := range []string{"-sshcmd", "-mountpoint", "--resync", "--force"} {
		if strings.Contains(line, s) {
			t.Errorf("%q must not be in %q", s, line)
		}
	}

	job.Path2, job.Conflict, job.ConflictLoser, job.CheckAccess = "peer:east/data", config.ConflictPath2, config.LoserDelete, true
	got, _ = Args(c, job)
	line = strings.Join(got, " ")
	for _, s := range []string{"ssh://east/data", "-prefer ssh://east/data", "-sshcmd control-tunnel", "-mountpoint RCLONE_TEST"} {
		if !strings.Contains(line, s) {
			t.Errorf("%q is missing in %q", s, line)
		}
	}
	if strings.Contains(line, "-copyonconflict") {
		t.Errorf("a deleted loser is not kept: %q", line)
	}
	job.Path2 = "peer:east"
	if got, _ = Args(c, job); !strings.Contains(strings.Join(got, " "), "ssh://east/.") {
		t.Errorf("the whole export is the directory '.': %q", got)
	}
	job.Conflict = config.ConflictNone
	if got, _ = Args(c, job); strings.Contains(strings.Join(got, " "), "-prefer") {
		t.Errorf("none resolves nothing: %q", got)
	}
}

func TestUnisonRunsWithTheStateAndTheTunnel(t *testing.T) {
	c := *config.Default()
	c.Nice = 0
	c.WorkerName = "west"
	c.SyncDir = t.TempDir()
	t.Setenv("GO_FAKE_UNISON", "1")
	r := Runner{UnisonBinary: os.Args[0], UnisonPrefix: []string{"-test.run=TestUnisonHelper", "--"}}
	job := config.Job{Name: "mirror", Type: config.TypeSync, Engine: config.EngineUnison, Path1: "/a", Path2: "peer:east"}
	args, _ := Args(c, job)
	ctx := WithTunnel(context.Background(), Tunnel{URL: "https://east:9080", Password: "pw", CAFile: "/ca", Job: "mirror", Owner: "west"})
	var lines []string
	if _, err := r.Exec(ctx, c, func(l string) { lines = append(lines, l) }, args...); err != nil {
		t.Fatal(err)
	}
	out := strings.Join(lines, "\n")
	for _, s := range []string{
		"unison=" + filepath.Join(c.SyncDir, "mirror"), "host=west", "url=https://east:9080", "pass=pw", "ca=/ca", "job=mirror", "owner=west", "args=/a ssh://east/.",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("%q is missing in %q", s, out)
		}
	}
	if st, err := os.Stat(filepath.Join(c.SyncDir, "mirror")); err != nil || !st.IsDir() {
		t.Errorf("the state directory must exist: %v", err)
	}

	// Exit codes of unison are explained.
	t.Setenv("GO_FAKE_UNISON_EXIT", "1")
	_, err := r.Exec(context.Background(), c, nil, args...)
	if err == nil || !strings.Contains(err.Error(), "skipped") {
		t.Errorf("err = %v", err)
	}
}

// TestUnisonHelper is re-executed as a fake unison.
func TestUnisonHelper(t *testing.T) {
	if os.Getenv("GO_FAKE_UNISON") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	fmt.Println("unison=" + os.Getenv("UNISON"))
	fmt.Println("host=" + os.Getenv("UNISONLOCALHOSTNAME"))
	fmt.Println("url=" + os.Getenv("CONTROL_TUNNEL_URL"))
	fmt.Println("pass=" + os.Getenv("CONTROL_TUNNEL_PASSWORD"))
	fmt.Println("ca=" + os.Getenv("CONTROL_TUNNEL_CA"))
	fmt.Println("job=" + os.Getenv("CONTROL_TUNNEL_JOB"))
	fmt.Println("owner=" + os.Getenv("CONTROL_TUNNEL_OWNER"))
	fmt.Println("args=" + strings.Join(args[:2], " "))
	if code := os.Getenv("GO_FAKE_UNISON_EXIT"); code != "" {
		n, _ := strconv.Atoi(code)
		os.Exit(n)
	}
	os.Exit(0)
}

func TestLastRewrite(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                        "plain",
		"Starting...\r      \r[BGN] x": "[BGN] x",
		"50%\r     \r100% done\r    ":  "100% done",
		"\r   \r":                      "",
	} {
		if got := lastRewrite(in); got != want {
			t.Errorf("lastRewrite(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPreflightChecksEveryMount(t *testing.T) {
	mounted := t.TempDir()
	if err := os.WriteFile(filepath.Join(mounted, ".mounted"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	unmounted := t.TempDir()

	r := Runner{}
	if err := r.Preflight(context.Background(), config.Config{}); err != nil {
		t.Errorf("no guard checked: %v", err)
	}
	ok := config.Config{MountCheck: config.MountCheck{Path: mounted, MarkerFile: ".mounted"}}
	if err := r.Preflight(context.Background(), ok); err != nil {
		t.Errorf("mounted repository refused: %v", err)
	}
	// One of the additional volumes is unmounted, nothing may be written anywhere.
	ok.MountChecks = []config.MountCheck{{Path: unmounted, MarkerFile: ".mounted"}, {Path: mounted}}
	if err := r.Preflight(context.Background(), ok); err == nil {
		t.Error("unmounted additional volume accepted")
	}
	// A sync job brings its own guards, the global ones do not apply.
	jc := ok.ForJob(config.Job{Type: config.TypeSync, Mounts: []config.MountCheck{{Path: mounted, MarkerFile: ".mounted"}}})
	if err := r.Preflight(context.Background(), jc); err != nil {
		t.Errorf("guards of the job refused: %v", err)
	}
	jc = ok.ForJob(config.Job{Type: config.TypeSync, Mounts: []config.MountCheck{{Path: unmounted, MarkerFile: ".mounted"}}})
	if err := r.Preflight(context.Background(), jc); err == nil {
		t.Error("unmounted volume of the job accepted")
	}
}
