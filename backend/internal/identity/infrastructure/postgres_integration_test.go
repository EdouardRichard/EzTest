//go:build integration

package identityinfra_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

func newUser(id int64, username string, role identity.Role, at time.Time) identity.User {
	return identity.User{
		ID:           id,
		Username:     username,
		DisplayName:  username,
		PasswordHash: "hash:" + username,
		Role:         role,
		Status:       identity.StatusActive,
		CreatedAt:    at,
		UpdatedAt:    at,
	}
}

func TestRepositoryUserCRUDAndConstraints(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := identityinfra.NewRepository(pool)
	ctx := context.Background()
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

	count, err := repo.CountUsers(ctx)
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	if count != 0 {
		t.Fatalf("initial CountUsers = %d, want 0", count)
	}

	admin := newUser(1001, "admin", identity.RoleAdmin, at)
	if err := repo.CreateUser(ctx, admin); err != nil {
		t.Fatalf("CreateUser admin: %v", err)
	}
	if err := repo.CreateUser(ctx, newUser(1002, "admin2", identity.RoleAdmin, at)); !errors.Is(err, identity.ErrAdminExists) {
		t.Fatalf("second admin error = %v, want ErrAdminExists", err)
	}

	loaded, err := repo.GetUser(ctx, admin.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if loaded.Username != "admin" || loaded.Role != identity.RoleAdmin || loaded.Status != identity.StatusActive {
		t.Fatalf("loaded = %#v", loaded)
	}
	if !loaded.CreatedAt.Equal(at) || !loaded.UpdatedAt.Equal(at) {
		t.Fatalf("timestamps = %v / %v", loaded.CreatedAt, loaded.UpdatedAt)
	}
	if got, err := repo.GetUserByUsername(ctx, "ADMIN"); err != nil || got.ID != admin.ID {
		t.Fatalf("GetUserByUsername case-insensitive = %#v, %v", got, err)
	}
	if _, err := repo.GetUser(ctx, 999999); !errors.Is(err, identity.ErrUserNotFound) {
		t.Fatalf("missing GetUser error = %v", err)
	}
	if _, err := repo.GetUserByUsername(ctx, "nobody"); !errors.Is(err, identity.ErrUserNotFound) {
		t.Fatalf("missing GetUserByUsername error = %v", err)
	}

	for i := int64(0); i < 4; i++ {
		user := newUser(1003+i, "qa0"+string(rune('0'+i)), identity.RoleUser, at)
		if err := repo.CreateUser(ctx, user); err != nil {
			t.Fatalf("CreateUser %s: %v", user.Username, err)
		}
	}
	if err := repo.CreateUser(ctx, newUser(1100, "QA01", identity.RoleUser, at)); !errors.Is(err, identity.ErrUsernameTaken) {
		t.Fatalf("duplicate username error = %v, want ErrUsernameTaken", err)
	}

	count, err = repo.CountUsers(ctx)
	if err != nil {
		t.Fatalf("CountUsers after create: %v", err)
	}
	if count != 5 {
		t.Fatalf("CountUsers = %d, want 5", count)
	}

	page, err := repo.ListUsers(ctx, 2, nil)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(page) != 2 || page[0].ID != 1006 || page[1].ID != 1005 {
		t.Fatalf("first page = %#v", page)
	}
	cursor := int64(1005)
	older, err := repo.ListUsers(ctx, 10, &cursor)
	if err != nil {
		t.Fatalf("ListUsers cursor: %v", err)
	}
	if len(older) != 3 || older[0].ID != 1004 || older[2].ID != 1001 {
		t.Fatalf("cursor page = %#v", older)
	}

	updatedAt := at.Add(time.Hour)
	updated, err := repo.UpdateUser(ctx, 1003, "QA 03 renamed", identity.StatusDisabled, updatedAt)
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if updated.DisplayName != "QA 03 renamed" || updated.Status != identity.StatusDisabled || !updated.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("updated = %#v", updated)
	}
	if _, err := repo.UpdateUser(ctx, 999999, "x", identity.StatusActive, updatedAt); !errors.Is(err, identity.ErrUserNotFound) {
		t.Fatalf("missing UpdateUser error = %v", err)
	}

	if err := repo.UpdatePassword(ctx, 1003, "new-password-hash", updatedAt); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}
	reloaded, err := repo.GetUser(ctx, 1003)
	if err != nil {
		t.Fatalf("GetUser after password: %v", err)
	}
	if reloaded.PasswordHash != "new-password-hash" || !reloaded.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("reloaded = %#v", reloaded)
	}
	if err := repo.UpdatePassword(ctx, 999999, "x", updatedAt); !errors.Is(err, identity.ErrUserNotFound) {
		t.Fatalf("missing UpdatePassword error = %v", err)
	}
}

func TestRepositoryTokenLifecycleAndRevocation(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := identityinfra.NewRepository(pool)
	ctx := context.Background()
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	user := newUser(2001, "qa01", identity.RoleUser, at)
	if err := repo.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	tokenA := identity.Token{ID: 5001, UserID: user.ID, TokenHash: "hash-a", ExpiresAt: at.Add(7 * 24 * time.Hour), CreatedAt: at}
	if err := repo.CreateToken(ctx, tokenA); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if err := repo.CreateToken(ctx, tokenA); err == nil {
		t.Fatal("duplicate token hash accepted")
	}
	loaded, err := repo.GetTokenByHash(ctx, "hash-a")
	if err != nil {
		t.Fatalf("GetTokenByHash: %v", err)
	}
	if loaded.UserID != user.ID || loaded.TokenHash != "hash-a" || !loaded.ExpiresAt.Equal(tokenA.ExpiresAt) {
		t.Fatalf("loaded token = %#v", loaded)
	}
	if _, err := repo.GetTokenByHash(ctx, "missing"); !errors.Is(err, identity.ErrTokenNotFound) {
		t.Fatalf("missing token error = %v", err)
	}

	tokenB := identity.Token{ID: 5002, UserID: user.ID, TokenHash: "hash-b", ExpiresAt: at.Add(7 * 24 * time.Hour), CreatedAt: at}
	if err := repo.CreateToken(ctx, tokenB); err != nil {
		t.Fatalf("CreateToken B: %v", err)
	}
	if err := repo.DeleteToken(ctx, "hash-a"); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}
	if _, err := repo.GetTokenByHash(ctx, "hash-a"); !errors.Is(err, identity.ErrTokenNotFound) {
		t.Fatalf("deleted token error = %v", err)
	}
	if err := repo.DeleteToken(ctx, "hash-a"); err != nil {
		t.Fatalf("idempotent DeleteToken: %v", err)
	}

	if err := repo.DeleteTokensForUser(ctx, user.ID); err != nil {
		t.Fatalf("DeleteTokensForUser: %v", err)
	}
	if _, err := repo.GetTokenByHash(ctx, "hash-b"); !errors.Is(err, identity.ErrTokenNotFound) {
		t.Fatalf("revoked token error = %v", err)
	}

	// Deleting the account cascades to its tokens.
	tokenC := identity.Token{ID: 5003, UserID: user.ID, TokenHash: "hash-c", ExpiresAt: at.Add(time.Hour), CreatedAt: at}
	if err := repo.CreateToken(ctx, tokenC); err != nil {
		t.Fatalf("CreateToken C: %v", err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM app_user WHERE id = $1", user.ID); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if _, err := repo.GetTokenByHash(ctx, "hash-c"); !errors.Is(err, identity.ErrTokenNotFound) {
		t.Fatalf("cascaded token error = %v", err)
	}
}
