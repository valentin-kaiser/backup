// Package auth holds the one password of a deployment. The web portal and the control daemon read it from the same
// file (a Docker secret): whoever signs in to the portal with it is also who the portal speaks for to the daemon, and
// the CLI authenticates with it too. There are no users, roles or tokens.
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/security"
)

// Limits of the password.
const (
	MinLength = 12
	MaxLength = 256
)

// ErrNoPassword is returned while there is no usable password file.
var ErrNoPassword = apperror.NewError("no password is set, point password-file at a file with a password of at least 12 characters")

// Validate enforces the password policy.
func Validate(password string) error {
	n := len([]rune(password))
	switch {
	case n < MinLength:
		return apperror.NewErrorf("the password must have at least %d characters", MinLength)
	case len(password) > MaxLength:
		return apperror.NewErrorf("the password must not be longer than %d bytes", MaxLength)
	}
	return nil
}

// Read reads a password from a file. Surrounding whitespace, such as the final line break, is not part of it.
func Read(path string) (string, error) {
	if path == "" {
		return "", ErrNoPassword
	}
	data, err := os.ReadFile(path) //nolint:gosec // the path is configuration
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNoPassword
	}
	if err != nil {
		return "", apperror.NewError("failed to read the password file").AddError(err)
	}
	password := strings.TrimSpace(string(data))
	if err := Validate(password); err != nil {
		return "", err
	}
	return password, nil
}

// Password is the password of a file. It reads the file again when it changes, so replacing the secret applies to a
// running process.
type Password struct {
	path func() string

	mu      sync.Mutex
	file    string
	mtime   time.Time
	size    int64
	value   string
	digest  [sha256.Size]byte
	err     error
	version uint64
}

// New creates the password of the file the function names, which may change with the configuration.
func New(path func() string) *Password { return &Password{path: path} }

// load reads the file when it is not the one that was read last. The caller holds the lock.
func (p *Password) load() {
	file := p.path()
	info, statErr := os.Stat(file)
	if statErr == nil && file == p.file && info.ModTime().Equal(p.mtime) && info.Size() == p.size && p.err == nil {
		return
	}
	value, err := Read(file)
	if err == nil && value == p.value && p.err == nil {
		p.file, p.mtime, p.size = file, info.ModTime(), info.Size()
		return
	}
	p.file, p.value, p.err = file, value, err
	p.digest = sha256.Sum256([]byte(value))
	if statErr == nil {
		p.mtime, p.size = info.ModTime(), info.Size()
	}
	p.version++
}

// Value returns the password, for the portal to present it to the daemon.
func (p *Password) Value() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.load()
	return p.value, p.err
}

// Version changes whenever the password or the file it comes from changed.
func (p *Password) Version() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.load()
	return p.version
}

// Check compares a presented password with the password in constant time. It fails with ErrNoPassword or the reason
// the file is unusable while there is no password.
func (p *Password) Check(candidate string) (bool, error) {
	p.mu.Lock()
	p.load()
	digest, err := p.digest, p.err
	p.mu.Unlock()
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(sum[:], digest[:]) == 1, nil
}

// Ensure creates the password file with a random password when it is missing or empty, so a new installation can
// start without a password written by hand. An existing password is never touched, also not when it is too short.
// The daemon and the portal both call it for the same file, whoever comes first creates it and the other one reads it.
// It reports whether the file was created.
func Ensure(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // the path is configuration
	switch {
	case err == nil && strings.TrimSpace(string(data)) != "":
		return false, nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return false, apperror.NewError("failed to read the password file").AddError(err)
	}
	raw, err := security.GetRandomBytes(12)
	if err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".password-*")
	if err != nil {
		return false, apperror.NewError("failed to create the password file").AddError(err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, werr := tmp.WriteString(hex.EncodeToString(raw) + "\n")
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), 0o600)
	}
	if werr != nil {
		return false, apperror.NewError("failed to write the password file").AddError(werr)
	}
	if err == nil {
		// An empty file is replaced.
		return true, os.Rename(tmp.Name(), path)
	}
	// Linking fails when the other process was faster, the file is complete when it appears.
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, apperror.NewError("failed to create the password file").AddError(err)
	}
	return true, nil
}
