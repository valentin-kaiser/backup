package notify

import (
	"strings"
	"testing"
	"time"
)

func reset() {
	mu.Lock()
	lastSent = map[string]time.Time{}
	mu.Unlock()
}

func TestAllowed(t *testing.T) {
	s := Defaults()
	s.Enabled = true
	cases := []struct {
		kind Kind
		sev  Severity
		want bool
	}{
		{KindJob, SeverityFailure, true},
		{KindJob, SeveritySuccess, false},
		{KindJob, SeverityWarning, false},
		{KindStale, SeverityFailure, true},
		{KindSecurity, SeverityWarning, true},
		{KindLifecycle, SeverityInfo, false},
	}
	for _, c := range cases {
		if got := allowed(s, c.kind, c.sev); got != c.want {
			t.Errorf("%s/%s = %v, want %v", c.kind, c.sev, got, c.want)
		}
	}
	s.Enabled = false
	if allowed(s, KindJob, SeverityFailure) {
		t.Error("disabled settings must send nothing")
	}
}

func TestCooldown(t *testing.T) {
	reset()
	fail := Event{Kind: KindJob, Severity: SeverityFailure, Key: "a"}
	ok := Event{Kind: KindJob, Severity: SeveritySuccess, Key: "a"}
	if !allow(fail, 60) || allow(fail, 60) {
		t.Fatal("the second failure of the same job inside the cooldown must be suppressed")
	}
	if !allow(Event{Kind: KindJob, Severity: SeverityFailure, Key: "b"}, 60) {
		t.Error("another job must not be muted")
	}
	if !allow(ok, 60) {
		t.Fatal("first success")
	}
	if allow(ok, 60) {
		t.Fatal("repeated success inside the cooldown")
	}
	allow(fail, 0)
	if !allow(ok, 60) {
		t.Error("a failure must clear the success cooldown of the job")
	}
	if !allow(fail, 0) || !allow(fail, 0) {
		t.Error("a cooldown of 0 never suppresses")
	}
}

func TestValidate(t *testing.T) {
	s := Defaults()
	if err := s.Validate(); err != nil {
		t.Fatalf("disabled defaults: %v", err)
	}
	s.CooldownMinutes = -1
	if s.Validate() == nil {
		t.Error("negative cooldown")
	}
	s = Defaults()
	s.Enabled = true
	if s.Validate() == nil {
		t.Error("enabled without recipients")
	}
	s.Recipients = []string{"a@example.com"}
	if s.Validate() == nil {
		t.Error("enabled without mail client")
	}
	s.Mail.Enabled, s.Mail.Host, s.Mail.Port, s.Mail.From = true, "smtp", 25, "backup@example.com"
	if err := s.Validate(); err != nil {
		t.Errorf("valid: %v", err)
	}
	s.Mail.Auth, s.Mail.Username = true, "u"
	if s.Validate() == nil {
		t.Error("auth without password")
	}
	s.PasswordFile = "/run/secrets/smtp"
	if err := s.Validate(); err != nil {
		t.Errorf("password from file: %v", err)
	}
	s.PasswordSecret = "smtp"
	if s.Validate() == nil {
		t.Error("file and secret exclude each other")
	}
}

func TestBuild(t *testing.T) {
	s := Defaults()
	s.Recipients = []string{"ops@example.com"}
	s.Mail.From = "backup@example.com"
	e := Event{
		Kind: KindJob, Severity: SeverityFailure, Title: "backup failed", Key: "backup",
		Fields: []Field{{Label: "Job", Value: "backup"}}, Detail: "exit status 1 <script>", Time: time.Now(),
	}
	if _, err := build(s, "host", e); err != nil {
		t.Fatalf("build: %v", err)
	}
	txt := text(view{Event: e, Label: "Failure", Host: "host", When: "now"})
	for _, want := range []string{"backup failed", "Failure", "Job:", "exit status 1"} {
		if !strings.Contains(txt, want) {
			t.Errorf("text body lacks %q:\n%s", want, txt)
		}
	}
}

func TestRelayTakesEveryEvent(t *testing.T) {
	var got []Event
	SetRelay(func(e Event) { got = append(got, e) })
	t.Cleanup(func() { SetRelay(nil) })
	Send(Event{Kind: KindSecurity, Severity: SeverityWarning, Title: "x"})
	if len(got) != 1 || got[0].Title != "x" {
		t.Errorf("relayed %v", got)
	}
}
