package manager

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/config"
	"github.com/Valentin-Kaiser/backup/pkg/peer"
	pb "github.com/Valentin-Kaiser/backup/pkg/proto/api/v1"
	"github.com/Valentin-Kaiser/backup/pkg/runner"
	"github.com/Valentin-Kaiser/backup/pkg/store"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakePeer records the calls a worker makes to its peer.
type fakePeer struct {
	mu      sync.Mutex
	calls   []string
	grant   bool
	holder  string
	status  *pb.ControlStatus
	pushed  []*pb.PushJobRequest
	updated []*pb.JobDefinition
}

func (f *fakePeer) record(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakePeer) called(s string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.calls, s)
}

func (f *fakePeer) GetStatus(context.Context, *pb.GetStatusRequest) (*pb.ControlStatus, error) {
	f.record("GetStatus")
	return f.status, nil
}
func (f *fakePeer) ListRuns(_ context.Context, in *pb.ListRunsRequest) (*pb.ListRunsResponse, error) {
	f.record("ListRuns " + in.GetJob())
	return &pb.ListRunsResponse{Runs: []*pb.Run{{Id: "r1", Job: in.GetJob(), Status: pb.RunStatus_RUN_STATUS_SUCCESS}}}, nil
}
func (f *fakePeer) GetRun(_ context.Context, in *pb.GetRunRequest) (*pb.Run, error) {
	f.record("GetRun " + in.GetId())
	return &pb.Run{Id: in.GetId(), Output: []string{"line"}}, nil
}
func (f *fakePeer) Watch(ctx context.Context, _ *pb.WatchRequest, _ chan<- *pb.Event) error {
	<-ctx.Done()
	return nil
}

func (f *fakePeer) StartJob(_ context.Context, in *pb.JobRequest) (*pb.Run, error) {
	f.record("StartJob " + in.GetJob())
	return &pb.Run{Id: "remote", Job: in.GetJob(), Status: pb.RunStatus_RUN_STATUS_QUEUED}, nil
}
func (f *fakePeer) CancelJob(_ context.Context, in *pb.JobRequest) (*emptypb.Empty, error) {
	f.record("CancelJob " + in.GetJob())
	return &emptypb.Empty{}, nil
}
func (f *fakePeer) UnlockJob(_ context.Context, in *pb.JobRequest) (*emptypb.Empty, error) {
	f.record("UnlockJob " + in.GetJob())
	return &emptypb.Empty{}, nil
}
func (f *fakePeer) PauseJob(_ context.Context, in *pb.JobRequest) (*emptypb.Empty, error) {
	f.record("PauseJob " + in.GetJob())
	return &emptypb.Empty{}, nil
}
func (f *fakePeer) ResumeJob(_ context.Context, in *pb.JobRequest) (*emptypb.Empty, error) {
	f.record("ResumeJob " + in.GetJob())
	return &emptypb.Empty{}, nil
}
func (f *fakePeer) ResyncJob(_ context.Context, in *pb.JobRequest) (*pb.Run, error) {
	f.record("ResyncJob " + in.GetJob())
	return &pb.Run{Id: "resync", Job: in.GetJob()}, nil
}
func (f *fakePeer) UpdateJob(_ context.Context, in *pb.JobDefinition) (*pb.Job, error) {
	f.record("UpdateJob " + in.GetName())
	f.mu.Lock()
	f.updated = append(f.updated, in)
	f.mu.Unlock()
	return &pb.Job{Name: in.GetName()}, nil
}
func (f *fakePeer) DeleteJob(_ context.Context, in *pb.JobRequest) (*emptypb.Empty, error) {
	f.record("DeleteJob " + in.GetJob())
	return &emptypb.Empty{}, nil
}
func (f *fakePeer) AcquireSyncLock(_ context.Context, in *pb.SyncLockRequest) (*pb.SyncLockResponse, error) {
	f.record("AcquireSyncLock " + in.GetJob() + " " + in.GetOwner())
	return &pb.SyncLockResponse{Granted: f.grant, Holder: f.holder}, nil
}
func (f *fakePeer) ReleaseSyncLock(_ context.Context, in *pb.SyncLockRequest) (*emptypb.Empty, error) {
	f.record("ReleaseSyncLock " + in.GetJob())
	return &emptypb.Empty{}, nil
}
func (f *fakePeer) PushJob(_ context.Context, in *pb.PushJobRequest) (*emptypb.Empty, error) {
	f.record("PushJob " + in.GetDefinition().GetName())
	f.mu.Lock()
	f.pushed = append(f.pushed, in)
	f.mu.Unlock()
	return &emptypb.Empty{}, nil
}
func (f *fakePeer) DropJob(_ context.Context, in *pb.PushJobRequest) (*emptypb.Empty, error) {
	f.record("DropJob " + in.GetDefinition().GetName())
	return &emptypb.Empty{}, nil
}

var _ peer.Client = (*fakePeer)(nil)

// peerManager returns a manager of worker "west" with a peer "east" that has a control API, plus its editable
// configuration.
func peerManager(t *testing.T, fp *fakePeer) (*Manager, *config.Config) {
	t.Helper()
	m, j := syncManager(t, "ok")
	c := m.cfg()
	c.WorkerName = "west"
	c.Mode, c.PasswordFile, c.API.PasswordFile = config.ModePortal, "", "/run/secrets/portal_password"
	c.SyncServe.Path = t.TempDir()
	pwFile := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pwFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.Peers = []config.Peer{{Name: "east", URL: "https://east:9081", Username: "sync", PasswordFile: pwFile, APIURL: "https://east:9080"}}
	c.Jobs = slices.Clone(c.Jobs)
	var mu sync.Mutex
	m.cfg = func() config.Config { mu.Lock(); defer mu.Unlock(); return c }
	m.SetUpdater(func(change func(*config.Config) error) error {
		mu.Lock()
		defer mu.Unlock()
		n := c
		n.Jobs, n.Peers = slices.Clone(c.Jobs), slices.Clone(c.Peers)
		if err := change(&n); err != nil {
			return err
		}
		if err := n.Validate(); err != nil {
			return err
		}
		c = n
		return nil
	})
	m.SetDialer(func(config.Peer, string) (peer.Client, error) { return fp, nil })
	_ = j
	return m, &c
}

// ownJob replaces the first sync job of the configuration with one that syncs with the peer.
func ownJob(t *testing.T, m *Manager) config.Job {
	t.Helper()
	j := config.Job{Name: "mirror", Type: config.TypeSync, Enabled: true, Schedule: "*/5 * * * *", Path1: t.TempDir(), Path2: "peer:east/data"}
	if _, err := m.UpdateJob(j); err != nil {
		t.Fatal(err)
	}
	return j
}

func TestSyncWithPeerReservesTheJobAtThePeer(t *testing.T) {
	fp := &fakePeer{grant: true}
	m, _ := peerManager(t, fp)
	ownJob(t, m)
	t.Setenv("GO_FAKE_RESTIC", "1")
	run, err := m.Trigger("mirror", "manual", true)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.StatusSuccess {
		t.Fatalf("run = %+v", run)
	}
	if !fp.called("AcquireSyncLock mirror west") || !fp.called("ReleaseSyncLock mirror") {
		t.Errorf("calls = %q", fp.calls)
	}
}

func TestSyncIsSkippedWhileThePeerHoldsTheJob(t *testing.T) {
	fp := &fakePeer{grant: false, holder: "east"}
	m, _ := peerManager(t, fp)
	ownJob(t, m)
	run, _ := m.Trigger("mirror", "manual", true)
	if run.Status != store.StatusSkipped {
		t.Fatalf("run = %+v", run)
	}
	if fp.called("ReleaseSyncLock mirror") {
		t.Error("a reservation that was not granted must not be released")
	}
}

func TestOwnedJobIsMirroredAtThePeer(t *testing.T) {
	fp := &fakePeer{grant: true}
	m, _ := peerManager(t, fp)
	j := ownJob(t, m)
	m.syncPeers()
	if len(fp.pushed) != 1 || fp.pushed[0].Owner != "west" || fp.pushed[0].Definition.Path1 != j.Path1 {
		t.Fatalf("pushed = %+v", fp.pushed)
	}
	// Once the peer has it nothing is sent again.
	m.syncPeers()
	if len(fp.pushed) != 1 {
		t.Errorf("pushed again: %d", len(fp.pushed))
	}
	// A change sends it again.
	j.Schedule = "0 * * * *"
	if _, err := m.UpdateJob(j); err != nil {
		t.Fatal(err)
	}
	m.syncPeers()
	if len(fp.pushed) != 2 {
		t.Errorf("pushed = %d, want 2", len(fp.pushed))
	}
}

func TestReceiveJobCreatesAMirror(t *testing.T) {
	fp := &fakePeer{}
	m, _ := peerManager(t, fp)
	c := m.cfg()
	// The peer is called "east" here, it owns the job.
	def := config.Job{Name: "shared", Type: config.TypeSync, Enabled: true, Schedule: "*/5 * * * *", Path1: "/data", Path2: "peer:west/projects/../projects", Conflict: config.ConflictPath1}
	if err := m.ReceiveJob(def, "east"); err != nil {
		t.Fatal(err)
	}
	got, ok := m.cfg().Job("shared")
	if !ok || !got.Mirror() || got.Owner != "east" || got.Enabled || got.Schedule != "" {
		t.Fatalf("mirror = %+v", got)
	}
	if want := filepath.Join(c.SyncServe.Path, "projects"); got.Path1 != want || got.Path2 != "peer:east" {
		t.Errorf("paths = %q %q, want %q peer:east", got.Path1, got.Path2, want)
	}
	if got.Conflict != config.ConflictPath2 {
		t.Errorf("conflict = %q, path1 of the owner is path2 of the mirror", got.Conflict)
	}
	// A path that leaves the exported directory stays inside it.
	def.Name, def.Path2 = "escape", "peer:west/../../etc"
	if err := m.ReceiveJob(def, "east"); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.cfg().Job("escape"); got.Path1 != filepath.Join(c.SyncServe.Path, "etc") {
		t.Errorf("path1 = %q", got.Path1)
	}
	if err := m.ReceiveJob(def, "stranger"); err == nil {
		t.Error("only peers may send jobs")
	}
	// A job of this worker is never replaced.
	def.Name = "mirror"
	if err := m.ReceiveJob(def, "east"); err == nil {
		t.Error("a local job must not be replaced by a mirror")
	}
	if err := m.DropMirror("mirror", "east"); err == nil {
		t.Error("a local job must not be dropped by a peer")
	}
	if err := m.DropMirror("shared", "east"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.cfg().Job("shared"); ok {
		t.Error("mirror not removed")
	}
}

func TestMirrorForwardsToTheOwner(t *testing.T) {
	fp := &fakePeer{grant: true}
	m, _ := peerManager(t, fp)
	def := config.Job{Name: "shared", Type: config.TypeSync, Path1: "/data", Path2: "peer:west"}
	if err := m.ReceiveJob(def, "east"); err != nil {
		t.Fatal(err)
	}

	run, err := m.Trigger("shared", "manual", false)
	if err != nil || run.ID != "remote" {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	if err := m.Cancel("shared"); err != nil {
		t.Fatal(err)
	}
	if err := m.Pause("shared"); err != nil {
		t.Fatal(err)
	}
	if err := m.Resume("shared"); err != nil {
		t.Fatal(err)
	}
	if r, err := m.ResyncJob("shared"); err != nil || r.ID != "resync" {
		t.Fatalf("run = %+v, err = %v", r, err)
	}
	for _, want := range []string{"StartJob shared", "CancelJob shared", "PauseJob shared", "ResumeJob shared", "ResyncJob shared"} {
		if !fp.called(want) {
			t.Errorf("%q was not forwarded: %q", want, fp.calls)
		}
	}
	if runs := m.Runs("shared", 10); len(runs) != 1 || runs[0].ID != "r1" {
		t.Errorf("runs = %+v", runs)
	}
	if r, ok := m.Run("unknown"); !ok || len(r.Output) != 1 {
		t.Errorf("run = %+v, %v", r, ok)
	}
	// Mirrors are never scheduled and never created by hand.
	if _, err := m.CreateJob(config.Job{Name: "x", Type: config.TypeSync, Owner: "east", Path1: "/a", Path2: "peer:east"}); err == nil {
		t.Error("a mirror must come from its owner")
	}
}

func TestMirrorUpdateChangesTheOwner(t *testing.T) {
	fp := &fakePeer{status: &pb.ControlStatus{Jobs: []*pb.Job{{
		Name:       "shared",
		Enabled:    true,
		Schedule:   "*/5 * * * *",
		Definition: &pb.JobDefinition{Name: "shared", Type: "sync", Path1: "/data", Path2: "peer:west", Conflict: "path1"},
		Last:       &pb.Run{Id: "x", Status: pb.RunStatus_RUN_STATUS_SUCCESS, FinishTime: timestamppb.New(time.Now())},
	}}}}
	m, _ := peerManager(t, fp)
	if err := m.ReceiveJob(config.Job{Name: "shared", Type: config.TypeSync, Path1: "/data", Path2: "peer:west", Conflict: "path1"}, "east"); err != nil {
		t.Fatal(err)
	}
	m.syncPeers()
	var st JobStatus
	for _, s := range m.Status() {
		if s.Name == "shared" {
			st = s
		}
	}
	if !st.Enabled || st.Schedule != "*/5 * * * *" || st.Last == nil || st.Last.Status != store.StatusSuccess {
		t.Fatalf("the mirror must show the state of the owner: %+v", st)
	}

	mirror, _ := m.cfg().Job("shared")
	mirror.Schedule, mirror.Enabled, mirror.Conflict = "0 * * * *", true, config.ConflictPath2
	if _, err := m.UpdateJob(mirror); err != nil {
		t.Fatal(err)
	}
	if len(fp.updated) != 1 {
		t.Fatalf("updated = %+v", fp.updated)
	}
	u := fp.updated[0]
	if u.Schedule != "0 * * * *" || u.Path1 != "/data" || u.Conflict != "path1" {
		t.Errorf("the owner must get the settings, keep its paths and see its own side of the conflict policy: %+v", u)
	}

	if err := m.DeleteJob("shared"); err != nil {
		t.Fatal(err)
	}
	if !fp.called("DeleteJob shared") {
		t.Error("the owner must delete the job")
	}
	if _, ok := m.cfg().Job("shared"); ok {
		t.Error("mirror not removed")
	}
}

func TestPeerLocks(t *testing.T) {
	m, _ := peerManager(t, &fakePeer{})
	if ok, _ := m.AcquireSyncLock("mirror2", "east", time.Minute); !ok {
		t.Fatal("free job must be granted")
	}
	if ok, holder := m.AcquireSyncLock("mirror2", "other", time.Minute); ok || holder != "east" {
		t.Errorf("granted = %v, holder = %q", ok, holder)
	}
	if ok, _ := m.AcquireSyncLock("mirror2", "east", time.Minute); !ok {
		t.Error("the holder may renew")
	}
	m.ReleaseSyncLock("mirror2", "other")
	if _, locked := m.lockedByPeer("mirror2"); !locked {
		t.Error("only the holder releases")
	}
	m.ReleaseSyncLock("mirror2", "east")
	if ok, _ := m.AcquireSyncLock("mirror2", "other", time.Minute); !ok {
		t.Error("released job must be granted")
	}
	// A reservation expires.
	m.peers.mu.Lock()
	m.peers.locks["mirror2"] = peerLock{owner: "east", until: time.Now().Add(-time.Second)}
	m.peers.mu.Unlock()
	if _, locked := m.lockedByPeer("mirror2"); locked {
		t.Error("expired reservation")
	}
	// A job of this worker that is running cannot be reserved, and one reserved by the peer cannot be started.
	if ok, _ := m.AcquireSyncLock("mirror", "east", time.Minute); !ok {
		t.Fatal("idle job")
	}
	if _, err := m.Trigger("mirror", "manual", true); err == nil {
		t.Error("a job reserved by the peer must not start")
	}
}

func TestPeersAndSyncServe(t *testing.T) {
	m, _ := peerManager(t, &fakePeer{})
	if err := m.SetPeer(config.Peer{Name: "north", URL: "http://north:9081"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.cfg().Peer("north"); !ok {
		t.Fatal("peer not stored")
	}
	if err := m.SetPeer(config.Peer{Name: "bad name", URL: "http://x"}); err == nil {
		t.Error("invalid peer accepted")
	}
	if err := m.DeletePeer("north"); err != nil {
		t.Fatal(err)
	}
	ownJob(t, m)
	if err := m.DeletePeer("east"); err == nil {
		t.Error("a peer that a job uses must not be removed")
	}
	if err := m.SetSyncServe(config.SyncServe{Enabled: true, Path: m.cfg().SyncServe.Path, Port: 9081}); err == nil {
		t.Error("a sync server without credentials must be refused")
	}
	pw := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pw, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.SetSyncServe(config.SyncServe{Enabled: true, Path: m.cfg().SyncServe.Path, Port: 9081, Username: "sync", PasswordFile: pw}); err != nil {
		t.Fatal(err)
	}
}

func unisonRunner(m *Manager) {
	m.local = runner.Runner{
		Binary: os.Args[0], Prefix: []string{"-test.run=TestHelperProcess", "--"},
		UnisonBinary: os.Args[0], UnisonPrefix: []string{"-test.run=TestHelperProcess", "--"},
	}
}

func TestUnisonJobRunsAndExplainsItsExitCode(t *testing.T) {
	m, j := syncManager(t, "ok")
	unisonRunner(m)
	c := m.cfg()
	c.Jobs[len(c.Jobs)-1].Engine = config.EngineUnison
	m.cfg = func() config.Config { return c }
	_ = j
	run, err := m.Trigger("mirror", "manual", true)
	if err != nil || run.Status != store.StatusSuccess {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	if _, err := os.Stat(runner.SyncStateDir(c, "mirror")); err != nil {
		t.Errorf("the state directory must exist: %v", err)
	}

	t.Setenv("GO_FAKE_MODE", "fail") // exit code 1 of unison: files were skipped
	run, _ = m.Trigger("mirror", "manual", true)
	if run.Status != store.StatusFailed || !strings.Contains(run.Error, "skipped") {
		t.Errorf("run = %+v", run)
	}
}

func TestUnisonWithAPeerGetsTheTunnelAndRetriesTheReservation(t *testing.T) {
	fp := &fakePeer{grant: true}
	m, _ := peerManager(t, fp)
	unisonRunner(m)
	old := peerRetryDelays
	peerRetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { peerRetryDelays = old })

	pwFile := filepath.Join(t.TempDir(), "api")
	if err := os.WriteFile(pwFile, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.SetPeer(config.Peer{Name: "east", URL: "https://east:9081", Username: "sync", APIURL: "https://east:9080", APIPasswordFile: pwFile}); err != nil {
		t.Fatal(err)
	}
	j := config.Job{Name: "mirror", Type: config.TypeSync, Engine: config.EngineUnison, Enabled: true, Schedule: "*/5 * * * *", Path1: t.TempDir(), Path2: "peer:east/data"}
	if _, err := m.UpdateJob(j); err != nil {
		t.Fatal(err)
	}
	ctx, err := m.withTunnel(context.Background(), m.cfg(), j)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Value(struct{}{}) != nil {
		t.Error("unexpected value")
	}

	// The first reservation attempts fail, the third works.
	failing := &flakyPeer{fakePeer: fp, fail: 2}
	m.SetDialer(func(config.Peer, string) (peer.Client, error) { return failing, nil })
	run, err := m.Trigger("mirror", "manual", true)
	if err != nil || run.Status != store.StatusSuccess {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	if failing.calls != 3 {
		t.Errorf("reservations = %d, want 3", failing.calls)
	}
	found := false
	for _, l := range run.Output {
		found = found || strings.Contains(l, "trying again")
	}
	if !found {
		t.Errorf("the retries must show in the output: %q", run.Output)
	}

	// A peer that stays away fails the run with the reason, a busy peer skips it.
	failing.fail, failing.calls = 10, 0
	if run, _ = m.Trigger("mirror", "manual", true); run.Status != store.StatusFailed || failing.calls != 3 {
		t.Errorf("run = %+v, reservations = %d", run, failing.calls)
	}
	fp.grant = false
	failing.fail, failing.calls = 0, 0
	if run, _ = m.Trigger("mirror", "manual", true); run.Status != store.StatusSkipped || failing.calls != 1 {
		t.Errorf("run = %+v, reservations = %d", run, failing.calls)
	}
}

type flakyPeer struct {
	*fakePeer
	fail  int
	calls int
}

func (f *flakyPeer) AcquireSyncLock(ctx context.Context, in *pb.SyncLockRequest) (*pb.SyncLockResponse, error) {
	f.calls++
	if f.calls <= f.fail {
		return nil, context.DeadlineExceeded
	}
	return f.fakePeer.AcquireSyncLock(ctx, in)
}

func TestTunnelIsOnlyForAPeerThatReservedItsJob(t *testing.T) {
	m, _ := peerManager(t, &fakePeer{})
	c := m.cfg()
	if err := m.AuthorizeTunnel("shared", "east"); err == nil {
		t.Error("the tunnel must be switched on")
	}
	if err := m.SetSyncServe(config.SyncServe{Tunnel: true, Path: c.SyncServe.Path, Address: "0.0.0.0", Port: 9081}); err != nil {
		t.Fatal(err)
	}
	if err := m.AuthorizeTunnel("shared", "east"); err == nil {
		t.Error("there is no such job")
	}
	def := config.Job{Name: "shared", Type: config.TypeSync, Engine: config.EngineUnison, Path1: "/data", Path2: "peer:west/data"}
	if err := m.ReceiveJob(def, "east"); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.cfg().Job("shared"); got.Engine != config.EngineUnison || got.Owner != "east" {
		t.Fatalf("the mirror keeps the engine: %+v", got)
	}
	if err := m.AuthorizeTunnel("shared", "east"); err == nil {
		t.Error("the peer has not reserved the job")
	}
	m.AcquireSyncLock("shared", "east", time.Minute)
	if err := m.AuthorizeTunnel("shared", "east"); err != nil {
		t.Errorf("reserved by the owner: %v", err)
	}
	if err := m.AuthorizeTunnel("shared", "stranger"); err == nil {
		t.Error("only the owner of the job may use the tunnel")
	}
	// A job of this worker is never reachable through the tunnel.
	if err := m.AuthorizeTunnel("mirror", "east"); err == nil {
		t.Error("a local job must not be reachable")
	}
}
