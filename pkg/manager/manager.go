// Package manager schedules, runs and tracks the backup jobs.
package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/config"
	"github.com/Valentin-Kaiser/backup/pkg/peer"
	pb "github.com/Valentin-Kaiser/backup/pkg/proto/api/v1"
	"github.com/Valentin-Kaiser/backup/pkg/runner"
	"github.com/Valentin-Kaiser/backup/pkg/store"
	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/logging/log"
	"github.com/valentin-kaiser/go-core/queue"
)

const maxOutputLines = 300

// JobRestore is the name of restore runs. They are not scheduled, only started on demand.
const JobRestore = config.RestoreJob

// ErrBusy is returned when a job is already queued or running.
var ErrBusy = apperror.NewError("job is already queued or running")

// Event is published to subscribers whenever a run changes or prints a line.
type Event struct {
	Type   string       `json:"type"` // "status" or "line"
	Run    string       `json:"run"`
	Job    string       `json:"job"`
	Status store.Status `json:"status,omitempty"`
	Line   string       `json:"line,omitempty"`
	Time   time.Time    `json:"time"`
}

// JobStatus is the externally visible state of a job.
type JobStatus struct {
	Name        string     `json:"name"`
	Type        string     `json:"type,omitempty"`
	Definition  config.Job `json:"definition"`
	Enabled     bool       `json:"enabled"`
	Paused      bool       `json:"paused"`
	Schedule    string     `json:"schedule,omitempty"`
	NextRun     *time.Time `json:"nextRun,omitempty"`
	Active      *store.Run `json:"active,omitempty"`
	Last        *store.Run `json:"last,omitempty"`
	LastSuccess *time.Time `json:"lastSuccess,omitempty"`
}

type active struct {
	run    store.Run
	cancel context.CancelFunc
}

// Manager owns the scheduler, the running jobs and the history.
type Manager struct {
	cfg     func() config.Config
	local   runner.Executor
	secrets Secrets

	initMu sync.Mutex
	// ready is set once Start ran, before that Reload only registers the schedules.
	ready   atomic.Bool
	store   *store.Store
	started time.Time

	ctx    context.Context
	cancel context.CancelFunc

	sched *queue.TaskScheduler

	mu         sync.Mutex
	sems       map[string]chan struct{} // per repository, serialises everything that touches it
	touched    map[string]config.Config // repositories this process ran restic on, unlocked on shutdown
	active     map[string]*active
	paused     map[string]bool
	registered map[string]bool // jobs known to the scheduler
	update     Updater
	subs       map[chan Event]struct{}
	wg         sync.WaitGroup
	client     *http.Client
	peers      *peerState
}

// Updater changes the persistent configuration. The change has to be applied, validated and saved before it
// returns, and the manager has to know about it afterwards (Reload), see config.Update.
type Updater func(change func(*config.Config) error) error

// SetUpdater allows the manager to create, change and delete jobs.
func (m *Manager) SetUpdater(u Updater) { m.update = u }

// unavailable fails everything with the reason restic cannot be used, so the callers need no special case.
type unavailable struct{ err error }

func (u unavailable) Exec(context.Context, config.Config, func(string), ...string) (runner.Result, error) {
	return runner.Result{ExitCode: -1}, u.err
}
func (u unavailable) Output(context.Context, config.Config, ...string) ([]byte, error) {
	return nil, u.err
}
func (u unavailable) Preflight(context.Context, config.Config) error { return u.err }

// ex returns the executor for c, with the password of its secret when it uses one.
func (m *Manager) ex(c config.Config) runner.Executor {
	return m.withSecret(m.local, c)
}

// New creates a manager. cfg is called whenever the current configuration is needed.
func New(cfg func() config.Config, r runner.Executor, st *store.Store) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		cfg:        cfg,
		local:      r,
		store:      st,
		started:    time.Now(),
		ctx:        ctx,
		cancel:     cancel,
		sched:      queue.NewTaskScheduler(),
		sems:       map[string]chan struct{}{},
		touched:    map[string]config.Config{},
		active:     map[string]*active{},
		paused:     map[string]bool{},
		registered: map[string]bool{},
		subs:       map[chan Event]struct{}{},
		client:     &http.Client{Timeout: 10 * time.Second},
		peers:      newPeerState(),
	}
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Start registers the schedules, starts the scheduler and prepares the repositories.
func (m *Manager) Start() error {
	if err := m.Reload(); err != nil {
		return err
	}
	if err := m.sched.Start(m.ctx); err != nil {
		return err
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.initRepositories()
	}()
	m.wg.Add(1)
	go m.peerLoop()
	m.wg.Add(1)
	go m.staleLoop()
	// From now on a changed configuration prepares the repositories of new jobs as well.
	m.ready.Store(true)
	return nil
}

// Reload applies the current configuration to the schedules. Once the manager runs it also creates the repositories
// that new or changed jobs need, so a job added in the portal can run right away.
func (m *Manager) Reload() error {
	if err := m.reload(); err != nil {
		return err
	}
	if m.ready.Load() {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.initRepositories()
		}()
	}
	return nil
}

func (m *Manager) reload() error {
	c := m.cfg()
	m.mu.Lock()
	defer m.mu.Unlock()
	configured := map[string]bool{}
	for _, j := range c.Jobs {
		configured[j.Name] = true
	}
	for name := range m.registered {
		if !configured[name] {
			_ = m.sched.RemoveTask(name)
			delete(m.registered, name)
			delete(m.paused, name)
		}
	}
	for _, j := range c.Jobs {
		name := j.Name
		if !j.Enabled || j.Mirror() { // a mirrored job is scheduled by its owner
			_ = m.sched.RemoveTask(name)
			delete(m.registered, name)
			continue
		}
		timeout := time.Duration(j.TimeoutMinutes) * time.Minute
		if timeout == 0 {
			timeout = 72 * time.Hour
		}
		err := m.sched.RegisterOrRescheduleCronTaskWithOptions(name, j.Schedule, func(context.Context) error {
			if _, err := m.Trigger(name, "schedule", true); err != nil {
				log.Warn().Err(err).Msgf("scheduled %s run skipped", name)
			}
			return nil
		}, queue.TaskOptions{Timeout: timeout + time.Hour})
		if err != nil {
			return apperror.NewErrorf("invalid schedule for %s", name).AddError(err)
		}
		m.registered[name] = true
		if m.paused[name] {
			_ = m.sched.DisableTask(name)
		}
	}
	return nil
}

// initRepositories creates the repositories of the jobs that do not exist yet and starts their first backups.
func (m *Manager) initRepositories() {
	// One at a time, a start and a changed configuration may come at once.
	m.initMu.Lock()
	defer m.initMu.Unlock()
	c := m.cfg()
	for _, r := range c.Repos() {
		if !c.UsedByJobs(r.Name) {
			continue // nothing works on it yet, it is created with its first job
		}
		rc := c.ForRepository(r)
		// The mount guard of the repository, its volume may be unmounted while another one is mounted.
		if err := m.ex(rc).Preflight(m.ctx, rc); err != nil {
			log.Warn().Err(err).Msgf("repository %s is not available, initialisation postponed", r.Name)
			continue
		}
		if !m.initRepository(rc) {
			continue
		}
		// The repository was just created, its backups do not have to wait for the schedule.
		for _, b := range c.Jobs {
			if b.Type == config.TypeBackup && b.Enabled && c.RepositoryKey(b) == rc.Repository {
				if _, err := m.Trigger(b.Name, "init", false); err != nil {
					log.Warn().Err(err).Msgf("initial %s run not started", b.Name)
				}
			}
		}
	}
}

// initRepository creates the repository of c when it does not exist and reports whether it did.
func (m *Manager) initRepository(c config.Config) bool {
	ctx, cancel := context.WithTimeout(m.ctx, 2*time.Minute)
	defer cancel()
	if _, err := m.ex(c).Exec(ctx, c, nil, "cat", "config"); err == nil {
		return false
	}
	log.Info().Msgf("repository %s not found, initialising", c.Repository)
	if _, err := m.ex(c).Exec(ctx, c, func(l string) { log.Info().Msg(l) }, "init"); err != nil {
		log.Error().Err(err).Msgf("initialisation of %s failed", c.Repository)
		return false
	}
	return true
}

// Trigger starts a configured job. When wait is set it returns after the run finished.
func (m *Manager) Trigger(name, trigger string, wait bool) (store.Run, error) {
	c := m.cfg()
	j, ok := c.Job(name)
	if !ok {
		return store.Run{}, apperror.NewErrorf("unknown job %q", name)
	}
	if j.Mirror() {
		return m.forwardRun(j, func(ctx context.Context, cl peer.Client) (*pb.Run, error) {
			return cl.StartJob(ctx, &pb.JobRequest{Job: name})
		})
	}
	if holder, locked := m.lockedByPeer(name); locked {
		return store.Run{}, apperror.NewErrorf("peer %s is syncing this job", holder).AddError(ErrBusy)
	}
	jc := c.ForJob(j)
	if j.Type == config.TypeSync {
		// A sync job has no repository, the pair of directories takes its place as the lock that serialises runs.
		jc.Repository, jc.PasswordFile, jc.PasswordSecret = config.SyncKey(j), "", ""
	}
	args, err := runner.Args(jc, j)
	if err != nil {
		return store.Run{}, err
	}
	timeout := time.Duration(j.TimeoutMinutes) * time.Minute
	return m.start(name, trigger, jc, args, timeout, wait)
}

// ResyncJob drops the state of a sync job and runs it again. The next run builds the listings of both sides from
// scratch, files that differ are copied and nothing is deleted.
func (m *Manager) ResyncJob(name string) (store.Run, error) {
	c := m.cfg()
	j, ok := c.Job(name)
	if !ok || j.Type != config.TypeSync {
		return store.Run{}, apperror.NewErrorf("%q is not a sync job", name)
	}
	if j.Mirror() {
		return m.forwardRun(j, func(ctx context.Context, cl peer.Client) (*pb.Run, error) {
			return cl.ResyncJob(ctx, &pb.JobRequest{Job: name})
		})
	}
	m.mu.Lock()
	_, busy := m.active[name]
	m.mu.Unlock()
	if busy {
		return store.Run{}, ErrBusy
	}
	if err := runner.ResetSync(c, name); err != nil {
		return store.Run{}, err
	}
	return m.Trigger(name, "resync", false)
}

// syncPreflight refuses to sync while one of the local directories is missing, which is what an unmounted volume
// looks like. Syncing against an empty mount point would delete everything on the other side.
func (m *Manager) syncPreflight(j config.Job) error {
	paths := []string{j.Path1}
	if _, _, peer := j.PeerPath(); !peer {
		paths = append(paths, j.Path2)
	}
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return apperror.NewErrorf("sync path %s is not available", p).AddError(err)
		}
		if !st.IsDir() {
			return apperror.NewErrorf("sync path %s is not a directory", p)
		}
	}
	// The mount guards of the job, the volume a sync path lives on may be unmounted while the directory is
	// still there, and syncing against an empty volume would empty the other side too.
	return runner.CheckMounts(m.cfg().ForJob(j))
}

// Restore starts a restore run from a repository, see repository for how it is chosen. Restores are limited to the
// restore directory of the repository.
func (m *Manager) Restore(repo, job, snapshot, target string, include []string) (store.Run, error) {
	c, err := m.repository(repo, job)
	if err != nil {
		return store.Run{}, err
	}
	root, err := filepath.Abs(c.RestoreRoot)
	if err != nil {
		return store.Run{}, apperror.Wrap(err)
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return store.Run{}, apperror.Wrap(err)
	}
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return store.Run{}, apperror.NewErrorf("restore target must be below %s", root)
	}
	if snapshot == "" {
		snapshot = "latest"
	}
	if strings.HasPrefix(snapshot, "-") {
		return store.Run{}, apperror.NewError("invalid snapshot")
	}
	// A snapshot stores files below the absolute path that was backed up, restoring it as is would recreate that
	// path (e.g. /source) inside the target. Restore the content of that directory instead.
	if base := m.snapshotBase(c, snapshot); base != "" {
		snapshot += ":" + base
		for i, p := range include {
			p = "/" + strings.TrimLeft(p, "/")
			if p == base {
				p = "/"
			} else if rest, ok := strings.CutPrefix(p, base+"/"); ok {
				p = "/" + rest
			}
			include[i] = p
		}
	}
	args := []string{"restore"}
	if c.LockRetry != "" {
		args = append(args, "--retry-lock", c.LockRetry)
	}
	for _, i := range include {
		args = append(args, "--include", i)
	}
	args = append(args, "--target", abs, "--", snapshot)
	return m.start(JobRestore, "manual", c, args, 0, false)
}

// snapshotBase returns the single directory a snapshot was created from, empty when it cannot be determined
// or when the snapshot covers several paths.
func (m *Manager) snapshotBase(c config.Config, snapshot string) string {
	ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
	defer cancel()
	out, err := m.ex(c).Output(ctx, c, "snapshots", "--json", "--no-lock", "--", snapshot)
	if err != nil {
		return ""
	}
	var snaps []struct {
		Paths []string `json:"paths"`
	}
	if json.Unmarshal(out, &snaps) != nil || len(snaps) != 1 || len(snaps[0].Paths) != 1 {
		return ""
	}
	base := path.Clean(snaps[0].Paths[0])
	if !path.IsAbs(base) || base == "/" {
		return ""
	}
	return base
}

// repository returns the configuration for a repository, named directly or through a job that works on it. With
// both empty it is the only repository, when there is just one.
func (m *Manager) repository(repo, job string) (config.Config, error) {
	c := m.cfg()
	switch {
	case repo != "":
		r, ok := c.Repo(repo)
		if !ok {
			return config.Config{}, apperror.NewErrorf("unknown repository %q", repo)
		}
		return c.ForRepository(r), nil
	case job != "":
		j, ok := c.Job(job)
		if !ok {
			return config.Config{}, apperror.NewErrorf("unknown job %q", job)
		}
		if j.Type == config.TypeSync {
			return config.Config{}, apperror.NewErrorf("job %q is a sync job and has no repository", job)
		}
		return c.ForJob(j), nil
	}
	repos := c.Repos()
	if len(repos) != 1 {
		return config.Config{}, apperror.NewErrorf("there are %d repositories, name one", len(repos))
	}
	return c.ForRepository(repos[0]), nil
}

// sem returns the lock of a repository.
func (m *Manager) sem(repository string) chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sems[repository]
	if !ok {
		s = make(chan struct{}, 1)
		m.sems[repository] = s
	}
	return s
}

func (m *Manager) start(name, trigger string, c config.Config, args []string, timeout time.Duration, wait bool) (store.Run, error) {
	m.mu.Lock()
	if _, busy := m.active[name]; busy {
		m.mu.Unlock()
		return store.Run{}, ErrBusy
	}
	ctx, cancel := context.WithCancel(m.ctx)
	a := &active{
		run:    store.Run{ID: newID(), Job: name, Trigger: trigger, Status: store.StatusQueued, Queued: time.Now()},
		cancel: cancel,
	}
	m.active[name] = a
	snapshot := a.run
	m.wg.Add(1)
	m.mu.Unlock()

	m.publish(Event{Type: "status", Run: a.run.ID, Job: name, Status: store.StatusQueued})
	done := make(chan struct{})
	go func() {
		defer m.wg.Done()
		defer close(done)
		m.execute(ctx, a, c, args, timeout)
	}()
	if wait {
		<-done
		m.mu.Lock()
		snapshot = a.run
		m.mu.Unlock()
	}
	return snapshot, nil
}

func (m *Manager) setStatus(a *active, s store.Status, mutate func(*store.Run)) {
	m.mu.Lock()
	a.run.Status = s
	if mutate != nil {
		mutate(&a.run)
	}
	id, job := a.run.ID, a.run.Job
	m.mu.Unlock()
	m.publish(Event{Type: "status", Run: id, Job: job, Status: s})
}

// execute runs one job with the configuration c, which has the repository of the job applied.
func (m *Manager) execute(ctx context.Context, a *active, c config.Config, args []string, timeout time.Duration) {
	defer a.cancel()
	job := a.run.Job

	// Wait for the repository, everything that touches it runs one at a time.
	sem := m.sem(c.Repository)
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		m.finish(a, store.StatusCancelled, 0, "cancelled while queued")
		return
	}

	if sj, ok := m.cfg().Job(job); !ok || sj.Type != config.TypeSync {
		m.mu.Lock()
		m.touched[c.Repository] = c
		m.mu.Unlock()
	}

	now := time.Now()
	m.setStatus(a, store.StatusRunning, func(r *store.Run) { r.Started = &now })
	m.ping(c, "/start")

	var err error
	sj, isSync := m.cfg().Job(job)
	if isSync = isSync && sj.Type == config.TypeSync; isSync {
		err = m.syncPreflight(sj)
	} else {
		err = m.ex(c).Preflight(ctx, c)
	}
	if errors.Is(err, runner.ErrUnavailable) {
		// Restic cannot be used, that is a failure and not a repository that is not mounted.
		log.Error().Err(err).Msgf("%s failed", job)
		m.line(a, "failed: "+err.Error())
		m.finish(a, store.StatusFailed, -1, err.Error())
		m.ping(c, "/fail")
		return
	} else if err != nil {
		log.Error().Err(err).Msgf("%s skipped", job)
		m.line(a, "skipped: "+err.Error())
		m.finish(a, store.StatusSkipped, 0, err.Error())
		m.ping(c, "/fail")
		return
	}

	if isSync {
		// The peer must not run the same job while this worker syncs with it.
		release, err := m.lockPeerRetrying(ctx, a, m.cfg(), sj, timeout)
		switch {
		case errors.Is(err, errPeerBusy):
			log.Warn().Err(err).Msgf("%s skipped", job)
			m.line(a, "skipped: "+err.Error())
			m.finish(a, store.StatusSkipped, 0, err.Error())
			return
		case err != nil:
			log.Error().Err(err).Msgf("%s failed", job)
			m.line(a, "failed: "+err.Error())
			m.finish(a, store.StatusFailed, -1, err.Error())
			m.ping(c, "/fail")
			return
		}
		defer release()
		ctx = m.withPeerPasswords(ctx, m.cfg())
		if sj.Unison() {
			var terr error
			if ctx, terr = m.withTunnel(ctx, m.cfg(), sj); terr != nil {
				m.line(a, "failed: "+terr.Error())
				m.finish(a, store.StatusFailed, -1, terr.Error())
				m.ping(c, "/fail")
				return
			}
		}
	}

	runCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	log.Info().Msgf("%s started (%s)", job, a.run.Trigger)
	res, err := m.ex(c).Exec(runCtx, c, func(l string) { m.line(a, l) }, args...)
	switch {
	case err == nil:
		m.finish(a, store.StatusSuccess, 0, "")
		m.ping(c, "")
		log.Info().Msgf("%s finished", job)
	case ctx.Err() != nil:
		if !isSync {
			m.unlock(c)
		}
		m.finish(a, store.StatusCancelled, res.ExitCode, "cancelled")
		log.Warn().Msgf("%s cancelled", job)
	case runCtx.Err() != nil:
		if !isSync {
			m.unlock(c)
		}
		m.finish(a, store.StatusFailed, res.ExitCode, "timeout exceeded")
		m.ping(c, "/fail")
		log.Error().Msgf("%s timed out", job)
	default:
		if !isSync {
			m.unlock(c)
		}
		m.finish(a, store.StatusFailed, res.ExitCode, err.Error())
		m.ping(c, "/fail")
		log.Error().Err(err).Msgf("%s failed", job)
	}
}

// Unlock removes all locks from the repository of a job, also those of processes that are still alive. It refuses
// while a job works on the repository of this worker, a lock of a running job must not be removed.
func (m *Manager) Unlock(name string) error {
	if j, ok := m.mirror(name); ok {
		return m.forward(j, func(ctx context.Context, cl peer.Client) error {
			_, err := cl.UnlockJob(ctx, &pb.JobRequest{Job: name})
			return err
		})
	}
	j, ok := m.cfg().Job(name)
	if !ok {
		return apperror.NewErrorf("unknown job %q", name)
	}
	if j.Type == config.TypeSync {
		return apperror.NewErrorf("job %q is a sync job and has no repository", name)
	}
	c, err := m.repository("", name)
	if err != nil {
		return err
	}
	select {
	case m.sem(c.Repository) <- struct{}{}:
		defer func() { <-m.sem(c.Repository) }()
	default:
		return apperror.NewErrorf("a job is working on the repository of %q", name).AddError(ErrBusy)
	}
	ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
	defer cancel()
	if err := m.ex(c).Preflight(ctx, c); err != nil {
		return err
	}
	if _, err := m.ex(c).Exec(ctx, c, nil, "unlock", "--remove-all"); err != nil {
		return err
	}
	log.Warn().Msgf("removed all locks of the repository of %s", name)
	return nil
}

// unlock removes stale locks left behind by an aborted restic process.
func (m *Manager) unlock(c config.Config) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := m.ex(c).Exec(ctx, c, nil, "unlock"); err != nil {
		log.Warn().Err(err).Msg("restic unlock failed")
	}
}

func (m *Manager) line(a *active, l string) {
	m.mu.Lock()
	a.run.Output = append(a.run.Output, l)
	if len(a.run.Output) > maxOutputLines {
		a.run.Output = a.run.Output[len(a.run.Output)-maxOutputLines:]
	}
	id, job := a.run.ID, a.run.Job
	m.mu.Unlock()
	m.publish(Event{Type: "line", Run: id, Job: job, Line: l})
}

func (m *Manager) finish(a *active, s store.Status, exit int, msg string) {
	end := time.Now()
	m.mu.Lock()
	a.run.Status = s
	a.run.Finished = &end
	a.run.ExitCode = exit
	a.run.Error = msg
	r := a.run
	delete(m.active, r.Job)
	m.mu.Unlock()
	if err := m.store.Add(r); err != nil {
		log.Error().Err(err).Msg("failed to persist run")
	}
	m.publish(Event{Type: "status", Run: r.ID, Job: r.Job, Status: s})
	m.notifyRun(r)
	if s == store.StatusSuccess {
		for _, next := range m.cfg().Followers(r.Job) {
			go func() {
				if _, err := m.Trigger(next, "after:"+r.Job, false); err != nil {
					log.Warn().Err(err).Msgf("%s not started after %s", next, r.Job)
				}
			}()
		}
	}
}

// ping notifies the configured healthchecks-style webhook.
func (m *Manager) ping(c config.Config, suffix string) {
	if c.WebhookURL == "" {
		return
	}
	url := strings.TrimRight(c.WebhookURL, "/") + suffix
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return
		}
		resp, err := m.client.Do(req)
		if err != nil {
			log.Warn().Msg("webhook ping failed")
			return
		}
		_ = resp.Body.Close()
	}()
}

// Cancel aborts a queued or running job.
func (m *Manager) Cancel(name string) error {
	if j, ok := m.mirror(name); ok {
		return m.forward(j, func(ctx context.Context, cl peer.Client) error {
			_, err := cl.CancelJob(ctx, &pb.JobRequest{Job: name})
			return err
		})
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.active[name]
	if !ok {
		return apperror.NewErrorf("job %q is not running", name)
	}
	a.cancel()
	return nil
}

// Pause stops a job from being scheduled, manual runs still work.
func (m *Manager) Pause(name string) error { return m.setPaused(name, true) }

// Resume re-enables the schedule of a job.
func (m *Manager) Resume(name string) error { return m.setPaused(name, false) }

func (m *Manager) setPaused(name string, p bool) error {
	j, ok := m.cfg().Job(name)
	if !ok {
		return apperror.NewErrorf("unknown job %q", name)
	}
	if j.Mirror() {
		return m.forward(j, func(ctx context.Context, cl peer.Client) error {
			var err error
			if p {
				_, err = cl.PauseJob(ctx, &pb.JobRequest{Job: name})
			} else {
				_, err = cl.ResumeJob(ctx, &pb.JobRequest{Job: name})
			}
			return err
		})
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.paused[name] = p
	var err error
	if p {
		err = m.sched.DisableTask(name)
	} else {
		err = m.sched.EnableTask(name)
	}
	if err != nil && j.Enabled {
		return err
	}
	return nil
}

// Status returns the state of every job, restore comes last.
func (m *Manager) Status() []JobStatus {
	c := m.cfg()
	jobs := append(append([]config.Job{}, c.Jobs...), config.Job{Name: JobRestore})
	out := make([]JobStatus, 0, len(jobs))
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range jobs {
		st := JobStatus{Name: j.Name, Type: j.Type, Definition: j, Enabled: j.Enabled, Paused: m.paused[j.Name], Schedule: j.Schedule}
		if t, err := m.sched.GetTask(j.Name); err == nil && j.Enabled && !st.Paused {
			next := t.NextRun
			st.NextRun = &next
		}
		if a, ok := m.active[j.Name]; ok {
			r := a.run
			r.Output = nil
			st.Active = &r
		}
		if r, ok := m.store.Last(j.Name); ok {
			r.Output = nil
			st.Last = &r
		}
		if r, ok := m.store.LastSuccess(j.Name); ok && r.Finished != nil {
			st.LastSuccess = r.Finished
		}
		if j.Mirror() {
			m.overlayMirror(&st)
		}
		out = append(out, st)
	}
	return out
}

// Config returns the current configuration.
func (m *Manager) Config() config.Config { return m.cfg() }

// ErrReadOnly is returned when jobs cannot be changed.
var ErrReadOnly = apperror.NewError("the configuration is read-only")

// CreateJob adds a job to the configuration and schedules it.
func (m *Manager) CreateJob(j config.Job) (JobStatus, error) {
	j = normalize(j)
	if j.Mirror() {
		return JobStatus{}, apperror.NewError("a mirrored job is created by its owner")
	}
	if err := m.check(j); err != nil {
		return JobStatus{}, err
	}
	err := m.change(func(c *config.Config) error {
		if _, ok := c.Job(j.Name); ok {
			return apperror.NewErrorf("job %q already exists", j.Name)
		}
		c.Jobs = append(c.Jobs, j)
		return nil
	})
	if err != nil {
		return JobStatus{}, err
	}
	m.afterJobChange(nil, j)
	return m.jobStatus(j.Name), nil
}

// UpdateJob replaces the configuration of an existing job.
func (m *Manager) UpdateJob(j config.Job) (JobStatus, error) {
	j = normalize(j)
	if err := m.check(j); err != nil {
		return JobStatus{}, err
	}
	var old *config.Job
	if cur, ok := m.cfg().Job(j.Name); ok {
		if cur.Mirror() {
			return m.updateMirror(j, cur)
		}
		old = &cur
	}
	j.Owner = ""
	err := m.change(func(c *config.Config) error {
		for i := range c.Jobs {
			if c.Jobs[i].Name == j.Name {
				// The mounts are not part of the API, editing a job must not drop them.
				j.Mounts = c.Jobs[i].Mounts
				c.Jobs[i] = j
				return nil
			}
		}
		return apperror.NewErrorf("unknown job %q", j.Name)
	})
	if err != nil {
		return JobStatus{}, err
	}
	m.afterJobChange(old, j)
	return m.jobStatus(j.Name), nil
}

// DeleteJob removes a job from the configuration. Its history is kept.
func (m *Manager) DeleteJob(name string) error {
	m.mu.Lock()
	_, running := m.active[name]
	m.mu.Unlock()
	if running {
		return apperror.NewErrorf("job %q is running, cancel it first", name)
	}
	if next := m.cfg().Followers(name); len(next) > 0 {
		return apperror.NewErrorf("job %q is followed by %s, change their \"after\" first", name, strings.Join(next, ", "))
	}
	old, found := m.cfg().Job(name)
	if found && old.Mirror() {
		// The owner removes the job and tells this worker to drop the mirror. If that does not arrive the mirror
		// is removed here.
		if err := m.forward(old, func(ctx context.Context, cl peer.Client) error {
			_, err := cl.DeleteJob(ctx, &pb.JobRequest{Job: name})
			return err
		}); err != nil {
			return err
		}
		return m.DropMirror(name, old.Owner)
	}
	err := m.change(func(c *config.Config) error {
		for i := range c.Jobs {
			if c.Jobs[i].Name == name {
				c.Jobs = slices.Delete(c.Jobs, i, i+1)
				return nil
			}
		}
		return apperror.NewErrorf("unknown job %q", name)
	})
	if err == nil && found {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.dropAtPeer(old)
		}()
	}
	return err
}

func (m *Manager) change(f func(*config.Config) error) error {
	if m.update == nil {
		return ErrReadOnly
	}
	return m.update(f)
}

func normalize(j config.Job) config.Job {
	j.Name = strings.TrimSpace(j.Name)
	j.Schedule = strings.TrimSpace(j.Schedule)
	j.Source = strings.TrimSpace(j.Source)
	j.Path1 = strings.TrimSpace(j.Path1)
	j.Path2 = strings.TrimSpace(j.Path2)
	j.Repository = strings.TrimSpace(j.Repository)
	return j
}

// check rejects jobs whose restic arguments cannot be built, the rest is validated with the configuration.
func (m *Manager) check(j config.Job) error {
	_, err := runner.Args(m.cfg().ForJob(j), j)
	return err
}

func (m *Manager) jobStatus(name string) JobStatus {
	for _, s := range m.Status() {
		if s.Name == name {
			return s
		}
	}
	return JobStatus{Name: name}
}

// ActiveRun returns the live copy of a running run, including its output.
func (m *Manager) ActiveRun(id string) (store.Run, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.active {
		if a.run.ID == id {
			return a.run, true
		}
	}
	return store.Run{}, false
}

// Run returns a run from the history or the live runs.
func (m *Manager) Run(id string) (store.Run, bool) {
	if r, ok := m.ActiveRun(id); ok {
		return r, true
	}
	if r, ok := m.store.Get(id); ok {
		return r, true
	}
	return m.remoteRun(id)
}

// Runs lists the history newest first, active runs first.
func (m *Manager) Runs(job string, n int) []store.Run {
	if j, ok := m.mirror(job); ok {
		return m.remoteRuns(j, n)
	}
	var out []store.Run
	m.mu.Lock()
	for _, a := range m.active {
		if job == "" || a.run.Job == job {
			r := a.run
			r.Output = nil
			out = append(out, r)
		}
	}
	m.mu.Unlock()
	for _, r := range m.store.List(job, n) {
		r.Output = nil
		out = append(out, r)
	}
	if job == "" {
		// The runs of mirrored jobs happen at their owner.
		if remote := m.mirroredRuns(n); len(remote) > 0 {
			out = append(out, remote...)
			sort.SliceStable(out, func(i, k int) bool { return out[i].Queued.After(out[k].Queued) })
		}
	}
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// Health reports whether the daemon is in a good state.
func (m *Manager) Health() (bool, []string) {
	c := m.cfg()
	var problems []string
	for _, j := range c.Jobs {
		name := j.Name
		if !j.Enabled || j.Mirror() {
			continue
		}
		if last, ok := m.store.Last(name); ok && last.Status == store.StatusFailed {
			problems = append(problems, name+": last run failed")
		}
		if j.MaxAgeHours > 0 {
			maxAge := time.Duration(j.MaxAgeHours) * time.Hour
			last, ok := m.store.LastSuccess(name)
			switch {
			case ok && last.Finished != nil && time.Since(*last.Finished) > maxAge:
				problems = append(problems, name+": no successful run within "+maxAge.String())
			case !ok && time.Since(m.started) > maxAge:
				problems = append(problems, name+": no successful run since start")
			}
		}
	}
	return len(problems) == 0, problems
}

// Snapshots returns the restic snapshot list of a repository as raw JSON, see repository for how it is chosen.
func (m *Manager) Snapshots(ctx context.Context, repo, job string) (json.RawMessage, error) {
	return m.query(ctx, repo, job, "snapshots", "--json", "--no-lock")
}

// Stats returns the restic statistics of a repository as raw JSON, see repository for how it is chosen.
func (m *Manager) Stats(ctx context.Context, repo, job string) (json.RawMessage, error) {
	return m.query(ctx, repo, job, "stats", "--json", "--no-lock", "--mode", "raw-data")
}

// Listing returns the output of `restic ls --json` for a directory of a snapshot, one JSON object per line.
// The snapshot is read from a repository, see repository for how it is chosen.
func (m *Manager) Listing(ctx context.Context, repo, job, snapshot, dir string) ([]byte, error) {
	if snapshot == "" || strings.HasPrefix(snapshot, "-") || strings.ContainsAny(snapshot, " \t\r\n\x00") {
		return nil, apperror.NewError("invalid snapshot")
	}
	if !strings.HasPrefix(dir, "/") || strings.ContainsRune(dir, 0) {
		return nil, apperror.NewError("invalid path")
	}
	c, err := m.repository(repo, job)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := m.ex(c).Preflight(ctx, c); err != nil {
		return nil, err
	}
	return m.ex(c).Output(ctx, c, "ls", "--json", "--no-lock", "--", snapshot, dir)
}

func (m *Manager) query(ctx context.Context, repo, job string, args ...string) (json.RawMessage, error) {
	c, err := m.repository(repo, job)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := m.ex(c).Preflight(ctx, c); err != nil {
		return nil, err
	}
	out, err := m.ex(c).Output(ctx, c, args...)
	if err != nil {
		return nil, err
	}
	if !json.Valid(out) {
		return nil, apperror.NewError("restic returned invalid JSON")
	}
	return out, nil
}

// HasJob reports whether a job is configured.
func (m *Manager) HasJob(name string) bool {
	_, ok := m.cfg().Job(name)
	return ok
}

// Subscribe returns a channel of events and a function to cancel the subscription.
func (m *Manager) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 256)
	m.mu.Lock()
	m.subs[ch] = struct{}{}
	m.mu.Unlock()
	return ch, func() {
		m.mu.Lock()
		if _, ok := m.subs[ch]; ok {
			delete(m.subs, ch)
			close(ch)
		}
		m.mu.Unlock()
	}
}

func (m *Manager) publish(e Event) {
	e.Time = time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for ch := range m.subs {
		select {
		case ch <- e:
		default: // slow subscriber, drop the event
		}
	}
}

// Shutdown stops scheduling, gives running jobs the grace period to finish and cancels them afterwards.
func (m *Manager) Shutdown(grace time.Duration) {
	stopped := make(chan struct{})
	go func() {
		m.sched.Stop()
		m.wg.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(grace):
		log.Warn().Msg("grace period exceeded, cancelling running jobs")
		m.cancel()
		select {
		case <-stopped:
		case <-time.After(30 * time.Second):
			log.Error().Msg("jobs did not stop in time")
		}
	}
	m.cancel()
	m.unlockAll()
}

// unlockAll removes the stale locks of every repository this process worked on. A job that was killed after its
// grace period cannot release its lock itself and the next start would wait for it until restic treats it as stale.
func (m *Manager) unlockAll() {
	m.mu.Lock()
	repos := make([]config.Config, 0, len(m.touched))
	for _, c := range m.touched {
		repos = append(repos, c)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, c := range repos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.unlock(c)
		}()
	}
	wg.Wait()
}

// Context returns the context that is cancelled on shutdown.
func (m *Manager) Context() context.Context { return m.ctx }
