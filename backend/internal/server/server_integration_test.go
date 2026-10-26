//go:build integration

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditpostgres "github.com/EdouardRichard/EzTest/backend/internal/audit/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/config"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure/password"
	identitypostgres "github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// TestUserAndAuditAPIsAgainstDatabase is the Task 7 end-to-end check on the
// real PostgreSQL repositories: admin login, user create/list/pagination,
// non-admin 403, status update, password reset with token revocation and the
// persisted audit trail carrying the actor and the request metadata.
func TestUserAndAuditAPIsAgainstDatabase(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	userRepo := identitypostgres.NewUserRepository(pool)
	tokenRepo := identitypostgres.NewTokenRepository(pool)
	hasher := password.NewHasher(12)
	auditor := auditapp.NewService(auditpostgres.NewRepository(pool))

	userSvc := application.NewUserService(userRepo, hasher, auditor, tokenRepo)
	if err := userSvc.EnsureAdmin(ctx, "admin", "admin-pass-123"); err != nil {
		t.Fatal(err)
	}
	authSvc := application.NewAuthService(userRepo, tokenRepo, hasher, time.Now, auditor)

	h := New(config.Config{Env: "test"}, testLogger(), Dependencies{
		Pinger: fakePinger{},
		Auth:   authSvc,
		Users:  userSvc,
		Audit:  auditor,
	})

	adminToken := doLogin(t, h, "admin", "admin-pass-123")

	// The admin creates a regular user.
	rec := doJSON(t, h, http.MethodPost, "/api/users",
		`{"username":"qa01","password":"qa-pass-1234"}`, adminToken, "req_int_create_1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", rec.Code, rec.Body)
	}
	var created struct {
		Data struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
			Role     string `json:"role"`
			Status   string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create %q: %v", rec.Body, err)
	}
	if created.Data.ID != 2 || created.Data.Username != "qa01" ||
		created.Data.Role != string(identity.RoleUser) || created.Data.Status != string(identity.StatusActive) {
		t.Fatalf("created=%+v", created.Data)
	}

	// The list is paginated with an opaque cursor and has_more.
	rec = doJSON(t, h, http.MethodGet, "/api/users?limit=1", "", adminToken, "req_int_list_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("list=%d %s", rec.Code, rec.Body)
	}
	var firstPage struct {
		Data       []json.RawMessage `json:"data"`
		NextCursor string            `json:"next_cursor"`
		HasMore    bool              `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &firstPage); err != nil {
		t.Fatalf("decode list %q: %v", rec.Body, err)
	}
	if len(firstPage.Data) != 1 || !firstPage.HasMore || firstPage.NextCursor == "" {
		t.Fatalf("first page=%s", rec.Body)
	}
	rec = doJSON(t, h, http.MethodGet,
		"/api/users?limit=1&cursor="+url.QueryEscape(firstPage.NextCursor), "", adminToken, "req_int_list_2")
	if rec.Code != http.StatusOK {
		t.Fatalf("second page=%d %s", rec.Code, rec.Body)
	}
	var secondPage struct {
		Data []struct {
			Username string `json:"username"`
		} `json:"data"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &secondPage); err != nil {
		t.Fatalf("decode second page %q: %v", rec.Body, err)
	}
	if len(secondPage.Data) != 1 || secondPage.Data[0].Username != "qa01" || secondPage.HasMore {
		t.Fatalf("second page=%s", rec.Body)
	}

	// A regular user cannot reach the admin users API.
	userToken := doLogin(t, h, "qa01", "qa-pass-1234")
	rec = doJSON(t, h, http.MethodGet, "/api/users", "", userToken, "req_int_forbidden_1")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("user list=%d %s want 403", rec.Code, rec.Body)
	}

	// Disabling the user immediately invalidates its sessions.
	rec = doJSON(t, h, http.MethodPatch, "/api/users/2", `{"status":"disabled"}`, adminToken, "req_int_disable_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("disable=%d %s", rec.Code, rec.Body)
	}
	rec = doJSON(t, h, http.MethodGet, "/api/auth/me", "", userToken, "req_int_me_disabled")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled user me=%d %s want 401", rec.Code, rec.Body)
	}
	// The single admin cannot disable itself.
	rec = doJSON(t, h, http.MethodPatch, "/api/users/1", `{"status":"disabled"}`, adminToken, "req_int_self_disable")
	if rec.Code != http.StatusConflict {
		t.Fatalf("self-disable=%d %s want 409", rec.Code, rec.Body)
	}

	// Re-enable the user, log in again, then reset its password: every token
	// of that user must be revoked.
	rec = doJSON(t, h, http.MethodPatch, "/api/users/2", `{"status":"active"}`, adminToken, "req_int_enable_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("enable=%d %s", rec.Code, rec.Body)
	}
	// The disable revoked the token; the re-enable must not resurrect it.
	rec = doJSON(t, h, http.MethodGet, "/api/auth/me", "", userToken, "req_int_me_reenabled")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("token issued before disable became valid again after re-enable: me=%d %s", rec.Code, rec.Body)
	}
	userToken = doLogin(t, h, "qa01", "qa-pass-1234")
	rec = doJSON(t, h, http.MethodPost, "/api/users/2/reset-password",
		`{"new_password":"new-pass-1234"}`, adminToken, "req_int_reset_1")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("reset=%d %s want 204", rec.Code, rec.Body)
	}
	rec = doJSON(t, h, http.MethodGet, "/api/auth/me", "", userToken, "req_int_me_reset")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("token survived password reset: me=%d %s", rec.Code, rec.Body)
	}
	doLogin(t, h, "qa01", "new-pass-1234")

	// The audit log is persisted and carries the actor and the request
	// metadata the HTTP handlers attached.
	rec = doJSON(t, h, http.MethodGet, "/api/admin/audit?limit=100", "", adminToken, "req_int_audit_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("audit=%d %s", rec.Code, rec.Body)
	}
	var auditPage struct {
		Data []struct {
			ActorUserID *int64         `json:"actor_user_id"`
			Action      string         `json:"action"`
			TargetID    string         `json:"target_id"`
			RequestID   string         `json:"request_id"`
			IP          string         `json:"ip"`
			UserAgent   string         `json:"user_agent"`
			Metadata    map[string]any `json:"metadata"`
		} `json:"data"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &auditPage); err != nil {
		t.Fatalf("decode audit %q: %v", rec.Body, err)
	}
	if len(auditPage.Data) == 0 {
		t.Fatal("audit log is empty")
	}
	byAction := make(map[string]int, len(auditPage.Data))
	for i, entry := range auditPage.Data {
		byAction[entry.Action] = i
	}
	for _, action := range []string{
		auditapp.ActionUserCreated,
		auditapp.ActionUserUpdated,
		auditapp.ActionUserPasswordReset,
		auditapp.ActionLoginSucceeded,
	} {
		if _, ok := byAction[action]; !ok {
			t.Fatalf("audit log has no %s entry: %+v", action, auditPage.Data)
		}
	}
	createdEntry := auditPage.Data[byAction[auditapp.ActionUserCreated]]
	if createdEntry.ActorUserID == nil || *createdEntry.ActorUserID != 1 {
		t.Fatalf("user.created actor=%v want admin 1", createdEntry.ActorUserID)
	}
	if createdEntry.TargetID != "2" {
		t.Fatalf("user.created target=%q want 2", createdEntry.TargetID)
	}
	if createdEntry.RequestID != "req_int_create_1" {
		t.Fatalf("user.created request_id=%q want req_int_create_1", createdEntry.RequestID)
	}
	if createdEntry.IP != "192.0.2.1" {
		t.Fatalf("user.created ip=%q want 192.0.2.1", createdEntry.IP)
	}
	if createdEntry.UserAgent != "eztest-integration/1.0" {
		t.Fatalf("user.created user_agent=%q", createdEntry.UserAgent)
	}
	if createdEntry.Metadata["username"] != "qa01" {
		t.Fatalf("user.created metadata=%v", createdEntry.Metadata)
	}
	resetEntry := auditPage.Data[byAction[auditapp.ActionUserPasswordReset]]
	if resetEntry.ActorUserID == nil || *resetEntry.ActorUserID != 1 {
		t.Fatalf("password reset actor=%v want admin 1", resetEntry.ActorUserID)
	}
}

// doJSON sends one JSON request through h and returns the recorder. requestID
// is sent as X-Request-ID so the audit tests can correlate the persisted entry
// with the request that produced it.
func doJSON(t *testing.T, h http.Handler, method, path, body, token, requestID string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("X-Request-ID", requestID)
	req.Header.Set("User-Agent", "eztest-integration/1.0")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// doLogin logs in over HTTP and returns the raw bearer token.
func doLogin(t *testing.T, h http.Handler, username, password string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, h, http.MethodPost, "/api/auth/login", string(body), "", "req_int_login_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("login %q=%d %s", username, rec.Code, rec.Body)
	}
	var payload struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode login %q: %v", rec.Body, err)
	}
	if payload.Data.Token == "" {
		t.Fatalf("login %q returned no token: %s", username, rec.Body)
	}
	return payload.Data.Token
}
