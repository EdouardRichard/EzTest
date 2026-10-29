package application

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	audit "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
)

// LoginResult is a successful login: the raw bearer token (returned to the
// client exactly once), its expiry and the authenticated user.
type LoginResult struct {
	Token     string
	ExpiresAt time.Time
	User      identity.User
}

// AuthService implements login, token authentication and logout on top of the
// user and token repository ports.
type AuthService struct {
	users    identity.Repository
	tokens   identity.TokenRepository
	hasher   Hasher
	now      func() time.Time
	recorder audit.Recorder
}

// NewAuthService wires the auth service to its repositories, password hasher,
// clock and audit recorder. A nil clock means time.Now.
func NewAuthService(users identity.Repository, tokens identity.TokenRepository, hasher Hasher, now func() time.Time, recorder audit.Recorder) *AuthService {
	if now == nil {
		now = time.Now
	}
	return &AuthService{users: users, tokens: tokens, hasher: hasher, now: now, recorder: recorder}
}

// Login verifies the credentials, issues a 12-hour bearer token and records the
// login time. Unknown users, disabled users and wrong passwords all return
// ErrUnauthenticated so callers cannot enumerate accounts.
func (s *AuthService) Login(ctx context.Context, username, password string) (LoginResult, error) {
	user, err := s.users.GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, identity.ErrUserNotFound) {
			s.recordLoginFailed(ctx, username)
			return LoginResult{}, identity.ErrUnauthenticated
		}
		return LoginResult{}, fmt.Errorf("identity: login: %w", err)
	}
	// Verify before checking the status so disabled accounts do not answer
	// measurably faster than wrong passwords.
	passwordOK := s.hasher.Verify(user.PasswordHash, password)
	if !passwordOK || user.Status != identity.StatusActive {
		s.recordLoginFailed(ctx, username)
		return LoginResult{}, identity.ErrUnauthenticated
	}

	rawToken, tokenHash, err := identity.GenerateToken()
	if err != nil {
		return LoginResult{}, err
	}
	// Normalize to UTC so response timestamps are RFC3339 UTC regardless of
	// the process time zone.
	now := s.now().UTC()
	expiresAt := now.Add(identity.TokenTTL)
	if err := s.tokens.Create(ctx, user.ID, tokenHash, expiresAt); err != nil {
		return LoginResult{}, fmt.Errorf("identity: store login token: %w", err)
	}
	if err := s.users.UpdateLastLogin(ctx, user.ID); err != nil {
		return LoginResult{}, fmt.Errorf("identity: update last login: %w", err)
	}
	user.LastLoginAt = &now
	recordAudit(ctx, s.recorder, auditdomain.Entry{
		Action:      audit.ActionLoginSucceeded,
		ActorUserID: &user.ID,
		TargetType:  "user",
		TargetID:    strconv.FormatInt(user.ID, 10),
		Metadata:    map[string]any{"username": user.Username},
	})
	return LoginResult{Token: rawToken, ExpiresAt: expiresAt, User: user}, nil
}

// recordLoginFailed records a rejected login. The attempted password never
// reaches the entry: only the (already validated) username is kept, so the log
// stays useful without storing a credential. The actor is nil because the
// caller is not authenticated.
func (s *AuthService) recordLoginFailed(ctx context.Context, username string) {
	recordAudit(ctx, s.recorder, auditdomain.Entry{
		Action:     audit.ActionLoginFailed,
		TargetType: "user",
		Metadata:   map[string]any{"username": username},
	})
}

// Authenticate resolves a raw bearer token to its user. Unknown, revoked or
// expired tokens and disabled users all return ErrUnauthenticated. A successful
// authentication best-effort refreshes the token's last-seen timestamp, at most
// once per identity.TokenSeenInterval.
func (s *AuthService) Authenticate(ctx context.Context, rawToken string) (identity.User, error) {
	if rawToken == "" {
		return identity.User{}, identity.ErrUnauthenticated
	}
	token, err := s.tokens.GetByHash(ctx, identity.HashToken(rawToken))
	if err != nil {
		if errors.Is(err, identity.ErrTokenNotFound) {
			return identity.User{}, identity.ErrUnauthenticated
		}
		return identity.User{}, fmt.Errorf("identity: authenticate: %w", err)
	}
	now := s.now()
	if token.RevokedAt != nil || !now.Before(token.ExpiresAt) {
		return identity.User{}, identity.ErrUnauthenticated
	}
	user, err := s.users.GetByID(ctx, token.UserID)
	if err != nil {
		if errors.Is(err, identity.ErrUserNotFound) {
			return identity.User{}, identity.ErrUnauthenticated
		}
		return identity.User{}, fmt.Errorf("identity: authenticate: %w", err)
	}
	if user.Status != identity.StatusActive {
		return identity.User{}, identity.ErrUnauthenticated
	}
	// Best-effort: record token usage at most every TokenSeenInterval. A failed
	// touch must not fail an otherwise valid authentication; the next request
	// retries it.
	_ = s.tokens.TouchLastSeen(ctx, token.TokenHash, now.Add(-identity.TokenSeenInterval))
	return user, nil
}

// Logout revokes the token presented by the client. Revoking an already revoked
// token is a no-op, so repeated logout calls succeed.
func (s *AuthService) Logout(ctx context.Context, rawToken string) error {
	if rawToken == "" {
		return identity.ErrUnauthenticated
	}
	tokenHash := identity.HashToken(rawToken)
	// Resolve the owner before revoking so the audit entry names who logged
	// out. An unknown token stays a no-op: Revoke is idempotent.
	var actorUserID *int64
	if token, err := s.tokens.GetByHash(ctx, tokenHash); err == nil {
		actorUserID = &token.UserID
	}
	if err := s.tokens.Revoke(ctx, tokenHash); err != nil {
		return fmt.Errorf("identity: logout: %w", err)
	}
	entry := auditdomain.Entry{
		Action:      audit.ActionLogout,
		ActorUserID: actorUserID,
		TargetType:  "user",
	}
	if actorUserID != nil {
		entry.TargetID = strconv.FormatInt(*actorUserID, 10)
	}
	recordAudit(ctx, s.recorder, entry)
	return nil
}
