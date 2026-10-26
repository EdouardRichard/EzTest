// Package postgres implements the identity repository ports with PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
)

// uniqueViolationCode is PostgreSQL's SQLSTATE for unique_violation.
const uniqueViolationCode = "23505"

// userColumns is the column list shared by every user query and the row order
// scanUser expects.
const userColumns = `id, username, password_hash, role, status, created_at, updated_at, last_login_at`

// UserRepository implements identity.Repository on a pgx pool.
type UserRepository struct {
	pool *pgxpool.Pool
}

// NewUserRepository returns a user repository backed by pool.
func NewUserRepository(pool *pgxpool.Pool) *UserRepository {
	return &UserRepository{pool: pool}
}

// rowScanner is the subset of pgx.Row and pgx.Rows used to scan one user.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanUser scans one row in userColumns order.
func scanUser(row rowScanner) (identity.User, error) {
	var (
		user   identity.User
		role   string
		status string
	)
	if err := row.Scan(
		&user.ID,
		&user.Username,
		&user.PasswordHash,
		&role,
		&status,
		&user.CreatedAt,
		&user.UpdatedAt,
		&user.LastLoginAt,
	); err != nil {
		return identity.User{}, err
	}
	user.Role = identity.Role(role)
	user.Status = identity.Status(status)
	return user, nil
}

// Create inserts a user and returns the stored row.
func (r *UserRepository) Create(ctx context.Context, params identity.CreateParams) (identity.User, error) {
	const query = `INSERT INTO users (username, password_hash, role) VALUES ($1, $2, $3) RETURNING ` + userColumns
	user, err := scanUser(r.pool.QueryRow(ctx, query, params.Username, params.PasswordHash, string(params.Role)))
	if err != nil {
		if mapped := mapUniqueViolation(err); mapped != nil {
			return identity.User{}, mapped
		}
		return identity.User{}, fmt.Errorf("postgres: create user: %w", err)
	}
	return user, nil
}

// GetByUsername returns the account with the given username.
func (r *UserRepository) GetByUsername(ctx context.Context, username string) (identity.User, error) {
	const query = `SELECT ` + userColumns + ` FROM users WHERE username = $1`
	user, err := scanUser(r.pool.QueryRow(ctx, query, username))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.User{}, identity.ErrUserNotFound
		}
		return identity.User{}, fmt.Errorf("postgres: get user by username: %w", err)
	}
	return user, nil
}

// GetByID returns the account with the given id.
func (r *UserRepository) GetByID(ctx context.Context, id int64) (identity.User, error) {
	const query = `SELECT ` + userColumns + ` FROM users WHERE id = $1`
	user, err := scanUser(r.pool.QueryRow(ctx, query, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.User{}, identity.ErrUserNotFound
		}
		return identity.User{}, fmt.Errorf("postgres: get user by id: %w", err)
	}
	return user, nil
}

// List returns up to limit users after afterID in ascending ID order. more
// reports whether another row exists after the returned page.
func (r *UserRepository) List(ctx context.Context, limit int, afterID int64) ([]identity.User, bool, error) {
	if limit <= 0 {
		return nil, false, fmt.Errorf("postgres: list users: limit must be positive")
	}
	const query = `SELECT ` + userColumns + ` FROM users WHERE id > $1 ORDER BY id ASC LIMIT $2`
	rows, err := r.pool.Query(ctx, query, afterID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list users: %w", err)
	}
	defer rows.Close()

	users := make([]identity.User, 0, limit+1)
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, false, fmt.Errorf("postgres: scan user: %w", err)
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("postgres: list users: %w", err)
	}

	more := len(users) > limit
	if more {
		users = users[:limit]
	}
	return users, more, nil
}

// UpdateStatus changes a user's status and returns the stored row.
func (r *UserRepository) UpdateStatus(ctx context.Context, id int64, status identity.Status) (identity.User, error) {
	const query = `UPDATE users SET status = $2, updated_at = now() WHERE id = $1 RETURNING ` + userColumns
	user, err := scanUser(r.pool.QueryRow(ctx, query, id, string(status)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.User{}, identity.ErrUserNotFound
		}
		return identity.User{}, fmt.Errorf("postgres: update user status: %w", err)
	}
	return user, nil
}

// UpdatePasswordHash replaces a user's password hash.
func (r *UserRepository) UpdatePasswordHash(ctx context.Context, id int64, passwordHash string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`,
		id, passwordHash)
	if err != nil {
		return fmt.Errorf("postgres: update user password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return identity.ErrUserNotFound
	}
	return nil
}

// CountAdmins returns the number of admin accounts. The users_one_admin index
// keeps this at most 1 in P0.
func (r *UserRepository) CountAdmins(ctx context.Context) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE role = 'admin'`).Scan(&count); err != nil {
		return 0, fmt.Errorf("postgres: count admins: %w", err)
	}
	return count, nil
}

// mapUniqueViolation translates the users table's unique violations into domain
// errors. It returns nil when err is not a unique violation.
func mapUniqueViolation(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != uniqueViolationCode {
		return nil
	}
	switch pgErr.ConstraintName {
	case "users_username_key":
		return identity.ErrUsernameTaken
	case "users_one_admin":
		return identity.ErrAdminExists
	default:
		return nil
	}
}
