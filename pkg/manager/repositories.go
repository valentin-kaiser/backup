package manager

import (
	"slices"

	"github.com/Valentin-Kaiser/backup/pkg/config"
	"github.com/valentin-kaiser/go-core/apperror"
)

// ErrNoRepositories is returned when repositories are managed in standalone mode, which has the one implicit one.
var ErrNoRepositories = apperror.NewError("repositories can only be managed in portal mode, standalone mode has the one repository of its configuration")

// Repositories returns the repositories sorted by name. Standalone mode has the one implicit repository.
func (m *Manager) Repositories() []config.Repository {
	out := slices.Clone(m.cfg().Repos())
	slices.SortFunc(out, func(a, b config.Repository) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		}
		return 0
	})
	return out
}

// JobsOfRepository returns the names of the restic jobs that work on a repository.
func (m *Manager) JobsOfRepository(name string) []string { return m.cfg().JobsOf(name) }

// SetRepository creates a repository or replaces its settings. The jobs that use it follow at once. Changing the path
// points them at another repository, it does not move any data.
func (m *Manager) SetRepository(r config.Repository) error {
	if !m.cfg().Portal() {
		return ErrNoRepositories
	}
	// The change reloads the configuration, which also creates a repository that is new or was moved.
	return m.change(func(c *config.Config) error {
		c.Repositories = slices.Clone(c.Repositories)
		for i := range c.Repositories {
			if c.Repositories[i].Name == r.Name {
				c.Repositories[i] = r
				return nil
			}
		}
		c.Repositories = append(c.Repositories, r)
		return nil
	})
}

// DeleteRepository removes a repository that no job uses from the configuration. Its data stays where it is.
func (m *Manager) DeleteRepository(name string) error {
	if !m.cfg().Portal() {
		return ErrNoRepositories
	}
	return m.change(func(c *config.Config) error {
		if jobs := c.JobsOf(name); len(jobs) > 0 {
			return apperror.NewErrorf("repository %s is used by %d job(s): %v", name, len(jobs), jobs)
		}
		c.Repositories = slices.Clone(c.Repositories)
		for i := range c.Repositories {
			if c.Repositories[i].Name == name {
				c.Repositories = slices.Delete(c.Repositories, i, i+1)
				return nil
			}
		}
		return apperror.NewErrorf("unknown repository %q", name)
	})
}
