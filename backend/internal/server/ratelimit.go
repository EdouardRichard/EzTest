package server

import (
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/config"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

const (
	// rateLimitBucketIdleTTL is how long an idle token bucket stays in memory.
	// A user that stops calling for this long starts from a full burst again.
	rateLimitBucketIdleTTL = 10 * time.Minute
	// rateLimitMaxEntries bounds the limiter maps so an unauthenticated flood
	// from many source addresses cannot grow them without bound.
	rateLimitMaxEntries = 100_000
	// rateLimitSweepEvery is the number of requests between idle sweeps.
	rateLimitSweepEvery = 1024
	// rateLimitSSERetryAfter is the Retry-After hint of a rejected stream: the
	// client should close an existing stream before opening another.
	rateLimitSSERetryAfter = 5
)

// rateBucket is one token bucket. tokens is a float so refills are exact.
type rateBucket struct {
	tokens float64
	last   time.Time
}

// RateLimiter is the process-wide rate limiter of design spec §10.1. It keys
// the ordinary token bucket by authenticated user ID (after the auth
// middleware stored the user) and by client IP for unauthenticated public
// routes. SSE connections are counted separately per key and are not charged
// to the ordinary bucket. The zero value is not usable; construct it with
// NewRateLimiter.
//
// The limiter is safe for concurrent use. It is deliberately in-memory: one
// instance enforces its own limits, which matches the single-instance default
// (multi-instance deployments configure per-instance budgets).
type RateLimiter struct {
	rps     int
	burst   int
	sseMax  int
	now     func() time.Time
	logger  *slog.Logger
	mu      sync.Mutex
	buckets map[string]*rateBucket
	sse     map[string]int
	served  uint64
}

// NewRateLimiter returns a limiter configured from cfg. A nil clock uses the
// wall clock; tests inject a fake clock to advance refills deterministically.
func NewRateLimiter(cfg config.Config, now func() time.Time) *RateLimiter {
	if now == nil {
		now = time.Now
	}
	return &RateLimiter{
		rps:     cfg.RateLimitRPS,
		burst:   cfg.RateLimitBurst,
		sseMax:  cfg.RateLimitSSEMax,
		now:     now,
		logger:  slog.Default(),
		buckets: make(map[string]*rateBucket),
		sse:     make(map[string]int),
	}
}

// Middleware wraps next with the rate limit. It must be mounted after the auth
// middleware so authenticated requests resolve their user; public routes pass
// through the auth middleware untouched and fall back to the IP key.
func (l *RateLimiter) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if l == nil {
				next.ServeHTTP(w, r)
				return
			}
			if isSSERequest(r) {
				l.serveSSE(w, r, next)
				return
			}
			if rateLimitExempt(r) || l.rps <= 0 {
				next.ServeHTTP(w, r)
				return
			}
			key, scope := rateLimitKey(r)
			if allowed, retryAfter := l.allow(key); !allowed {
				l.reject(w, r, scope, "requests_per_second", l.rps, retryAfter)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// serveSSE enforces the per-key SSE concurrency cap and releases the slot when
// the stream handler returns (including through a panic).
func (l *RateLimiter) serveSSE(w http.ResponseWriter, r *http.Request, next http.Handler) {
	if l.sseMax <= 0 {
		next.ServeHTTP(w, r)
		return
	}
	key, scope := rateLimitKey(r)
	if !l.acquireSSE(key) {
		l.reject(w, r, scope, "sse_concurrency", l.sseMax, rateLimitSSERetryAfter)
		return
	}
	defer l.releaseSSE(key)
	next.ServeHTTP(w, r)
}

// allow refills key's bucket and spends one token. The second result is the
// Retry-After hint in seconds when the request is rejected.
func (l *RateLimiter) allow(key string) (bool, int) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	l.served++
	if l.served%rateLimitSweepEvery == 0 {
		l.sweepLocked(now)
	}

	bucket, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= rateLimitMaxEntries {
			l.evictOldestLocked()
		}
		bucket = &rateBucket{tokens: float64(l.burst), last: now}
		l.buckets[key] = bucket
	}
	if elapsed := now.Sub(bucket.last).Seconds(); elapsed > 0 {
		bucket.tokens = math.Min(float64(l.burst), bucket.tokens+elapsed*float64(l.rps))
		bucket.last = now
	}
	if bucket.tokens >= 1 {
		bucket.tokens--
		return true, 0
	}
	retryAfter := int(math.Ceil((1 - bucket.tokens) / float64(l.rps)))
	if retryAfter < 1 {
		retryAfter = 1
	}
	return false, retryAfter
}

// sweepLocked drops buckets idle for longer than rateLimitBucketIdleTTL. It is
// called every rateLimitSweepEvery requests so abandoned keys do not stay in
// memory forever.
func (l *RateLimiter) sweepLocked(now time.Time) {
	for key, bucket := range l.buckets {
		if now.Sub(bucket.last) >= rateLimitBucketIdleTTL {
			delete(l.buckets, key)
		}
	}
	for key, count := range l.sse {
		if count <= 0 {
			delete(l.sse, key)
		}
	}
}

// evictOldestLocked removes the least recently refilled bucket when the map
// reached its cap. It is a linear scan over a large map, but it only runs once
// the cap is hit, which keeps the steady-state cost off the hot path.
func (l *RateLimiter) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for key, bucket := range l.buckets {
		if oldestKey == "" || bucket.last.Before(oldest) {
			oldestKey, oldest = key, bucket.last
		}
	}
	if oldestKey != "" {
		delete(l.buckets, oldestKey)
	}
}

// acquireSSE reserves one stream slot for key.
func (l *RateLimiter) acquireSSE(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.sse) >= rateLimitMaxEntries {
		l.evictOldestSSELocked()
	}
	if l.sse[key] >= l.sseMax {
		return false
	}
	l.sse[key]++
	return true
}

// releaseSSE frees one stream slot for key.
func (l *RateLimiter) releaseSSE(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sse[key] <= 1 {
		delete(l.sse, key)
		return
	}
	l.sse[key]--
}

// evictOldestSSELocked drops one arbitrary idle entry when the SSE map is at
// its cap. Live entries are never dropped because the count itself is the
// active slot reservation.
func (l *RateLimiter) evictOldestSSELocked() {
	for key, count := range l.sse {
		if count <= 0 {
			delete(l.sse, key)
			return
		}
	}
}

// reject writes the shared 429 envelope with Retry-After and records a hit
// without logging credentials, tokens or the client key.
func (l *RateLimiter) reject(w http.ResponseWriter, r *http.Request, scope, reason string, limit, retryAfter int) {
	if retryAfter < 1 {
		retryAfter = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	if l.logger != nil {
		l.logger.Warn("rate limit exceeded",
			"request_id", platform.RequestID(r.Context()),
			"path", r.URL.Path,
			"scope", scope,
			"reason", reason,
			"limit", limit,
			"retry_after_seconds", retryAfter,
		)
	}
	platform.WriteError(w, r, http.StatusTooManyRequests, platform.CodeRateLimited,
		"rate limit exceeded",
		platform.WithRetryable(true),
		platform.WithDetails(map[string]any{
			"scope":               scope,
			"reason":              reason,
			"limit":               limit,
			"retry_after_seconds": retryAfter,
		}))
}

// rateLimitKey returns the bucket key and its scope. Authenticated requests
// are per user; public requests fall back to the peer IP.
func rateLimitKey(r *http.Request) (key, scope string) {
	if user, ok := identityhttp.UserFromContext(r.Context()); ok {
		return "user:" + strconv.FormatInt(user.ID, 10), "user"
	}
	return "ip:" + remoteIP(r), "ip"
}

// rateLimitExempt reports whether the path is outside the ordinary request
// budget: probes, the public OpenAPI document, static assets and CORS
// preflights are excluded so they cannot exhaust a user's API budget.
func rateLimitExempt(r *http.Request) bool {
	if r == nil {
		return true
	}
	if r.Method == http.MethodOptions {
		return true
	}
	switch r.URL.Path {
	case "/healthz", "/api/health", "/api/openapi.yaml", "/favicon.ico":
		return true
	}
	for _, prefix := range []string{"/assets/", "/static/", "/_next/"} {
		if strings.HasPrefix(r.URL.Path, prefix) {
			return true
		}
	}
	return false
}

// isSSERequest reports whether the request is a server-sent-events stream. SSE
// connections are long-lived, so heartbeats are not ordinary request traffic;
// they are limited by concurrency instead. The Accept header catches future
// stream endpoints and the explicit paths pin the two current ones.
func isSSERequest(r *http.Request) bool {
	if r == nil || r.Method != http.MethodGet {
		return false
	}
	switch r.URL.Path {
	case "/api/stream", "/api/stream/events":
		return true
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream")
}
