package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/config"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/load"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

func TestRequestIDEchoesValidInbound(t *testing.T) {
	var seen string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = requestid.From(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set(requestid.Header, "req_12345678")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if seen != "req_12345678" || rec.Header().Get(requestid.Header) != "req_12345678" {
		t.Fatalf("context=%q header=%q", seen, rec.Header().Get(requestid.Header))
	}
}

func TestRequestIDReplacesInvalidInbound(t *testing.T) {
	var seen string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = requestid.From(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set(requestid.Header, "bad id")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if seen == "" || seen == "bad id" || !requestid.Valid(seen) {
		t.Fatalf("seen = %q", seen)
	}
	if rec.Header().Get(requestid.Header) != seen {
		t.Fatalf("header = %q, context = %q", rec.Header().Get(requestid.Header), seen)
	}
}

func TestRecoverWritesErrorEnvelope(t *testing.T) {
	handler := RequestID(Recover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})))
	req := httptest.NewRequest(http.MethodGet, "/api/versions", nil)
	req.Header.Set(requestid.Header, "req_panic_1234")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Error struct {
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "internal" || body.Error.RequestID != "req_panic_1234" {
		t.Fatalf("error = %#v", body.Error)
	}
}

func TestCORSAllowsDerivedOrigins(t *testing.T) {
	cfg := config.Config{WebPort: 6868}
	handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/versions", nil)
	req.Header.Set("Origin", "http://localhost:6868")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:6868" {
		t.Fatalf("allow origin = %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}

	req = httptest.NewRequest(http.MethodOptions, "/api/versions", nil)
	req.Header.Set("Origin", "http://127.0.0.1:6868")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Last-Event-ID") {
		t.Fatalf("allow headers = %q", rec.Header().Get("Access-Control-Allow-Headers"))
	}
	if !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Idempotency-Key") {
		t.Fatalf("allow headers must include Idempotency-Key: %q", rec.Header().Get("Access-Control-Allow-Headers"))
	}
}

func TestCORSRejectsUnknownOrigin(t *testing.T) {
	cfg := config.Config{WebPort: 6868}
	handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/versions", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("allow origin = %q", got)
	}
}

type fakeAuthenticator struct {
	actor platform.Actor
	err   error
}

func (f fakeAuthenticator) Authenticate(context.Context, string) (platform.Actor, error) {
	return f.actor, f.err
}

func TestRequireAuth(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, ok := platform.ActorFrom(r.Context())
		if !ok || actor.ID != 7 {
			t.Fatalf("actor = %#v, %v", actor, ok)
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := RequireAuth(fakeAuthenticator{actor: platform.Actor{ID: 7, Role: "user"}})(next)

	req := httptest.NewRequest(http.MethodGet, "/api/versions", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/versions", nil)
	req.Header.Set("Authorization", "Bearer ezt_token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token status = %d", rec.Code)
	}
}

func TestRequireAuthRejectsBadToken(t *testing.T) {
	handler := RequireAuth(fakeAuthenticator{err: platform.Unauthenticated()})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	)
	req := httptest.NewRequest(http.MethodGet, "/api/versions", nil)
	req.Header.Set("Authorization", "Bearer bad")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestRequireAdmin(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	user := RequireAdmin(fakeAuthenticator{actor: platform.Actor{ID: 7, Role: "user"}})(next)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil)
	req.Header.Set("Authorization", "Bearer ezt_token")
	rec := httptest.NewRecorder()
	user.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("user status = %d", rec.Code)
	}

	admin := RequireAdmin(fakeAuthenticator{actor: platform.Actor{ID: 1, Role: "admin"}})(next)
	req = httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil)
	req.Header.Set("Authorization", "Bearer ezt_token")
	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d", rec.Code)
	}
}

func TestTrackInflightCountsConcurrentRequests(t *testing.T) {
	var counter load.Counter
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	handler := TrackInflight(&counter)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/versions", nil))
		}()
	}
	for i := 0; i < 4; i++ {
		<-started
	}
	if got := counter.Value(); got != 4 {
		t.Fatalf("inflight = %d, want 4", got)
	}
	close(release)
	wg.Wait()
	if got := counter.Value(); got != 0 {
		t.Fatalf("inflight after completion = %d, want 0", got)
	}
}

func TestTrackInflightSkipsHealthAndSSE(t *testing.T) {
	var counter load.Counter
	handler := TrackInflight(&counter)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, path := range []string{"/healthz", "/api/health", "/api/health/ready", "/api/stream"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
		if got := counter.Value(); got != 0 {
			t.Fatalf("%s counted as inflight=%d", path, got)
		}
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/versions", nil))
	if got := counter.Value(); got != 0 {
		t.Fatalf("request not balanced: inflight=%d", got)
	}
}
