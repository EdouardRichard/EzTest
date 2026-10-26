package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
)

// tokenColumns is the column list shared by the token read query and the scan
// order in scanToken.
const tokenColumns = `id, user_id, token_hash, expires_at, revoked_at`

// TokenRepository implements identity.TokenRepository on a pgx pool.
type TokenRepository struct {
	pool *pgxpool.Pool
}

// NewTokenRepository returns a token repository backed by pool.
func NewTokenRepository(pool *pgxpool.Pool) *TokenRepository {
	return &TokenRepository{pool: pool}
}

// Create stores a token hash for a user. The raw token never reaches the
// database.
func (r *TokenRepository) Create(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) error {
	const query = `INSERT INTO auth_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`
	if _, err := r.pool.Exec(ctx, query, userID, tokenHash, expiresAt); err != nil {
		return fmt.Errorf("postgres: create token: %w", err)
	}
	return nil
}

// GetByHash returns the token with the given hash.
func (r *TokenRepository) GetByHash(ctx context.Context, tokenHash string) (identity.Token, error) {
	const query = `SELECT ` + tokenColumns + ` FROM auth_tokens WHERE token_hash = $1`
	var token identity.Token
	err := r.pool.QueryRow(ctx, query, tokenHash).Scan(
		&token.ID,
		&token.UserID,
		&token.TokenHash,
		&token.ExpiresAt,
		&token.RevokedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.Token{}, identity.ErrTokenNotFound
		}
		return identity.Token{}, fmt.Errorf("postgres: get token by hash: %w", err)
	}
	return token, nil
}

// Revoke marks a token revoked. Revoking an unknown or already revoked token is
// a successful no-op so logout stays idempotent.
func (r *TokenRepository) Revoke(ctx context.Context, tokenHash string) error {
	const query = `UPDATE auth_tokens SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`
	if _, err := r.pool.Exec(ctx, query, tokenHash); err != nil {
		return fmt.Errorf("postgres: revoke token: %w", err)
	}
	return nil
}

// RevokeAllForUser revokes every unrevoked token of a user.
func (r *TokenRepository) RevokeAllForUser(ctx context.Context, userID int64) error {
	const query = `UPDATE auth_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`
	if _, err := r.pool.Exec(ctx, query, userID); err != nil {
		return fmt.Errorf("postgres: revoke user tokens: %w", err)
	}
	return nil
}
