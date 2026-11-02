// Package identity is the identity bounded context: application users, their
// opaque bearer tokens, and the persistence ports behind both.
package identity

import (
	"errors"
	"time"
)

// Role is the account role. P0 knows exactly one administrator.
type Role string

const (
	// RoleAdmin is the single seeded administrator.
	RoleAdmin Role = "admin"
	// RoleUser is a regular user created through the admin API.
	RoleUser Role = "user"
)

// Status is the account lifecycle state.
type Status string

const (
	// StatusActive accounts may log in and use their tokens.
	StatusActive Status = "active"
	// StatusDisabled accounts may not log in and their tokens are revoked.
	StatusDisabled Status = "disabled"
)

// Domain errors. The application layer maps them onto the shared API error
// envelope; the PostgreSQL adapter maps its driver errors onto them.
var (
	ErrUserNotFound  = errors.New("identity: user not found")
	ErrTokenNotFound = errors.New("identity: token not found")
	ErrUsernameTaken = errors.New("identity: username already exists")
	ErrAdminExists   = errors.New("identity: administrator already exists")
)

// User is one application account. PasswordHash never leaves the backend: the
// HTTP DTOs omit it and audit events never carry it.
type User struct {
	ID           int64
	Username     string
	DisplayName  string
	PasswordHash string
	Role         Role
	Status       Status
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// IsAdmin reports whether the user has the administrator role.
func (u User) IsAdmin() bool { return u.Role == RoleAdmin }

// IsActive reports whether the account may authenticate.
func (u User) IsActive() bool { return u.Status == StatusActive }
