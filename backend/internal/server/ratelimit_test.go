package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type fakeLimitSettings struct {
	mu    sync.Mutex
	limit int64
	err   error
	calls int
}

func (f *fakeLimitSettings) Int(context.Context, string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.limit, f.err
}

func (f *fakeLimitSettings) set(limit int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.limit = limit
}

func TestRateLimitDisabledWhenZero(t *testing.T) {
	settings := &fakeLimitSettings{}
	handler := RateLimit(settings)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for i := 0; i < 20; i++ {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/versions", nil))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("request %d status = %d, want 204", i, recorder.Code)
		}
	}
}

func TestRateLimitReturns429WithRetryAfter(t *testing.T) {
	settings := &fakeLimitSettings{limit: 30}
	handler := RateLimit(settings)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	var limited int
	for i := 0; i < 40; i++ {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/versions", nil))
		if recorder.Code == http.StatusTooManyRequests {
			limited++
			if recorder.Header().Get("Retry-After") == "" {
				t.Fatal("429 response misses Retry-After")
			}
			if recorder.Header().Get("X-RateLimit-Limit") != "30" {
				t.Fatalf("X-RateLimit-Limit = %q", recorder.Header().Get("X-RateLimit-Limit"))
			}
		}
	}
	if limited == 0 {
		t.Fatal("rate limit never returned 429")
	}
}

func TestRateLimitSkipsHealthAndSSE(t *testing.T) {
	settings := &fakeLimitSettings{limit: 1}
	handler := RateLimit(settings)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, path := range []string{"/healthz", "/api/health", "/api/health/ready", "/api/stream", "/api/internal/executors/heartbeat"} {
		for i := 0; i < 5; i++ {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
			if recorder.Code != http.StatusNoContent {
				t.Fatalf("%s request %d status = %d", path, i, recorder.Code)
			}
		}
	}
}

func TestRateLimitReadsSettingPerRequest(t *testing.T) {
	settings := &fakeLimitSettings{limit: 30}
	handler := RateLimit(settings)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	// Drain the bucket.
	limited := false
	for i := 0; i < 40; i++ {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/versions", nil))
		if recorder.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("expected the bucket to drain")
	}
	settings.set(0)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/versions", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("disabled limit still rejected: %d", recorder.Code)
	}
}
