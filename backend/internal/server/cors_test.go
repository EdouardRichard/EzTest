package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCORSPreflight(t *testing.T) {
	h := CORS("http://localhost:6868")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	req := httptest.NewRequest(http.MethodOptions, "/api/users", nil)
	req.Header.Set("Origin", "http://localhost:6868")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Fatalf("code=%d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:6868" {
		t.Fatal("missing ACAO")
	}
	if !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatal("missing auth header")
	}
}

func TestCORSPreflightAdvertisesContractHeadersAndMethods(t *testing.T) {
	h := CORS("http://localhost:6868")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("preflight must not reach the next handler")
	}))
	req := httptest.NewRequest(http.MethodOptions, "/api/users", nil)
	req.Header.Set("Origin", "http://localhost:6868")
	req.Header.Set("Access-Control-Request-Method", "PATCH")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	for _, header := range []string{"Authorization", "Content-Type", "X-Request-ID", "Idempotency-Key", "Last-Event-ID"} {
		if !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), header) {
			t.Fatalf("Access-Control-Allow-Headers=%q misses %s", rec.Header().Get("Access-Control-Allow-Headers"), header)
		}
	}
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		if !strings.Contains(rec.Header().Get("Access-Control-Allow-Methods"), method) {
			t.Fatalf("Access-Control-Allow-Methods=%q misses %s", rec.Header().Get("Access-Control-Allow-Methods"), method)
		}
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("credentials must be disabled, got %q", got)
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Origin") {
		t.Fatalf("Vary=%q must contain Origin", rec.Header().Get("Vary"))
	}
}

func TestCORSActualRequestExposesHeaders(t *testing.T) {
	h := CORS("http://localhost:6868")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	req.Header.Set("Origin", "http://localhost:6868")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:6868" {
		t.Fatalf("Access-Control-Allow-Origin=%q", got)
	}
	exposed := rec.Header().Get("Access-Control-Expose-Headers")
	for _, header := range []string{"X-Request-ID", "X-Snapshot-Cursor", "Idempotent-Replayed"} {
		if !strings.Contains(exposed, header) {
			t.Fatalf("Access-Control-Expose-Headers=%q misses %s", exposed, header)
		}
	}
	if rec.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("credentials must be disabled")
	}
}

func TestCORSRejectsForeignOrigin(t *testing.T) {
	h := CORS("http://localhost:6868")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("foreign origin was allowed: %q", got)
	}
	if got := rec.Header().Get("Access-Control-Expose-Headers"); got != "" {
		t.Fatalf("foreign origin got exposed headers: %q", got)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("non-CORS request must still be served: code=%d", rec.Code)
	}
}

func TestCORSPreflightFromForeignOriginHasNoAllowOrigin(t *testing.T) {
	h := CORS("http://localhost:6868")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("preflight must not reach the next handler")
	}))
	req := httptest.NewRequest(http.MethodOptions, "/api/users", nil)
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("code=%d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("foreign preflight was allowed: %q", got)
	}
}

func TestCORSPassesThroughRequestsWithoutOrigin(t *testing.T) {
	h := CORS("http://localhost:6868")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("code=%d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unexpected CORS header for a non-CORS request: %q", got)
	}
}
