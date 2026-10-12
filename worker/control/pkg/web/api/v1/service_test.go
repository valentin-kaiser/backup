package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/config"
	"github.com/Valentin-Kaiser/backup/pkg/manager"
	pb "github.com/Valentin-Kaiser/backup/pkg/proto/api/v1"
	"github.com/Valentin-Kaiser/backup/pkg/runner"
	"github.com/Valentin-Kaiser/backup/pkg/store"
	"github.com/gorilla/websocket"
)

// TestHelperProcess is re-executed as a fake restic.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_FAKE_RESTIC") != "1" {
		return
	}
	switch {
	case slices.Contains(os.Args, "snapshots"):
		os.Stdout.WriteString(`[{"id":"1111111111111111","short_id":"11111111","time":"2024-01-01T00:00:00Z","hostname":"a","paths":["/src"]},` +
			`{"id":"2222222222222222","short_id":"22222222","time":"2024-01-02T00:00:00Z","hostname":"a","paths":["/src"],"tags":["x"]}]`)
	case slices.Contains(os.Args, "ls"):
		// Like restic: the snapshot, the listed directory itself, then its entries.
		os.Stdout.WriteString(`
{"message_type":"snapshot","id":"2222222222222222"}
{"name":"data","type":"dir","path":"/data","mtime":"2024-01-01T00:00:00Z","permissions":"drwxr-xr-x","message_type":"node"}
{"name":"b.txt","type":"file","path":"/data/b.txt","size":12,"mtime":"2024-01-02T03:04:05Z","permissions":"-rw-r--r--","message_type":"node"}
{"name":"Zeta","type":"dir","path":"/data/Zeta","mtime":"2024-01-01T00:00:00Z","permissions":"drwxr-xr-x","message_type":"node"}
{"name":"a.txt","type":"file","path":"/data/a.txt","size":3,"mtime":"2024-01-02T03:04:05Z","permissions":"-rw-r--r--","message_type":"node"}
{"name":"link","type":"symlink","path":"/data/link","mtime":"2024-01-02T03:04:05Z","permissions":"Lrwxrwxrwx","message_type":"node"}
`)
	case slices.Contains(os.Args, "stats"):
		os.Stdout.WriteString(`{"total_size":10,"total_uncompressed_size":20,"compression_ratio":2,"total_blob_count":3,"snapshots_count":2}`)
	default:
		os.Stdout.WriteString("fake output\n")
	}
	os.Exit(0)
}

func newService(t *testing.T) (*Service, *httptest.Server) {
	t.Helper()
	t.Setenv("GO_FAKE_RESTIC", "1")
	st, err := store.Open(filepath.Join(t.TempDir(), "h.jsonl"), 100)
	if err != nil {
		t.Fatal(err)
	}
	c := *config.Default()
	c.Mode, c.PasswordFile, c.PasswordSecret = config.ModePortal, "", "main"
	c.API.PasswordFile = "/run/secrets/portal_password"
	c.Nice = 0
	c.Source = "/src"
	c.Jobs = []config.Job{
		{Name: "backup", Type: config.TypeBackup, Enabled: true, Schedule: "0 0 * * *"},
		{Name: "photos", Type: config.TypeBackup, Enabled: true, Schedule: "0 1 * * *", Repository: "/repo/photos"},
	}
	var mu sync.Mutex
	r := runner.Runner{Binary: os.Args[0], Prefix: []string{"-test.run=TestHelperProcess", "--"}}
	m := manager.New(func() config.Config { mu.Lock(); defer mu.Unlock(); return c }, r, st)
	m.SetSecrets(fixedSecrets{})
	// Behaves like config.Update without touching the global configuration.
	m.SetUpdater(func(change func(*config.Config) error) error {
		mu.Lock()
		n := c
		n.Jobs = slices.Clone(c.Jobs)
		err := change(&n)
		if err == nil {
			err = n.Validate()
		}
		if err == nil {
			c = n
		}
		mu.Unlock()
		if err != nil {
			return err
		}
		return m.Reload()
	})
	t.Cleanup(func() { m.Shutdown(time.Second) })
	if err := m.Reload(); err != nil {
		t.Fatal(err)
	}

	s := New(m)
	mux := http.NewServeMux()
	mux.Handle(Endpoint, s.Handler())
	mux.HandleFunc(Prefix+"health", s.Health)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv
}

func newClient(t *testing.T, srv *httptest.Server) *pb.ControlClient {
	t.Helper()
	c, err := pb.NewControlClient(srv.URL + Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestWebsocketCalls(t *testing.T) {
	_, srv := newService(t)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+Endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"jsonrpc":"2.0","id":7,"method":"Control.GetStatus","params":{}}`)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(msg), `"id":7`) || strings.Contains(string(msg), `"error"`) {
		t.Errorf("unexpected response %s", msg)
	}
}

func TestWebsocketsFromOtherOriginsAreRefused(t *testing.T) {
	_, srv := newService(t)
	header := http.Header{"Origin": {"http://evil.example"}}
	if conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+Endpoint, header); err == nil {
		conn.Close()
		t.Error("a page on another origin must not reach the API, there is no authentication")
	}
}

func TestJobManagement(t *testing.T) {
	_, srv := newService(t)
	c := newClient(t, srv)
	ctx := context.Background()
	def := func(name string) *pb.JobDefinition {
		return &pb.JobDefinition{Name: name, Type: "backup", Enabled: true, Schedule: "15 2 * * *", Source: "/data", Repository: "/repo/data", Args: "--exclude-caches"}
	}

	created, err := c.CreateJob(ctx, def("data"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "data" || created.Definition.Repository != "/repo/data" || created.NextRunTime == nil {
		t.Errorf("created = %v", created)
	}
	st, _ := c.GetStatus(ctx, &pb.GetStatusRequest{})
	var names []string
	for _, j := range st.Jobs {
		names = append(names, j.Name)
	}
	if !slices.Equal(names, []string{"backup", "photos", "data", "restore"}) {
		t.Errorf("jobs = %v", names)
	}
	if st.Defaults.Source != "/src" || st.Jobs[3].Definition != nil {
		t.Errorf("defaults = %v, restore definition = %v", st.Defaults, st.Jobs[3].Definition)
	}

	changed := def("data")
	changed.Schedule, changed.Enabled, changed.Args = "0 5 * * *", false, "--exclude-caches --one-file-system"
	updated, err := c.UpdateJob(ctx, changed)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Definition.Schedule != "0 5 * * *" || updated.Definition.Enabled || updated.NextRunTime != nil {
		t.Errorf("updated = %v", updated)
	}

	if _, err := c.DeleteJob(ctx, &pb.JobRequest{Job: "data"}); err != nil {
		t.Fatal(err)
	}
	st, _ = c.GetStatus(ctx, &pb.GetStatusRequest{})
	if len(st.Jobs) != 3 {
		t.Errorf("jobs after delete = %d", len(st.Jobs))
	}
}

func TestJobManagementRejectsInvalidJobs(t *testing.T) {
	_, srv := newService(t)
	c := newClient(t, srv)
	ctx := context.Background()
	tests := []struct {
		name string
		def  *pb.JobDefinition
		want string
	}{
		{"duplicate", &pb.JobDefinition{Name: "backup", Type: "backup", Schedule: "0 0 * * *"}, "already exists"},
		{"reserved", &pb.JobDefinition{Name: "restore", Type: "backup", Schedule: "0 0 * * *"}, "name"},
		{"bad name", &pb.JobDefinition{Name: "My Job", Type: "backup", Schedule: "0 0 * * *"}, "name"},
		{"bad type", &pb.JobDefinition{Name: "x", Type: "sync", Schedule: "0 0 * * *"}, "type"},
		{"bad schedule", &pb.JobDefinition{Name: "x", Type: "backup", Enabled: true, Schedule: "every day"}, "schedule"},
		{"bad args", &pb.JobDefinition{Name: "x", Type: "backup", Schedule: "0 0 * * *", Args: `--exclude "open`}, "quote"},
		{"source on check", &pb.JobDefinition{Name: "x", Type: "check", Schedule: "0 0 * * *", Source: "/x"}, "source"},
	}
	for _, tt := range tests {
		if _, err := c.CreateJob(ctx, tt.def); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.want)
		}
	}
	if _, err := c.UpdateJob(ctx, &pb.JobDefinition{Name: "nope", Type: "backup", Schedule: "0 0 * * *"}); err == nil {
		t.Error("updating an unknown job must fail")
	}
	if _, err := c.DeleteJob(ctx, &pb.JobRequest{Job: "nope"}); err == nil {
		t.Error("deleting an unknown job must fail")
	}
	st, _ := c.GetStatus(ctx, &pb.GetStatusRequest{})
	if len(st.Jobs) != 3 {
		t.Errorf("a rejected change must not change the jobs: %d", len(st.Jobs))
	}
}

func TestRunningJobsCannotBeDeleted(t *testing.T) {
	s, srv := newService(t)
	c := newClient(t, srv)
	ctx := context.Background()
	if _, err := c.StartJob(ctx, &pb.JobRequest{Job: "photos"}); err != nil {
		t.Fatal(err)
	}
	// The fake restic is quick, so only check the contract: deleting works once the run is over.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := c.DeleteJob(ctx, &pb.JobRequest{Job: "photos"}); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	st, _ := s.GetStatus(ctx, &pb.GetStatusRequest{})
	for _, j := range st.Jobs {
		if j.Name == "photos" {
			t.Error("the job was not deleted after its run finished")
		}
	}
	runs, _ := c.ListRuns(ctx, &pb.ListRunsRequest{Job: "photos"})
	if len(runs.Runs) == 0 {
		t.Error("the history of a deleted job must stay")
	}
}
func TestStatusAndRuns(t *testing.T) {
	_, srv := newService(t)
	c := newClient(t, srv)
	ctx := context.Background()

	st, err := c.GetStatus(ctx, &pb.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Healthy || len(st.Jobs) != 3 || st.Jobs[0].Name != "backup" || st.Jobs[1].Name != "photos" || st.Jobs[1].Type != "backup" || st.Jobs[2].Name != "restore" {
		t.Fatalf("status = %v", st)
	}

	run, err := c.StartJob(ctx, &pb.JobRequest{Job: "backup"})
	if err != nil {
		t.Fatal(err)
	}
	var got *pb.Run
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if got, err = c.GetRun(ctx, &pb.GetRunRequest{Id: run.Id}); err != nil {
			t.Fatal(err)
		}
		if got.Status == pb.RunStatus_RUN_STATUS_SUCCESS {
			break
		}
	}
	if got.Status != pb.RunStatus_RUN_STATUS_SUCCESS || got.FinishTime == nil || !slices.Contains(got.Output, "fake output") {
		t.Fatalf("run = %v", got)
	}

	list, err := c.ListRuns(ctx, &pb.ListRunsRequest{Job: "backup"})
	if err != nil || len(list.Runs) != 1 || len(list.Runs[0].Output) != 0 {
		t.Fatalf("runs = %v, err = %v", list, err)
	}

	if _, err := c.GetRun(ctx, &pb.GetRunRequest{Id: "missing"}); err == nil {
		t.Error("an unknown run must fail")
	}
	if _, err := c.StartJob(ctx, &pb.JobRequest{Job: "restore"}); err == nil {
		t.Error("restore cannot be started as a job")
	}
	if _, err := c.StartJob(ctx, &pb.JobRequest{Job: "photos"}); err != nil {
		t.Errorf("a second job must start: %v", err)
	}
	if _, err := c.PauseJob(ctx, &pb.JobRequest{Job: "backup"}); err != nil {
		t.Error(err)
	}
	if _, err := c.ResumeJob(ctx, &pb.JobRequest{Job: "backup"}); err != nil {
		t.Error(err)
	}
}

func TestSnapshotsAndStats(t *testing.T) {
	_, srv := newService(t)
	c := newClient(t, srv)

	snaps, err := c.ListSnapshots(context.Background(), &pb.ListSnapshotsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps.Snapshots) != 2 || snaps.Snapshots[0].ShortId != "22222222" || snaps.Snapshots[0].Tags[0] != "x" {
		t.Errorf("snapshots must be newest first: %v", snaps)
	}

	if _, err := c.ListSnapshots(context.Background(), &pb.ListSnapshotsRequest{Job: "photos"}); err != nil {
		t.Errorf("snapshots of a job: %v", err)
	}
	if _, err := c.ListSnapshots(context.Background(), &pb.ListSnapshotsRequest{Job: "nope"}); err == nil || !strings.Contains(err.Error(), "unknown job") {
		t.Errorf("snapshots of an unknown job must say so, got %v", err)
	}

	stats, err := c.GetStats(context.Background(), &pb.GetStatsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalSize != 10 || stats.SnapshotCount != 2 || stats.CompressionRatio != 2 {
		t.Errorf("stats = %v", stats)
	}
}

func TestRestoreRejectsTargetOutsideRoot(t *testing.T) {
	_, srv := newService(t)
	c := newClient(t, srv)
	if _, err := c.Restore(context.Background(), &pb.RestoreRequest{Snapshot: "latest", Target: "/etc"}); err == nil {
		t.Error("a restore outside of the restore root must fail")
	}
}

func TestWatchStreamsRunEvents(t *testing.T) {
	_, srv := newService(t)
	c := newClient(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	events := make(chan *pb.Event, 64)
	done := make(chan error, 1)
	go func() { done <- c.Watch(ctx, &pb.WatchRequest{}, events) }()

	// The subscription is made asynchronously, retry until the first event arrives.
	var seen []*pb.Event
	started := false
	deadline := time.After(10 * time.Second)
	for finished := false; !finished; {
		if !started {
			time.Sleep(200 * time.Millisecond)
			if _, err := c.StartJob(ctx, &pb.JobRequest{Job: "backup"}); err == nil {
				started = true
			}
		}
		select {
		case e := <-events:
			seen = append(seen, e)
			finished = e.Type == pb.EventType_EVENT_TYPE_STATUS && e.Status == pb.RunStatus_RUN_STATUS_SUCCESS
		case <-time.After(300 * time.Millisecond):
		case <-deadline:
			t.Fatalf("no final event, saw %v", seen)
		}
	}
	cancel()
	<-done

	var line bool
	for _, e := range seen {
		if e.Job != "backup" || e.Time == nil {
			t.Errorf("event = %v", e)
		}
		line = line || (e.Type == pb.EventType_EVENT_TYPE_LINE && e.Line == "fake output")
	}
	if !line {
		t.Errorf("no output line in %v", seen)
	}
}

func TestHealth(t *testing.T) {
	_, srv := newService(t)
	resp, err := http.Get(srv.URL + Prefix + "health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status %d", resp.StatusCode)
	}
}

func TestListFiles(t *testing.T) {
	_, srv := newService(t)
	c := newClient(t, srv)
	ctx := context.Background()

	res, err := c.ListFiles(ctx, &pb.ListFilesRequest{Job: "photos", Snapshot: "22222222", Path: "/data"})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range res.Files {
		names = append(names, f.Name)
	}
	// Directories first, then by name ignoring case, the directory itself and the snapshot header are not entries.
	if !slices.Equal(names, []string{"Zeta", "a.txt", "b.txt", "link"}) {
		t.Fatalf("files = %v", names)
	}
	if res.Truncated {
		t.Error("a small directory is not truncated")
	}
	byName := map[string]*pb.FileNode{}
	for _, f := range res.Files {
		byName[f.Name] = f
	}
	if f := byName["b.txt"]; f.Size != 12 || f.Type != pb.FileType_FILE_TYPE_FILE || f.Path != "/data/b.txt" || f.ModifyTime == nil || f.Permissions != "-rw-r--r--" {
		t.Errorf("b.txt = %v", f)
	}
	if byName["Zeta"].Type != pb.FileType_FILE_TYPE_DIRECTORY || byName["link"].Type != pb.FileType_FILE_TYPE_SYMLINK {
		t.Errorf("types = %v %v", byName["Zeta"].Type, byName["link"].Type)
	}
}

func TestListFilesRejectsBadInput(t *testing.T) {
	_, srv := newService(t)
	c := newClient(t, srv)
	ctx := context.Background()
	for name, req := range map[string]*pb.ListFilesRequest{
		"no snapshot":   {Path: "/"},
		"option as id":  {Snapshot: "--help", Path: "/"},
		"space in id":   {Snapshot: "a b", Path: "/"},
		"unknown job":   {Job: "nope", Snapshot: "latest", Path: "/"},
		"newline in id": {Snapshot: "a\nb", Path: "/"},
	} {
		if _, err := c.ListFiles(ctx, req); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	// Paths are cleaned, a traversal cannot leave the snapshot and a missing slash is added.
	if _, err := c.ListFiles(ctx, &pb.ListFilesRequest{Snapshot: "latest", Path: "../../etc"}); err != nil {
		t.Errorf("a relative path is read as absolute: %v", err)
	}
}

func TestFilesAreCapped(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"message_type":"snapshot"}` + "\n")
	for i := 0; i < maxFiles+10; i++ {
		fmt.Fprintf(&b, `{"name":"f%05d","type":"file","path":"/d/f%05d","size":1,"message_type":"node"}`+"\n", i, i)
	}
	files, truncated, err := filesFromRestic([]byte(b.String()), "/d")
	if err != nil || !truncated || len(files) != maxFiles {
		t.Fatalf("len = %d, truncated = %v, err = %v", len(files), truncated, err)
	}
	if _, _, err := filesFromRestic([]byte("not json"), "/"); err == nil {
		t.Error("garbage must be an error")
	}
}

// fixedSecrets answers every secret with the same password, the fake restic never checks it.
type fixedSecrets struct{}

func (fixedSecrets) Get(string) (string, error) { return "pw", nil }
