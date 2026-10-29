package application

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	audit "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure/password"
)

// fixedNow is the injectable clock value every auth test runs at.
var fixedNow = time.Date(2026, 10, 27, 3, 0, 0, 0, time.UTC)

func TestLoginCreatesHashedTokenAndAuthenticates(t *testing.T) {
	users := newFakeUserRepoWith(t, "admin", "admin-pass-123", identity.RoleAdmin)
	tokens := newFakeTokenRepo()
	svc := NewAuthService(users, tokens, password.NewHasher(12), func() time.Time { return fixedNow }, audit.NopRecorder{})
	res, err := svc.Login(ctx, "admin", "admin-pass-123")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Token, "ezt_") {
		t.Fatalf("token prefix: %s", res.Token)
	}
	if tokens.lastHash == res.Token {
		t.Fatal("raw token stored")
	}
	u, err := svc.Authenticate(ctx, res.Token)
	if err != nil || u.Username != "admin" {
		t.Fatalf("u=%v err=%v", u, err)
	}

	// The stored value is the hex SHA-256 of the raw token and the expiry is
	// the fixed clock plus the documented 12-hour TTL.
	if want := identity.HashToken(res.Token); tokens.lastHash != want {
		t.Fatalf("stored hash=%q want %q", tokens.lastHash, want)
	}
	if want := fixedNow.Add(identity.TokenTTL); !res.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at=%v want %v", res.ExpiresAt, want)
	}
	if len(res.Token) != len(identity.TokenPrefix)+43 {
		t.Fatalf("raw token length=%d, want %d", len(res.Token), len(identity.TokenPrefix)+43)
	}
	random, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(res.Token, identity.TokenPrefix))
	if err != nil {
		t.Fatalf("raw token is not base64url: %v", err)
	}
	if len(random) != 32 {
		t.Fatalf("raw token entropy=%d bytes, want 32", len(random))
	}
}

func TestLoginExpiryIsUTC(t *testing.T) {
	users := newFakeUserRepoWith(t, "admin", "admin-pass-123", identity.RoleAdmin)
	tokens := newFakeTokenRepo()
	local := time.FixedZone("UTC+8", 8*60*60)
	localNow := fixedNow.In(local)
	svc := NewAuthService(users, tokens, password.NewHasher(12), func() time.Time { return localNow }, audit.NopRecorder{})

	res, err := svc.Login(ctx, "admin", "admin-pass-123")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExpiresAt.Location() != time.UTC {
		t.Fatalf("expires_at location=%v, want UTC", res.ExpiresAt.Location())
	}
	if want := localNow.Add(identity.TokenTTL); !res.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at=%v want %v", res.ExpiresAt, want)
	}
}

func TestLoginIssuesUniqueTokensAndUpdatesLastLogin(t *testing.T) {
	users := newFakeUserRepoWith(t, "admin", "admin-pass-123", identity.RoleAdmin)
	tokens := newFakeTokenRepo()
	svc := NewAuthService(users, tokens, password.NewHasher(12), func() time.Time { return fixedNow }, audit.NopRecorder{})

	first, err := svc.Login(ctx, "admin", "admin-pass-123")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Login(ctx, "admin", "admin-pass-123")
	if err != nil {
		t.Fatal(err)
	}
	if first.Token == second.Token {
		t.Fatal("two logins returned the same token")
	}

	if first.User.LastLoginAt == nil || !first.User.LastLoginAt.Equal(fixedNow) {
		t.Fatalf("login result last_login_at=%v want %v", first.User.LastLoginAt, fixedNow)
	}
	stored, err := users.GetByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastLoginAt == nil {
		t.Fatal("UpdateLastLogin was not called")
	}
}

func TestExpiredRevokedDisabledRejected(t *testing.T) {
	hasher := password.NewHasher(12)

	// seedValidToken stores a token for the user and returns the raw value.
	seedValidToken := func(t *testing.T, users *fakeUserRepo, tokens *fakeTokenRepo) string {
		t.Helper()
		admin, err := users.GetByUsername(ctx, "admin")
		if err != nil {
			t.Fatal(err)
		}
		raw := identity.TokenPrefix + "seeded-token"
		hash := identity.HashToken(raw)
		tokens.byHash[hash] = identity.Token{
			ID:        1,
			UserID:    admin.ID,
			TokenHash: hash,
			ExpiresAt: fixedNow.Add(time.Hour),
		}
		return raw
	}

	tests := []struct {
		name    string
		prepare func(t *testing.T, users *fakeUserRepo, tokens *fakeTokenRepo) string
	}{
		{
			name: "expired",
			prepare: func(t *testing.T, users *fakeUserRepo, tokens *fakeTokenRepo) string {
				raw := seedValidToken(t, users, tokens)
				token := tokens.byHash[identity.HashToken(raw)]
				token.ExpiresAt = fixedNow.Add(-time.Second)
				tokens.byHash[token.TokenHash] = token
				return raw
			},
		},
		{
			name: "expires exactly now",
			prepare: func(t *testing.T, users *fakeUserRepo, tokens *fakeTokenRepo) string {
				raw := seedValidToken(t, users, tokens)
				token := tokens.byHash[identity.HashToken(raw)]
				token.ExpiresAt = fixedNow
				tokens.byHash[token.TokenHash] = token
				return raw
			},
		},
		{
			name: "revoked",
			prepare: func(t *testing.T, users *fakeUserRepo, tokens *fakeTokenRepo) string {
				raw := seedValidToken(t, users, tokens)
				token := tokens.byHash[identity.HashToken(raw)]
				revokedAt := fixedNow.Add(-time.Minute)
				token.RevokedAt = &revokedAt
				tokens.byHash[token.TokenHash] = token
				return raw
			},
		},
		{
			name: "disabled user",
			prepare: func(t *testing.T, users *fakeUserRepo, tokens *fakeTokenRepo) string {
				raw := seedValidToken(t, users, tokens)
				admin, err := users.GetByUsername(ctx, "admin")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := users.UpdateStatus(ctx, admin.ID, identity.StatusDisabled); err != nil {
					t.Fatal(err)
				}
				return raw
			},
		},
		{
			name: "unknown token",
			prepare: func(t *testing.T, _ *fakeUserRepo, _ *fakeTokenRepo) string {
				return identity.TokenPrefix + "never-issued"
			},
		},
		{
			name: "empty token",
			prepare: func(t *testing.T, _ *fakeUserRepo, _ *fakeTokenRepo) string {
				return ""
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := newFakeUserRepoWith(t, "admin", "admin-pass-123", identity.RoleAdmin)
			tokens := newFakeTokenRepo()
			svc := NewAuthService(users, tokens, hasher, func() time.Time { return fixedNow }, audit.NopRecorder{})
			raw := tt.prepare(t, users, tokens)
			if _, err := svc.Authenticate(ctx, raw); !errors.Is(err, identity.ErrUnauthenticated) {
				t.Fatalf("Authenticate(%q) err=%v, want ErrUnauthenticated", raw, err)
			}
		})
	}
}

func TestLoginRejectsInvalidCredentials(t *testing.T) {
	hasher := password.NewHasher(12)
	tests := []struct {
		name     string
		username string
		password string
		disable  bool
	}{
		{name: "wrong password", username: "admin", password: "wrong-pass-123"},
		{name: "unknown user", username: "nobody", password: "admin-pass-123"},
		{name: "empty credentials", username: "", password: ""},
		{name: "disabled user", username: "admin", password: "admin-pass-123", disable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := newFakeUserRepoWith(t, "admin", "admin-pass-123", identity.RoleAdmin)
			if tt.disable {
				admin, err := users.GetByUsername(ctx, "admin")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := users.UpdateStatus(ctx, admin.ID, identity.StatusDisabled); err != nil {
					t.Fatal(err)
				}
			}
			tokens := newFakeTokenRepo()
			svc := NewAuthService(users, tokens, hasher, func() time.Time { return fixedNow }, audit.NopRecorder{})
			if _, err := svc.Login(ctx, tt.username, tt.password); !errors.Is(err, identity.ErrUnauthenticated) {
				t.Fatalf("Login(%q,%q) err=%v, want ErrUnauthenticated", tt.username, tt.password, err)
			}
			if len(tokens.byHash) != 0 {
				t.Fatalf("rejected login stored %d tokens", len(tokens.byHash))
			}
		})
	}
}

func TestTokenValidUntilExpiry(t *testing.T) {
	users := newFakeUserRepoWith(t, "admin", "admin-pass-123", identity.RoleAdmin)
	tokens := newFakeTokenRepo()
	now := fixedNow
	svc := NewAuthService(users, tokens, password.NewHasher(12), func() time.Time { return now }, audit.NopRecorder{})

	res, err := svc.Login(ctx, "admin", "admin-pass-123")
	if err != nil {
		t.Fatal(err)
	}

	now = res.ExpiresAt.Add(-time.Nanosecond)
	if _, err := svc.Authenticate(ctx, res.Token); err != nil {
		t.Fatalf("token rejected before expiry: %v", err)
	}
	now = res.ExpiresAt
	if _, err := svc.Authenticate(ctx, res.Token); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("token accepted at expiry: err=%v", err)
	}
	now = res.ExpiresAt.Add(time.Hour)
	if _, err := svc.Authenticate(ctx, res.Token); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("token accepted after expiry: err=%v", err)
	}
}

func TestAuthenticateTouchesLastSeenAtMostEveryFiveMinutes(t *testing.T) {
	users := newFakeUserRepoWith(t, "admin", "admin-pass-123", identity.RoleAdmin)
	tokens := newFakeTokenRepo()
	now := fixedNow
	svc := NewAuthService(users, tokens, password.NewHasher(12), func() time.Time { return now }, audit.NopRecorder{})

	res, err := svc.Login(ctx, "admin", "admin-pass-123")
	if err != nil {
		t.Fatal(err)
	}
	hash := identity.HashToken(res.Token)
	authenticate := func() {
		t.Helper()
		if _, err := svc.Authenticate(ctx, res.Token); err != nil {
			t.Fatal(err)
		}
	}

	authenticate()
	if got := tokens.touchCount[hash]; got != 1 {
		t.Fatalf("touches after the first authentication=%d want 1", got)
	}
	// The service passes a five-minute cutoff so the repository only writes when
	// the stored last_seen_at is older than the interval.
	if got, want := tokens.lastCutoff[hash], fixedNow.Add(-5*time.Minute); !got.Equal(want) {
		t.Fatalf("last-seen cutoff=%v want %v", got, want)
	}

	// Repeated authentication inside the five-minute interval must not write
	// last_seen_at again.
	for _, offset := range []time.Duration{time.Minute, 4 * time.Minute, 5 * time.Minute} {
		now = fixedNow.Add(offset)
		authenticate()
	}
	if got := tokens.touchCount[hash]; got != 1 {
		t.Fatalf("touches within five minutes=%d want 1", got)
	}

	// Once the stored value is older than five minutes, the next authentication
	// refreshes it.
	now = fixedNow.Add(5*time.Minute + time.Second)
	authenticate()
	if got := tokens.touchCount[hash]; got != 2 {
		t.Fatalf("touches after five minutes=%d want 2", got)
	}
	if got, want := tokens.lastCutoff[hash], now.Add(-5*time.Minute); !got.Equal(want) {
		t.Fatalf("last-seen cutoff=%v want %v", got, want)
	}
}

func TestAuthenticateSucceedsWhenLastSeenTouchFails(t *testing.T) {
	users := newFakeUserRepoWith(t, "admin", "admin-pass-123", identity.RoleAdmin)
	tokens := newFakeTokenRepo()
	svc := NewAuthService(users, tokens, password.NewHasher(12), func() time.Time { return fixedNow }, audit.NopRecorder{})

	res, err := svc.Login(ctx, "admin", "admin-pass-123")
	if err != nil {
		t.Fatal(err)
	}
	tokens.touchErr = errors.New("database down")

	user, err := svc.Authenticate(ctx, res.Token)
	if err != nil {
		t.Fatalf("Authenticate failed because the last-seen touch failed: %v", err)
	}
	if user.Username != "admin" {
		t.Fatalf("user=%+v", user)
	}
	if got := tokens.touchAttempts[identity.HashToken(res.Token)]; got != 1 {
		t.Fatalf("last-seen touch attempts=%d want 1 (the touch is best-effort, not skipped)", got)
	}
}

func TestLogoutRevokesCurrentToken(t *testing.T) {
	users := newFakeUserRepoWith(t, "admin", "admin-pass-123", identity.RoleAdmin)
	tokens := newFakeTokenRepo()
	svc := NewAuthService(users, tokens, password.NewHasher(12), func() time.Time { return fixedNow }, audit.NopRecorder{})

	res, err := svc.Login(ctx, "admin", "admin-pass-123")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(ctx, res.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, res.Token); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("revoked token still authenticates: err=%v", err)
	}
	stored := tokens.byHash[identity.HashToken(res.Token)]
	if stored.RevokedAt == nil {
		t.Fatal("token was not marked revoked")
	}
	// Logging out twice must not fail: tokens are simply already revoked.
	if err := svc.Logout(ctx, res.Token); err != nil {
		t.Fatalf("second logout: %v", err)
	}
	if err := svc.Logout(ctx, ""); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("empty token logout: err=%v", err)
	}
}

func TestLoginDoesNotStorePlaintextToken(t *testing.T) {
	users := newFakeUserRepoWith(t, "admin", "admin-pass-123", identity.RoleAdmin)
	tokens := newFakeTokenRepo()
	svc := NewAuthService(users, tokens, password.NewHasher(12), func() time.Time { return fixedNow }, audit.NopRecorder{})

	res, err := svc.Login(ctx, "admin", "admin-pass-123")
	if err != nil {
		t.Fatal(err)
	}
	for hash := range tokens.byHash {
		if hash == res.Token {
			t.Fatal("plaintext token stored as a key")
		}
		if strings.HasPrefix(hash, identity.TokenPrefix) {
			t.Fatalf("stored token still carries the ezt_ prefix: %s", hash)
		}
	}
}

// newFakeUserRepoWith seeds one active user with a real bcrypt hash so the
// service's password verification is exercised rather than stubbed.
func newFakeUserRepoWith(t *testing.T, username, plaintext string, role identity.Role) *fakeUserRepo {
	t.Helper()
	hash, err := password.NewHasher(12).Hash(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	repo := newFakeUserRepo()
	if _, err := repo.Create(ctx, identity.CreateParams{
		Username:     username,
		PasswordHash: hash,
		Role:         role,
	}); err != nil {
		t.Fatal(err)
	}
	return repo
}

// fakeTokenRepo is an in-memory identity.TokenRepository for service unit tests.
// It mirrors the database invariants the service relies on: hashes are unique
// and revocation is idempotent.
type fakeTokenRepo struct {
	byHash        map[string]identity.Token
	lastHash      string
	nextID        int64
	revokedAt     time.Time
	lastSeen      map[string]time.Time
	touchAttempts map[string]int
	touchCount    map[string]int
	lastCutoff    map[string]time.Time
	touchErr      error
}

func newFakeTokenRepo() *fakeTokenRepo {
	return &fakeTokenRepo{
		byHash:        make(map[string]identity.Token),
		revokedAt:     fixedNow,
		lastSeen:      make(map[string]time.Time),
		touchAttempts: make(map[string]int),
		touchCount:    make(map[string]int),
		lastCutoff:    make(map[string]time.Time),
	}
}

func (r *fakeTokenRepo) Create(_ context.Context, userID int64, tokenHash string, expiresAt time.Time) error {
	if _, ok := r.byHash[tokenHash]; ok {
		return errors.New("duplicate token hash")
	}
	r.nextID++
	r.lastHash = tokenHash
	r.byHash[tokenHash] = identity.Token{
		ID:        r.nextID,
		UserID:    userID,
		TokenHash: tokenHash,
		ExpiresAt: expiresAt,
	}
	return nil
}

func (r *fakeTokenRepo) GetByHash(_ context.Context, tokenHash string) (identity.Token, error) {
	token, ok := r.byHash[tokenHash]
	if !ok {
		return identity.Token{}, identity.ErrTokenNotFound
	}
	return token, nil
}

func (r *fakeTokenRepo) Revoke(_ context.Context, tokenHash string) error {
	token, ok := r.byHash[tokenHash]
	if !ok {
		return nil
	}
	if token.RevokedAt == nil {
		revokedAt := r.revokedAt
		token.RevokedAt = &revokedAt
	}
	r.byHash[tokenHash] = token
	return nil
}

func (r *fakeTokenRepo) RevokeAllForUser(_ context.Context, userID int64) error {
	for tokenHash, token := range r.byHash {
		if token.UserID != userID || token.RevokedAt != nil {
			continue
		}
		revokedAt := r.revokedAt
		token.RevokedAt = &revokedAt
		r.byHash[tokenHash] = token
	}
	return nil
}

// TouchLastSeen mirrors the Postgres UPDATE: it only writes when the stored
// value is missing or older than the cutoff. The simulated write time is the
// service's cutoff plus five minutes, i.e. "now" when the service passes
// now-5m. touchErr lets tests prove the touch is best-effort.
func (r *fakeTokenRepo) TouchLastSeen(_ context.Context, tokenHash string, olderThan time.Time) error {
	r.touchAttempts[tokenHash]++
	if r.touchErr != nil {
		return r.touchErr
	}
	if _, ok := r.byHash[tokenHash]; !ok {
		return nil
	}
	r.lastCutoff[tokenHash] = olderThan
	if seen, ok := r.lastSeen[tokenHash]; ok && !seen.Before(olderThan) {
		return nil
	}
	r.lastSeen[tokenHash] = olderThan.Add(5 * time.Minute)
	r.touchCount[tokenHash]++
	return nil
}
