// Package notify sends mail notifications about job results, stale backups, security and lifecycle events.
//
// It wraps github.com/valentin-kaiser/go-core/mail. The SMTP transport and the user-facing switches live in
// Settings, which the configuration of the control daemon embeds. The portal has none, it relays its events to the daemon (SetRelay).
package notify

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/logging/log"
	"github.com/valentin-kaiser/go-core/mail"
)

//go:embed templates/*.html
var templatesFS embed.FS

const templateName = "templates/event.html"

// Kind classifies an event, the settings switch kinds on and off.
type Kind string

const (
	// KindJob is the result of a job run.
	KindJob Kind = "job"
	// KindStale is a job that has not succeeded within its MaxAgeHours.
	KindStale Kind = "stale"
	// KindSecurity is a failed sign-in, a locked address or a refused call.
	KindSecurity Kind = "security"
	// KindLifecycle is the start or stop of the daemon and configuration problems.
	KindLifecycle Kind = "lifecycle"
)

// Severity colours the mail and decides which switch applies to a job event.
type Severity string

const (
	SeverityFailure Severity = "failure"
	SeverityWarning Severity = "warning"
	SeveritySuccess Severity = "success"
	SeverityInfo    Severity = "info"
)

// Settings is the notification configuration.
type Settings struct {
	Enabled         bool              `usage:"send mail notifications"`
	Mail            mail.ClientConfig `usage:"SMTP transport"`
	PasswordFile    string            `usage:"file with the SMTP password, replaces mail.password"`
	PasswordSecret  string            `usage:"stored secret with the SMTP password, replaces passwordFile and mail.password (control in portal mode only)"`
	Recipients      []string          `usage:"recipient addresses"`
	SubjectPrefix   string            `usage:"prefix of the subject of every mail"`
	CooldownMinutes int               `usage:"minimum minutes between two mails of the same kind, severity and subject, 0 = no cooldown"`
	OnFailure       bool              `usage:"mail failed job runs"`
	OnSuccess       bool              `usage:"mail successful job runs"`
	OnSkipped       bool              `usage:"mail skipped and cancelled job runs"`
	OnStale         bool              `usage:"mail jobs without a successful run within their maxAgeHours"`
	OnSecurity      bool              `usage:"mail failed sign-ins, locked addresses and refused calls"`
	OnLifecycle     bool              `usage:"mail daemon start and stop"`
}

// Defaults returns the default settings: disabled, failures, stale backups and security events switched on.
func Defaults() Settings {
	return Settings{
		SubjectPrefix:   "[BACKUP]",
		CooldownMinutes: 60,
		OnFailure:       true,
		OnStale:         true,
		OnSecurity:      true,
	}
}

// Validate checks the settings, it only complains when notifications are enabled.
func (s Settings) Validate() error {
	if s.CooldownMinutes < 0 {
		return apperror.NewError("notify.cooldownminutes must not be negative")
	}
	if !s.Enabled {
		return nil
	}
	if len(s.Recipients) == 0 {
		return apperror.NewError("notify.recipients must not be empty when notifications are enabled")
	}
	if !s.Mail.Enabled {
		return apperror.NewError("notify.mail.enabled must be true when notifications are enabled")
	}
	if strings.TrimSpace(s.Mail.From) == "" {
		return apperror.NewError("notify.mail.from must be set when notifications are enabled")
	}
	if s.PasswordFile != "" && s.PasswordSecret != "" {
		return apperror.NewError("notify.passwordfile and notify.passwordsecret exclude each other")
	}
	// The password of an authenticated connection may come from a file or secret, which are read at start.
	c := s.Mail
	if c.Auth && c.Password == "" && (s.PasswordFile != "" || s.PasswordSecret != "") {
		c.Password = "-"
	}
	return c.Validate()
}

// Field is one labelled value of the mail.
type Field struct {
	Label string
	Value string
}

// Event is one notification.
type Event struct {
	Kind     Kind
	Severity Severity
	// Title is the headline and the subject, without prefix.
	Title string
	// Key tells events of the same kind and severity apart for the cooldown, e.g. the job name or the address.
	Key    string
	Fields []Field
	// Detail is preformatted text such as the error or the last lines of the output.
	Detail string
	Time   time.Time
}

// Resolver reads a password from a stored secret or a file.
type Resolver func(secret, file string) (string, error)

// FileResolver reads passwords from files and refuses stored secrets.
func FileResolver(secret, file string) (string, error) {
	if secret != "" {
		return "", apperror.NewErrorf("the secret %s is configured but there is no secret store", secret)
	}
	if file == "" {
		return "", nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", apperror.NewErrorf("the SMTP password file %s cannot be read", file).AddError(err)
	}
	return strings.TrimSpace(string(b)), nil
}

var (
	mu       sync.Mutex
	manager  *mail.Manager
	get      func() Settings
	resolve  Resolver = FileResolver
	lastSent          = map[string]time.Time{}
	pending  sync.WaitGroup
	hostname string
	relay    func(Event)
)

// Configure sets where the current settings come from. resolve may be nil to read passwords from files only.
func Configure(settings func() Settings, r Resolver) {
	mu.Lock()
	defer mu.Unlock()
	get = settings
	if r != nil {
		resolve = r
	}
	hostname, _ = os.Hostname()
}

func current() Settings {
	mu.Lock()
	g := get
	mu.Unlock()
	if g == nil {
		return Settings{}
	}
	return g()
}

// Start starts the mail manager when notifications and the mail client are enabled. Disabled is not an error.
func Start() error {
	mu.Lock()
	defer mu.Unlock()
	if manager != nil || get == nil {
		return nil
	}
	s := get()
	if !s.Enabled || !s.Mail.Enabled {
		log.Debug().Msg("notify: notifications disabled")
		return nil
	}
	client := s.Mail
	if s.PasswordFile != "" || s.PasswordSecret != "" {
		pw, err := resolve(s.PasswordSecret, s.PasswordFile)
		if err != nil {
			return apperror.NewError("failed to read the SMTP password").AddError(err)
		}
		client.Password = pw
	}
	m := mail.NewManager(&mail.Config{
		Client: client,
		Templates: mail.TemplateConfig{
			Enabled:          true,
			DefaultTemplate:  templateName,
			FileSystem:       templatesFS,
			WithDefaultFuncs: true,
			GlobalFuncs:      templateFuncs,
		},
	}, nil)
	if err := m.Start(); err != nil {
		return apperror.NewError("failed to start the mail manager").AddError(err)
	}
	manager = m
	log.Info().Msg("notify: mail manager started")
	return nil
}

// Stop waits for the mails that are being sent and stops the mail manager.
func Stop(ctx context.Context) {
	Wait(ctx)
	mu.Lock()
	defer mu.Unlock()
	if manager == nil {
		return
	}
	if err := manager.Stop(ctx); err != nil {
		log.Error().Err(err).Msg("notify: failed to stop the mail manager")
	}
	manager = nil
}

// Restart picks up changed transport settings.
func Restart(ctx context.Context) {
	Stop(ctx)
	if err := Start(); err != nil {
		log.Error().Err(err).Msg("notify: failed to restart the mail manager")
	}
}

// Wait blocks until all mails handed to Send are delivered or ctx ends. Call it before the process exits.
func Wait(ctx context.Context) {
	done := make(chan struct{})
	go func() { pending.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Enabled reports whether an event of this kind and severity would be sent, so callers can skip building it.
func Enabled(k Kind, sev Severity) bool {
	return allowed(current(), k, sev)
}

func allowed(s Settings, k Kind, sev Severity) bool {
	if !s.Enabled {
		return false
	}
	switch k {
	case KindJob:
		switch sev {
		case SeverityFailure:
			return s.OnFailure
		case SeveritySuccess:
			return s.OnSuccess
		default:
			return s.OnSkipped
		}
	case KindStale:
		return s.OnStale
	case KindSecurity:
		return s.OnSecurity
	case KindLifecycle:
		return s.OnLifecycle
	}
	return false
}

// SetRelay hands every event to f instead of mailing it here. The portal uses it to let the daemon mail with its settings.
func SetRelay(f func(Event)) {
	mu.Lock()
	defer mu.Unlock()
	relay = f
}

// Send mails the event according to the settings. It returns at once, the delivery runs in the background.
func Send(e Event) {
	mu.Lock()
	r := relay
	mu.Unlock()
	if r != nil {
		r(e)
		return
	}
	s := current()
	if !allowed(s, e.Kind, e.Severity) {
		return
	}
	if len(s.Recipients) == 0 {
		log.Warn().Msg("notify: notifications enabled but no recipients configured")
		return
	}
	if !allow(e, s.CooldownMinutes) {
		log.Debug().Msgf("notify: %s %s %q suppressed by the cooldown", e.Kind, e.Severity, e.Key)
		return
	}
	mu.Lock()
	m := manager
	host := hostname
	mu.Unlock()
	if m == nil || !m.IsRunning() {
		log.Warn().Msg("notify: mail requested but the mail manager is not running")
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	msg, err := build(s, host, e)
	if err != nil {
		log.Error().Err(err).Msg("notify: failed to build the message")
		return
	}
	pending.Add(1)
	go func() {
		defer pending.Done()
		// Detached from the caller so a job or a shutdown does not cancel the delivery, but bounded.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := m.Send(ctx, msg); err != nil {
			log.Error().Err(err).Msgf("notify: failed to send %s mail", e.Kind)
			return
		}
		log.Info().Msgf("notify: sent %s %s mail %q", e.Kind, e.Severity, e.Title)
	}()
}

// allow applies the cooldown per kind, severity and key. A failure clears the success cooldown of the same key, so a
// success after a failure is always reported.
func allow(e Event, cooldownMinutes int) bool {
	mu.Lock()
	defer mu.Unlock()
	id := string(e.Kind) + "|" + string(e.Severity) + "|" + e.Key
	if cooldownMinutes > 0 {
		if last, ok := lastSent[id]; ok && time.Since(last) < time.Duration(cooldownMinutes)*time.Minute {
			return false
		}
	}
	lastSent[id] = time.Now()
	if e.Severity == SeverityFailure {
		delete(lastSent, string(e.Kind)+"|"+string(SeveritySuccess)+"|"+e.Key)
	}
	return true
}

// view is what the template renders.
type view struct {
	Event
	Label    string
	Host     string
	When     string
	Color    string
	Preheads string
}

func build(s Settings, host string, e Event) (*mail.Message, error) {
	prefix := strings.TrimSpace(s.SubjectPrefix)
	subject := strings.TrimSpace(prefix + " " + e.Title)
	v := view{
		Event:    e,
		Label:    label(e.Severity),
		Host:     host,
		When:     e.Time.Format("2006-01-02 15:04:05 MST"),
		Color:    color(e.Severity),
		Preheads: e.Title,
	}
	b := mail.NewMessage().
		To(s.Recipients...).
		Subject(subject).
		TextBody(text(v)).
		Template(templateName, v)
	if s.Mail.From != "" {
		b = b.From(s.Mail.From)
	}
	msg, err := b.Build()
	if err != nil {
		return nil, apperror.NewError("failed to build the message").AddError(err)
	}
	return msg, nil
}

func text(v view) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s\n\n", v.Title, strings.Repeat("=", len(v.Title)))
	fmt.Fprintf(&b, "%-10s %s\n", "Severity:", v.Label)
	if v.Host != "" {
		fmt.Fprintf(&b, "%-10s %s\n", "Host:", v.Host)
	}
	fmt.Fprintf(&b, "%-10s %s\n", "Time:", v.When)
	for _, f := range v.Fields {
		fmt.Fprintf(&b, "%-10s %s\n", f.Label+":", f.Value)
	}
	if v.Detail != "" {
		fmt.Fprintf(&b, "\n%s\n", v.Detail)
	}
	return b.String()
}

func label(s Severity) string {
	switch s {
	case SeverityFailure:
		return "Failure"
	case SeverityWarning:
		return "Warning"
	case SeveritySuccess:
		return "Success"
	}
	return "Info"
}

func color(s Severity) string {
	switch s {
	case SeverityFailure:
		return "#dc2626"
	case SeverityWarning:
		return "#d97706"
	case SeveritySuccess:
		return "#059669"
	}
	return "#2563eb"
}

var templateFuncs = template.FuncMap{}

// SendTest mails a test message to the recipients and waits for the delivery. It ignores the cooldown and the event
// switches, so it only tests the transport and the addresses.
func SendTest(ctx context.Context) ([]string, error) {
	s := current()
	if !s.Enabled || !s.Mail.Enabled {
		return nil, apperror.NewError("notifications are not enabled, or no SMTP server is configured")
	}
	if len(s.Recipients) == 0 {
		return nil, apperror.NewError("there are no recipients")
	}
	mu.Lock()
	m := manager
	host := hostname
	mu.Unlock()
	if m == nil || !m.IsRunning() {
		return nil, apperror.NewError("the mail manager is not running, check the SMTP settings and the log")
	}
	msg, err := build(s, host, Event{
		Kind: KindLifecycle, Severity: SeverityInfo, Title: "Test mail", Time: time.Now(),
		Fields: []Field{{Label: "Recipients", Value: strings.Join(s.Recipients, ", ")}},
		Detail: "If you can read this, backup can send mail.",
	})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := m.Send(ctx, msg); err != nil {
		return nil, apperror.NewError("the test mail could not be sent").AddError(err)
	}
	return s.Recipients, nil
}
