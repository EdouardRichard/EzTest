package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/config"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/idempotency"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

type deadlinePinger struct {
	called   bool
	deadline time.Time
}

func (p *deadlinePinger) Ping(ctx context.Context) error {
	p.called = true
	p.deadline, _ = ctx.Deadline()
	return nil
}

func TestAPIHealth(t *testing.T) {
	h := New(config.Config{Env: "test"}, testLogger(), Dependencies{Pinger: fakePinger{}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type=%q", ct)
	}
	var body struct {
		Data struct {
			Status        string `json:"status"`
			Version       string `json:"version"`
			Commit        string `json:"commit"`
			DB            string `json:"db"`
			LLMConfigured bool   `json:"llm_configured"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body=%q: %v", rec.Body.String(), err)
	}
	if body.Data.Status != "ok" || body.Data.DB != "ok" {
		t.Fatalf("unexpected health payload: %s", rec.Body)
	}
	if body.Data.Version != "dev" || body.Data.Commit != "" {
		t.Fatalf("version=%q commit=%q", body.Data.Version, body.Data.Commit)
	}
	if body.Data.LLMConfigured {
		t.Fatal("llm_configured must be false before LLM configuration exists")
	}
}

func TestAPIHealthDBDown(t *testing.T) {
	h := New(config.Config{Env: "test"}, testLogger(), Dependencies{Pinger: fakePinger{err: errors.New("down")}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d", rec.Code)
	}
	var body struct {
		Data struct {
			Status string `json:"status"`
			DB     string `json:"db"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body=%q: %v", rec.Body.String(), err)
	}
	if body.Data.Status != "unavailable" {
		t.Fatalf("status=%q body=%s", body.Data.Status, rec.Body)
	}
}

func TestAPIHealthPingTimeout(t *testing.T) {
	pinger := &deadlinePinger{}
	h := New(config.Config{Env: "test"}, testLogger(), Dependencies{Pinger: pinger})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if !pinger.called {
		t.Fatal("Ping was not called")
	}
	if pinger.deadline.IsZero() {
		t.Fatal("Ping context has no deadline")
	}
	remaining := time.Until(pinger.deadline)
	if remaining < time.Second || remaining > 3*time.Second {
		t.Fatalf("Ping deadline %v is not the expected ~2s timeout", remaining)
	}
}

func TestHealthzAndRequestID(t *testing.T) {
	h := New(config.Config{Env: "test"}, slog.New(slog.NewTextHandler(io.Discard, nil)), Dependencies{})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Fatal("missing request id")
	}
	if got := rec.Header().Get("X-Request-ID"); got != req.Header.Get("X-Request-ID") && req.Header.Get("X-Request-ID") != "" {
		t.Fatalf("request id mismatch")
	}
}

func TestHealthzReturnsOKJSON(t *testing.T) {
	h := New(config.Config{Env: "test"}, testLogger(), Dependencies{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type=%q", ct)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body=%q: %v", rec.Body.String(), err)
	}
	if body.Status != "ok" {
		t.Fatalf("status=%q", body.Status)
	}
}

func TestRequestIDValidation(t *testing.T) {
	h := New(config.Config{Env: "test"}, testLogger(), Dependencies{})

	const valid = "client-request_id_1234"
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-ID", valid)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Request-ID"); got != valid {
		t.Fatalf("valid request id not echoed: got %q", got)
	}

	invalid := []string{
		"short",
		"bad request id!",
		strings.Repeat("a", 129),
		"",
	}
	for _, value := range invalid {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Header.Set("X-Request-ID", value)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		got := rec.Header().Get("X-Request-ID")
		if got == "" {
			t.Fatalf("missing request id for input %q", value)
		}
		if value != "" && got == value {
			t.Fatalf("invalid request id %q was echoed", value)
		}
		if !requestIDPattern.MatchString(got) {
			t.Fatalf("generated request id %q does not match the allowed pattern", got)
		}
	}
}

func TestRecoverMiddlewareReturns500(t *testing.T) {
	h := recoverMiddleware(testLogger())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d", rec.Code)
	}
}

// TestPanicResponseUsesSharedErrorEnvelope proves the recover middleware writes
// the same error envelope as handlers (code, request_id, retryable, details)
// and that its request ID matches the X-Request-ID header and the panic log.
func TestPanicResponseUsesSharedErrorEnvelope(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	h := recoverMiddleware(logger)(requestIDMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})))

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	req.Header.Set("X-Request-ID", "req_panic_1234")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type=%q", ct)
	}
	var raw struct {
		Error struct {
			Code      string          `json:"code"`
			Message   string          `json:"message"`
			RequestID string          `json:"request_id"`
			Retryable bool            `json:"retryable"`
			Details   json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body=%q: %v", rec.Body.String(), err)
	}
	if raw.Error.Code != platform.CodeInternal {
		t.Fatalf("code=%q want %s", raw.Error.Code, platform.CodeInternal)
	}
	if raw.Error.RequestID != "req_panic_1234" {
		t.Fatalf("request_id=%q want req_panic_1234", raw.Error.RequestID)
	}
	if raw.Error.RequestID != rec.Header().Get(requestIDHeader) {
		t.Fatalf("envelope request_id=%q, header=%q", raw.Error.RequestID, rec.Header().Get(requestIDHeader))
	}
	if raw.Error.Retryable {
		t.Fatal("panic response must not be retryable")
	}
	if string(raw.Error.Details) != "{}" {
		t.Fatalf("details=%s, want {}", raw.Error.Details)
	}
	if got := logField(logs.String(), "request_id"); got != raw.Error.RequestID {
		t.Fatalf("panic log request_id=%q, envelope request_id=%q", got, raw.Error.RequestID)
	}
}

// TestRequestIDMiddlewareStoresPlatformContext proves the server middleware and
// platform.WriteError share one request ID source.
func TestRequestIDMiddlewareStoresPlatformContext(t *testing.T) {
	var seen string
	h := requestIDMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = platform.RequestID(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if seen == "" {
		t.Fatal("platform.RequestID is empty inside the handler")
	}
	if seen != rec.Header().Get(requestIDHeader) {
		t.Fatalf("context request id=%q, response header=%q", seen, rec.Header().Get(requestIDHeader))
	}
}

func TestLoggingMiddlewarePreservesFlusher(t *testing.T) {
	h := loggingMiddleware(testLogger())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("logging middleware hides http.Flusher")
		}
		flusher.Flush()
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stream", nil))
	if !rec.Flushed {
		t.Fatal("Flush did not reach the underlying writer")
	}
}

// TestPanicLogAndResponseShareRequestID is the regression test for the panic
// log reporting an empty request ID even though the response carried one.
// It composes the middleware in production order (recover -> requestID).
func TestPanicLogAndResponseShareRequestID(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	h := recoverMiddleware(logger)(requestIDMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	responseID := rec.Header().Get(requestIDHeader)
	if responseID == "" {
		t.Fatal("panic response missing X-Request-ID")
	}
	loggedID := logField(logs.String(), "request_id")
	if loggedID == "" {
		t.Fatalf("panic log has no request_id (response header had %q): %s", responseID, logs.String())
	}
	if loggedID != responseID {
		t.Fatalf("panic log request_id=%q, response header request_id=%q", loggedID, responseID)
	}
}

// logField extracts the first text-handler field value for key from a log.
func logField(output, key string) string {
	for _, field := range strings.Fields(output) {
		if value, ok := strings.CutPrefix(field, key+"="); ok {
			return strings.Trim(value, `"`)
		}
	}
	return ""
}

func TestLoggingRecordsRequestWithoutSensitiveHeaders(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	h := New(config.Config{Env: "test"}, logger, Dependencies{})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Authorization", "Bearer super-secret-token")
	req.Header.Set("X-Request-ID", "req_12345678")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	out := buf.String()
	for _, want := range []string{"method=GET", "path=/healthz", "status=200", "request_id=req_12345678"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "super-secret-token") || strings.Contains(out, "Authorization") {
		t.Fatalf("log leaked sensitive request header: %s", out)
	}
}

// stubAuth is an AuthService that rejects every login. It only proves that the
// server exposes the auth routes when the dependency is wired.
type stubAuth struct{}

func (stubAuth) Login(context.Context, string, string) (application.LoginResult, error) {
	return application.LoginResult{}, identity.ErrUnauthenticated
}

func (stubAuth) Authenticate(context.Context, string) (identity.User, error) {
	return identity.User{}, identity.ErrUnauthenticated
}

func (stubAuth) Logout(context.Context, string) error { return nil }

// acceptingAuth authenticates exactly one bearer token. It implements
// identityhttp.AuthService so it can be wired into Dependencies.Auth.
type acceptingAuth struct {
	token string
	user  identity.User
}

func (a acceptingAuth) Login(context.Context, string, string) (application.LoginResult, error) {
	return application.LoginResult{}, identity.ErrUnauthenticated
}

func (a acceptingAuth) Authenticate(_ context.Context, rawToken string) (identity.User, error) {
	if rawToken != a.token {
		return identity.User{}, identity.ErrUnauthenticated
	}
	return a.user, nil
}

func (a acceptingAuth) Logout(context.Context, string) error { return nil }

// memoryIdempotencyStore is an in-memory idempotency.Store for middleware-chain
// tests.
type memoryIdempotencyStore struct {
	mu      sync.Mutex
	entries map[string]idempotency.StoredResponse
}

func newMemoryIdempotencyStore() *memoryIdempotencyStore {
	return &memoryIdempotencyStore{entries: make(map[string]idempotency.StoredResponse)}
}

func (s *memoryIdempotencyStore) Get(_ context.Context, userID int64, key, method, path string) (*idempotency.StoredResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[fmt.Sprintf("%d|%s|%s|%s", userID, key, method, path)]
	if !ok {
		return nil, nil
	}
	return &entry, nil
}

func (s *memoryIdempotencyStore) Save(_ context.Context, userID int64, key, method, path, requestHash string, status int, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[fmt.Sprintf("%d|%s|%s|%s", userID, key, method, path)] = idempotency.StoredResponse{
		Status:      status,
		Body:        append([]byte(nil), body...),
		RequestHash: requestHash,
	}
	return nil
}

// TestMiddlewareChainAuthenticatesBeforeIdempotency pins the production order:
// auth runs first, so idempotency sees the authenticated user and a retried
// write is replayed instead of re-executed.
func TestMiddlewareChainAuthenticatesBeforeIdempotency(t *testing.T) {
	mux := http.NewServeMux()
	var calls int32
	mux.HandleFunc("POST /api/things", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":1}}`))
	})
	deps := Dependencies{
		Auth: acceptingAuth{
			token: "ezt_valid",
			user:  identity.User{ID: 7, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive},
		},
		Idempotency: newMemoryIdempotencyStore(),
	}
	h := withMiddleware(mux, config.Config{Env: "test", WebOrigin: "http://localhost:6868"}, deps, testLogger())

	newReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/api/things", strings.NewReader(`{"name":"x"}`))
		req.Header.Set("Authorization", "Bearer ezt_valid")
		req.Header.Set("Idempotency-Key", "key_12345678")
		return req
	}
	first := httptest.NewRecorder()
	h.ServeHTTP(first, newReq())
	if first.Code != http.StatusCreated {
		t.Fatalf("first code=%d body=%s", first.Code, first.Body)
	}
	second := httptest.NewRecorder()
	h.ServeHTTP(second, newReq())
	if second.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatal("retried write was not replayed; idempotency ran before authentication")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("handler calls=%d, want 1", got)
	}
}

func TestMiddlewareChainRequiresAuthForProtectedAPIRoutes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/things", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	deps := Dependencies{Auth: acceptingAuth{token: "ezt_valid"}}
	h := withMiddleware(mux, config.Config{Env: "test"}, deps, testLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/things", nil)
	req.Header.Set("X-Request-ID", "req_auth_chain_1234")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d body=%s, want 401", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("WWW-Authenticate=%q", got)
	}
	assertAuthErrorEnvelope(t, rec, platform.CodeUnauthenticated, "req_auth_chain_1234")
}

// TestMiddlewareChainForbiddenUsesSharedErrorEnvelope covers the RequireAdmin
// 403 that the global auth chain now also produces for a non-admin caller.
func TestMiddlewareChainForbiddenUsesSharedErrorEnvelope(t *testing.T) {
	auth := acceptingAuth{
		token: "ezt_valid",
		user:  identity.User{ID: 7, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive},
	}
	mux := http.NewServeMux()
	mux.Handle("GET /api/admin/things", identityhttp.AuthMiddleware(auth)(identityhttp.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))))
	h := withMiddleware(mux, config.Config{Env: "test"}, Dependencies{Auth: auth}, testLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/admin/things", nil)
	req.Header.Set("Authorization", "Bearer ezt_valid")
	req.Header.Set("X-Request-ID", "req_forbidden_1234")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d body=%s, want 403", rec.Code, rec.Body)
	}
	assertAuthErrorEnvelope(t, rec, platform.CodeForbidden, "req_forbidden_1234")
}

// assertAuthErrorEnvelope proves an auth failure uses the design's §9.1
// envelope: every field present, request_id non-empty and equal to the
// X-Request-ID response header (which the request ID middleware set).
func assertAuthErrorEnvelope(t *testing.T, rec *httptest.ResponseRecorder, wantCode, wantRequestID string) {
	t.Helper()
	var payload struct {
		Error struct {
			Code      string          `json:"code"`
			Message   string          `json:"message"`
			RequestID *string         `json:"request_id"`
			Retryable *bool           `json:"retryable"`
			Details   json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error response %q: %v", rec.Body.Bytes(), err)
	}
	if payload.Error.Code != wantCode {
		t.Fatalf("error code=%q want %q", payload.Error.Code, wantCode)
	}
	if payload.Error.Message == "" {
		t.Fatal("error envelope has no message")
	}
	if payload.Error.RequestID == nil || *payload.Error.RequestID == "" {
		t.Fatalf("error envelope has no request_id: %s", rec.Body)
	}
	if *payload.Error.RequestID != wantRequestID {
		t.Fatalf("envelope request_id=%q want %q", *payload.Error.RequestID, wantRequestID)
	}
	if got := rec.Header().Get(platform.RequestIDHeader); got != *payload.Error.RequestID {
		t.Fatalf("envelope request_id=%q, X-Request-ID header=%q", *payload.Error.RequestID, got)
	}
	if payload.Error.Retryable == nil {
		t.Fatalf("error envelope has no retryable field: %s", rec.Body)
	}
	if string(payload.Error.Details) != "{}" {
		t.Fatalf("error envelope details=%s, want {}: %s", payload.Error.Details, rec.Body)
	}
}

func TestMiddlewareChainKeepsPublicRoutesPublic(t *testing.T) {
	deps := Dependencies{Pinger: fakePinger{}, Auth: acceptingAuth{token: "ezt_valid"}}

	// The health routes are registered by New; use a local mux that mirrors the
	// public routes to exercise the auth carve-outs without the full server.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /api/health", handleAPIHealth(deps.Pinger))
	mux.HandleFunc("GET /api/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := withMiddleware(mux, config.Config{Env: "test"}, deps, testLogger())

	for _, path := range []string{"/healthz", "/api/health", "/api/openapi.yaml"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code=%d body=%s, want 200 without a token", path, rec.Code, rec.Body)
		}
	}
}

func TestMiddlewareChainAppliesCORSPreflightBeforeAuth(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/things", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("preflight must not reach the route")
	})
	deps := Dependencies{Auth: acceptingAuth{token: "ezt_valid"}}
	h := withMiddleware(mux, config.Config{Env: "test", WebOrigin: "http://localhost:6868"}, deps, testLogger())

	req := httptest.NewRequest(http.MethodOptions, "/api/things", nil)
	req.Header.Set("Origin", "http://localhost:6868")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("code=%d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:6868" {
		t.Fatalf("Access-Control-Allow-Origin=%q", got)
	}
}

func TestMiddlewareChainPanicEnvelopeHasRequestID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/boom", func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})
	h := withMiddleware(mux, config.Config{Env: "test"}, Dependencies{Auth: acceptingAuth{token: "ezt_valid"}}, testLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/boom", nil)
	req.Header.Set("Authorization", "Bearer ezt_valid")
	req.Header.Set("X-Request-ID", "req_chain_1234")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d", rec.Code)
	}
	var body struct {
		Error struct {
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body=%q: %v", rec.Body.String(), err)
	}
	if body.Error.Code != platform.CodeInternal || body.Error.RequestID != "req_chain_1234" {
		t.Fatalf("envelope=%+v", body.Error)
	}
}

func TestAuthRoutesWired(t *testing.T) {
	h := New(config.Config{Env: "test"}, testLogger(), Dependencies{Auth: stubAuth{}})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"nope"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("login=%d body=%s want 401", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/me", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("me=%d want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("WWW-Authenticate=%q want Bearer", got)
	}
}
