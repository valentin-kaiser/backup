// Package store persists the run history as JSON lines.
package store

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/valentin-kaiser/go-core/apperror"
)

// Status of a run.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSuccess   Status = "success"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
	StatusSkipped   Status = "skipped"
)

// Finished reports whether the status is final.
func (s Status) Finished() bool {
	return s != StatusQueued && s != StatusRunning
}

// Run is one execution of a job.
type Run struct {
	ID       string     `json:"id"`
	Job      string     `json:"job"`
	Trigger  string     `json:"trigger"`
	Status   Status     `json:"status"`
	Queued   time.Time  `json:"queued"`
	Started  *time.Time `json:"started,omitempty"`
	Finished *time.Time `json:"finished,omitempty"`
	ExitCode int        `json:"exitCode"`
	Error    string     `json:"error,omitempty"`
	Output   []string   `json:"output,omitempty"`
}

// Store keeps finished runs in memory and appends them to a file.
type Store struct {
	mu    sync.RWMutex
	path  string
	limit int
	runs  []Run
}

// Open loads the history at path and keeps at most limit runs.
func Open(path string, limit int) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, apperror.Wrap(err)
	}
	s := &Store{path: path, limit: limit}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, apperror.Wrap(err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var r Run
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.ID != "" {
			s.runs = append(s.runs, r)
		}
	}
	if len(s.runs) > limit {
		s.runs = s.runs[len(s.runs)-limit:]
		_ = s.rewrite()
	}
	return s, nil
}

// Add appends a finished run.
func (s *Store) Add(r Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs = append(s.runs, r)
	if len(s.runs) > s.limit+s.limit/4 {
		s.runs = s.runs[len(s.runs)-s.limit:]
		return s.rewrite()
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return apperror.Wrap(err)
	}
	defer func() { _ = f.Close() }()
	b, err := json.Marshal(r)
	if err != nil {
		return apperror.Wrap(err)
	}
	_, err = f.Write(append(b, '\n'))
	return apperror.Wrap(err)
}

func (s *Store) rewrite() error {
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return apperror.Wrap(err)
	}
	enc := json.NewEncoder(f)
	for _, r := range s.runs {
		if err := enc.Encode(r); err != nil {
			_ = f.Close()
			return apperror.Wrap(err)
		}
	}
	if err := f.Close(); err != nil {
		return apperror.Wrap(err)
	}
	return apperror.Wrap(os.Rename(tmp, s.path))
}

// List returns runs newest first, optionally filtered by job, at most n (0 = all).
func (s *Store) List(job string, n int) []Run {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Run
	for i := len(s.runs) - 1; i >= 0; i-- {
		if job != "" && s.runs[i].Job != job {
			continue
		}
		out = append(out, s.runs[i])
		if n > 0 && len(out) >= n {
			break
		}
	}
	return out
}

// Get returns the run with id.
func (s *Store) Get(id string) (Run, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := len(s.runs) - 1; i >= 0; i-- {
		if s.runs[i].ID == id {
			return s.runs[i], true
		}
	}
	return Run{}, false
}

// Last returns the newest run of job.
func (s *Store) Last(job string) (Run, bool) {
	l := s.List(job, 1)
	if len(l) == 0 {
		return Run{}, false
	}
	return l[0], true
}

// LastSuccess returns the newest successful run of job.
func (s *Store) LastSuccess(job string) (Run, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := len(s.runs) - 1; i >= 0; i-- {
		if s.runs[i].Job == job && s.runs[i].Status == StatusSuccess {
			return s.runs[i], true
		}
	}
	return Run{}, false
}
