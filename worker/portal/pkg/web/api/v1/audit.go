package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"time"

	pb "github.com/Valentin-Kaiser/backup/pkg/proto/api/v1"
	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/audit"
	"github.com/valentin-kaiser/go-core/logging/log"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// exportFormats maps the formats of an export to their media type.
var exportFormats = map[string]string{"csv": "text/csv; charset=utf-8", "json": "application/x-ndjson"}

// AuditLog implements the reading of the audit log.
type AuditLog struct {
	pb.UnimplementedAuditLogServer
	auth *Auth
	log  *audit.Log
}

var _ pb.AuditLogServer = (*AuditLog)(nil)

// NewAuditLog creates the service.
func NewAuditLog(auth *Auth, l *audit.Log) *AuditLog { return &AuditLog{auth: auth, log: l} }

func query(f *pb.AuditFilter) audit.Query {
	q := audit.Query{Action: f.GetAction(), Result: f.GetResult(), Text: f.GetText()}
	if f.GetSince() != nil {
		q.Since = f.GetSince().AsTime()
	}
	if f.GetUntil() != nil {
		q.Until = f.GetUntil().AsTime()
	}
	return q
}

func toEntry(e audit.Entry) *pb.AuditEntry {
	return &pb.AuditEntry{
		Id: e.ID, Time: timestamppb.New(e.Time), Ip: e.IP, Action: e.Action, Target: e.Target,
		Result: e.Result, Detail: e.Detail,
	}
}

func (s *AuditLog) ready(ctx context.Context) error {
	if err := s.auth.require(ctx); err != nil {
		return err
	}
	if s.log == nil {
		return fail(ctx, http.StatusServiceUnavailable, "the audit log is not enabled")
	}
	return nil
}

// ListAuditLog lists entries, newest first.
func (s *AuditLog) ListAuditLog(ctx context.Context, in *pb.ListAuditLogRequest) (*pb.ListAuditLogResponse, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	if in.GetBeforeId() < 0 || in.GetLimit() < 0 {
		return nil, fail(ctx, http.StatusBadRequest, "before_id and limit must not be negative")
	}
	q := query(in.GetFilter())
	q.Before, q.Limit = in.GetBeforeId(), int(in.GetLimit())
	entries, err := s.log.Query(q)
	if err != nil {
		log.Error().Err(err).Msg("reading the audit log failed")
		return nil, fail(ctx, http.StatusInternalServerError, "reading the audit log failed")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	out := &pb.ListAuditLogResponse{Entries: make([]*pb.AuditEntry, 0, len(entries))}
	for _, e := range entries {
		out.Entries = append(out.Entries, toEntry(e))
	}
	if len(entries) > 0 && len(entries) >= min(limit, 1000) {
		out.NextId = entries[len(entries)-1].ID
	}
	return out, nil
}

// ExportAuditLog returns the matching entries as a file.
func (s *AuditLog) ExportAuditLog(ctx context.Context, in *pb.ExportAuditLogRequest) (*pb.ExportAuditLogResponse, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	contentType, ok := exportFormats[in.GetFormat()]
	if !ok {
		return nil, fail(ctx, http.StatusBadRequest, "format is csv or json")
	}
	// Taking the log out of the portal is worth a line in it.
	s.log.Add(audit.Entry{IP: requestIP(ctx), Action: "audit_export", Result: audit.OK, Detail: in.GetFormat()})
	var buf bytes.Buffer
	if err := s.log.Export(&buf, in.GetFormat(), query(in.GetFilter())); err != nil {
		if errors.Is(err, audit.ErrTooLarge) {
			return nil, fail(ctx, http.StatusBadRequest, err.Error())
		}
		log.Error().Err(err).Msg("exporting the audit log failed")
		return nil, fail(ctx, http.StatusInternalServerError, "exporting the audit log failed")
	}
	return &pb.ExportAuditLogResponse{
		Filename:    "audit-" + time.Now().UTC().Format("2006-01-02") + "." + in.GetFormat(),
		ContentType: contentType,
		Data:        buf.Bytes(),
	}, nil
}
