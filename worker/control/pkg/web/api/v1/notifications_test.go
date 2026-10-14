package api

import (
	"context"
	"testing"

	"github.com/Valentin-Kaiser/backup/pkg/notify"
	pb "github.com/Valentin-Kaiser/backup/pkg/proto/api/v1"
)

func TestNotificationsFromProto(t *testing.T) {
	s, err := notificationsFromProto(&pb.Notifications{
		Enabled: true, SubjectPrefix: " [X] ", CooldownMinutes: 5, OnFailure: true,
		Recipients: []string{" a@example.com ", "A@example.com", "", "b@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Recipients) != 2 || s.Recipients[0] != "a@example.com" || s.Recipients[1] != "b@example.com" {
		t.Errorf("recipients are trimmed and unique: %v", s.Recipients)
	}
	if !s.Enabled || s.SubjectPrefix != "[X]" || s.CooldownMinutes != 5 || !s.OnFailure || s.OnSuccess {
		t.Errorf("settings: %+v", s)
	}
	for _, bad := range []string{"nobody", "Name <a@example.com>", "a@example.com, b@example.com"} {
		if _, err := notificationsFromProto(&pb.Notifications{Recipients: []string{bad}}); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestNotificationsToProtoReportsTransport(t *testing.T) {
	s, _ := secretService(t, &repoRestic{})
	cfg := notify.Defaults()
	cfg.Mail.Enabled, cfg.Mail.Host, cfg.Mail.Port, cfg.Mail.From, cfg.Mail.Password = true, "smtp.example.com", 587, "backup@example.com", "secret"
	n := s.notificationsToProto(cfg)
	if !n.TransportConfigured || !n.TransportEditable || n.SmtpHost != "smtp.example.com" || n.SmtpPort != 587 || n.SmtpFrom != "backup@example.com" || !n.SmtpPasswordSet {
		t.Errorf("transport: %+v", n)
	}
	if n.SmtpPassword != "" {
		t.Error("the password must never be returned")
	}
	if s.notificationsToProto(notify.Defaults()).TransportConfigured {
		t.Error("no host, no transport")
	}
}

func TestSetNotificationsStoresTransportAndPassword(t *testing.T) {
	s, store := secretService(t, &repoRestic{})
	ctx := context.Background()
	in := &pb.Notifications{
		Enabled: true, Recipients: []string{"ops@example.com"}, OnFailure: true,
		SmtpHost: " smtp.example.com ", SmtpPort: 587, SmtpFrom: "backup@example.com", SmtpEncryption: "starttls",
		SmtpAuth: true, SmtpAuthMethod: "plain", SmtpUsername: "backup", SmtpPassword: "hunter2",
	}
	out, err := s.SetNotifications(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.SmtpHost != "smtp.example.com" || out.SmtpEncryption != "STARTTLS" || out.SmtpAuthMethod != "PLAIN" || !out.SmtpPasswordSet || !out.TransportConfigured {
		t.Errorf("result: %+v", out)
	}
	if v, _ := store.Get(smtpSecret); v != "hunter2" {
		t.Errorf("stored password %q", v)
	}
	cfg := s.m.Config().Notify
	if cfg.PasswordSecret != smtpSecret || cfg.Mail.Password != "" {
		t.Errorf("the password belongs in the secret store: %+v", cfg)
	}

	// An empty password keeps the stored one.
	in.SmtpPassword, in.SmtpPort = "", 25
	if out, err = s.SetNotifications(ctx, in); err != nil || out.SmtpPort != 25 || !out.SmtpPasswordSet {
		t.Fatalf("keep = %+v, %v", out, err)
	}
	if v, _ := store.Get(smtpSecret); v != "hunter2" {
		t.Errorf("password changed to %q", v)
	}

	// Clearing removes the secret and the reference.
	in.SmtpAuth, in.ClearSmtpPassword = false, true
	if out, err = s.SetNotifications(ctx, in); err != nil || out.SmtpPasswordSet {
		t.Fatalf("clear = %+v, %v", out, err)
	}
	if store.Has(smtpSecret) {
		t.Error("the secret must be removed")
	}
}

func TestSetNotificationsRefusesInvalidTransport(t *testing.T) {
	s, store := secretService(t, &repoRestic{})
	_, err := s.SetNotifications(context.Background(), &pb.Notifications{
		Enabled: true, Recipients: []string{"ops@example.com"}, SmtpHost: "smtp.example.com", SmtpPassword: "x",
	})
	if err == nil {
		t.Error("an enabled setup without a sender must be refused")
	}
	if store.Has(smtpSecret) {
		t.Error("a refused request must not store the password")
	}
}

func TestSetNotificationsKeepsTransportOutsidePortalMode(t *testing.T) {
	s, _ := newService(t)
	if s.transportEditable() {
		t.Fatal("no secret store, no editable transport")
	}
	// The echoed server fields are ignored, only a password is a real change.
	out, err := s.SetNotifications(context.Background(), &pb.Notifications{SmtpHost: "other.example.com", OnFailure: true})
	if err != nil || out.SmtpHost != "" || out.TransportEditable {
		t.Errorf("echo = %+v, %v", out, err)
	}
	if _, err := s.SetNotifications(context.Background(), &pb.Notifications{SmtpPassword: "x"}); err == nil {
		t.Error("a password cannot be set here")
	}
}
