package platform_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func TestErrorEnvelopeAndRequestID(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r = r.WithContext(platform.WithRequestID(r.Context(), "req_12345678"))
	platform.WriteError(rec, r, http.StatusBadRequest, "invalid_argument", "bad", platform.WithDetails(map[string]any{"field": "username"}))
	var body struct {
		Error platform.APIError `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if body.Error.RequestID != "req_12345678" || body.Error.Code != "invalid_argument" {
		t.Fatalf("%+v", body)
	}
	if body.Error.Message != "bad" {
		t.Fatalf("message=%q", body.Error.Message)
	}
	if body.Error.Retryable {
		t.Fatal("retryable must default to false")
	}
	if body.Error.Details["field"] != "username" {
		t.Fatalf("details=%v", body.Error.Details)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type=%q", ct)
	}
	if got := rec.Header().Get("X-Request-ID"); got != "req_12345678" {
		t.Fatalf("X-Request-ID=%q", got)
	}
}

func TestWriteErrorDefaultsToEmptyDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r = r.WithContext(platform.WithRequestID(r.Context(), "req_12345678"))
	platform.WriteError(rec, r, http.StatusNotFound, "not_found", "missing")

	var raw struct {
		Error struct {
			Details json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if string(raw.Error.Details) != "{}" {
		t.Fatalf("details=%s, want {}", raw.Error.Details)
	}
}

func TestWriteErrorOptionsAndHeaderFallback(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-ID", "req_header1234")
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	// No platform request ID in the context: WriteError falls back to the
	// X-Request-ID header set by the middleware, so the envelope is never
	// missing the ID.
	platform.WriteError(rec, r, http.StatusServiceUnavailable, platform.CodeUnavailable, "down",
		platform.WithRetryable(true))

	var body struct {
		Error platform.APIError `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if body.Error.RequestID != "req_header1234" {
		t.Fatalf("request_id=%q", body.Error.RequestID)
	}
	if !body.Error.Retryable {
		t.Fatal("retryable was not applied")
	}
	if body.Error.Details == nil {
		t.Fatal("details must be an object, not nil")
	}
}

func TestRequestIDContext(t *testing.T) {
	if got := platform.RequestID(t.Context()); got != "" {
		t.Fatalf("empty context request id=%q", got)
	}
	ctx := platform.WithRequestID(t.Context(), "req_first")
	if got := platform.RequestID(ctx); got != "req_first" {
		t.Fatalf("request id=%q", got)
	}
	ctx = platform.WithRequestID(ctx, "req_second")
	if got := platform.RequestID(ctx); got != "req_second" {
		t.Fatalf("overridden request id=%q", got)
	}
}

// TestErrorCodes pins the API error codes from the design's §9.1 and §8.5.
func TestErrorCodes(t *testing.T) {
	want := map[string]string{
		platform.CodeInvalidArgument:     "invalid_argument",
		platform.CodeUnauthenticated:     "unauthenticated",
		platform.CodeForbidden:           "forbidden",
		platform.CodeNotFound:            "not_found",
		platform.CodeConflict:            "conflict",
		platform.CodeIdempotencyConflict: "idempotency_conflict",
		platform.CodeRateLimited:         "rate_limited",
		platform.CodeInternal:            "internal",
		platform.CodeUnavailable:         "unavailable",
		platform.CodeStorageCorrupted:    "storage_corrupted",
		platform.CodeLLMNotConfigured:    "llm_not_configured",
		platform.CodeLLMAuthFailed:       "llm_auth_failed",
		platform.CodeLLMRateLimited:      "llm_rate_limited",
		platform.CodeLLMBadRequest:       "llm_bad_request",
		platform.CodeLLMUpstreamError:    "llm_upstream_error",
		platform.CodeLLMTimeout:          "llm_timeout",
		platform.CodeLLMInvalidResponse:  "llm_invalid_response",
	}
	for got, expected := range want {
		if got != expected {
			t.Fatalf("error code constant %q, want %q", got, expected)
		}
	}
}

func TestParseLimit(t *testing.T) {
	valid := map[string]int{
		"":    platform.DefaultLimit,
		"1":   1,
		"20":  20,
		"100": 100,
		" 7 ": 7,
	}
	for raw, want := range valid {
		got, err := platform.ParseLimit(raw)
		if err != nil {
			t.Fatalf("ParseLimit(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("ParseLimit(%q)=%d, want %d", raw, got, want)
		}
	}
	if platform.DefaultLimit != 20 {
		t.Fatalf("DefaultLimit=%d, want 20", platform.DefaultLimit)
	}
	if platform.MaxLimit != 100 {
		t.Fatalf("MaxLimit=%d, want 100", platform.MaxLimit)
	}

	for _, raw := range []string{"0", "-1", "101", "200", "abc", "1.5"} {
		if got, err := platform.ParseLimit(raw); err == nil {
			t.Fatalf("ParseLimit(%q)=%d, want an error", raw, got)
		}
	}
}

// TestLimitErrorMessages pins the two distinct limit messages of spec §5.6 C6
// and the structured details the HTTP layers copy into the error envelope. Both
// rejection classes must keep matching ErrInvalidLimit.
func TestLimitErrorMessages(t *testing.T) {
	type detailer interface {
		Details() map[string]any
	}

	for _, testCase := range []struct {
		raw     string
		message string
	}{
		{"abc", "limit must be an integer"},
		{"1.5", "limit must be an integer"},
		{"0", "limit must be between 1 and 100"},
		{"101", "limit must be between 1 and 100"},
		{"200", "limit must be between 1 and 100"},
	} {
		_, err := platform.ParseLimit(testCase.raw)
		if err == nil {
			t.Fatalf("ParseLimit(%q): want an error", testCase.raw)
		}
		if !errors.Is(err, platform.ErrInvalidLimit) {
			t.Fatalf("ParseLimit(%q) error %v does not unwrap to ErrInvalidLimit", testCase.raw, err)
		}
		if got := err.Error(); got != testCase.message {
			t.Fatalf("ParseLimit(%q) message=%q, want %q", testCase.raw, got, testCase.message)
		}
		var withDetails detailer
		if !errors.As(err, &withDetails) {
			t.Fatalf("ParseLimit(%q) error %T does not expose details", testCase.raw, err)
		}
		details := withDetails.Details()
		if details["field"] != "limit" || details["min"] != 1 || details["max"] != platform.MaxLimit {
			t.Fatalf("ParseLimit(%q) details=%v, want field=limit min=1 max=%d",
				testCase.raw, details, platform.MaxLimit)
		}
	}
}

func TestCursorRoundTrip(t *testing.T) {
	for _, id := range []int64{0, 1, 42, 1 << 40} {
		cursor := platform.EncodeCursor(id)
		for _, c := range []string{"+", "/", "="} {
			if strings.Contains(cursor, c) {
				t.Fatalf("cursor %q is not URL-safe", cursor)
			}
		}
		got, err := platform.DecodeCursor(cursor)
		if err != nil {
			t.Fatalf("DecodeCursor(EncodeCursor(%d)=%q): %v", id, cursor, err)
		}
		if got != id {
			t.Fatalf("round trip id=%d, got %d", id, got)
		}
	}

	if got, err := platform.DecodeCursor(""); err != nil || got != 0 {
		t.Fatalf("DecodeCursor(\"\")=%d, %v; want 0, nil (first page)", got, err)
	}
}

func TestDecodeCursorRejectsMalformed(t *testing.T) {
	for _, raw := range []string{
		"!!!",
		"not a cursor",
		base64.RawURLEncoding.EncodeToString([]byte("abc")),
		base64.RawURLEncoding.EncodeToString([]byte("-3")),
	} {
		if got, err := platform.DecodeCursor(raw); err == nil {
			t.Fatalf("DecodeCursor(%q)=%d, want an error", raw, got)
		}
	}
}

type decodeTarget struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func TestDecodeJSON(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"username":"qa01","password":"qa-pass-123"}`))
	var got decodeTarget
	if err := platform.DecodeJSON(r, &got, 0); err != nil {
		t.Fatalf("DecodeJSON: %v", err)
	}
	if got.Username != "qa01" || got.Password != "qa-pass-123" {
		t.Fatalf("decoded=%+v", got)
	}
}

func TestDecodeJSONRejectsUnknownFields(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"username":"qa01","role":"admin"}`))
	var got decodeTarget
	if err := platform.DecodeJSON(r, &got, 0); err == nil {
		t.Fatalf("unknown field was accepted: %+v", got)
	}
}

func TestDecodeJSONRejectsMalformedBodies(t *testing.T) {
	for _, body := range []string{
		`{"username":`,
		`{"username":"qa01"} {"username":"qa02"}`,
		`null`,
	} {
		r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
		var got decodeTarget
		if err := platform.DecodeJSON(r, &got, 0); err == nil {
			t.Fatalf("DecodeJSON(%q) accepted the body: %+v", body, got)
		}
	}
}

func TestDecodeJSONRejectsOversizedBody(t *testing.T) {
	oversized := `{"username":"` + strings.Repeat("a", int(platform.DefaultMaxBodyBytes)) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(oversized))
	var got decodeTarget
	err := platform.DecodeJSON(r, &got, 0)
	if err == nil {
		t.Fatal("oversized body was accepted")
	}
	if !errors.Is(err, platform.ErrBodyTooLarge) {
		t.Fatalf("error=%v, want ErrBodyTooLarge", err)
	}
}

func TestDecodeJSONUsesEndpointBodyLimit(t *testing.T) {
	body := `{"username":"` + strings.Repeat("a", 100) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
	var got decodeTarget
	if err := platform.DecodeJSON(r, &got, 32); !errors.Is(err, platform.ErrBodyTooLarge) {
		t.Fatalf("error=%v, want ErrBodyTooLarge", err)
	}

	r = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"username":"qa01"}`))
	if err := platform.DecodeJSON(r, &got, 32); err != nil {
		t.Fatalf("DecodeJSON within the endpoint limit: %v", err)
	}
}
