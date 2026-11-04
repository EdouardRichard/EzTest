package server

import (
	"net/http"
	"testing"

	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

// TestRequirementCleanupRoutesAreBehindAuthentication pins the Task 83 mount
// point: the draft cleanup routes are part of the normal requirement route
// table and never bypass the bearer guard.
func TestRequirementCleanupRoutesAreBehindAuthentication(t *testing.T) {
	mux := http.NewServeMux()
	registerRequirementRoutes(mux, denyAuth, versionapi.NewRequirementHandler(nil))

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/versions/1/requirements/dedupe"},
		{http.MethodPost, "/api/versions/1/requirements/merge"},
		{http.MethodDelete, "/api/versions/1/requirements/2"},
	}
	for _, tc := range cases {
		if status := serve(mux, tc.method, tc.path); status != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401 (route registered behind auth)",
				tc.method, tc.path, status)
		}
	}
}
