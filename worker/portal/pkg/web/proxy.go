package web

import (
	"crypto/tls"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/valentin-kaiser/go-core/logging/log"
)

// Proxy forwards the API calls and the websocket of the browser to the control daemon. The daemon does the
// authentication with the token the browser sends, the UI process never sees a secret of its own.
//
// Browsers send an Origin on websockets and the daemon only accepts its own, so the proxy enforces the
// same-origin rule for the browser side and presents the daemon its own origin.
func Proxy(target *url.URL, skipVerify bool) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = target.Host
			if r.In.Header.Get("Origin") != "" {
				r.Out.Header.Set("Origin", target.Scheme+"://"+target.Host)
			}
		},
		Transport: &http.Transport{
			Proxy:           http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{InsecureSkipVerify: skipVerify}, //nolint:gosec // opt-in for self-signed daemons
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			log.Warn().Err(err).Msg("the control daemon is not reachable")
			http.Error(w, "the control daemon is not reachable", http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host {
				http.Error(w, "cross-origin requests are not allowed", http.StatusForbidden)
				return
			}
		}
		proxy.ServeHTTP(w, r)
	})
}
