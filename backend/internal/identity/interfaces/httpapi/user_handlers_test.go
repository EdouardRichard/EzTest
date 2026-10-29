package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
)

// testUserAgent is sent with write requests so the audit tests can prove the
// handler captured it.
const testUserAgent = "eztest-http-test/1.0"

// TestAdminUserCRUDAndForbidden is the Task 7 acceptance test: an admin can
// create a user with only username/password, and the new regular user is
// forbidden from the admin-only user list.
func TestAdminUserCRUDAndForbidden(t *testing.T) {
	api := newUserTestAPI(t)
	admin := loginAs(t, api, "admin", "admin-pass-123")
	rec := postJSONWithBearer(api, "/api/users", `{"username":"qa01","password":"qa-pass-123"}`, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", rec.Code, rec.Body)
	}
	user := loginAs(t, api, "qa01", "qa-pass-123")
	rec = getWithBearer(api, "/api/users", user)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if code := decodeAPIError(t, rec).Code; code != "forbidden" {
		t.Fatalf("error code=%q want forbidden", code)
	}
}

// TestCreateUserResponseAndAudit pins the create contract: 201 with a
// password-free `{data:...}` payload, and an audit entry carrying the actor and
// the HTTP request metadata from the context.
func TestCreateUserResponseAndAudit(t *testing.T) {
	api := newUserTestAPI(t)
	admin := loginAs(t, api, "admin", "admin-pass-123")

	rec := postJSONWithBearer(api, "/api/users", `{"username":"qa01","password":"qa-pass-123"}`, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", rec.Code, rec.Body)
	}
	var body struct {
		Data userPayload `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode create response %q: %v", rec.Body, err)
	}
	if body.Data.ID != 2 || body.Data.Username != "qa01" || body.Data.Role != identity.RoleUser || body.Data.Status != identity.StatusActive {
		t.Fatalf("unexpected created user: %s", rec.Body)
	}
	for _, banned := range []string{"password", "hash"} {
		if strings.Contains(strings.ToLower(rec.Body.String()), banned) {
			t.Fatalf("create response leaks %q: %s", banned, rec.Body)
		}
	}

	entry, ok := api.audit.lastEntry(auditapp.ActionUserCreated)
	if !ok {
		t.Fatalf("no %s audit entry: %+v", auditapp.ActionUserCreated, api.audit.entries)
	}
	if entry.ActorUserID == nil || *entry.ActorUserID != 1 {
		t.Fatalf("created actor=%v want admin 1", entry.ActorUserID)
	}
	if entry.RequestID != testRequestID || entry.IP != "192.0.2.1" || entry.UserAgent != testUserAgent {
		t.Fatalf("created request meta=(%q,%q,%q) want (%q,192.0.2.1,%q)",
			entry.RequestID, entry.IP, entry.UserAgent, testRequestID, testUserAgent)
	}
	if entry.Metadata["username"] != "qa01" || entry.Metadata["role"] != "user" {
		t.Fatalf("created metadata=%v", entry.Metadata)
	}
}

// TestListUsersPagination pins the generic list contract: data plus
// next_cursor/has_more, an opaque cursor derived from the last visible ID and
// Task 6's parsed default limit of 20.
func TestListUsersPagination(t *testing.T) {
	api := newUserTestAPI(t)
	admin := loginAs(t, api, "admin", "admin-pass-123")
	for _, username := range []string{"qa01", "qa02", "qa03"} {
		rec := postJSONWithBearer(api, "/api/users", `{"username":"`+username+`","password":"qa-pass-123"}`, admin)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %s=%d %s", username, rec.Code, rec.Body)
		}
	}

	rec := getWithBearer(api, "/api/users?limit=2", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("list=%d %s", rec.Code, rec.Body)
	}
	page := decodeUserList(t, rec.Body)
	if len(page.Data) != 2 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("first page=%+v want 2 users, has_more, cursor", page)
	}
	if page.Data[0].ID != 1 || page.Data[0].Username != "admin" || page.Data[1].ID != 2 {
		t.Fatalf("first page=%+v want admin, qa01", page.Data)
	}

	rec = getWithBearer(api, "/api/users?limit=2&cursor="+url.QueryEscape(page.NextCursor), admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("next page=%d %s", rec.Code, rec.Body)
	}
	rest := decodeUserList(t, rec.Body)
	if len(rest.Data) != 2 || rest.HasMore || rest.NextCursor != "" ||
		rest.Data[0].Username != "qa02" || rest.Data[1].Username != "qa03" {
		t.Fatalf("second page=%+v want qa02, qa03, done", rest)
	}
}

// TestListUsersDefaultLimitIsTwenty proves the user list honors Task 6's parsed
// default instead of an ad-hoc handler default.
func TestListUsersDefaultLimitIsTwenty(t *testing.T) {
	api := newUserTestAPI(t)
	admin := loginAs(t, api, "admin", "admin-pass-123")
	for i := 0; i < 21; i++ {
		username := "qa" + strconv.Itoa(i)
		rec := postJSONWithBearer(api, "/api/users", `{"username":"`+username+`","password":"qa-pass-123"}`, admin)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %s=%d %s", username, rec.Code, rec.Body)
		}
	}

	rec := getWithBearer(api, "/api/users", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("list=%d %s", rec.Code, rec.Body)
	}
	page := decodeUserList(t, rec.Body)
	if len(page.Data) != 20 || !page.HasMore {
		t.Fatalf("default page=%d has_more=%v want 20/true", len(page.Data), page.HasMore)
	}
}

// TestListUsersRejectsInvalidPagination proves the handler validates limit and
// cursor with the shared platform helpers.
func TestListUsersRejectsInvalidPagination(t *testing.T) {
	api := newUserTestAPI(t)
	admin := loginAs(t, api, "admin", "admin-pass-123")

	for _, query := range []string{"limit=0", "limit=101", "limit=abc", "cursor=!!!not-base64!!!"} {
		rec := getWithBearer(api, "/api/users?"+query, admin)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d body=%s want 400", query, rec.Code, rec.Body)
		}
		if code := decodeAPIError(t, rec).Code; code != "invalid_argument" {
			t.Fatalf("%s: error code=%q want invalid_argument", query, code)
		}
	}
}

// TestCreateUserValidation pins the create rules: only role=user is accepted
// (a role field is rejected as unknown), duplicates conflict and weak passwords
// are rejected without leaking anything.
func TestCreateUserValidation(t *testing.T) {
	api := newUserTestAPI(t)
	admin := loginAs(t, api, "admin", "admin-pass-123")

	if rec := postJSONWithBearer(api, "/api/users", `{"username":"qa01","password":"qa-pass-123"}`, admin); rec.Code != http.StatusCreated {
		t.Fatalf("seed create=%d %s", rec.Code, rec.Body)
	}

	dup := postJSONWithBearer(api, "/api/users", `{"username":"qa01","password":"other-pass-123"}`, admin)
	if dup.Code != http.StatusConflict {
		t.Fatalf("duplicate=%d %s want 409", dup.Code, dup.Body)
	}
	if code := decodeAPIError(t, dup).Code; code != "conflict" {
		t.Fatalf("duplicate error code=%q want conflict", code)
	}

	weak := postJSONWithBearer(api, "/api/users", `{"username":"qa02","password":"short"}`, admin)
	if weak.Code != http.StatusBadRequest {
		t.Fatalf("weak password=%d %s want 400", weak.Code, weak.Body)
	}
	if code := decodeAPIError(t, weak).Code; code != "invalid_argument" {
		t.Fatalf("weak password error code=%q want invalid_argument", code)
	}

	// The P0 API only creates regular users: a role field must never be
	// smuggled through the create body.
	role := postJSONWithBearer(api, "/api/users", `{"username":"qa03","password":"qa-pass-123","role":"admin"}`, admin)
	if role.Code != http.StatusBadRequest {
		t.Fatalf("role field=%d %s want 400", role.Code, role.Body)
	}

	malformed := postJSONWithBearer(api, "/api/users", `{"username":`, admin)
	if malformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed=%d %s want 400", malformed.Code, malformed.Body)
	}
	for _, rec := range []*httptest.ResponseRecorder{weak, role, malformed} {
		if strings.Contains(strings.ToLower(rec.Body.String()), "password\":") {
			t.Fatalf("validation error echoes a password: %s", rec.Body)
		}
	}
}

// TestGetUser pins GET /api/users/{id}: admin-only read, 404 for a missing
// account and 400 for an id that is not a positive integer.
func TestGetUser(t *testing.T) {
	api := newUserTestAPI(t)
	admin := loginAs(t, api, "admin", "admin-pass-123")
	if rec := postJSONWithBearer(api, "/api/users", `{"username":"qa01","password":"qa-pass-123"}`, admin); rec.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", rec.Code, rec.Body)
	}

	rec := getWithBearer(api, "/api/users/2", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("get=%d %s", rec.Code, rec.Body)
	}
	var body struct {
		Data userPayload `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode get response %q: %v", rec.Body, err)
	}
	if body.Data.Username != "qa01" || body.Data.Role != identity.RoleUser {
		t.Fatalf("unexpected user: %s", rec.Body)
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "password") {
		t.Fatalf("get response leaks a password: %s", rec.Body)
	}

	for _, tc := range []struct {
		path string
		want int
		code string
	}{
		{path: "/api/users/99999", want: http.StatusNotFound, code: "not_found"},
		{path: "/api/users/abc", want: http.StatusBadRequest, code: "invalid_argument"},
		{path: "/api/users/0", want: http.StatusBadRequest, code: "invalid_argument"},
	} {
		rec := getWithBearer(api, tc.path, admin)
		if rec.Code != tc.want {
			t.Fatalf("%s: code=%d body=%s want %d", tc.path, rec.Code, rec.Body, tc.want)
		}
		if code := decodeAPIError(t, rec).Code; code != tc.code {
			t.Fatalf("%s: error code=%q want %q", tc.path, code, tc.code)
		}
	}
}

// TestUpdateUserStatus pins PATCH /api/users/{id}: status-only updates, enum
// validation, and the admin cannot disable itself.
func TestUpdateUserStatus(t *testing.T) {
	api := newUserTestAPI(t)
	admin := loginAs(t, api, "admin", "admin-pass-123")
	if rec := postJSONWithBearer(api, "/api/users", `{"username":"qa01","password":"qa-pass-123"}`, admin); rec.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", rec.Code, rec.Body)
	}

	rec := patchJSONWithBearer(api, "/api/users/2", `{"status":"disabled"}`, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable=%d %s", rec.Code, rec.Body)
	}
	var body struct {
		Data userPayload `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode patch response %q: %v", rec.Body, err)
	}
	if body.Data.Status != identity.StatusDisabled {
		t.Fatalf("status=%q want disabled", body.Data.Status)
	}
	if rec := patchJSONWithBearer(api, "/api/users/2", `{"status":"active"}`, admin); rec.Code != http.StatusOK {
		t.Fatalf("reenable=%d %s", rec.Code, rec.Body)
	}

	// The single admin cannot disable itself: P0 has no recovery path.
	self := patchJSONWithBearer(api, "/api/users/1", `{"status":"disabled"}`, admin)
	if self.Code != http.StatusConflict {
		t.Fatalf("self-disable=%d %s want 409", self.Code, self.Body)
	}
	if code := decodeAPIError(t, self).Code; code != "conflict" {
		t.Fatalf("self-disable error code=%q want conflict", code)
	}
	storedAdmin, err := api.repo.GetByID(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if storedAdmin.Status != identity.StatusActive {
		t.Fatal("self-disable changed the admin status")
	}

	for _, tc := range []struct {
		name string
		path string
		body string
		want int
		code string
	}{
		{name: "invalid status", path: "/api/users/2", body: `{"status":"bogus"}`, want: http.StatusBadRequest, code: "invalid_argument"},
		{name: "unknown field", path: "/api/users/2", body: `{"username":"qa02"}`, want: http.StatusBadRequest, code: "invalid_argument"},
		{name: "missing user", path: "/api/users/99999", body: `{"status":"disabled"}`, want: http.StatusNotFound, code: "not_found"},
		{name: "bad id", path: "/api/users/abc", body: `{"status":"disabled"}`, want: http.StatusBadRequest, code: "invalid_argument"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := patchJSONWithBearer(api, tc.path, tc.body, admin)
			if rec.Code != tc.want {
				t.Fatalf("code=%d body=%s want %d", rec.Code, rec.Body, tc.want)
			}
			if code := decodeAPIError(t, rec).Code; code != tc.code {
				t.Fatalf("error code=%q want %q", code, tc.code)
			}
		})
	}

	// The status change that succeeded is audited against the acting admin.
	entry, ok := api.audit.lastEntry(auditapp.ActionUserUpdated)
	if !ok {
		t.Fatalf("no %s audit entry", auditapp.ActionUserUpdated)
	}
	if entry.ActorUserID == nil || *entry.ActorUserID != 1 {
		t.Fatalf("updated actor=%v want 1", entry.ActorUserID)
	}
}

// TestDisableRevokesTokensPermanently pins the security requirement that
// disabling an account revokes its sessions instead of only hiding them while
// the account is disabled: a bearer token issued before the disable must stay
// rejected after the account is re-enabled.
func TestDisableRevokesTokensPermanently(t *testing.T) {
	api := newUserTestAPI(t)
	admin := loginAs(t, api, "admin", "admin-pass-123")
	if rec := postJSONWithBearer(api, "/api/users", `{"username":"qa01","password":"qa-pass-123"}`, admin); rec.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", rec.Code, rec.Body)
	}
	oldToken := loginAs(t, api, "qa01", "qa-pass-123")
	if rec := getWithBearer(api, "/api/auth/me", oldToken); rec.Code != http.StatusOK {
		t.Fatalf("me before disable=%d %s", rec.Code, rec.Body)
	}

	if rec := patchJSONWithBearer(api, "/api/users/2", `{"status":"disabled"}`, admin); rec.Code != http.StatusOK {
		t.Fatalf("disable=%d %s", rec.Code, rec.Body)
	}
	// The disabled account is rejected either way; the re-enable below is what
	// distinguishes a revoked token from one merely hidden by the status check.
	if rec := getWithBearer(api, "/api/auth/me", oldToken); rec.Code != http.StatusUnauthorized {
		t.Fatalf("me while disabled=%d %s", rec.Code, rec.Body)
	}

	if rec := patchJSONWithBearer(api, "/api/users/2", `{"status":"active"}`, admin); rec.Code != http.StatusOK {
		t.Fatalf("reenable=%d %s", rec.Code, rec.Body)
	}
	if rec := getWithBearer(api, "/api/auth/me", oldToken); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token issued before disable became valid again after re-enable: code=%d body=%s", rec.Code, rec.Body)
	}
	// The account itself is active again: a fresh login succeeds.
	loginAs(t, api, "qa01", "qa-pass-123")
}

// TestResetPasswordRevokesTokens pins the security requirement: a successful
// admin password reset revokes every session of the target user, so the old
// bearer token and the old password stop working.
func TestResetPasswordRevokesTokens(t *testing.T) {
	api := newUserTestAPI(t)
	admin := loginAs(t, api, "admin", "admin-pass-123")
	if rec := postJSONWithBearer(api, "/api/users", `{"username":"qa01","password":"qa-pass-123"}`, admin); rec.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", rec.Code, rec.Body)
	}
	userToken := loginAs(t, api, "qa01", "qa-pass-123")

	rec := postJSONWithBearer(api, "/api/users/2/reset-password", `{"new_password":"new-pass-1234"}`, admin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("reset=%d %s want 204", rec.Code, rec.Body)
	}
	if rec := getWithBearer(api, "/api/auth/me", userToken); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old token still valid after reset: code=%d body=%s", rec.Code, rec.Body)
	}
	if rec := postJSON(api, "/api/auth/login", `{"username":"qa01","password":"qa-pass-123"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old password still logs in: code=%d body=%s", rec.Code, rec.Body)
	}
	loginAs(t, api, "qa01", "new-pass-1234")

	weak := postJSONWithBearer(api, "/api/users/2/reset-password", `{"new_password":"short"}`, admin)
	if weak.Code != http.StatusBadRequest {
		t.Fatalf("weak reset=%d %s want 400", weak.Code, weak.Body)
	}
	if code := decodeAPIError(t, weak).Code; code != "invalid_argument" {
		t.Fatalf("weak reset error code=%q want invalid_argument", code)
	}
	missing := postJSONWithBearer(api, "/api/users/99999/reset-password", `{"new_password":"new-pass-1234"}`, admin)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing reset=%d %s want 404", missing.Code, missing.Body)
	}

	entry, ok := api.audit.lastEntry(auditapp.ActionUserPasswordReset)
	if !ok {
		t.Fatalf("no %s audit entry", auditapp.ActionUserPasswordReset)
	}
	if entry.ActorUserID == nil || *entry.ActorUserID != 1 {
		t.Fatalf("reset actor=%v want 1", entry.ActorUserID)
	}
	for _, secret := range []string{"qa-pass-123", "new-pass-1234", "hash:"} {
		raw, err := json.Marshal(entry.Metadata)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), secret) {
			t.Fatalf("reset audit metadata leaks %q: %s", secret, raw)
		}
	}
}

// TestWrongPasswordIsAuditedOnce proves the handler does not duplicate the
// auth.login_failed entry that AuthService already records for a wrong
// password: exactly one entry is written for one attempt.
func TestWrongPasswordIsAuditedOnce(t *testing.T) {
	api := newUserTestAPI(t)
	rec := postJSON(api, "/api/auth/login", `{"username":"admin","password":"wrong-password"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password=%d %s want 401", rec.Code, rec.Body)
	}
	failures := 0
	for _, entry := range api.audit.entries {
		if entry.Action == auditapp.ActionLoginFailed {
			failures++
		}
	}
	if failures != 1 {
		t.Fatalf("wrong-password attempt recorded %d login_failed entries, want 1", failures)
	}
}

// TestUserRoutesRequireAuthenticationAndAdmin proves every user route is behind
// the global bearer auth and the per-route admin guard.
func TestUserRoutesRequireAuthenticationAndAdmin(t *testing.T) {
	api := newUserTestAPI(t)
	admin := loginAs(t, api, "admin", "admin-pass-123")
	if rec := postJSONWithBearer(api, "/api/users", `{"username":"qa01","password":"qa-pass-123"}`, admin); rec.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", rec.Code, rec.Body)
	}
	user := loginAs(t, api, "qa01", "qa-pass-123")

	// Without a bearer token the global auth middleware rejects the request
	// before the route runs.
	rec := getWithBearer(api, "/api/users", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list=%d want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("WWW-Authenticate=%q want Bearer", got)
	}
	if code := decodeAPIError(t, rec).Code; code != "unauthenticated" {
		t.Fatalf("unauthenticated error code=%q", code)
	}

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: "/api/users"},
		{method: http.MethodGet, path: "/api/users/1"},
		{method: http.MethodPost, path: "/api/users", body: `{"username":"qa02","password":"qa-pass-123"}`},
		{method: http.MethodPatch, path: "/api/users/1", body: `{"status":"active"}`},
		{method: http.MethodPost, path: "/api/users/1/reset-password", body: `{"new_password":"new-pass-1234"}`},
	} {
		req := newTestRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+user)
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s with user token=%d body=%s want 403", tc.method, tc.path, rec.Code, rec.Body)
		}
		if code := decodeAPIError(t, rec).Code; code != "forbidden" {
			t.Fatalf("%s %s: error code=%q want forbidden", tc.method, tc.path, code)
		}
	}
}

// newUserTestAPI builds the admin user API over real identity services with
// in-memory repositories, a cheap hasher and a capturing audit recorder. The
// admin user routes are wrapped in AuthMiddleware exactly like the server's
// global middleware chain so the per-route RequireAdmin guards see a user.
func newUserTestAPI(t *testing.T) *userTestAPI {
	t.Helper()
	repo := newStubUserRepo()
	tokens := newStubTokenRepo()
	recorder := &capturingRecorder{}
	hasher := stubHasher{}
	now := func() time.Time { return fixedNow }

	userSvc := application.NewUserService(repo, hasher, recorder, tokens)
	authSvc := application.NewAuthService(repo, tokens, hasher, now, recorder)
	if _, err := repo.Create(context.Background(), identity.CreateParams{
		Username:     "admin",
		PasswordHash: "hash:admin-pass-123",
		Role:         identity.RoleAdmin,
	}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	RegisterAuthRoutes(mux, authSvc, NewLoginLimiter(now), recorder)
	userMux := http.NewServeMux()
	RegisterUserRoutes(userMux, userSvc)
	authenticated := AuthMiddleware(authSvc)
	mux.Handle("/api/users", authenticated(userMux))
	mux.Handle("/api/users/", authenticated(userMux))

	return &userTestAPI{Handler: mux, repo: repo, audit: recorder}
}

// userTestAPI is the HTTP surface under test: the production auth routes and
// the production admin user routes over in-memory state.
type userTestAPI struct {
	http.Handler
	repo  *stubUserRepo
	audit *capturingRecorder
}

// loginAs logs in over HTTP and returns the bearer token.
func loginAs(t *testing.T, h http.Handler, username, password string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	rec := postJSON(h, "/api/auth/login", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("login %q=%d %s", username, rec.Code, rec.Body)
	}
	return decodeToken(t, rec.Body)
}

func postJSONWithBearer(h http.Handler, path, body, token string) *httptest.ResponseRecorder {
	req := newTestRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", testUserAgent)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func patchJSONWithBearer(h http.Handler, path, body, token string) *httptest.ResponseRecorder {
	req := newTestRequest(http.MethodPatch, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", testUserAgent)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// itoa is a tiny decimal formatter so test names stay short and allocation-free.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits [20]byte
	pos := len(digits)
	for i > 0 {
		pos--
		digits[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(digits[pos:])
}

type userListResponse struct {
	Data       []userPayload `json:"data"`
	NextCursor string        `json:"next_cursor"`
	HasMore    bool          `json:"has_more"`
}

func decodeUserList(t *testing.T, body io.Reader) userListResponse {
	t.Helper()
	var payload userListResponse
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		t.Fatalf("decode user list: %v", err)
	}
	return payload
}

// capturingRecorder is an in-memory audit.Recorder that captures every entry so
// tests can assert handler- and service-level audit behavior.
type capturingRecorder struct {
	entries []auditdomain.Entry
	err     error
}

func (r *capturingRecorder) Record(_ context.Context, entry auditdomain.Entry) error {
	r.entries = append(r.entries, entry)
	return r.err
}

func (r *capturingRecorder) List(context.Context, int, int64) ([]auditdomain.Entry, bool, error) {
	return append([]auditdomain.Entry(nil), r.entries...), false, nil
}

func (r *capturingRecorder) ListFiltered(context.Context, auditdomain.ListFilter, int, int64) ([]auditdomain.Entry, bool, error) {
	return append([]auditdomain.Entry(nil), r.entries...), false, nil
}

var _ auditapp.Recorder = (*capturingRecorder)(nil)

// lastEntry returns the newest captured entry with action.
func (r *capturingRecorder) lastEntry(action string) (auditdomain.Entry, bool) {
	for i := len(r.entries) - 1; i >= 0; i-- {
		if r.entries[i].Action == action {
			return r.entries[i], true
		}
	}
	return auditdomain.Entry{}, false
}

// stubHasher is a cheap password hasher: bcrypt at cost 12 would dominate these
// HTTP tests. Production wires password.NewHasher(12).
type stubHasher struct{}

func (stubHasher) Hash(password string) (string, error) { return "hash:" + password, nil }
func (stubHasher) Verify(hash, password string) bool    { return hash == "hash:"+password }

// stubUserRepo is an in-memory identity.Repository for HTTP tests. It mirrors
// the database invariants the handlers rely on: unique usernames, at most one
// admin and ascending ID ordering.
type stubUserRepo struct {
	users  map[int64]identity.User
	nextID int64
}

func newStubUserRepo() *stubUserRepo {
	return &stubUserRepo{users: make(map[int64]identity.User)}
}

func (r *stubUserRepo) Create(_ context.Context, params identity.CreateParams) (identity.User, error) {
	for _, u := range r.users {
		if u.Username == params.Username {
			return identity.User{}, identity.ErrUsernameTaken
		}
		if u.Role == identity.RoleAdmin && params.Role == identity.RoleAdmin {
			return identity.User{}, identity.ErrAdminExists
		}
	}
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

func (r *stubUserRepo) GetByUsername(_ context.Context, username string) (identity.User, error) {
	for _, u := range r.users {
		if u.Username == username {
			return u, nil
		}
	}
	return identity.User{}, identity.ErrUserNotFound
}

func (r *stubUserRepo) GetByID(_ context.Context, id int64) (identity.User, error) {
	user, ok := r.users[id]
	if !ok {
		return identity.User{}, identity.ErrUserNotFound
	}
	return user, nil
}

func (r *stubUserRepo) List(_ context.Context, limit int, afterID int64) ([]identity.User, bool, error) {
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

func (r *stubUserRepo) UpdateStatus(_ context.Context, id int64, status identity.Status) (identity.User, error) {
	user, ok := r.users[id]
	if !ok {
		return identity.User{}, identity.ErrUserNotFound
	}
	user.Status = status
	user.UpdatedAt = fixedNow
	r.users[id] = user
	return user, nil
}

func (r *stubUserRepo) UpdatePasswordHash(_ context.Context, id int64, passwordHash string) error {
	user, ok := r.users[id]
	if !ok {
		return identity.ErrUserNotFound
	}
	user.PasswordHash = passwordHash
	user.UpdatedAt = fixedNow
	r.users[id] = user
	return nil
}

func (r *stubUserRepo) UpdateLastLogin(_ context.Context, id int64) error {
	user, ok := r.users[id]
	if !ok {
		return identity.ErrUserNotFound
	}
	loginAt := fixedNow
	user.LastLoginAt = &loginAt
	r.users[id] = user
	return nil
}

func (r *stubUserRepo) CountAdmins(_ context.Context) (int, error) {
	count := 0
	for _, u := range r.users {
		if u.Role == identity.RoleAdmin {
			count++
		}
	}
	return count, nil
}

// stubTokenRepo is an in-memory identity.TokenRepository. RevokeAllForUser
// stamps every live token of the user, which is what the reset-password tests
// observe through subsequent authentication attempts.
type stubTokenRepo struct {
	byHash map[string]identity.Token
	nextID int64
}

func newStubTokenRepo() *stubTokenRepo {
	return &stubTokenRepo{byHash: make(map[string]identity.Token)}
}

func (r *stubTokenRepo) Create(_ context.Context, userID int64, tokenHash string, expiresAt time.Time) error {
	r.nextID++
	r.byHash[tokenHash] = identity.Token{
		ID:        r.nextID,
		UserID:    userID,
		TokenHash: tokenHash,
		ExpiresAt: expiresAt,
	}
	return nil
}

func (r *stubTokenRepo) GetByHash(_ context.Context, tokenHash string) (identity.Token, error) {
	token, ok := r.byHash[tokenHash]
	if !ok {
		return identity.Token{}, identity.ErrTokenNotFound
	}
	return token, nil
}

func (r *stubTokenRepo) Revoke(_ context.Context, tokenHash string) error {
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

func (r *stubTokenRepo) RevokeAllForUser(_ context.Context, userID int64) error {
	for hash, token := range r.byHash {
		if token.UserID != userID || token.RevokedAt != nil {
			continue
		}
		revokedAt := fixedNow
		token.RevokedAt = &revokedAt
		r.byHash[hash] = token
	}
	return nil
}

func (r *stubTokenRepo) TouchLastSeen(context.Context, string, time.Time) error { return nil }
