package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/config"
)

func TestOpenAPIDocumentIsServedPublicly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openapi.yaml")
	content := []byte("openapi: 3.0.3\ninfo:\n  title: EzTest P0\n  version: 0.1.0\npaths: {}\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write temp openapi document: %v", err)
	}

	// Auth is wired, so the middleware chain would reject an unauthenticated
	// /api request; the OpenAPI document must still be public.
	h := New(config.Config{Env: "test", OpenAPIPath: path}, testLogger(), Dependencies{
		Auth: acceptingAuth{token: "ezt_valid"},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s, want 200 without a token", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/yaml" {
		t.Fatalf("Content-Type=%q, want application/yaml", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatalf("body=%q, want the document bytes", rec.Body)
	}
}

func TestOpenAPIDocumentMissingIsNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")
	h := New(config.Config{Env: "test", OpenAPIPath: path}, testLogger(), Dependencies{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d body=%s, want 404", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type=%q, want the shared JSON error envelope", ct)
	}
	if !strings.Contains(rec.Body.String(), `"code":"not_found"`) {
		t.Fatalf("body=%s, want a not_found error envelope", rec.Body)
	}
}
