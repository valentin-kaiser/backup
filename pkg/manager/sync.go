package manager

import (
	"context"
	"errors"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/config"
	"github.com/Valentin-Kaiser/backup/pkg/peer"
	pb "github.com/Valentin-Kaiser/backup/pkg/proto/api/v1"
	"github.com/Valentin-Kaiser/backup/pkg/runner"
	"github.com/Valentin-Kaiser/backup/pkg/store"
	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/logging/log"
)

// peerInterval is how often mirrors are refreshed and unsent mirrors are pushed.
const peerInterval = 30 * time.Second

// errPeerBusy is returned when a peer holds the reservation of a sync job.
var errPeerBusy = apperror.NewError("the peer is syncing this job")

type peerLock struct {
	owner string
	until time.Time
}

// remoteJob is what the owner of a mirrored job last reported.
type remoteJob struct {
	job *pb.Job
	at  time.Time
	err string
}

// peerState holds everything the manager knows about its peers.
type peerState struct {
	mu      sync.Mutex
	dial    peer.Dialer
	locks   map[string]peerLock  // by job name, taken by a peer
	remote  map[string]remoteJob // by job name, mirrored jobs
	pushed  map[string]bool      // owned jobs whose mirror reached the peer
	pending map[string]struct{}  // owned jobs whose mirror has to be pushed again
	wake    chan struct{}
}

func newPeerState() *peerState {
	return &peerState{
		dial:    peer.Dial,
		locks:   map[string]peerLock{},
		remote:  map[string]remoteJob{},
		pushed:  map[string]bool{},
		pending: map[string]struct{}{},
		wake:    make(chan struct{}, 1),
	}
}

// SetDialer replaces the way peers are reached, used by tests.
func (m *Manager) SetDialer(d peer.Dialer) { m.peers.dial = d }

// secretOrFile returns the password of a stored secret, or the content of a password file.
func (m *Manager) secretOrFile(secret, file string) (string, error) {
	if secret != "" {
		if m.secrets == nil {
			return "", apperror.NewErrorf("the secret %s is configured but this daemon has no secret store", secret)
		}
		pw, err := m.secrets.Get(secret)
		if err != nil {
			return "", apperror.NewErrorf("the secret %s cannot be read", secret).AddError(err)
		}
		return pw, nil
	}
	if file == "" {
		return "", nil
	}
	return readPassword(file)
}

// peerPasswords resolves the data passwords of the peers that use a stored secret.
func (m *Manager) peerPasswords(c config.Config) map[string]string {
	out := map[string]string{}
	for _, p := range c.Peers {
		if p.PasswordSecret == "" {
			continue
		}
		if pw, err := m.secretOrFile(p.PasswordSecret, ""); err == nil {
			out[p.Name] = pw
		}
	}
	return out
}

// DataPassword returns the password of the sync server of a peer.
func (m *Manager) DataPassword(p config.Peer) (string, error) {
	return m.secretOrFile(p.PasswordSecret, p.PasswordFile)
}

// SyncServePassword returns the password peers have to use at this worker.
func (m *Manager) SyncServePassword(s config.SyncServe) (string, error) {
	return m.secretOrFile(s.PasswordSecret, s.PasswordFile)
}

// client connects to the control API of a peer.
func (m *Manager) peerClient(c config.Config, name string) (peer.Client, error) {
	p, ok := c.Peer(name)
	if !ok {
		return nil, apperror.NewErrorf("unknown peer %q", name)
	}
	if p.APIURL == "" {
		return nil, apperror.NewErrorf("peer %s has no API URL", name)
	}
	pw, err := m.secretOrFile(p.APIPasswordSecret, p.APIPasswordFile)
	if err != nil {
		return nil, err
	}
	return m.peers.dial(p, pw)
}

// hasAPI reports whether the peer of a sync job can be called.
func hasAPI(c config.Config, j config.Job) (string, bool) {
	name, _, ok := j.PeerPath()
	if !ok {
		return "", false
	}
	p, found := c.Peer(name)
	return name, found && p.APIURL != ""
}

func callCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 30*time.Second)
}

// ---- reservations ------------------------------------------------------------------------------------------------

// AcquireSyncLock reserves a job for a peer, so this worker does not run it while the peer syncs.
func (m *Manager) AcquireSyncLock(job, owner string, ttl time.Duration) (bool, string) {
	if ttl <= 0 {
		ttl = time.Hour
	}
	m.mu.Lock()
	_, running := m.active[job]
	m.mu.Unlock()
	if running {
		return false, m.cfg().Worker()
	}
	m.peers.mu.Lock()
	defer m.peers.mu.Unlock()
	if l, ok := m.peers.locks[job]; ok && l.owner != owner && time.Now().Before(l.until) {
		return false, l.owner
	}
	m.peers.locks[job] = peerLock{owner: owner, until: time.Now().Add(ttl)}
	return true, owner
}

// ReleaseSyncLock gives a reservation back.
func (m *Manager) ReleaseSyncLock(job, owner string) {
	m.peers.mu.Lock()
	defer m.peers.mu.Unlock()
	if l, ok := m.peers.locks[job]; ok && l.owner == owner {
		delete(m.peers.locks, job)
	}
}

// lockedByPeer returns the peer that reserved a job.
func (m *Manager) lockedByPeer(job string) (string, bool) {
	m.peers.mu.Lock()
	defer m.peers.mu.Unlock()
	l, ok := m.peers.locks[job]
	if ok && time.Now().After(l.until) {
		delete(m.peers.locks, job)
		return "", false
	}
	return l.owner, ok
}

// lockPeer reserves the job at the peer that holds the second path. The returned function gives it back.
func (m *Manager) lockPeer(ctx context.Context, c config.Config, j config.Job, timeout time.Duration) (func(), error) {
	name, ok := hasAPI(c, j)
	if !ok {
		return func() {}, nil
	}
	cl, err := m.peerClient(c, name)
	if err != nil {
		return nil, err
	}
	ttl := timeout + 5*time.Minute
	if timeout == 0 {
		ttl = 24 * time.Hour
	}
	cctx, cancel := callCtx(ctx)
	defer cancel()
	res, err := cl.AcquireSyncLock(cctx, &pb.SyncLockRequest{Job: j.Name, Owner: c.Worker(), TtlSeconds: int32(ttl / time.Second)}) //nolint:gosec // bounded
	if err != nil {
		return nil, apperror.NewErrorf("peer %s is not reachable", name).AddError(err)
	}
	if !res.GetGranted() {
		return nil, apperror.NewErrorf("peer %s is busy with this job (%s)", name, res.GetHolder()).AddError(errPeerBusy)
	}
	return func() {
		rctx, rcancel := callCtx(context.Background())
		defer rcancel()
		if _, err := cl.ReleaseSyncLock(rctx, &pb.SyncLockRequest{Job: j.Name, Owner: c.Worker()}); err != nil {
			log.Warn().Err(err).Msgf("releasing %s at peer %s failed, the reservation expires on its own", j.Name, name)
		}
	}, nil
}

// ---- mirrors -----------------------------------------------------------------------------------------------------

// swapConflict turns a conflict policy around when the paths are swapped.
func swapConflict(s string) string {
	switch s {
	case config.ConflictPath1:
		return config.ConflictPath2
	case config.ConflictPath2:
		return config.ConflictPath1
	}
	return s
}

// ReceiveJob creates or replaces the mirror of a job a peer owns. def is the job as the owner has it.
func (m *Manager) ReceiveJob(def config.Job, owner string) error {
	c := m.cfg()
	if _, ok := c.Peer(owner); !ok {
		return apperror.NewErrorf("%q is not a peer of this worker", owner)
	}
	if def.Type != config.TypeSync {
		return apperror.NewError("only sync jobs can be mirrored")
	}
	_, sub, ok := def.PeerPath()
	if !ok {
		return apperror.NewError("the job has no peer path")
	}
	sub = strings.TrimPrefix(path.Clean("/"+sub), "/")
	mirror := config.Job{
		Name: def.Name, Type: config.TypeSync, Args: def.Args, TimeoutMinutes: def.TimeoutMinutes, MaxAgeHours: def.MaxAgeHours,
		Path1: filepath.Join(c.SyncServe.Path, filepath.FromSlash(sub)), Path2: config.PeerPrefix + owner,
		Conflict: swapConflict(def.Conflict), ConflictLoser: def.ConflictLoser, MaxDeletePct: def.MaxDeletePct,
		CheckAccess: def.CheckAccess, Force: def.Force, Engine: def.Engine, Owner: owner,
	}
	return m.change(func(c *config.Config) error {
		for i := range c.Jobs {
			if c.Jobs[i].Name != mirror.Name {
				continue
			}
			if c.Jobs[i].Owner != owner {
				return apperror.NewErrorf("a job named %q already exists on this worker", mirror.Name)
			}
			c.Jobs[i] = mirror
			return nil
		}
		c.Jobs = append(c.Jobs, mirror)
		return nil
	})
}

// DropMirror removes the mirror of a job a peer owned.
func (m *Manager) DropMirror(name, owner string) error {
	return m.change(func(c *config.Config) error {
		for i := range c.Jobs {
			if c.Jobs[i].Name == name {
				if c.Jobs[i].Owner != owner {
					return apperror.NewErrorf("job %q is not mirrored from %s", name, owner)
				}
				c.Jobs = slices.Delete(c.Jobs, i, i+1)
				return nil
			}
		}
		return nil
	})
}

// pushMirror sends the definition of an owned sync job to the peer that holds its second path.
func (m *Manager) pushMirror(j config.Job) error {
	c := m.cfg()
	name, ok := hasAPI(c, j)
	if !ok || j.Mirror() {
		return nil
	}
	cl, err := m.peerClient(c, name)
	if err != nil {
		return err
	}
	ctx, cancel := callCtx(m.ctx)
	defer cancel()
	_, err = cl.PushJob(ctx, &pb.PushJobRequest{Definition: peer.DefinitionToProto(j), Owner: c.Worker()})
	return err
}

// afterJobChange keeps the mirror of an owned sync job up to date, in the background so a peer that is down does not
// block the change. Failures are retried by the peer loop.
func (m *Manager) afterJobChange(old *config.Job, j config.Job) {
	if j.Type != config.TypeSync || j.Mirror() {
		return
	}
	m.peers.mu.Lock()
	m.peers.pending[j.Name] = struct{}{}
	delete(m.peers.pushed, j.Name)
	m.peers.mu.Unlock()
	if old != nil && old.Path2 != j.Path2 {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.dropAtPeer(*old)
		}()
	}
	m.wakePeers()
}

func (m *Manager) wakePeers() {
	select {
	case m.peers.wake <- struct{}{}:
	default:
	}
}

// dropAtPeer removes the mirror of a job from its peer.
func (m *Manager) dropAtPeer(j config.Job) {
	c := m.cfg()
	name, ok := hasAPI(c, j)
	if !ok || j.Mirror() {
		return
	}
	cl, err := m.peerClient(c, name)
	if err == nil {
		ctx, cancel := callCtx(m.ctx)
		defer cancel()
		_, err = cl.DropJob(ctx, &pb.PushJobRequest{Definition: peer.DefinitionToProto(j), Owner: c.Worker()})
	}
	if err != nil {
		log.Warn().Err(err).Msgf("the mirror of %s at peer %s was not removed", j.Name, name)
	}
}

// peerLoop pushes mirrors and refreshes the state of mirrored jobs.
func (m *Manager) peerLoop() {
	defer m.wg.Done()
	t := time.NewTicker(peerInterval)
	defer t.Stop()
	for {
		m.syncPeers()
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
		case <-m.peers.wake:
		}
	}
}

func (m *Manager) syncPeers() {
	c := m.cfg()
	known := map[string]bool{}
	for _, j := range c.Jobs {
		if j.Type != config.TypeSync {
			continue
		}
		known[j.Name] = true
		if j.Mirror() {
			m.refreshMirror(c, j)
			continue
		}
		m.peers.mu.Lock()
		_, pending := m.peers.pending[j.Name]
		pushed := m.peers.pushed[j.Name]
		m.peers.mu.Unlock()
		if _, ok := hasAPI(c, j); !ok || (pushed && !pending) {
			continue
		}
		if err := m.pushMirror(j); err != nil {
			log.Warn().Err(err).Msgf("the mirror of %s was not sent to its peer, retrying", j.Name)
			continue
		}
		m.peers.mu.Lock()
		delete(m.peers.pending, j.Name)
		m.peers.pushed[j.Name] = true
		m.peers.mu.Unlock()
	}
	m.peers.mu.Lock()
	for name := range m.peers.remote {
		if !known[name] {
			delete(m.peers.remote, name)
		}
	}
	m.peers.mu.Unlock()
}

// refreshMirror asks the owner of a mirrored job for its state.
func (m *Manager) refreshMirror(c config.Config, j config.Job) {
	r := remoteJob{at: time.Now()}
	cl, err := m.peerClient(c, j.Owner)
	if err == nil {
		ctx, cancel := callCtx(m.ctx)
		var st *pb.ControlStatus
		st, err = cl.GetStatus(ctx, &pb.GetStatusRequest{})
		cancel()
		if err == nil {
			for _, rj := range st.GetJobs() {
				if rj.GetName() == j.Name {
					r.job = rj
				}
			}
			if r.job == nil {
				r.err = "the owner no longer has this job"
			}
		}
	}
	if err != nil {
		r.err = err.Error()
	}
	m.peers.mu.Lock()
	m.peers.remote[j.Name] = r
	m.peers.mu.Unlock()
}

// overlayMirror shows the state of the owner for a mirrored job.
func (m *Manager) overlayMirror(st *JobStatus) {
	m.peers.mu.Lock()
	r, ok := m.peers.remote[st.Name]
	m.peers.mu.Unlock()
	if !ok || r.job == nil {
		return
	}
	pj := r.job
	st.Enabled, st.Paused, st.Schedule = pj.GetEnabled(), pj.GetPaused(), pj.GetSchedule()
	st.Definition.Enabled, st.Definition.Schedule = pj.GetEnabled(), pj.GetSchedule()
	if t := pj.GetNextRunTime(); t != nil && !t.AsTime().IsZero() {
		v := t.AsTime()
		st.NextRun = &v
	}
	if pj.GetActive() != nil {
		a := peer.RunFromProto(pj.GetActive())
		st.Active = &a
	}
	if pj.GetLast() != nil {
		l := peer.RunFromProto(pj.GetLast())
		st.Last = &l
	}
	if t := pj.GetLastSuccessTime(); t != nil && !t.AsTime().IsZero() {
		v := t.AsTime()
		st.LastSuccess = &v
	}
	if d := pj.GetDefinition(); d != nil {
		st.Definition.TimeoutMinutes, st.Definition.MaxAgeHours = int(d.GetTimeoutMinutes()), int(d.GetMaxAgeHours())
	}
}

// MirrorError returns why the state of a mirrored job is not known, empty when it is.
func (m *Manager) MirrorError(name string) string {
	m.peers.mu.Lock()
	defer m.peers.mu.Unlock()
	return m.peers.remote[name].err
}

// ---- forwarding to the owner of a mirrored job -------------------------------------------------------------------

// mirror returns the job when this worker only mirrors it.
func (m *Manager) mirror(name string) (config.Job, bool) {
	j, ok := m.cfg().Job(name)
	return j, ok && j.Mirror()
}

func (m *Manager) forward(j config.Job, f func(context.Context, peer.Client) error) error {
	cl, err := m.peerClient(m.cfg(), j.Owner)
	if err != nil {
		return err
	}
	ctx, cancel := callCtx(m.ctx)
	defer cancel()
	if err := f(ctx, cl); err != nil {
		return apperror.NewErrorf("peer %s: %v", j.Owner, err)
	}
	m.wakePeers()
	return nil
}

func (m *Manager) forwardRun(j config.Job, f func(context.Context, peer.Client) (*pb.Run, error)) (store.Run, error) {
	var run store.Run
	err := m.forward(j, func(ctx context.Context, cl peer.Client) error {
		r, err := f(ctx, cl)
		if err != nil {
			return err
		}
		run = peer.RunFromProto(r)
		return nil
	})
	return run, err
}

// updateMirror changes the settings of a mirrored job at its owner. The paths stay those of the owner, who sends
// the changed mirror back.
func (m *Manager) updateMirror(def config.Job, old config.Job) (JobStatus, error) {
	err := m.forward(old, func(ctx context.Context, cl peer.Client) error {
		st, err := cl.GetStatus(ctx, &pb.GetStatusRequest{})
		if err != nil {
			return err
		}
		var cur *pb.JobDefinition
		for _, j := range st.GetJobs() {
			if j.GetName() == def.Name {
				cur = j.GetDefinition()
			}
		}
		if cur == nil {
			return apperror.NewError("the owner no longer has this job")
		}
		cur.Enabled, cur.Schedule, cur.Args = def.Enabled, def.Schedule, def.Args
		cur.TimeoutMinutes, cur.MaxAgeHours = int32(def.TimeoutMinutes), int32(def.MaxAgeHours) //nolint:gosec // validated
		cur.Conflict, cur.ConflictLoser = swapConflict(def.Conflict), def.ConflictLoser
		cur.MaxDeletePct, cur.CheckAccess, cur.Force = int32(def.MaxDeletePct), def.CheckAccess, def.Force //nolint:gosec // validated
		_, err = cl.UpdateJob(ctx, cur)
		return err
	})
	if err != nil {
		return JobStatus{}, err
	}
	m.refreshMirror(m.cfg(), old)
	return m.jobStatus(def.Name), nil
}

// ---- peers -------------------------------------------------------------------------------------------------------

// SetPeer creates a peer or replaces it.
func (m *Manager) SetPeer(p config.Peer) error {
	return m.change(func(c *config.Config) error {
		c.Peers = slices.Clone(c.Peers)
		for i := range c.Peers {
			if c.Peers[i].Name == p.Name {
				c.Peers[i] = p
				return nil
			}
		}
		c.Peers = append(c.Peers, p)
		return nil
	})
}

// DeletePeer removes a peer that no job uses.
func (m *Manager) DeletePeer(name string) error {
	return m.change(func(c *config.Config) error {
		for _, j := range c.Jobs {
			if p, _, ok := j.PeerPath(); (ok && p == name) || j.Owner == name {
				return apperror.NewErrorf("peer %s is used by job %s", name, j.Name)
			}
		}
		c.Peers = slices.Clone(c.Peers)
		for i := range c.Peers {
			if c.Peers[i].Name == name {
				c.Peers = slices.Delete(c.Peers, i, i+1)
				return nil
			}
		}
		return apperror.NewErrorf("unknown peer %q", name)
	})
}

// SetSyncServe changes how this worker exports a directory.
func (m *Manager) SetSyncServe(s config.SyncServe) error {
	return m.change(func(c *config.Config) error {
		c.SyncServe = s
		return nil
	})
}

// TestPeer checks the connections to a peer.
func (m *Manager) TestPeer(ctx context.Context, name string) (*pb.PeerStatus, error) {
	c := m.cfg()
	p, ok := c.Peer(name)
	if !ok {
		return nil, apperror.NewErrorf("unknown peer %q", name)
	}
	out := &pb.PeerStatus{Name: name}
	var problems []string
	if pw, err := m.DataPassword(p); err != nil {
		problems = append(problems, err.Error())
	} else if err := peer.CheckData(ctx, p, pw); err != nil {
		problems = append(problems, err.Error())
	} else {
		out.DataOk = true
	}
	if p.APIURL != "" {
		start := time.Now()
		cl, err := m.peerClient(c, name)
		if err == nil {
			cctx, cancel := callCtx(ctx)
			var st *pb.ControlStatus
			st, err = cl.GetStatus(cctx, &pb.GetStatusRequest{})
			cancel()
			if err == nil {
				out.ApiOk, out.WorkerName = true, st.GetWorkerName()
				out.LatencyMs = time.Since(start).Milliseconds()
				if st.GetWorkerName() != "" && st.GetWorkerName() != name {
					problems = append(problems, "the peer calls itself "+st.GetWorkerName()+", jobs are mirrored under that name: name the peer entry "+st.GetWorkerName())
				}
			}
		}
		if err != nil {
			problems = append(problems, "control API: "+err.Error())
		}
	}
	out.Error = strings.Join(problems, "; ")
	return out, nil
}

// remoteRuns lists the runs of a mirrored job at its owner.
func (m *Manager) remoteRuns(j config.Job, n int) []store.Run {
	var out []store.Run
	_ = m.forward(j, func(ctx context.Context, cl peer.Client) error {
		res, err := cl.ListRuns(ctx, &pb.ListRunsRequest{Job: j.Name, Limit: int32(n)}) //nolint:gosec // bounded by the caller
		if err != nil {
			return err
		}
		for _, r := range res.GetRuns() {
			out = append(out, peer.RunFromProto(r))
		}
		return nil
	})
	return out
}

// remoteRun looks for a run at the owners of the mirrored jobs, their output lives there.
func (m *Manager) remoteRun(id string) (store.Run, bool) {
	tried := map[string]bool{}
	for _, j := range m.cfg().Jobs {
		if !j.Mirror() || tried[j.Owner] {
			continue
		}
		tried[j.Owner] = true
		var run store.Run
		found := false
		_ = m.forward(j, func(ctx context.Context, cl peer.Client) error {
			r, err := cl.GetRun(ctx, &pb.GetRunRequest{Id: id})
			if err != nil {
				return err
			}
			run, found = peer.RunFromProto(r), true
			return nil
		})
		if found {
			return run, true
		}
	}
	return store.Run{}, false
}

// withPeerPasswords hands the stored passwords of the peers to the runner.
func (m *Manager) withPeerPasswords(ctx context.Context, c config.Config) context.Context {
	return runner.WithPeerPasswords(ctx, m.peerPasswords(c))
}

// peerRetryDelays are the pauses between the attempts to reach a peer at the start of a run. A peer that restarts or a
// network that hiccups should not fail a run that would work a few seconds later.
var peerRetryDelays = []time.Duration{5 * time.Second, 20 * time.Second}

// lockPeerRetrying reserves the job at the peer, trying again when the peer cannot be reached. A peer that is busy is
// not tried again, the run is skipped.
func (m *Manager) lockPeerRetrying(ctx context.Context, a *active, c config.Config, j config.Job, timeout time.Duration) (func(), error) {
	var err error
	for attempt := 0; ; attempt++ {
		var release func()
		release, err = m.lockPeer(ctx, c, j, timeout)
		if err == nil || errors.Is(err, errPeerBusy) || attempt >= len(peerRetryDelays) {
			return release, err
		}
		m.line(a, "peer not reachable, trying again in "+peerRetryDelays[attempt].String()+": "+err.Error())
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(peerRetryDelays[attempt]):
		}
	}
}

// withTunnel tells the runner how unison reaches the peer of a job.
func (m *Manager) withTunnel(ctx context.Context, c config.Config, j config.Job) (context.Context, error) {
	name, _, ok := j.PeerPath()
	if !ok {
		return ctx, nil
	}
	p, found := c.Peer(name)
	if !found || p.APIURL == "" {
		return ctx, apperror.NewErrorf("unison reaches peer %s through its control API, the peer has no API URL", name)
	}
	pw, err := m.secretOrFile(p.APIPasswordSecret, p.APIPasswordFile)
	if err != nil {
		return ctx, err
	}
	return runner.WithTunnel(ctx, runner.Tunnel{URL: p.APIURL, Password: pw, CAFile: p.APICAFile, Job: j.Name, Owner: c.Worker()}), nil
}

// AuthorizeTunnel decides whether a peer may open a tunnel to this worker: the worker has to export a directory for it,
// the job has to be one the peer owns, and the peer has to hold the reservation of the job, which it takes before every
// run and which expires.
func (m *Manager) AuthorizeTunnel(job, owner string) error {
	c := m.cfg()
	if !c.SyncServe.Tunnel {
		return apperror.NewError("this worker does not serve unison tunnels, set syncServe.tunnel")
	}
	if _, ok := c.Peer(owner); !ok {
		return apperror.NewErrorf("%q is not a peer of this worker", owner)
	}
	j, ok := c.Job(job)
	if !ok || j.Owner != owner || !j.Unison() {
		return apperror.NewErrorf("there is no unison job %q that %s owns", job, owner)
	}
	if holder, locked := m.lockedByPeer(job); !locked || holder != owner {
		return apperror.NewError("the job is not reserved for the caller")
	}
	return nil
}
