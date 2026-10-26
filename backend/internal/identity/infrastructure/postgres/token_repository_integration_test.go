//go:build integration

package postgres_test

import (
	"errors"
	"testing"
	"time"

	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure/password"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

func TestTokenRepositoryCreateGetRevoke(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	users := postgres.NewUserRepository(pool)
	tokens := postgres.NewTokenRepository(pool)

	user, err := users.Create(ctx, identity.CreateParams{
		Username:     "qa01",
		PasswordHash: mustHash(t, password.NewHasher(12), "qa-pass-1234"),
		Role:         identity.RoleUser,
	})
	if err != nil {
		t.Fatal(err)
	}

	raw := identity.TokenPrefix + "integration-token"
	tokenHash := identity.HashToken(raw)
	expiresAt := time.Now().UTC().Add(identity.TokenTTL).Truncate(time.Microsecond)
	if err := tokens.Create(ctx, user.ID, tokenHash, expiresAt); err != nil {
		t.Fatal(err)
	}

	stored, err := tokens.GetByHash(ctx, tokenHash)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID == 0 || stored.UserID != user.ID || stored.TokenHash != tokenHash {
		t.Fatalf("stored token=%+v", stored)
	}
	if !stored.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("expires_at=%v want %v", stored.ExpiresAt, expiresAt)
	}
	if stored.RevokedAt != nil {
		t.Fatalf("new token revoked_at=%v want nil", stored.RevokedAt)
	}

	// The raw token must never be accepted as a lookup key.
	if _, err := tokens.GetByHash(ctx, raw); !errors.Is(err, identity.ErrTokenNotFound) {
		t.Fatalf("raw token lookup: err=%v, want ErrTokenNotFound", err)
	}
	if _, err := tokens.GetByHash(ctx, identity.HashToken(identity.TokenPrefix+"missing")); !errors.Is(err, identity.ErrTokenNotFound) {
		t.Fatalf("missing token: err=%v, want ErrTokenNotFound", err)
	}
	if err := tokens.Create(ctx, user.ID, tokenHash, expiresAt); err == nil {
		t.Fatal("duplicate token hash accepted")
	}

	if err := tokens.Revoke(ctx, tokenHash); err != nil {
		t.Fatal(err)
	}
	revoked, err := tokens.GetByHash(ctx, tokenHash)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.RevokedAt == nil {
		t.Fatal("token was not marked revoked")
	}
	// Revocation is idempotent, including for tokens that do not exist.
	if err := tokens.Revoke(ctx, tokenHash); err != nil {
		t.Fatalf("revoking a revoked token: %v", err)
	}
	if err := tokens.Revoke(ctx, identity.HashToken(identity.TokenPrefix+"missing")); err != nil {
		t.Fatalf("revoking a missing token: %v", err)
	}
}

func TestTokenRepositoryRevokeAllForUser(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	users := postgres.NewUserRepository(pool)
	tokens := postgres.NewTokenRepository(pool)

	hash := mustHash(t, password.NewHasher(12), "qa-pass-1234")
	first, err := users.Create(ctx, identity.CreateParams{Username: "qa01", PasswordHash: hash, Role: identity.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	second, err := users.Create(ctx, identity.CreateParams{Username: "qa02", PasswordHash: hash, Role: identity.RoleUser})
	if err != nil {
		t.Fatal(err)
	}

	expiresAt := time.Now().UTC().Add(identity.TokenTTL)
	firstToken := identity.HashToken(identity.TokenPrefix + "first-token")
	secondToken := identity.HashToken(identity.TokenPrefix + "second-token")
	otherUserToken := identity.HashToken(identity.TokenPrefix + "other-user-token")
	for _, token := range []struct {
		userID int64
		hash   string
	}{
		{userID: first.ID, hash: firstToken},
		{userID: first.ID, hash: secondToken},
		{userID: second.ID, hash: otherUserToken},
	} {
		if err := tokens.Create(ctx, token.userID, token.hash, expiresAt); err != nil {
			t.Fatal(err)
		}
	}

	if err := tokens.RevokeAllForUser(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{firstToken, secondToken} {
		stored, err := tokens.GetByHash(ctx, hash)
		if err != nil {
			t.Fatal(err)
		}
		if stored.RevokedAt == nil {
			t.Fatalf("token %q of the target user was not revoked", hash)
		}
	}
	other, err := tokens.GetByHash(ctx, otherUserToken)
	if err != nil {
		t.Fatal(err)
	}
	if other.RevokedAt != nil {
		t.Fatal("RevokeAllForUser revoked another user's token")
	}

	if err := tokens.RevokeAllForUser(ctx, first.ID); err != nil {
		t.Fatalf("second RevokeAllForUser: %v", err)
	}
	if err := tokens.RevokeAllForUser(ctx, 99999); err != nil {
		t.Fatalf("RevokeAllForUser for a missing user: %v", err)
	}
}

func TestTokenRepositoryRejectsUnknownUser(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	tokens := postgres.NewTokenRepository(pool)

	err := tokens.Create(ctx, 99999, identity.HashToken(identity.TokenPrefix+"orphan"), time.Now().UTC().Add(time.Hour))
	if err == nil {
		t.Fatal("token for a missing user was accepted")
	}
}
