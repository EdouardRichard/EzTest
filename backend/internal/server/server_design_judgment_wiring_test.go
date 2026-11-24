package server

// T16 wiring assertions: the override/judgment-read routes must be mounted by
// registerDesignRoutes (never 404), and the override route must sit behind the
// admin middleware -- a non-admin bearer is rejected at the route boundary,
// independent of the handler's own defense-in-depth admin guard (pinned in
// internal/design/interfaces/httpapi's judgment_handler_test.go).

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	designapi "github.com/EdouardRichard/EzTest/backend/internal/design/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// TestDesignJudgmentRoutesMounted pins that both new judgment routes exist in
// the design route table: behind denyAuth they answer 401, never 404.
func TestDesignJudgmentRoutesMounted(t *testing.T) {
	mux := http.NewServeMux()
	registerDesignRoutes(mux, denyAuth, denyAuth, designapi.New(nil, nil))
	cases := []struct{ method, path string }{
		{http.MethodPost, "/api/versions/1001/design/judgments/7001/override"},
		{http.MethodGet, "/api/versions/1001/design/judgments"},
	}
	for _, tc := range cases {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s = %d, want 401 (mounted behind auth, never 404)", tc.method, tc.path, recorder.Code)
		}
	}
}

// TestDesignJudgmentOverrideRouteRequiresAdmin pins the route-level admin
// guard using the REAL RequireAdmin/RequireAuth middleware (the same one the
// composition root installs): a user-role bearer is rejected with 403 before
// the handler ever runs; the judgment read route stays reachable by a user,
// matching the design graph/case read visibility.
func TestDesignJudgmentOverrideRouteRequiresAdmin(t *testing.T) {
	muxFor := func(role string) *http.ServeMux {
		auth := fakeAuthenticator{actor: platform.Actor{ID: 7, Role: role}}
		authenticated := RequireAuth(auth)
		admin := RequireAdmin(auth)
		mux := http.NewServeMux()
		registerDesignRoutes(mux, authenticated, admin, designapi.New(nil, nil))
		return mux
	}

	userMux := muxFor("user")
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/versions/1001/design/judgments/7001/override", strings.NewReader(`{"reason":"接受风险"}`))
	request.Header.Set("Authorization", "Bearer ezt_token")
	userMux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("user override at the route boundary = %d body=%s, want 403", recorder.Code, recorder.Body.String())
	}

	// The judgment read route is NOT admin-gated: a user bearer passes the
	// route boundary (the unwired handler then answers 503, proving the
	// request reached the handler rather than being blocked as forbidden).
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/versions/1001/design/judgments", nil)
	request.Header.Set("Authorization", "Bearer ezt_token")
	userMux.ServeHTTP(recorder, request)
	if recorder.Code == http.StatusForbidden || recorder.Code == http.StatusUnauthorized {
		t.Fatalf("user judgment read at the route boundary = %d body=%s, want it to reach the handler (not 401/403)", recorder.Code, recorder.Body.String())
	}

	adminMux := muxFor("admin")
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/versions/1001/design/judgments/7001/override", strings.NewReader(`{"reason":"接受风险"}`))
	request.Header.Set("Authorization", "Bearer ezt_token")
	adminMux.ServeHTTP(recorder, request)
	if recorder.Code == http.StatusForbidden || recorder.Code == http.StatusUnauthorized {
		t.Fatalf("admin override at the route boundary = %d body=%s, want it to reach the handler (not 401/403)", recorder.Code, recorder.Body.String())
	}
}
