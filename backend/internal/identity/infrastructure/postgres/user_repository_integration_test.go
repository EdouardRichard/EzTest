//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure/password"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

var ctx = context.Background()

func TestUserRepositoryAndAdminSeed(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewUserRepository(pool)
	hash, _ := password.NewHasher(12).Hash("admin-pass-123")
	if _, err := repo.Create(ctx, identity.CreateParams{Username: "admin", PasswordHash: hash, Role: identity.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Create(ctx, identity.CreateParams{Username: "admin2", PasswordHash: hash, Role: identity.RoleAdmin}); err == nil {
		t.Fatal("expected single-admin index violation")
	} else if !errors.Is(err, identity.ErrAdminExists) {
		t.Fatalf("second admin: err=%v, want ErrAdminExists", err)
	}
	users, more, err := repo.List(ctx, 10, 0)
	if err != nil || len(users) != 1 || more {
		t.Fatalf("users=%v more=%v err=%v", users, more, err)
	}
}

func TestUserRepositoryCRUDAndPagination(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewUserRepository(pool)
	hasher := password.NewHasher(12)
	hash := mustHash(t, hasher, "qa-pass-1234")

	user, err := repo.Create(ctx, identity.CreateParams{Username: "qa01", PasswordHash: hash, Role: identity.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	if user.ID == 0 || user.Username != "qa01" || user.Role != identity.RoleUser || user.Status != identity.StatusActive {
		t.Fatalf("created user=%+v", user)
	}
	if user.CreatedAt.IsZero() || user.UpdatedAt.IsZero() {
		t.Fatalf("timestamps=%v/%v", user.CreatedAt, user.UpdatedAt)
	}
	if user.LastLoginAt != nil {
		t.Fatalf("last_login_at=%v want nil", user.LastLoginAt)
	}
	if n, err := repo.CountAdmins(ctx); err != nil || n != 0 {
		t.Fatalf("admins=%d err=%v", n, err)
	}

	byName, err := repo.GetByUsername(ctx, "qa01")
	if err != nil {
		t.Fatal(err)
	}
	if byName.ID != user.ID || byName.PasswordHash != hash {
		t.Fatalf("GetByUsername=%+v", byName)
	}
	byID, err := repo.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if byID.Username != "qa01" {
		t.Fatalf("GetByID=%+v", byID)
	}
	if _, err := repo.GetByUsername(ctx, "missing"); !errors.Is(err, identity.ErrUserNotFound) {
		t.Fatalf("missing username: err=%v", err)
	}
	if _, err := repo.GetByID(ctx, 99999); !errors.Is(err, identity.ErrUserNotFound) {
		t.Fatalf("missing id: err=%v", err)
	}

	if err := repo.UpdateLastLogin(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	loggedIn, err := repo.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loggedIn.LastLoginAt == nil {
		t.Fatal("UpdateLastLogin did not set last_login_at")
	}
	if err := repo.UpdateLastLogin(ctx, 99999); !errors.Is(err, identity.ErrUserNotFound) {
		t.Fatalf("missing last-login target: err=%v", err)
	}

	if _, err := repo.Create(ctx, identity.CreateParams{Username: "qa01", PasswordHash: hash, Role: identity.RoleUser}); !errors.Is(err, identity.ErrUsernameTaken) {
		t.Fatalf("duplicate username: err=%v", err)
	}

	disabled, err := repo.UpdateStatus(ctx, user.ID, identity.StatusDisabled)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Status != identity.StatusDisabled {
		t.Fatalf("UpdateStatus=%+v", disabled)
	}
	if _, err := repo.UpdateStatus(ctx, 99999, identity.StatusDisabled); !errors.Is(err, identity.ErrUserNotFound) {
		t.Fatalf("missing status target: err=%v", err)
	}

	newHash := mustHash(t, hasher, "new-pass-1234")
	if err := repo.UpdatePasswordHash(ctx, user.ID, newHash); err != nil {
		t.Fatal(err)
	}
	reloaded, err := repo.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PasswordHash != newHash {
		t.Fatal("password hash was not updated")
	}
	if err := repo.UpdatePasswordHash(ctx, 99999, newHash); !errors.Is(err, identity.ErrUserNotFound) {
		t.Fatalf("missing password target: err=%v", err)
	}

	for _, username := range []string{"qa02", "qa03"} {
		if _, err := repo.Create(ctx, identity.CreateParams{Username: username, PasswordHash: hash, Role: identity.RoleUser}); err != nil {
			t.Fatal(err)
		}
	}
	first, more, err := repo.List(ctx, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || !more {
		t.Fatalf("first page=%d more=%v", len(first), more)
	}
	rest, more, err := repo.List(ctx, 2, first[len(first)-1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || more || rest[0].Username != "qa03" {
		t.Fatalf("rest=%v more=%v", rest, more)
	}
}

func TestEnsureAdminSeedsOnceAgainstDatabase(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewUserRepository(pool)
	svc := application.NewUserService(repo, password.NewHasher(12))

	if err := svc.EnsureAdmin(ctx, "admin", "admin-pass-123"); err != nil {
		t.Fatal(err)
	}
	// A restart with the same or different seed values must not create a second
	// admin nor change the existing credentials.
	if err := svc.EnsureAdmin(ctx, "admin", "admin-pass-123"); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureAdmin(ctx, "", ""); err != nil {
		t.Fatalf("missing seed env with existing admin: %v", err)
	}
	admins, err := repo.CountAdmins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if admins != 1 {
		t.Fatalf("admins=%d", admins)
	}
	admin, err := repo.GetByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if admin.Role != identity.RoleAdmin || admin.Status != identity.StatusActive {
		t.Fatalf("admin=%+v", admin)
	}
	if !password.NewHasher(12).Verify(admin.PasswordHash, "admin-pass-123") {
		t.Fatal("admin password does not verify")
	}
}

func mustHash(t *testing.T, hasher password.Hasher, plaintext string) string {
	t.Helper()
	hash, err := hasher.Hash(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
