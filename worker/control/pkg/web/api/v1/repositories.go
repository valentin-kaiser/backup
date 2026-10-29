package api

import (
	"context"
	"errors"

	"github.com/Valentin-Kaiser/backup/pkg/manager"
	"github.com/Valentin-Kaiser/backup/pkg/peer"
	pb "github.com/Valentin-Kaiser/backup/pkg/proto/api/v1"
	"github.com/valentin-kaiser/go-core/logging/log"
	"github.com/valentin-kaiser/go-core/web/xrpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// repositories lists the repositories with the jobs that work on them.
func (s *Service) repositories() []*pb.Repository {
	repos := s.m.Repositories()
	out := make([]*pb.Repository, 0, len(repos))
	for _, r := range repos {
		p := peer.RepositoryToProto(r)
		p.Jobs = s.m.JobsOfRepository(r.Name)
		out = append(out, p)
	}
	return out
}

// ListRepositories lists the repositories.
func (s *Service) ListRepositories(context.Context, *pb.ListRepositoriesRequest) (*pb.ListRepositoriesResponse, error) {
	return &pb.ListRepositoriesResponse{Repositories: s.repositories()}, nil
}

// SetRepository creates a repository or changes its settings.
func (s *Service) SetRepository(_ context.Context, in *pb.Repository) (*pb.Repository, error) {
	r := peer.RepositoryFromProto(in)
	if err := s.checkSecretRef(r.PasswordSecret); err != nil {
		return nil, err
	}
	if err := s.m.SetRepository(r); err != nil {
		if errors.Is(err, manager.ErrNoRepositories) {
			return nil, xrpc.NewError(xrpc.CodeInvalidRequest, err.Error())
		}
		return nil, fail(err)
	}
	log.Info().Field("repository", r.Name).Msg("repository saved")
	out := peer.RepositoryToProto(r)
	out.Jobs = s.m.JobsOfRepository(r.Name)
	return out, nil
}

// DeleteRepository removes a repository that no job uses.
func (s *Service) DeleteRepository(_ context.Context, in *pb.RepositoryRequest) (*emptypb.Empty, error) {
	if err := s.m.DeleteRepository(in.GetName()); err != nil {
		if errors.Is(err, manager.ErrNoRepositories) {
			return nil, xrpc.NewError(xrpc.CodeInvalidRequest, err.Error())
		}
		return nil, fail(err)
	}
	log.Info().Field("repository", in.GetName()).Msg("repository deleted")
	return &emptypb.Empty{}, nil
}
