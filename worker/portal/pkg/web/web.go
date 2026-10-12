// Package web serves the Angular frontend and forwards its API calls to the control daemon.
package web

import (
	"crypto/tls"
	"crypto/x509/pkix"
	"embed"
	"io/fs"
	"net/url"
	"os"

	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/config"
	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/logging/log"
	"github.com/valentin-kaiser/go-core/security"
	"github.com/valentin-kaiser/go-core/web"
)

//go:embed all:static
var assets embed.FS

// apiPath is the only part of the daemon API the browser needs: the websocket and its plain HTTP form.
const apiPath = "/api/v1/rpc"

var done chan error

// Start starts the web server in the background.
func Start() error {
	c := config.Get()
	frontend, err := fs.Sub(assets, "static")
	if err != nil {
		return apperror.Wrap(err)
	}
	daemon, err := url.Parse(c.Daemon.URL)
	if err != nil {
		return apperror.Wrap(err)
	}

	s := web.Instance().
		WithHost(c.Address).
		WithSecurityHeaders().
		WithGzip().
		WithLog().
		WithHandler(apiPath, Proxy(daemon, c.Daemon.SkipCertificateVerification)).
		WithFS([]string{"/"}, frontend)

	if c.TLS {
		cfg, err := tlsConfig(c)
		if err != nil {
			return err
		}
		s = s.WithTLS(cfg).WithPort(uint16(c.Port), web.ProtocolHTTPS)
	} else {
		s = s.WithPort(uint16(c.Port), web.ProtocolHTTP)
	}
	if s.Error != nil {
		return apperror.Wrap(s.Error)
	}

	done = make(chan error, 1)
	s.StartAsync(done)
	go func() {
		if err := <-done; err != nil {
			log.Error().Err(err).Msg("web server stopped")
		}
	}()
	log.Info().Msgf("portal listening on %s (tls=%t), control daemon at %s", c.Addr(), c.TLS, c.Daemon.URL)
	return nil
}

// Stop stops the web server.
func Stop() error {
	return apperror.Wrap(web.Instance().Stop())
}

func tlsConfig(c config.Config) (*tls.Config, error) {
	if c.CertificateFile != "" && c.KeyFile != "" {
		if _, err := os.Stat(c.CertificateFile); err == nil {
			cfg, err := security.LoadCertAndConfig(c.CertificateFile, c.KeyFile, "", tls.NoClientCert)
			if err != nil {
				return nil, apperror.Wrap(err)
			}
			return cfg, nil
		}
	}
	log.Warn().Msg("no TLS certificate configured, using a self-signed certificate")
	cert, pool, err := security.GenerateSelfSignedCertificate(pkix.Name{CommonName: c.Address})
	if err != nil {
		return nil, apperror.Wrap(err)
	}
	return security.NewTLSConfig(cert, pool, tls.NoClientCert), nil
}
