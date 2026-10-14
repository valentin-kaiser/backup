// Package syncserve exports a directory of this worker to its peers over WebDAV, so a sync job of another worker
// can use it as second path. It supervises an rclone serve process.
package syncserve

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/config"
	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/logging/log"
)

// Server runs and restarts the rclone sync server.
type Server struct {
	// Binary is the rclone executable, "rclone" when empty.
	Binary string
	// Password returns the password of the configuration, the password file is read when nil. It allows stored secrets.
	Password func(config.SyncServe) (string, error)

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// Args builds the argument vector of the sync server. The password is not part of it, see Env.
func Args(s config.SyncServe) []string {
	args := []string{
		"serve", "webdav", s.Path,
		"--addr", s.Address + ":" + strconv.Itoa(s.Port),
		"--user", s.Username,
		"--vfs-cache-mode", "off",
		"--read-only=false",
	}
	if s.CertificateFile != "" {
		args = append(args, "--cert", s.CertificateFile, "--key", s.KeyFile)
	}
	return args
}

// Env returns the environment that hands the password to rclone, command lines are visible to every process.
func Env(s config.SyncServe) ([]string, error) {
	pw, err := os.ReadFile(s.PasswordFile)
	if err != nil {
		return nil, apperror.NewErrorf("sync server password file %s is not readable", s.PasswordFile).AddError(err)
	}
	return []string{"RCLONE_PASS=" + strings.TrimSpace(string(pw))}, nil
}

// Start starts the sync server when the configuration enables it. It is safe to call again after the configuration
// changed, the running server is replaced.
func (s *Server) Start(ctx context.Context, c config.SyncServe) {
	s.Stop()
	if !c.Enabled {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.mu.Lock()
	s.cancel, s.done = cancel, done
	s.mu.Unlock()
	go func() {
		defer close(done)
		s.run(ctx, c)
	}()
}

// Stop stops the sync server and waits for it.
func (s *Server) Stop() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel, s.done = nil, nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// env returns the environment of the server process.
func (s *Server) env(c config.SyncServe) ([]string, error) {
	if s.Password == nil {
		return Env(c)
	}
	pw, err := s.Password(c)
	if err != nil {
		return nil, err
	}
	return []string{"RCLONE_PASS=" + pw}, nil
}

func (s *Server) binary() string {
	if s.Binary != "" {
		return s.Binary
	}
	return "rclone"
}

// run keeps the server up until ctx ends, with a pause between restarts so a broken setup does not spin.
func (s *Server) run(ctx context.Context, c config.SyncServe) {
	for ctx.Err() == nil {
		env, err := s.env(c)
		if err != nil {
			log.Error().Err(err).Msg("sync server not started")
		} else {
			cmd := exec.CommandContext(ctx, s.binary(), Args(c)...)
			cmd.Env = append(os.Environ(), env...)
			cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
			cmd.WaitDelay = 10 * time.Second
			cmd.Stdout, cmd.Stderr = logWriter{}, logWriter{}
			log.Info().Msgf("serving %s to peers on %s:%d", c.Path, c.Address, c.Port)
			if err := cmd.Run(); err != nil && ctx.Err() == nil {
				log.Error().Err(err).Msg("sync server stopped")
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
	}
}

// logWriter forwards the output of rclone to the log.
type logWriter struct{}

func (logWriter) Write(p []byte) (int, error) {
	if l := strings.TrimSpace(string(p)); l != "" {
		log.Info().Msg("sync server: " + l)
	}
	return len(p), nil
}
