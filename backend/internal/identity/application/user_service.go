// Package application implements the identity use cases on top of the domain
// repository ports, a password hashing port and the audit recorder.
package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	audit "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
)

const (
	// defaultListLimit is used when a caller does not ask for a page size.
	defaultListLimit = 50
	// maxListLimit caps the page size so a client cannot request the whole table.
	maxListLimit = 100
)

// Hasher is the password hashing port of the identity use cases. It is
// implemented by the bcrypt infrastructure adapter.
type Hasher interface {
	Hash(password string) (string, error)
	Verify(hash, password string) bool
}

// TokenRevoker revokes every token of a user. It is implemented by the token
// repository and used when a password is reset: the old sessions must stop
// working immediately.
type TokenRevoker interface {
	RevokeAllForUser(ctx context.Context, userID int64) error
}

// UserService manages user accounts for the admin settings screen and seeds the
// single admin account at startup.
type UserService struct {
	repo     identity.Repository
	hasher   Hasher
	recorder audit.Recorder
	tokens   TokenRevoker
}

// NewUserService wires the user service to its repository, password hasher,
// audit recorder and token revoker.
func NewUserService(repo identity.Repository, hasher Hasher, recorder audit.Recorder, tokens TokenRevoker) *UserService {
	return &UserService{repo: repo, hasher: hasher, recorder: recorder, tokens: tokens}
}

// withRequestContext merges the HTTP request metadata and the authenticated
// actor carried by ctx into entry. The entry keeps whatever the service set
// explicitly; the context only fills the gaps. This is how handler-set metadata
// reaches existing service-level audit records without changing every service
// signature.
func withRequestContext(ctx context.Context, entry auditdomain.Entry) auditdomain.Entry {
	meta := audit.MetaFromContext(ctx)
	if entry.RequestID == "" {
		entry.RequestID = meta.RequestID
	}
	if entry.IP == "" {
		entry.IP = meta.IP
	}
	if entry.UserAgent == "" {
		entry.UserAgent = meta.UserAgent
	}
	if entry.ActorUserID == nil {
		if actorID, ok := audit.ActorIDFromContext(ctx); ok {
			entry.ActorUserID = &actorID
		}
	}
	return entry
}

// recordAudit writes one audit entry. Auditing is best-effort: a failed audit
// write must not turn a completed action into an error, but it is logged. The
// log line never includes the entry metadata, so credentials cannot leak
// through it. Request metadata and the authenticated actor are merged from the
// context before the entry is persisted.
func recordAudit(ctx context.Context, recorder audit.Recorder, entry auditdomain.Entry) {
	entry = withRequestContext(ctx, entry)
	if err := recorder.Record(ctx, entry); err != nil {
		slog.ErrorContext(ctx, "audit: record entry", "action", entry.Action, "error", err)
	}
}

// Create creates a regular user. The admin is only ever created by EnsureAdmin:
// P0 supports exactly one admin and no privilege escalation endpoint.
func (s *UserService) Create(ctx context.Context, username, password string) (identity.User, error) {
	if err := identity.ValidateUsername(username); err != nil {
		return identity.User{}, err
	}
	if err := identity.ValidatePassword(password); err != nil {
		return identity.User{}, err
	}
	hash, err := s.hasher.Hash(password)
	if err != nil {
		return identity.User{}, fmt.Errorf("identity: hash password: %w", err)
	}
	user, err := s.repo.Create(ctx, identity.CreateParams{
		Username:     username,
		PasswordHash: hash,
		Role:         identity.RoleUser,
	})
	if err != nil {
		return identity.User{}, err
	}
	recordAudit(ctx, s.recorder, auditdomain.Entry{
		Action:     audit.ActionUserCreated,
		TargetType: "user",
		TargetID:   strconv.FormatInt(user.ID, 10),
		Metadata: map[string]any{
			"username": user.Username,
			"role":     string(user.Role),
			"status":   string(user.Status),
		},
	})
	return user, nil
}

// EnsureAdmin creates the single admin account when the database has none.
// It is a no-op once an admin exists, so repeated startups are idempotent and a
// deployment whose seed has already run can start without EZTEST_ADMIN_*
// configured. Without an admin, missing or invalid credentials are an error.
// The seed is a startup bootstrap rather than an authenticated action, so it is
// deliberately not audited.
func (s *UserService) EnsureAdmin(ctx context.Context, username, password string) error {
	admins, err := s.repo.CountAdmins(ctx)
	if err != nil {
		return fmt.Errorf("identity: count admins: %w", err)
	}
	if admins > 0 {
		return nil
	}

	if err := identity.ValidateUsername(username); err != nil {
		return fmt.Errorf("identity: admin username: %w", err)
	}
	if err := identity.ValidatePassword(password); err != nil {
		return fmt.Errorf("identity: admin password: %w", err)
	}
	hash, err := s.hasher.Hash(password)
	if err != nil {
		return fmt.Errorf("identity: hash admin password: %w", err)
	}
	if _, err := s.repo.Create(ctx, identity.CreateParams{
		Username:     username,
		PasswordHash: hash,
		Role:         identity.RoleAdmin,
	}); err != nil {
		// Another instance can win the race between CountAdmins and Create. The
		// single-admin index still guarantees one admin, so a concurrent seed
		// is treated as success. A plain duplicate username (for example a
		// regular user already named like the seed) stays an error.
		if errors.Is(err, identity.ErrAdminExists) {
			return nil
		}
		if errors.Is(err, identity.ErrUsernameTaken) {
			if count, countErr := s.repo.CountAdmins(ctx); countErr == nil && count > 0 {
				return nil
			}
		}
		return fmt.Errorf("identity: create admin: %w", err)
	}
	return nil
}

// List returns a page of users ordered by ID. Non-positive limits fall back to
// the default page size and oversized limits are capped.
func (s *UserService) List(ctx context.Context, limit int, afterID int64) ([]identity.User, bool, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	return s.repo.List(ctx, limit, afterID)
}

// Get returns one user by ID.
func (s *UserService) Get(ctx context.Context, id int64) (identity.User, error) {
	return s.repo.GetByID(ctx, id)
}

// SetStatus changes a user's status. P0 has exactly one admin and no
// un-seeded recovery path, so an admin disabling itself would lock everyone
// out; that request is rejected with ErrLastAdmin.
func (s *UserService) SetStatus(ctx context.Context, actorID, userID int64, status identity.Status) (identity.User, error) {
	if !status.Valid() {
		return identity.User{}, identity.ErrInvalidStatus
	}
	if actorID == userID && status == identity.StatusDisabled {
		return identity.User{}, identity.ErrLastAdmin
	}
	user, err := s.repo.UpdateStatus(ctx, userID, status)
	if err != nil {
		return identity.User{}, err
	}
	recordAudit(ctx, s.recorder, auditdomain.Entry{
		Action:      audit.ActionUserUpdated,
		ActorUserID: &actorID,
		TargetType:  "user",
		TargetID:    strconv.FormatInt(user.ID, 10),
		Metadata: map[string]any{
			"username": user.Username,
			"status":   string(user.Status),
		},
	})
	return user, nil
}

// ResetPassword replaces a user's password with a freshly hashed one and
// revokes every token of that user, so sessions opened with the old password
// stop working immediately.
func (s *UserService) ResetPassword(ctx context.Context, userID int64, password string) error {
	if err := identity.ValidatePassword(password); err != nil {
		return err
	}
	hash, err := s.hasher.Hash(password)
	if err != nil {
		return fmt.Errorf("identity: hash password: %w", err)
	}
	if err := s.repo.UpdatePasswordHash(ctx, userID, hash); err != nil {
		return err
	}
	if err := s.tokens.RevokeAllForUser(ctx, userID); err != nil {
		return fmt.Errorf("identity: revoke user tokens: %w", err)
	}
	recordAudit(ctx, s.recorder, auditdomain.Entry{
		Action:     audit.ActionUserPasswordReset,
		TargetType: "user",
		TargetID:   strconv.FormatInt(userID, 10),
	})
	return nil
}
