// Package secrets keeps the secrets of the daemon, the restic repository passwords, encrypted on disk. Jobs refer
// to a secret by name. Values never leave the store except to run restic, no API returns them.
package secrets

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/secure"
	"github.com/valentin-kaiser/go-core/apperror"
)

// FileName is the name of the secret file inside the data directory.
const FileName = "secrets.json"

// MaxLength bounds a value, restic passwords are far shorter.
const MaxLength = 1024

// ErrNotFound is returned for an unknown secret.
var ErrNotFound = apperror.NewError("secret not found")

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// ValidateName checks the name of a secret.
func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return apperror.NewError("the name must be 1-32 lower case letters, digits, - or _")
	}
	return nil
}

// ValidateValue checks a value. restic reads the password file as it is, line breaks would become part of it.
func ValidateValue(v string) error {
	switch {
	case v == "":
		return apperror.NewError("the secret must not be empty")
	case len(v) > MaxLength:
		return apperror.NewErrorf("the secret must not be longer than %d bytes", MaxLength)
	case strings.ContainsAny(v, "\r\n\x00"):
		return apperror.NewError("the secret must not contain line breaks or NUL")
	}
	return nil
}

// Meta describes a secret without its value.
type Meta struct {
	Name    string
	Created time.Time
	Updated time.Time
}

type item struct {
	Sealed  string    `json:"sealed"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// Store keeps the secrets in a JSON file, each value sealed with the master key. The name is part of the
// authenticated data, so a value cannot be moved to another name.
type Store struct {
	path string
	key  secure.Key

	mu    sync.Mutex
	items map[string]item
}

// Open loads the secret file in dir. A missing file is an empty store.
func Open(dir string, key secure.Key) (*Store, error) {
	s := &Store{path: filepath.Join(dir, FileName), key: key, items: map[string]item{}}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &s.items); err != nil {
			return nil, apperror.NewErrorf("the secret file %s is corrupt", s.path).AddError(err)
		}
	}
	// Fail at start, not when a backup needs the secret at night.
	for name, it := range s.items {
		if _, err := key.Open(it.Sealed, context(name)); err != nil {
			return nil, apperror.NewErrorf("the secret %q cannot be decrypted, is the master key the one it was saved with?", name).AddError(err)
		}
	}
	return s, nil
}

func context(name string) string { return "secret:" + name }

func (s *Store) save() error {
	data, err := json.MarshalIndent(s.items, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Set creates a secret or replaces its value. It reports whether the secret is new.
func (s *Store) Set(name, value string) (bool, error) {
	if err := ValidateName(name); err != nil {
		return false, err
	}
	if err := ValidateValue(value); err != nil {
		return false, err
	}
	sealed, err := s.key.Seal([]byte(value), context(name))
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	old, existed := s.items[name]
	it := item{Sealed: sealed, Created: now, Updated: now}
	if existed {
		it.Created = old.Created
	}
	s.items[name] = it
	if err := s.save(); err != nil {
		if existed {
			s.items[name] = old
		} else {
			delete(s.items, name)
		}
		return false, err
	}
	return !existed, nil
}

// Get returns the value of a secret.
func (s *Store) Get(name string) (string, error) {
	s.mu.Lock()
	it, ok := s.items[name]
	s.mu.Unlock()
	if !ok {
		return "", apperror.NewErrorf("%s: %s", ErrNotFound.Message, name).AddError(ErrNotFound)
	}
	plain, err := s.key.Open(it.Sealed, context(name))
	return string(plain), err
}

// Has reports whether a secret exists.
func (s *Store) Has(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.items[name]
	return ok
}

// Delete removes a secret.
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.items[name]
	if !ok {
		return apperror.NewErrorf("%s: %s", ErrNotFound.Message, name).AddError(ErrNotFound)
	}
	delete(s.items, name)
	if err := s.save(); err != nil {
		s.items[name] = old
		return err
	}
	return nil
}

// List returns the secrets sorted by name, without their values.
func (s *Store) List() []Meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Meta, 0, len(s.items))
	for name, it := range s.items {
		out = append(out, Meta{Name: name, Created: it.Created, Updated: it.Updated})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Rekey re-encrypts the secret file in dir from the old key to the new one. The daemon must not run, it holds
// the secrets in memory.
func Rekey(dir string, old, next secure.Key) error {
	s, err := Open(dir, old)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	moved := make(map[string]item, len(s.items))
	for name, it := range s.items {
		sealed, err := next.Rekey(old, it.Sealed, context(name))
		if err != nil {
			return apperror.NewErrorf("secret %s", name).AddError(err)
		}
		it.Sealed = sealed
		moved[name] = it
	}
	s.items, s.key = moved, next
	return s.save()
}
