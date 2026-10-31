package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"
)

const (
	// TokenPrefix marks the opaque bearer token so it is recognizable in logs
	// and leak scanners.
	TokenPrefix = "ezt_"
	// TokenTTL is the fixed lifetime of a login session. Tokens are never
	// refreshed or extended.
	TokenTTL = 7 * 24 * time.Hour
	// tokenBytes is the raw entropy behind one token.
	tokenBytes = 32
)

// Token is one persisted session token. Only the SHA-256 hex digest of the raw
// token is stored; the raw value is returned to the client exactly once.
type Token struct {
	ID        int64
	UserID    int64
	TokenHash string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// NewTokenValue returns a fresh opaque token: "ezt_" followed by 32 bytes of
// crypto/rand encoded as unpadded base64url.
func NewTokenValue() (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("identity: read random token bytes: %w", err)
	}
	return TokenPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// TokenHash returns the lowercase hex SHA-256 digest stored in the database.
func TokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
