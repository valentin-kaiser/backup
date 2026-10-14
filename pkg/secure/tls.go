package secure

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/logging/log"
	"github.com/valentin-kaiser/go-core/security"
)

// ServerTLS describes the TLS settings of a listener.
type ServerTLS struct {
	// Dir is the directory relative certificate paths are relative to, usually the data directory.
	Dir             string
	CertificateFile string
	KeyFile         string
	// CommonName is used for the self-signed fallback certificate.
	CommonName string
	// ClientCAFile enables mutual TLS: clients must present a certificate signed by one of these CAs.
	ClientCAFile string
	// RequireClientCert rejects clients without a certificate. Without it, a certificate is verified when sent.
	RequireClientCert bool
}

// Config builds the tls.Config of a listener. When the certificate files do not exist yet a self-signed certificate is
// generated and written to them, so the certificate stays the same across restarts and can be trusted or pinned. A
// relative path is relative to Dir. Without any configured file a certificate is generated in memory only.
func (s ServerTLS) Config() (*tls.Config, error) {
	auth := tls.NoClientCert
	var pool *x509.CertPool
	if s.ClientCAFile != "" {
		pem, err := os.ReadFile(s.ClientCAFile)
		if err != nil {
			return nil, apperror.Wrap(err)
		}
		pool = x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, apperror.NewErrorf("no certificate found in %s", s.ClientCAFile)
		}
		auth = tls.VerifyClientCertIfGiven
		if s.RequireClientCert {
			auth = tls.RequireAndVerifyClientCert
		}
	} else if s.RequireClientCert {
		return nil, apperror.NewError("client certificates are required but no client CA is configured")
	}

	var cfg *tls.Config
	if s.CertificateFile != "" && s.KeyFile != "" {
		certFile, keyFile := Resolve(s.Dir, s.CertificateFile), Resolve(s.Dir, s.KeyFile)
		if _, err := os.Stat(certFile); errors.Is(err, os.ErrNotExist) {
			log.Warn().Msgf("no TLS certificate at %s, generating a self-signed certificate", certFile)
			cert, _, err := security.GenerateSelfSignedCertificate(pkix.Name{CommonName: s.CommonName})
			if err != nil {
				return nil, apperror.Wrap(err)
			}
			if err := security.WriteCertificate(cert, certFile, keyFile); err != nil {
				return nil, apperror.NewError("failed to write the self-signed certificate").AddError(err)
			}
		}
		var err error
		if cfg, err = security.LoadCertAndConfig(certFile, keyFile, "", auth); err != nil {
			return nil, apperror.NewError("failed to load the TLS certificate and key").AddError(err)
		}
	}
	if cfg == nil {
		log.Warn().Msg("no TLS certificate configured, using a self-signed certificate that is lost on restart")
		cert, certPool, err := security.GenerateSelfSignedCertificate(pkix.Name{CommonName: s.CommonName})
		if err != nil {
			return nil, apperror.Wrap(err)
		}
		cfg = security.NewTLSConfig(cert, certPool, auth)
	}
	cfg.ClientAuth = auth
	if pool != nil {
		cfg.ClientCAs = pool
	}
	cfg.MinVersion = tls.VersionTLS12
	return cfg, nil
}

// ClientTLS describes how to verify a server and how to identify against it.
type ClientTLS struct {
	// CAFile trusts this CA instead of the system roots.
	CAFile string
	// Fingerprint pins the SHA-256 fingerprint of the server's leaf certificate (hex, colons allowed). A pinned
	// certificate is accepted even if self-signed, any other certificate is refused.
	Fingerprint string
	// CertificateFile and KeyFile are the client certificate for mutual TLS.
	CertificateFile string
	KeyFile         string
	// SkipVerify disables all verification. Only for development.
	SkipVerify bool
}

// Config builds the tls.Config for an outgoing connection.
func (c ClientTLS) Config() (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, apperror.NewError("no certificate found in " + c.CAFile)
		}
		cfg.RootCAs = pool
	}
	if (c.CertificateFile == "") != (c.KeyFile == "") {
		return nil, apperror.NewError("the client certificate and its key must be set together")
	}
	if c.CertificateFile != "" {
		cert, err := tls.LoadX509KeyPair(c.CertificateFile, c.KeyFile)
		if err != nil {
			return nil, err
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	switch {
	case c.Fingerprint != "":
		want, err := ParseFingerprint(c.Fingerprint)
		if err != nil {
			return nil, err
		}
		// The pin replaces chain verification, so self-signed certificates work and nothing else does.
		cfg.InsecureSkipVerify = true //nolint:gosec // verified by VerifyConnection below
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return apperror.NewError("the server presented no certificate")
			}
			got := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(got[:], want) != 1 {
				return apperror.NewError("the server certificate does not match the pinned fingerprint")
			}
			return nil
		}
	case c.SkipVerify:
		cfg.InsecureSkipVerify = true //nolint:gosec // explicit opt-in
	}
	return cfg, nil
}

// ParseFingerprint decodes a SHA-256 fingerprint written as hex, with or without colons.
func ParseFingerprint(s string) ([]byte, error) {
	b, err := hex.DecodeString(strings.ReplaceAll(strings.TrimSpace(s), ":", ""))
	if err != nil || len(b) != sha256.Size {
		return nil, apperror.NewError("the fingerprint must be a SHA-256 hash in hex")
	}
	return b, nil
}

// Fingerprint returns the SHA-256 fingerprint of a certificate as colon separated upper case hex.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	parts := make([]string, 0, len(h)/2)
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}

// Resolve returns path as it is when it is absolute and otherwise relative to dir.
func Resolve(dir, path string) string {
	if path == "" || filepath.IsAbs(path) || dir == "" {
		return path
	}
	return filepath.Join(dir, path)
}
