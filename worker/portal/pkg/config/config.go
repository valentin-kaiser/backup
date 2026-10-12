// Package config provides the configuration of the portal process.
package config

import (
	"context"
	"net/url"
	"strconv"

	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/config"
	"github.com/valentin-kaiser/go-core/flag"
	"github.com/valentin-kaiser/go-core/logging/log"
)

// Daemon is the control daemon the UI talks to.
type Daemon struct {
	URL                         string `usage:"base URL of the control API, e.g. http://127.0.0.1:8080 or https://backup:8443"`
	SkipCertificateVerification bool   `usage:"do not verify the certificate of the daemon, it uses a self-signed one by default"`
}

// Config is the complete configuration of the portal.
type Config struct {
	LogLevel        int    `usage:"(-1 = trace, 0 = debug, 1 = info, 2 = warn, 3 = error)"`
	Address         string `usage:"address to bind the web server to"`
	Port            int    `usage:"port to bind the web server to"`
	TLS             bool   `usage:"serve HTTPS, a self-signed certificate is generated when no certificate files exist"`
	CertificateFile string `usage:"path to the TLS certificate file"`
	KeyFile         string `usage:"path to the TLS key file"`
	Daemon          Daemon `usage:"the control daemon"`
}

// Default returns the default configuration.
func Default() *Config {
	return &Config{
		LogLevel: 1,
		Address:  "127.0.0.1",
		Port:     8443,
		TLS:      true,
		Daemon:   Daemon{URL: "http://127.0.0.1:8080"},
	}
}

// Validate implements the go-core config interface.
func (c *Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return apperror.NewError("port must be between 1 and 65535")
	}
	u, err := url.Parse(c.Daemon.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return apperror.NewErrorf("daemon.url %q must be an http or https URL", c.Daemon.URL)
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
