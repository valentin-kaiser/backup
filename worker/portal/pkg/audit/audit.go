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
	db        *sql.DB
	retention time.Duration
	now       func() time.Time
}

// New creates the log and its table in db. Entries older than retention are pruned, zero keeps them.
func New(db *sql.DB, retention time.Duration) (*Log, error) {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS audit (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	time INTEGER NOT NULL,
	ip TEXT NOT NULL DEFAULT '',
	action TEXT NOT NULL,
	target TEXT NOT NULL DEFAULT '',
	result TEXT NOT NULL,
	detail TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS audit_time ON audit(time);
CREATE INDEX IF NOT EXISTS audit_action ON audit(action);`)
	if err != nil {
		return nil, err
	}
	return &Log{db: db, retention: retention, now: time.Now}, nil
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
	_, err := l.db.Exec(`INSERT INTO audit(time, ip, action, target, result, detail) VALUES(?,?,?,?,?,?)`,
		e.Time.UnixMilli(), clean(e.IP, 64), clean(e.Action, 64), clean(e.Target, maxTarget),
		clean(e.Result, 16), clean(e.Detail, maxDetail))
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

func (q Query) where() (string, []any) {
	var conds []string
	var args []any
	add := func(cond string, v any) {
		conds = append(conds, cond)
		args = append(args, v)
	}
	if q.Action != "" {
		add("action = ?", q.Action)
	}
	if q.Result != "" {
		add("result = ?", q.Result)
	}
	if q.Text != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q.Text) + "%"
		conds = append(conds, `(target LIKE ? ESCAPE '\' OR detail LIKE ? ESCAPE '\')`)
		args = append(args, like, like)
	}
	if !q.Since.IsZero() {
		add("time >= ?", q.Since.UnixMilli())
	}
	if !q.Until.IsZero() {
		add("time < ?", q.Until.UnixMilli())
	}
	if q.Before > 0 {
		add("id < ?", q.Before)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// Query returns the newest entries first.
func (l *Log) Query(q Query) ([]Entry, error) {
	if q.Limit <= 0 {
		q.Limit = defaultMax
	}
	q.Limit = min(q.Limit, hardMax)
	where, args := q.where()
	rows, err := l.db.Query(`SELECT id, time, ip, action, target, result, detail FROM audit`+where+
		` ORDER BY id DESC LIMIT ?`, append(args, q.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var ms int64
		if err := rows.Scan(&e.ID, &ms, &e.IP, &e.Action, &e.Target, &e.Result, &e.Detail); err != nil {
			return nil, err
		}
		e.Time = time.UnixMilli(ms).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// Prune deletes entries older than the retention and, beyond maxRows, the oldest ones.
func (l *Log) Prune() error {
	if l.retention > 0 {
		cutoff := l.now().Add(-l.retention).UnixMilli()
		if _, err := l.db.Exec(`DELETE FROM audit WHERE time < ?`, cutoff); err != nil {
			return err
		}
	}
	_, err := l.db.Exec(`DELETE FROM audit WHERE id <= (SELECT id FROM audit ORDER BY id DESC LIMIT 1 OFFSET ?)`, maxRows)
	return err
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
	q.Limit = 0
	where, args := q.where()
	rows, err := l.db.Query(`SELECT id, time, ip, action, target, result, detail FROM audit`+where+` ORDER BY id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	var cw *csv.Writer
	var enc *json.Encoder
	switch format {
	case "csv":
		cw = csv.NewWriter(w)
		_ = cw.Write([]string{"id", "time", "ip", "action", "target", "result", "detail"})
	case "json":
		enc = json.NewEncoder(w)
	default:
		return apperror.NewErrorf("unknown format %q", format)
	}
	for rows.Next() {
		var e Entry
		var ms int64
		if err := rows.Scan(&e.ID, &ms, &e.IP, &e.Action, &e.Target, &e.Result, &e.Detail); err != nil {
			return err
		}
		e.Time = time.UnixMilli(ms).UTC()
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
	return rows.Err()
}

var credentials = regexp.MustCompile(`(?i)(://)[^/@\s]+@`)

// Redact removes credentials from addresses, e.g. rest:https://user:secret@host/repo.
func Redact(s string) string { return credentials.ReplaceAllString(s, "${1}***@") }
