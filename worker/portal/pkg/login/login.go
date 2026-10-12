// Package login protects the portal with the one password of the deployment, the same one the daemon expects. There are
// no users and no roles: whoever knows the password can do everything the portal can. Sessions are kept as hashes of
// random tokens, the password itself is never stored here.
package login

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/auth"
	"github.com/Valentin-Kaiser/backup/pkg/secure"
	_ "modernc.org/sqlite" // sqlite driver without cgo

	"github.com/valentin-kaiser/go-core/apperror"
)

// Limits of the sessions.
const (
	SessionLifetime = 12 * time.Hour
	SessionIdle     = 2 * time.Hour
	touchInterval   = time.Minute
)

var (
	// ErrInvalidLogin is returned for a wrong password.
	ErrInvalidLogin = apperror.NewError("wrong password")
	// ErrLocked is returned while an address is locked after too many wrong passwords.
	ErrLocked = apperror.NewError("too many wrong passwords, try again later")
	// ErrNoPassword is returned while the portal has no usable password file.
	ErrNoPassword = apperror.NewError("the portal has no password, point password-file at a file with at least 12 characters")
)

// Store checks the password and keeps the sessions in SQLite.
type Store struct {
	db       *sql.DB
	now      func() time.Time
	password *auth.Password
	limiter  *auth.Limiter
}

// Open opens the database in dir, creates its tables and checks passwords against the given one.
func Open(dir string, password *auth.Password) (*Store, error) {
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "portal.db")) +
		"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, now: time.Now, password: password, limiter: auth.NewLimiter()}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS portal_sessions (
	hash TEXT PRIMARY KEY,
	binding TEXT NOT NULL,
	created INTEGER NOT NULL,
	expires INTEGER NOT NULL,
	last_seen INTEGER NOT NULL,
	ip TEXT NOT NULL DEFAULT ''
)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// DB returns the database, so other stores of the portal share the file.
func (s *Store) DB() *sql.DB { return s.db }

// Verify checks the password. An address is locked for auth.LockDuration after auth.MaxFailures wrong passwords in a
// row.
func (s *Store) Verify(password, ip string) error {
	if s.limiter.Locked(ip) {
		return ErrLocked
	}
	ok, err := s.password.Check(password)
	if err != nil {
		return ErrNoPassword
	}
	if !ok {
		s.limiter.Fail(ip)
		return ErrInvalidLogin
	}
	s.limiter.Clear(ip)
	return nil
}

// Login checks the password and opens a session. It returns the session token, which is only kept as a hash.
func (s *Store) Login(password, ip string) (string, error) {
	if err := s.Verify(password, ip); err != nil {
		return "", err
	}
	token, tokenHash, err := secure.NewToken()
	if err != nil {
		return "", err
	}
	binding, err := s.bind(tokenHash)
	if err != nil {
		return "", err
	}
	now := s.now()
	if _, err := s.db.Exec(`INSERT INTO portal_sessions(hash, binding, created, expires, last_seen, ip) VALUES(?,?,?,?,?,?)`,
		tokenHash, binding, now.Unix(), now.Add(SessionLifetime).Unix(), now.Unix(), ip); err != nil {
		return "", err
	}
	s.purge()
	return token, nil
}

// Valid reports whether a token belongs to a session that has neither expired nor been idle for too long, and that
// was opened with the password that is current: replacing the password ends every session.
func (s *Store) Valid(token string) bool {
	if token == "" {
		return false
	}
	hash := secure.HashToken(token)
	var binding string
	var expires, lastSeen int64
	if err := s.db.QueryRow(`SELECT binding, expires, last_seen FROM portal_sessions WHERE hash = ?`, hash).Scan(&binding, &expires, &lastSeen); err != nil {
		return false
	}
	if want, err := s.bind(hash); err != nil || !hmac.Equal([]byte(binding), []byte(want)) {
		// The password was replaced since the sign-in.
		_, _ = s.db.Exec(`DELETE FROM portal_sessions WHERE hash = ?`, hash)
		return false
	}
	now := s.now()
	if expires <= now.Unix() || lastSeen+int64(SessionIdle.Seconds()) <= now.Unix() {
		_, _ = s.db.Exec(`DELETE FROM portal_sessions WHERE hash = ?`, hash)
		return false
	}
	if now.Unix()-lastSeen >= int64(touchInterval.Seconds()) {
		_, _ = s.db.Exec(`UPDATE portal_sessions SET last_seen = ? WHERE hash = ?`, now.Unix(), hash)
	}
	return true
}

// bind ties a session to the password it was opened with: a keyed hash of the session, so replacing the password ends
// every session without keeping the password anywhere.
func (s *Store) bind(sessionHash string) (string, error) {
	password, err := s.password.Value()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(password))
	mac.Write([]byte(sessionHash))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// Logout ends a session.
func (s *Store) Logout(token string) {
	_, _ = s.db.Exec(`DELETE FROM portal_sessions WHERE hash = ?`, secure.HashToken(token))
}

// EndAll ends every session.
func (s *Store) EndAll() {
	_, _ = s.db.Exec(`DELETE FROM portal_sessions`)
}

// purge deletes sessions that can no longer be used.
func (s *Store) purge() {
	now := s.now().Unix()
	_, _ = s.db.Exec(`DELETE FROM portal_sessions WHERE expires <= ? OR last_seen <= ?`, now, now-int64(SessionIdle.Seconds()))
}
