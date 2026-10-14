package api

import (
	"context"
	"net/mail"
	"strings"

	"github.com/Valentin-Kaiser/backup/pkg/notify"
	pb "github.com/Valentin-Kaiser/backup/pkg/proto/api/v1"
	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/logging/log"
	gomail "github.com/valentin-kaiser/go-core/mail"
	"github.com/valentin-kaiser/go-core/web/xrpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// smtpSecret is the stored secret that holds the SMTP password when it is set through the API.
const smtpSecret = "smtp_password"

// transportEditable tells whether the SMTP server can be changed through the API. That needs portal mode, where the
// daemon has a secret store for the password and the portal has no mail settings of its own.
func (s *Service) transportEditable() bool {
	return s.m.Config().Portal() && s.secrets != nil
}

func (s *Service) notificationsToProto(n notify.Settings) *pb.Notifications {
	m := n.Mail
	return &pb.Notifications{
		Enabled:             n.Enabled,
		Recipients:          append([]string{}, n.Recipients...),
		SubjectPrefix:       n.SubjectPrefix,
		CooldownMinutes:     int32(n.CooldownMinutes),
		OnFailure:           n.OnFailure,
		OnSuccess:           n.OnSuccess,
		OnSkipped:           n.OnSkipped,
		OnStale:             n.OnStale,
		OnSecurity:          n.OnSecurity,
		OnLifecycle:         n.OnLifecycle,
		TransportConfigured: m.Enabled && m.Host != "" && m.From != "",
		TransportEditable:   s.transportEditable(),
		SmtpHost:            m.Host,
		SmtpPort:            int32(m.Port),
		SmtpFrom:            m.From,
		SmtpEncryption:      m.Encryption,
		SmtpAuth:            m.Auth,
		SmtpAuthMethod:      m.AuthMethod,
		SmtpUsername:        m.Username,
		SmtpSkipVerify:      m.SkipCertificateVerification,
		SmtpFqdn:            m.FQDN,
		SmtpPasswordSet:     m.Password != "" || n.PasswordFile != "" || n.PasswordSecret != "",
	}
}

// transportFromProto returns the SMTP server of the request on top of the current one, which keeps the timeouts and
// retries the API does not know.
func transportFromProto(in *pb.Notifications, cur gomail.ClientConfig) gomail.ClientConfig {
	m := cur
	m.Host = strings.TrimSpace(in.GetSmtpHost())
	m.Port = int(in.GetSmtpPort())
	m.From = strings.TrimSpace(in.GetSmtpFrom())
	m.Encryption = strings.ToUpper(strings.TrimSpace(in.GetSmtpEncryption()))
	m.Auth = in.GetSmtpAuth()
	m.AuthMethod = strings.ToUpper(strings.TrimSpace(in.GetSmtpAuthMethod()))
	m.Username = strings.TrimSpace(in.GetSmtpUsername())
	m.SkipCertificateVerification = in.GetSmtpSkipVerify()
	m.FQDN = strings.TrimSpace(in.GetSmtpFqdn())
	m.Enabled = m.Host != ""
	if !m.Auth {
		m.AuthMethod, m.Username = "", ""
	}
	return m
}

func notificationsFromProto(in *pb.Notifications) (notify.Settings, error) {
	s := notify.Settings{
		Enabled:         in.GetEnabled(),
		SubjectPrefix:   strings.TrimSpace(in.GetSubjectPrefix()),
		CooldownMinutes: int(in.GetCooldownMinutes()),
		OnFailure:       in.GetOnFailure(),
		OnSuccess:       in.GetOnSuccess(),
		OnSkipped:       in.GetOnSkipped(),
		OnStale:         in.GetOnStale(),
		OnSecurity:      in.GetOnSecurity(),
		OnLifecycle:     in.GetOnLifecycle(),
	}
	seen := map[string]bool{}
	for _, r := range in.GetRecipients() {
		r = strings.TrimSpace(r)
		if r == "" || seen[strings.ToLower(r)] {
			continue
		}
		// Only a plain address, "Name <a@b>" and lists are refused, every entry is one mailbox.
		a, err := mail.ParseAddress(r)
		if err != nil || a.Address != r {
			return s, apperror.NewErrorf("%q is not a mail address", r)
		}
		seen[strings.ToLower(r)] = true
		s.Recipients = append(s.Recipients, r)
	}
	return s, nil
}

// GetNotifications returns which mails this worker sends and through which server.
func (s *Service) GetNotifications(context.Context, *pb.GetNotificationsRequest) (*pb.Notifications, error) {
	return s.notificationsToProto(s.m.Config().Notify), nil
}

// SetNotifications changes which mails this worker sends. In portal mode it also changes the SMTP server and its
// password, which the portal uses through this worker. Otherwise the server must stay as the configuration file has it.
func (s *Service) SetNotifications(_ context.Context, in *pb.Notifications) (*pb.Notifications, error) {
	n, err := notificationsFromProto(in)
	if err != nil {
		return nil, fail(err)
	}
	cur := s.m.Config().Notify
	n.Mail, n.PasswordFile, n.PasswordSecret = cur.Mail, cur.PasswordFile, cur.PasswordSecret
	if s.transportEditable() {
		n.Mail = transportFromProto(in, cur.Mail)
		// The password lives in the secret store, never in the configuration file.
		switch {
		case in.GetClearSmtpPassword():
			n.Mail.Password, n.PasswordFile, n.PasswordSecret = "", "", ""
		case in.GetSmtpPassword() != "":
			n.Mail.Password, n.PasswordFile, n.PasswordSecret = "", "", smtpSecret
		}
	} else if in.GetSmtpPassword() != "" || in.GetClearSmtpPassword() {
		// The server fields are only echoed back by a client and ignored, a password is a real change.
		return nil, xrpc.NewError(xrpc.CodeInvalidParams, "the SMTP server is configured in the configuration file of this worker and cannot be changed here")
	}
	if err := n.Validate(); err != nil {
		return nil, fail(err)
	}
	// The password is stored before the settings that refer to it, the mail client reads it when they are applied.
	changedPassword := s.transportEditable() && (in.GetSmtpPassword() != "" || in.GetClearSmtpPassword())
	if s.transportEditable() {
		if pw := in.GetSmtpPassword(); pw != "" {
			if _, err := s.secrets.Set(smtpSecret, pw); err != nil {
				return nil, xrpc.NewError(xrpc.CodeInvalidParams, err.Error())
			}
			log.Info().Field("secret", smtpSecret).Msg("secret stored")
		}
	}
	if err := s.m.SetNotifications(n); err != nil {
		return nil, fail(err)
	}
	if s.transportEditable() && in.GetClearSmtpPassword() && in.GetSmtpPassword() == "" && s.secrets.Has(smtpSecret) {
		if err := s.secrets.Delete(smtpSecret); err != nil {
			log.Warn().Err(err).Msg("the SMTP password secret could not be removed")
		}
	}
	// A new password under the same secret name is no configuration change, so restart the mail client for it.
	if changedPassword {
		go notify.Restart(context.Background())
	}
	return s.notificationsToProto(s.m.Config().Notify), nil
}

// SendNotification mails a security event of the portal. Only that kind is accepted, the settings, switches and
// cooldown of this worker apply.
func (s *Service) SendNotification(_ context.Context, in *pb.NotificationEvent) (*emptypb.Empty, error) {
	sev := notify.Severity(in.GetSeverity())
	switch sev {
	case notify.SeverityFailure, notify.SeverityWarning, notify.SeveritySuccess, notify.SeverityInfo:
	default:
		sev = notify.SeverityWarning
	}
	e := notify.Event{
		Kind: notify.KindSecurity, Severity: sev, Title: clip(in.GetTitle(), 200), Key: clip(in.GetKey(), 200),
		Detail: clip(in.GetDetail(), 4000),
	}
	for i, f := range in.GetFields() {
		if i == 20 {
			break
		}
		e.Fields = append(e.Fields, notify.Field{Label: clip(f.GetLabel(), 64), Value: clip(f.GetValue(), 500)})
	}
	notify.Send(e)
	return &emptypb.Empty{}, nil
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// TestNotifications sends a test mail to the recipients.
func (s *Service) TestNotifications(ctx context.Context, _ *pb.TestNotificationsRequest) (*pb.TestNotificationsResponse, error) {
	to, err := notify.SendTest(ctx)
	if err != nil {
		return nil, fail(err)
	}
	return &pb.TestNotificationsResponse{Recipients: to}, nil
}
