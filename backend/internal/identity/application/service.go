package identityapp

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

const (
	// SeedAdminUsername is the fixed first-start administrator account.
	SeedAdminUsername = "admin"
	// SeedAdminPassword is the fixed first-start administrator password. It is
	// a development/test trust model default; deployments must change it.
	SeedAdminPassword = "admin123"
	// SeedAdminDisplayName is the seeded administrator display name.
	SeedAdminDisplayName = "管理员"

	// MinPasswordLength is the shortest accepted password, in runes.
	MinPasswordLength = 8
	// MaxPasswordLength is the longest accepted password, in runes.
	MaxPasswordLength = 128
	// MaxDisplayNameLength bounds display names.
	MaxDisplayNameLength = 100

	// DefaultListLimit is the user-list page size when the caller omits one.
	DefaultListLimit = 20
	// MaxListLimit caps the user-list page size.
	MaxListLimit = 100
)

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,64}$`)

// Service implements the identity use cases.
type Service struct {
	repo   identity.Repository
	hasher PasswordHasher
	ids    id.Generator
	clock  clock.Clock
	audit  auditapp.Recorder
}

// New wires an identity service. audit may be nil in unit tests, in which case
// audit recording is skipped.
func New(repo identity.Repository, hasher PasswordHasher, ids id.Generator, clk clock.Clock, audit auditapp.Recorder) *Service {
	return &Service{repo: repo, hasher: hasher, ids: ids, clock: clk, audit: audit}
}

// EnsureSeedAdmin creates the fixed admin/admin123 administrator when the user
// table is empty. It is idempotent: an existing user table is never touched.
func (s *Service) EnsureSeedAdmin(ctx context.Context) (bool, error) {
	count, err := s.repo.CountUsers(ctx)
	if err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil
	}

	passwordHash, err := s.hasher.Hash(SeedAdminPassword)
	if err != nil {
		return false, err
	}
	now := s.clock.Now().UTC()
	admin := identity.User{
		ID:           s.ids.Next(),
		Username:     SeedAdminUsername,
		DisplayName:  SeedAdminDisplayName,
		PasswordHash: passwordHash,
		Role:         identity.RoleAdmin,
		Status:       identity.StatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.repo.CreateUser(ctx, admin); err != nil {
		// A concurrent startup may have seeded the same fixed administrator
		// between our count and insert. Re-check before failing.
		if errors.Is(err, identity.ErrUsernameTaken) || errors.Is(err, identity.ErrAdminExists) {
			if count, countErr := s.repo.CountUsers(ctx); countErr == nil && count > 0 {
				return false, nil
			}
		}
		return false, err
	}
	return true, nil
}

// Login verifies credentials, issues a fixed-7-day opaque token and records the
// audit trail. Failures do not reveal whether the username or the password was
// wrong.
func (s *Service) Login(ctx context.Context, username, password, requestID, ip, ua string) (Session, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return Session{}, platform.Invalid("username", "username is required")
	}
	if password == "" {
		return Session{}, platform.Invalid("password", "password is required")
	}

	user, err := s.repo.GetUserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, identity.ErrUserNotFound) {
			s.recordLoginFailed(ctx, username, requestID, ip, ua, "unknown_user")
			return Session{}, platform.Unauthenticated()
		}
		return Session{}, err
	}
	if !s.hasher.Verify(user.PasswordHash, password) {
		s.recordLoginFailed(ctx, username, requestID, ip, ua, "invalid_password")
		return Session{}, platform.Unauthenticated()
	}
	if !user.IsActive() {
		s.recordLoginFailed(ctx, username, requestID, ip, ua, "disabled")
		return Session{}, platform.Unauthenticated()
	}

	rawToken, err := identity.NewTokenValue()
	if err != nil {
		return Session{}, err
	}
	now := s.clock.Now().UTC()
	expiresAt := now.Add(identity.TokenTTL)
	token := identity.Token{
		ID:        s.ids.Next(),
		UserID:    user.ID,
		TokenHash: identity.TokenHash(rawToken),
		ExpiresAt: expiresAt,
		CreatedAt: now,
	}
	if err := s.repo.CreateToken(ctx, token); err != nil {
		return Session{}, err
	}

	s.record(ctx, auditapp.Event{
		ActorID:    &user.ID,
		Action:     auditapp.ActionLogin,
		TargetKind: "user",
		TargetID:   formatID(user.ID),
		RequestID:  requestID,
		IP:         ip,
		UserAgent:  ua,
		Metadata:   map[string]any{"username": user.Username},
	})
	return Session{Token: rawToken, ExpiresAt: expiresAt, User: user}, nil
}

// Logout deletes the current token and records the audit entry.
func (s *Service) Logout(ctx context.Context, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return platform.Unauthenticated()
	}
	hash := identity.TokenHash(token)
	stored, err := s.repo.GetTokenByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, identity.ErrTokenNotFound) {
			return platform.Unauthenticated()
		}
		return err
	}
	user, err := s.repo.GetUser(ctx, stored.UserID)
	if err != nil {
		if errors.Is(err, identity.ErrUserNotFound) {
			return platform.Unauthenticated()
		}
		return err
	}
	if err := s.repo.DeleteToken(ctx, hash); err != nil {
		return err
	}

	s.record(ctx, auditapp.Event{
		ActorID:    &user.ID,
		Action:     auditapp.ActionLogout,
		TargetKind: "user",
		TargetID:   formatID(user.ID),
		RequestID:  requestid.From(ctx),
		Metadata:   map[string]any{"username": user.Username},
	})
	return nil
}

// Authenticate resolves a raw bearer token to its active user. Expired tokens,
// unknown tokens, disabled users and missing users are all unauthenticated.
func (s *Service) Authenticate(ctx context.Context, token string) (identity.User, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return identity.User{}, platform.Unauthenticated()
	}
	stored, err := s.repo.GetTokenByHash(ctx, identity.TokenHash(token))
	if err != nil {
		if errors.Is(err, identity.ErrTokenNotFound) {
			return identity.User{}, platform.Unauthenticated()
		}
		return identity.User{}, err
	}
	now := s.clock.Now()
	if !now.Before(stored.ExpiresAt) {
		// Expired tokens are dead weight; drop the row as a best effort.
		_ = s.repo.DeleteToken(ctx, stored.TokenHash)
		return identity.User{}, platform.Unauthenticated()
	}
	user, err := s.repo.GetUser(ctx, stored.UserID)
	if err != nil {
		if errors.Is(err, identity.ErrUserNotFound) {
			return identity.User{}, platform.Unauthenticated()
		}
		return identity.User{}, err
	}
	if !user.IsActive() {
		return identity.User{}, platform.Unauthenticated()
	}
	return user, nil
}

// ChangePassword verifies the old password and replaces the hash. Other tokens
// stay valid; the specification only requires revocation on reset/disable.
func (s *Service) ChangePassword(ctx context.Context, userID int64, oldPassword, newPassword string) error {
	user, err := s.repo.GetUser(ctx, userID)
	if err != nil {
		if errors.Is(err, identity.ErrUserNotFound) {
			return platform.NotFound("user")
		}
		return err
	}
	if err := validatePassword("new_password", newPassword); err != nil {
		return err
	}
	if !s.hasher.Verify(user.PasswordHash, oldPassword) {
		return platform.Invalid("old_password", "old password is incorrect")
	}
	passwordHash, err := s.hasher.Hash(newPassword)
	if err != nil {
		return err
	}
	if err := s.repo.UpdatePassword(ctx, user.ID, passwordHash, s.clock.Now().UTC()); err != nil {
		if errors.Is(err, identity.ErrUserNotFound) {
			return platform.NotFound("user")
		}
		return err
	}

	s.record(ctx, auditapp.Event{
		ActorID:    &user.ID,
		Action:     auditapp.ActionPasswordChanged,
		TargetKind: "user",
		TargetID:   formatID(user.ID),
		RequestID:  requestid.From(ctx),
		Metadata:   map[string]any{"username": user.Username},
	})
	return nil
}

// CreateUser creates a regular (role=user) account. Only an active
// administrator may do so.
func (s *Service) CreateUser(ctx context.Context, actorID int64, username, displayName, password string) (identity.User, error) {
	actor, err := s.requireAdmin(ctx, actorID)
	if err != nil {
		return identity.User{}, err
	}
	username = strings.TrimSpace(username)
	if err := validateUsername(username); err != nil {
		return identity.User{}, err
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = username
	}
	if err := validateDisplayName(displayName); err != nil {
		return identity.User{}, err
	}
	if err := validatePassword("password", password); err != nil {
		return identity.User{}, err
	}

	// Pre-check for a friendly conflict; the unique index remains the authority.
	if _, err := s.repo.GetUserByUsername(ctx, username); err == nil {
		return identity.User{}, platform.Conflict("username already exists")
	} else if !errors.Is(err, identity.ErrUserNotFound) {
		return identity.User{}, err
	}

	passwordHash, err := s.hasher.Hash(password)
	if err != nil {
		return identity.User{}, err
	}
	now := s.clock.Now().UTC()
	user := identity.User{
		ID:           s.ids.Next(),
		Username:     username,
		DisplayName:  displayName,
		PasswordHash: passwordHash,
		Role:         identity.RoleUser,
		Status:       identity.StatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.repo.CreateUser(ctx, user); err != nil {
		if errors.Is(err, identity.ErrUsernameTaken) {
			return identity.User{}, platform.Conflict("username already exists")
		}
		return identity.User{}, err
	}

	s.record(ctx, auditapp.Event{
		ActorID:    &actor.ID,
		Action:     auditapp.ActionUserCreated,
		TargetKind: "user",
		TargetID:   formatID(user.ID),
		RequestID:  requestid.From(ctx),
		Metadata:   map[string]any{"username": user.Username, "role": string(user.Role)},
	})
	return user, nil
}

// ListUsers returns one page of users newest first and reports whether an older
// page exists. The admin guard lives on the HTTP route.
func (s *Service) ListUsers(ctx context.Context, limit int, beforeID *int64) ([]identity.User, bool, error) {
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}
	users, err := s.repo.ListUsers(ctx, limit+1, beforeID)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(users) > limit
	if hasMore {
		users = users[:limit]
	}
	return users, hasMore, nil
}

// UpdateUser changes the display name and/or status of one account. Empty
// arguments mean "keep the current value". The single administrator can never
// be disabled and disabling a user revokes all of their tokens.
func (s *Service) UpdateUser(ctx context.Context, actorID, userID int64, displayName, status string) (identity.User, error) {
	actor, err := s.requireAdmin(ctx, actorID)
	if err != nil {
		return identity.User{}, err
	}
	target, err := s.repo.GetUser(ctx, userID)
	if err != nil {
		if errors.Is(err, identity.ErrUserNotFound) {
			return identity.User{}, platform.NotFound("user")
		}
		return identity.User{}, err
	}

	newDisplayName := strings.TrimSpace(displayName)
	if newDisplayName == "" {
		newDisplayName = target.DisplayName
	} else if err := validateDisplayName(newDisplayName); err != nil {
		return identity.User{}, err
	}

	newStatus := target.Status
	if strings.TrimSpace(status) != "" {
		newStatus, err = parseStatus(status)
		if err != nil {
			return identity.User{}, err
		}
	}
	if target.IsAdmin() && newStatus == identity.StatusDisabled {
		return identity.User{}, platform.Conflict("cannot disable the administrator")
	}

	// Revoke first: if the profile update then fails, the user is at worst
	// logged out, never disabled with live tokens.
	if newStatus == identity.StatusDisabled {
		if err := s.repo.DeleteTokensForUser(ctx, userID); err != nil {
			return identity.User{}, err
		}
	}

	updated, err := s.repo.UpdateUser(ctx, userID, newDisplayName, newStatus, s.clock.Now().UTC())
	if err != nil {
		if errors.Is(err, identity.ErrUserNotFound) {
			return identity.User{}, platform.NotFound("user")
		}
		return identity.User{}, err
	}

	s.record(ctx, auditapp.Event{
		ActorID:    &actor.ID,
		Action:     auditapp.ActionUserUpdated,
		TargetKind: "user",
		TargetID:   formatID(updated.ID),
		RequestID:  requestid.From(ctx),
		Metadata: map[string]any{
			"username":     updated.Username,
			"display_name": updated.DisplayName,
			"status":       string(updated.Status),
		},
	})
	return updated, nil
}

// ResetPassword replaces another user's password and revokes every token of
// that user. Only an active administrator may do so.
func (s *Service) ResetPassword(ctx context.Context, actorID, userID int64, password string) error {
	actor, err := s.requireAdmin(ctx, actorID)
	if err != nil {
		return err
	}
	target, err := s.repo.GetUser(ctx, userID)
	if err != nil {
		if errors.Is(err, identity.ErrUserNotFound) {
			return platform.NotFound("user")
		}
		return err
	}
	if err := validatePassword("new_password", password); err != nil {
		return err
	}
	passwordHash, err := s.hasher.Hash(password)
	if err != nil {
		return err
	}
	// Revoke first so a failed password write cannot leave usable sessions.
	if err := s.repo.DeleteTokensForUser(ctx, target.ID); err != nil {
		return err
	}
	if err := s.repo.UpdatePassword(ctx, target.ID, passwordHash, s.clock.Now().UTC()); err != nil {
		if errors.Is(err, identity.ErrUserNotFound) {
			return platform.NotFound("user")
		}
		return err
	}

	s.record(ctx, auditapp.Event{
		ActorID:    &actor.ID,
		Action:     auditapp.ActionUserPasswordReset,
		TargetKind: "user",
		TargetID:   formatID(target.ID),
		RequestID:  requestid.From(ctx),
		Metadata:   map[string]any{"username": target.Username},
	})
	return nil
}

func (s *Service) requireAdmin(ctx context.Context, actorID int64) (identity.User, error) {
	if actorID <= 0 {
		return identity.User{}, platform.Forbidden()
	}
	actor, err := s.repo.GetUser(ctx, actorID)
	if err != nil {
		if errors.Is(err, identity.ErrUserNotFound) {
			return identity.User{}, platform.Forbidden()
		}
		return identity.User{}, err
	}
	if !actor.IsAdmin() || !actor.IsActive() {
		return identity.User{}, platform.Forbidden()
	}
	return actor, nil
}

func (s *Service) record(ctx context.Context, event auditapp.Event) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Record(ctx, event)
}

func (s *Service) recordLoginFailed(ctx context.Context, username, requestID, ip, ua, reason string) {
	s.record(ctx, auditapp.Event{
		Action:     auditapp.ActionLoginFailed,
		TargetKind: "user",
		RequestID:  requestID,
		IP:         ip,
		UserAgent:  ua,
		Metadata:   map[string]any{"username": username, "reason": reason},
	})
}

func validateUsername(username string) error {
	if !usernamePattern.MatchString(username) {
		return platform.Invalid("username", "username must match ^[a-zA-Z0-9_.-]{3,64}$")
	}
	return nil
}

func validatePassword(field, password string) error {
	length := utf8.RuneCountInString(password)
	if length < MinPasswordLength {
		return platform.Invalid(field, "password must be at least "+strconv.Itoa(MinPasswordLength)+" characters")
	}
	if length > MaxPasswordLength {
		return platform.Invalid(field, "password must be at most "+strconv.Itoa(MaxPasswordLength)+" characters")
	}
	return nil
}

func validateDisplayName(displayName string) error {
	if strings.TrimSpace(displayName) == "" {
		return platform.Invalid("display_name", "display_name must not be empty")
	}
	if utf8.RuneCountInString(displayName) > MaxDisplayNameLength {
		return platform.Invalid("display_name", "display_name must be at most "+strconv.Itoa(MaxDisplayNameLength)+" characters")
	}
	return nil
}

func parseStatus(raw string) (identity.Status, error) {
	switch identity.Status(strings.TrimSpace(raw)) {
	case identity.StatusActive:
		return identity.StatusActive, nil
	case identity.StatusDisabled:
		return identity.StatusDisabled, nil
	default:
		return "", platform.Invalid("status", "status must be active or disabled")
	}
}

func formatID(value int64) string {
	return strconv.FormatInt(value, 10)
}
