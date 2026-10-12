// Package api exposes the services of the portal itself as xrpc services: the sign-in and the audit log. The calls to a control daemon do not belong here, the portal forwards them, see the web package.
//
// Every service except the sign-in needs a session, which is an HttpOnly cookie. Calls come in over HTTP POST, the
// cookie and the origin check only make sense there.
package api

import (
	"context"
	"net"
	"net/http"
	"net/url"

	"github.com/valentin-kaiser/go-core/web/xrpc"
)

// Endpoint is the path of the xrpc services of the portal, it serves JSON-RPC over HTTP POST.
const Endpoint = "/api/v1/portal"

const maxBody = 64 << 10

// FieldNamesHeader selects field names instead of field numbers in the JSON of a call, handy for scripts and tests.
const FieldNamesHeader = "X-Field-Names"

// Handler returns the xrpc services of the portal. State changing calls must come from the origin of the page: the
// cookie is the only credential, SameSite=Strict already keeps it from cross-site requests and this is the second
// layer.
func Handler(auth *Auth, log *AuditLog) http.Handler {
	rpc := xrpc.Register(auth, log).WithMaxBodySize(maxBody).WithFieldNamesHeader(FieldNamesHeader)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodPost && !sameOrigin(r) {
			http.Error(w, "cross-origin requests are not allowed", http.StatusForbidden)
			return
		}
		rpc.ServeHTTP(w, r)
	})
}

// sameOrigin only allows requests from the page's own origin. A request without an Origin header is refused, a
// browser sends one with every POST.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}

// ClientIP is the address of the peer. Forwarded headers are not trusted, they are spoofable.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// requestIP is the address of the caller of an rpc.
func requestIP(ctx context.Context) string {
	if r, ok := xrpc.GetRequest(ctx); ok {
		return ClientIP(r)
	}
	return ""
}

// fail answers with the given HTTP status, so the portal can tell a missing session (401) from a refused action.
func fail(ctx context.Context, status int, msg string) error {
	xrpc.SetStatus(ctx, status)
	code := xrpc.CodeServerError
	if status == http.StatusBadRequest {
		code = xrpc.CodeInvalidParams
	}
	return xrpc.NewError(code, msg)
}
