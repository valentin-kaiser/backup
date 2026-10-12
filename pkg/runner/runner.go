// Package runner executes restic commands without a shell and streams their output.
package runner

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/config"
	"github.com/valentin-kaiser/go-core/apperror"
)

// SplitArgs splits s like a POSIX shell would, honouring quotes and backslash
// escapes, without ever expanding variables or executing anything.
func SplitArgs(s string) ([]string, error) {
	var (
		args    []string
		cur     strings.Builder
		inArg   bool
		quote   rune
		escaped bool
	)
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped = true
			inArg = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			inArg = true
		case r == ' ' || r == '\t' || r == '\n':
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil, apperror.NewErrorf("unterminated quote in %q", s)
	}
	if escaped {
		return nil, apperror.NewErrorf("trailing backslash in %q", s)
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args, nil
}

// Runner starts restic processes.
type Runner struct {
	// Binary is the restic executable, "restic" when empty.
	Binary string
	// Prefix is inserted before the restic arguments (used by tests).
	Prefix []string
}

// Result is the outcome of a restic invocation.
type Result struct {
	ExitCode int
}

func (r Runner) binary() string {
	if r.Binary != "" {
		return r.Binary
	}
	return "restic"
}

// Args builds the restic argument vector of a job. c is the configuration with the overrides
// of the job applied, see config.Config.ForJob.
func Args(c config.Config, job config.Job) ([]string, error) {
	switch job.Type {
	case config.TypeBackup, config.TypeForget, config.TypeCheck:
	default:
		return nil, apperror.NewErrorf("unknown job type %q", job.Type)
	}
	user, err := SplitArgs(job.Args)
	if err != nil {
		return nil, err
	}
	args := []string{job.Type}
	if c.LockRetry != "" {
		args = append(args, "--retry-lock", c.LockRetry)
	}
	if c.PackSize > 0 {
		args = append(args, "--pack-size", strconv.Itoa(c.PackSize))
	}
	var extra []string
	if job.Type == config.TypeBackup {
		if c.ReadConcurrency > 0 {
			args = append(args, "--read-concurrency", strconv.Itoa(c.ReadConcurrency))
		}
		if c.OneFileSystem {
			args = append(args, "--one-file-system")
		}
		extra = []string{"--", c.Source}
	}
	args = append(args, user...)
	return append(args, extra...), nil
}

// Command builds an exec.Cmd for restic with the repository environment applied.
func (r Runner) Command(ctx context.Context, c config.Config, args ...string) *exec.Cmd {
	bin := r.binary()
	full := append(append([]string{}, r.Prefix...), args...)
	if c.Nice > 0 {
		full = append([]string{"-n", strconv.Itoa(c.Nice), bin}, full...)
		bin = "nice"
	}
	cmd := exec.CommandContext(ctx, bin, full...)
	cmd.Env = append(os.Environ(),
		"RESTIC_REPOSITORY="+c.Repository,
		"RESTIC_PASSWORD_FILE="+c.PasswordFile,
		"RESTIC_COMPRESSION="+c.Compression,
		"RESTIC_CACHE_DIR="+c.CacheDir,
	)
	// restic removes its repository lock on SIGTERM, a kill would leave it behind.
	cmd.Cancel = func() error {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 15 * time.Second
	return cmd
}

// Exec runs restic with args and calls onLine for every line of stdout and stderr.
// A non-zero exit code is reported in the result together with an error.
func (r Runner) Exec(ctx context.Context, c config.Config, onLine func(string), args ...string) (Result, error) {
	cmd := r.Command(ctx, c, args...)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			if onLine != nil {
				onLine(sc.Text())
			}
		}
		_, _ = io.Copy(io.Discard, pr)
	}()

	err := cmd.Run()
	_ = pw.Close()
	wg.Wait()

	res := Result{}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
		} else {
			res.ExitCode = -1
		}
		if ctx.Err() != nil {
			return res, apperror.Wrap(ctx.Err())
		}
		return res, apperror.NewErrorf("restic %s failed", firstOr(args, "")).AddError(err)
	}
	return res, nil
}

// Output runs restic and returns its stdout. Stderr is returned as part of the error.
func (r Runner) Output(ctx context.Context, c config.Config, args ...string) ([]byte, error) {
	cmd := r.Command(ctx, c, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, apperror.NewErrorf("restic %s failed: %s", firstOr(args, ""), strings.TrimSpace(stderr.String())).AddError(err)
	}
	return out, nil
}

func firstOr(s []string, d string) string {
	if len(s) > 0 {
		return s[0]
	}
	return d
}
