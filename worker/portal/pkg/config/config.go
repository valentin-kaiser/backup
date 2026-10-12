// Package config provides the configuration of the portal process.
package config

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/Valentin-Kaiser/backup/pkg/secure"
	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/config"
	"github.com/valentin-kaiser/go-core/flag"
	"github.com/valentin-kaiser/go-core/logging/log"
)

// Daemon is the one control daemon the portal manages.
type Daemon struct {
	URL                         string `usage:"base URL of the control API, e.g. http://127.0.0.1:8080 or https://backup:8443"`
	CAFile                      string `usage:"CA file that signed the certificate of the daemon"`
	Fingerprint                 string `usage:"SHA-256 fingerprint of the daemon certificate to pin, accepts self-signed certificates"`
	SkipCertificateVerification bool   `usage:"do not verify the certificate of the daemon, prefer fingerprint or ca-file"`
	ClientCertificateFile       string `usage:"client certificate presented to the daemon (mutual TLS)"`
	ClientKeyFile               string `usage:"key of the client certificate"`
}

// Config is the complete configuration of the portal.
type Config struct {
	LogLevel           int    `usage:"(-1 = trace, 0 = debug, 1 = info, 2 = warn, 3 = error)"`
	Address            string `usage:"address to bind the web server to"`
	Port               int    `usage:"port to bind the web server to"`
	TLS                bool   `usage:"serve HTTPS, a self-signed certificate is generated when no certificate files exist"`
	CertificateFile    string `usage:"path to the TLS certificate file, relative to the data directory, generated (self-signed) when it does not exist"`
	KeyFile            string `usage:"path to the TLS key file, relative to the data directory"`
	ClientCAFile       string `usage:"CA file for mutual TLS, browsers must then present a certificate signed by it"`
	PasswordFile       string `usage:"file with the password of the portal and the daemon (a Docker secret), read again when it changes"`
	AuditRetentionDays int    `usage:"days the audit log is kept, 0 keeps it forever"`
	Daemon             Daemon `usage:"the control daemon the portal manages"`
}

// Default returns the default configuration.
func Default() *Config {
	return &Config{
		LogLevel:           1,
		Address:            "127.0.0.1",
		Port:               8443,
		TLS:                true,
		CertificateFile:    "certs/portal.cert",
		KeyFile:            "certs/portal.key",
		Daemon:             Daemon{URL: "http://127.0.0.1:8080"},
		AuditRetentionDays: 365,
	}
}

// Validate implements the go-core config interface.
func (c *Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return apperror.NewError("port must be between 1 and 65535")
	}
	if c.TLS && (strings.TrimSpace(c.CertificateFile) == "" || strings.TrimSpace(c.KeyFile) == "") {
		return apperror.NewError("certificate-file and key-file must not be empty when tls is set")
	}
	u, err := url.Parse(c.Daemon.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.User != nil {
		return apperror.NewErrorf("daemon.url %q must be the base address of the daemon, an http or https URL without path or credentials", c.Daemon.URL)
	}
	if c.Daemon.Fingerprint != "" {
		if _, err := secure.ParseFingerprint(c.Daemon.Fingerprint); err != nil {
			return err
		}
	}
	if (c.Daemon.ClientCertificateFile == "") != (c.Daemon.ClientKeyFile == "") {
		return apperror.NewError("daemon.client-certificate-file and daemon.client-key-file must be set together")
	}
	return nil
}

// Addr returns host:port of the web server.
func (c Config) Addr() string {
	return c.Address + ":" + strconv.Itoa(c.Port)
}

// Init registers and reads the configuration.
func Init() {
	if err := config.Manager().WithName("portal").Register(Default()); err != nil {
		log.Fatal().Err(err).Msg("failed to register configuration")
	}
	flag.Init()
	if err := config.Read(); err != nil {
		log.Fatal().Err(err).Msg("failed to read configuration")
	}
}

// Get returns a copy of the current configuration.
func Get() Config {
	c, ok := config.Get().(*Config)
	if !ok || c == nil {
		return *Default()
	}
	return *c
}

// Watch starts watching the configuration file.
func Watch(ctx context.Context) {
	if err := config.StartWatch(ctx); err != nil {
		log.Error().Err(err).Msg("failed to start configuration watch")
	}
}

// Stop stops watching the configuration file.
func Stop() { config.StopWatch() }

// OnChange registers a typed change handler.
func OnChange(f func(o, n *Config) error) {
	config.OnChange(func(o, n config.Config) error {
		oc, ook := o.(*Config)
		nc, nok := n.(*Config)
		if !ook || !nok || oc == nil || nc == nil {
			return apperror.NewError("configuration has unexpected type")
		}
		return f(oc, nc)
	})
}
