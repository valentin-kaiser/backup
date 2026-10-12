package secure

import (
	"encoding/hex"

	"github.com/valentin-kaiser/go-core/security"
)

// NewToken returns a random 256 bit token (hex) together with the hash to store. Only the hash is kept, the
// token is shown once.
func NewToken() (token, hash string, err error) {
	raw, err := security.GetRandomBytes(32)
	if err != nil {
		return "", "", err
	}
	token = hex.EncodeToString(raw)
	return token, HashToken(token), nil
}

// HashToken returns the storable hash of a token. Tokens carry 256 bits of entropy, so a plain SHA-256 is
// sufficient and allows a lookup by hash.
func HashToken(token string) string {
	return security.SHA256([]byte(token))
}
