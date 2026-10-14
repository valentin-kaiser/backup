// Package login protects the portal with the one password of the deployment, the same one the daemon expects. There are
// no users and no roles: whoever knows the password can do everything the portal can. Sessions are kept as hashes of
// random tokens, the password itself is never stored here.
package login

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/auth"
	"github.com/Valentin-Kaiser/backup/pkg/secure"
	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/database"
	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/database/schema"

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
	q        *schema.Queries
	now      func() time.Time
	password *auth.Password
	limiter  *auth.Limiter
}

// Open opens the database in dir, migrates its tables and checks passwords against the given one.
func Open(dir string, password *auth.Password) (*Store, error) {
	db, err := database.Open(dir)
	if err != nil {
		return nil, err
	}
	return &Store{db: db, q: schema.New(db), now: time.Now, password: password, limiter: auth.NewLimiter()}, nil
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
	if err := s.q.CreateSession(context.Background(), schema.CreateSessionParams{
		Hash:     tokenHash,
		Binding:  binding,
		Created:  now.Unix(),
		Expires:  now.Add(SessionLifetime).Unix(),
		LastSeen: now.Unix(),
		Ip:       ip,
	}); err != nil {
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
	ctx := context.Background()
	session, err := s.q.GetSession(ctx, hash)
	if err != nil {
		return false
	}
	if want, err := s.bind(hash); err != nil || !hmac.Equal([]byte(session.Binding), []byte(want)) {
		// The password was replaced since the sign-in.
		_ = s.q.DeleteSession(ctx, hash)
		return false
	}
	now := s.now()
	if session.Expires <= now.Unix() || session.LastSeen+int64(SessionIdle.Seconds()) <= now.Unix() {
		_ = s.q.DeleteSession(ctx, hash)
		return false
	}
	if now.Unix()-session.LastSeen >= int64(touchInterval.Seconds()) {
		_ = s.q.TouchSession(ctx, schema.TouchSessionParams{LastSeen: now.Unix(), Hash: hash})
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
	_ = s.q.DeleteSession(context.Background(), secure.HashToken(token))
}

// EndAll ends every session.
func (s *Store) EndAll() {
	_ = s.q.DeleteAllSessions(context.Background())
}

// purge deletes sessions that can no longer be used.
func (s *Store) purge() {
	now := s.now().Unix()
	_ = s.q.PurgeSessions(context.Background(), schema.PurgeSessionsParams{
		Now:        now,
		IdleBefore: now - int64(SessionIdle.Seconds()),
	})
}
