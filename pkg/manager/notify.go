package manager

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/config"
	"github.com/Valentin-Kaiser/backup/pkg/notify"
	"github.com/Valentin-Kaiser/backup/pkg/store"
)

const (
	// staleInterval is how often the jobs are checked for a missing successful run.
	staleInterval = 15 * time.Minute
	// staleRemind repeats the mail about a job that is still stale.
	staleRemind = 24 * time.Hour
	// mailOutputLines is the number of console lines that go into a job mail.
	mailOutputLines = 100
)

// notifyRun mails the result of a finished run according to the notification settings.
func (m *Manager) notifyRun(r store.Run) {
	var sev notify.Severity
	var verb string
	switch r.Status {
	case store.StatusSuccess:
		sev, verb = notify.SeveritySuccess, "succeeded"
	case store.StatusFailed:
		sev, verb = notify.SeverityFailure, "failed"
	case store.StatusSkipped:
		sev, verb = notify.SeverityWarning, "was skipped"
	case store.StatusCancelled:
		sev, verb = notify.SeverityWarning, "was cancelled"
	default:
		return
	}
	if !notify.Enabled(notify.KindJob, sev) {
		return
	}
	f := []notify.Field{
		{Label: "Job", Value: r.Job},
		{Label: "Status", Value: string(r.Status)},
		{Label: "Trigger", Value: r.Trigger},
	}
	if r.Started != nil && r.Finished != nil {
		f = append(f, notify.Field{Label: "Duration", Value: r.Finished.Sub(*r.Started).Round(time.Second).String()})
	}
	if r.Status == store.StatusFailed {
		f = append(f, notify.Field{Label: "Exit code", Value: strconv.Itoa(r.ExitCode)})
	}
	detail := r.Error
	if len(r.Output) > 0 {
		out := r.Output
		if len(out) > mailOutputLines {
			out = out[len(out)-mailOutputLines:]
		}
		if detail != "" {
			detail += "\n\n"
		}
		heading := "Console log:"
		if len(out) < len(r.Output) {
			heading = fmt.Sprintf("Console log (last %d of %d lines):", len(out), len(r.Output))
		}
		detail += heading + "\n" + strings.Join(out, "\n")
	}
	notify.Send(notify.Event{
		Kind:     notify.KindJob,
		Severity: sev,
		Title:    fmt.Sprintf("%s %s", r.Job, verb),
		Key:      r.Job,
		Fields:   f,
		Detail:   detail,
	})
}

// staleLoop mails about jobs that have not succeeded within their maxAgeHours.
func (m *Manager) staleLoop() {
	defer m.wg.Done()
	t := time.NewTicker(staleInterval)
	defer t.Stop()
	sent := map[string]time.Time{}
	for {
		m.checkStale(sent)
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
		}
	}
}

// checkStale sends one mail per stale job, repeated every staleRemind, and forgets jobs that are healthy again.
func (m *Manager) checkStale(sent map[string]time.Time) {
	if !notify.Enabled(notify.KindStale, notify.SeverityFailure) {
		return
	}
	stale := map[string]string{}
	for _, j := range m.cfg().Jobs {
		if !j.Enabled || j.Mirror() || j.MaxAgeHours <= 0 {
			continue
		}
		maxAge := time.Duration(j.MaxAgeHours) * time.Hour
		last, ok := m.store.LastSuccess(j.Name)
		switch {
		case ok && last.Finished != nil && time.Since(*last.Finished) > maxAge:
			stale[j.Name] = fmt.Sprintf("Last success %s ago (limit %s)", time.Since(*last.Finished).Round(time.Minute), maxAge)
		case !ok && time.Since(m.started) > maxAge:
			stale[j.Name] = fmt.Sprintf("No successful run since the daemon started %s ago (limit %s)", time.Since(m.started).Round(time.Minute), maxAge)
		}
	}
	for name := range sent {
		if _, still := stale[name]; !still {
			delete(sent, name)
		}
	}
	for name, why := range stale {
		if at, ok := sent[name]; ok && time.Since(at) < staleRemind {
			continue
		}
		sent[name] = time.Now()
		notify.Send(notify.Event{
			Kind:     notify.KindStale,
			Severity: notify.SeverityFailure,
			Title:    name + " has no recent successful run",
			Key:      name,
			Fields:   []notify.Field{{Label: "Job", Value: name}},
			Detail:   why,
		})
	}
}

// SetNotifications replaces the notification settings, including the SMTP server and the source of its password.
func (m *Manager) SetNotifications(s notify.Settings) error {
	return m.change(func(c *config.Config) error {
		c.Notify = s
		return nil
	})
}
