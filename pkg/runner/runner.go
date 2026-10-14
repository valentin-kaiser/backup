// Package runner executes restic commands without a shell and streams their output.
package runner

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// ErrUnavailable is wrapped by errors of an executor that cannot run restic, as opposed to restic failing.
var ErrUnavailable = apperror.NewError("restic unavailable")

// Executor runs restic. Runner does it in this process, the wrappers add to the calls (e.g. a stored password).
type Executor interface {
	// Exec runs restic and calls onLine for every line of its output.
	Exec(ctx context.Context, c config.Config, onLine func(string), args ...string) (Result, error)
	// Output runs restic and returns its stdout.
	Output(ctx context.Context, c config.Config, args ...string) ([]byte, error)
	// Preflight verifies the mount guard of the repository on the machine that runs restic.
	Preflight(ctx context.Context, c config.Config) error
}

var _ Executor = Runner{}

// Preflight verifies the repository mount guard, it fails while the repository is not mounted.
func (r Runner) Preflight(_ context.Context, c config.Config) error {
	if c.MountCheck.Path == "" {
		return nil
	}
	if _, err := os.Stat(c.MountCheck.Path); err != nil {
		return apperror.NewErrorf("repository path %s is not available", c.MountCheck.Path).AddError(err)
	}
	if c.MountCheck.MarkerFile != "" {
		marker := c.MountCheck.MarkerFile
		if !filepath.IsAbs(marker) {
			marker = filepath.Join(c.MountCheck.Path, marker)
		}
		if _, err := os.Stat(marker); err != nil {
			return apperror.NewErrorf("mount marker %s not found, is the repository mounted?", marker).AddError(err)
		}
	}
	return nil
}

// Runner starts restic processes.
type Runner struct {
	// Binary is the restic executable, "restic" when empty.
	Binary string
	// Prefix is inserted before the restic arguments (used by tests).
	Prefix []string
	// SyncBinary is the rclone executable of sync jobs, "rclone" when empty.
	SyncBinary string
	// SyncPrefix is inserted before the rclone arguments (used by tests).
	SyncPrefix []string
	// UnisonBinary is the unison executable, "unison" when empty.
	UnisonBinary string
	// UnisonPrefix is inserted before the unison arguments (used by tests).
	UnisonPrefix []string
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
	case config.TypeSync:
		if job.Unison() {
			return unisonArgs(c, job)
		}
		return syncArgs(c, job)
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

type passwordKey struct{}
type newPasswordKey struct{}

// NewPasswordFile is replaced in the arguments by the path of a private file holding the new password, see
// WithNewPassword. It is used for `restic key passwd --new-password-file`.
const NewPasswordFile = "{{new-password-file}}"

// WithPassword makes the runner use password as the repository password, instead of the password file of the
// configuration. The password is handed to restic in a private temporary file that is removed afterwards. It travels
// in the context and not in the configuration, which is written to disk.
func WithPassword(ctx context.Context, password string) context.Context {
	return context.WithValue(ctx, passwordKey{}, password)
}

// WithNewPassword provides the password that replaces NewPasswordFile in the arguments.
func WithNewPassword(ctx context.Context, password string) context.Context {
	return context.WithValue(ctx, newPasswordKey{}, password)
}

// PasswordFrom returns the password of WithPassword.
func PasswordFrom(ctx context.Context) string { s, _ := ctx.Value(passwordKey{}).(string); return s }

// NewPasswordFrom returns the password of WithNewPassword.
func NewPasswordFrom(ctx context.Context) string {
	s, _ := ctx.Value(newPasswordKey{}).(string)
	return s
}

// prepare builds the command like Command and, when the context carries passwords, writes them to files only the
// current user can read. The returned function removes them, call it after the process ended.
func (r Runner) prepare(ctx context.Context, c config.Config, args []string) (*exec.Cmd, func(), error) {
	switch firstOr(args, "") {
	case SyncCommand:
		cmd, err := r.syncCommand(ctx, c, args)
		return cmd, func() {}, err
	case UnisonCommand:
		cmd, err := r.unisonCommand(ctx, c, args)
		return cmd, func() {}, err
	}
	pw, newPw := PasswordFrom(ctx), NewPasswordFrom(ctx)
	if pw == "" && newPw == "" {
		return r.Command(ctx, c, args...), func() {}, nil
	}
	dir, err := os.MkdirTemp("", "restic-secret-")
	if err != nil {
		return nil, nil, apperror.Wrap(err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	write := func(name, value string) (string, error) {
		p := filepath.Join(dir, name)
		return p, os.WriteFile(p, []byte(value), 0o600)
	}
	var pwFile string
	if pw != "" {
		if pwFile, err = write("password", pw); err != nil {
			cleanup()
			return nil, nil, apperror.Wrap(err)
		}
	}
	full := slices.Clone(args)
	for i, a := range full {
		if a != NewPasswordFile {
			continue
		}
		if newPw == "" {
			cleanup()
			return nil, nil, apperror.NewError("the arguments need a new password and none was given")
		}
		p, err := write("new-password", newPw)
		if err != nil {
			cleanup()
			return nil, nil, apperror.Wrap(err)
		}
		full[i] = p
	}
	cmd := r.Command(ctx, c, full...)
	if pwFile != "" {
		// The last value of a duplicate wins, this replaces the password file of the configuration.
		cmd.Env = append(cmd.Env, "RESTIC_PASSWORD_FILE="+pwFile)
	}
	return cmd, cleanup, nil
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
	r.finishCommand(cmd)
	return cmd
}

// finishCommand makes the process stop with SIGTERM when its context ends.
func (r Runner) finishCommand(cmd *exec.Cmd) {
	// restic removes its repository lock on SIGTERM, a kill would leave it behind. rclone saves its state.
	cmd.Cancel = func() error {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 15 * time.Second
}

// lastRewrite returns what a terminal would show of a line that was redrawn with carriage returns, like the progress
// bar of unison: the last non-empty part.
func lastRewrite(l string) string {
	if !strings.Contains(l, "\r") {
		return l
	}
	parts := strings.Split(l, "\r")
	for i := len(parts) - 1; i >= 0; i-- {
		if strings.TrimSpace(parts[i]) != "" {
			return parts[i]
		}
	}
	return ""
}

// Exec runs restic with args and calls onLine for every line of stdout and stderr.
// A non-zero exit code is reported in the result together with an error.
func (r Runner) Exec(ctx context.Context, c config.Config, onLine func(string), args ...string) (Result, error) {
	cmd, cleanup, err := r.prepare(ctx, c, args)
	if err != nil {
		return Result{ExitCode: -1}, err
	}
	defer cleanup()
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
				onLine(lastRewrite(sc.Text()))
			}
		}
		_, _ = io.Copy(io.Discard, pr)
	}()

	err = cmd.Run()
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
		if firstOr(args, "") == UnisonCommand {
			if hint := unisonExitHint(res.ExitCode); hint != "" {
				return res, apperror.NewErrorf("unison: %s", hint).AddError(err)
			}
		}
		return res, apperror.NewErrorf("%s failed", r.name(args)).AddError(err)
	}
	return res, nil
}

// Output runs restic and returns its stdout. Stderr is returned as part of the error.
func (r Runner) Output(ctx context.Context, c config.Config, args ...string) ([]byte, error) {
	cmd, cleanup, err := r.prepare(ctx, c, args)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, apperror.NewErrorf("%s failed: %s", r.name(args), strings.TrimSpace(stderr.String())).AddError(err)
	}
	return out, nil
}

// name is the program and command of a call, for messages.
func (r Runner) name(args []string) string {
	switch firstOr(args, "") {
	case SyncCommand:
		return "rclone " + SyncCommand
	case UnisonCommand:
		return "unison"
	}
	return "restic " + firstOr(args, "")
}

func firstOr(s []string, d string) string {
	if len(s) > 0 {
		return s[0]
	}
	return d
}
