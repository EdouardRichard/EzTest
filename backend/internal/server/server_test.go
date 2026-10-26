package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/config"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
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
