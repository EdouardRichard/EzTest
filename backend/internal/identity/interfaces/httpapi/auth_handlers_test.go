package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
)

var fixedNow = time.Date(2026, 10, 27, 3, 0, 0, 0, time.UTC)

const (
	adminToken = "ezt_admin-token"
	userToken  = "ezt_user-token"
	adminPass  = "admin-pass-123"
	userPass   = "qa-pass-1234"
)

func TestLoginAndMe(t *testing.T) {
	h := newTestAPI(t) // 注入 fake AuthService
	rec := postJSON(h, "/api/auth/login", `{"username":"admin","password":"admin-pass-123"}`)
	if rec.Code != 200 {
		t.Fatalf("login=%d %s", rec.Code, rec.Body)
	}
	token := decodeToken(t, rec.Body)
	rec = getWithBearer(h, "/api/auth/me", token)
	if rec.Code != 200 {
		t.Fatalf("me=%d", rec.Code)
	}
}

func TestAdminEndpointForbiddenForUser(t *testing.T) {
	h := newTestAPI(t)

	rec := postJSON(h, "/api/auth/login", `{"username":"qa01","password":"qa-pass-1234"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("user login=%d %s", rec.Code, rec.Body)
	}
	user := decodeToken(t, rec.Body)
	if rec := getWithBearer(h, "/api/admin/ping", user); rec.Code != http.StatusForbidden {
		t.Fatalf("user token on admin endpoint=%d %s, want 403", rec.Code, rec.Body)
	}

	rec = postJSON(h, "/api/auth/login", `{"username":"admin","password":"admin-pass-123"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin login=%d %s", rec.Code, rec.Body)
	}
	admin := decodeToken(t, rec.Body)
	if rec := getWithBearer(h, "/api/admin/ping", admin); rec.Code != http.StatusOK {
		t.Fatalf("admin token on admin endpoint=%d %s, want 200", rec.Code, rec.Body)
	}

	if rec := getWithBearer(h, "/api/admin/ping", "ezt_bogus"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bogus token on admin endpoint=%d, want 401", rec.Code)
	}
}

func TestMeReturnsCurrentUser(t *testing.T) {
	h := newTestAPI(t)
	rec := postJSON(h, "/api/auth/login", `{"username":"admin","password":"admin-pass-123"}`)
	token := decodeToken(t, rec.Body)

	rec = getWithBearer(h, "/api/auth/me", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("me=%d %s", rec.Code, rec.Body)
	}
	var body struct {
		Data struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
			Role     string `json:"role"`
			Status   string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body=%q: %v", rec.Body, err)
	}
	if body.Data.ID != 1 || body.Data.Username != "admin" || body.Data.Role != "admin" || body.Data.Status != "active" {
		t.Fatalf("unexpected me payload: %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("me response leaks a password field: %s", rec.Body)
	}
}

func TestMeRequiresBearerToken(t *testing.T) {
	h := newTestAPI(t)
	for _, header := range []string{"", "Bearer", "Bearer ", "Token " + userToken, "Basic YWRtaW46YWRtaW4="} {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("authorization %q: code=%d want 401", header, rec.Code)
		}
		if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
			t.Fatalf("authorization %q: WWW-Authenticate=%q want Bearer", header, got)
		}
		if code := decodeErrorCode(t, rec.Body.Bytes()); code != "unauthenticated" {
			t.Fatalf("authorization %q: error code=%q want unauthenticated", header, code)
		}
	}
}

func TestLoginRejectsInvalidCredentials(t *testing.T) {
	h := newTestAPI(t)
	for _, body := range []string{
		`{"username":"admin","password":"wrong-password"}`,
		`{"username":"nobody","password":"admin-pass-123"}`,
		`{"username":"","password":""}`,
	} {
		rec := postJSON(h, "/api/auth/login", body)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("login %s: code=%d body=%s want 401", body, rec.Code, rec.Body)
		}
		if code := decodeErrorCode(t, rec.Body.Bytes()); code != "unauthenticated" {
			t.Fatalf("login %s: error code=%q", body, code)
		}
	}
}

func TestLoginRejectsMalformedJSON(t *testing.T) {
	h := newTestAPI(t)
	rec := postJSON(h, "/api/auth/login", "{")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: code=%d body=%s want 400", rec.Code, rec.Body)
	}
	if code := decodeErrorCode(t, rec.Body.Bytes()); code != "invalid_argument" {
		t.Fatalf("malformed body: error code=%q", code)
	}
}

func TestLoginInternalErrorIsNotUnauthenticated(t *testing.T) {
	h := newTestAPI(t)
	h.auth.loginErr = errors.New("database down")
	rec := postJSON(h, "/api/auth/login", `{"username":"admin","password":"admin-pass-123"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("internal failure: code=%d body=%s want 500", rec.Code, rec.Body)
	}
	if code := decodeErrorCode(t, rec.Body.Bytes()); code != "internal" {
		t.Fatalf("internal failure: error code=%q", code)
	}
}

func TestLogoutRevokesCurrentToken(t *testing.T) {
	h := newTestAPI(t)
	rec := postJSON(h, "/api/auth/login", `{"username":"admin","password":"admin-pass-123"}`)
	token := decodeToken(t, rec.Body)

	rec = postWithBearer(h, "/api/auth/logout", token)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout=%d %s want 204", rec.Code, rec.Body)
	}
	if h.auth.lastLogoutToken != token {
		t.Fatalf("logout revoked %q want %q", h.auth.lastLogoutToken, token)
	}
	if rec := getWithBearer(h, "/api/auth/me", token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token still valid after logout: code=%d", rec.Code)
	}
}

func TestLogoutRequiresAuthentication(t *testing.T) {
	h := newTestAPI(t)
	rec := postWithBearer(h, "/api/auth/logout", "ezt_unknown")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("logout without a valid token=%d want 401", rec.Code)
	}
}

func TestLoginRateLimitedAfterFiveFailures(t *testing.T) {
	h := newTestAPI(t)
	for i := 0; i < 5; i++ {
		rec := postJSON(h, "/api/auth/login", `{"username":"admin","password":"wrong-password"}`)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: code=%d body=%s", i+1, rec.Code, rec.Body)
		}
	}
	if h.auth.loginCalls != 5 {
		t.Fatalf("login calls=%d want 5", h.auth.loginCalls)
	}

	rec := postJSON(h, "/api/auth/login", `{"username":"admin","password":"admin-pass-123"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("locked login: code=%d body=%s want 429", rec.Code, rec.Body)
	}
	if code := decodeErrorCode(t, rec.Body.Bytes()); code != "rate_limited" {
		t.Fatalf("locked login: error code=%q want rate_limited", code)
	}
	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After=%q want 60", got)
	}
	if h.auth.loginCalls != 5 {
		t.Fatalf("locked request reached the auth service: calls=%d", h.auth.loginCalls)
	}

	// Once the one-minute lock elapses, valid credentials work again.
	h.advance(time.Minute + time.Second)
	rec = postJSON(h, "/api/auth/login", `{"username":"admin","password":"admin-pass-123"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login after lock expiry: code=%d body=%s", rec.Code, rec.Body)
	}
}

func TestLoginLimiterWindowAndLock(t *testing.T) {
	now := fixedNow
	limiter := NewLoginLimiter(func() time.Time { return now })
	key := "192.0.2.1|admin"

	for i := 0; i < 4; i++ {
		if limiter.Locked(key) {
			t.Fatalf("locked after %d failures", i)
		}
		limiter.Fail(key)
	}
	if limiter.Locked(key) {
		t.Fatal("locked after 4 failures, want the 5th to trigger the lock")
	}
	limiter.Fail(key)
	if !limiter.Locked(key) {
		t.Fatal("not locked after 5 failures")
	}
	now = now.Add(59 * time.Second)
	if !limiter.Locked(key) {
		t.Fatal("lock expired before one minute")
	}
	now = now.Add(2 * time.Second)
	if limiter.Locked(key) {
		t.Fatal("lock did not expire after one minute")
	}

	// The old window is discarded: the next failure starts a fresh count.
	limiter.Fail(key)
	if limiter.Locked(key) {
		t.Fatal("failure after lock expiry reused the old window")
	}
	limiter.Reset(key)
	if limiter.Locked(key) {
		t.Fatal("Reset did not clear the limiter entry")
	}
}

// fakeAuthService is an in-memory AuthService for HTTP tests. It issues fixed
// tokens per account so tests can assert role behaviour without a database.
type fakeAuthService struct {
	loginCalls      int
	lastLogoutToken string
	loginErr        error
	tokens          map[string]identity.User
}

func newFakeAuthService() *fakeAuthService {
	return &fakeAuthService{tokens: map[string]identity.User{
		adminToken: {ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive},
		userToken:  {ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive},
	}}
}

func (f *fakeAuthService) Login(_ context.Context, username, password string) (application.LoginResult, error) {
	f.loginCalls++
	if f.loginErr != nil {
		return application.LoginResult{}, f.loginErr
	}
	switch {
	case username == "admin" && password == adminPass:
		return application.LoginResult{Token: adminToken, ExpiresAt: fixedNow.Add(identity.TokenTTL), User: f.tokens[adminToken]}, nil
	case username == "qa01" && password == userPass:
		return application.LoginResult{Token: userToken, ExpiresAt: fixedNow.Add(identity.TokenTTL), User: f.tokens[userToken]}, nil
	default:
		return application.LoginResult{}, identity.ErrUnauthenticated
	}
}

func (f *fakeAuthService) Authenticate(_ context.Context, rawToken string) (identity.User, error) {
	user, ok := f.tokens[rawToken]
	if !ok {
		return identity.User{}, identity.ErrUnauthenticated
	}
	return user, nil
}

func (f *fakeAuthService) Logout(_ context.Context, rawToken string) error {
	f.lastLogoutToken = rawToken
	delete(f.tokens, rawToken)
	return nil
}

// testAPI is the HTTP surface under test: the production auth routes plus an
// admin-only probe endpoint so RequireAdmin can be exercised before Task 7 adds
// real user endpoints.
type testAPI struct {
	http.Handler
	auth *fakeAuthService
	now  *time.Time
}

func newTestAPI(t *testing.T) *testAPI {
	t.Helper()
	now := fixedNow
	auth := newFakeAuthService()
	mux := http.NewServeMux()
	RegisterAuthRoutes(mux, auth, NewLoginLimiter(func() time.Time { return now }))
	mux.Handle("GET /api/admin/ping", AuthMiddleware(auth)(RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))))
	return &testAPI{Handler: mux, auth: auth, now: &now}
}

func (api *testAPI) advance(d time.Duration) {
	*api.now = api.now.Add(d)
}

func postJSON(h http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func getWithBearer(h http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func postWithBearer(h http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeToken(t *testing.T, body io.Reader) string {
	t.Helper()
	var payload struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if payload.Data.Token == "" {
		t.Fatal("login response has no token")
	}
	return payload.Data.Token
}

func decodeErrorCode(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode error response %q: %v", body, err)
	}
	return payload.Error.Code
}
