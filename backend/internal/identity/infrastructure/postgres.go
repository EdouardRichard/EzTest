// Package identityinfra is the identity infrastructure layer: the PostgreSQL
// adapter for users and tokens, and the bcrypt password hasher.
package identityinfra

import (
	"context"
	"errors"
	"fmt"
	"time"

	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const userColumns = `id, username, display_name, password_hash, role, status, created_at, updated_at`

// Repository is the PostgreSQL implementation of identity.Repository.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository wraps a pgx pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// CountUsers returns the number of app_user rows.
func (r *Repository) CountUsers(ctx context.Context) (int64, error) {
	var count int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM app_user`).Scan(&count); err != nil {
		return 0, fmt.Errorf("identity: count users: %w", err)
	}
	return count, nil
}

// CreateUser inserts one user and maps unique-index violations to domain
// errors.
func (r *Repository) CreateUser(ctx context.Context, user identity.User) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO app_user (id, username, display_name, password_hash, role, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, user.ID, user.Username, user.DisplayName, user.PasswordHash, string(user.Role), string(user.Status), user.CreatedAt, user.UpdatedAt)
	if err != nil {
		return mapUserInsertError(err)
	}
	return nil
}

// GetUser loads one user by ID.
func (r *Repository) GetUser(ctx context.Context, id int64) (identity.User, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM app_user WHERE id = $1`, id)
	return scanUser(row, fmt.Sprintf("get user %d", id))
}

// GetUserByUsername loads one user by case-insensitive username.
func (r *Repository) GetUserByUsername(ctx context.Context, username string) (identity.User, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM app_user WHERE lower(username) = lower($1)`, username)
	return scanUser(row, "get user by username")
}

// ListUsers returns users with ID less than beforeID, newest first.
func (r *Repository) ListUsers(ctx context.Context, limit int, beforeID *int64) ([]identity.User, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+userColumns+`
		FROM app_user
		WHERE ($1::BIGINT IS NULL OR id < $1)
		ORDER BY id DESC
		LIMIT $2
	`, beforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("identity: list users: %w", err)
	}
	defer rows.Close()

	users := make([]identity.User, 0)
	for rows.Next() {
		user, err := scanUser(rows, "list users")
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("identity: list users: %w", err)
	}
	return users, nil
}

// UpdateUser updates the mutable profile fields and returns the stored row.
func (r *Repository) UpdateUser(ctx context.Context, id int64, displayName string, status identity.Status, at time.Time) (identity.User, error) {
	row := r.pool.QueryRow(ctx, `
		UPDATE app_user
		SET display_name = $2, status = $3, updated_at = $4
		WHERE id = $1
		RETURNING `+userColumns+`
	`, id, displayName, string(status), at)
	return scanUser(row, fmt.Sprintf("update user %d", id))
}

// UpdatePassword replaces the password hash.
func (r *Repository) UpdatePassword(ctx context.Context, id int64, passwordHash string, at time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE app_user
		SET password_hash = $2, updated_at = $3
		WHERE id = $1
	`, id, passwordHash, at)
	if err != nil {
		return fmt.Errorf("identity: update password %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return identity.ErrUserNotFound
	}
	return nil
}

// CreateToken inserts one token digest row.
func (r *Repository) CreateToken(ctx context.Context, token identity.Token) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO auth_token (id, user_id, token_hash, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, token.ID, token.UserID, token.TokenHash, token.ExpiresAt, token.CreatedAt)
	if err != nil {
		return fmt.Errorf("identity: create token: %w", err)
	}
	return nil
}

// GetTokenByHash loads one token by its lowercase hex SHA-256 digest.
func (r *Repository) GetTokenByHash(ctx context.Context, hash string) (identity.Token, error) {
	var token identity.Token
	err := r.pool.QueryRow(ctx, `
		SELECT id, user_id, token_hash, expires_at, created_at
		FROM auth_token
		WHERE token_hash = $1
	`, hash).Scan(&token.ID, &token.UserID, &token.TokenHash, &token.ExpiresAt, &token.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.Token{}, identity.ErrTokenNotFound
	}
	if err != nil {
		return identity.Token{}, fmt.Errorf("identity: get token: %w", err)
	}
	return token, nil
}

// DeleteToken removes one token digest; missing rows are not an error.
func (r *Repository) DeleteToken(ctx context.Context, hash string) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM auth_token WHERE token_hash = $1`, hash); err != nil {
		return fmt.Errorf("identity: delete token: %w", err)
	}
	return nil
}

// DeleteTokensForUser revokes every token of one user.
func (r *Repository) DeleteTokensForUser(ctx context.Context, userID int64) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM auth_token WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("identity: delete user tokens: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(row rowScanner, operation string) (identity.User, error) {
	var user identity.User
	var role, status string
	err := row.Scan(
		&user.ID, &user.Username, &user.DisplayName, &user.PasswordHash,
		&role, &status, &user.CreatedAt, &user.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.User{}, identity.ErrUserNotFound
	}
	if err != nil {
		return identity.User{}, fmt.Errorf("identity: %s: %w", operation, err)
	}
	user.Role = identity.Role(role)
	user.Status = identity.Status(status)
	return user, nil
}

func mapUserInsertError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case "uk_app_user_username":
			return identity.ErrUsernameTaken
		case "uk_app_user_one_admin":
			return identity.ErrAdminExists
		}
	}
	return fmt.Errorf("identity: create user: %w", err)
}
