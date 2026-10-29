package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Valentin-Kaiser/backup/pkg/config"
	"github.com/Valentin-Kaiser/backup/pkg/runner"
	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/logging/log"
)

// Secrets provides the values of the stored secrets.
type Secrets interface {
	Get(name string) (string, error)
}

// SetSecrets enables jobs to take their repository password from a stored secret.
func (m *Manager) SetSecrets(s Secrets) { m.secrets = s }

// secretExec gives every restic call of its executor the repository password of a secret.
type secretExec struct {
	runner.Executor
	password string
}

func (s secretExec) Exec(ctx context.Context, c config.Config, onLine func(string), args ...string) (runner.Result, error) {
	return s.Executor.Exec(runner.WithPassword(ctx, s.password), c, onLine, args...)
}

func (s secretExec) Output(ctx context.Context, c config.Config, args ...string) ([]byte, error) {
	return s.Executor.Output(runner.WithPassword(ctx, s.password), c, args...)
}

// withSecret wraps ex when the configuration takes its password from a secret. A secret that cannot be read
// makes the executor fail.
func (m *Manager) withSecret(ex runner.Executor, c config.Config) runner.Executor {
	if c.PasswordSecret == "" {
		return ex
	}
	if m.secrets == nil {
		return unavailable{apperror.NewErrorf("the password secret %s is configured but this daemon has no secret store", c.PasswordSecret).AddError(runner.ErrUnavailable)}
	}
	pw, err := m.secrets.Get(c.PasswordSecret)
	if err != nil {
		return unavailable{apperror.NewErrorf("the password secret %s cannot be read: %v", c.PasswordSecret, err).AddError(runner.ErrUnavailable)}
	}
	return secretExec{Executor: ex, password: pw}
}

// SecretUsers returns the names of the repositories that take their password from a secret and the other users of
// it: peers (peer:<name>) and the sync server.
func (m *Manager) SecretUsers(name string) (repositories, others []string) {
	c := m.cfg()
	for _, r := range c.Repositories {
		if r.PasswordSecret == name {
			repositories = append(repositories, r.Name)
		}
	}
	for _, p := range c.Peers {
		if p.PasswordSecret == name || p.APIPasswordSecret == name {
			others = append(others, "peer:"+p.Name)
		}
	}
	if c.SyncServe.PasswordSecret == name {
		others = append(others, "syncserve")
	}
	return repositories, others
}

// secretRepositories returns one configuration for every repository that uses the secret.
func (m *Manager) secretRepositories(name string) []config.Config {
	c := m.cfg()
	found := map[string]config.Config{}
	for _, r := range c.Repositories {
		if r.PasswordSecret == name {
			jc := c.ForRepository(r)
			found[jc.Repository] = jc
		}
	}
	out := make([]config.Config, 0, len(found))
	for _, jc := range found {
		out = append(out, jc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repository < out[j].Repository })
	return out
}

// keyIDs reads the id of the key the current password opens, which is the one to remove afterwards.
func (m *Manager) currentKey(ctx context.Context, ex runner.Executor, c config.Config) (string, error) {
	out, err := ex.Output(ctx, c, "key", "list", "--json")
	if err != nil {
		return "", err
	}
	var keys []struct {
		ID      string `json:"id"`
		Current bool   `json:"current"`
	}
	if err := json.Unmarshal(out, &keys); err != nil {
		return "", apperror.NewError("restic returned an unexpected key list").AddError(err)
	}
	for _, k := range keys {
		if k.Current && k.ID != "" && !strings.HasPrefix(k.ID, "-") {
			return k.ID, nil
		}
	}
	return "", apperror.NewError("restic did not say which key is in use")
}

// PasswordChange is the outcome of ChangePassword.
type PasswordChange struct {
	// Repositories is the number of repositories that have the new password now.
	Repositories int
	Warnings     []string
}

// pwTarget is a repository whose password is being changed.
type pwTarget struct {
	c     config.Config
	ex    runner.Executor
	oldID string
	added bool
}

// ChangePassword makes newPassword the password of every repository that uses the secret name, then calls save to
// store it. It does not replace the old password in one step: it adds a key for the new password, saves, and only
// then removes the key of the old one. At any point at least one of the two passwords opens each repository and the
// stored secret is the one that is known to work, even if the daemon dies half way.
//
// The warnings of the result are non-fatal, e.g. an old key that could not be removed.
func (m *Manager) ChangePassword(ctx context.Context, name, oldPassword, newPassword string, save func() error) (PasswordChange, error) {
	repos := m.secretRepositories(name)
	if len(repos) == 0 {
		return PasswordChange{}, apperror.NewErrorf("no repository uses the secret %s, there is nothing to change", name)
	}

	// Nothing else may touch these repositories meanwhile.
	for _, c := range repos {
		sem := m.sem(c.Repository)
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		case <-ctx.Done():
			return PasswordChange{}, apperror.Wrap(ctx.Err())
		}
	}

	oldCtx := runner.WithPassword(ctx, oldPassword)
	newCtx := runner.WithNewPassword(oldCtx, newPassword)

	// Check everything before changing anything.
	targets := make([]*pwTarget, 0, len(repos))
	for _, c := range repos {
		ex := m.ex(withoutSecret(c))
		if err := ex.Preflight(ctx, c); err != nil {
			return PasswordChange{}, apperror.NewErrorf("repository %s: %v, nothing was changed", c.Repository, err)
		}
		if _, err := ex.Exec(oldCtx, c, nil, "cat", "config"); err != nil {
			return PasswordChange{}, apperror.NewErrorf("repository %s does not open with the stored password, nothing was changed", c.Repository).AddError(err)
		}
		id, err := m.currentKey(oldCtx, ex, c)
		if err != nil {
			return PasswordChange{}, apperror.NewErrorf("repository %s: %v, nothing was changed", c.Repository, err)
		}
		targets = append(targets, &pwTarget{c: c, ex: ex, oldID: id})
	}

	// Add the new password everywhere. If one repository refuses, take the new key out of the others again.
	for _, t := range targets {
		if _, err := t.ex.Exec(newCtx, t.c, nil, "key", "add", "--new-password-file", runner.NewPasswordFile); err != nil {
			m.rollbackKeys(ctx, targets, oldPassword, newPassword)
			return PasswordChange{}, apperror.NewErrorf("adding the new password to %s failed, the old password is unchanged", t.c.Repository).AddError(err)
		}
		t.added = true
	}

	if err := save(); err != nil {
		m.rollbackKeys(ctx, targets, oldPassword, newPassword)
		return PasswordChange{}, apperror.NewError("saving the new password failed, the old password is unchanged").AddError(err)
	}

	// The new password is stored and works, retire the old one.
	var warnings []string
	nctx := runner.WithPassword(ctx, newPassword)
	for _, t := range targets {
		if _, err := t.ex.Exec(nctx, t.c, nil, "key", "remove", t.oldID); err != nil {
			log.Warn().Err(err).Msgf("the old key of %s could not be removed", t.c.Repository)
			warnings = append(warnings, fmt.Sprintf("the old password still opens %s, remove key %s with restic", t.c.Repository, t.oldID))
		}
	}
	return PasswordChange{Repositories: len(targets), Warnings: warnings}, nil
}

// withoutSecret returns c with the secret ignored, the caller supplies the password through the context.
func withoutSecret(c config.Config) config.Config {
	c.PasswordSecret = ""
	return c
}

// rollbackKeys removes the keys of the new password that were added, using the old password. It is best effort: a
// key that stays behind is harmless, it only opens the repository with a password nobody uses.
func (m *Manager) rollbackKeys(ctx context.Context, targets []*pwTarget, oldPassword, newPassword string) {
	// The ids are only known with the new password, which opens the repository now.
	newCtx := runner.WithPassword(ctx, newPassword)
	oldCtx := runner.WithPassword(ctx, oldPassword)
	for _, t := range targets {
		if !t.added {
			continue
		}
		id, err := m.currentKey(newCtx, t.ex, t.c)
		if err == nil {
			_, err = t.ex.Exec(oldCtx, t.c, nil, "key", "remove", id)
		}
		if err != nil {
			log.Warn().Err(err).Msgf("the new key of %s could not be removed again", t.c.Repository)
		}
	}
}

// readPassword reads a password file.
func readPassword(file string) (string, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return "", apperror.NewErrorf("password file %s is not readable", file).AddError(err)
	}
	return strings.TrimSpace(string(b)), nil
}
