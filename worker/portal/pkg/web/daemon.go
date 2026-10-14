package web

import (
	"context"
	"net/http"
	"net/url"
	"sync"

	"github.com/Valentin-Kaiser/backup/pkg/auth"
	"github.com/Valentin-Kaiser/backup/pkg/secure"
	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/audit"
	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/config"
	"github.com/valentin-kaiser/go-core/logging/log"
)

// daemonRoute is the path of the control API of the daemon as the browser sees it.
const daemonRoute = "/api/v1/rpc"

// Daemon serves the API of the one control daemon the portal manages. The proxy is built when it is first used, from the
// settings of the configuration, and dropped when they change or the password changes, which also ends its open
// websockets. The next call builds it again, so a changed configuration needs no restart.
type Daemon struct {
	password *auth.Password
	access   Access
	audit    *audit.Log
	settings func() config.Daemon

	mu      sync.Mutex
	handler http.Handler
	cancel  context.CancelFunc
	// last and version are the settings and the version of the password the handler was built with.
	last    config.Daemon
	version uint64
}

// NewDaemon creates the proxy for the daemon the settings describe. audit may be nil.
func NewDaemon(settings func() config.Daemon, password *auth.Password, access Access, a *audit.Log) *Daemon {
	return &Daemon{settings: settings, password: password, access: access, audit: a}
}

func (d *Daemon) reset() {
	if d.cancel != nil {
		d.cancel()
	}
	d.handler, d.cancel = nil, nil
}

// resolve returns the target and TLS settings of the daemon.
func resolve(d config.Daemon) (*url.URL, secure.ClientTLS, error) {
	target, err := url.Parse(d.URL)
	if err != nil {
		return nil, secure.ClientTLS{}, err
	}
	return target, secure.ClientTLS{
		CAFile: d.CAFile, Fingerprint: d.Fingerprint, SkipVerify: d.SkipCertificateVerification,
		CertificateFile: d.ClientCertificateFile, KeyFile: d.ClientKeyFile,
	}, nil
}

func (d *Daemon) build() (http.Handler, int, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.settings()
	if d.handler != nil && (c != d.last || d.password.Version() != d.version) {
		d.reset()
	}
	if d.handler != nil {
		return d.handler, 0, ""
	}
	target, tls, err := resolve(c)
	if err != nil {
		log.Error().Err(err).Msg("the settings of the daemon are unusable")
		return nil, http.StatusBadGateway, "the settings of the daemon are unusable, check the portal log"
	}
	cfg, err := tls.Config()
	if err != nil {
		log.Error().Err(err).Msg("the TLS settings of the daemon are unusable")
		return nil, http.StatusBadGateway, "the settings of the daemon are unusable, check the portal log"
	}
	token, err := d.password.Value()
	if err != nil {
		log.Error().Err(err).Msg("the portal has no usable password to present to the daemon")
		return nil, http.StatusBadGateway, "the portal has no usable password, check the portal log"
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.handler, d.cancel = newProxy(ctx, target, cfg, token, d.access, &Auditor{Log: d.audit}), cancel
	d.last, d.version = c, d.password.Version()
	return d.handler, 0, ""
}

// ServeHTTP handles the control API calls and websockets of the browser.
func (d *Daemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The proxy checks the session itself, but the settings must not be touched for strangers.
	if !d.access(r) {
		http.Error(w, "sign in required", http.StatusUnauthorized)
		return
	}
	h, status, msg := d.build()
	if h == nil {
		http.Error(w, msg, status)
		return
	}
	h.ServeHTTP(w, r)
}
