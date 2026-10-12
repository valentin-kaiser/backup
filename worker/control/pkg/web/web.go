// Package web serves the management API of the daemon. The web UI is a separate process, see worker/portal.
package web

import (
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/auth"
	"github.com/Valentin-Kaiser/backup/pkg/config"
	"github.com/Valentin-Kaiser/backup/pkg/secure"
	api "github.com/Valentin-Kaiser/backup/worker/control/pkg/web/api/v1"
	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/flag"
	"github.com/valentin-kaiser/go-core/logging/log"
	"github.com/valentin-kaiser/go-core/web"
)

// Start starts the API server in the background.
func Start(h *api.Service, password *auth.Password) error {
	c := config.Get()
	rpc := h.Handler()
	if c.API.DisableAuth {
		log.Warn().Msg("API authentication is disabled, anyone who can reach the API has full control")
	} else {
		rpc = auth.Middleware(rpc, password, auth.NewLimiter())
	}
	s := web.Instance().
		WithHost(c.API.Address).
		WithSecurityHeaders().
		WithLog().
		WithRateLimit(api.Prefix, 300, time.Minute).
		WithHandlerFunc(api.Prefix+"health", h.Health).
		WithHandler(api.Endpoint, rpc)

	s, err := listen(s, c)
	if err != nil {
		return err
	}
	if s.Error != nil {
		return apperror.Wrap(s.Error)
	}

	done := make(chan error, 1)
	s.StartAsync(done)
	go func() {
		if err := <-done; err != nil {
			log.Error().Err(err).Msg("api server stopped")
		}
	}()
	log.Info().Msgf("api listening on %s (tls=%t)", c.Addr(), c.API.TLS)
	return nil
}

// Stop stops the API server.
func Stop() error {
	return apperror.Wrap(web.Instance().Stop())
}

// StartHealth serves only the health probe, on loopback and without TLS. Standalone mode uses it for the Docker
// healthcheck (control health), it has no API.
func StartHealth(h *api.Service) error {
	c := config.Get()
	s := web.Instance().
		WithHost("127.0.0.1").
		WithHandlerFunc(api.Prefix+"health", h.Health).
		WithPort(uint16(c.API.Port), web.ProtocolHTTP)
	if s.Error != nil {
		return apperror.Wrap(s.Error)
	}
	done := make(chan error, 1)
	s.StartAsync(done)
	go func() {
		if err := <-done; err != nil {
			log.Error().Err(err).Msg("health server stopped")
		}
	}()
	log.Info().Msgf("health probe listening on 127.0.0.1:%d", c.API.Port)
	return nil
}

// listen applies the address, port and TLS settings of the API to the server.
func listen(s *web.Server, c config.Config) (*web.Server, error) {
	s = s.WithHost(c.API.Address)
	if !c.API.TLS {
		return s.WithPort(uint16(c.API.Port), web.ProtocolHTTP), nil
	}
	cfg, err := secure.ServerTLS{
		Dir:               flag.Path,
		CertificateFile:   c.API.CertificateFile,
		KeyFile:           c.API.KeyFile,
		CommonName:        c.API.Address,
		ClientCAFile:      c.API.ClientCAFile,
		RequireClientCert: c.API.RequireClientCert,
	}.Config()
	if err != nil {
		return nil, err
	}
	return s.WithTLS(cfg).WithPort(uint16(c.API.Port), web.ProtocolHTTPS), nil
}

// Restart applies a changed address, port or TLS setting of the API without restarting the process. Disabling or
// enabling the authentication needs a restart.
func Restart() {
	s, err := listen(web.Instance(), config.Get())
	if err != nil {
		log.Error().Err(err).Msg("failed to reload the TLS configuration, keeping the running server")
		return
	}
	done := make(chan error, 1)
	s.RestartAsync(done)
	if err := <-done; err != nil {
		log.Error().Err(err).Msg("api server failed to restart")
	}
}
