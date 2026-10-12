// Package web serves the management API of the daemon. The web UI is a separate process, see worker/ui.
package web

import (
	"crypto/tls"
	"crypto/x509/pkix"
	"os"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/config"
	api "github.com/Valentin-Kaiser/backup/worker/control/pkg/web/api/v1"
	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/logging/log"
	"github.com/valentin-kaiser/go-core/security"
	"github.com/valentin-kaiser/go-core/web"
)

var done chan error

// Start starts the API server in the background.
func Start(h *api.Service) error {
	c := config.Get()
	s := web.Instance().
		WithHost(c.API.Address).
		WithSecurityHeaders().
		WithLog().
		WithRateLimit(api.Prefix, 300, time.Minute).
		WithHandlerFunc(api.Prefix+"health", h.Health).
		WithHandler(api.Endpoint, h.Handler())

	if c.API.TLS {
		cfg, err := tlsConfig(c)
		if err != nil {
			return err
		}
		s = s.WithTLS(cfg).WithPort(uint16(c.API.Port), web.ProtocolHTTPS)
	} else {
		s = s.WithPort(uint16(c.API.Port), web.ProtocolHTTP)
	}
	if s.Error != nil {
		return apperror.Wrap(s.Error)
	}

	done = make(chan error, 1)
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

func tlsConfig(c config.Config) (*tls.Config, error) {
	if c.API.CertificateFile != "" && c.API.KeyFile != "" {
		if _, err := os.Stat(c.API.CertificateFile); err == nil {
			cfg, err := security.LoadCertAndConfig(c.API.CertificateFile, c.API.KeyFile, "", tls.NoClientCert)
			if err != nil {
				return nil, apperror.Wrap(err)
			}
			return cfg, nil
		}
	}
	log.Warn().Msg("no TLS certificate configured, using a self-signed certificate")
	cert, pool, err := security.GenerateSelfSignedCertificate(pkix.Name{CommonName: c.API.Address})
	if err != nil {
		return nil, apperror.Wrap(err)
	}
	return security.NewTLSConfig(cert, pool, tls.NoClientCert), nil
}
