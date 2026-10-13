package audit

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

var dbs int

func newLog(t *testing.T, retention time.Duration) *Log {
	t.Helper()
	dbs++
	db, err := sql.Open("sqlite", fmt.Sprintf("file:audit%d?mode=memory&cache=shared", dbs))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	l, err := New(db, retention)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestAddAndQueryNewestFirst(t *testing.T) {
	l := newLog(t, 0)
	l.Add(Entry{IP: "1.1.1.1", Action: LoginOK})
	l.Add(Entry{IP: "1.1.1.1", Action: "start_job", Target: "backup", Result: Failed, Detail: "job is already queued"})
	got, err := l.Query(Query{})
	if err != nil || len(got) != 2 {
		t.Fatalf("query = %v, %v", got, err)
	}
	if got[0].Action != "start_job" || got[0].Result != Failed || got[1].Result != OK {
		t.Errorf("entries = %+v", got)
	}
	if got[0].Time.IsZero() || got[0].ID <= got[1].ID {
		t.Errorf("time/id = %v %v", got[0].Time, got[0].ID)
	}
}

func TestANilLogIgnoresEntries(t *testing.T) {
	var l *Log
	l.Add(Entry{Action: LoginOK}) // must not panic
}

func TestFilters(t *testing.T) {
	l := newLog(t, 0)
	now := time.Now()
	l.Add(Entry{Action: LoginOK, Time: now.Add(-3 * time.Hour)})
	l.Add(Entry{Action: LoginFailed, Result: Denied, Detail: "wrong password", Time: now.Add(-2 * time.Hour)})
	l.Add(Entry{Action: "start_job", Target: "photos", Time: now.Add(-time.Hour)})
	l.Add(Entry{Action: "start_job", Target: "docs_100%", Time: now})

	count := func(q Query) int {
		got, err := l.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	for name, tc := range map[string]struct {
		q    Query
		want int
	}{
		"action":                          {Query{Action: "start_job"}, 2},
		"result":                          {Query{Result: Denied}, 1},
		"text in target":                  {Query{Text: "photos"}, 1},
		"text in detail":                  {Query{Text: "wrong"}, 1},
		"percent is literal":              {Query{Text: "100%"}, 1},
		"percent alone is not a wildcard": {Query{Text: "%"}, 1},
		"underscore is literal":           {Query{Text: "docs_"}, 1},
		"since":                           {Query{Since: now.Add(-90 * time.Minute)}, 2},
		"until":                           {Query{Until: now.Add(-90 * time.Minute)}, 2},
		"combined":                        {Query{Action: "start_job", Text: "photos"}, 1},
		"limit":                           {Query{Limit: 3}, 3},
	} {
		if got := count(tc.q); got != tc.want {
			t.Errorf("%s: %d, want %d", name, got, tc.want)
		}
	}
}

func TestPaging(t *testing.T) {
	l := newLog(t, 0)
	for i := 0; i < 25; i++ {
		l.Add(Entry{Action: "a"})
	}
	first, _ := l.Query(Query{Limit: 10})
	second, _ := l.Query(Query{Limit: 10, Before: first[len(first)-1].ID})
	third, _ := l.Query(Query{Limit: 10, Before: second[len(second)-1].ID})
	if len(first) != 10 || len(second) != 10 || len(third) != 5 {
		t.Fatalf("pages = %d %d %d", len(first), len(second), len(third))
	}
	if first[9].ID <= second[0].ID {
		t.Error("pages must not overlap")
	}
}

func TestInjectionAndControlCharactersAreNeutralised(t *testing.T) {
	l := newLog(t, 0)
	l.Add(Entry{Action: "x", Target: "a'; DROP TABLE audit;--", Detail: "line1\nline2\r\x00end"})
	got, err := l.Query(Query{})
	if err != nil || len(got) != 1 {
		t.Fatalf("query = %v, %v", got, err)
	}
	if strings.ContainsAny(got[0].Detail, "\n\r\x00") || got[0].Detail != "line1 line2  end" {
		t.Errorf("detail = %q", got[0].Detail)
	}
	if got[0].Target != "a'; DROP TABLE audit;--" {
		t.Errorf("target = %q", got[0].Target)
	}
	if _, err := l.Query(Query{Text: "' OR 1=1 --"}); err != nil {
		t.Fatal(err)
	}
}

func TestLongValuesAreShortened(t *testing.T) {
	l := newLog(t, 0)
	l.Add(Entry{Action: "x", Detail: strings.Repeat("é", 5000), Target: strings.Repeat("t", 5000)})
	got, _ := l.Query(Query{})
	if len([]rune(got[0].Detail)) > maxDetail || len([]rune(got[0].Target)) > maxTarget {
		t.Errorf("lengths %d %d", len([]rune(got[0].Detail)), len([]rune(got[0].Target)))
	}
	if !strings.HasSuffix(got[0].Detail, "…") {
		t.Error("a shortened value must say so")
	}
}

func TestPruneKeepsRecentEntries(t *testing.T) {
	l := newLog(t, 24*time.Hour)
	now := time.Now()
	l.Add(Entry{Action: "old", Time: now.Add(-48 * time.Hour)})
	l.Add(Entry{Action: "new", Time: now.Add(-time.Hour)})
	if err := l.Prune(); err != nil {
		t.Fatal(err)
	}
	got, _ := l.Query(Query{})
	if len(got) != 1 || got[0].Action != "new" {
		t.Fatalf("after prune = %+v", got)
	}
	forever := newLog(t, 0)
	forever.Add(Entry{Action: "old", Time: now.Add(-100 * 24 * 365 * time.Hour)})
	_ = forever.Prune()
	if got, _ := forever.Query(Query{}); len(got) != 1 {
		t.Error("a retention of zero keeps everything")
	}
}

func TestExportCSVNeutralisesFormulas(t *testing.T) {
	l := newLog(t, 0)
	l.Add(Entry{Action: "set_secret", Target: "=HYPERLINK(\"http://evil\")", Detail: "ok, with comma"})
	l.Add(Entry{Action: "start_job", Target: "photos"})
	var buf bytes.Buffer
	if err := l.Export(&buf, "csv", Query{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "id,time,ip,action,target,result,detail\n") {
		t.Errorf("header: %q", out)
	}
	if strings.Contains(out, ",=HYPERLINK") || !strings.Contains(out, `'=HYPERLINK`) {
		t.Errorf("a cell that starts with = must be neutralised:\n%s", out)
	}
	if strings.Index(out, "set_secret") > strings.Index(out, "start_job") {
		t.Error("an export is oldest first")
	}
}

func TestExportJSONLines(t *testing.T) {
	l := newLog(t, 0)
	l.Add(Entry{Action: "a", Target: "x"})
	l.Add(Entry{Action: "b", Target: "y"})
	var buf bytes.Buffer
	if err := l.Export(&buf, "json", Query{Action: "b"}); err != nil {
		t.Fatal(err)
	}
	var e Entry
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &e); err != nil || e.Action != "b" || e.Target != "y" {
		t.Fatalf("export = %q, %v", buf.String(), err)
	}
	if err := l.Export(&buf, "xml", Query{}); err == nil {
		t.Error("an unknown format must be refused")
	}
}

func TestRedactRemovesCredentialsFromAddresses(t *testing.T) {
	for in, want := range map[string]string{
		"rest:https://user:secret@host/repo":          "rest:https://***@host/repo",
		"sftp://me@host:22/path":                      "sftp://***@host:22/path",
		"REST:HTTPS://u:p@h":                          "REST:HTTPS://***@h",
		"s3:https://s3.example/bucket":                "s3:https://s3.example/bucket",
		"/srv/backup":                                 "/srv/backup",
		"two rest:http://a:b@x and rest:http://c:d@y": "two rest:http://***@x and rest:http://***@y",
	} {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExportRefusesMoreThanTheLimit(t *testing.T) {
	l := newLog(t, 0)
	for i := 0; i < MaxExport+1; i++ {
		l.Add(Entry{Action: "start_job"})
	}
	if err := l.Export(io.Discard, "json", Query{}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
	if err := l.Export(io.Discard, "json", Query{Before: 100}); err != nil {
		t.Fatalf("a narrowed export must work: %v", err)
	}
}
