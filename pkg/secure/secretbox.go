// Package secure holds the primitives used to protect credentials: encryption of secrets at rest, password and
// token hashing and the TLS settings shared by the daemon and the portal.
package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"strings"

	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/security"
)

const keySize = 32

// KeyID derives the id of a key from its content, so a rotated key is told apart from the old one.
func KeyID(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:4])
}

// Key is an AES-256 master key. ID is carried in every sealed value so a rotated key can be told apart.
type Key struct {
	ID  string
	key []byte
}

// NewKey creates a key from raw bytes, which must be 32 bytes long. The id is derived from the key when empty.
func NewKey(id string, raw []byte) (Key, error) {
	if id == "" {
		id = KeyID(raw)
	}
	if len(raw) != keySize {
		return Key{}, apperror.NewErrorf("the master key must be %d bytes, got %d", keySize, len(raw))
	}
	if id == "" || strings.Contains(id, ":") {
		return Key{}, apperror.NewError("the key id must not be empty or contain ':'")
	}
	return Key{ID: id, key: append([]byte(nil), raw...)}, nil
}

// LoadOrCreateKey reads the base64 encoded master key from path. A missing file is created with a fresh random
// key and 0600 permissions, so the first start works without any setup.
func LoadOrCreateKey(path string) (Key, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the path is configuration
	if errors.Is(err, os.ErrNotExist) {
		raw, err := security.GetRandomBytes(keySize)
		if err != nil {
			return Key{}, err
		}
		enc := base64.StdEncoding.EncodeToString(raw)
		if err := os.WriteFile(path, []byte(enc+"\n"), 0o600); err != nil {
			return Key{}, err
		}
		return NewKey("", raw)
	}
	if err != nil {
		return Key{}, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		return Key{}, apperror.NewError("the master key file is not valid base64").AddError(err)
	}
	return NewKey("", raw)
}

// GenerateKey creates a new random key.
func GenerateKey() (Key, error) {
	raw, err := security.GetRandomBytes(keySize)
	if err != nil {
		return Key{}, err
	}
	return NewKey("", raw)
}

// Encode returns the base64 form of the key as stored in key files.
func (k Key) Encode() string { return base64.StdEncoding.EncodeToString(k.key) }

func (k Key) aead() (cipher.AEAD, error) {
	if len(k.key) != keySize {
		return nil, apperror.NewError("the master key is not initialised")
	}
	block, err := aes.NewCipher(k.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts plaintext with AES-256-GCM. The result is "id:base64(nonce||ciphertext)". The context is
// authenticated but not stored, binding a secret to its owner so a value cannot be moved to another row.
func (k Key) Seal(plaintext []byte, context string) (string, error) {
	gcm, err := k.aead()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := gcm.Seal(nonce, nonce, plaintext, []byte(context))
	return k.ID + ":" + base64.StdEncoding.EncodeToString(out), nil
}

// Open decrypts a value produced by Seal with the same context.
func (k Key) Open(sealed, context string) ([]byte, error) {
	id, enc, ok := strings.Cut(sealed, ":")
	if !ok {
		return nil, apperror.NewError("malformed sealed value")
	}
	if id != k.ID {
		return nil, apperror.NewErrorf("the value was sealed with key %q, not %q", id, k.ID)
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, apperror.NewError("malformed sealed value")
	}
	gcm, err := k.aead()
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, apperror.NewError("malformed sealed value")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte(context))
	if err != nil {
		return nil, apperror.NewError("the value cannot be decrypted, wrong key or context")
	}
	return plain, nil
}

// Rekey re-encrypts a value sealed with old so that it is sealed with k.
func (k Key) Rekey(old Key, sealed, context string) (string, error) {
	plain, err := old.Open(sealed, context)
	if err != nil {
		return "", err
	}
	return k.Seal(plain, context)
}
