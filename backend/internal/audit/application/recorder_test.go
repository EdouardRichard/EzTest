package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	identityapp "github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
)

var ctx = context.Background()

// fixedNow is the injectable clock value the identity services run at.
var fixedNow = time.Date(2026, 10, 27, 4, 0, 0, 0, time.UTC)

// TestAuthAndUserActionsAreAudited is the Task 5 acceptance test: every audited
// identity action writes exactly one entry, in order, and no entry carries a
// password, token or hash.
func TestAuthAndUserActionsAreAudited(t *testing.T) {
	rec := &fakeRecorder{}
	users, admin := newFakeUserRepo(t)
	tokens := newFakeTokenRepo()
	hasher := fakeHasher{}
	userSvc := identityapp.NewUserService(users, hasher, rec)
	authSvc := identityapp.NewAuthService(users, tokens, hasher, func() time.Time { return fixedNow }, rec)

	// A failed login is recorded and never includes the attempted password.
	if _, err := authSvc.Login(ctx, "admin", "wrong-pass-123"); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("wrong password: err=%v, want ErrUnauthenticated", err)
	}
	if len(rec.entries) != 1 {
		t.Fatalf("entries after failed login=%+v", rec.entries)
	}
	failed := rec.entries[0]
	if failed.Action != auditapp.ActionLoginFailed {
		t.Fatalf("action=%q want %q", failed.Action, auditapp.ActionLoginFailed)
	}
	if failed.ActorUserID != nil {
		t.Fatalf("failed login actor=%d want nil", *failed.ActorUserID)
	}
	assertNoSecrets(t, failed, "wrong-pass-123", "admin-pass-123", "hash:admin-pass-123")

	// A successful login is recorded with the account as actor and the raw
	// token is never written to the audit log.
	login, err := authSvc.Login(ctx, "admin", "admin-pass-123")
	if err != nil {
		t.Fatal(err)
	}
	loginEntry := rec.entries[len(rec.entries)-1]
	if loginEntry.Action != auditapp.ActionLoginSucceeded {
		t.Fatalf("action=%q want %q", loginEntry.Action, auditapp.ActionLoginSucceeded)
	}
	if loginEntry.ActorUserID == nil || *loginEntry.ActorUserID != admin.ID {
		t.Fatalf("login actor=%v want %d", loginEntry.ActorUserID, admin.ID)
	}
	if loginEntry.Metadata["username"] != "admin" {
		t.Fatalf("login metadata=%v want username=admin", loginEntry.Metadata)
	}
	assertNoSecrets(t, loginEntry, login.Token, identity.HashToken(login.Token), "admin-pass-123", "hash:admin-pass-123")

	// Authenticate is the per-request path, not a user action: it must not
	// produce an audit entry per request.
	beforeAuthenticate := len(rec.entries)
	if _, err := authSvc.Authenticate(ctx, login.Token); err != nil {
		t.Fatal(err)
	}
	if len(rec.entries) != beforeAuthenticate {
		t.Fatalf("Authenticate wrote %d audit entries", len(rec.entries)-beforeAuthenticate)
	}

	// Logout is recorded with the token owner as actor.
	if err := authSvc.Logout(ctx, login.Token); err != nil {
		t.Fatal(err)
	}
	logoutEntry := rec.entries[len(rec.entries)-1]
	if logoutEntry.Action != auditapp.ActionLogout {
		t.Fatalf("action=%q want %q", logoutEntry.Action, auditapp.ActionLogout)
	}
	if logoutEntry.ActorUserID == nil || *logoutEntry.ActorUserID != admin.ID {
		t.Fatalf("logout actor=%v want %d", logoutEntry.ActorUserID, admin.ID)
	}
	assertNoSecrets(t, logoutEntry, login.Token, identity.HashToken(login.Token))

	// Creating a user is recorded against the new account, without password
	// or password hash.
	created, err := userSvc.Create(ctx, "qa01", "qa-pass-1234")
	if err != nil {
		t.Fatal(err)
	}
	createdEntry := rec.entries[len(rec.entries)-1]
	if createdEntry.Action != auditapp.ActionUserCreated {
		t.Fatalf("action=%q want %q", createdEntry.Action, auditapp.ActionUserCreated)
	}
	if createdEntry.TargetType != "user" || createdEntry.TargetID != strconv.FormatInt(created.ID, 10) {
		t.Fatalf("created target=%q/%q want user/%d", createdEntry.TargetType, createdEntry.TargetID, created.ID)
	}
	if createdEntry.Metadata["username"] != "qa01" {
		t.Fatalf("created metadata=%v want username=qa01", createdEntry.Metadata)
	}
	assertNoSecrets(t, createdEntry, "qa-pass-1234", "hash:qa-pass-1234", created.PasswordHash)

	// Updating a user (status) names the acting admin and the target user.
	if _, err := userSvc.SetStatus(ctx, admin.ID, created.ID, identity.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	updatedEntry := rec.entries[len(rec.entries)-1]
	if updatedEntry.Action != auditapp.ActionUserUpdated {
		t.Fatalf("action=%q want %q", updatedEntry.Action, auditapp.ActionUserUpdated)
	}
	if updatedEntry.ActorUserID == nil || *updatedEntry.ActorUserID != admin.ID {
		t.Fatalf("updated actor=%v want %d", updatedEntry.ActorUserID, admin.ID)
	}
	if updatedEntry.TargetID != strconv.FormatInt(created.ID, 10) {
		t.Fatalf("updated target=%q want %d", updatedEntry.TargetID, created.ID)
	}
	assertNoSecrets(t, updatedEntry, "qa-pass-1234", "hash:qa-pass-1234", created.PasswordHash)

	// Resetting a password is recorded against the target only.
	if err := userSvc.ResetPassword(ctx, created.ID, "new-qa-pass-1234"); err != nil {
		t.Fatal(err)
	}
	resetEntry := rec.entries[len(rec.entries)-1]
	if resetEntry.Action != auditapp.ActionUserPasswordReset {
		t.Fatalf("action=%q want %q", resetEntry.Action, auditapp.ActionUserPasswordReset)
	}
	if resetEntry.TargetID != strconv.FormatInt(created.ID, 10) {
		t.Fatalf("reset target=%q want %d", resetEntry.TargetID, created.ID)
	}
	assertNoSecrets(t, resetEntry, "qa-pass-1234", "new-qa-pass-1234", "hash:qa-pass-1234", "hash:new-qa-pass-1234", created.PasswordHash)

	// Exactly the six documented actions, once each, in the order they
	// happened.
	wantActions := []string{
		auditapp.ActionLoginFailed,
		auditapp.ActionLoginSucceeded,
		auditapp.ActionLogout,
		auditapp.ActionUserCreated,
		auditapp.ActionUserUpdated,
		auditapp.ActionUserPasswordReset,
	}
	if len(rec.entries) != len(wantActions) {
		t.Fatalf("entries=%d want %d: %+v", len(rec.entries), len(wantActions), rec.entries)
	}
	for i, want := range wantActions {
		if rec.entries[i].Action != want {
			t.Fatalf("entries[%d].Action=%q want %q", i, rec.entries[i].Action, want)
		}
	}
}

// TestAuditFailureDoesNotFailIdentityActions pins the best-effort contract: an
// audit write failure must not turn a completed login/user change into an
// error.
func TestAuditFailureDoesNotFailIdentityActions(t *testing.T) {
	// The failure path logs by design; discard the logs so the test output
	// stays clean.
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	rec := &fakeRecorder{err: errors.New("audit database down")}
	users, admin := newFakeUserRepo(t)
	tokens := newFakeTokenRepo()
	hasher := fakeHasher{}
	userSvc := identityapp.NewUserService(users, hasher, rec)
	authSvc := identityapp.NewAuthService(users, tokens, hasher, func() time.Time { return fixedNow }, rec)

	if _, err := authSvc.Login(ctx, "admin", "admin-pass-123"); err != nil {
		t.Fatalf("login failed because audit failed: %v", err)
	}
	created, err := userSvc.Create(ctx, "qa01", "qa-pass-1234")
	if err != nil {
		t.Fatalf("create failed because audit failed: %v", err)
	}
	if _, err := userSvc.SetStatus(ctx, admin.ID, created.ID, identity.StatusDisabled); err != nil {
		t.Fatalf("update failed because audit failed: %v", err)
	}
	if err := userSvc.ResetPassword(ctx, created.ID, "new-qa-pass-1234"); err != nil {
		t.Fatalf("password reset failed because audit failed: %v", err)
	}
	if _, err := authSvc.Login(ctx, "admin", "wrong-pass-123"); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("failed login returned %v, want ErrUnauthenticated", err)
	}
}

// TestRecorderRecordAndList proves the application recorder delegates writes
// and paging to the repository unchanged.
func TestRecorderRecordAndList(t *testing.T) {
	repo := &fakeAuditRepo{}
	rec := auditapp.NewService(repo)

	entry := auditdomain.Entry{
		Action:     auditapp.ActionUserCreated,
		TargetType: "user",
		TargetID:   "7",
		Metadata:   map[string]any{"username": "qa01"},
	}
	if err := rec.Record(ctx, entry); err != nil {
		t.Fatal(err)
	}
	if len(repo.entries) != 1 || repo.entries[0].Action != auditapp.ActionUserCreated {
		t.Fatalf("repository entries=%+v", repo.entries)
	}

	entries, more, err := rec.List(ctx, 10, 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 || more {
		t.Fatalf("entries=%v more=%v", entries, more)
	}
	if repo.lastLimit != 10 || repo.lastAfterID != 42 {
		t.Fatalf("repository got limit=%d afterID=%d want 10/42", repo.lastLimit, repo.lastAfterID)
	}
}

func TestRecorderListClampsLimit(t *testing.T) {
	repo := &fakeAuditRepo{}
	rec := auditapp.NewService(repo)

	for _, tc := range []struct {
		limit int
		want  int
	}{
		{limit: 0, want: 50},
		{limit: -3, want: 50},
		{limit: 25, want: 25},
		{limit: 101, want: 100},
	} {
		if _, _, err := rec.List(ctx, tc.limit, 0); err != nil {
			t.Fatalf("limit %d: %v", tc.limit, err)
		}
		if repo.lastLimit != tc.want {
			t.Fatalf("limit %d: repository got %d want %d", tc.limit, repo.lastLimit, tc.want)
		}
	}
}

func TestNopRecorder(t *testing.T) {
	var rec auditapp.Recorder = auditapp.NopRecorder{}
	if err := rec.Record(ctx, auditdomain.Entry{Action: "test.action"}); err != nil {
		t.Fatalf("NopRecorder.Record: %v", err)
	}
	entries, more, err := rec.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("NopRecorder.List: %v", err)
	}
	if len(entries) != 0 || more {
		t.Fatalf("NopRecorder.List entries=%v more=%v", entries, more)
	}
}

// assertNoSecrets fails when the entry metadata contains any of secrets or a
// key that looks like a credential.
func assertNoSecrets(t *testing.T, entry auditdomain.Entry, secrets ...string) {
	t.Helper()
	raw, err := json.Marshal(entry.Metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if strings.Contains(string(raw), secret) {
			t.Fatalf("%s metadata leaks %q: %s", entry.Action, secret, raw)
		}
	}
	for key := range entry.Metadata {
		lower := strings.ToLower(key)
		for _, banned := range []string{"password", "passwd", "token", "hash", "secret", "api_key", "apikey", "authorization", "header", "key"} {
			if strings.Contains(lower, banned) {
				t.Fatalf("%s metadata contains sensitive key %q: %s", entry.Action, key, raw)
			}
		}
	}
}

// fakeRecorder is an in-memory audit.Recorder that captures every entry.
type fakeRecorder struct {
	entries []auditdomain.Entry
	err     error
}

func (r *fakeRecorder) Record(_ context.Context, entry auditdomain.Entry) error {
	r.entries = append(r.entries, entry)
	return r.err
}

func (r *fakeRecorder) List(context.Context, int, int64) ([]auditdomain.Entry, bool, error) {
	return r.entries, false, nil
}

var _ auditapp.Recorder = (*fakeRecorder)(nil)

// fakeAuditRepo is an in-memory auditdomain.Repository used to test the
// application recorder itself.
type fakeAuditRepo struct {
	auditdomain.Repository
	entries     []auditdomain.Entry
	lastLimit   int
	lastAfterID int64
}

func (r *fakeAuditRepo) Record(_ context.Context, entry auditdomain.Entry) error {
	r.entries = append(r.entries, entry)
	return nil
}

func (r *fakeAuditRepo) List(_ context.Context, limit int, afterID int64) ([]auditdomain.Entry, bool, error) {
	r.lastLimit = limit
	r.lastAfterID = afterID
	return nil, false, nil
}

// fakeHasher is a trivial Hasher so the audit tests do not pay bcrypt's cost.
type fakeHasher struct{}

func (fakeHasher) Hash(password string) (string, error) { return "hash:" + password, nil }
func (fakeHasher) Verify(hash, password string) bool    { return hash == "hash:"+password }

// fakeUserRepo is a minimal identity.Repository: it implements only what the
// audit wiring exercises. The embedded nil interface satisfies the remaining
// methods; calling one panics, which keeps accidental use visible in tests.
type fakeUserRepo struct {
	identity.Repository
	users  map[int64]identity.User
	nextID int64
}

func newFakeUserRepo(t *testing.T) (*fakeUserRepo, identity.User) {
	t.Helper()
	repo := &fakeUserRepo{users: make(map[int64]identity.User)}
	admin, err := repo.Create(ctx, identity.CreateParams{
		Username:     "admin",
		PasswordHash: "hash:admin-pass-123",
		Role:         identity.RoleAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	return repo, admin
}

func (r *fakeUserRepo) Create(_ context.Context, params identity.CreateParams) (identity.User, error) {
	r.nextID++
	user := identity.User{
		ID:           r.nextID,
		Username:     params.Username,
		PasswordHash: params.PasswordHash,
		Role:         params.Role,
		Status:       identity.StatusActive,
		CreatedAt:    fixedNow,
		UpdatedAt:    fixedNow,
	}
	r.users[user.ID] = user
	return user, nil
}

func (r *fakeUserRepo) GetByUsername(_ context.Context, username string) (identity.User, error) {
	for _, user := range r.users {
		if user.Username == username {
			return user, nil
		}
	}
	return identity.User{}, identity.ErrUserNotFound
}

func (r *fakeUserRepo) GetByID(_ context.Context, id int64) (identity.User, error) {
	user, ok := r.users[id]
	if !ok {
		return identity.User{}, identity.ErrUserNotFound
	}
	return user, nil
}

func (r *fakeUserRepo) UpdateStatus(_ context.Context, id int64, status identity.Status) (identity.User, error) {
	user, ok := r.users[id]
	if !ok {
		return identity.User{}, identity.ErrUserNotFound
	}
	user.Status = status
	user.UpdatedAt = fixedNow
	r.users[id] = user
	return user, nil
}

func (r *fakeUserRepo) UpdatePasswordHash(_ context.Context, id int64, passwordHash string) error {
	user, ok := r.users[id]
	if !ok {
		return identity.ErrUserNotFound
	}
	user.PasswordHash = passwordHash
	user.UpdatedAt = fixedNow
	r.users[id] = user
	return nil
}

func (r *fakeUserRepo) UpdateLastLogin(_ context.Context, id int64) error {
	user, ok := r.users[id]
	if !ok {
		return identity.ErrUserNotFound
	}
	loginAt := fixedNow
	user.LastLoginAt = &loginAt
	r.users[id] = user
	return nil
}

// fakeTokenRepo is a minimal identity.TokenRepository for service tests.
type fakeTokenRepo struct {
	identity.TokenRepository
	byHash map[string]identity.Token
	nextID int64
}

func newFakeTokenRepo() *fakeTokenRepo {
	return &fakeTokenRepo{byHash: make(map[string]identity.Token)}
}

func (r *fakeTokenRepo) Create(_ context.Context, userID int64, tokenHash string, expiresAt time.Time) error {
	r.nextID++
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
		revokedAt := fixedNow
		token.RevokedAt = &revokedAt
	}
	r.byHash[tokenHash] = token
	return nil
}

func (r *fakeTokenRepo) TouchLastSeen(context.Context, string, time.Time) error { return nil }
