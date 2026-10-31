package identityapp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	identityapp "github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

const testRequestID = "req_0123456789abcdef"

var testNow = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

type fixedClock struct{ at time.Time }

func (f fixedClock) Now() time.Time { return f.at }

type seqIDs struct{ next int64 }

func (s *seqIDs) Next() int64 { s.next++; return s.next }

type fakeHasher struct{}

func (fakeHasher) Hash(password string) (string, error) { return "hash:" + password, nil }

func (fakeHasher) Verify(hash, password string) bool { return hash == "hash:"+password }

type fakeAudit struct {
	events []auditapp.Event
}

func (f *fakeAudit) Record(_ context.Context, event auditapp.Event) error {
	f.events = append(f.events, event)
	return nil
}

type fakeRepo struct {
	users  map[int64]identity.User
	tokens map[string]identity.Token

	createUserErr   error
	createTokenErr  error
	updateUserErr   error
	updatePassErr   error
	deleteTokenErr  error
	deleteTokensErr error
	listErr         error

	lastListLimit  int
	lastListBefore *int64
	deleteToken    int
	deleteAll      int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		users:  map[int64]identity.User{},
		tokens: map[string]identity.Token{},
	}
}

func (f *fakeRepo) CountUsers(context.Context) (int64, error) {
	return int64(len(f.users)), nil
}

func (f *fakeRepo) CreateUser(_ context.Context, user identity.User) error {
	if f.createUserErr != nil {
		return f.createUserErr
	}
	for _, existing := range f.users {
		if strings.EqualFold(existing.Username, user.Username) {
			return identity.ErrUsernameTaken
		}
		if existing.Role == identity.RoleAdmin && user.Role == identity.RoleAdmin {
			return identity.ErrAdminExists
		}
	}
	f.users[user.ID] = user
	return nil
}

func (f *fakeRepo) GetUser(_ context.Context, id int64) (identity.User, error) {
	user, ok := f.users[id]
	if !ok {
		return identity.User{}, identity.ErrUserNotFound
	}
	return user, nil
}

func (f *fakeRepo) GetUserByUsername(_ context.Context, username string) (identity.User, error) {
	for _, user := range f.users {
		if strings.EqualFold(user.Username, username) {
			return user, nil
		}
	}
	return identity.User{}, identity.ErrUserNotFound
}

func (f *fakeRepo) ListUsers(_ context.Context, limit int, beforeID *int64) ([]identity.User, error) {
	f.lastListLimit = limit
	f.lastListBefore = beforeID
	if f.listErr != nil {
		return nil, f.listErr
	}
	list := make([]identity.User, 0, len(f.users))
	for _, user := range f.users {
		if beforeID != nil && user.ID >= *beforeID {
			continue
		}
		list = append(list, user)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID > list[j].ID })
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	return list, nil
}

func (f *fakeRepo) UpdateUser(_ context.Context, id int64, displayName string, status identity.Status, at time.Time) (identity.User, error) {
	if f.updateUserErr != nil {
		return identity.User{}, f.updateUserErr
	}
	user, ok := f.users[id]
	if !ok {
		return identity.User{}, identity.ErrUserNotFound
	}
	user.DisplayName = displayName
	user.Status = status
	user.UpdatedAt = at
	f.users[id] = user
	return user, nil
}

func (f *fakeRepo) UpdatePassword(_ context.Context, id int64, passwordHash string, at time.Time) error {
	if f.updatePassErr != nil {
		return f.updatePassErr
	}
	user, ok := f.users[id]
	if !ok {
		return identity.ErrUserNotFound
	}
	user.PasswordHash = passwordHash
	user.UpdatedAt = at
	f.users[id] = user
	return nil
}

func (f *fakeRepo) CreateToken(_ context.Context, token identity.Token) error {
	if f.createTokenErr != nil {
		return f.createTokenErr
	}
	f.tokens[token.TokenHash] = token
	return nil
}

func (f *fakeRepo) GetTokenByHash(_ context.Context, hash string) (identity.Token, error) {
	token, ok := f.tokens[hash]
	if !ok {
		return identity.Token{}, identity.ErrTokenNotFound
	}
	return token, nil
}

func (f *fakeRepo) DeleteToken(_ context.Context, hash string) error {
	if f.deleteTokenErr != nil {
		return f.deleteTokenErr
	}
	f.deleteToken++
	delete(f.tokens, hash)
	return nil
}

func (f *fakeRepo) DeleteTokensForUser(_ context.Context, userID int64) error {
	if f.deleteTokensErr != nil {
		return f.deleteTokensErr
	}
	f.deleteAll++
	for hash, token := range f.tokens {
		if token.UserID == userID {
			delete(f.tokens, hash)
		}
	}
	return nil
}

type testEnv struct {
	svc   *identityapp.Service
	repo  *fakeRepo
	audit *fakeAudit
	ids   *seqIDs
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	repo := newFakeRepo()
	audit := &fakeAudit{}
	ids := &seqIDs{}
	svc := identityapp.New(repo, fakeHasher{}, ids, fixedClock{at: testNow}, audit)
	return &testEnv{svc: svc, repo: repo, audit: audit, ids: ids}
}

// seedAdmin creates the fixed admin row directly in the fake repository.
func (e *testEnv) seedAdmin(t *testing.T) identity.User {
	t.Helper()
	user := identity.User{
		ID:           e.ids.Next(),
		Username:     identityapp.SeedAdminUsername,
		DisplayName:  identityapp.SeedAdminDisplayName,
		PasswordHash: "hash:" + identityapp.SeedAdminPassword,
		Role:         identity.RoleAdmin,
		Status:       identity.StatusActive,
		CreatedAt:    testNow,
		UpdatedAt:    testNow,
	}
	e.repo.users[user.ID] = user
	return user
}

// seedUser inserts a regular active user with the given password.
func (e *testEnv) seedUser(t *testing.T, username, password string) identity.User {
	t.Helper()
	user := identity.User{
		ID:           e.ids.Next(),
		Username:     username,
		DisplayName:  username,
		PasswordHash: "hash:" + password,
		Role:         identity.RoleUser,
		Status:       identity.StatusActive,
		CreatedAt:    testNow,
		UpdatedAt:    testNow,
	}
	e.repo.users[user.ID] = user
	return user
}

func assertAPIError(t *testing.T, err error, status int, code string) *platform.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("expected API error %d %s, got nil", status, code)
	}
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not *platform.Error", err)
	}
	if apiErr.Status != status || apiErr.Code != code {
		t.Fatalf("error = %d %s, want %d %s", apiErr.Status, apiErr.Code, status, code)
	}
	return apiErr
}

func lastEvent(t *testing.T, audit *fakeAudit) auditapp.Event {
	t.Helper()
	if len(audit.events) == 0 {
		t.Fatal("no audit event recorded")
	}
	return audit.events[len(audit.events)-1]
}

func eventWithAction(t *testing.T, audit *fakeAudit, action string) auditapp.Event {
	t.Helper()
	for i := len(audit.events) - 1; i >= 0; i-- {
		if audit.events[i].Action == action {
			return audit.events[i]
		}
	}
	t.Fatalf("audit action %q not recorded", action)
	return auditapp.Event{}
}

func TestEnsureSeedAdminCreatesAdminOnlyOnce(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	created, err := env.svc.EnsureSeedAdmin(ctx)
	if err != nil {
		t.Fatalf("EnsureSeedAdmin: %v", err)
	}
	if !created {
		t.Fatal("first EnsureSeedAdmin did not report creation")
	}
	if len(env.repo.users) != 1 {
		t.Fatalf("users = %d, want 1", len(env.repo.users))
	}
	var admin identity.User
	for _, user := range env.repo.users {
		admin = user
	}
	if admin.Username != "admin" || admin.DisplayName != "管理员" || admin.Role != identity.RoleAdmin || admin.Status != identity.StatusActive {
		t.Fatalf("admin = %#v", admin)
	}
	if admin.PasswordHash != "hash:admin123" {
		t.Fatalf("password hash = %q", admin.PasswordHash)
	}
	if !admin.CreatedAt.Equal(testNow) || !admin.UpdatedAt.Equal(testNow) {
		t.Fatalf("timestamps = %v / %v", admin.CreatedAt, admin.UpdatedAt)
	}

	created, err = env.svc.EnsureSeedAdmin(ctx)
	if err != nil {
		t.Fatalf("second EnsureSeedAdmin: %v", err)
	}
	if created {
		t.Fatal("second EnsureSeedAdmin reported creation")
	}
	if len(env.repo.users) != 1 {
		t.Fatalf("second seed changed users = %d", len(env.repo.users))
	}
}

func TestEnsureSeedAdminDoesNotOverwriteExistingUsers(t *testing.T) {
	env := newTestEnv(t)
	existing := env.seedUser(t, "qa01", "qa-password")

	created, err := env.svc.EnsureSeedAdmin(context.Background())
	if err != nil {
		t.Fatalf("EnsureSeedAdmin: %v", err)
	}
	if created {
		t.Fatal("seed created an admin although the user table is not empty")
	}
	if len(env.repo.users) != 1 {
		t.Fatalf("users = %d, want 1", len(env.repo.users))
	}
	if got := env.repo.users[existing.ID]; got.PasswordHash != existing.PasswordHash {
		t.Fatalf("seed overwrote an existing user: %#v", got)
	}
}

func TestLoginReturnsOpaqueTokenAndAudits(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedAdmin(t)
	ctx := context.Background()

	session, err := env.svc.Login(ctx, "admin", "admin123", testRequestID, "192.0.2.5", "unit-test-agent")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !strings.HasPrefix(session.Token, identity.TokenPrefix) {
		t.Fatalf("token %q lacks the ezt_ prefix", session.Token)
	}
	if len(session.Token) != len(identity.TokenPrefix)+43 {
		t.Fatalf("token length = %d, want %d", len(session.Token), len(identity.TokenPrefix)+43)
	}
	if !session.ExpiresAt.Equal(testNow.Add(identity.TokenTTL)) {
		t.Fatalf("expires_at = %v, want %v", session.ExpiresAt, testNow.Add(identity.TokenTTL))
	}
	if identity.TokenTTL != 7*24*time.Hour {
		t.Fatalf("TokenTTL = %v, want 7 days", identity.TokenTTL)
	}
	if session.User.ID != admin.ID {
		t.Fatalf("session user = %#v", session.User)
	}

	sum := sha256.Sum256([]byte(session.Token))
	wantHash := hex.EncodeToString(sum[:])
	if len(env.repo.tokens) != 1 {
		t.Fatalf("stored tokens = %d, want 1", len(env.repo.tokens))
	}
	stored, ok := env.repo.tokens[wantHash]
	if !ok {
		t.Fatalf("token not stored under sha256 hex; stored keys = %v", keysOf(env.repo.tokens))
	}
	if stored.TokenHash == session.Token {
		t.Fatal("raw token was stored instead of its hash")
	}
	if stored.UserID != admin.ID || !stored.ExpiresAt.Equal(testNow.Add(identity.TokenTTL)) || !stored.CreatedAt.Equal(testNow) {
		t.Fatalf("stored token = %#v", stored)
	}

	event := lastEvent(t, env.audit)
	if event.Action != auditapp.ActionLogin {
		t.Fatalf("audit action = %q", event.Action)
	}
	if event.ActorID == nil || *event.ActorID != admin.ID {
		t.Fatalf("audit actor = %v", event.ActorID)
	}
	if event.TargetKind != "user" || event.TargetID != fmt.Sprintf("%d", admin.ID) {
		t.Fatalf("audit target = %q/%q", event.TargetKind, event.TargetID)
	}
	if event.RequestID != testRequestID || event.IP != "192.0.2.5" || event.UserAgent != "unit-test-agent" {
		t.Fatalf("audit request metadata = %#v", event)
	}
	if event.Metadata["username"] != "admin" {
		t.Fatalf("audit metadata = %#v", event.Metadata)
	}
}

func TestLoginFailuresAreRejectedAndAudited(t *testing.T) {
	cases := []struct {
		name     string
		username string
		password string
		prepare  func(t *testing.T, env *testEnv)
	}{
		{
			name:     "unknown user",
			username: "nobody",
			password: "whatever-1",
			prepare:  func(*testing.T, *testEnv) {},
		},
		{
			name:     "wrong password",
			username: "admin",
			password: "wrong-password",
			prepare:  func(t *testing.T, env *testEnv) { env.seedAdmin(t) },
		},
		{
			name:     "disabled user",
			username: "qa01",
			password: "qa-password",
			prepare: func(t *testing.T, env *testEnv) {
				user := env.seedUser(t, "qa01", "qa-password")
				user.Status = identity.StatusDisabled
				env.repo.users[user.ID] = user
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			tc.prepare(t, env)

			_, err := env.svc.Login(context.Background(), tc.username, tc.password, testRequestID, "192.0.2.5", "ua")
			assertAPIError(t, err, 401, "unauthenticated")
			if len(env.repo.tokens) != 0 {
				t.Fatalf("failed login stored %d tokens", len(env.repo.tokens))
			}
			event := lastEvent(t, env.audit)
			if event.Action != auditapp.ActionLoginFailed {
				t.Fatalf("audit action = %q", event.Action)
			}
			if event.ActorID != nil {
				t.Fatalf("failed login actor = %v, want nil", event.ActorID)
			}
			if event.Metadata["username"] != tc.username {
				t.Fatalf("audit metadata = %#v", event.Metadata)
			}
			if _, ok := event.Metadata["password"]; ok {
				t.Fatal("audit metadata contains a password")
			}
		})
	}
}

func TestAuthenticateValidToken(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t)
	session, err := env.svc.Login(context.Background(), "admin", "admin123", testRequestID, "", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	user, err := env.svc.Authenticate(context.Background(), session.Token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if user.Username != "admin" || user.Role != identity.RoleAdmin || !user.IsActive() {
		t.Fatalf("authenticated user = %#v", user)
	}
}

func TestAuthenticateRejectsInvalidTokens(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, env *testEnv) string
	}{
		{
			name:    "unknown token",
			prepare: func(*testing.T, *testEnv) string { return "ezt_unknown" },
		},
		{
			name: "expired token",
			prepare: func(t *testing.T, env *testEnv) string {
				session, err := env.svc.Login(context.Background(), "admin", "admin123", testRequestID, "", "")
				if err != nil {
					t.Fatalf("Login: %v", err)
				}
				token := env.repo.tokens[identity.TokenHash(session.Token)]
				token.ExpiresAt = testNow
				env.repo.tokens[identity.TokenHash(session.Token)] = token
				return session.Token
			},
		},
		{
			name: "disabled user",
			prepare: func(t *testing.T, env *testEnv) string {
				session, err := env.svc.Login(context.Background(), "admin", "admin123", testRequestID, "", "")
				if err != nil {
					t.Fatalf("Login: %v", err)
				}
				admin := env.repo.users[session.User.ID]
				admin.Status = identity.StatusDisabled
				env.repo.users[admin.ID] = admin
				return session.Token
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			env.seedAdmin(t)
			token := tc.prepare(t, env)
			_, err := env.svc.Authenticate(context.Background(), token)
			assertAPIError(t, err, 401, "unauthenticated")
		})
	}
}

func TestLogoutDeletesCurrentTokenAndAudits(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedAdmin(t)
	session, err := env.svc.Login(context.Background(), "admin", "admin123", testRequestID, "", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	if err := env.svc.Logout(context.Background(), session.Token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if len(env.repo.tokens) != 0 {
		t.Fatalf("tokens after logout = %d", len(env.repo.tokens))
	}
	if _, err := env.svc.Authenticate(context.Background(), session.Token); err == nil {
		t.Fatal("logged-out token still authenticates")
	}
	event := lastEvent(t, env.audit)
	if event.Action != auditapp.ActionLogout {
		t.Fatalf("audit action = %q", event.Action)
	}
	if event.ActorID == nil || *event.ActorID != admin.ID {
		t.Fatalf("audit actor = %v", event.ActorID)
	}
}

func TestLogoutRejectsUnknownToken(t *testing.T) {
	env := newTestEnv(t)
	err := env.svc.Logout(context.Background(), "ezt_does-not-exist")
	assertAPIError(t, err, 401, "unauthenticated")
}

func TestChangePasswordRejectsWrongOldPassword(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedAdmin(t)
	before := env.repo.users[admin.ID].PasswordHash

	err := env.svc.ChangePassword(context.Background(), admin.ID, "wrong-old", "new-password-1")
	apiErr := assertAPIError(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "old_password" {
		t.Fatalf("details = %#v", apiErr.Details)
	}
	if got := env.repo.users[admin.ID].PasswordHash; got != before {
		t.Fatalf("password hash changed on rejected change: %q", got)
	}
}

func TestChangePasswordUpdatesHashAndAudits(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedAdmin(t)

	if err := env.svc.ChangePassword(context.Background(), admin.ID, "admin123", "new-password-1"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if got := env.repo.users[admin.ID].PasswordHash; got != "hash:new-password-1" {
		t.Fatalf("password hash = %q", got)
	}
	if _, err := env.svc.Login(context.Background(), "admin", "admin123", testRequestID, "", ""); err == nil {
		t.Fatal("old password still logs in")
	}
	if _, err := env.svc.Login(context.Background(), "admin", "new-password-1", testRequestID, "", ""); err != nil {
		t.Fatalf("new password login: %v", err)
	}
	event := eventWithAction(t, env.audit, auditapp.ActionPasswordChanged)
	if event.Action != auditapp.ActionPasswordChanged {
		t.Fatalf("audit action = %q", event.Action)
	}
	if event.ActorID == nil || *event.ActorID != admin.ID {
		t.Fatalf("audit actor = %v", event.ActorID)
	}
}

func TestPasswordLengthBounds(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedAdmin(t)
	ctx := context.Background()

	short := strings.Repeat("a", identityapp.MinPasswordLength-1)
	assertAPIError(t, env.svc.ChangePassword(ctx, admin.ID, "admin123", short), 400, "invalid_argument")

	long := strings.Repeat("a", identityapp.MaxPasswordLength+1)
	assertAPIError(t, env.svc.ChangePassword(ctx, admin.ID, "admin123", long), 400, "invalid_argument")

	minimum := strings.Repeat("a", identityapp.MinPasswordLength)
	if err := env.svc.ChangePassword(ctx, admin.ID, "admin123", minimum); err != nil {
		t.Fatalf("minimum password rejected: %v", err)
	}
	maximum := strings.Repeat("b", identityapp.MaxPasswordLength)
	if err := env.svc.ChangePassword(ctx, admin.ID, minimum, maximum); err != nil {
		t.Fatalf("maximum password rejected: %v", err)
	}
	if _, err := env.svc.Login(ctx, "admin", maximum, testRequestID, "", ""); err != nil {
		t.Fatalf("login with maximum password: %v", err)
	}
}

func TestCreateUserRequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	normal := env.seedUser(t, "qa01", "qa-password")

	_, err := env.svc.CreateUser(context.Background(), normal.ID, "qa02", "QA 02", "qa-password")
	assertAPIError(t, err, 403, "forbidden")

	_, err = env.svc.CreateUser(context.Background(), 999999, "qa02", "QA 02", "qa-password")
	assertAPIError(t, err, 403, "forbidden")

	for _, user := range env.repo.users {
		if user.Username == "qa02" {
			t.Fatal("non-admin created a user")
		}
	}
}

func TestCreateUserValidatesUsernameAndPassword(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedAdmin(t)
	ctx := context.Background()

	_, err := env.svc.CreateUser(ctx, admin.ID, "ab", "short name", "qa-password")
	assertAPIError(t, err, 400, "invalid_argument")

	_, err = env.svc.CreateUser(ctx, admin.ID, "bad name", "bad name", "qa-password")
	assertAPIError(t, err, 400, "invalid_argument")

	_, err = env.svc.CreateUser(ctx, admin.ID, "qa02", "QA 02", "short")
	assertAPIError(t, err, 400, "invalid_argument")
}

func TestCreateUserCreatesRegularUserAndAudits(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedAdmin(t)

	user, err := env.svc.CreateUser(context.Background(), admin.ID, "qa02", "QA 02", "qa-password")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.ID == 0 || user.Username != "qa02" || user.DisplayName != "QA 02" {
		t.Fatalf("created user = %#v", user)
	}
	if user.Role != identity.RoleUser || user.Status != identity.StatusActive {
		t.Fatalf("created role/status = %q/%q", user.Role, user.Status)
	}
	if user.PasswordHash != "hash:qa-password" {
		t.Fatalf("created password hash = %q", user.PasswordHash)
	}
	if !user.CreatedAt.Equal(testNow) || !user.UpdatedAt.Equal(testNow) {
		t.Fatalf("created timestamps = %v / %v", user.CreatedAt, user.UpdatedAt)
	}
	event := lastEvent(t, env.audit)
	if event.Action != auditapp.ActionUserCreated {
		t.Fatalf("audit action = %q", event.Action)
	}
	if event.ActorID == nil || *event.ActorID != admin.ID {
		t.Fatalf("audit actor = %v", event.ActorID)
	}
	if event.TargetID != fmt.Sprintf("%d", user.ID) {
		t.Fatalf("audit target = %q", event.TargetID)
	}
}

func TestCreateUserDuplicateUsernameConflict(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedAdmin(t)
	env.seedUser(t, "qa01", "qa-password")

	_, err := env.svc.CreateUser(context.Background(), admin.ID, "QA01", "QA 01", "qa-password")
	assertAPIError(t, err, 409, "conflict")
}

func TestCreateUserMapsRepositoryUniqueViolation(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedAdmin(t)
	env.repo.createUserErr = identity.ErrUsernameTaken

	_, err := env.svc.CreateUser(context.Background(), admin.ID, "qa02", "QA 02", "qa-password")
	assertAPIError(t, err, 409, "conflict")
}

func TestUpdateUserRequiresAdminAndChecksExistence(t *testing.T) {
	env := newTestEnv(t)
	normal := env.seedUser(t, "qa01", "qa-password")
	admin := env.seedAdmin(t)

	_, err := env.svc.UpdateUser(context.Background(), normal.ID, normal.ID, "renamed", "active")
	assertAPIError(t, err, 403, "forbidden")

	_, err = env.svc.UpdateUser(context.Background(), admin.ID, 999999, "renamed", "active")
	assertAPIError(t, err, 404, "not_found")
}

func TestUpdateUserDisablesAndRevokesAllTokens(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedAdmin(t)
	user := env.seedUser(t, "qa01", "qa-password")
	session, err := env.svc.Login(context.Background(), "qa01", "qa-password", testRequestID, "", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if len(env.repo.tokens) != 1 {
		t.Fatalf("tokens = %d", len(env.repo.tokens))
	}

	updated, err := env.svc.UpdateUser(context.Background(), admin.ID, user.ID, "QA 01 renamed", "disabled")
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if updated.DisplayName != "QA 01 renamed" || updated.Status != identity.StatusDisabled {
		t.Fatalf("updated = %#v", updated)
	}
	if !updated.UpdatedAt.Equal(testNow) {
		t.Fatalf("updated_at = %v", updated.UpdatedAt)
	}
	if len(env.repo.tokens) != 0 {
		t.Fatalf("disabling did not revoke tokens: %d left", len(env.repo.tokens))
	}
	if _, err := env.svc.Authenticate(context.Background(), session.Token); err == nil {
		t.Fatal("disabled user token still authenticates")
	}
	event := lastEvent(t, env.audit)
	if event.Action != auditapp.ActionUserUpdated {
		t.Fatalf("audit action = %q", event.Action)
	}
	if event.Metadata["username"] != "qa01" || event.Metadata["status"] != "disabled" {
		t.Fatalf("audit metadata = %#v", event.Metadata)
	}
}

func TestUpdateUserCannotDisableTheAdministrator(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedAdmin(t)

	_, err := env.svc.UpdateUser(context.Background(), admin.ID, admin.ID, "管理员", "disabled")
	assertAPIError(t, err, 409, "conflict")
	if got := env.repo.users[admin.ID]; got.Status != identity.StatusActive {
		t.Fatalf("admin was disabled: %#v", got)
	}
}

func TestUpdateUserRejectsInvalidStatus(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedAdmin(t)
	user := env.seedUser(t, "qa01", "qa-password")

	_, err := env.svc.UpdateUser(context.Background(), admin.ID, user.ID, "QA 01", "superuser")
	assertAPIError(t, err, 400, "invalid_argument")
}

func TestUpdateUserKeepsUnspecifiedFields(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedAdmin(t)
	user := env.seedUser(t, "qa01", "qa-password")

	updated, err := env.svc.UpdateUser(context.Background(), admin.ID, user.ID, "", "")
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if updated.DisplayName != "qa01" || updated.Status != identity.StatusActive {
		t.Fatalf("unspecified fields changed: %#v", updated)
	}
}

func TestResetPasswordRequiresAdminAndRevokesTokens(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedAdmin(t)
	user := env.seedUser(t, "qa01", "qa-password")
	normal := user
	normal.ID = env.ids.Next()
	normal.Username = "qa02"
	env.repo.users[normal.ID] = normal
	session, err := env.svc.Login(context.Background(), "qa01", "qa-password", testRequestID, "", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	err = env.svc.ResetPassword(context.Background(), normal.ID, user.ID, "reset-password")
	assertAPIError(t, err, 403, "forbidden")

	err = env.svc.ResetPassword(context.Background(), admin.ID, user.ID, "short")
	assertAPIError(t, err, 400, "invalid_argument")

	if err := env.svc.ResetPassword(context.Background(), admin.ID, user.ID, "reset-password"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if got := env.repo.users[user.ID].PasswordHash; got != "hash:reset-password" {
		t.Fatalf("password hash = %q", got)
	}
	if len(env.repo.tokens) != 0 {
		t.Fatalf("reset did not revoke tokens: %d left", len(env.repo.tokens))
	}
	if _, err := env.svc.Authenticate(context.Background(), session.Token); err == nil {
		t.Fatal("token survived password reset")
	}
	if _, err := env.svc.Login(context.Background(), "qa01", "reset-password", testRequestID, "", ""); err != nil {
		t.Fatalf("login with reset password: %v", err)
	}
	event := eventWithAction(t, env.audit, auditapp.ActionUserPasswordReset)
	if event.Action != auditapp.ActionUserPasswordReset {
		t.Fatalf("audit action = %q", event.Action)
	}
}

func TestResetPasswordUnknownUser(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedAdmin(t)

	err := env.svc.ResetPassword(context.Background(), admin.ID, 999999, "reset-password")
	assertAPIError(t, err, 404, "not_found")
}

func TestListUsersPaginates(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t)
	for i := 0; i < 4; i++ {
		env.seedUser(t, fmt.Sprintf("qa%02d", i), "qa-password")
	}

	users, hasMore, err := env.svc.ListUsers(context.Background(), 2, nil)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 2 || !hasMore {
		t.Fatalf("users = %d, hasMore = %v", len(users), hasMore)
	}
	if env.repo.lastListLimit != 3 {
		t.Fatalf("repo limit = %d, want 3 (limit+1)", env.repo.lastListLimit)
	}
	if users[0].ID <= users[1].ID {
		t.Fatalf("users not newest first: %v, %v", users[0].ID, users[1].ID)
	}

	cursor := users[len(users)-1].ID
	older, hasMore, err := env.svc.ListUsers(context.Background(), 10, &cursor)
	if err != nil {
		t.Fatalf("ListUsers cursor: %v", err)
	}
	if len(older) != 3 || hasMore {
		t.Fatalf("older page = %d, hasMore = %v", len(older), hasMore)
	}
	if env.repo.lastListBefore == nil || *env.repo.lastListBefore != cursor {
		t.Fatalf("repo before = %v", env.repo.lastListBefore)
	}
}

func TestListUsersClampsLimit(t *testing.T) {
	cases := []struct {
		limit int
		want  int
	}{
		{0, identityapp.DefaultListLimit + 1},
		{-1, identityapp.DefaultListLimit + 1},
		{1000, identityapp.MaxListLimit + 1},
		{7, 8},
	}
	for _, tc := range cases {
		env := newTestEnv(t)
		if _, _, err := env.svc.ListUsers(context.Background(), tc.limit, nil); err != nil {
			t.Fatalf("ListUsers(%d): %v", tc.limit, err)
		}
		if env.repo.lastListLimit != tc.want {
			t.Fatalf("ListUsers(%d) repo limit = %d, want %d", tc.limit, env.repo.lastListLimit, tc.want)
		}
	}
}

func TestAuditEventsNeverCarrySecrets(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedAdmin(t)
	ctx := context.Background()

	session, err := env.svc.Login(ctx, "admin", "admin123", testRequestID, "", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	user, err := env.svc.CreateUser(ctx, admin.ID, "qa02", "QA 02", "qa-password")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := env.svc.UpdateUser(ctx, admin.ID, user.ID, "QA 02", "disabled"); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if err := env.svc.ResetPassword(ctx, admin.ID, user.ID, "reset-password"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if err := env.svc.ChangePassword(ctx, admin.ID, "admin123", "changed-password"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if err := env.svc.Logout(ctx, session.Token); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	secrets := []string{"admin123", "qa-password", "reset-password", "changed-password", session.Token}
	for _, event := range env.audit.events {
		for key := range event.Metadata {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "password") || strings.Contains(lower, "token") || strings.Contains(lower, "hash") || strings.Contains(lower, "secret") {
				t.Fatalf("audit event %q metadata key %q looks sensitive", event.Action, key)
			}
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		for _, secret := range secrets {
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("audit event %q leaks %q", event.Action, secret)
			}
		}
	}
}

func keysOf(m map[string]identity.Token) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}
