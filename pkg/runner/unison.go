package runner

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Valentin-Kaiser/backup/pkg/config"
	"github.com/Valentin-Kaiser/backup/pkg/tunnel"
	"github.com/valentin-kaiser/go-core/apperror"
)

// UnisonCommand is the first argument of a unison run, like SyncCommand it tells the runner which program to start.
const UnisonCommand = "unison-sync"

// markerFile has to exist in both directories when a job checks access, the same file as rclone's --check-access.
const markerFile = "RCLONE_TEST"

// Tunnel is how unison reaches the other worker: through its control API.
type Tunnel struct {
	URL      string
	Password string
	CAFile   string
	// Job and Owner identify the run to the peer, which only accepts a tunnel for a job it has reserved.
	Job   string
	Owner string
}

type tunnelKey struct{}

// WithTunnel tells the runner how to reach the peer of a unison job.
func WithTunnel(ctx context.Context, t Tunnel) context.Context {
	return context.WithValue(ctx, tunnelKey{}, t)
}

// unisonRoot converts a path of a sync job to a root of unison. Paths on a peer become ssh://<peer>/<directory>, the
// directory is relative to the exported directory and the "ssh" is the tunnel.
func unisonRoot(p string) string {
	rest, ok := strings.CutPrefix(p, config.PeerPrefix)
	if !ok {
		return p
	}
	name, dir, _ := strings.Cut(rest, "/")
	dir = strings.Trim(dir, "/")
	if dir == "" {
		dir = "."
	}
	return "ssh://" + name + "/" + dir
}

// unisonArgs builds the arguments of a unison run: the command word, the state directory, then the arguments of unison.
func unisonArgs(c config.Config, job config.Job) ([]string, error) {
	user, err := SplitArgs(job.Args)
	if err != nil {
		return nil, err
	}
	r1, r2 := job.Path1, unisonRoot(job.Path2)
	args := []string{
		UnisonCommand, SyncStateDir(c, job.Name), r1, r2,
		// No questions: conflicts that are not resolved by a preference are skipped and reported by the exit code.
		// The transfers and the summary stay in the output, the run page shows what was synchronized.
		"-batch",
		// The runs of a pair never overlap (see the manager), so a lock left behind by a killed run is stale.
		"-ignorelocks",
		// Failed transfers are tried again before the run is reported as failed.
		"-retry", "2",
		// Keep modification times, they decide the newer version.
		"-times",
		// Refuse to empty a replica, which is what an unmounted volume looks like.
		"-confirmbigdel",
	}
	switch job.Conflict {
	case config.ConflictNewer, "":
		args = append(args, "-prefer", "newer")
	case config.ConflictOlder:
		args = append(args, "-prefer", "older")
	case config.ConflictPath1:
		args = append(args, "-prefer", r1)
	case config.ConflictPath2:
		args = append(args, "-prefer", r2)
	}
	if job.ConflictLoser != config.LoserDelete {
		// The losing version is kept next to the winner, named "file (conflict_on_DATE)".
		args = append(args, "-copyonconflict")
	}
	if job.CheckAccess {
		args = append(args, "-mountpoint", markerFile)
	}
	if strings.HasPrefix(r2, "ssh://") {
		args = append(args, "-sshcmd", tunnel.ClientName)
	}
	return append(args, user...), nil
}

// unisonExitHint explains the exit codes of unison.
func unisonExitHint(code int) string {
	switch code {
	case 1:
		return "some files were skipped, conflicts that no preference resolves need a decision (see the output)"
	case 2:
		return "some files could not be transferred (see the output)"
	case 3:
		return "unison stopped (see the output)"
	}
	return ""
}

// unisonBinary is the unison executable.
func (r Runner) unisonBinary() string {
	if r.UnisonBinary != "" {
		return r.UnisonBinary
	}
	return "unison"
}

// unisonCommand builds the unison process of a run.
func (r Runner) unisonCommand(ctx context.Context, c config.Config, args []string) (*exec.Cmd, error) {
	if len(args) < 3 {
		return nil, apperror.NewError("the unison run has no state directory")
	}
	state := args[1]
	if err := os.MkdirAll(state, 0o750); err != nil {
		return nil, apperror.NewErrorf("the state directory %s cannot be created", state).AddError(err)
	}
	bin := r.unisonBinary()
	full := append(append([]string{}, r.UnisonPrefix...), args[2:]...)
	if c.Nice > 0 {
		full = append([]string{"-n", strconv.Itoa(c.Nice), bin}, full...)
		bin = "nice"
	}
	cmd := exec.CommandContext(ctx, bin, full...)
	cmd.Env = append(os.Environ(),
		// Where unison keeps what it knows about the last synchronised state.
		"UNISON="+state,
		// The archives are named after the host. A container gets a new host name whenever it is created, which
		// would make unison forget the state and treat the next run as the first one.
		"UNISONLOCALHOSTNAME="+c.Worker(),
	)
	if t, ok := ctx.Value(tunnelKey{}).(Tunnel); ok {
		cmd.Env = append(cmd.Env,
			tunnel.EnvURL+"="+t.URL, tunnel.EnvPassword+"="+t.Password, tunnel.EnvCA+"="+t.CAFile,
			tunnel.EnvJob+"="+t.Job, tunnel.EnvOwner+"="+t.Owner,
		)
	}
	r.finishCommand(cmd)
	return cmd, nil
}
