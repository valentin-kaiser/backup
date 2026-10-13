// Package audit records what happens through the portal: sign-ins, changes of daemons and every call that changes
// something on a daemon. Reads are not recorded. The portal has one password and no users, so an entry says what was
// done, to what, when, from which address and whether it worked, not by whom. Secrets are never recorded, only the
// names of what they belong to.
package audit

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/database"
	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/database/schema"
	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/logging/log"
)

// Actions of the portal itself. Calls to a daemon are recorded under the snake case name of the method, e.g.
// start_job.
const (
	LoginOK     = "login"
	LoginFailed = "login_failed"
	Logout      = "logout"
)

// Results of an action.
const (
	// OK means it worked.
	OK = "ok"
	// Failed means the daemon answered with an error.
	Failed = "failed"
	// Denied means the portal refused it.
	Denied = "denied"
	// Unknown means the connection ended before the daemon answered, the call may have worked.
	Unknown = "unknown"
)

const (
	maxDetail  = 400
	maxTarget  = 200
	defaultMax = 100
	hardMax    = 1000
	// maxRows is the most the table keeps, whatever the retention says.
	maxRows = 1_000_000
)

// Entry is one line of the log.
type Entry struct {
	ID     int64     `json:"id"`
	Time   time.Time `json:"time"`
	IP     string    `json:"ip"`
	Action string    `json:"action"`
	Target string    `json:"target,omitempty"`
	Result string    `json:"result"`
	Detail string    `json:"detail,omitempty"`
}

// Log is the audit log in the portal database.
type Log struct {
	q         *schema.Queries
	retention time.Duration
	now       func() time.Time
}

// New creates the log and its table in db. Entries older than retention are pruned, zero keeps them.
func New(db *sql.DB, retention time.Duration) (*Log, error) {
	if err := database.Migrate(db); err != nil {
		return nil, err
	}
	return &Log{q: schema.New(db), retention: retention, now: time.Now}, nil
}

// clean removes control characters, they could forge lines in a text export, and shortens s.
func clean(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

// Add appends an entry. A failure to write is logged and never stops the action that is recorded.
func (l *Log) Add(e Entry) {
	if l == nil {
		return
	}
	if e.Time.IsZero() {
		e.Time = l.now()
	}
	if e.Result == "" {
		e.Result = OK
	}
	err := l.q.CreateAuditEntry(context.Background(), schema.CreateAuditEntryParams{
		Time:   e.Time.UnixMilli(),
		Ip:     clean(e.IP, 64),
		Action: clean(e.Action, 64),
		Target: clean(e.Target, maxTarget),
		Result: clean(e.Result, 16),
		Detail: clean(e.Detail, maxDetail),
	})
	if err != nil {
		log.Error().Err(err).Field("action", e.Action).Msg("the audit entry could not be written")
	}
}

// Query selects entries. Zero values do not filter.
type Query struct {
	Action string
	Result string
	// Text matches a part of the target or the detail.
	Text  string
	Since time.Time
	Until time.Time
	// Before returns entries with a smaller id, to page through the log.
	Before int64
	Limit  int
}

// millis is the filter value of t, zero when t is not set.
func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func (q Query) export() schema.ExportAuditEntriesParams {
	return schema.ExportAuditEntriesParams{
		Action: q.Action,
		Result: q.Result,
		Text:   q.Text,
		Since:  millis(q.Since),
		Until:  millis(q.Until),
		Before: max(q.Before, 0),
	}
}

func entry(r *schema.Audit) Entry {
	return Entry{ID: r.ID, Time: time.UnixMilli(r.Time).UTC(), IP: r.Ip, Action: r.Action, Target: r.Target,
		Result: r.Result, Detail: r.Detail}
}

// Query returns the newest entries first.
func (l *Log) Query(q Query) ([]Entry, error) {
	if q.Limit <= 0 {
		q.Limit = defaultMax
	}
	q.Limit = min(q.Limit, hardMax)
	p := q.export()
	rows, err := l.q.ListAuditEntries(context.Background(), schema.ListAuditEntriesParams{
		Action: p.Action, Result: p.Result, Text: p.Text, Since: p.Since, Until: p.Until, Before: p.Before,
		MaxRows: int64(q.Limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(rows))
	for _, r := range rows {
		out = append(out, entry(r))
	}
	return out, nil
}

// Prune deletes entries older than the retention and, beyond maxRows, the oldest ones.
func (l *Log) Prune() error {
	ctx := context.Background()
	if l.retention > 0 {
		if err := l.q.DeleteAuditEntriesBefore(ctx, l.now().Add(-l.retention).UnixMilli()); err != nil {
			return err
		}
	}
	return l.q.DeleteAuditEntriesBeyond(ctx, maxRows)
}

// Run prunes now and then once a day until ctx ends.
func (l *Log) Run(ctx context.Context) {
	prune := func() {
		if err := l.Prune(); err != nil {
			log.Warn().Err(err).Msg("pruning the audit log failed")
		}
	}
	prune()
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			prune()
		}
	}
}

// csvSafe keeps a spreadsheet from running a cell as a formula.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// Export writes the entries that match q, oldest first, as "csv" or "json" (one object per line).
func (l *Log) Export(w io.Writer, format string, q Query) error {
	var cw *csv.Writer
	var enc *json.Encoder
	switch format {
	case "csv":
		cw = csv.NewWriter(w)
	case "json":
		enc = json.NewEncoder(w)
	default:
		return apperror.NewErrorf("unknown format %q", format)
	}
	rows, err := l.q.ExportAuditEntries(context.Background(), q.export())
	if err != nil {
		return err
	}
	if cw != nil {
		_ = cw.Write([]string{"id", "time", "ip", "action", "target", "result", "detail"})
	}
	for _, r := range rows {
		e := entry(r)
		if cw != nil {
			if err := cw.Write([]string{fmt.Sprint(e.ID), e.Time.Format(time.RFC3339), csvSafe(e.IP), csvSafe(e.Action),
				csvSafe(e.Target), csvSafe(e.Result), csvSafe(e.Detail)}); err != nil {
				return err
			}
		} else if err := enc.Encode(e); err != nil {
			return err
		}
	}
	if cw != nil {
		cw.Flush()
		return cw.Error()
	}
	return nil
}

var credentials = regexp.MustCompile(`(?i)(://)[^/@\s]+@`)

// Redact removes credentials from addresses, e.g. rest:https://user:secret@host/repo.
func Redact(s string) string { return credentials.ReplaceAllString(s, "${1}***@") }
