package server

import (
	"container/list"
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
	// from many source addresses cannot grow them without bound. Ordinary
	// buckets are evicted least-recently-used in O(1); the SSE map never exceeds
	// this many live keys because new keys are rejected while it is full.
	rateLimitMaxEntries = 100_000
	// rateLimitSweepEvery is the number of requests between idle sweeps.
	rateLimitSweepEvery = 1024
	// rateLimitSSERetryAfter is the Retry-After hint of a rejected stream: the
	// client should close an existing stream before opening another.
	rateLimitSSERetryAfter = 5
)

// rateBucket is one token bucket. tokens is a float so refills are exact. key
// lets the LRU list find the map entry when it evicts the tail element.
type rateBucket struct {
	key    string
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
//
// Ordinary buckets are ordered by last use in a linked list so a full map is
// evicted least-recently-used in O(1) instead of scanning the whole map per new
// key. The SSE map holds only live per-key concurrency counts (an entry is
// deleted as soon as its last stream ends), so its size is bounded by the
// connections the process actually serves.
type RateLimiter struct {
	rps        int
	burst      int
	sseMax     int
	now        func() time.Time
	logger     *slog.Logger
	maxEntries int
	mu         sync.Mutex
	buckets    map[string]*list.Element
	order      *list.List
	sse        map[string]int
	served     uint64
}

// NewRateLimiter returns a limiter configured from cfg. A nil clock uses the
// wall clock; tests inject a fake clock to advance refills deterministically.
func NewRateLimiter(cfg config.Config, now func() time.Time) *RateLimiter {
	if now == nil {
		now = time.Now
	}
	limiter := &RateLimiter{
		rps:        cfg.RateLimitRPS,
		burst:      cfg.RateLimitBurst,
		sseMax:     cfg.RateLimitSSEMax,
		now:        now,
		logger:     slog.Default(),
		maxEntries: rateLimitMaxEntries,
		buckets:    make(map[string]*list.Element),
		order:      list.New(),
		sse:        make(map[string]int),
	}
	if limiter.maxEntries <= 0 {
		limiter.maxEntries = rateLimitMaxEntries
	}
	return limiter
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

	element, ok := l.buckets[key]
	if ok {
		// Move-to-front keeps the list ordered by last use so the tail is the
		// least recently used bucket.
		l.order.MoveToFront(element)
	} else {
		l.evictLocked()
		bucket := &rateBucket{key: key, tokens: float64(l.burst), last: now}
		element = l.order.PushFront(bucket)
		l.buckets[key] = element
	}
	bucket := element.Value.(*rateBucket)
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

// sweepLocked drops buckets idle for longer than rateLimitBucketIdleTTL. The
// LRU list is ordered by last use, so idle buckets are contiguous at the tail:
// the sweep stops at the first active bucket instead of scanning the map. It is
// called every rateLimitSweepEvery requests so abandoned keys do not stay in
// memory forever.
func (l *RateLimiter) sweepLocked(now time.Time) {
	for {
		oldest := l.order.Back()
		if oldest == nil {
			return
		}
		bucket := oldest.Value.(*rateBucket)
		if now.Sub(bucket.last) < rateLimitBucketIdleTTL {
			break
		}
		l.order.Remove(oldest)
		delete(l.buckets, bucket.key)
	}
	// Defensive: releaseSSE deletes an entry when its last stream ends, so no
	// zero-count entry should ever be retained.
	for key, count := range l.sse {
		if count <= 0 {
			delete(l.sse, key)
		}
	}
}

// evictLocked drops least-recently-used buckets until a new entry fits under
// the cap. It removes at most one element in steady state and is O(1); the loop
// also lets tests lower maxEntries at runtime without leaking entries.
func (l *RateLimiter) evictLocked() {
	for len(l.buckets) >= l.maxEntries {
		oldest := l.order.Back()
		if oldest == nil {
			return
		}
		bucket := oldest.Value.(*rateBucket)
		l.order.Remove(oldest)
		delete(l.buckets, bucket.key)
	}
}

// acquireSSE reserves one stream slot for key. Live entries are never evicted:
// the count is the reservation itself, so dropping it would admit more than
// sseMax concurrent streams. A new key is instead rejected with 429 while the
// map is full (which requires that many genuinely live connections). The map
// therefore never exceeds the cap and holds no zero-count entries.
func (l *RateLimiter) acquireSSE(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	count := l.sse[key]
	if count > 0 {
		if count >= l.sseMax {
			return false
		}
		l.sse[key] = count + 1
		return true
	}
	if len(l.sse) >= l.maxEntries {
		return false
	}
	l.sse[key] = 1
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

// isSSERequest reports whether the request targets one of the real long-lived
// event streams. The set is closed and path-based: Accept: text/event-stream is
// client-controlled and must never move an ordinary request out of its token
// bucket. GET /api/stream is the global channel stream (including the
// agent:{session} channels) and GET /api/versions/{id}/stream is the version
// channel. GET /api/stream/events is a JSON snapshot and stays ordinary.
// A new streaming endpoint must be added here explicitly.
func isSSERequest(r *http.Request) bool {
	if r == nil || r.Method != http.MethodGet {
		return false
	}
	switch r.URL.Path {
	case "/api/stream":
		return true
	}
	const versionPrefix = "/api/versions/"
	if !strings.HasPrefix(r.URL.Path, versionPrefix) || !strings.HasSuffix(r.URL.Path, "/stream") {
		return false
	}
	versionID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, versionPrefix), "/stream")
	return versionID != "" && !strings.Contains(versionID, "/")
}
