package server

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
)

// RateLimitSettingKey is the per-instance request limit (requests/minute). A
// zero value disables limiting. The documented total rate for a deployment is
// replicas x limit.
const RateLimitSettingKey = "security.rate_limit_per_minute"

// LimitSettings reads the live rate-limit setting. *settingsapp.Service
// satisfies it.
type LimitSettings interface {
	Int(ctx context.Context, key string) (int64, error)
}

// rateLimiter is a per-instance token bucket. Each API replica enforces its own
// bucket; there is no shared Redis/DB quota in 0.2 (architecture §12.4 allows
// per-instance quotas and requires the total rate to be documented).
type rateLimiter struct {
	settings LimitSettings

	mu     sync.Mutex
	limit  int64
	tokens float64
	last   time.Time
	now    func() time.Time
}

// RateLimit returns the middleware that enforces
// security.rate_limit_per_minute. Health probes and the long-lived SSE stream
// are never limited.
func RateLimit(settings LimitSettings) func(http.Handler) http.Handler {
	limiter := &rateLimiter{settings: settings, now: time.Now}
	return limiter.middleware
}

func (l *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if l.settings == nil || skipRateLimit(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		limit, err := l.settings.Int(r.Context(), RateLimitSettingKey)
		if err != nil || limit <= 0 {
			next.ServeHTTP(w, r)
			return
		}
		allowed, retryAfter := l.take(limit)
		w.Header().Set("X-RateLimit-Limit", strconv.FormatInt(limit, 10))
		if !allowed {
			if retryAfter < 1 {
				retryAfter = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			httpx.Error(w, r, platform.NewError(http.StatusTooManyRequests, "rate_limited", "too many requests"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// take consumes one token from the bucket. The burst is max(limit/10, 1) and
// the refill rate is limit/60 tokens per second.
func (l *rateLimiter) take(limit int64) (bool, int) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	burst := float64(limit) / 10
	if burst < 1 {
		burst = 1
	}
	if l.limit != limit || l.last.IsZero() {
		l.limit = limit
		l.tokens = burst
		l.last = now
	}
	elapsed := now.Sub(l.last).Seconds()
	if elapsed > 0 {
		l.tokens = math.Min(burst, l.tokens+elapsed*float64(limit)/60)
		l.last = now
	}
	if l.tokens >= 1 {
		l.tokens--
		return true, 0
	}
	missing := 1 - l.tokens
	retry := int(math.Ceil(missing / (float64(limit) / 60)))
	return false, retry
}

func skipRateLimit(path string) bool {
	switch {
	case path == "/healthz", path == "/api/health", path == "/api/health/ready":
		return true
	case path == "/api/stream" || strings.HasPrefix(path, "/api/stream/"):
		return true
	default:
		return false
	}
}
