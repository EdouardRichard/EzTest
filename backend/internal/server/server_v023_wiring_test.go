package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	llmapi "github.com/EdouardRichard/EzTest/backend/internal/llm/interfaces/httpapi"
)

// TestV023LLMResetRouteMounted pins the C-01/S-01 route registration: behind a
// denying middleware a mounted route answers 401, never the ServeMux 404.
func TestV023LLMResetRouteMounted(t *testing.T) {
	mux := http.NewServeMux()
	deny := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
	}
	registerLLMRoutes(mux, deny, deny, llmapi.New(nil))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/admin/llm/configs/1/models/2/reset-params", nil))
	if rec.Code == http.StatusNotFound {
		t.Fatal("POST reset-params is not mounted (got 404)")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 behind deny middleware", rec.Code)
	}
}

// TestV023VersionAttachmentListMounted pins the C-05 route registration.
func TestV023VersionAttachmentListMounted(t *testing.T) {
	mux := newW0Mux(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/versions/7/attachments", nil))
	if rec.Code == http.StatusNotFound {
		t.Fatal("GET version attachments is not mounted (got 404)")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 behind deny middleware", rec.Code)
	}
}
