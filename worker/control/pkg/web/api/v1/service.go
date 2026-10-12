// Package api exposes the control API as an xrpc service. Calls and the live event stream share one websocket,
// which is what the CLI and the separate web portal talk to.
//
// The API has no authentication. Anyone who can reach it can run jobs, change the configuration and restore
// files, so it has to stay on loopback or a private network. Browsers are kept out by xrpc itself, it only
// accepts websockets from the origin of the page that opened them.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"

	"github.com/Valentin-Kaiser/backup/pkg/manager"
	pb "github.com/Valentin-Kaiser/backup/pkg/proto/api/v1"
	"github.com/Valentin-Kaiser/backup/pkg/secrets"
	"github.com/valentin-kaiser/go-core/logging/log"
	"github.com/valentin-kaiser/go-core/version"
	"github.com/valentin-kaiser/go-core/web/xrpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

const maxBody = 64 << 10

const (
	defaultRuns = 50
	maxRuns     = 500
)

// Prefix is the URL prefix of the API.
const Prefix = "/api/v1/"

// Endpoint is the path of the xrpc service, it serves JSON-RPC over HTTP POST and websockets.
const Endpoint = Prefix + "rpc"

// Service implements the control API.
type Service struct {
	pb.UnimplementedControlServer
	m       *manager.Manager
	secrets *secrets.Store
}

var _ pb.ControlServer = (*Service)(nil)

// New creates the service.
func New(m *manager.Manager) *Service {
	return &Service{m: m}
}

// Handler returns the xrpc service.
func (s *Service) Handler() http.Handler {
	rpc := xrpc.Register(s).WithMaxBodySize(maxBody)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		rpc.ServeHTTP(w, r)
	})
}

// Health answers the health probe, it only reveals healthy or unhealthy.
func (s *Service) Health(w http.ResponseWriter, _ *http.Request) {
	ok, _ := s.m.Health()
	code := http.StatusOK
	if !ok {
		code = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(map[string]bool{"healthy": ok}); err != nil {
		log.Debug().Err(err).Msg("failed to write response")
	}
}

// fail turns manager errors into rpc errors.
func fail(err error) error {
	code := xrpc.CodeServerError
	if errors.Is(err, manager.ErrBusy) {
		code = xrpc.CodeInvalidRequest
	}
	return xrpc.NewError(code, err.Error())
}

// GetStatus returns the health and all jobs.
func (s *Service) GetStatus(context.Context, *pb.GetStatusRequest) (*pb.ControlStatus, error) {
	ok, problems := s.m.Health()
	status := s.m.Status()
	jobs := make([]*pb.Job, 0, len(status))
	for _, j := range status {
		jobs = append(jobs, jobToProto(j))
	}
	c := s.m.Config()
	defaults := &pb.Defaults{Source: c.Source, Repository: c.Repository, PasswordFile: c.PasswordFile, RestoreRoot: c.RestoreRoot, PasswordSecret: c.PasswordSecret}
	return &pb.ControlStatus{Version: version.String(), Healthy: ok, Problems: problems, Jobs: jobs, Defaults: defaults}, nil
}

// ListRuns lists the run history.
func (s *Service) ListRuns(_ context.Context, in *pb.ListRunsRequest) (*pb.ListRunsResponse, error) {
	n := int(in.GetLimit())
	if n <= 0 || n > maxRuns {
		n = defaultRuns
	}
	runs := s.m.Runs(in.GetJob(), n)
	out := make([]*pb.Run, 0, len(runs))
	for _, r := range runs {
		out = append(out, runToProto(r))
	}
	return &pb.ListRunsResponse{Runs: out}, nil
}

// GetRun returns a single run including its output.
func (s *Service) GetRun(_ context.Context, in *pb.GetRunRequest) (*pb.Run, error) {
	r, ok := s.m.Run(in.GetId())
	if !ok {
		return nil, xrpc.NewError(xrpc.CodeInvalidParams, "run not found")
	}
	return runToProto(r), nil
}

// CreateJob adds a job.
func (s *Service) CreateJob(_ context.Context, in *pb.JobDefinition) (*pb.Job, error) {
	if err := s.checkSecretRef(in.GetPasswordSecret()); err != nil {
		return nil, err
	}
	st, err := s.m.CreateJob(definitionFromProto(in))
	if err != nil {
		return nil, fail(err)
	}
	return jobToProto(st), nil
}

// UpdateJob changes a job.
func (s *Service) UpdateJob(_ context.Context, in *pb.JobDefinition) (*pb.Job, error) {
	if err := s.checkSecretRef(in.GetPasswordSecret()); err != nil {
		return nil, err
	}
	st, err := s.m.UpdateJob(definitionFromProto(in))
	if err != nil {
		return nil, fail(err)
	}
	return jobToProto(st), nil
}

// DeleteJob removes a job.
func (s *Service) DeleteJob(_ context.Context, in *pb.JobRequest) (*emptypb.Empty, error) {
	if err := s.m.DeleteJob(in.GetJob()); err != nil {
		return nil, fail(err)
	}
	return &emptypb.Empty{}, nil
}

// StartJob starts a job now.
func (s *Service) StartJob(_ context.Context, in *pb.JobRequest) (*pb.Run, error) {
	r, err := s.m.Trigger(in.GetJob(), "manual", false)
	if err != nil {
		return nil, fail(err)
	}
	return runToProto(r), nil
}

// CancelJob cancels the active run of a job.
func (s *Service) CancelJob(_ context.Context, in *pb.JobRequest) (*emptypb.Empty, error) {
	if err := s.m.Cancel(in.GetJob()); err != nil {
		return nil, fail(err)
	}
	return &emptypb.Empty{}, nil
}

// PauseJob stops a job from being scheduled.
func (s *Service) PauseJob(_ context.Context, in *pb.JobRequest) (*emptypb.Empty, error) {
	if err := s.m.Pause(in.GetJob()); err != nil {
		return nil, fail(err)
	}
	return &emptypb.Empty{}, nil
}

// ResumeJob schedules a paused job again.
func (s *Service) ResumeJob(_ context.Context, in *pb.JobRequest) (*emptypb.Empty, error) {
	if err := s.m.Resume(in.GetJob()); err != nil {
		return nil, fail(err)
	}
	return &emptypb.Empty{}, nil
}

// ListSnapshots lists the restic snapshots, newest first.
func (s *Service) ListSnapshots(ctx context.Context, in *pb.ListSnapshotsRequest) (*pb.ListSnapshotsResponse, error) {
	if err := s.knownJob(in.GetJob()); err != nil {
		return nil, err
	}
	raw, err := s.m.Snapshots(ctx, in.GetJob())
	if err != nil {
		return nil, repositoryError(err)
	}
	snapshots, err := snapshotsFromRestic(raw)
	if err != nil {
		return nil, repositoryError(err)
	}
	return &pb.ListSnapshotsResponse{Snapshots: snapshots}, nil
}

// ListFiles lists the entries directly inside a directory of a snapshot.
func (s *Service) ListFiles(ctx context.Context, in *pb.ListFilesRequest) (*pb.ListFilesResponse, error) {
	if err := s.knownJob(in.GetJob()); err != nil {
		return nil, err
	}
	dir := path.Clean("/" + in.GetPath())
	raw, err := s.m.Listing(ctx, in.GetJob(), in.GetSnapshot(), dir)
	if err != nil {
		return nil, repositoryError(err)
	}
	files, truncated, err := filesFromRestic(raw, dir)
	if err != nil {
		return nil, repositoryError(err)
	}
	return &pb.ListFilesResponse{Files: files, Truncated: truncated}, nil
}

// GetStats returns the repository statistics.
func (s *Service) GetStats(ctx context.Context, in *pb.GetStatsRequest) (*pb.RepositoryStats, error) {
	if err := s.knownJob(in.GetJob()); err != nil {
		return nil, err
	}
	raw, err := s.m.Stats(ctx, in.GetJob())
	if err != nil {
		return nil, repositoryError(err)
	}
	stats, err := statsFromRestic(raw)
	if err != nil {
		return nil, repositoryError(err)
	}
	return stats, nil
}

// knownJob rejects job names that are not configured, an empty name selects the global repository.
func (s *Service) knownJob(job string) error {
	if job != "" && !s.m.HasJob(job) {
		return xrpc.NewError(xrpc.CodeInvalidParams, fmt.Sprintf("unknown job %q", job))
	}
	return nil
}

// repositoryError reports why restic could not be queried, e.g. a wrong password or a missing repository.
func repositoryError(err error) error {
	log.Warn().Err(err).Msg("repository query failed")
	return xrpc.NewError(xrpc.CodeServerError, "repository query failed: "+err.Error())
}

// Restore starts a restore run.
func (s *Service) Restore(_ context.Context, in *pb.RestoreRequest) (*pb.Run, error) {
	r, err := s.m.Restore(in.GetJob(), in.GetSnapshot(), in.GetTarget(), in.GetInclude())
	if err != nil {
		return nil, fail(err)
	}
	return runToProto(r), nil
}

// Watch streams run events until the client goes away.
func (s *Service) Watch(ctx context.Context, _ *pb.WatchRequest, out chan<- *pb.Event) error {
	events, cancel := s.m.Subscribe()
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-events:
			if !ok {
				return nil
			}
			select {
			case out <- eventToProto(e):
			case <-ctx.Done():
				return nil
			}
		}
	}
}
