package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/server"
)

// TestHealthVersion021 pins the 0.2.1 release identity. The composition root
// constant is what server.New feeds into GET /api/health, and the S0
// acceptance environment check asserts that version (spec §12.5, §13.2).
func TestHealthVersion021(t *testing.T) {
	if version != "0.2.1" {
		t.Fatalf("API version constant = %q, want %q", version, "0.2.1")
	}

	// No database is wired in this unit test, so readiness is 503; the health
	// envelope still reports the release identity S0 reads.
	rec := httptest.NewRecorder()
	server.New(server.Deps{Version: version}).ServeHTTP(
		rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("health status = %d, want 503 (no db in this unit test)", rec.Code)
	}
	var body struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /api/health: %v", err)
	}
	if body.Data.Version != "0.2.1" {
		t.Fatalf("/api/health version = %q, want %q", body.Data.Version, "0.2.1")
	}
}
