package api

import (
	"net/http"

	"context"
	"sort"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/peer"
	pb "github.com/Valentin-Kaiser/backup/pkg/proto/api/v1"
	"github.com/Valentin-Kaiser/backup/pkg/tunnel"
	"github.com/valentin-kaiser/go-core/web/xrpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// ResyncJob drops the state of a sync job and runs it again.
func (s *Service) ResyncJob(_ context.Context, in *pb.JobRequest) (*pb.Run, error) {
	r, err := s.m.ResyncJob(in.GetJob())
	if err != nil {
		return nil, fail(err)
	}
	return runToProto(r), nil
}

// ListPeers returns the peers and the name of this worker.
func (s *Service) ListPeers(context.Context, *pb.ListPeersRequest) (*pb.ListPeersResponse, error) {
	c := s.m.Config()
	out := make([]*pb.Peer, 0, len(c.Peers))
	for _, p := range c.Peers {
		out = append(out, peer.PeerToProto(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
	return &pb.ListPeersResponse{Peers: out, WorkerName: c.Worker()}, nil
}

// SetPeer creates a peer or replaces it.
func (s *Service) SetPeer(_ context.Context, in *pb.Peer) (*pb.Peer, error) {
	if err := s.checkSecretRef(in.GetPasswordSecret()); err != nil {
		return nil, err
	}
	if err := s.checkSecretRef(in.GetApiPasswordSecret()); err != nil {
		return nil, err
	}
	p := peer.PeerFromProto(in)
	if err := s.m.SetPeer(p); err != nil {
		return nil, fail(err)
	}
	return peer.PeerToProto(p), nil
}

// DeletePeer removes a peer that no job uses.
func (s *Service) DeletePeer(_ context.Context, in *pb.PeerRequest) (*emptypb.Empty, error) {
	if err := s.m.DeletePeer(in.GetName()); err != nil {
		return nil, fail(err)
	}
	return &emptypb.Empty{}, nil
}

// TestPeer checks the connections to a peer.
func (s *Service) TestPeer(ctx context.Context, in *pb.PeerRequest) (*pb.PeerStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	st, err := s.m.TestPeer(ctx, in.GetName())
	if err != nil {
		return nil, fail(err)
	}
	return st, nil
}

// GetSyncServe returns how this worker exports a directory.
func (s *Service) GetSyncServe(context.Context, *pb.GetSyncServeRequest) (*pb.SyncServe, error) {
	return peer.SyncServeToProto(s.m.Config().SyncServe), nil
}

// SetSyncServe changes how this worker exports a directory.
func (s *Service) SetSyncServe(_ context.Context, in *pb.SyncServe) (*pb.SyncServe, error) {
	if err := s.checkSecretRef(in.GetPasswordSecret()); err != nil {
		return nil, err
	}
	c := peer.SyncServeFromProto(in)
	if err := s.m.SetSyncServe(c); err != nil {
		return nil, fail(err)
	}
	return peer.SyncServeToProto(c), nil
}

// AcquireSyncLock reserves a job for the calling peer.
func (s *Service) AcquireSyncLock(_ context.Context, in *pb.SyncLockRequest) (*pb.SyncLockResponse, error) {
	if in.GetJob() == "" || in.GetOwner() == "" {
		return nil, xrpc.NewError(xrpc.CodeInvalidParams, "job and owner are required")
	}
	ok, holder := s.m.AcquireSyncLock(in.GetJob(), in.GetOwner(), time.Duration(in.GetTtlSeconds())*time.Second)
	return &pb.SyncLockResponse{Granted: ok, Holder: holder}, nil
}

// ReleaseSyncLock gives a reservation back.
func (s *Service) ReleaseSyncLock(_ context.Context, in *pb.SyncLockRequest) (*emptypb.Empty, error) {
	s.m.ReleaseSyncLock(in.GetJob(), in.GetOwner())
	return &emptypb.Empty{}, nil
}

// PushJob creates or replaces the mirror of a job the calling peer owns.
func (s *Service) PushJob(_ context.Context, in *pb.PushJobRequest) (*emptypb.Empty, error) {
	if in.GetDefinition() == nil {
		return nil, xrpc.NewError(xrpc.CodeInvalidParams, "definition is required")
	}
	if err := s.m.ReceiveJob(peer.DefinitionFromProto(in.GetDefinition()), in.GetOwner()); err != nil {
		return nil, fail(err)
	}
	return &emptypb.Empty{}, nil
}

// DropJob removes the mirror of a job the calling peer owned.
func (s *Service) DropJob(_ context.Context, in *pb.PushJobRequest) (*emptypb.Empty, error) {
	if err := s.m.DropMirror(in.GetDefinition().GetName(), in.GetOwner()); err != nil {
		return nil, fail(err)
	}
	return &emptypb.Empty{}, nil
}

// Tunnel returns the handler that carries the protocol of unison for a peer, see package tunnel.
func (s *Service) Tunnel() http.Handler {
	return &tunnel.Server{
		Root:      func() string { return s.m.Config().SyncServe.Path },
		State:     func() string { return s.m.Config().SyncDir },
		Host:      func() string { return s.m.Config().Worker() },
		Authorize: s.m.AuthorizeTunnel,
	}
}
