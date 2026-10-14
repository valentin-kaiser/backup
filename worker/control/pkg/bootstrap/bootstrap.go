// Package bootstrap holds the start-up steps of the control daemon that are not part of its main loop.
package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Valentin-Kaiser/backup/pkg/config"
	"github.com/Valentin-Kaiser/backup/pkg/manager"
	"github.com/Valentin-Kaiser/backup/pkg/store"
	"github.com/valentin-kaiser/go-core/flag"
)

// RunOnce runs the standalone job of the given type, waits for it and returns the exit code of the process.
func RunOnce(m *manager.Manager, jobType string) int {
	c := config.Get()
	if c.Portal() {
		fmt.Fprintln(os.Stderr, "error: once is a standalone command, in portal mode start jobs with the portal or 'control run'")
		return 2
	}
	for _, j := range c.Jobs {
		if j.Type != jobType && j.Name != jobType {
			continue
		}
		run, err := m.Trigger(j.Name, "once", true)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		if run.Status != store.StatusSuccess {
			fmt.Fprintf(os.Stderr, "%s %s: %s\n", j.Name, run.Status, run.Error)
			if run.ExitCode > 0 {
				return run.ExitCode
			}
			return 1
		}
		return 0
	}
	fmt.Fprintf(os.Stderr, "error: no %q job configured, use backup, forget, check, sync or a job name\n", jobType)
	return 2
}

// MasterKeyPath is where the key that encrypts the stored secrets lives. Point master-key-file at a Docker secret to
// keep the key apart from the secrets.
func MasterKeyPath(c config.Config) string {
	if c.MasterKeyFile != "" {
		return c.MasterKeyFile
	}
	return filepath.Join(flag.Path, "master.key")
}
