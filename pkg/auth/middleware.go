package auth

import (
	"net"
	"net/http"
	"strings"

	"github.com/Valentin-Kaiser/backup/pkg/notify"
	"github.com/valentin-kaiser/go-core/logging/log"
)

// BearerPassword extracts the password of an "Authorization: Bearer" header.
func BearerPassword(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Middleware requires the password as bearer credential. An address is locked after too many wrong ones. Health probes
// are not wrapped, see web.Start.
func Middleware(next http.Handler, password *Password, limiter *Limiter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := remoteIP(r)
		if limiter.Locked(ip) {
			http.Error(w, "too many wrong passwords, try again later", http.StatusTooManyRequests)
			return
		}
		ok, err := password.Check(BearerPassword(r))
		if err != nil {
			http.Error(w, "the daemon has no usable password", http.StatusServiceUnavailable)
			return
		}
		if !ok {
			limiter.Fail(ip)
			log.Warn().Field("ip", ip).Msg("failed API authentication")
			if limiter.Locked(ip) {
				notify.Send(notify.Event{
					Kind: notify.KindSecurity, Severity: notify.SeverityFailure,
					Title:  "API address locked after wrong passwords",
					Key:    "api|" + ip,
					Fields: []notify.Field{{Label: "Address", Value: ip}, {Label: "Component", Value: "control API"}},
					Detail: "The address sent too many wrong passwords to the control API and is locked for a while.",
				})
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="control"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		limiter.Clear(ip)
		next.ServeHTTP(w, r)
	})
}
