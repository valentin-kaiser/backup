package manager

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/config"
	"github.com/Valentin-Kaiser/backup/pkg/store"
)

// groupManager returns a manager with a backup, a forget and a check job that only run with the group "nightly".
func groupManager(t *testing.T, mode string, g config.Job) *Manager {
	t.Helper()
	m := newManager(t, mode)
	c := m.cfg()
	g.Name, g.Type, g.Enabled, g.Schedule = "nightly", config.TypeGroup, true, "0 1 * * *"
	c.Jobs = []config.Job{
		{Name: "backup", Type: config.TypeBackup},
		{Name: "forget", Type: config.TypeForget},
		{Name: "check", Type: config.TypeCheck},
		g,
	}
	m.cfg = func() config.Config { return c }
	if err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	return m
}

func lastStatus(m *Manager, job string) store.Status {
	if r, ok := m.store.Last(job); ok {
		return r.Status
	}
	return ""
}

func TestGroupRunsMembersInOrder(t *testing.T) {
	m := groupManager(t, "ok", config.Job{Members: []string{"backup", "forget", "check"}})
	run, err := m.Trigger("nightly", "manual", true)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.StatusSuccess || run.Finished == nil {
		t.Fatalf("run = %+v", run)
	}
	var order []string
	for _, name := range []string{"backup", "forget", "check"} {
		r, ok := m.store.Last(name)
		if !ok || r.Status != store.StatusSuccess || r.Trigger != "group:nightly" {
			t.Fatalf("%s: run = %+v", name, r)
		}
		order = append(order, r.Started.Format(time.RFC3339Nano))
	}
	if !slices.IsSorted(order) {
		t.Errorf("members did not run in order: %v", order)
	}
	if out := strings.Join(run.Output, "\n"); !strings.Contains(out, "[1/3] backup") || !strings.Contains(out, "check: success") {
		t.Errorf("output = %q", out)
	}
	if got, ok := m.store.Last("nightly"); !ok || got.Status != store.StatusSuccess {
		t.Errorf("history = %+v", got)
	}
}

func TestGroupStopsAtTheFirstFailure(t *testing.T) {
	m := groupManager(t, "fail-forget", config.Job{Members: []string{"backup", "forget", "check"}})
	run, err := m.Trigger("nightly", "manual", true)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.StatusFailed || !strings.Contains(run.Error, "forget") {
		t.Fatalf("run = %+v", run)
	}
	if got := lastStatus(m, "backup"); got != store.StatusSuccess {
		t.Errorf("backup = %q", got)
	}
	if got := lastStatus(m, "check"); got != "" {
		t.Errorf("check ran after the failure: %q", got)
	}
}

func TestGroupContinuesOnError(t *testing.T) {
	m := groupManager(t, "fail-forget", config.Job{Members: []string{"backup", "forget", "check"}, ContinueOnError: true})
	run, err := m.Trigger("nightly", "manual", true)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.StatusFailed || !strings.Contains(run.Error, "forget") || strings.Contains(run.Error, "check") {
		t.Fatalf("run = %+v", run)
	}
	if got := lastStatus(m, "check"); got != store.StatusSuccess {
		t.Errorf("check = %q, want it to run after the failure", got)
	}
}

func TestGroupIsScheduledAndItsMembersAreNot(t *testing.T) {
	m := groupManager(t, "ok", config.Job{Members: []string{"backup", "check"}})
	byName := map[string]JobStatus{}
	for _, s := range m.Status() {
		byName[s.Name] = s
	}
	if g := byName["nightly"]; g.Type != config.TypeGroup || g.NextRun == nil || g.Schedule != "0 1 * * *" {
		t.Errorf("group = %+v", g)
	}
	if byName["backup"].NextRun != nil {
		t.Error("a member must not be scheduled on its own")
	}
}

func TestGroupCancelStopsTheMemberAndTheRest(t *testing.T) {
	m := groupManager(t, "sleep", config.Job{Members: []string{"backup", "check"}})
	if _, err := m.Trigger("nightly", "manual", false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		_, member := m.active["backup"]
		m.mu.Unlock()
		if member {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if err := m.Cancel("nightly"); err != nil {
		t.Fatal(err)
	}
	for time.Now().Before(deadline) {
		if lastStatus(m, "nightly") != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := lastStatus(m, "nightly"); got != store.StatusCancelled {
		t.Errorf("group = %q", got)
	}
	if got := lastStatus(m, "backup"); got != store.StatusCancelled {
		t.Errorf("backup = %q", got)
	}
	if got := lastStatus(m, "check"); got != "" {
		t.Errorf("check ran after the cancel: %q", got)
	}
}

func TestGroupMembersCannotBeDeletedOrQueried(t *testing.T) {
	m := groupManager(t, "ok", config.Job{Members: []string{"backup"}})
	m.SetUpdater(func(f func(*config.Config) error) error { c := m.cfg(); return f(&c) })
	if err := m.DeleteJob("backup"); err == nil || !strings.Contains(err.Error(), "nightly") {
		t.Errorf("deleting a member = %v", err)
	}
	if _, err := m.Snapshots(t.Context(), "nightly"); err == nil {
		t.Error("a group has no repository")
	}
	if err := m.Unlock("nightly"); err == nil {
		t.Error("a group has no repository to unlock")
	}
}

func TestEnablingAGroupTakesTheScheduleFromItsMembers(t *testing.T) {
	m := newManager(t, "ok")
	c := m.cfg()
	c.Jobs = []config.Job{
		{Name: "backup", Type: config.TypeBackup, Enabled: true, Schedule: "0 0 * * *"},
		{Name: "check", Type: config.TypeCheck, Enabled: true, Schedule: "0 5 * * 3"},
	}
	m.cfg = func() config.Config { return c }
	m.SetUpdater(func(f func(*config.Config) error) error {
		n := c
		n.Jobs = slices.Clone(c.Jobs)
		if err := f(&n); err != nil {
			return err
		}
		if err := n.Validate(); err != nil {
			return err
		}
		c = n
		return nil
	})
	if _, err := m.CreateJob(config.Job{Name: "nightly", Type: config.TypeGroup, Enabled: true, Schedule: "0 1 * * *", Members: []string{"backup", "check"}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"backup", "check"} {
		if j, _ := c.Job(name); j.Enabled {
			t.Errorf("%s is still scheduled on its own", name)
		}
	}
}
