package runner

import (
	"context"
	"os"
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
