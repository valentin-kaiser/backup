package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/config"
	"github.com/Valentin-Kaiser/backup/pkg/manager"
	pb "github.com/Valentin-Kaiser/backup/pkg/proto/api/v1"
	"github.com/Valentin-Kaiser/backup/pkg/store"
	"github.com/valentin-kaiser/go-core/apperror"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func timeToProto(t *time.Time) *timestamppb.Timestamp {
	if t == nil || t.IsZero() {
		return nil
	}
	return timestamppb.New(*t)
}

func statusToProto(s store.Status) pb.RunStatus {
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

func runToProto(r store.Run) *pb.Run {
	return &pb.Run{
		Id:         r.ID,
		Job:        r.Job,
		Trigger:    r.Trigger,
		Status:     statusToProto(r.Status),
		QueueTime:  timeToProto(&r.Queued),
		StartTime:  timeToProto(r.Started),
		FinishTime: timeToProto(r.Finished),
		ExitCode:   int32(r.ExitCode), //nolint:gosec // exit codes are small
		Error:      r.Error,
		Output:     r.Output,
	}
}

func optionalRun(r *store.Run) *pb.Run {
	if r == nil {
		return nil
	}
	return runToProto(*r)
}

func jobToProto(j manager.JobStatus) *pb.Job {
	return &pb.Job{
		Name:            j.Name,
		Type:            j.Type,
		Definition:      definitionToProto(j.Definition),
		Enabled:         j.Enabled,
		Paused:          j.Paused,
		Schedule:        j.Schedule,
		NextRunTime:     timeToProto(j.NextRun),
		Active:          optionalRun(j.Active),
		Last:            optionalRun(j.Last),
		LastSuccessTime: timeToProto(j.LastSuccess),
	}
}

func definitionToProto(j config.Job) *pb.JobDefinition {
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
	}
}

func definitionFromProto(d *pb.JobDefinition) config.Job {
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
	}
}

func eventToProto(e manager.Event) *pb.Event {
	t := pb.EventType_EVENT_TYPE_STATUS
	if e.Type == "line" {
		t = pb.EventType_EVENT_TYPE_LINE
	}
	return &pb.Event{
		Type:   t,
		Run:    e.Run,
		Job:    e.Job,
		Status: statusToProto(e.Status),
		Line:   e.Line,
		Time:   timestamppb.New(e.Time),
	}
}

// snapshotsFromRestic converts the output of `restic snapshots --json`, newest first.
func snapshotsFromRestic(raw json.RawMessage) ([]*pb.Snapshot, error) {
	var in []struct {
		ID       string    `json:"id"`
		ShortID  string    `json:"short_id"`
		Time     time.Time `json:"time"`
		Hostname string    `json:"hostname"`
		Username string    `json:"username"`
		Paths    []string  `json:"paths"`
		Tags     []string  `json:"tags"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, apperror.NewError("unexpected snapshot list").AddError(err)
	}
	out := make([]*pb.Snapshot, 0, len(in))
	for i := len(in) - 1; i >= 0; i-- {
		s := in[i]
		if s.ShortID == "" {
			s.ShortID = s.ID
			if len(s.ShortID) > 8 {
				s.ShortID = s.ShortID[:8]
			}
		}
		out = append(out, &pb.Snapshot{
			Id:       s.ID,
			ShortId:  s.ShortID,
			Time:     timestamppb.New(s.Time),
			Hostname: s.Hostname,
			Username: s.Username,
			Paths:    s.Paths,
			Tags:     s.Tags,
		})
	}
	return out, nil
}

// maxFiles caps one directory listing, a directory can hold millions of entries.
const maxFiles = 5000

// filesFromRestic converts the output of `restic ls --json` for dir: directories first, then by name. The
// snapshot header and the directory itself, which restic prints as well, are left out.
func filesFromRestic(raw []byte, dir string) ([]*pb.FileNode, bool, error) {
	dir = path.Clean(dir)
	var files []*pb.FileNode
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var n struct {
			Name        string    `json:"name"`
			Type        string    `json:"type"`
			Path        string    `json:"path"`
			Size        uint64    `json:"size"`
			Mtime       time.Time `json:"mtime"`
			Permissions string    `json:"permissions"`
			MessageType string    `json:"message_type"`
		}
		if err := json.Unmarshal(line, &n); err != nil {
			return nil, false, apperror.NewError("unexpected file listing").AddError(err)
		}
		if n.MessageType == "snapshot" || n.Name == "" || path.Clean(n.Path) == dir {
			continue
		}
		t := pb.FileType_FILE_TYPE_OTHER
		switch n.Type {
		case "file":
			t = pb.FileType_FILE_TYPE_FILE
		case "dir":
			t = pb.FileType_FILE_TYPE_DIRECTORY
		case "symlink":
			t = pb.FileType_FILE_TYPE_SYMLINK
		}
		files = append(files, &pb.FileNode{
			Name:        n.Name,
			Path:        n.Path,
			Type:        t,
			Size:        n.Size,
			ModifyTime:  timeToProto(&n.Mtime),
			Permissions: n.Permissions,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, false, apperror.NewError("unexpected file listing").AddError(err)
	}
	sort.SliceStable(files, func(i, j int) bool {
		di, dj := files[i].Type == pb.FileType_FILE_TYPE_DIRECTORY, files[j].Type == pb.FileType_FILE_TYPE_DIRECTORY
		if di != dj {
			return di
		}
		return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
	})
	if len(files) > maxFiles {
		return files[:maxFiles], true, nil
	}
	return files, false, nil
}

// statsFromRestic converts the output of `restic stats --json --mode raw-data`.
func statsFromRestic(raw json.RawMessage) (*pb.RepositoryStats, error) {
	var in struct {
		TotalSize        uint64  `json:"total_size"`
		UncompressedSize uint64  `json:"total_uncompressed_size"`
		CompressionRatio float64 `json:"compression_ratio"`
		BlobCount        uint64  `json:"total_blob_count"`
		SnapshotCount    uint64  `json:"snapshots_count"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, apperror.NewError("unexpected repository statistics").AddError(err)
	}
	return &pb.RepositoryStats{
		TotalSize:        in.TotalSize,
		UncompressedSize: in.UncompressedSize,
		CompressionRatio: in.CompressionRatio,
		BlobCount:        in.BlobCount,
		SnapshotCount:    in.SnapshotCount,
	}, nil
}
