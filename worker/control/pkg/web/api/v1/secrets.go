package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	pb "github.com/Valentin-Kaiser/backup/pkg/proto/api/v1"
	"github.com/Valentin-Kaiser/backup/pkg/secrets"
	"github.com/valentin-kaiser/go-core/logging/log"
	"github.com/valentin-kaiser/go-core/web/xrpc"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// changeTimeout bounds a password change, restic has to rewrite a key file in every repository.
const changeTimeout = 10 * time.Minute

// WithSecrets enables the management of stored secrets.
func (s *Service) WithSecrets(store *secrets.Store) *Service {
	s.secrets = store
	return s
}

func (s *Service) secretsEnabled() error {
	if s.secrets == nil {
		return xrpc.NewError(xrpc.CodeInvalidRequest, "secrets are not enabled on this daemon")
	}
	return nil
}

// generatePassword returns a random password of 256 bits as 43 URL-safe characters.
func generatePassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// secretInfo describes a secret together with the jobs that use it.
func (s *Service) secretInfo(m secrets.Meta) *pb.SecretInfo {
	jobs, global := s.m.SecretUsers(m.Name)
	return &pb.SecretInfo{
		Name: m.Name, CreateTime: timestamppb.New(m.Created), UpdateTime: timestamppb.New(m.Updated),
		Jobs: jobs, DefaultPassword: global,
	}
}

func (s *Service) metaOf(name string) (secrets.Meta, bool) {
	for _, m := range s.secrets.List() {
		if m.Name == name {
			return m, true
		}
	}
	return secrets.Meta{}, false
}

// checkSecretRef makes sure a job only refers to a secret that exists, so a typo is reported when saving and not
// when the backup runs.
func (s *Service) checkSecretRef(name string) error {
	if name == "" {
		return nil
	}
	if s.secrets == nil {
		return xrpc.NewError(xrpc.CodeInvalidParams, "this daemon has no secret store")
	}
	if !s.secrets.Has(name) {
		return xrpc.NewError(xrpc.CodeInvalidParams, "the secret "+name+" does not exist")
	}
	return nil
}

// ListSecrets returns the stored secrets without their values.
func (s *Service) ListSecrets(context.Context, *pb.ListSecretsRequest) (*pb.ListSecretsResponse, error) {
	if err := s.secretsEnabled(); err != nil {
		return nil, err
	}
	list := s.secrets.List()
	out := make([]*pb.SecretInfo, 0, len(list))
	for _, m := range list {
		out = append(out, s.secretInfo(m))
	}
	return &pb.ListSecretsResponse{Secrets: out}, nil
}

// valueOf returns the value of a request: generated when asked, the given one otherwise.
func valueOf(generate bool, value string) (v string, generated bool, err error) {
	if generate {
		v, err = generatePassword()
		return v, true, err
	}
	return value, false, nil
}

// SetSecret creates a secret or replaces its value.
func (s *Service) SetSecret(_ context.Context, in *pb.SetSecretRequest) (*pb.SetSecretResponse, error) {
	if err := s.secretsEnabled(); err != nil {
		return nil, err
	}
	value, generated, err := valueOf(in.GetGenerate(), in.GetValue())
	if err != nil {
		return nil, fail(err)
	}
	created, err := s.secrets.Set(in.GetName(), value)
	if err != nil {
		return nil, xrpc.NewError(xrpc.CodeInvalidParams, err.Error())
	}
	// Only the name is logged, never the value.
	log.Info().Field("secret", in.GetName()).Field("created", created).Msg("secret stored")
	if created {
		// Repositories whose password was missing at start-up can be initialised now.
		if err := s.m.Reload(); err != nil {
			log.Warn().Err(err).Msg("reload after creating a secret failed")
		}
	}
	m, _ := s.metaOf(in.GetName())
	resp := &pb.SetSecretResponse{Secret: s.secretInfo(m)}
	if generated {
		resp.GeneratedValue = value
	}
	return resp, nil
}

// ChangeRepositoryPassword changes the password of the repositories that use a secret and stores the new one.
func (s *Service) ChangeRepositoryPassword(ctx context.Context, in *pb.ChangeRepositoryPasswordRequest) (*pb.ChangeRepositoryPasswordResponse, error) {
	if err := s.secretsEnabled(); err != nil {
		return nil, err
	}
	name := in.GetName()
	old, err := s.secrets.Get(name)
	if err != nil {
		if errors.Is(err, secrets.ErrNotFound) {
			return nil, xrpc.NewError(xrpc.CodeInvalidParams, err.Error())
		}
		return nil, fail(err)
	}
	value, generated, err := valueOf(in.GetGenerate(), in.GetValue())
	if err != nil {
		return nil, fail(err)
	}
	if err := secrets.ValidateValue(value); err != nil {
		return nil, xrpc.NewError(xrpc.CodeInvalidParams, err.Error())
	}
	if value == old {
		return nil, xrpc.NewError(xrpc.CodeInvalidParams, "the new password is the current one")
	}

	ctx, cancel := context.WithTimeout(ctx, changeTimeout)
	defer cancel()
	res, err := s.m.ChangePassword(ctx, name, old, value, func() error {
		_, err := s.secrets.Set(name, value)
		return err
	})
	if err != nil {
		log.Warn().Err(err).Field("secret", name).Msg("changing the repository password failed")
		return nil, xrpc.NewError(xrpc.CodeServerError, err.Error())
	}
	log.Info().Field("secret", name).Field("repositories", res.Repositories).Msg("repository password changed")
	m, _ := s.metaOf(name)
	resp := &pb.ChangeRepositoryPasswordResponse{Secret: s.secretInfo(m), Repositories: int32(res.Repositories), Warnings: res.Warnings} //nolint:gosec // small
	if generated {
		resp.GeneratedValue = value
	}
	return resp, nil
}

// DeleteSecret removes a secret that is not in use.
func (s *Service) DeleteSecret(_ context.Context, in *pb.SecretRequest) (*emptypb.Empty, error) {
	if err := s.secretsEnabled(); err != nil {
		return nil, err
	}
	if jobs, global := s.m.SecretUsers(in.GetName()); len(jobs) > 0 || global {
		users := strings.Join(jobs, ", ")
		if global {
			users = strings.TrimPrefix(users+", the default password", ", ")
		}
		return nil, xrpc.NewError(xrpc.CodeInvalidRequest, "the secret is still used by "+users+", move them to another password first")
	}
	if err := s.secrets.Delete(in.GetName()); err != nil {
		if errors.Is(err, secrets.ErrNotFound) {
			return nil, xrpc.NewError(xrpc.CodeInvalidParams, err.Error())
		}
		return nil, fail(err)
	}
	log.Info().Field("secret", in.GetName()).Msg("secret deleted")
	return &emptypb.Empty{}, nil
}
