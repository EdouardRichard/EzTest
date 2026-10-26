package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

const (
	// TokenPrefix prefixes every raw bearer token issued by EzTest.
	TokenPrefix = "ezt_"
	// TokenTTL is how long a freshly issued token stays valid.
	TokenTTL = 12 * time.Hour
	// TokenSeenInterval is the maximum frequency at which an authenticated
	// request refreshes auth_tokens.last_seen_at (spec §6.2).
	TokenSeenInterval = 5 * time.Minute
	// tokenRandomBytes is the entropy of a raw token.
	tokenRandomBytes = 32
)

// Token is a stored bearer-token record. Only the SHA-256 hash of the raw token
// is persisted, so a database leak does not expose usable credentials.
type Token struct {
	ID        int64
	UserID    int64
	TokenHash string
	ExpiresAt time.Time
	RevokedAt *time.Time
}

// Token-related domain errors. Callers use errors.Is rather than comparing
// messages.
var (
	// ErrUnauthenticated means the presented credentials or token are not
	// valid. It deliberately does not say which check failed.
	ErrUnauthenticated = errors.New("identity: unauthenticated")
	// ErrTokenNotFound means no token matches the requested hash.
	ErrTokenNotFound = errors.New("identity: token not found")
)

// GenerateToken returns a new random raw token and the hash to persist. Raw
// tokens are `ezt_` followed by the unpadded base64url encoding of 32 random
// bytes; the hash is the lowercase hex SHA-256 of the raw token.
func GenerateToken() (raw string, tokenHash string, err error) {
	buf := make([]byte, tokenRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("identity: generate token: %w", err)
	}
	raw = TokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	return raw, HashToken(raw), nil
}

// HashToken returns the lowercase hex SHA-256 of a raw token. It is the only
// representation stored in the database and used for lookups.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
