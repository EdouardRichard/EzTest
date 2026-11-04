package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

// TestVersionReadServerRoutesMounted pins the Task 62 composition root: the four
// version-scoped read routes are absent (404, never a panic) when
// P6.VersionRead is nil and mounted behind the bearer middleware when it is
// set. The P6 handler group is the mount point used by the running assembly.
func TestVersionReadServerRoutesMounted(t *testing.T) {
	paths := []string{
		"/api/versions/7/facts",
		"/api/versions/7/plan-items",
		"/api/versions/7/work-items",
		"/api/versions/7/work-items/401",
	}

	t.Run("nil P6.VersionRead leaves the read routes unmounted", func(t *testing.T) {
		handler := New(Deps{Version: "0.2.1-p0-t6"})
		for _, path := range paths {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("GET %s status = %d, want 404 (version read routes not wired)",
					path, recorder.Code)
			}
		}
	})

	t.Run("P6.VersionRead mounts the read routes behind the bearer middleware", func(t *testing.T) {
		handler := New(Deps{
			Version: "0.2.1-p0-t6",
			P6: &P6Handlers{
				VersionRead: versionapi.NewVersionReadHandler(nil, nil),
			},
		})
		for _, path := range paths {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("GET %s status = %d, want 401 (mounted behind auth)",
					path, recorder.Code)
			}
		}
	})
}
