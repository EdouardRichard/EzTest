package identityapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	identityapp "github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	identityapi "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

var handlerNow = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

type fakeService struct {
	loginSession identityapp.Session
	loginErr     error
	loginArgs    []string

	logoutErr  error
	logoutTok  string
	logoutCall int

	authenticateUser identity.User
	authenticateErr  error
	authToken        string

	changeErr  error
	changeArgs []string

	createUser identity.User
	createErr  error
	createArgs []string
	createCall int

	listUsers  []identity.User
	listMore   bool
	listErr    error
	listLimit  int
	listBefore *int64

	updateUser identity.User
	updateErr  error
	updateArgs []string

	resetErr  error
	resetArgs []string
}

func (f *fakeService) Login(_ context.Context, username, password, requestID, ip, ua string) (identityapp.Session, error) {
	f.loginArgs = []string{username, password, requestID, ip, ua}
	return f.loginSession, f.loginErr
}

func (f *fakeService) Logout(_ context.Context, token string) error {
	f.logoutTok = token
	f.logoutCall++
	return f.logoutErr
}

func (f *fakeService) Authenticate(_ context.Context, token string) (identity.User, error) {
	f.authToken = token
	return f.authenticateUser, f.authenticateErr
}

func (f *fakeService) ChangePassword(_ context.Context, userID int64, oldPassword, newPassword string) error {
	f.changeArgs = []string{strconv.FormatInt(userID, 10), oldPassword, newPassword}
	return f.changeErr
}

func (f *fakeService) CreateUser(_ context.Context, actorID int64, username, displayName, password string) (identity.User, error) {
	f.createCall++
	f.createArgs = []string{strconv.FormatInt(actorID, 10), username, displayName, password}
	return f.createUser, f.createErr
}

func (f *fakeService) ListUsers(_ context.Context, limit int, beforeID *int64) ([]identity.User, bool, error) {
	f.listLimit = limit
	f.listBefore = beforeID
	return f.listUsers, f.listMore, f.listErr
}

func (f *fakeService) UpdateUser(_ context.Context, actorID, userID int64, displayName, status string) (identity.User, error) {
	f.updateArgs = []string{strconv.FormatInt(actorID, 10), strconv.FormatInt(userID, 10), displayName, status}
	return f.updateUser, f.updateErr
}

func (f *fakeService) ResetPassword(_ context.Context, actorID, userID int64, password string) error {
	f.resetArgs = []string{strconv.FormatInt(actorID, 10), strconv.FormatInt(userID, 10), password}
	return f.resetErr
}

func sampleUser(id int64, username string, role identity.Role) identity.User {
	return identity.User{
		ID:          id,
		Username:    username,
		DisplayName: strings.ToUpper(username[:1]) + username[1:],
		Role:        role,
		Status:      identity.StatusActive,
		CreatedAt:   handlerNow,
		UpdatedAt:   handlerNow,
	}
}

func newTestHandler() (*identityapi.Handler, *fakeService) {
	svc := &fakeService{}
	svc.loginSession = identityapp.Session{
		Token:     "ezt_raw-token-value",
		ExpiresAt: handlerNow.Add(7 * 24 * time.Hour),
		User:      sampleUser(42, "admin", identity.RoleAdmin),
	}
	svc.authenticateUser = sampleUser(42, "admin", identity.RoleAdmin)
	svc.createUser = sampleUser(77, "qa01", identity.RoleUser)
	svc.updateUser = sampleUser(77, "qa01", identity.RoleUser)
	return identityapi.New(svc), svc
}

func withActor(request *http.Request, id int64, role string) *http.Request {
	return request.WithContext(platform.WithActor(request.Context(), platform.Actor{ID: id, Role: role}))
}

func postJSON(t *testing.T, path, body string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func decodeData(t *testing.T, rec *httptest.ResponseRecorder, target any) {
	t.Helper()
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v (body %s)", err, rec.Body.String())
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		t.Fatalf("decode data: %v (body %s)", err, rec.Body.String())
	}
}

func decodeErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v (body %s)", err, rec.Body.String())
	}
	return envelope.Error.Code
}

type userJSON struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func assertNoCredentials(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	body := rec.Body.String()
	for _, forbidden := range []string{"password_hash", "token_hash", `"password"`, "passwordhash"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaks %q: %s", forbidden, body)
		}
	}
}

func TestLoginReturnsTokenAndUserWithoutCredentials(t *testing.T) {
	handler, svc := newTestHandler()
	request := postJSON(t, "/api/auth/login", `{"username":" admin ","password":"admin123"}`)
	request.Header.Set("User-Agent", "handler-test")
	request.RemoteAddr = "192.0.2.7:54321"
	request = request.WithContext(requestid.With(request.Context(), "req_login_123"))
	rec := httptest.NewRecorder()

	handler.Login(rec, request)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var data struct {
		Token     string   `json:"token"`
		ExpiresAt string   `json:"expires_at"`
		User      userJSON `json:"user"`
	}
	decodeData(t, rec, &data)
	if data.Token != "ezt_raw-token-value" {
		t.Fatalf("token = %q", data.Token)
	}
	if data.ExpiresAt != "2026-11-07T12:00:00Z" {
		t.Fatalf("expires_at = %q", data.ExpiresAt)
	}
	if data.User.ID != "42" || data.User.Username != "admin" || data.User.Role != "admin" || data.User.Status != "active" {
		t.Fatalf("user = %#v", data.User)
	}
	if data.User.CreatedAt != "2026-10-31T12:00:00Z" {
		t.Fatalf("created_at = %q", data.User.CreatedAt)
	}
	assertNoCredentials(t, rec)

	wantArgs := []string{"admin", "admin123", "req_login_123", "192.0.2.7", "handler-test"}
	if len(svc.loginArgs) != len(wantArgs) {
		t.Fatalf("login args = %#v", svc.loginArgs)
	}
	for i := range wantArgs {
		if svc.loginArgs[i] != wantArgs[i] {
			t.Fatalf("login args[%d] = %q, want %q", i, svc.loginArgs[i], wantArgs[i])
		}
	}
}

func TestLoginRejectsInvalidBodyAndMapsUnauthenticated(t *testing.T) {
	handler, svc := newTestHandler()

	rec := httptest.NewRecorder()
	handler.Login(rec, postJSON(t, "/api/auth/login", `{"username":`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON status = %d", rec.Code)
	}
	if code := decodeErrorCode(t, rec); code != "invalid_argument" {
		t.Fatalf("invalid JSON code = %q", code)
	}

	rec = httptest.NewRecorder()
	handler.Login(rec, postJSON(t, "/api/auth/login", `{"username":"admin"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing password status = %d", rec.Code)
	}
	if svc.loginArgs != nil {
		t.Fatalf("service called for invalid body: %#v", svc.loginArgs)
	}

	svc.loginErr = platform.Unauthenticated()
	rec = httptest.NewRecorder()
	handler.Login(rec, postJSON(t, "/api/auth/login", `{"username":"admin","password":"wrong"}`))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", rec.Code)
	}
	if code := decodeErrorCode(t, rec); code != "unauthenticated" {
		t.Fatalf("code = %q", code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("WWW-Authenticate = %q", got)
	}
}

func TestMeReturnsCurrentUser(t *testing.T) {
	handler, svc := newTestHandler()
	request := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	request.Header.Set("Authorization", "Bearer ezt_session")
	rec := httptest.NewRecorder()

	handler.Me(rec, request)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var user userJSON
	decodeData(t, rec, &user)
	if user.ID != "42" || user.Username != "admin" || user.Role != "admin" {
		t.Fatalf("user = %#v", user)
	}
	if svc.authToken != "ezt_session" {
		t.Fatalf("authenticate token = %q", svc.authToken)
	}
	assertNoCredentials(t, rec)

	rec = httptest.NewRecorder()
	handler.Me(rec, httptest.NewRequest(http.MethodGet, "/api/auth/me", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer status = %d", rec.Code)
	}

	svc.authenticateErr = platform.Unauthenticated()
	request = httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	request.Header.Set("Authorization", "Bearer stale")
	rec = httptest.NewRecorder()
	handler.Me(rec, request)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("stale bearer status = %d", rec.Code)
	}
}

func TestLogoutRevokesBearerToken(t *testing.T) {
	handler, svc := newTestHandler()
	request := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	request.Header.Set("Authorization", "Bearer ezt_current")
	rec := httptest.NewRecorder()

	handler.Logout(rec, request)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("204 body = %q", rec.Body.String())
	}
	if svc.logoutTok != "ezt_current" || svc.logoutCall != 1 {
		t.Fatalf("logout token = %q calls = %d", svc.logoutTok, svc.logoutCall)
	}

	rec = httptest.NewRecorder()
	handler.Logout(rec, httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer status = %d", rec.Code)
	}

	svc.logoutErr = platform.Unauthenticated()
	request = httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	request.Header.Set("Authorization", "Bearer stale")
	rec = httptest.NewRecorder()
	handler.Logout(rec, request)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("stale bearer status = %d", rec.Code)
	}
}

func TestChangePasswordUsesAuthenticatedActor(t *testing.T) {
	handler, svc := newTestHandler()
	request := withActor(postJSON(t, "/api/auth/password", `{"old_password":"admin123","new_password":"new-password-1"}`), 42, "admin")
	rec := httptest.NewRecorder()

	handler.ChangePassword(rec, request)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	want := []string{"42", "admin123", "new-password-1"}
	for i := range want {
		if svc.changeArgs[i] != want[i] {
			t.Fatalf("change args = %#v", svc.changeArgs)
		}
	}

	rec = httptest.NewRecorder()
	handler.ChangePassword(rec, postJSON(t, "/api/auth/password", `{"old_password":"x","new_password":"new-password-1"}`))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing actor status = %d", rec.Code)
	}

	svc.changeErr = platform.Invalid("old_password", "old password is incorrect")
	request = withActor(postJSON(t, "/api/auth/password", `{"old_password":"bad","new_password":"new-password-1"}`), 42, "admin")
	rec = httptest.NewRecorder()
	handler.ChangePassword(rec, request)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong old password status = %d", rec.Code)
	}
	if code := decodeErrorCode(t, rec); code != "invalid_argument" {
		t.Fatalf("code = %q", code)
	}
}

func TestListUsersPaginatesAndEncodesIDsAsStrings(t *testing.T) {
	handler, svc := newTestHandler()
	svc.listUsers = []identity.User{sampleUser(9002, "qa02", identity.RoleUser), sampleUser(9001, "qa01", identity.RoleUser)}
	svc.listMore = true
	request := httptest.NewRequest(http.MethodGet, "/api/admin/users?limit=2&cursor=9010", nil)
	rec := httptest.NewRecorder()

	handler.ListUsers(rec, request)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data       []userJSON `json:"data"`
		NextCursor string     `json:"next_cursor"`
		HasMore    bool       `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Data) != 2 || envelope.Data[0].ID != "9002" || envelope.Data[1].ID != "9001" {
		t.Fatalf("data = %#v", envelope.Data)
	}
	if envelope.NextCursor != "9001" || !envelope.HasMore {
		t.Fatalf("cursor = %q has_more = %v", envelope.NextCursor, envelope.HasMore)
	}
	if svc.listLimit != 2 || svc.listBefore == nil || *svc.listBefore != 9010 {
		t.Fatalf("service args = %d %v", svc.listLimit, svc.listBefore)
	}
	assertNoCredentials(t, rec)

	rec = httptest.NewRecorder()
	handler.ListUsers(rec, httptest.NewRequest(http.MethodGet, "/api/admin/users?cursor=abc", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor status = %d", rec.Code)
	}
}

func TestListUsersReturnsEmptyArrayNotNull(t *testing.T) {
	handler, svc := newTestHandler()
	svc.listUsers = nil
	svc.listMore = false
	rec := httptest.NewRecorder()
	handler.ListUsers(rec, httptest.NewRequest(http.MethodGet, "/api/admin/users", nil))

	var envelope struct {
		Data       []userJSON `json:"data"`
		NextCursor string     `json:"next_cursor"`
		HasMore    bool       `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Data == nil || len(envelope.Data) != 0 {
		t.Fatalf("data = %#v, want []", envelope.Data)
	}
	if envelope.NextCursor != "" || envelope.HasMore {
		t.Fatalf("cursor = %q has_more = %v", envelope.NextCursor, envelope.HasMore)
	}
}

func TestListUsersMapsServiceErrors(t *testing.T) {
	handler, svc := newTestHandler()
	svc.listErr = platform.Forbidden()
	rec := httptest.NewRecorder()
	handler.ListUsers(rec, httptest.NewRequest(http.MethodGet, "/api/admin/users", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d", rec.Code)
	}
	if code := decodeErrorCode(t, rec); code != "forbidden" {
		t.Fatalf("code = %q", code)
	}
}

func TestCreateUserReturnsCreatedUser(t *testing.T) {
	handler, svc := newTestHandler()
	request := withActor(postJSON(t, "/api/admin/users", `{"username":"qa01","display_name":"QA 01","password":"qa-password"}`), 42, "admin")
	rec := httptest.NewRecorder()

	handler.CreateUser(rec, request)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var user userJSON
	decodeData(t, rec, &user)
	if user.ID != "77" || user.Username != "qa01" || user.Role != "user" {
		t.Fatalf("user = %#v", user)
	}
	want := []string{"42", "qa01", "QA 01", "qa-password"}
	for i := range want {
		if svc.createArgs[i] != want[i] {
			t.Fatalf("create args = %#v", svc.createArgs)
		}
	}
	assertNoCredentials(t, rec)

	svc.createErr = platform.Conflict("username already exists")
	request = withActor(postJSON(t, "/api/admin/users", `{"username":"qa01","password":"qa-password"}`), 42, "admin")
	rec = httptest.NewRecorder()
	handler.CreateUser(rec, request)
	if rec.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.CreateUser(rec, postJSON(t, "/api/admin/users", `{"username":"qa01","password":"qa-password"}`))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing actor status = %d", rec.Code)
	}
}

func TestUpdateUserPatchesDisplayNameAndStatus(t *testing.T) {
	handler, svc := newTestHandler()
	svc.updateUser = sampleUser(77, "qa01", identity.RoleUser)
	svc.updateUser.Status = identity.StatusDisabled
	request := withActor(postJSON(t, "/api/admin/users/77", `{"display_name":"QA Renamed","status":"disabled"}`), 42, "admin")
	request.SetPathValue("id", "77")
	rec := httptest.NewRecorder()

	handler.UpdateUser(rec, request)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var user userJSON
	decodeData(t, rec, &user)
	if user.ID != "77" || user.Status != "disabled" {
		t.Fatalf("user = %#v", user)
	}
	want := []string{"42", "77", "QA Renamed", "disabled"}
	for i := range want {
		if svc.updateArgs[i] != want[i] {
			t.Fatalf("update args = %#v", svc.updateArgs)
		}
	}

	request = withActor(postJSON(t, "/api/admin/users/abc", `{"status":"disabled"}`), 42, "admin")
	request.SetPathValue("id", "abc")
	rec = httptest.NewRecorder()
	handler.UpdateUser(rec, request)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid id status = %d", rec.Code)
	}

	svc.updateErr = platform.NotFound("user")
	request = withActor(postJSON(t, "/api/admin/users/999", `{"status":"disabled"}`), 42, "admin")
	request.SetPathValue("id", "999")
	rec = httptest.NewRecorder()
	handler.UpdateUser(rec, request)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("not found status = %d", rec.Code)
	}

	request = withActor(postJSON(t, "/api/admin/users/77", `{}`), 42, "admin")
	request.SetPathValue("id", "77")
	rec = httptest.NewRecorder()
	handler.UpdateUser(rec, request)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty patch status = %d", rec.Code)
	}
}

func TestUpdateUserForbiddenMapsTo403(t *testing.T) {
	handler, svc := newTestHandler()
	svc.updateErr = platform.Forbidden()
	request := withActor(postJSON(t, "/api/admin/users/77", `{"status":"active"}`), 9, "user")
	request.SetPathValue("id", "77")
	rec := httptest.NewRecorder()

	handler.UpdateUser(rec, request)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d", rec.Code)
	}
	if code := decodeErrorCode(t, rec); code != "forbidden" {
		t.Fatalf("code = %q", code)
	}
}

func TestResetPasswordRevokesAndReturnsNoContent(t *testing.T) {
	handler, svc := newTestHandler()
	request := withActor(postJSON(t, "/api/admin/users/77/reset-password", `{"new_password":"reset-password"}`), 42, "admin")
	request.SetPathValue("id", "77")
	rec := httptest.NewRecorder()

	handler.ResetPassword(rec, request)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	want := []string{"42", "77", "reset-password"}
	for i := range want {
		if svc.resetArgs[i] != want[i] {
			t.Fatalf("reset args = %#v", svc.resetArgs)
		}
	}

	svc.resetErr = platform.NotFound("user")
	request = withActor(postJSON(t, "/api/admin/users/999/reset-password", `{"new_password":"reset-password"}`), 42, "admin")
	request.SetPathValue("id", "999")
	rec = httptest.NewRecorder()
	handler.ResetPassword(rec, request)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("not found status = %d", rec.Code)
	}

	request = withActor(postJSON(t, "/api/admin/users/abc/reset-password", `{"new_password":"reset-password"}`), 42, "admin")
	request.SetPathValue("id", "abc")
	rec = httptest.NewRecorder()
	handler.ResetPassword(rec, request)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid id status = %d", rec.Code)
	}
}

func TestRegisterMountsRoutesBehindMiddleware(t *testing.T) {
	handler, _ := newTestHandler()
	mux := http.NewServeMux()
	authenticated := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer user" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 42, Role: "user"})))
		})
	}
	admin := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("Authorization") {
			case "Bearer admin":
				next.ServeHTTP(w, r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 42, Role: "admin"})))
			case "Bearer user":
				w.WriteHeader(http.StatusForbidden)
			default:
				w.WriteHeader(http.StatusUnauthorized)
			}
		})
	}
	handler.Register(mux, authenticated, admin)
	server := httptest.NewServer(mux)
	defer server.Close()

	send := func(method, path, authorization, body string) int {
		t.Helper()
		var reader *bytes.Reader
		if body == "" {
			reader = bytes.NewReader(nil)
		} else {
			reader = bytes.NewReader([]byte(body))
		}
		request, err := http.NewRequest(method, server.URL+path, reader)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}

	if status := send(http.MethodPost, "/api/auth/login", "", `{"username":"admin","password":"admin123"}`); status != http.StatusOK {
		t.Fatalf("login status = %d", status)
	}
	if status := send(http.MethodGet, "/api/auth/me", "Bearer user", ""); status != http.StatusOK {
		t.Fatalf("me status = %d", status)
	}
	if status := send(http.MethodGet, "/api/auth/me", "", ""); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated me status = %d", status)
	}
	if status := send(http.MethodGet, "/api/admin/users", "Bearer admin", ""); status != http.StatusOK {
		t.Fatalf("admin users status = %d", status)
	}
	if status := send(http.MethodGet, "/api/admin/users", "Bearer user", ""); status != http.StatusForbidden {
		t.Fatalf("non-admin users status = %d", status)
	}
	if status := send(http.MethodGet, "/api/auth/login", "", ""); status != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method status = %d", status)
	}
	if status := send(http.MethodGet, "/api/does-not-exist", "", ""); status != http.StatusNotFound {
		t.Fatalf("unknown route status = %d", status)
	}
}

func TestDTONeverExposesPasswordHash(t *testing.T) {
	handler, svc := newTestHandler()
	svc.loginSession.User.PasswordHash = "super-secret-hash"

	rec := httptest.NewRecorder()
	handler.Login(rec, postJSON(t, "/api/auth/login", `{"username":"admin","password":"admin123"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "super-secret-hash") {
		t.Fatalf("login response leaks the password hash: %s", rec.Body.String())
	}

	svc.createUser.PasswordHash = "super-secret-hash"
	request := withActor(postJSON(t, "/api/admin/users", `{"username":"qa01","password":"qa-password"}`), 42, "admin")
	rec = httptest.NewRecorder()
	handler.CreateUser(rec, request)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "super-secret-hash") {
		t.Fatalf("create response leaks the password hash: %s", rec.Body.String())
	}
}
