// Package web serves the Angular frontend and forwards its API calls to the control daemon.
package web

import (
	"embed"
	"io/fs"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/auth"
	"github.com/Valentin-Kaiser/backup/pkg/secure"
	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/audit"
	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/config"
	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/login"
	api "github.com/Valentin-Kaiser/backup/worker/portal/pkg/web/api/v1"
	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/flag"
	"github.com/valentin-kaiser/go-core/logging/log"
	"github.com/valentin-kaiser/go-core/web"
)

//go:embed all:static
var assets embed.FS

// Start starts the web server in the background.
func Start(store *login.Store, password *auth.Password, aud *audit.Log) error {
	c := config.Get()
	frontend, err := fs.Sub(assets, "static")
	if err != nil {
		return apperror.Wrap(err)
	}
	authn := api.NewAuth(store, c.TLS, aud)
	rpc := api.Handler(authn, api.NewAuditLog(authn, aud))
	daemon := NewDaemon(func() config.Daemon { return config.Get().Daemon }, password, authn.Access, aud)

	s := web.Instance().
		WithHost(c.Address).
		WithSecurityHeaders().
		WithGzip().
		WithLog().
		WithRateLimit(api.Endpoint, 300, time.Minute).
		WithHandler(api.Endpoint, rpc).
		WithHandler(daemonRoute, daemon).
		WithFS([]string{"/"}, frontend)

	s, err = listen(s, c)
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
			log.Error().Err(err).Msg("web server stopped")
		}
	}()
	log.Info().Msgf("portal listening on %s (tls=%t)", c.Addr(), c.TLS)
	return nil
}

// Stop stops the web server.
func Stop() error {
	return apperror.Wrap(web.Instance().Stop())
}

// listen applies the address, port and TLS settings of the configuration to the server.
func listen(s *web.Server, c config.Config) (*web.Server, error) {
	s = s.WithHost(c.Address)
	if !c.TLS {
		return s.WithPort(uint16(c.Port), web.ProtocolHTTP), nil
	}
	cfg, err := secure.ServerTLS{
		Dir:             flag.Path,
		CertificateFile: c.CertificateFile,
		KeyFile:         c.KeyFile,
		CommonName:      c.Address,
		ClientCAFile:    c.ClientCAFile,
		// A client CA means mutual TLS: a browser without a certificate is turned away.
		RequireClientCert: c.ClientCAFile != "",
	}.Config()
	if err != nil {
		return nil, err
	}
	return s.WithTLS(cfg).WithPort(uint16(c.Port), web.ProtocolHTTPS), nil
}

// Restart applies a changed address, port or TLS setting without restarting the process.
func Restart() {
	s, err := listen(web.Instance(), config.Get())
	if err != nil {
		log.Error().Err(err).Msg("failed to reload the TLS configuration, keeping the running server")
		return
	}
	done := make(chan error, 1)
	s.RestartAsync(done)
	if err := <-done; err != nil {
		log.Error().Err(err).Msg("web server failed to restart")
	}
}
