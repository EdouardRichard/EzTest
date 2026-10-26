package identity

import (
	"context"
	"time"
)

// TokenRepository is the persistence port for bearer tokens. Every method
// returns ErrTokenNotFound (when applicable) or a wrapped infrastructure error.
type TokenRepository interface {
	// Create stores a token hash for a user. The raw token never reaches the
	// repository.
	Create(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) error
	// GetByHash returns the token with the given hash.
	GetByHash(ctx context.Context, tokenHash string) (Token, error)
	// Revoke marks the token with the given hash revoked. It is idempotent:
	// revoking an unknown or already revoked token is not an error.
	Revoke(ctx context.Context, tokenHash string) error
	// RevokeAllForUser revokes every token of a user. It is idempotent.
	RevokeAllForUser(ctx context.Context, userID int64) error
}
