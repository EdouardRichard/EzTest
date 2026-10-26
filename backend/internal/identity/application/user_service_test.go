package application

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure/password"
)

var ctx = context.Background()

func TestPasswordHashAndVerify(t *testing.T) {
	h := password.NewHasher(12)
	hash, err := h.Hash("s3cret-pass")
	if err != nil {
		t.Fatal(err)
	}
	if !h.Verify(hash, "s3cret-pass") {
		t.Fatal("verify failed")
	}
	if h.Verify(hash, "wrong") {
		t.Fatal("wrong password accepted")
	}
}

// TestPasswordHashCostAndMaxLength pins the documented bcrypt cost and proves
// that passwords up to the documented 128-character limit are not truncated by
// bcrypt's 72-byte input limit.
func TestPasswordHashCostAndMaxLength(t *testing.T) {
	h := password.NewHasher(12)
	long := strings.Repeat("a", identity.MaxPasswordLength)
	hash, err := h.Hash(long)
	if err != nil {
		t.Fatalf("hash %d-character password: %v", identity.MaxPasswordLength, err)
	}
	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil {
		t.Fatalf("bcrypt cost: %v", err)
	}
	if cost != 12 {
		t.Fatalf("cost=%d want 12", cost)
	}
	if !h.Verify(hash, long) {
		t.Fatal("verify failed for maximum-length password")
	}
	// A password that differs only after bcrypt's 72-byte window must not
	// verify: truncation would be a credential-collision bug. The candidate's
	// first 72 bytes are identical to long's, so a truncating implementation
	// accepts it and only a full-length hash rejects it.
	candidate := long[:72] + "b"
	if h.Verify(hash, candidate) {
		t.Fatal("password truncated at 72 bytes")
	}
}

func TestCreateUserRejectsDuplicateAndWeak(t *testing.T) {
	repo := newFakeUserRepo()
	svc := NewUserService(repo, password.NewHasher(12))
	if _, err := svc.Create(ctx, "qa01", "1234567890"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, "qa01", "1234567890"); !errors.Is(err, identity.ErrUsernameTaken) {
		t.Fatalf("err=%v", err)
	}
	if _, err := svc.Create(ctx, "qa02", "short"); !errors.Is(err, identity.ErrWeakPassword) {
		t.Fatalf("err=%v", err)
	}
}

func TestCreateUserValidatesUsername(t *testing.T) {
	invalid := []string{
		"",
		"ab",
		"qa 01",
		"qa/01",
		"qa01!",
		strings.Repeat("a", 65),
	}
	for _, username := range invalid {
		t.Run("invalid/"+username, func(t *testing.T) {
			svc := NewUserService(newFakeUserRepo(), password.NewHasher(12))
			if _, err := svc.Create(ctx, username, "1234567890"); !errors.Is(err, identity.ErrInvalidUsername) {
				t.Fatalf("username %q: err=%v", username, err)
			}
		})
	}

	valid := []string{"qa1", "qa_01", "qa-01", "qa.01", strings.Repeat("a", 64)}
	for _, username := range valid {
		t.Run("valid/"+username, func(t *testing.T) {
			svc := NewUserService(newFakeUserRepo(), password.NewHasher(12))
			if _, err := svc.Create(ctx, username, "1234567890"); err != nil {
				t.Fatalf("username %q: %v", username, err)
			}
		})
	}
}

func TestCreateUserValidatesPassword(t *testing.T) {
	invalid := []string{
		"",
		"short",
		"123456789",
		strings.Repeat("a", identity.MaxPasswordLength+1),
	}
	for _, pw := range invalid {
		t.Run("weak", func(t *testing.T) {
			svc := NewUserService(newFakeUserRepo(), password.NewHasher(12))
			if _, err := svc.Create(ctx, "qa01", pw); !errors.Is(err, identity.ErrWeakPassword) {
				t.Fatalf("password length %d: err=%v", len(pw), err)
			}
		})
	}

	svc := NewUserService(newFakeUserRepo(), password.NewHasher(12))
	if _, err := svc.Create(ctx, "qa01", strings.Repeat("a", identity.MaxPasswordLength)); err != nil {
		t.Fatalf("maximum-length password rejected: %v", err)
	}
}

func TestCreateUserStoresHashedUser(t *testing.T) {
	repo := newFakeUserRepo()
	hasher := password.NewHasher(12)
	svc := NewUserService(repo, hasher)

	user, err := svc.Create(ctx, "qa01", "1234567890")
	if err != nil {
		t.Fatal(err)
	}
	if user.ID == 0 {
		t.Fatal("user ID not assigned")
	}
	if user.Role != identity.RoleUser {
		t.Fatalf("role=%q, Create must only create users", user.Role)
	}
	if user.Status != identity.StatusActive {
		t.Fatalf("status=%q", user.Status)
	}
	if user.PasswordHash == "" || user.PasswordHash == "1234567890" {
		t.Fatalf("password was not hashed: %q", user.PasswordHash)
	}
	if !hasher.Verify(user.PasswordHash, "1234567890") {
		t.Fatal("stored hash does not verify the password")
	}

	stored, err := repo.GetByUsername(ctx, "qa01")
	if err != nil {
		t.Fatal(err)
	}
	if stored.PasswordHash != user.PasswordHash {
		t.Fatal("repository stored a different hash than Create returned")
	}
}

func TestEnsureAdminSeedsOnce(t *testing.T) {
	repo := newFakeUserRepo()
	hasher := password.NewHasher(12)
	svc := NewUserService(repo, hasher)

	if err := svc.EnsureAdmin(ctx, "admin", "admin-pass-123"); err != nil {
		t.Fatal(err)
	}
	admin, err := repo.GetByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if admin.Role != identity.RoleAdmin {
		t.Fatalf("role=%q want admin", admin.Role)
	}
	if !hasher.Verify(admin.PasswordHash, "admin-pass-123") {
		t.Fatal("admin password hash does not verify")
	}
	if n, err := repo.CountAdmins(ctx); err != nil || n != 1 {
		t.Fatalf("admins=%d err=%v", n, err)
	}

	// A later boot with different credentials must not create or change anything.
	if err := svc.EnsureAdmin(ctx, "root", "root-pass-1234"); err != nil {
		t.Fatal(err)
	}
	admin2, err := repo.GetByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if admin2.PasswordHash != admin.PasswordHash {
		t.Fatal("existing admin password was changed")
	}
	if _, err := repo.GetByUsername(ctx, "root"); !errors.Is(err, identity.ErrUserNotFound) {
		t.Fatalf("second admin was created: err=%v", err)
	}
	if n, err := repo.CountAdmins(ctx); err != nil || n != 1 {
		t.Fatalf("admins=%d err=%v", n, err)
	}

	// Missing seed env is fine once an admin exists: restarts must not fail.
	if err := svc.EnsureAdmin(ctx, "", ""); err != nil {
		t.Fatalf("missing seed env with existing admin: %v", err)
	}
}

func TestEnsureAdminToleratesConcurrentSeed(t *testing.T) {
	svc := NewUserService(concurrentAdminRepo{newFakeUserRepo()}, password.NewHasher(12))
	if err := svc.EnsureAdmin(ctx, "admin", "admin-pass-123"); err != nil {
		t.Fatalf("concurrent admin seed should be a no-op: %v", err)
	}

	// Same race, but PostgreSQL reports the duplicate username constraint
	// instead of the single-admin index; the service must re-check for an admin.
	racing := &duplicateNameRepo{fakeUserRepo: newFakeUserRepo()}
	svc = NewUserService(racing, password.NewHasher(12))
	if err := svc.EnsureAdmin(ctx, "admin", "admin-pass-123"); err != nil {
		t.Fatalf("concurrent duplicate-name seed should be a no-op: %v", err)
	}
	if racing.counts != 2 {
		t.Fatalf("CountAdmins calls=%d want 2", racing.counts)
	}
}

func TestEnsureAdminRequiresCredentialsWhenNoAdmin(t *testing.T) {
	repo := newFakeUserRepo()
	svc := NewUserService(repo, password.NewHasher(12))

	if err := svc.EnsureAdmin(ctx, "", "admin-pass-123"); !errors.Is(err, identity.ErrInvalidUsername) {
		t.Fatalf("missing username: err=%v", err)
	}
	if err := svc.EnsureAdmin(ctx, "admin", ""); !errors.Is(err, identity.ErrWeakPassword) {
		t.Fatalf("missing password: err=%v", err)
	}
	if n, err := repo.CountAdmins(ctx); err != nil || n != 0 {
		t.Fatalf("admins=%d err=%v", n, err)
	}
}

func TestSetStatusRejectsSelfDisable(t *testing.T) {
	repo := newFakeUserRepo()
	svc := NewUserService(repo, password.NewHasher(12))
	admin, err := repo.Create(ctx, identity.CreateParams{Username: "admin", PasswordHash: "hash", Role: identity.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	user, err := repo.Create(ctx, identity.CreateParams{Username: "qa01", PasswordHash: "hash", Role: identity.RoleUser})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.SetStatus(ctx, admin.ID, admin.ID, identity.StatusDisabled); !errors.Is(err, identity.ErrLastAdmin) {
		t.Fatalf("self-disable: err=%v", err)
	}
	disabled, err := svc.SetStatus(ctx, admin.ID, user.ID, identity.StatusDisabled)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Status != identity.StatusDisabled {
		t.Fatalf("status=%q", disabled.Status)
	}
	active, err := svc.SetStatus(ctx, admin.ID, user.ID, identity.StatusActive)
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != identity.StatusActive {
		t.Fatalf("status=%q", active.Status)
	}
	if _, err := svc.SetStatus(ctx, admin.ID, user.ID, identity.Status("bogus")); !errors.Is(err, identity.ErrInvalidStatus) {
		t.Fatalf("invalid status: err=%v", err)
	}
	if _, err := svc.SetStatus(ctx, admin.ID, 999, identity.StatusDisabled); !errors.Is(err, identity.ErrUserNotFound) {
		t.Fatalf("missing user: err=%v", err)
	}
}

func TestResetPasswordRehashes(t *testing.T) {
	repo := newFakeUserRepo()
	hasher := password.NewHasher(12)
	svc := NewUserService(repo, hasher)
	user, err := repo.Create(ctx, identity.CreateParams{
		Username:     "qa01",
		PasswordHash: mustHash(t, hasher, "1234567890"),
		Role:         identity.RoleUser,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.ResetPassword(ctx, user.ID, "new-pass-1234"); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasher.Verify(updated.PasswordHash, "new-pass-1234") {
		t.Fatal("new password does not verify")
	}
	if hasher.Verify(updated.PasswordHash, "1234567890") {
		t.Fatal("old password still verifies")
	}

	if err := svc.ResetPassword(ctx, user.ID, "short"); !errors.Is(err, identity.ErrWeakPassword) {
		t.Fatalf("weak password: err=%v", err)
	}
	unchanged, err := repo.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.PasswordHash != updated.PasswordHash {
		t.Fatal("weak password reset changed the hash")
	}

	if err := svc.ResetPassword(ctx, 999, "new-pass-1234"); !errors.Is(err, identity.ErrUserNotFound) {
		t.Fatalf("missing user: err=%v", err)
	}
}

func TestGetAndListUsers(t *testing.T) {
	repo := newFakeUserRepo()
	svc := NewUserService(repo, password.NewHasher(12))
	for _, username := range []string{"qa01", "qa02", "qa03"} {
		if _, err := svc.Create(ctx, username, "1234567890"); err != nil {
			t.Fatal(err)
		}
	}

	got, err := svc.Get(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "qa02" {
		t.Fatalf("username=%q", got.Username)
	}
	if _, err := svc.Get(ctx, 99); !errors.Is(err, identity.ErrUserNotFound) {
		t.Fatalf("missing user: err=%v", err)
	}

	users, more, err := svc.List(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 3 || more {
		t.Fatalf("users=%d more=%v", len(users), more)
	}
	for i, want := range []string{"qa01", "qa02", "qa03"} {
		if users[i].Username != want {
			t.Fatalf("users[%d]=%q want %q", i, users[i].Username, want)
		}
	}

	page, more, err := svc.List(ctx, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || !more {
		t.Fatalf("page=%d more=%v", len(page), more)
	}
	rest, more, err := svc.List(ctx, 2, page[len(page)-1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || more || rest[0].Username != "qa03" {
		t.Fatalf("rest=%v more=%v", rest, more)
	}

	// A non-positive limit falls back to the service default instead of
	// leaking an empty or unrestricted page to callers.
	all, more, err := svc.List(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || more {
		t.Fatalf("default limit: users=%d more=%v", len(all), more)
	}
}

func mustHash(t *testing.T, hasher Hasher, password string) string {
	t.Helper()
	hash, err := hasher.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

// concurrentAdminRepo simulates another instance creating the admin between the
// seed's CountAdmins check and its Create call: the single-admin index rejects
// the insert with ErrAdminExists.
type concurrentAdminRepo struct {
	*fakeUserRepo
}

func (r concurrentAdminRepo) Create(context.Context, identity.CreateParams) (identity.User, error) {
	return identity.User{}, identity.ErrAdminExists
}

// duplicateNameRepo simulates the same race when PostgreSQL reports the
// duplicate username constraint instead. The second CountAdmins call (the
// seed's post-conflict re-check) reports the concurrent admin.
type duplicateNameRepo struct {
	*fakeUserRepo
	counts int
}

func (r *duplicateNameRepo) CountAdmins(context.Context) (int, error) {
	r.counts++
	if r.counts == 1 {
		return 0, nil
	}
	return 1, nil
}

func (r *duplicateNameRepo) Create(context.Context, identity.CreateParams) (identity.User, error) {
	return identity.User{}, identity.ErrUsernameTaken
}

// fakeUserRepo is an in-memory identity.Repository for service unit tests. It
// mirrors the database invariants the service relies on: unique usernames, at
// most one admin and ascending ID ordering.
type fakeUserRepo struct {
	users  map[int64]identity.User
	nextID int64
}

func newFakeUserRepo() *fakeUserRepo {
	return &fakeUserRepo{users: make(map[int64]identity.User)}
}

func (r *fakeUserRepo) Create(_ context.Context, params identity.CreateParams) (identity.User, error) {
	for _, u := range r.users {
		if u.Username == params.Username {
			return identity.User{}, identity.ErrUsernameTaken
		}
		if u.Role == identity.RoleAdmin && params.Role == identity.RoleAdmin {
			return identity.User{}, identity.ErrAdminExists
		}
	}
	r.nextID++
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	user := identity.User{
		ID:           r.nextID,
		Username:     params.Username,
		PasswordHash: params.PasswordHash,
		Role:         params.Role,
		Status:       identity.StatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	r.users[user.ID] = user
	return user, nil
}

func (r *fakeUserRepo) GetByUsername(_ context.Context, username string) (identity.User, error) {
	for _, u := range r.users {
		if u.Username == username {
			return u, nil
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

func (r *fakeUserRepo) List(_ context.Context, limit int, afterID int64) ([]identity.User, bool, error) {
	users := make([]identity.User, 0, len(r.users))
	for _, u := range r.users {
		if u.ID > afterID {
			users = append(users, u)
		}
	}
	sort.Slice(users, func(i, j int) bool { return users[i].ID < users[j].ID })
	if len(users) > limit {
		return users[:limit], true, nil
	}
	return users, false, nil
}

func (r *fakeUserRepo) UpdateStatus(_ context.Context, id int64, status identity.Status) (identity.User, error) {
	user, ok := r.users[id]
	if !ok {
		return identity.User{}, identity.ErrUserNotFound
	}
	user.Status = status
	r.users[id] = user
	return user, nil
}

func (r *fakeUserRepo) UpdatePasswordHash(_ context.Context, id int64, passwordHash string) error {
	user, ok := r.users[id]
	if !ok {
		return identity.ErrUserNotFound
	}
	user.PasswordHash = passwordHash
	r.users[id] = user
	return nil
}

func (r *fakeUserRepo) UpdateLastLogin(_ context.Context, id int64) error {
	user, ok := r.users[id]
	if !ok {
		return identity.ErrUserNotFound
	}
	now := time.Now().UTC()
	user.LastLoginAt = &now
	r.users[id] = user
	return nil
}

func (r *fakeUserRepo) CountAdmins(_ context.Context) (int, error) {
	count := 0
	for _, u := range r.users {
		if u.Role == identity.RoleAdmin {
			count++
		}
	}
	return count, nil
}
