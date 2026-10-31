package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

func TestJSONWrapsData(t *testing.T) {
	rec := httptest.NewRecorder()
	JSON(rec, http.StatusCreated, map[string]string{"id": "1"})

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	var body struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data["id"] != "1" {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestErrorWritesEnvelopeAndRequestID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/versions", nil)
	req = req.WithContext(requestid.With(req.Context(), "req_12345678"))
	rec := httptest.NewRecorder()

	Error(rec, req, platform.Invalid("title", "required"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("X-Request-ID"); got != "req_12345678" {
		t.Fatalf("header request id = %q", got)
	}
	var body struct {
		Error struct {
			Code      string         `json:"code"`
			Message   string         `json:"message"`
			RequestID string         `json:"request_id"`
			Retryable bool           `json:"retryable"`
			Details   map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "invalid_argument" || body.Error.RequestID != "req_12345678" {
		t.Fatalf("error = %#v", body.Error)
	}
	if body.Error.Details["field"] != "title" {
		t.Fatalf("details = %#v", body.Error.Details)
	}
}

func TestErrorUnknownAndUnauthenticated(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/versions", nil)
	Error(rec, req, errors.New("boom"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	Error(rec, req, platform.Unauthenticated())
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("WWW-Authenticate = %q", got)
	}
}
