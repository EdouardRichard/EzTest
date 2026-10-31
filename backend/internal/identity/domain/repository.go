package identity

import (
	"context"
	"time"
)

// Repository is the persistence port for users and tokens. The PostgreSQL
// adapter implements it; unit tests use an in-memory fake.
type Repository interface {
	// CountUsers returns the number of rows in app_user. The startup seed uses
	// it to decide whether the fixed administrator must be created.
	CountUsers(ctx context.Context) (int64, error)
	// CreateUser inserts one user. A duplicate username (case-insensitive) is
	// reported as ErrUsernameTaken; a second administrator row as ErrAdminExists.
	CreateUser(ctx context.Context, user User) error
	// GetUser loads one user by ID or returns ErrUserNotFound.
	GetUser(ctx context.Context, id int64) (User, error)
	// GetUserByUsername loads one user by case-insensitive username or returns
	// ErrUserNotFound.
	GetUserByUsername(ctx context.Context, username string) (User, error)
	// ListUsers returns up to limit users with ID less than beforeID, newest
	// first. A nil beforeID starts at the newest user.
	ListUsers(ctx context.Context, limit int, beforeID *int64) ([]User, error)
	// UpdateUser replaces the mutable profile fields and returns the stored row.
	UpdateUser(ctx context.Context, id int64, displayName string, status Status, at time.Time) (User, error)
	// UpdatePassword replaces the password hash and touches updated_at.
	UpdatePassword(ctx context.Context, id int64, passwordHash string, at time.Time) error

	// CreateToken stores one token digest row.
	CreateToken(ctx context.Context, token Token) error
	// GetTokenByHash loads one token by its lowercase hex SHA-256 digest or
	// returns ErrTokenNotFound.
	GetTokenByHash(ctx context.Context, hash string) (Token, error)
	// DeleteToken removes one token digest. Missing rows are not an error.
	DeleteToken(ctx context.Context, hash string) error
	// DeleteTokensForUser revokes every token of one user.
	DeleteTokensForUser(ctx context.Context, userID int64) error
}
