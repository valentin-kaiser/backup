package auth

import (
	"net"
	"net/http"
	"strings"
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
			w.Header().Set("WWW-Authenticate", `Bearer realm="control"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		limiter.Clear(ip)
		next.ServeHTTP(w, r)
	})
}
