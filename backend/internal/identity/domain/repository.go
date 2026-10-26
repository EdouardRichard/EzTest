package identity

import "context"

// CreateParams are the fields needed to create a user. PasswordHash must
// already be hashed by the caller; the repository never sees plaintext.
type CreateParams struct {
	Username     string
	PasswordHash string
	Role         Role
}

// Repository is the persistence port for users. Every method returns
// ErrUserNotFound (when applicable) or a wrapped infrastructure error.
type Repository interface {
	// Create inserts a user and returns the stored row. A duplicate username
	// returns ErrUsernameTaken and a second admin returns ErrAdminExists.
	Create(ctx context.Context, params CreateParams) (User, error)
	// GetByUsername returns the account with the given username.
	GetByUsername(ctx context.Context, username string) (User, error)
	// GetByID returns the account with the given id.
	GetByID(ctx context.Context, id int64) (User, error)
	// List returns up to limit users with ID greater than afterID, ordered by
	// ID ascending. more reports whether at least one further user exists.
	List(ctx context.Context, limit int, afterID int64) ([]User, bool, error)
	// UpdateStatus changes a user's status and returns the stored row.
	UpdateStatus(ctx context.Context, id int64, status Status) (User, error)
	// UpdatePasswordHash replaces a user's password hash.
	UpdatePasswordHash(ctx context.Context, id int64, passwordHash string) error
	// UpdateLastLogin records a successful login by setting last_login_at to
	// the database clock.
	UpdateLastLogin(ctx context.Context, id int64) error
	// CountAdmins returns the number of users with the admin role.
	CountAdmins(ctx context.Context) (int, error)
}
