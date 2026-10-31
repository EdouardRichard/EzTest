// Package identityapp implements the identity use cases: the startup
// administrator seed, login/logout/authentication, self-service password
// change, and the admin user-management operations.
package identityapp

import (
	"context"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
)

// Session is the result of a successful login. Token carries the raw bearer
// value and must be shown to the client exactly once.
type Session struct {
	Token     string
	ExpiresAt time.Time
	User      identity.User
}

// PasswordHasher hashes and verifies passwords. The production implementation
// pre-hashes with SHA-256+base64 and then bcrypt cost 12.
type PasswordHasher interface {
	// Hash returns the encoded password hash.
	Hash(password string) (string, error)
	// Verify reports whether password matches the encoded hash.
	Verify(hash, password string) bool
}

type actorKey struct{}

// WithActor stores the authenticated user in the context. HTTP handlers that
// need the full user profile read it back with ActorFrom.
func WithActor(ctx context.Context, user identity.User) context.Context {
	return context.WithValue(ctx, actorKey{}, user)
}

// ActorFrom reads the authenticated user from the context.
func ActorFrom(ctx context.Context) (identity.User, bool) {
	user, ok := ctx.Value(actorKey{}).(identity.User)
	return user, ok
}
