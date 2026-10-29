package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Valentin-Kaiser/backup/pkg/config"
	"github.com/Valentin-Kaiser/backup/pkg/runner"
	"github.com/Valentin-Kaiser/backup/pkg/store"
)

// fakeRestic simulates the key handling of restic for several repositories: a password opens a repository when one
// of its keys has it, and the key that opened the session cannot be removed.
type fakeRestic struct {
	mu         sync.Mutex
	keys       map[string]map[string]string // repository -> key id -> password
	next       int
	failAddOn  string
	failRemove bool
	seenBackup []string // passwords backups ran with
	events     []string
}

func newFakeRestic(passwords map[string]string) *fakeRestic {
	f := &fakeRestic{keys: map[string]map[string]string{}}
	for repo, pw := range passwords {
		f.keys[repo] = map[string]string{"key-" + repo: pw}
	}
	return f
}

func (f *fakeRestic) session(ctx context.Context, repo string) (string, error) {
	pw := runner.PasswordFrom(ctx)
	for id, p := range f.keys[repo] {
		if p == pw && pw != "" {
			return id, nil
		}
	}
	return "", errors.New("wrong password or no key found")
}

func (f *fakeRestic) Exec(ctx context.Context, c config.Config, onLine func(string), args ...string) (runner.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, err := f.session(ctx, c.Repository)
	if err != nil {
		return runner.Result{ExitCode: 1}, err
	}
	switch {
	case args[0] == "backup":
		f.seenBackup = append(f.seenBackup, runner.PasswordFrom(ctx))
		if onLine != nil {
			onLine("saved")
		}
	case args[0] == "key" && args[1] == "add":
		if f.failAddOn == c.Repository {
			return runner.Result{ExitCode: 1}, errors.New("repository is read-only")
		}
		if args[2] != "--new-password-file" || args[3] != runner.NewPasswordFile {
			return runner.Result{}, fmt.Errorf("unexpected arguments %v", args)
		}
		f.next++
		f.keys[c.Repository][fmt.Sprintf("added-%d", f.next)] = runner.NewPasswordFrom(ctx)
		f.events = append(f.events, "add "+c.Repository)
	case args[0] == "key" && args[1] == "remove":
		if f.failRemove {
			return runner.Result{ExitCode: 1}, errors.New("lock timeout")
		}
		if args[2] == cur {
			return runner.Result{ExitCode: 1}, errors.New("refusing to remove key currently used to access the repository")
		}
		delete(f.keys[c.Repository], args[2])
		f.events = append(f.events, "remove "+c.Repository)
	}
	return runner.Result{}, nil
}

func (f *fakeRestic) Output(ctx context.Context, c config.Config, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, err := f.session(ctx, c.Repository)
	if err != nil {
		return nil, err
	}
	if args[0] == "key" && args[1] == "list" {
		var list []map[string]any
		for id := range f.keys[c.Repository] {
			list = append(list, map[string]any{"id": id, "current": id == cur})
		}
		return json.Marshal(list)
	}
	return []byte("[]"), nil
}

func (f *fakeRestic) Preflight(context.Context, config.Config) error { return nil }

func (f *fakeRestic) opens(repo, pw string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.keys[repo] {
		if p == pw {
			return true
		}
	}
	return false
}

type secretMap map[string]string

func (s secretMap) Get(name string) (string, error) {
	if v, ok := s[name]; ok {
		return v, nil
	}
	return "", errors.New("secret not found: " + name)
}

// jobSpec is a job together with the repository it works on.
type jobSpec struct {
	job  config.Job
	repo config.Repository
}

// secretJob is a backup job on the repository at path repo, which takes its password from the secret.
func secretJob(name, repo, secret string) jobSpec {
	repoName := strings.ReplaceAll(strings.Trim(repo, "/"), "/", "-")
	return jobSpec{
		job:  config.Job{Name: name, Type: config.TypeBackup, Enabled: true, Schedule: "0 0 * * *", Repository: repoName, Source: "/src"},
		repo: config.Repository{Name: repoName, Path: repo, PasswordSecret: secret},
	}
}

func secretManager(t *testing.T, f *fakeRestic, secrets Secrets, specs ...jobSpec) *Manager {
	t.Helper()
	m := newManager(t, "fail")
	var repos []config.Repository
	var jobs []config.Job
	for _, s := range specs {
		jobs = append(jobs, s.job)
		if !slices.ContainsFunc(repos, func(r config.Repository) bool { return r.Name == s.repo.Name }) {
			repos = append(repos, s.repo)
		}
	}
	c := portalOf(m.cfg(), repos...)
	c.Jobs = jobs
	m.cfg = func() config.Config { return c }
	m.local = f
	m.SetSecrets(secrets)
	t.Cleanup(func() { m.cancel(); m.wg.Wait() })
	return m
}

func TestAJobRunsWithThePasswordOfItsSecret(t *testing.T) {
	f := newFakeRestic(map[string]string{"/r1": "pw-one"})
	m := secretManager(t, f, secretMap{"one": "pw-one"}, secretJob("backup", "/r1", "one"))
	run, err := m.Trigger("backup", "manual", true)
	if err != nil || run.Status != store.StatusSuccess {
		t.Fatalf("run = %+v, %v", run, err)
	}
	if len(f.seenBackup) != 1 || f.seenBackup[0] != "pw-one" {
		t.Errorf("restic ran with %v", f.seenBackup)
	}
}

func TestAMissingSecretFailsTheRunWithAReason(t *testing.T) {
	f := newFakeRestic(map[string]string{"/r1": "pw-one"})
	m := secretManager(t, f, secretMap{}, secretJob("backup", "/r1", "gone"))
	run, _ := m.Trigger("backup", "manual", true)
	if run.Status != store.StatusFailed || !strings.Contains(run.Error, "gone") {
		t.Fatalf("run = %+v", run)
	}
	if ok, _ := m.Health(); ok {
		t.Error("a missing secret must make the daemon unhealthy")
	}
}

func TestWithoutASecretStoreASecretJobFails(t *testing.T) {
	f := newFakeRestic(map[string]string{"/r1": "pw-one"})
	m := secretManager(t, f, nil, secretJob("backup", "/r1", "one"))
	m.SetSecrets(nil)
	run, _ := m.Trigger("backup", "manual", true)
	if run.Status != store.StatusFailed || !strings.Contains(run.Error, "no secret store") {
		t.Fatalf("run = %+v", run)
	}
}

func TestSecretUsers(t *testing.T) {
	m := secretManager(t, newFakeRestic(nil), secretMap{}, secretJob("a", "/r1", "s"), secretJob("b", "/r2", "s"), secretJob("c", "/r3", "other"))
	repos, others := m.SecretUsers("s")
	if len(repos) != 2 || repos[0] != "r1" || repos[1] != "r2" || len(others) != 0 {
		t.Errorf("users = %v, %v", repos, others)
	}
	c := m.cfg()
	c.Peers = []config.Peer{{Name: "west", PasswordSecret: "s"}}
	c.SyncServe.PasswordSecret = "s"
	m.cfg = func() config.Config { return c }
	if _, others := m.SecretUsers("s"); len(others) != 2 || others[0] != "peer:west" || others[1] != "syncserve" {
		t.Errorf("peers and the sync server must be reported: %v", others)
	}
}

func TestChangingThePasswordOfAllRepositories(t *testing.T) {
	f := newFakeRestic(map[string]string{"/r1": "old", "/r2": "old"})
	// Two jobs share /r1, it must be changed once.
	m := secretManager(t, f, secretMap{"s": "old"}, secretJob("a", "/r1", "s"), secretJob("a2", "/r1", "s"), secretJob("b", "/r2", "s"))
	var saved []string
	res, err := m.ChangePassword(context.Background(), "s", "old", "new", func() error {
		saved = append(saved, strings.Join(f.events, ","))
		return nil
	})
	if err != nil || len(res.Warnings) != 0 || res.Repositories != 2 {
		t.Fatalf("change = %+v, %v", res, err)
	}
	for _, repo := range []string{"/r1", "/r2"} {
		if !f.opens(repo, "new") || f.opens(repo, "old") {
			t.Errorf("%s: the new password must open it and the old one not", repo)
		}
		if len(f.keys[repo]) != 1 {
			t.Errorf("%s keeps %d keys", repo, len(f.keys[repo]))
		}
	}
	// The value is stored after the new keys exist and before the old ones go.
	if len(saved) != 1 || strings.Count(saved[0], "add") != 2 || strings.Contains(saved[0], "remove") {
		t.Errorf("save ran after %v", saved)
	}
}

func TestNothingChangesWhenARepositoryDoesNotOpen(t *testing.T) {
	f := newFakeRestic(map[string]string{"/r1": "old", "/r2": "someone changed it"})
	m := secretManager(t, f, secretMap{"s": "old"}, secretJob("a", "/r1", "s"), secretJob("b", "/r2", "s"))
	saved := false
	_, err := m.ChangePassword(context.Background(), "s", "old", "new", func() error { saved = true; return nil })
	if err == nil || !strings.Contains(err.Error(), "/r2") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("err = %v", err)
	}
	if saved || f.opens("/r1", "new") || len(f.events) != 0 {
		t.Errorf("saved=%v events=%v", saved, f.events)
	}
}

func TestARefusingRepositoryRollsTheOthersBack(t *testing.T) {
	f := newFakeRestic(map[string]string{"/r1": "old", "/r2": "old"})
	f.failAddOn = "/r2"
	m := secretManager(t, f, secretMap{"s": "old"}, secretJob("a", "/r1", "s"), secretJob("b", "/r2", "s"))
	saved := false
	_, err := m.ChangePassword(context.Background(), "s", "old", "new", func() error { saved = true; return nil })
	if err == nil || !strings.Contains(err.Error(), "/r2") {
		t.Fatalf("err = %v", err)
	}
	if saved {
		t.Error("the secret must not be saved")
	}
	for _, repo := range []string{"/r1", "/r2"} {
		if !f.opens(repo, "old") || f.opens(repo, "new") || len(f.keys[repo]) != 1 {
			t.Errorf("%s must be as before: %v", repo, f.keys[repo])
		}
	}
}

func TestAFailingSaveRollsEverythingBack(t *testing.T) {
	f := newFakeRestic(map[string]string{"/r1": "old"})
	m := secretManager(t, f, secretMap{"s": "old"}, secretJob("a", "/r1", "s"))
	_, err := m.ChangePassword(context.Background(), "s", "old", "new", func() error { return errors.New("disk full") })
	if err == nil || !strings.Contains(err.Error(), "old password is unchanged") {
		t.Fatalf("err = %v", err)
	}
	if !f.opens("/r1", "old") || f.opens("/r1", "new") {
		t.Errorf("keys = %v", f.keys["/r1"])
	}
}

func TestAnOldKeyThatStaysIsAWarningNotAFailure(t *testing.T) {
	f := newFakeRestic(map[string]string{"/r1": "old"})
	f.failRemove = true
	m := secretManager(t, f, secretMap{"s": "old"}, secretJob("a", "/r1", "s"))
	saved := false
	res, err := m.ChangePassword(context.Background(), "s", "old", "new", func() error { saved = true; return nil })
	if err != nil || !saved || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "key-/r1") {
		t.Fatalf("result = %+v, err = %v, saved = %v", res, err, saved)
	}
	if !f.opens("/r1", "new") {
		t.Error("the new password must work")
	}
}

func TestChangingASecretNobodyUses(t *testing.T) {
	m := secretManager(t, newFakeRestic(nil), secretMap{"s": "x"}, secretJob("a", "/r1", "other"))
	if _, err := m.ChangePassword(context.Background(), "s", "x", "y", func() error { return nil }); err == nil {
		t.Fatal("expected an error")
	}
}
