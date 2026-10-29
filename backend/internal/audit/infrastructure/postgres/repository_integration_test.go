//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	audit "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	auditpostgres "github.com/EdouardRichard/EzTest/backend/internal/audit/infrastructure/postgres"
	identityapp "github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure/password"
	identitypostgres "github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

var ctx = context.Background()

// TestAuditRepositoryRecordAndList proves the audit table stores every column,
// lists newest first by ID cursor, and nulls the actor when its user is deleted
// (ON DELETE SET NULL).
func TestAuditRepositoryRecordAndList(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	users := identitypostgres.NewUserRepository(pool)
	admin, err := users.Create(ctx, identity.CreateParams{
		Username:     "admin",
		PasswordHash: "hash",
		Role:         identity.RoleAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}

	repo := auditpostgres.NewRepository(pool)
	created := []audit.Entry{
		{
			Action:     "audit.first",
			TargetType: "user",
			TargetID:   "1",
			RequestID:  "req_1",
			IP:         "127.0.0.1",
			UserAgent:  "integration-test",
		},
		{
			ActorUserID: &admin.ID,
			Action:      "audit.second",
			TargetType:  "user",
			TargetID:    "2",
			RequestID:   "req_2",
			IP:          "127.0.0.2",
			UserAgent:   "integration-test",
			Metadata:    map[string]any{"username": "qa01", "status": "active"},
		},
		{
			ActorUserID: &admin.ID,
			Action:      "audit.third",
		},
	}
	for _, entry := range created {
		if err := repo.Record(ctx, entry); err != nil {
			t.Fatalf("record %s: %v", entry.Action, err)
		}
	}

	page, more, err := repo.List(ctx, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || !more {
		t.Fatalf("first page=%d more=%v", len(page), more)
	}
	if page[0].Action != "audit.third" || page[1].Action != "audit.second" {
		t.Fatalf("first page actions=%q,%q want audit.third,audit.second", page[0].Action, page[1].Action)
	}
	if page[0].ID <= page[1].ID {
		t.Fatalf("IDs are not descending: %d <= %d", page[0].ID, page[1].ID)
	}
	if page[1].ActorUserID == nil || *page[1].ActorUserID != admin.ID {
		t.Fatalf("actor=%v want %d", page[1].ActorUserID, admin.ID)
	}
	if page[1].TargetType != "user" || page[1].TargetID != "2" ||
		page[1].RequestID != "req_2" || page[1].IP != "127.0.0.2" ||
		page[1].UserAgent != "integration-test" {
		t.Fatalf("stored columns=%+v", page[1])
	}
	if page[1].Metadata["username"] != "qa01" || page[1].Metadata["status"] != "active" {
		t.Fatalf("metadata=%v", page[1].Metadata)
	}
	if page[0].CreatedAt.IsZero() {
		t.Fatal("created_at was not set")
	}
	// A missing metadata object is stored and returned as an empty object, not
	// a nil map.
	if page[0].Metadata == nil || len(page[0].Metadata) != 0 {
		t.Fatalf("empty metadata=%v want empty map", page[0].Metadata)
	}

	rest, more, err := repo.List(ctx, 2, page[len(page)-1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || more || rest[0].Action != "audit.first" {
		t.Fatalf("second page=%+v more=%v", rest, more)
	}
	if rest[0].ActorUserID != nil {
		t.Fatalf("first entry actor=%v want nil", rest[0].ActorUserID)
	}

	if _, _, err := repo.List(ctx, 0, 0); err == nil {
		t.Fatal("List accepted a non-positive limit")
	}

	// Deleting the acting user must keep the entries and null their actor.
	if _, err := pool.Exec(ctx, `DELETE FROM user_account WHERE id = $1`, admin.ID); err != nil {
		t.Fatal(err)
	}
	all, _, err := repo.List(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("entries after actor delete=%d want 3", len(all))
	}
	for _, entry := range all {
		if entry.ActorUserID != nil {
			t.Fatalf("%s actor=%d want nil after ON DELETE SET NULL", entry.Action, *entry.ActorUserID)
		}
	}
}

// TestIdentityActionsAreAudited wires the identity services to the real
// PostgreSQL recorder and proves the six documented actions land in audit_log
// with no credentials in metadata. Authenticate must stay un-audited.
func TestIdentityActionsAreAudited(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	hasher := password.NewHasher(12)
	users := identitypostgres.NewUserRepository(pool)
	tokens := identitypostgres.NewTokenRepository(pool)
	recorder := auditapp.NewService(auditpostgres.NewRepository(pool))

	admin, err := users.Create(ctx, identity.CreateParams{
		Username:     "admin",
		PasswordHash: mustHash(t, hasher, "admin-pass-123"),
		Role:         identity.RoleAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	userSvc := identityapp.NewUserService(users, hasher, recorder, tokens)
	authSvc := identityapp.NewAuthService(users, tokens, hasher, time.Now, recorder)

	// Failed and successful login.
	if _, err := authSvc.Login(ctx, "admin", "wrong-pass-123"); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("wrong password: err=%v", err)
	}
	login, err := authSvc.Login(ctx, "admin", "admin-pass-123")
	if err != nil {
		t.Fatal(err)
	}
	// Authenticate is per-request and must not write audit entries.
	if _, err := authSvc.Authenticate(ctx, login.Token); err != nil {
		t.Fatal(err)
	}

	// Create, update and password-reset a user.
	created, err := userSvc.Create(ctx, "qa01", "qa-pass-1234")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := userSvc.SetStatus(ctx, admin.ID, created.ID, identity.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	if err := userSvc.ResetPassword(ctx, created.ID, "new-qa-pass-1234"); err != nil {
		t.Fatal(err)
	}
	if err := authSvc.Logout(ctx, login.Token); err != nil {
		t.Fatal(err)
	}

	entries, more, err := recorder.List(ctx, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if more {
		t.Fatal("unexpected second audit page")
	}
	if len(entries) != 6 {
		t.Fatalf("audit entries=%d want 6: %+v", len(entries), entries)
	}

	byAction := make(map[string]audit.Entry, len(entries))
	for _, entry := range entries {
		if entry.CreatedAt.IsZero() {
			t.Fatalf("%s created_at is zero", entry.Action)
		}
		if _, ok := byAction[entry.Action]; ok {
			t.Fatalf("duplicate action %q", entry.Action)
		}
		byAction[entry.Action] = entry
	}
	for _, action := range []string{
		auditapp.ActionLoginFailed,
		auditapp.ActionLoginSucceeded,
		auditapp.ActionLogout,
		auditapp.ActionUserCreated,
		auditapp.ActionUserUpdated,
		auditapp.ActionUserPasswordReset,
	} {
		if _, ok := byAction[action]; !ok {
			t.Fatalf("missing audit action %q: %+v", action, entries)
		}
	}

	if entry := byAction[auditapp.ActionLoginSucceeded]; entry.ActorUserID == nil || *entry.ActorUserID != admin.ID {
		t.Fatalf("login_succeeded actor=%v want %d", entry.ActorUserID, admin.ID)
	}
	if entry := byAction[auditapp.ActionLogout]; entry.ActorUserID == nil || *entry.ActorUserID != admin.ID {
		t.Fatalf("logout actor=%v want %d", entry.ActorUserID, admin.ID)
	}
	if entry := byAction[auditapp.ActionUserUpdated]; entry.ActorUserID == nil || *entry.ActorUserID != admin.ID {
		t.Fatalf("user.updated actor=%v want %d", entry.ActorUserID, admin.ID)
	}
	if entry := byAction[auditapp.ActionLoginFailed]; entry.ActorUserID != nil {
		t.Fatalf("login_failed actor=%v want nil", entry.ActorUserID)
	}
	for _, action := range []string{auditapp.ActionUserCreated, auditapp.ActionUserUpdated, auditapp.ActionUserPasswordReset} {
		if entry := byAction[action]; entry.TargetType != "user" || entry.TargetID != strconv.FormatInt(created.ID, 10) {
			t.Fatalf("%s target=%q/%q want user/%d", action, entry.TargetType, entry.TargetID, created.ID)
		}
	}
	if entry := byAction[auditapp.ActionUserCreated]; entry.Metadata["username"] != "qa01" {
		t.Fatalf("user.created metadata=%v want username=qa01", entry.Metadata)
	}

	// No password, raw token or hash may reach metadata (or any other stored
	// text field).
	resetUser, err := users.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	secrets := []string{
		"admin-pass-123",
		"wrong-pass-123",
		"qa-pass-1234",
		"new-qa-pass-1234",
		login.Token,
		identity.HashToken(login.Token),
		admin.PasswordHash,
		resetUser.PasswordHash,
	}
	for _, entry := range entries {
		assertNoSecrets(t, entry, secrets...)
	}
}

// TestAuditRepositoryListFiltered proves the viewer filters narrow the page in
// SQL: actor, exact action and the inclusive from/to window all combine with
// the ID cursor and keep the newest-first order.
func TestAuditRepositoryListFiltered(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	users := identitypostgres.NewUserRepository(pool)
	admin, err := users.Create(ctx, identity.CreateParams{
		Username: "admin", PasswordHash: "hash", Role: identity.RoleAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := users.Create(ctx, identity.CreateParams{
		Username: "qa01", PasswordHash: "hash", Role: identity.RoleUser,
	})
	if err != nil {
		t.Fatal(err)
	}

	repo := auditpostgres.NewRepository(pool)
	base := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	recorded := []audit.Entry{
		{ActorUserID: &admin.ID, Action: "version.created", CreatedAt: base.Add(1 * time.Hour)},
		{ActorUserID: &admin.ID, Action: "version.updated", CreatedAt: base.Add(2 * time.Hour)},
		{ActorUserID: &other.ID, Action: "version.updated", CreatedAt: base.Add(3 * time.Hour)},
		{Action: "auth.login_failed", CreatedAt: base.Add(4 * time.Hour)},
	}
	for _, entry := range recorded {
		if err := repo.Record(ctx, entry); err != nil {
			t.Fatalf("record %s: %v", entry.Action, err)
		}
	}

	// Actor filter.
	page, more, err := repo.ListFiltered(ctx, audit.ListFilter{ActorUserID: &admin.ID}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if more || len(page) != 2 || page[0].Action != "version.updated" || page[1].Action != "version.created" {
		t.Fatalf("actor filter page=%+v more=%v", page, more)
	}

	// Exact action filter.
	page, _, err = repo.ListFiltered(ctx, audit.ListFilter{Action: "version.updated"}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 {
		t.Fatalf("action filter size=%d want 2", len(page))
	}
	for _, entry := range page {
		if entry.Action != "version.updated" {
			t.Fatalf("action filter returned %q", entry.Action)
		}
	}

	// Inclusive from/to window: only the 03:00 entry is in [03:00, 03:00].
	from := base.Add(3 * time.Hour)
	to := base.Add(3 * time.Hour)
	page, _, err = repo.ListFiltered(ctx, audit.ListFilter{From: &from, To: &to}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].Action != "version.updated" || page[0].ActorUserID == nil || *page[0].ActorUserID != other.ID {
		t.Fatalf("time filter page=%+v", page)
	}

	// Filter + cursor: the second page of the actor+action filter is empty
	// because only one admin entry has the action.
	first, _, err := repo.ListFiltered(ctx, audit.ListFilter{ActorUserID: &admin.ID, Action: "version.updated"}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Action != "version.updated" {
		t.Fatalf("combined filter page=%+v", first)
	}
	second, more, err := repo.ListFiltered(ctx, audit.ListFilter{ActorUserID: &admin.ID, Action: "version.updated"}, 1, first[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 || more {
		t.Fatalf("cursor under filter page=%+v more=%v", second, more)
	}

	// A zero filter behaves exactly like List.
	all, _, err := repo.ListFiltered(ctx, audit.ListFilter{}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("zero filter size=%d want 4", len(all))
	}
}

// mustHash hashes plaintext with the real bcrypt adapter.
func mustHash(t *testing.T, hasher password.Hasher, plaintext string) string {
	t.Helper()
	hash, err := hasher.Hash(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

// assertNoSecrets fails when an entry stores any of secrets in its string
// fields or metadata.
func assertNoSecrets(t *testing.T, entry audit.Entry, secrets ...string) {
	t.Helper()
	stored := []string{entry.Action, entry.TargetType, entry.TargetID, entry.RequestID, entry.IP, entry.UserAgent}
	raw, err := json.Marshal(entry.Metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	stored = append(stored, string(raw))
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		for _, field := range stored {
			if strings.Contains(field, secret) {
				t.Fatalf("%s leaks credential %q: %+v", entry.Action, secret, entry)
			}
		}
	}
	for key := range entry.Metadata {
		lower := strings.ToLower(key)
		for _, banned := range []string{"password", "passwd", "token", "hash", "secret", "api_key", "apikey", "authorization", "header", "key"} {
			if strings.Contains(lower, banned) {
				t.Fatalf("%s metadata contains sensitive key %q", entry.Action, key)
			}
		}
	}
}
