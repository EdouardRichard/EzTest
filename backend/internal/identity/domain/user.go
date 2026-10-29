// Package identity is the identity context's domain model: users with their
// roles and statuses, the repository port that persists them and the domain
// errors shared by the application layer.
package identity

import (
	"errors"
	"regexp"
	"time"
	"unicode/utf8"
)

// Role is a user's authorization role. P0 has exactly one admin, seeded at
// startup; the API only creates regular users.
type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

// Status is a user's lifecycle status.
type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

// Valid reports whether s is a status the API accepts.
func (s Status) Valid() bool {
	return s == StatusActive || s == StatusDisabled
}

// User is an account. PasswordHash is a bcrypt hash and is never returned to
// clients nor written to logs.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         Role
	Status       Status
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastLoginAt  *time.Time
}

const (
	// MinPasswordLength and MaxPasswordLength bound the accepted password
	// length in characters.
	MinPasswordLength = 10
	MaxPasswordLength = 128
)

// usernamePattern is the documented username format. Usernames are matched
// case-sensitively and stored verbatim.
var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,64}$`)

// ValidateUsername enforces the username format.
func ValidateUsername(username string) error {
	if !usernamePattern.MatchString(username) {
		return ErrInvalidUsername
	}
	return nil
}

// ValidatePassword enforces the password length limits.
func ValidatePassword(password string) error {
	length := utf8.RuneCountInString(password)
	if length < MinPasswordLength || length > MaxPasswordLength {
		return ErrWeakPassword
	}
	return nil
}

// Domain errors shared by the repository port, the user service and the HTTP
// layer. Callers use errors.Is rather than comparing messages.
var (
	// ErrUsernameTaken means another account already uses the username.
	ErrUsernameTaken = errors.New("identity: username already taken")
	// ErrUserNotFound means no account matches the requested identifier.
	ErrUserNotFound = errors.New("identity: user not found")
	// ErrLastAdmin means the operation would leave P0 without its single admin.
	ErrLastAdmin = errors.New("identity: cannot disable the last admin")
	// ErrInvalidUsername means the username does not match the documented format.
	ErrInvalidUsername = errors.New("identity: invalid username")
	// ErrWeakPassword means the password length is outside the documented range.
	ErrWeakPassword = errors.New("identity: password must be between 10 and 128 characters")
	// ErrAdminExists means the single-admin index rejected a second admin. It is
	// returned to EnsureAdmin so a concurrent startup can treat it as success.
	ErrAdminExists = errors.New("identity: an admin already exists")
	// ErrInvalidStatus means the requested status is not active or disabled.
	ErrInvalidStatus = errors.New("identity: invalid user status")
)
