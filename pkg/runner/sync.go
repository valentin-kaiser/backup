package runner

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Valentin-Kaiser/backup/pkg/config"
	"github.com/valentin-kaiser/go-core/apperror"
)

// SyncCommand is the first argument of a sync run. Restic has no command of that name, so it tells the runner to start
// rclone instead.
const SyncCommand = "bisync"

// defaultMaxDelete is the share of files a sync run may delete before it aborts.
const defaultMaxDelete = 50

// PeerRemote returns the name of the rclone remote of a peer.
func PeerRemote(name string) string { return "peer_" + name }

// syncPath converts a path of a sync job to an rclone path: local paths stay as they are, peer:<name>/dir becomes
// peer_<name>:dir.
func syncPath(p string) string {
	rest, ok := strings.CutPrefix(p, config.PeerPrefix)
	if !ok {
		return p
	}
	name, dir, _ := strings.Cut(rest, "/")
	return PeerRemote(name) + ":" + strings.Trim(dir, "/")
}

// SyncStateDir is the directory in which rclone keeps the listings of a sync job.
func SyncStateDir(c config.Config, job string) string { return filepath.Join(c.SyncDir, job) }

// NeedsResync reports whether a sync job has no state yet. The first run has to build the listings of both sides with
// --resync, which also happens after the state was reset.
func NeedsResync(c config.Config, job string) bool {
	entries, err := os.ReadDir(SyncStateDir(c, job))
	if err != nil {
		return true
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".lst") {
			return false
		}
	}
	return true
}

// ResetSync removes the state of a sync job, the next run starts with --resync.
func ResetSync(c config.Config, job string) error {
	if err := os.RemoveAll(SyncStateDir(c, job)); err != nil {
		return apperror.Wrap(err)
	}
	return nil
}

func syncArgs(c config.Config, job config.Job) ([]string, error) {
	user, err := SplitArgs(job.Args)
	if err != nil {
		return nil, err
	}
	conflict, loser, maxDelete := job.Conflict, job.ConflictLoser, job.MaxDeletePct
	if conflict == "" {
		conflict = config.ConflictNewer
	}
	if loser == "" {
		loser = config.LoserNumber
	}
	if maxDelete == 0 {
		maxDelete = defaultMaxDelete
	}
	args := []string{
		SyncCommand, job.Path1, syncPath(job.Path2),
		"--workdir", SyncStateDir(c, job.Name),
		"--conflict-resolve", conflict,
		"--conflict-loser", loser,
		"--max-delete", strconv.Itoa(maxDelete),
		"--resilient", "--recover", "--create-empty-src-dirs",
	}
	if job.CheckAccess {
		args = append(args, "--check-access")
	}
	if job.Force {
		args = append(args, "--force")
	}
	if NeedsResync(c, job.Name) {
		args = append(args, "--resync")
	}
	return append(args, user...), nil
}

// syncBinary is the rclone executable.
func (r Runner) syncBinary() string {
	if r.SyncBinary != "" {
		return r.SyncBinary
	}
	return "rclone"
}

// obscure hides a password the way rclone wants it in the configuration.
func (r Runner) obscure(ctx context.Context, password string) (string, error) {
	cmd := exec.CommandContext(ctx, r.syncBinary(), append(append([]string{}, r.SyncPrefix...), "obscure", "-")...)
	cmd.Stdin = strings.NewReader(password)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", apperror.NewErrorf("rclone obscure failed: %s", strings.TrimSpace(stderr.String())).AddError(err)
	}
	return strings.TrimSpace(out.String()), nil
}

// peerEnv defines the rclone remotes of all peers in the environment, so no password ends up on a command line.
func (r Runner) peerEnv(ctx context.Context, c config.Config, args []string) ([]string, error) {
	var env []string
	for _, p := range c.Peers {
		if !usesPeer(args, p.Name) {
			continue
		}
		pw, err := peerPassword(ctx, p)
		if err != nil {
			return nil, err
		}
		obscured, err := r.obscure(ctx, pw)
		if err != nil {
			return nil, err
		}
		prefix := "RCLONE_CONFIG_" + strings.ToUpper(PeerRemote(p.Name)) + "_"
		env = append(env,
			prefix+"TYPE=webdav",
			prefix+"URL="+p.URL,
			prefix+"VENDOR=rclone",
			prefix+"USER="+p.Username,
			prefix+"PASS="+obscured,
		)
		if p.CAFile != "" {
			// rclone has no per remote CA, the global flag applies to all remotes.
			env = append(env, "RCLONE_CA_CERT="+p.CAFile)
		}
	}
	return env, nil
}

// syncCommand builds the rclone process of a sync run.
func (r Runner) syncCommand(ctx context.Context, c config.Config, args []string) (*exec.Cmd, error) {
	env, err := r.peerEnv(ctx, c, args)
	if err != nil {
		return nil, err
	}
	bin := r.syncBinary()
	full := append(append([]string{}, r.SyncPrefix...), args...)
	if c.Nice > 0 {
		full = append([]string{"-n", strconv.Itoa(c.Nice), bin}, full...)
		bin = "nice"
	}
	cmd := exec.CommandContext(ctx, bin, full...)
	cmd.Env = append(os.Environ(), env...)
	r.finishCommand(cmd)
	return cmd, nil
}

type peerPasswordsKey struct{}

// WithPeerPasswords hands the passwords of stored secrets to the runner, by peer name. A peer without an entry takes
// its password from the password file.
func WithPeerPasswords(ctx context.Context, passwords map[string]string) context.Context {
	return context.WithValue(ctx, peerPasswordsKey{}, passwords)
}

// usesPeer reports whether one of the arguments is a path on the peer.
func usesPeer(args []string, name string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, PeerRemote(name)+":") {
			return true
		}
	}
	return false
}

func peerPassword(ctx context.Context, p config.Peer) (string, error) {
	if m, _ := ctx.Value(peerPasswordsKey{}).(map[string]string); m != nil {
		if pw, ok := m[p.Name]; ok {
			return pw, nil
		}
	}
	if p.PasswordFile == "" {
		return "", apperror.NewErrorf("peer %s has no password", p.Name)
	}
	pw, err := os.ReadFile(p.PasswordFile)
	if err != nil {
		return "", apperror.NewErrorf("password of peer %s is not readable", p.Name).AddError(err)
	}
	return strings.TrimSpace(string(pw)), nil
}
