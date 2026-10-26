// Package password is the bcrypt adapter for hashing and verifying user
// passwords.
package password

import (
	"crypto/sha256"
	"encoding/base64"

	"golang.org/x/crypto/bcrypt"
)

// Hasher hashes and verifies passwords. The plaintext password is never stored
// nor logged.
type Hasher interface {
	Hash(password string) (string, error)
	Verify(hash, password string) bool
}

// NewHasher returns a bcrypt password hasher with the given cost.
func NewHasher(cost int) Hasher {
	return bcryptHasher{cost: cost}
}

type bcryptHasher struct {
	cost int
}

// Hash returns a bcrypt hash of the password.
//
// bcrypt only reads the first 72 bytes of its input while the domain accepts
// passwords up to 128 characters, so the password is first reduced to a
// fixed-size SHA-256 digest. This keeps the full password significant instead
// of silently truncating it; bcrypt itself (and its cost) is unchanged.
func (h bcryptHasher) Hash(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword(prepare(password), h.cost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// Verify reports whether password matches hash in constant time.
func (h bcryptHasher) Verify(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), prepare(password)) == nil
}

// prepare maps a password to the fixed-size input bcrypt operates on.
func prepare(password string) []byte {
	sum := sha256.Sum256([]byte(password))
	return []byte(base64.RawStdEncoding.EncodeToString(sum[:]))
}
