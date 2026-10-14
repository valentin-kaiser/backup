package api

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/config"
	"github.com/Valentin-Kaiser/backup/pkg/manager"
	pb "github.com/Valentin-Kaiser/backup/pkg/proto/api/v1"
	"github.com/Valentin-Kaiser/backup/pkg/runner"
	"github.com/Valentin-Kaiser/backup/pkg/secrets"
	"github.com/Valentin-Kaiser/backup/pkg/secure"
	"github.com/Valentin-Kaiser/backup/pkg/store"
)

// repoRestic is a restic repository with keys: a password opens it when some key has it.
type repoRestic struct {
	mu   sync.Mutex
	keys map[string]string // id -> password
	n    int
}

func (r *repoRestic) session(ctx context.Context) (string, error) {
	for id, p := range r.keys {
		if p == runner.PasswordFrom(ctx) && p != "" {
			return id, nil
		}
	}
	return "", errors.New("wrong password")
}

func (r *repoRestic) Exec(ctx context.Context, _ config.Config, _ func(string), args ...string) (runner.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, err := r.session(ctx)
	if err != nil {
		return runner.Result{ExitCode: 1}, err
	}
	if args[0] == "key" && args[1] == "add" {
		r.n++
		r.keys[string(rune('a'+r.n))] = runner.NewPasswordFrom(ctx)
	}
	if args[0] == "key" && args[1] == "remove" && args[2] != cur {
		delete(r.keys, args[2])
	}
	return runner.Result{}, nil
}

func (r *repoRestic) Output(ctx context.Context, _ config.Config, _ ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, err := r.session(ctx)
	if err != nil {
		return nil, err
	}
	var list []map[string]any
	for id := range r.keys {
		list = append(list, map[string]any{"id": id, "current": id == cur})
	}
	return json.Marshal(list)
}

func (r *repoRestic) Preflight(context.Context, config.Config) error { return nil }

func (r *repoRestic) opens(pw string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Contains(valuesOf(r.keys), pw)
}

func valuesOf(m map[string]string) []string {
	var v []string
	for _, x := range m {
		v = append(v, x)
	}
	return v
}

func secretService(t *testing.T, repo *repoRestic) (*Service, *secrets.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "h.jsonl"), 100)
	if err != nil {
		t.Fatal(err)
	}
	c := *config.Default()
	c.Mode, c.PasswordFile = config.ModePortal, ""
	c.API.PasswordFile = "/run/secrets/portal_password"
	c.Source = "/src"
	c.Jobs = []config.Job{{Name: "photos", Type: config.TypeBackup, Enabled: true, Schedule: "0 1 * * *", Repository: "/repo", PasswordSecret: "photos"}}
	var mu sync.Mutex
	m := manager.New(func() config.Config { mu.Lock(); defer mu.Unlock(); return c }, repo, st)
	m.SetUpdater(func(change func(*config.Config) error) error {
		mu.Lock()
		n := c
		n.Jobs = slices.Clone(c.Jobs)
		err := change(&n)
		if err == nil {
			err = n.Validate()
		}
		if err == nil {
			c = n
		}
		mu.Unlock()
		if err != nil {
			return err
		}
		return m.Reload()
	})
	t.Cleanup(func() { m.Shutdown(time.Second) })
	if err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	key, _ := secure.GenerateKey()
	store, err := secrets.Open(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	m.SetSecrets(store)
	return New(m).WithSecrets(store), store
}

func TestSecretRPCsNeedASecretStore(t *testing.T) {
	s, _ := newService(t)
	if _, err := s.ListSecrets(context.Background(), &pb.ListSecretsRequest{}); err == nil {
		t.Error("list without a store must fail")
	}
	if _, err := s.SetSecret(context.Background(), &pb.SetSecretRequest{Name: "a", Value: "x"}); err == nil {
		t.Error("set without a store must fail")
	}
}

func TestSettingAndListingSecrets(t *testing.T) {
	s, store := secretService(t, &repoRestic{})
	ctx := context.Background()
	resp, err := s.SetSecret(ctx, &pb.SetSecretRequest{Name: "photos", Value: "pw-photos"})
	if err != nil || resp.GeneratedValue != "" || resp.Secret.Name != "photos" {
		t.Fatalf("set = %v, %v", resp, err)
	}
	if v, _ := store.Get("photos"); v != "pw-photos" {
		t.Errorf("stored %q", v)
	}
	list, err := s.ListSecrets(ctx, &pb.ListSecretsRequest{})
	if err != nil || len(list.Secrets) != 1 || len(list.Secrets[0].Jobs) != 1 || list.Secrets[0].Jobs[0] != "photos" {
		t.Fatalf("list = %v, %v", list, err)
	}
	for name, value := range map[string]string{"Bad Name": "x", "ok": "", "ok2": "two\nlines"} {
		if _, err := s.SetSecret(ctx, &pb.SetSecretRequest{Name: name, Value: value}); err == nil {
			t.Errorf("%q = %q must be refused", name, value)
		}
	}
}

func TestAGeneratedSecretIsReturnedOnce(t *testing.T) {
	s, store := secretService(t, &repoRestic{})
	resp, err := s.SetSecret(context.Background(), &pb.SetSecretRequest{Name: "fresh", Generate: true, Value: "ignored"})
	if err != nil || len(resp.GeneratedValue) < 40 {
		t.Fatalf("set = %v, %v", resp, err)
	}
	if v, _ := store.Get("fresh"); v != resp.GeneratedValue {
		t.Error("the returned value must be the stored one")
	}
	other, _ := s.SetSecret(context.Background(), &pb.SetSecretRequest{Name: "fresh2", Generate: true})
	if other.GeneratedValue == resp.GeneratedValue {
		t.Error("generated values must differ")
	}
}

func TestASecretInUseCannotBeDeleted(t *testing.T) {
	s, store := secretService(t, &repoRestic{})
	_, _ = store.Set("photos", "pw")
	_, _ = store.Set("spare", "pw")
	ctx := context.Background()
	if _, err := s.DeleteSecret(ctx, &pb.SecretRequest{Name: "photos"}); err == nil || !strings.Contains(err.Error(), "photos") {
		t.Fatalf("err = %v", err)
	}
	if !store.Has("photos") {
		t.Error("the secret must still be there")
	}
	if _, err := s.DeleteSecret(ctx, &pb.SecretRequest{Name: "spare"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteSecret(ctx, &pb.SecretRequest{Name: "spare"}); err == nil {
		t.Error("deleting twice must fail")
	}
}

func TestJobsCanOnlyUseSecretsThatExist(t *testing.T) {
	s, store := secretService(t, &repoRestic{})
	def := &pb.JobDefinition{Name: "docs", Type: "backup", Enabled: true, Schedule: "0 3 * * *", PasswordSecret: "docs"}
	if _, err := s.CreateJob(context.Background(), def); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v", err)
	}
	_, _ = store.Set("docs", "pw")
	if _, err := s.CreateJob(context.Background(), def); err != nil {
		t.Fatalf("with the secret: %v", err)
	}
	def.PasswordSecret = "typo"
	if _, err := s.UpdateJob(context.Background(), def); err == nil {
		t.Error("an update with an unknown secret must fail")
	}
}

func TestChangingTheRepositoryPassword(t *testing.T) {
	repo := &repoRestic{keys: map[string]string{"k0": "old-pw"}}
	s, store := secretService(t, repo)
	_, _ = store.Set("photos", "old-pw")

	resp, err := s.ChangeRepositoryPassword(context.Background(), &pb.ChangeRepositoryPasswordRequest{Name: "photos", Value: "new-pw"})
	if err != nil || resp.Repositories != 1 || len(resp.Warnings) != 0 {
		t.Fatalf("change = %v, %v", resp, err)
	}
	if v, _ := store.Get("photos"); v != "new-pw" {
		t.Errorf("stored %q", v)
	}
	if !repo.opens("new-pw") || repo.opens("old-pw") {
		t.Errorf("keys = %v", repo.keys)
	}

	gen, err := s.ChangeRepositoryPassword(context.Background(), &pb.ChangeRepositoryPasswordRequest{Name: "photos", Generate: true})
	if err != nil || len(gen.GeneratedValue) < 40 {
		t.Fatalf("generate = %v, %v", gen, err)
	}
	if v, _ := store.Get("photos"); v != gen.GeneratedValue || !repo.opens(gen.GeneratedValue) {
		t.Error("the generated password must be stored and set")
	}
}

func TestAFailedChangeLeavesTheStoredPasswordAlone(t *testing.T) {
	repo := &repoRestic{keys: map[string]string{"k0": "someone changed it"}}
	s, store := secretService(t, repo)
	_, _ = store.Set("photos", "old-pw")
	_, err := s.ChangeRepositoryPassword(context.Background(), &pb.ChangeRepositoryPasswordRequest{Name: "photos", Value: "new-pw"})
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("err = %v", err)
	}
	if v, _ := store.Get("photos"); v != "old-pw" {
		t.Errorf("stored %q", v)
	}
	if repo.opens("new-pw") {
		t.Error("the repository must be unchanged")
	}
}

func TestChangeRepositoryPasswordValidation(t *testing.T) {
	repo := &repoRestic{keys: map[string]string{"k0": "old-pw"}}
	s, store := secretService(t, repo)
	_, _ = store.Set("photos", "old-pw")
	ctx := context.Background()
	for name, req := range map[string]*pb.ChangeRepositoryPasswordRequest{
		"unknown secret": {Name: "ghost", Value: "x"},
		"same password":  {Name: "photos", Value: "old-pw"},
		"empty":          {Name: "photos", Value: ""},
		"newline":        {Name: "photos", Value: "a\nb"},
	} {
		if _, err := s.ChangeRepositoryPassword(ctx, req); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if !repo.opens("old-pw") || len(repo.keys) != 1 {
		t.Errorf("nothing may have changed: %v", repo.keys)
	}
}
