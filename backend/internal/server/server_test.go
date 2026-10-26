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

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/config"
	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	eventinginfra "github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	llm "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	llmhttp "github.com/EdouardRichard/EzTest/backend/internal/llm/interfaces/httpapi"
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
	mux.HandleFunc("GET /api/health", handleAPIHealth(deps.Pinger, nil))
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

// stubUserService is a minimal identityhttp.UserService used to prove the
// server exposes the admin user routes with the global auth and per-route
// RequireAdmin guard.
type stubUserService struct{ users []identity.User }

func (s stubUserService) Create(context.Context, string, string) (identity.User, error) {
	return identity.User{ID: 3, Username: "qa02", Role: identity.RoleUser, Status: identity.StatusActive}, nil
}

func (s stubUserService) List(context.Context, int, int64) ([]identity.User, bool, error) {
	return s.users, false, nil
}

func (s stubUserService) Get(_ context.Context, id int64) (identity.User, error) {
	for _, user := range s.users {
		if user.ID == id {
			return user, nil
		}
	}
	return identity.User{}, identity.ErrUserNotFound
}

func (s stubUserService) SetStatus(_ context.Context, _, userID int64, status identity.Status) (identity.User, error) {
	return identity.User{ID: userID, Status: status}, nil
}

func (s stubUserService) ResetPassword(context.Context, int64, string) error { return nil }

// tokenMapAuth authenticates a fixed set of bearer tokens so the server wiring
// test can exercise both the admin and the regular-user paths.
type tokenMapAuth map[string]identity.User

func (a tokenMapAuth) Login(context.Context, string, string) (application.LoginResult, error) {
	return application.LoginResult{}, identity.ErrUnauthenticated
}

func (a tokenMapAuth) Authenticate(_ context.Context, rawToken string) (identity.User, error) {
	user, ok := a[rawToken]
	if !ok {
		return identity.User{}, identity.ErrUnauthenticated
	}
	return user, nil
}

func (a tokenMapAuth) Logout(context.Context, string) error { return nil }

// TestUserAndAuditRoutesWired proves the composition root mounts the user and
// audit endpoints, keeps the global auth for them and enforces admin-only
// access per route.
func TestUserAndAuditRoutesWired(t *testing.T) {
	admin := identity.User{ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive}
	regular := identity.User{ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive}
	deps := Dependencies{
		Pinger: fakePinger{},
		Auth:   tokenMapAuth{"ezt_admin": admin, "ezt_user": regular},
		Users:  stubUserService{users: []identity.User{admin, regular}},
		Audit:  auditapp.NopRecorder{},
	}
	h := New(config.Config{Env: "test"}, testLogger(), deps)

	newReq := func(method, path, token, body string) *http.Request {
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, reader)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return req
	}

	// The admin can list users and read the audit log.
	for _, path := range []string{"/api/users", "/api/admin/audit"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, newReq(http.MethodGet, path, "ezt_admin", ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("admin GET %s=%d body=%s want 200", path, rec.Code, rec.Body)
		}
	}

	// The admin can create a user through the mounted route.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newReq(http.MethodPost, "/api/users", "ezt_admin",
		`{"username":"qa02","password":"qa-pass-123"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin create=%d body=%s want 201", rec.Code, rec.Body)
	}

	// A regular user is forbidden from both admin surfaces.
	for _, path := range []string{"/api/users", "/api/admin/audit"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, newReq(http.MethodGet, path, "ezt_user", ""))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("user GET %s=%d body=%s want 403", path, rec.Code, rec.Body)
		}
	}

	// Without a token the global auth middleware rejects both.
	for _, path := range []string{"/api/users", "/api/admin/audit"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, newReq(http.MethodGet, path, "", ""))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous GET %s=%d body=%s want 401", path, rec.Code, rec.Body)
		}
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

// stubEventService is a minimal server.EventService used to prove the stream
// routes are mounted. The snapshot is empty and a publish returns seq 1.
type stubEventService struct {
	snapshot []eventing.Event
}

func (s stubEventService) Snapshot(context.Context, string, int64, int) ([]eventing.Event, int64, bool, error) {
	return s.snapshot, int64(len(s.snapshot)), false, nil
}

func (s stubEventService) Publish(_ context.Context, event eventing.NewEvent) (eventing.Event, error) {
	return eventing.Event{Seq: 1, Channel: event.Channel, Type: event.Type, TS: event.TS}, nil
}

// stubEventReader is an empty EventReader for route-wiring tests.
type stubEventReader struct{}

func (stubEventReader) ListAfter(context.Context, string, int64, int) ([]eventing.Event, error) {
	return nil, nil
}

func (stubEventReader) LatestSeq(context.Context, string) (int64, error) { return 0, nil }

// capturingAudit records entries so route tests can assert the test publisher's
// audit entry carries the actor and request metadata.
type capturingAudit struct {
	entries []auditdomain.Entry
}

func (c *capturingAudit) Record(_ context.Context, entry auditdomain.Entry) error {
	c.entries = append(c.entries, entry)
	return nil
}

func (c *capturingAudit) List(context.Context, int, int64) ([]auditdomain.Entry, bool, error) {
	return c.entries, false, nil
}

// TestEventStreamRoutesWired proves the composition root mounts the stream
// endpoints, keeps the global auth, enforces admin-only on the test publisher,
// registers the publisher only when test endpoints are enabled and attaches the
// audit actor/request metadata.
func TestEventStreamRoutesWired(t *testing.T) {
	admin := identity.User{ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive}
	regular := identity.User{ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive}
	auditor := &capturingAudit{}
	deps := Dependencies{
		Pinger:      fakePinger{},
		Auth:        tokenMapAuth{"ezt_admin": admin, "ezt_user": regular},
		Events:      stubEventService{},
		EventReader: stubEventReader{},
		EventHub:    eventinginfra.NewHub(),
		Audit:       auditor,
	}
	cfg := config.Config{Env: "test", EnableTestEndpoints: true}
	h := New(cfg, testLogger(), deps)

	newReq := func(method, path, token, body string) *http.Request {
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, reader)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return req
	}

	// Snapshot is available to any authenticated user, never anonymous.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newReq(http.MethodGet, "/api/stream/events?channel=system", "ezt_user", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot as user=%d body=%s want 200", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"snapshot_cursor":0`) ||
		!strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("snapshot body=%s", rec.Body)
	}
	for _, path := range []string{"/api/stream/events?channel=system", "/api/stream?channel=system"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, newReq(http.MethodGet, path, "", ""))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous GET %s=%d want 401", path, rec.Code)
		}
	}

	// The admin can publish test events through the mounted route; the audit
	// entry carries the actor and the request metadata from the server context.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, newReq(http.MethodPost, "/api/admin/stream/test-events", "ezt_admin", `{"count":3}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("publish=%d body=%s want 200", rec.Code, rec.Body)
	}
	if len(auditor.entries) != 1 {
		t.Fatalf("audit entries=%d want 1", len(auditor.entries))
	}
	entry := auditor.entries[0]
	if entry.Action != "sse.test_published" {
		t.Fatalf("audit action=%q", entry.Action)
	}
	if entry.ActorUserID == nil || *entry.ActorUserID != admin.ID {
		t.Fatalf("audit actor=%v want %d", entry.ActorUserID, admin.ID)
	}
	if entry.RequestID == "" {
		t.Fatal("audit entry has no request id")
	}
	if entry.IP != "192.0.2.1" {
		t.Fatalf("audit ip=%q want the peer address 192.0.2.1", entry.IP)
	}
	if entry.Metadata["count"] != 3 {
		t.Fatalf("audit metadata=%v", entry.Metadata)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, newReq(http.MethodPost, "/api/admin/stream/test-events", "ezt_user", `{"count":1}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("publish as user=%d want 403", rec.Code)
	}

	// GET /api/stream is a real streaming endpoint: open it against a live
	// server, check the headers, then close the body.
	srv := httptest.NewServer(h)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/stream?channel=system", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer ezt_admin")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream=%d want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("stream Content-Type=%q", ct)
	}
	if got := resp.Header.Get("X-Snapshot-Cursor"); got != "0" {
		t.Fatalf("X-Snapshot-Cursor=%q want 0", got)
	}
}

// TestTestPublisherRouteDisabled proves the admin test publisher is only
// registered when EZTEST_ENABLE_TEST_ENDPOINTS is enabled.
func TestTestPublisherRouteDisabled(t *testing.T) {
	admin := identity.User{ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive}
	deps := Dependencies{
		Pinger:      fakePinger{},
		Auth:        tokenMapAuth{"ezt_admin": admin},
		Events:      stubEventService{},
		EventReader: stubEventReader{},
		EventHub:    eventinginfra.NewHub(),
	}
	h := New(config.Config{Env: "test", EnableTestEndpoints: false}, testLogger(), deps)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/stream/test-events", strings.NewReader(`{"count":1}`))
	req.Header.Set("Authorization", "Bearer ezt_admin")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled test publisher=%d body=%s want 404", rec.Code, rec.Body)
	}
}

// fakeLLMConfigService is a minimal llmhttp.ConfigService for the server-level
// health and route-wiring tests.
type fakeLLMConfigService struct {
	configs       []llm.Config
	defaultConfig llm.Config
	defaultErr    error
}

func (s *fakeLLMConfigService) Create(context.Context, llm.CreateParams) (llm.Config, error) {
	return llm.Config{}, nil
}

func (s *fakeLLMConfigService) Update(context.Context, int64, llm.UpdateParams) (llm.Config, error) {
	return llm.Config{}, nil
}

func (s *fakeLLMConfigService) Delete(context.Context, int64) error { return nil }

func (s *fakeLLMConfigService) Get(_ context.Context, id int64) (llm.Config, error) {
	for _, cfg := range s.configs {
		if cfg.ID == id {
			return cfg, nil
		}
	}
	return llm.Config{}, llm.ErrConfigNotFound
}

func (s *fakeLLMConfigService) List(context.Context) ([]llm.Config, error) {
	return s.configs, nil
}

func (s *fakeLLMConfigService) GetDefault(context.Context) (llm.Config, error) {
	if s.defaultErr != nil {
		return llm.Config{}, s.defaultErr
	}
	if s.defaultConfig.ID == 0 {
		return llm.Config{}, llm.ErrConfigNotFound
	}
	return s.defaultConfig, nil
}

// stubLLMClient is a successful in-process llm.Client used for route mounting.
type stubLLMClient struct{ text string }

func (c stubLLMClient) Chat(context.Context, llmapp.ChatRequest) (llmapp.ChatResponse, error) {
	return llmapp.ChatResponse{Text: c.text}, nil
}

func (c stubLLMClient) ChatStream(context.Context, llmapp.ChatRequest) (<-chan llmapp.StreamEvent, error) {
	events := make(chan llmapp.StreamEvent, 1)
	events <- llmapp.StreamEvent{Done: true}
	close(events)
	return events, nil
}

func (c stubLLMClient) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{}, nil
}

// TestAPIHealthLLMConfigured proves llm_configured is true only when a default
// configuration exists and its key decrypts (the fake service models a decrypt
// failure as a GetDefault error).
func TestAPIHealthLLMConfigured(t *testing.T) {
	cases := []struct {
		name     string
		service  llmhttp.ConfigService
		expected bool
	}{
		{
			name: "configured with decryptable key",
			service: &fakeLLMConfigService{defaultConfig: llm.Config{
				ID: 1, Name: "default", APIKey: "sk-test", IsDefault: true,
			}},
			expected: true,
		},
		{
			name:     "default without a key",
			service:  &fakeLLMConfigService{defaultConfig: llm.Config{ID: 1, Name: "default", IsDefault: true}},
			expected: false,
		},
		{
			name:     "no default config",
			service:  &fakeLLMConfigService{defaultErr: llm.ErrConfigNotFound},
			expected: false,
		},
		{
			name:     "key cannot be decrypted",
			service:  &fakeLLMConfigService{defaultErr: errors.New("crypto: unknown key version")},
			expected: false,
		},
		{
			name:     "llm not wired",
			service:  nil,
			expected: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := Dependencies{Pinger: fakePinger{}}
			if tc.service != nil {
				deps.LLMConfigs = tc.service
			}
			h := New(config.Config{Env: "test"}, testLogger(), deps)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
			}
			var body struct {
				Data struct {
					LLMConfigured bool `json:"llm_configured"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode health %q: %v", rec.Body, err)
			}
			if body.Data.LLMConfigured != tc.expected {
				t.Fatalf("llm_configured=%v want %v body=%s", body.Data.LLMConfigured, tc.expected, rec.Body)
			}
		})
	}
}

// TestLLMAdminRoutesWired proves the composition root mounts the LLM admin
// routes with the global bearer auth plus the per-route RequireAdmin guard, and
// that the test endpoint's audit entry carries the actor and request metadata.
func TestLLMAdminRoutesWired(t *testing.T) {
	admin := identity.User{ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive}
	regular := identity.User{ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive}
	cfg := llm.Config{
		ID: 7, Name: "default", Protocol: llm.ProtocolChat,
		BaseURL: "https://api.deepseek.com", Model: "deepseek-flash",
		APIKey: "sk-test", KeyVersion: 1, Headers: map[string]string{"X-Test": "header-value"},
		RetryCount: 2, RetryIntervalMS: 500, TimeoutMS: 60000,
		StreamEnabled: true, MultimodalEnabled: true, IsDefault: true,
	}
	auditor := &capturingAudit{}
	deps := Dependencies{
		Pinger: fakePinger{},
		Auth:   tokenMapAuth{"ezt_admin": admin, "ezt_user": regular},
		Audit:  auditor,
		LLMConfigs: &fakeLLMConfigService{
			configs:       []llm.Config{cfg},
			defaultConfig: cfg,
		},
		LLMClientFactory: func(llm.Config) llmapp.Client { return stubLLMClient{text: "hello"} },
	}
	h := New(config.Config{Env: "test"}, testLogger(), deps)

	newReq := func(method, path, token, body string) *http.Request {
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, reader)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("X-Request-ID", "req_llm_wired_1234")
		return req
	}

	// Admin list is 200 and masked; regular user 403; anonymous 401.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newReq(http.MethodGet, "/api/admin/llm/configs", "ezt_admin", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin list=%d body=%s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "sk-test") || strings.Contains(rec.Body.String(), "header-value") {
		t.Fatalf("admin list leaked secrets: %s", rec.Body)
	}
	for _, tc := range []struct {
		token string
		want  int
	}{{token: "ezt_user", want: http.StatusForbidden}, {token: "", want: http.StatusUnauthorized}} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, newReq(http.MethodGet, "/api/admin/llm/configs", tc.token, ""))
		if rec.Code != tc.want {
			t.Fatalf("list token=%q=%d want %d", tc.token, rec.Code, tc.want)
		}
	}

	// The admin test endpoint works through the mounted route and is audited.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, newReq(http.MethodPost, "/api/admin/llm/configs/7/test", "ezt_admin", `{"prompt":"hi"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin test=%d body=%s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"text":"hello"`) {
		t.Fatalf("test body=%s", rec.Body)
	}
	if len(auditor.entries) != 1 {
		t.Fatalf("audit entries=%d want 1: %+v", len(auditor.entries), auditor.entries)
	}
	entry := auditor.entries[0]
	if entry.Action != "llm.test_succeeded" {
		t.Fatalf("audit action=%q", entry.Action)
	}
	if entry.ActorUserID == nil || *entry.ActorUserID != admin.ID {
		t.Fatalf("audit actor=%v want %d", entry.ActorUserID, admin.ID)
	}
	if entry.RequestID != "req_llm_wired_1234" {
		t.Fatalf("audit request_id=%q", entry.RequestID)
	}

	// The admin test endpoint is admin-only.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, newReq(http.MethodPost, "/api/admin/llm/configs/7/test", "ezt_user", `{}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("user test=%d want 403", rec.Code)
	}

	// Health now reports the configured default.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, newReq(http.MethodGet, "/api/health", "", ""))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"llm_configured":true`) {
		t.Fatalf("health=%d body=%s", rec.Code, rec.Body)
	}
}
