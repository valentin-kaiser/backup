package api

import (
	"net/http/httptest"
	"testing"
)

func TestClientIPTrustsForwardedHeaderOnlyFromTrustedProxies(t *testing.T) {
	proxySetting = func() string { return "10.0.0.0/8, 192.168.1.2" }
	t.Cleanup(func() { proxySetting = func() string { return "" } })

	for _, c := range []struct{ name, remote, xff, want string }{
		{"direct peer", "203.0.113.9:1234", "1.2.3.4", "203.0.113.9"},
		{"trusted proxy", "10.1.1.1:1234", "198.51.100.7", "198.51.100.7"},
		{"spoofed prefix", "10.1.1.1:1234", "1.2.3.4, 198.51.100.7", "198.51.100.7"},
		{"chain of proxies", "10.1.1.1:1234", "198.51.100.7, 10.2.2.2", "198.51.100.7"},
		{"no header", "192.168.1.2:1234", "", "192.168.1.2"},
		{"garbage header", "10.1.1.1:1234", "nonsense", "10.1.1.1"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := ClientIP(r); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}
