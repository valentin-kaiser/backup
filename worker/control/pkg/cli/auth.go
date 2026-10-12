package cli

import (
	"net/http"

	"github.com/Valentin-Kaiser/backup/pkg/auth"
)

// dataDir is the directory of the daemon state, it holds the master key of the secrets.
func dataDir() string { return envOr("CONTROL_PATH", "/var/lib/control") }

// clientPassword returns the password the client presents: CONTROL_PASSWORD or the file in CONTROL_PASSWORD_FILE.
func clientPassword() string {
	if p := envOr("CONTROL_PASSWORD", ""); p != "" {
		return p
	}
	if file := envOr("CONTROL_PASSWORD_FILE", ""); file != "" {
		if p, err := auth.Read(file); err == nil {
			return p
		}
	}
	return ""
}

// authHeader returns the header sent with every call, empty without a password.
func authHeader() http.Header {
	h := http.Header{}
	if p := clientPassword(); p != "" {
		h.Set("Authorization", "Bearer "+p)
	}
	return h
}
