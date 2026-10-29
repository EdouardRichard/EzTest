package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	return rateLimitRequestWithHeaders(h, method, path, token, remoteAddr, nil)
}

// rateLimitRequestWithHeaders is rateLimitRequest plus extra request headers,
// used to prove the limiter ignores client-controlled SSE hints.
func rateLimitRequestWithHeaders(h http.Handler, method, path, token, remoteAddr string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
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
// consume the request budget (their heartbeats are not ordinary traffic). It
// uses the real version stream path; the JSON snapshot endpoint is ordinary
// traffic and is covered by TestRateLimiterSnapshotEndpointIsNotSSE.
func TestRateLimiterSSEExcludedFromOrdinaryBucket(t *testing.T) {
	clock := newRateLimitClock()
	auth := rateLimitAuthenticator{"ezt_alice": {ID: 1, Role: identity.RoleUser}}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := rateLimitHarness(config.Config{RateLimitRPS: 1, RateLimitBurst: 1, RateLimitSSEMax: 10}, clock, auth, next)

	for i := 0; i < 5; i++ {
		if rec := rateLimitRequest(h, http.MethodGet, "/api/versions/101/stream", "ezt_alice", ""); rec.Code != http.StatusOK {
			t.Fatalf("stream %d=%d body=%s want 200", i, rec.Code, rec.Body)
		}
	}
	if rec := rateLimitRequest(h, http.MethodGet, "/api/versions", "ezt_alice", ""); rec.Code != http.StatusOK {
		t.Fatalf("business request=%d body=%s want 200 (SSE must not spend the bucket)", rec.Code, rec.Body)
	}
}

// TestRateLimiterIgnoresSSEAcceptHeaderOnOrdinaryRoute pins the fix for the
// SSE identification bypass: a client-controlled Accept header must never move
// an ordinary GET into the SSE concurrency class. The request has to spend the
// ordinary token bucket and be rejected once the burst is exhausted.
func TestRateLimiterIgnoresSSEAcceptHeaderOnOrdinaryRoute(t *testing.T) {
	clock := newRateLimitClock()
	auth := rateLimitAuthenticator{"ezt_user": {ID: 42, Role: identity.RoleUser}}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := rateLimitHarness(config.Config{RateLimitRPS: 1, RateLimitBurst: 1, RateLimitSSEMax: 10}, clock, auth, ok)
	sseAccept := map[string]string{"Accept": "text/event-stream"}

	if rec := rateLimitRequestWithHeaders(h, http.MethodGet, "/api/versions", "ezt_user", "", sseAccept); rec.Code != http.StatusOK {
		t.Fatalf("first request code=%d body=%s want 200", rec.Code, rec.Body)
	}
	rec := rateLimitRequestWithHeaders(h, http.MethodGet, "/api/versions", "ezt_user", "", sseAccept)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request with Accept: text/event-stream code=%d body=%s want 429 (ordinary bucket must apply)", rec.Code, rec.Body)
	}
	if retry := rec.Header().Get("Retry-After"); retry == "" {
		t.Fatal("429 response is missing Retry-After")
	}
}

// TestRateLimiterCountsVersionStreamsWithoutAcceptHeader pins the real version
// stream path: it is recognised as SSE even when the client sends no Accept
// header, capped at sseMax concurrent streams per user, and a freed slot is
// admitted again. The 11th concurrent stream against the default cap is 429.
func TestRateLimiterCountsVersionStreamsWithoutAcceptHeader(t *testing.T) {
	clock := newRateLimitClock()
	auth := rateLimitAuthenticator{"ezt_alice": {ID: 1, Role: identity.RoleUser}}
	entered := make(chan struct{}, 12)
	release := make(chan struct{})
	var calls int32
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Only the first sseMax requests hold a stream slot so that a regression
		// which routes the 11th request to the handler fails fast instead of
		// deadlocking the test.
		if atomic.AddInt32(&calls, 1) <= 10 {
			entered <- struct{}{}
			<-release
		}
		w.WriteHeader(http.StatusOK)
	})
	h := rateLimitHarness(config.Config{RateLimitRPS: 100, RateLimitBurst: 100, RateLimitSSEMax: 10}, clock, auth, next)

	done := make(chan *httptest.ResponseRecorder, 10)
	for i := 0; i < 10; i++ {
		go func() {
			done <- rateLimitRequest(h, http.MethodGet, "/api/versions/101/stream", "ezt_alice", "")
		}()
	}
	for i := 0; i < 10; i++ {
		<-entered
	}

	rec := rateLimitRequest(h, http.MethodGet, "/api/versions/101/stream", "ezt_alice", "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("11th concurrent version stream code=%d body=%s want 429", rec.Code, rec.Body)
	}
	if code, _ := decodeRateLimitError(t, rec); code != "rate_limited" {
		t.Fatalf("SSE error code=%q want rate_limited", code)
	}
	if retry := rec.Header().Get("Retry-After"); retry == "" {
		t.Fatal("SSE 429 is missing Retry-After")
	}

	close(release)
	for i := 0; i < 10; i++ {
		if r := <-done; r.Code != http.StatusOK {
			t.Fatalf("admitted version stream code=%d body=%s want 200", r.Code, r.Body)
		}
	}

	// The rejected slot was released with the streams: a new stream is admitted.
	if rec := rateLimitRequest(h, http.MethodGet, "/api/versions/101/stream", "ezt_alice", ""); rec.Code != http.StatusOK {
		t.Fatalf("stream after release code=%d body=%s want 200", rec.Code, rec.Body)
	}
}

// TestRateLimiterSnapshotEndpointIsNotSSE pins that GET /api/stream/events is a
// JSON snapshot, not a long-lived stream: it never reserves an SSE slot, so a
// slow snapshot request cannot push the same user over the SSE concurrency cap.
func TestRateLimiterSnapshotEndpointIsNotSSE(t *testing.T) {
	clock := newRateLimitClock()
	auth := rateLimitAuthenticator{"ezt_alice": {ID: 1, Role: identity.RoleUser}}
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls int32
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(entered)
			<-release
		}
		w.WriteHeader(http.StatusOK)
	})
	h := rateLimitHarness(config.Config{RateLimitRPS: 100, RateLimitBurst: 100, RateLimitSSEMax: 1}, clock, auth, next)

	first := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		first <- rateLimitRequest(h, http.MethodGet, "/api/stream/events?channel=system", "ezt_alice", "")
	}()
	<-entered

	// If the snapshot were counted as SSE, this concurrent request would hit the
	// per-user cap of one and be rejected.
	if rec := rateLimitRequest(h, http.MethodGet, "/api/stream/events?channel=system", "ezt_alice", ""); rec.Code != http.StatusOK {
		t.Fatalf("concurrent snapshot code=%d body=%s want 200 (snapshot is not SSE)", rec.Code, rec.Body)
	}
	close(release)
	if rec := <-first; rec.Code != http.StatusOK {
		t.Fatalf("slow snapshot code=%d body=%s want 200", rec.Code, rec.Body)
	}
}

// TestRateLimitRejectionExposesRetryAfterViaCORS pins that a browser can read
// the Retry-After hint of the shared 429 envelope: the CORS middleware has to
// expose the header on the actual response (not only on preflights).
func TestRateLimitRejectionExposesRetryAfterViaCORS(t *testing.T) {
	clock := newRateLimitClock()
	auth := rateLimitAuthenticator{"ezt_user": {ID: 42, Role: identity.RoleUser}}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	limiter := NewRateLimiter(config.Config{RateLimitRPS: 1, RateLimitBurst: 1, RateLimitSSEMax: 10}, clock.Now)
	h := requestIDMiddleware(CORS("http://localhost:6868")(authenticatedAPI(auth)(limiter.Middleware()(ok))))

	origin := map[string]string{"Origin": "http://localhost:6868"}
	if rec := rateLimitRequestWithHeaders(h, http.MethodGet, "/api/versions", "ezt_user", "", origin); rec.Code != http.StatusOK {
		t.Fatalf("first request code=%d body=%s want 200", rec.Code, rec.Body)
	}
	rec := rateLimitRequestWithHeaders(h, http.MethodGet, "/api/versions", "ezt_user", "", origin)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over-burst code=%d body=%s want 429", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Fatal("429 response is missing Retry-After")
	}
	exposed := rec.Header().Get("Access-Control-Expose-Headers")
	if !strings.Contains(exposed, "Retry-After") {
		t.Fatalf("Access-Control-Expose-Headers=%q must expose Retry-After so the SSE client can read it", exposed)
	}
}

// TestRateLimiterEvictsLeastRecentlyUsedBucketAtCap pins the bounded-bucket
// policy: once the map is full the least recently used bucket is dropped in
// O(1), so a high-cardinality flood cannot make inserts scan the whole map.
func TestRateLimiterEvictsLeastRecentlyUsedBucketAtCap(t *testing.T) {
	clock := newRateLimitClock()
	limiter := NewRateLimiter(config.Config{RateLimitRPS: 10, RateLimitBurst: 10, RateLimitSSEMax: 10}, clock.Now)
	limiter.maxEntries = 3

	for _, key := range []string{"a", "b", "c"} {
		if allowed, _ := limiter.allow(key); !allowed {
			t.Fatalf("allow(%q) unexpectedly rejected", key)
		}
	}
	// Touch a: b becomes the least recently used bucket and is the next victim.
	if allowed, _ := limiter.allow("a"); !allowed {
		t.Fatal("allow(a) unexpectedly rejected")
	}
	if allowed, _ := limiter.allow("d"); !allowed {
		t.Fatal("allow(d) unexpectedly rejected")
	}

	limiter.mu.Lock()
	keys := make(map[string]struct{}, len(limiter.buckets))
	for key := range limiter.buckets {
		keys[key] = struct{}{}
	}
	limiter.mu.Unlock()

	if len(keys) != 3 {
		t.Fatalf("bucket map has %d keys, want 3 (bounded by maxEntries)", len(keys))
	}
	if _, ok := keys["b"]; ok {
		t.Fatal("least recently used bucket b was not evicted")
	}
	for _, key := range []string{"a", "c", "d"} {
		if _, ok := keys[key]; !ok {
			t.Fatalf("bucket %q must stay after eviction; keys=%v", key, keys)
		}
	}
}

// TestRateLimiterHighCardinalityStaysBounded pins the high-cardinality flood
// path: 100k distinct client keys must complete without the map growing past
// the cap. The elapsed guard is deliberately generous; a linear eviction scan
// per insert would take minutes instead of milliseconds.
func TestRateLimiterHighCardinalityStaysBounded(t *testing.T) {
	clock := newRateLimitClock()
	limiter := NewRateLimiter(config.Config{RateLimitRPS: 1000, RateLimitBurst: 1000, RateLimitSSEMax: 10}, clock.Now)
	limiter.maxEntries = 50_000

	started := time.Now()
	for i := 0; i < 100_000; i++ {
		if allowed, _ := limiter.allow("ip:203.0.113." + strconv.Itoa(i)); !allowed {
			t.Fatalf("allow(%d) unexpectedly rejected", i)
		}
	}
	elapsed := time.Since(started)

	limiter.mu.Lock()
	size := len(limiter.buckets)
	limiter.mu.Unlock()
	if size > limiter.maxEntries {
		t.Fatalf("bucket map size=%d exceeds maxEntries=%d", size, limiter.maxEntries)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("100k distinct keys took %s; eviction must not scan the map per insert", elapsed)
	}
}

// TestRateLimiterSSECapRejectsNewKeysAndReleases pins the SSE key cap: live
// streams keep their slots, a new key is rejected while the map is full (never
// silently admitted past the cap), and releasing a stream frees the slot.
func TestRateLimiterSSECapRejectsNewKeysAndReleases(t *testing.T) {
	limiter := NewRateLimiter(config.Config{RateLimitRPS: 10, RateLimitBurst: 10, RateLimitSSEMax: 1}, newRateLimitClock().Now)
	limiter.maxEntries = 2

	if !limiter.acquireSSE("u1") || !limiter.acquireSSE("u2") {
		t.Fatal("first two SSE keys must be admitted")
	}
	if limiter.acquireSSE("u3") {
		t.Fatal("SSE key cap must reject a third live key instead of exceeding the cap")
	}
	limiter.releaseSSE("u2")
	if !limiter.acquireSSE("u3") {
		t.Fatal("released SSE key slot must admit a new key")
	}
	if limiter.acquireSSE("u4") {
		t.Fatal("SSE key cap must reject again once the map is full")
	}

	limiter.mu.Lock()
	size := len(limiter.sse)
	limiter.mu.Unlock()
	if size != 2 {
		t.Fatalf("SSE map size=%d, want 2", size)
	}

	limiter.releaseSSE("u1")
	limiter.releaseSSE("u3")
	limiter.mu.Lock()
	size = len(limiter.sse)
	limiter.mu.Unlock()
	if size != 0 {
		t.Fatalf("SSE map size=%d after all releases, want 0 (slots must be freed)", size)
	}
}

// BenchmarkRateLimiterNewKeyAtCapacity measures the insert path while the
// bucket map is full. O(1) LRU eviction keeps this flat; a linear eviction scan
// would be orders of magnitude slower at the 100k cap.
func BenchmarkRateLimiterNewKeyAtCapacity(b *testing.B) {
	clock := newRateLimitClock()
	limiter := NewRateLimiter(config.Config{RateLimitRPS: 1, RateLimitBurst: 1, RateLimitSSEMax: 10}, clock.Now)
	limiter.maxEntries = rateLimitMaxEntries
	for i := 0; i < limiter.maxEntries; i++ {
		limiter.allow("warm:" + strconv.Itoa(i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		limiter.allow("bench:" + strconv.Itoa(i))
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
