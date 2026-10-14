// Package cli implements the control client commands that talk to the daemon.
package cli

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	pb "github.com/Valentin-Kaiser/backup/pkg/proto/api/v1"
	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/version"
	"github.com/valentin-kaiser/go-core/web/xrpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Commands lists the client sub commands.
var Commands = map[string]bool{
	"status": true, "runs": true, "run": true, "cancel": true, "unlock": true, "pause": true, "resume": true,
	"snapshots": true, "restore": true, "logs": true, "health": true, "version": true, "secret": true,
	"peer": true, "resync": true,
}

const usage = `usage: control <command> [options]

  daemon                      run the scheduler; CONTROL_MODE=portal also serves the API for the portal
  once <backup|forget|check|NAME>  standalone: run one job now and exit with the result of restic
  status                      show jobs, schedules and last results (portal mode)
  runs [-job NAME] [-n 20]    show the run history
  run <job> [-wait]           start a configured job now
  cancel|unlock|pause|resume <job>   control a job (unlock removes all repository locks)
  resync <job> [-wait]        sync jobs: drop the state and sync from scratch (nothing is deleted)
  peer list                   the peers sync jobs can use
  peer add NAME -url URL [-user U] [-password-file F|-password-secret S] [-api-url URL] ...
  peer test|delete NAME
  snapshots [-job NAME]       list restic snapshots, of the repository of a job
  restore [-job NAME] [-snapshot ID] -target DIR [-include PATH]...
  logs [-f]                   print or follow live output
  health                      exit 0 when healthy (Docker HEALTHCHECK)
  secret list                 the stored secrets (repository passwords), never their values
  secret set NAME [-generate]     store a password; reads it from the terminal or stdin
  secret passwd NAME [-generate]  change the password of the repositories that use it
  secret delete NAME
  secret rekey                new master key for the secrets (daemon stopped)
  version

environment: CONTROL_URL (default http://127.0.0.1:8080)
             CONTROL_PASSWORD or CONTROL_PASSWORD_FILE, the password of the portal (the API refuses calls without it)
             CONTROL_PATH (default /var/lib/control, where master.key lives)
`

// rpcPath is the xrpc endpoint of the daemon.
const rpcPath = "/api/v1/rpc"

type client struct {
	base    string
	http    *http.Client
	control *pb.ControlClient
}

func newClient() (*client, error) {
	base := strings.TrimRight(envOr("CONTROL_URL", "http://127.0.0.1:8080"), "/")
	tr := &http.Transport{}
	// The daemon generates a self-signed certificate by default, only accept that for loopback.
	if u, err := url.Parse(base); err == nil && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1") {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // loopback only
	}
	c := &client{base: base, http: &http.Client{Transport: tr, Timeout: 2 * time.Minute}}
	control, err := pb.NewControlClient(base+rpcPath,
		xrpc.WithHTTPClient(c.http),
		xrpc.WithHeader(authHeader()),
		xrpc.WithTLSConfig(tr.TLSClientConfig),
	)
	if err != nil {
		return nil, err
	}
	c.control = control
	return c, nil
}
func envOr(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}

// health asks the unauthenticated health endpoint.
func (c *client) health() error {
	resp, err := c.http.Get(c.base + "/api/v1/health")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	var h struct {
		Healthy bool `json:"healthy"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&h)
	if resp.StatusCode != http.StatusOK || !h.Healthy {
		return apperror.NewError("unhealthy")
	}
	return nil
}

func ts(t *timestamppb.Timestamp) string {
	if t == nil || t.AsTime().IsZero() {
		return "-"
	}
	return t.AsTime().Local().Format("2006-01-02 15:04:05")
}

// state is the lower case name of a run status, e.g. "success".
func state(s pb.RunStatus) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "RUN_STATUS_"))
}

// Run executes a client command and returns the process exit code.
func Run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	if cmd == "version" {
		fmt.Print(version.String())
		return 0
	}
	if cmd == "secret" && len(rest) > 0 && rest[0] == "rekey" {
		if err := rekey(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		return 0
	}
	c, err := newClient()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := c.dispatch(cmd, rest); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func (c *client) dispatch(cmd string, args []string) error {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	ctx := context.Background()
	switch cmd {
	case "secret":
		return c.secret(args)
	case "peer":
		return c.peer(args)
	case "resync":
		return c.resync(args)
	case "health":
		if err := c.health(); err != nil {
			return err
		}
		fmt.Println("healthy")
		return nil
	case "status":
		st, err := c.control.GetStatus(ctx, &pb.GetStatusRequest{})
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "JOB\tTYPE\tSCHEDULE\tNEXT RUN\tSTATE\tLAST RUN\tLAST SUCCESS")
		for _, j := range st.Jobs {
			s, last := "idle", "-"
			if j.Active != nil {
				s = state(j.Active.Status)
			} else if j.Last != nil {
				s = state(j.Last.Status)
			}
			if j.Last != nil {
				last = ts(j.Last.FinishTime)
			}
			sched := j.Schedule
			if j.Name == "restore" {
				sched = ""
			} else if !j.Enabled {
				sched = "disabled"
			} else if j.Paused {
				sched += " (paused)"
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", j.Name, j.Type, sched, ts(j.NextRunTime), s, last, ts(j.LastSuccessTime))
		}
		_ = w.Flush()
		if !st.Healthy {
			fmt.Println("\nUNHEALTHY:", strings.Join(st.Problems, "; "))
		}
		return nil
	case "runs":
		job := fs.String("job", "", "filter by job")
		n := fs.Int("n", 20, "number of runs")
		if err := fs.Parse(args); err != nil {
			return err
		}
		res, err := c.control.ListRuns(ctx, &pb.ListRunsRequest{Job: *job, Limit: int32(*n)}) //nolint:gosec // capped by the daemon
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tJOB\tTRIGGER\tSTATUS\tSTARTED\tFINISHED\tERROR")
		for _, r := range res.Runs {
			started := r.StartTime
			if started == nil {
				started = r.QueueTime
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Id, r.Job, r.Trigger, state(r.Status), ts(started), ts(r.FinishTime), r.Error)
		}
		return w.Flush()
	case "run":
		wait := fs.Bool("wait", false, "wait for the run and print its output")
		job, err := parseJob(fs, args)
		if err != nil {
			return err
		}
		r, err := c.control.StartJob(ctx, &pb.JobRequest{Job: job})
		if err != nil {
			return err
		}
		fmt.Println("started run", r.Id)
		if *wait {
			return c.wait(r.Id)
		}
		return nil
	case "cancel", "unlock", "pause", "resume":
		job, err := parseJob(fs, args)
		if err != nil {
			return err
		}
		req := &pb.JobRequest{Job: job}
		switch cmd {
		case "cancel":
			_, err = c.control.CancelJob(ctx, req)
		case "unlock":
			_, err = c.control.UnlockJob(ctx, req)
		case "pause":
			_, err = c.control.PauseJob(ctx, req)
		default:
			_, err = c.control.ResumeJob(ctx, req)
		}
		return err
	case "snapshots":
		job := fs.String("job", "", "list the repository of this job instead of the global one")
		if err := fs.Parse(args); err != nil {
			return err
		}
		res, err := c.control.ListSnapshots(ctx, &pb.ListSnapshotsRequest{Job: *job})
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tTIME\tHOST\tPATHS")
		for _, s := range res.Snapshots {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.ShortId, ts(s.Time), s.Hostname, strings.Join(s.Paths, ","))
		}
		return w.Flush()
	case "restore":
		snap := fs.String("snapshot", "latest", "snapshot id")
		target := fs.String("target", "", "target directory")
		job := fs.String("job", "", "restore from the repository of this job instead of the global one")
		var include multi
		fs.Var(&include, "include", "path to restore, may be repeated")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *target == "" {
			return apperror.NewErrorf("-target is required")
		}
		r, err := c.control.Restore(ctx, &pb.RestoreRequest{Job: *job, Snapshot: *snap, Target: *target, Include: include})
		if err != nil {
			return err
		}
		fmt.Println("started run", r.Id)
		return c.wait(r.Id)
	case "logs":
		follow := fs.Bool("f", false, "follow")
		if err := fs.Parse(args); err != nil {
			return err
		}
		return c.logs(ctx, *follow)
	}
	fmt.Fprint(os.Stderr, usage)
	return apperror.NewErrorf("unknown command %q", cmd)
}

func parseJob(fs *flag.FlagSet, args []string) (string, error) {
	// Allow flags before and after the job name.
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return "", err
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(pos) != 1 {
		return "", apperror.NewErrorf("exactly one job name is required")
	}
	return pos[0], nil
}

func (c *client) wait(id string) error {
	printed := 0
	for {
		r, err := c.control.GetRun(context.Background(), &pb.GetRunRequest{Id: id})
		if err != nil {
			return err
		}
		for ; printed < len(r.Output); printed++ {
			fmt.Println(r.Output[printed])
		}
		switch r.Status {
		case pb.RunStatus_RUN_STATUS_QUEUED, pb.RunStatus_RUN_STATUS_RUNNING:
			time.Sleep(time.Second)
		case pb.RunStatus_RUN_STATUS_SUCCESS:
			return nil
		default:
			return apperror.NewErrorf("run %s: %s %s", r.Id, state(r.Status), r.Error)
		}
	}
}

func (c *client) logs(ctx context.Context, follow bool) error {
	if !follow {
		res, err := c.control.ListRuns(ctx, &pb.ListRunsRequest{Limit: 1})
		if err != nil {
			return err
		}
		if len(res.Runs) == 0 {
			return nil
		}
		r, err := c.control.GetRun(ctx, &pb.GetRunRequest{Id: res.Runs[0].Id})
		if err != nil {
			return err
		}
		fmt.Println(strings.Join(r.Output, "\n"))
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan *pb.Event, 64)
	errs := make(chan error, 1)
	go func() {
		errs <- c.control.Watch(ctx, &pb.WatchRequest{}, events)
	}()
	for {
		select {
		case err := <-errs:
			return err
		case e, ok := <-events:
			if !ok {
				return <-errs
			}
			if e.Type == pb.EventType_EVENT_TYPE_LINE {
				fmt.Printf("[%s] %s\n", e.Job, e.Line)
			} else {
				fmt.Printf("[%s] -- %s\n", e.Job, state(e.Status))
			}
		}
	}
}
