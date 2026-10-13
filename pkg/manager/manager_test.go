package manager

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/config"
	"github.com/Valentin-Kaiser/backup/pkg/runner"
	"github.com/Valentin-Kaiser/backup/pkg/store"
)

// TestHelperProcess is re-executed as a fake restic.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_FAKE_RESTIC") != "1" {
		return
	}
	os.Stdout.WriteString("fake output\n")
	switch os.Getenv("GO_FAKE_MODE") {
	case "fail":
		os.Stderr.WriteString("boom\n")
		os.Exit(1)
	case "fail-forget":
		if slices.Contains(os.Args, "forget") {
			os.Stderr.WriteString("boom\n")
			os.Exit(1)
		}
	case "sleep":
		if !slices.Contains(os.Args, "backup") {
			break
		}
		time.Sleep(30 * time.Second)
	}
	os.Exit(0)
}

func newManager(t *testing.T, mode string) *Manager {
	t.Helper()
	t.Setenv("GO_FAKE_RESTIC", "1")
	t.Setenv("GO_FAKE_MODE", mode)
	st, err := store.Open(filepath.Join(t.TempDir(), "h.jsonl"), 100)
	if err != nil {
		t.Fatal(err)
	}
	c := *config.Default()
	c.Nice = 0
	c.Source = "/src"
	c.Jobs = []config.Job{{Name: "backup", Type: config.TypeBackup, Enabled: true, Schedule: "0 0 * * *", MaxAgeHours: 26}}
	r := runner.Runner{Binary: os.Args[0], Prefix: []string{"-test.run=TestHelperProcess", "--"}}
	m := New(func() config.Config { return c }, r, st)
	t.Cleanup(func() { m.cancel() })
	return m
}

func TestTriggerSuccess(t *testing.T) {
	m := newManager(t, "ok")
	run, err := m.Trigger("backup", "manual", true)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.StatusSuccess || run.Finished == nil {
		t.Fatalf("run = %+v", run)
	}
	if got, ok := m.store.Last("backup"); !ok || got.Status != store.StatusSuccess || len(got.Output) == 0 {
		t.Errorf("history = %+v", got)
	}
	if ok, _ := m.Health(); !ok {
		t.Error("expected healthy")
	}
}

func TestTriggerFailureIsReported(t *testing.T) {
	m := newManager(t, "fail")
	run, err := m.Trigger("backup", "manual", true)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.StatusFailed || run.ExitCode != 1 {
		t.Fatalf("run = %+v", run)
	}
	if ok, problems := m.Health(); ok || len(problems) == 0 {
		t.Error("expected unhealthy after failed run")
	}
}

func TestBusyAndCancel(t *testing.T) {
	m := newManager(t, "sleep")
	if _, err := m.Trigger("backup", "manual", false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Trigger("backup", "manual", false); !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy, got %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := m.Cancel("backup"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if r, ok := m.store.Last("backup"); ok {
			if r.Status != store.StatusCancelled {
				t.Fatalf("status = %s", r.Status)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("run did not finish after cancel")
}

func TestMountCheckSkipsRun(t *testing.T) {
	m := newManager(t, "ok")
	c := m.cfg()
	c.MountCheck.Path = t.TempDir()
	c.MountCheck.MarkerFile = ".mounted"
	m.cfg = func() config.Config { return c }
	run, err := m.Trigger("backup", "manual", true)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.StatusSkipped {
		t.Fatalf("status = %s", run.Status)
	}
}

func TestRestoreTargetMustBeBelowRoot(t *testing.T) {
	m := newManager(t, "ok")
	c := m.cfg()
	c.RestoreRoot = t.TempDir()
	m.cfg = func() config.Config { return c }
	if _, err := m.Restore("", "latest", filepath.Join(c.RestoreRoot, "..", "evil"), nil); err == nil {
		t.Error("expected restore outside the root to be refused")
	}
	if _, err := m.Restore("", "-x", filepath.Join(c.RestoreRoot, "ok"), nil); err == nil {
		t.Error("expected option-like snapshot to be refused")
	}
	if _, err := m.Restore("", "latest", filepath.Join(c.RestoreRoot, "ok"), nil); err != nil {
		t.Errorf("valid restore refused: %v", err)
	}
}

func TestMultipleJobsUseTheirOwnRepository(t *testing.T) {
	m := newManager(t, "ok")
	c := m.cfg()
	c.Jobs = []config.Job{
		{Name: "photos", Type: config.TypeBackup, Enabled: true, Schedule: "0 0 * * *", Source: "/photos", Repository: "/repo/photos"},
		{Name: "docs", Type: config.TypeBackup, Enabled: true, Schedule: "0 1 * * *", Source: "/docs", Repository: "/repo/docs"},
		{Name: "docs-prune", Type: config.TypeForget, Enabled: false, Schedule: "0 2 * * *", Repository: "/repo/docs"},
	}
	m.cfg = func() config.Config { return c }
	if err := m.Reload(); err != nil {
		t.Fatal(err)
	}

	names := []string{}
	for _, s := range m.Status() {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, []string{"photos", "docs", "docs-prune", "restore"}) {
		t.Fatalf("status = %v", names)
	}

	for _, name := range []string{"photos", "docs", "docs-prune"} {
		run, err := m.Trigger(name, "manual", true)
		if err != nil || run.Status != store.StatusSuccess {
			t.Fatalf("%s: run = %+v, err = %v", name, run, err)
		}
	}
	if _, err := m.Trigger("nope", "manual", true); err == nil {
		t.Error("an unknown job must be refused")
	}
	if m.sem("/repo/docs") == m.sem("/repo/photos") || m.sem("/repo/docs") != m.sem("/repo/docs") {
		t.Error("jobs must share a lock per repository, not across repositories")
	}

	// Removed jobs leave the schedule, history stays.
	c.Jobs = c.Jobs[:1]
	if err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	if len(m.Status()) != 2 {
		t.Errorf("status = %v", m.Status())
	}
	if err := m.Pause("docs"); err == nil {
		t.Error("a removed job cannot be paused")
	}
}

func TestSnapshotsOfUnknownJobFail(t *testing.T) {
	m := newManager(t, "ok")
	if _, err := m.Snapshots(t.Context(), "nope"); err == nil {
		t.Error("expected an error for an unknown job")
	}
}

// syncManager returns a manager with a sync job over two directories, rclone is the fake helper process.
func syncManager(t *testing.T, mode string) (*Manager, config.Job) {
	t.Helper()
	m := newManager(t, mode)
	m.local = runner.Runner{
		Binary: os.Args[0], Prefix: []string{"-test.run=TestHelperProcess", "--"},
		SyncBinary: os.Args[0], SyncPrefix: []string{"-test.run=TestHelperProcess", "--"},
	}
	c := m.cfg()
	c.SyncDir = t.TempDir()
	// A password secret of the global settings must not matter for a sync job.
	c.PasswordSecret = "main"
	j := config.Job{Name: "mirror", Type: config.TypeSync, Enabled: true, Schedule: "*/5 * * * *", Path1: t.TempDir(), Path2: t.TempDir()}
	c.Jobs = append(slices.Clone(c.Jobs), j)
	m.cfg = func() config.Config { return c }
	return m, j
}

func TestSyncJobRuns(t *testing.T) {
	m, _ := syncManager(t, "ok")
	run, err := m.Trigger("mirror", "manual", true)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.StatusSuccess {
		t.Fatalf("run = %+v", run)
	}
}

func TestSyncJobFailureIsReported(t *testing.T) {
	m, _ := syncManager(t, "fail")
	run, _ := m.Trigger("mirror", "manual", true)
	if run.Status != store.StatusFailed || run.ExitCode != 1 {
		t.Fatalf("run = %+v", run)
	}
}

func TestSyncJobSkippedWhileAVolumeIsMissing(t *testing.T) {
	m, j := syncManager(t, "ok")
	c := m.cfg()
	c.Jobs[len(c.Jobs)-1].Path2 = filepath.Join(j.Path2, "unmounted")
	m.cfg = func() config.Config { return c }
	run, err := m.Trigger("mirror", "manual", true)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.StatusSkipped {
		t.Fatalf("status = %s, a missing volume must not be synced", run.Status)
	}
}

func TestSyncJobsOfTheSamePairNeverRunTogether(t *testing.T) {
	m, j := syncManager(t, "ok")
	c := m.cfg()
	other := j
	other.Name = "mirror2"
	other.Path1, other.Path2 = j.Path2, j.Path1 // same pair, other order
	c.Jobs = append(slices.Clone(c.Jobs), other)
	m.cfg = func() config.Config { return c }
	if m.cfg().RepositoryKey(j) != m.cfg().RepositoryKey(other) {
		t.Fatal("the pair must give the same lock")
	}
}

func TestResyncJobDropsTheState(t *testing.T) {
	m, _ := syncManager(t, "ok")
	c := m.cfg()
	state := runner.SyncStateDir(c, "mirror")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "x.path1.lst"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	run, err := m.ResyncJob("mirror")
	if err != nil {
		t.Fatal(err)
	}
	if run.Trigger != "resync" {
		t.Errorf("trigger = %q", run.Trigger)
	}
	m.wg.Wait() // the run writes into the temporary directories
	if _, err := m.ResyncJob("backup"); err == nil {
		t.Error("only sync jobs can be resynced")
	}
}

func TestSyncJobsDoNotInitialiseARepository(t *testing.T) {
	m, _ := syncManager(t, "fail")
	m.cfg = func() config.Config {
		c := *config.Default()
		c.Nice, c.SyncDir = 0, t.TempDir()
		c.Jobs = []config.Job{{Name: "mirror", Type: config.TypeSync, Path1: t.TempDir(), Path2: t.TempDir()}}
		return c
	}
	// With the fake in "fail" mode a repository check would fail and be logged, an init would be attempted. Nothing
	// may be started for a configuration that only has sync jobs.
	m.initRepositories()
	if len(m.Runs("", 10)) != 0 {
		t.Error("no run expected")
	}
}
