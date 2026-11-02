package identityinfra

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// DefaultBcryptCost is the specification-mandated bcrypt work factor.
const DefaultBcryptCost = 12

// Hasher pre-hashes passwords with SHA-256+base64 before bcrypt. The prehash
// removes bcrypt's 72-byte input truncation, so the full 8-128 rune password
// policy contributes entropy.
type Hasher struct {
	cost int
}

// NewHasher builds a hasher. A non-positive cost falls back to
// DefaultBcryptCost.
func NewHasher(cost int) *Hasher {
	if cost <= 0 {
		cost = DefaultBcryptCost
	}
	return &Hasher{cost: cost}
}

// Hash returns the bcrypt string for password.
func (h *Hasher) Hash(password string) (string, error) {
	encoded, err := bcrypt.GenerateFromPassword(prehash(password), h.cost)
	if err != nil {
		return "", fmt.Errorf("identity: hash password: %w", err)
	}
	return string(encoded), nil
}

// Verify reports whether password matches hash.
func (h *Hasher) Verify(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), prehash(password)) == nil
}

// prehash applies SHA-256 then base64 so bcrypt never sees more than 44 bytes
// of input and never truncates a password.
func prehash(password string) []byte {
	sum := sha256.Sum256([]byte(password))
	encoded := make([]byte, base64.StdEncoding.EncodedLen(len(sum)))
	base64.StdEncoding.Encode(encoded, sum[:])
	return encoded
}
