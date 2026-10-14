package manager

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/config"
	"github.com/Valentin-Kaiser/backup/pkg/runner"
	"github.com/Valentin-Kaiser/backup/pkg/store"
)

// chainManager returns a manager with a backup job followed by forget and check.
func chainManager(t *testing.T, mode string) *Manager {
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
	c.Jobs = []config.Job{
		{Name: "backup", Type: config.TypeBackup, Enabled: true, Schedule: "0 0 * * *"},
		{Name: "forget", Type: config.TypeForget, After: "backup"},
		{Name: "check", Type: config.TypeCheck, After: "forget"},
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	r := runner.Runner{Binary: os.Args[0], Prefix: []string{"-test.run=TestHelperProcess", "--"}}
	m := New(func() config.Config { return c }, r, st)
	t.Cleanup(func() { m.cancel() })
	return m
}

func waitRun(t *testing.T, m *Manager, job string) (store.Run, bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r, ok := m.store.Last(job); ok {
			return r, true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return store.Run{}, false
}

func TestSuccessStartsTheFollowers(t *testing.T) {
	m := chainManager(t, "ok")
	if _, err := m.Trigger("backup", "manual", true); err != nil {
		t.Fatal(err)
	}
	for job, trigger := range map[string]string{"forget": "after:backup", "check": "after:forget"} {
		r, ok := waitRun(t, m, job)
		if !ok || r.Trigger != trigger {
			t.Errorf("%s: run = %+v, found %v", job, r, ok)
		}
	}
}

func TestFailureDoesNotStartTheFollowers(t *testing.T) {
	m := chainManager(t, "fail")
	if _, err := m.Trigger("backup", "manual", true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, ok := m.store.Last("forget"); ok {
		t.Error("forget ran after a failed backup")
	}
}

func TestFollowedJobCannotBeDeleted(t *testing.T) {
	m := chainManager(t, "ok")
	if err := m.DeleteJob("backup"); err == nil {
		t.Error("deleting a job that others follow must fail")
	}
}
