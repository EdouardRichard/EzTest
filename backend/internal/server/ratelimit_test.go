package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/config"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
)

// rateLimitClock is a mutable clock so the token bucket refill can be advanced
// deterministically instead of sleeping.
type rateLimitClock struct {
	mu  sync.Mutex
	now time.Time
}

func newRateLimitClock() *rateLimitClock {
	return &rateLimitClock{now: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
}

func (c *rateLimitClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *rateLimitClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// rateLimitAuthenticator resolves a small fixed token→user table.
type rateLimitAuthenticator map[string]identity.User

func (a rateLimitAuthenticator) Authenticate(_ context.Context, token string) (identity.User, error) {
	user, ok := a[token]
	if !ok {
		return identity.User{}, identity.ErrUnauthenticated
	}
	return user, nil
}

// rateLimitHarness assembles the production middleware order (request ID, auth,
// then the rate limiter) around next and returns the entry point.
func rateLimitHarness(cfg config.Config, clock *rateLimitClock, auth identityhttp.Authenticator, next http.Handler) http.Handler {
	limiter := NewRateLimiter(cfg, clock.Now)
	return requestIDMiddleware(authenticatedAPI(auth)(limiter.Middleware()(next)))
}

// rateLimitRequest issues one request through h.
func rateLimitRequest(h http.Handler, method, path, token, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeRateLimitError(t *testing.T, rec *httptest.ResponseRecorder) (code, requestID string) {
	t.Helper()
	var body struct {
		Error struct {
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid error body %q: %v", rec.Body.String(), err)
	}
	return body.Error.Code, body.Error.RequestID
}

// TestRateLimiterRejectsAboveBurst pins the ordinary token bucket: the burst is
// spent on the first calls, the next one answers 429 rate_limited with a
// positive Retry-After and the request ID on the response and in the envelope.
func TestRateLimiterRejectsAboveBurst(t *testing.T) {
	clock := newRateLimitClock()
	auth := rateLimitAuthenticator{"ezt_user": {ID: 42, Role: identity.RoleUser}}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := rateLimitHarness(config.Config{RateLimitRPS: 1, RateLimitBurst: 2, RateLimitSSEMax: 10}, clock, auth, ok)

	for i := 0; i < 2; i++ {
		if rec := rateLimitRequest(h, http.MethodGet, "/api/versions", "ezt_user", ""); rec.Code != http.StatusOK {
			t.Fatalf("request %d code=%d body=%s want 200", i, rec.Code, rec.Body)
		}
	}

	rec := rateLimitRequest(h, http.MethodGet, "/api/versions", "ezt_user", "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over-burst code=%d body=%s want 429", rec.Code, rec.Body)
	}
	if retry := rec.Header().Get("Retry-After"); retry != "1" {
		t.Fatalf("Retry-After=%q want 1", retry)
	}
	if got := rec.Header().Get("X-Request-ID"); got == "" {
		t.Fatal("429 response is missing X-Request-ID")
	}
	code, requestID := decodeRateLimitError(t, rec)
	if code != "rate_limited" {
		t.Fatalf("error code=%q want rate_limited", code)
	}
	if requestID == "" || requestID != rec.Header().Get("X-Request-ID") {
		t.Fatalf("envelope request_id=%q header=%q", requestID, rec.Header().Get("X-Request-ID"))
	}

	// One token per second: after a second the same user is allowed again.
	clock.Advance(time.Second)
	if rec := rateLimitRequest(h, http.MethodGet, "/api/versions", "ezt_user", ""); rec.Code != http.StatusOK {
		t.Fatalf("after refill code=%d body=%s want 200", rec.Code, rec.Body)
	}
}

// TestRateLimiterQueuesUsersIndependently proves the bucket is keyed by the
// authenticated user instead of one process-wide bucket.
func TestRateLimiterQueuesUsersIndependently(t *testing.T) {
	clock := newRateLimitClock()
	auth := rateLimitAuthenticator{
		"ezt_alice": {ID: 1, Role: identity.RoleUser},
		"ezt_bob":   {ID: 2, Role: identity.RoleUser},
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := rateLimitHarness(config.Config{RateLimitRPS: 1, RateLimitBurst: 1, RateLimitSSEMax: 10}, clock, auth, ok)

	if rec := rateLimitRequest(h, http.MethodGet, "/api/versions", "ezt_alice", ""); rec.Code != http.StatusOK {
		t.Fatalf("alice first=%d body=%s", rec.Code, rec.Body)
	}
	if rec := rateLimitRequest(h, http.MethodGet, "/api/versions", "ezt_alice", ""); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("alice second=%d body=%s want 429", rec.Code, rec.Body)
	}
	if rec := rateLimitRequest(h, http.MethodGet, "/api/versions", "ezt_bob", ""); rec.Code != http.StatusOK {
		t.Fatalf("bob first=%d body=%s want 200 (independent bucket)", rec.Code, rec.Body)
	}
}

// TestRateLimiterQueuesUnauthenticatedByIP proves public routes without a user
// in context fall back to the client IP bucket, independently per address.
func TestRateLimiterQueuesUnauthenticatedByIP(t *testing.T) {
	clock := newRateLimitClock()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := rateLimitHarness(config.Config{RateLimitRPS: 1, RateLimitBurst: 1, RateLimitSSEMax: 10}, clock, rateLimitAuthenticator{}, ok)

	if rec := rateLimitRequest(h, http.MethodPost, "/api/auth/login", "", "192.0.2.10:1234"); rec.Code != http.StatusOK {
		t.Fatalf("client A first=%d body=%s", rec.Code, rec.Body)
	}
	if rec := rateLimitRequest(h, http.MethodPost, "/api/auth/login", "", "192.0.2.10:9999"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("client A second=%d body=%s want 429 (same IP, different port)", rec.Code, rec.Body)
	}
	if rec := rateLimitRequest(h, http.MethodPost, "/api/auth/login", "", "192.0.2.11:1234"); rec.Code != http.StatusOK {
		t.Fatalf("client B first=%d body=%s want 200", rec.Code, rec.Body)
	}
}

// TestRateLimiterDisabledByZeroRPS pins the documented off switch: RPS=0 means
// the ordinary bucket never rejects, even far above the configured burst.
func TestRateLimiterDisabledByZeroRPS(t *testing.T) {
	clock := newRateLimitClock()
	auth := rateLimitAuthenticator{"ezt_user": {ID: 42, Role: identity.RoleUser}}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := rateLimitHarness(config.Config{RateLimitRPS: 0, RateLimitBurst: 1, RateLimitSSEMax: 10}, clock, auth, ok)

	for i := 0; i < 100; i++ {
		if rec := rateLimitRequest(h, http.MethodGet, "/api/versions", "ezt_user", ""); rec.Code != http.StatusOK {
			t.Fatalf("request %d=%d body=%s want 200 with RPS=0", i, rec.Code, rec.Body)
		}
	}
}

// TestRateLimiterExemptsHealthDoesNotSpendTokens proves the liveness/readiness
// probes are outside the ordinary bucket: a user whose burst is one token can
// still issue one business request after any number of health checks.
func TestRateLimiterExemptsHealthDoesNotSpendTokens(t *testing.T) {
	clock := newRateLimitClock()
	auth := rateLimitAuthenticator{"ezt_user": {ID: 42, Role: identity.RoleUser}}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := rateLimitHarness(config.Config{RateLimitRPS: 1, RateLimitBurst: 1, RateLimitSSEMax: 10}, clock, auth, ok)

	for i := 0; i < 20; i++ {
		if rec := rateLimitRequest(h, http.MethodGet, "/api/health", "ezt_user", ""); rec.Code != http.StatusOK {
			t.Fatalf("health %d=%d body=%s", i, rec.Code, rec.Body)
		}
		if rec := rateLimitRequest(h, http.MethodGet, "/healthz", "ezt_user", ""); rec.Code != http.StatusOK {
			t.Fatalf("healthz %d=%d body=%s", i, rec.Code, rec.Body)
		}
	}
	if rec := rateLimitRequest(h, http.MethodGet, "/api/versions", "ezt_user", ""); rec.Code != http.StatusOK {
		t.Fatalf("business request=%d body=%s want 200", rec.Code, rec.Body)
	}
	if rec := rateLimitRequest(h, http.MethodGet, "/api/versions", "ezt_user", ""); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second business request=%d body=%s want 429", rec.Code, rec.Body)
	}
}

// TestRateLimiterSSEConcurrency pins the per-user SSE cap and its release: the
// second concurrent stream of one user is rejected with 429, another user is
// still admitted, and a slot frees as soon as the blocked stream returns.
func TestRateLimiterSSEConcurrency(t *testing.T) {
	clock := newRateLimitClock()
	auth := rateLimitAuthenticator{
		"ezt_alice": {ID: 1, Role: identity.RoleUser},
		"ezt_bob":   {ID: 2, Role: identity.RoleUser},
	}
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	done := make(chan struct{})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	})
	h := rateLimitHarness(config.Config{RateLimitRPS: 100, RateLimitBurst: 100, RateLimitSSEMax: 1}, clock, auth, next)

	go func() {
		rec := rateLimitRequest(h, http.MethodGet, "/api/stream", "ezt_alice", "")
		if rec.Code != http.StatusOK {
			t.Errorf("first stream code=%d body=%s want 200", rec.Code, rec.Body)
		}
		close(done)
	}()
	<-entered

	rec := rateLimitRequest(h, http.MethodGet, "/api/stream", "ezt_alice", "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second concurrent stream code=%d body=%s want 429", rec.Code, rec.Body)
	}
	if retry := rec.Header().Get("Retry-After"); retry == "" {
		t.Fatal("SSE 429 is missing Retry-After")
	}
	if code, _ := decodeRateLimitError(t, rec); code != "rate_limited" {
		t.Fatalf("SSE error code=%q want rate_limited", code)
	}

	// A different user has their own SSE slot.
	bobDone := make(chan struct{})
	go func() {
		rec := rateLimitRequest(h, http.MethodGet, "/api/stream", "ezt_bob", "")
		if rec.Code != http.StatusOK {
			t.Errorf("bob stream code=%d body=%s want 200", rec.Code, rec.Body)
		}
		close(bobDone)
	}()
	<-entered
	close(release)
	<-done
	<-bobDone

	// The slot was released: a new stream is admitted.
	rec = rateLimitRequest(h, http.MethodGet, "/api/stream", "ezt_alice", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("stream after release code=%d body=%s want 200", rec.Code, rec.Body)
	}
}

// TestRateLimiterSSEExcludedFromOrdinaryBucket proves long-lived streams do not
// consume the request budget (their heartbeats are not ordinary traffic).
func TestRateLimiterSSEExcludedFromOrdinaryBucket(t *testing.T) {
	clock := newRateLimitClock()
	auth := rateLimitAuthenticator{"ezt_alice": {ID: 1, Role: identity.RoleUser}}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := rateLimitHarness(config.Config{RateLimitRPS: 1, RateLimitBurst: 1, RateLimitSSEMax: 10}, clock, auth, next)

	for i := 0; i < 5; i++ {
		if rec := rateLimitRequest(h, http.MethodGet, "/api/stream/events", "ezt_alice", ""); rec.Code != http.StatusOK {
			t.Fatalf("stream %d=%d body=%s want 200", i, rec.Code, rec.Body)
		}
	}
	if rec := rateLimitRequest(h, http.MethodGet, "/api/versions", "ezt_alice", ""); rec.Code != http.StatusOK {
		t.Fatalf("business request=%d body=%s want 200 (SSE must not spend the bucket)", rec.Code, rec.Body)
	}
}

// TestRateLimitExemptPaths pins the paths that are not ordinary API traffic.
func TestRateLimitExemptPaths(t *testing.T) {
	for _, path := range []string{"/healthz", "/api/health", "/assets/app.js", "/_next/static/chunk.js", "/api/openapi.yaml"} {
		req := &http.Request{Method: http.MethodGet, URL: mustURL(t, path)}
		if !rateLimitExempt(req) {
			t.Fatalf("%s should be exempt", path)
		}
	}
	for _, path := range []string{"/api/versions", "/api/auth/login"} {
		req := &http.Request{Method: http.MethodGet, URL: mustURL(t, path)}
		if rateLimitExempt(req) {
			t.Fatalf("%s should not be exempt", path)
		}
	}
	if !rateLimitExempt(&http.Request{Method: http.MethodOptions, URL: mustURL(t, "/api/versions")}) {
		t.Fatal("CORS preflight should be exempt")
	}
}

func mustURL(t *testing.T, path string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(path)
	if err != nil {
		t.Fatalf("parse %q: %v", path, err)
	}
	return parsed
}
