package peer

import (
	"strings"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/config"
	pb "github.com/Valentin-Kaiser/backup/pkg/proto/api/v1"
	"github.com/Valentin-Kaiser/backup/pkg/store"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func timeToProto(t *time.Time) *timestamppb.Timestamp {
	if t == nil || t.IsZero() {
		return nil
	}
	return timestamppb.New(*t)
}

func timeFromProto(t *timestamppb.Timestamp) *time.Time {
	if t == nil {
		return nil
	}
	v := t.AsTime()
	if v.IsZero() {
		return nil
	}
	return &v
}

// StatusToProto converts the status of a run.
func StatusToProto(s store.Status) pb.RunStatus {
	switch s {
	case store.StatusQueued:
		return pb.RunStatus_RUN_STATUS_QUEUED
	case store.StatusRunning:
		return pb.RunStatus_RUN_STATUS_RUNNING
	case store.StatusSuccess:
		return pb.RunStatus_RUN_STATUS_SUCCESS
	case store.StatusFailed:
		return pb.RunStatus_RUN_STATUS_FAILED
	case store.StatusCancelled:
		return pb.RunStatus_RUN_STATUS_CANCELLED
	case store.StatusSkipped:
		return pb.RunStatus_RUN_STATUS_SKIPPED
	}
	return pb.RunStatus_RUN_STATUS_UNSPECIFIED
}

// StatusFromProto converts the status of a run.
func StatusFromProto(s pb.RunStatus) store.Status {
	switch s {
	case pb.RunStatus_RUN_STATUS_QUEUED:
		return store.StatusQueued
	case pb.RunStatus_RUN_STATUS_RUNNING:
		return store.StatusRunning
	case pb.RunStatus_RUN_STATUS_SUCCESS:
		return store.StatusSuccess
	case pb.RunStatus_RUN_STATUS_FAILED:
		return store.StatusFailed
	case pb.RunStatus_RUN_STATUS_CANCELLED:
		return store.StatusCancelled
	case pb.RunStatus_RUN_STATUS_SKIPPED:
		return store.StatusSkipped
	}
	return store.Status(strings.ToLower(strings.TrimPrefix(s.String(), "RUN_STATUS_")))
}

// RunToProto converts a run.
func RunToProto(r store.Run) *pb.Run {
	return &pb.Run{
		Id:         r.ID,
		Job:        r.Job,
		Trigger:    r.Trigger,
		Status:     StatusToProto(r.Status),
		QueueTime:  timeToProto(&r.Queued),
		StartTime:  timeToProto(r.Started),
		FinishTime: timeToProto(r.Finished),
		ExitCode:   int32(r.ExitCode), //nolint:gosec // exit codes are small
		Error:      r.Error,
		Output:     r.Output,
	}
}

// RunFromProto converts a run.
func RunFromProto(r *pb.Run) store.Run {
	out := store.Run{
		ID:       r.GetId(),
		Job:      r.GetJob(),
		Trigger:  r.GetTrigger(),
		Status:   StatusFromProto(r.GetStatus()),
		Started:  timeFromProto(r.GetStartTime()),
		Finished: timeFromProto(r.GetFinishTime()),
		ExitCode: int(r.GetExitCode()),
		Error:    r.GetError(),
		Output:   r.GetOutput(),
	}
	if q := timeFromProto(r.GetQueueTime()); q != nil {
		out.Queued = *q
	}
	return out
}

// DefinitionToProto converts the configuration of a job.
func DefinitionToProto(j config.Job) *pb.JobDefinition {
	if j.Name == config.RestoreJob {
		return nil
	}
	return &pb.JobDefinition{
		Name:           j.Name,
		Type:           j.Type,
		Enabled:        j.Enabled,
		Schedule:       j.Schedule,
		Args:           j.Args,
		TimeoutMinutes: int32(j.TimeoutMinutes), //nolint:gosec // validated to be small
		MaxAgeHours:    int32(j.MaxAgeHours),    //nolint:gosec // validated to be small
		Source:         j.Source,
		Repository:     j.Repository,
		PasswordFile:   j.PasswordFile,
		PasswordSecret: j.PasswordSecret,
		Path1:          j.Path1,
		Path2:          j.Path2,
		Conflict:       j.Conflict,
		ConflictLoser:  j.ConflictLoser,
		MaxDeletePct:   int32(j.MaxDeletePct), //nolint:gosec // validated to be 0-100
		CheckAccess:    j.CheckAccess,
		Force:          j.Force,
		Engine:         j.Engine,
		Owner:          j.Owner,
	}
}

// DefinitionFromProto converts the configuration of a job.
func DefinitionFromProto(d *pb.JobDefinition) config.Job {
	return config.Job{
		Name:           d.GetName(),
		Type:           d.GetType(),
		Enabled:        d.GetEnabled(),
		Schedule:       d.GetSchedule(),
		Args:           d.GetArgs(),
		TimeoutMinutes: int(d.GetTimeoutMinutes()),
		MaxAgeHours:    int(d.GetMaxAgeHours()),
		Source:         d.GetSource(),
		Repository:     d.GetRepository(),
		PasswordFile:   d.GetPasswordFile(),
		PasswordSecret: d.GetPasswordSecret(),
		Path1:          d.GetPath1(),
		Path2:          d.GetPath2(),
		Conflict:       d.GetConflict(),
		ConflictLoser:  d.GetConflictLoser(),
		MaxDeletePct:   int(d.GetMaxDeletePct()),
		CheckAccess:    d.GetCheckAccess(),
		Force:          d.GetForce(),
		Engine:         d.GetEngine(),
		Owner:          d.GetOwner(),
	}
}

// PeerToProto converts a peer.
func PeerToProto(p config.Peer) *pb.Peer {
	return &pb.Peer{
		Name: p.Name, Url: p.URL, Username: p.Username, PasswordFile: p.PasswordFile, PasswordSecret: p.PasswordSecret,
		CaFile: p.CAFile, ApiUrl: p.APIURL, ApiPasswordFile: p.APIPasswordFile, ApiPasswordSecret: p.APIPasswordSecret,
		ApiCaFile: p.APICAFile,
	}
}

// PeerFromProto converts a peer.
func PeerFromProto(p *pb.Peer) config.Peer {
	return config.Peer{
		Name: strings.TrimSpace(p.GetName()), URL: strings.TrimSpace(p.GetUrl()), Username: p.GetUsername(),
		PasswordFile: strings.TrimSpace(p.GetPasswordFile()), PasswordSecret: strings.TrimSpace(p.GetPasswordSecret()),
		CAFile: strings.TrimSpace(p.GetCaFile()), APIURL: strings.TrimSpace(p.GetApiUrl()),
		APIPasswordFile:   strings.TrimSpace(p.GetApiPasswordFile()),
		APIPasswordSecret: strings.TrimSpace(p.GetApiPasswordSecret()), APICAFile: strings.TrimSpace(p.GetApiCaFile()),
	}
}

// SyncServeToProto converts the export settings.
func SyncServeToProto(s config.SyncServe) *pb.SyncServe {
	return &pb.SyncServe{
		Enabled: s.Enabled, Path: s.Path, Address: s.Address, Port: int32(s.Port), //nolint:gosec // validated
		Username: s.Username, PasswordFile: s.PasswordFile, PasswordSecret: s.PasswordSecret,
		CertificateFile: s.CertificateFile, KeyFile: s.KeyFile, Tunnel: s.Tunnel,
	}
}

// SyncServeFromProto converts the export settings.
func SyncServeFromProto(s *pb.SyncServe) config.SyncServe {
	return config.SyncServe{
		Enabled: s.GetEnabled(), Path: strings.TrimSpace(s.GetPath()), Address: strings.TrimSpace(s.GetAddress()),
		Port: int(s.GetPort()), Username: s.GetUsername(), PasswordFile: strings.TrimSpace(s.GetPasswordFile()),
		PasswordSecret: strings.TrimSpace(s.GetPasswordSecret()), CertificateFile: strings.TrimSpace(s.GetCertificateFile()),
		KeyFile: strings.TrimSpace(s.GetKeyFile()), Tunnel: s.GetTunnel(),
	}
}
