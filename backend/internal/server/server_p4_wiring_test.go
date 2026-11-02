package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	chainapi "github.com/EdouardRichard/EzTest/backend/internal/chain/interfaces/httpapi"
	deliveryapi "github.com/EdouardRichard/EzTest/backend/internal/delivery/interfaces/httpapi"
	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

// denyAuth is the minimal bearer guard used by the wiring assertions: the
// route exists but never reaches the handler without a session.
func denyAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func serve(mux *http.ServeMux, method, path string) int {
	request := httptest.NewRequest(method, path, nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder.Code
}

func TestP4WiringNilHandlersDoNotPanic(t *testing.T) {
	mux := http.NewServeMux()
	registerDeliveryRoutes(mux, denyAuth, nil)
	registerChainRoutes(mux, denyAuth, nil)
	registerVersionP4Routes(mux, denyAuth, nil, nil)

	if status := serve(mux, http.MethodGet, "/api/versions/1/issues"); status != http.StatusNotFound {
		t.Fatalf("nil delivery handler status = %d, want 404", status)
	}
	if status := serve(mux, http.MethodGet, "/api/versions/1/exploration-chain"); status != http.StatusNotFound {
		t.Fatalf("nil chain handler status = %d, want 404", status)
	}
}

func TestP4WiringRoutesAreBehindAuthentication(t *testing.T) {
	mux := http.NewServeMux()
	// Handler construction is nil-safe; the auth middleware rejects before the
	// service is touched.
	versionHandler := versionapi.New(nil, versionapi.WithVersionReopener(nil))
	requirementHandler := versionapi.NewRequirementHandler(nil,
		versionapi.WithRequirementReopener(nil),
		versionapi.WithRequirementClosures(nil),
	)
	registerVersionP4Routes(mux, denyAuth, versionHandler, requirementHandler)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/versions/1/reopen"},
		{http.MethodPost, "/api/versions/1/requirements/2/reopen"},
	}
	for _, tc := range cases {
		if status := serve(mux, tc.method, tc.path); status != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401 (route registered behind auth)", tc.method, tc.path, status)
		}
	}
	if status := serve(mux, http.MethodGet, "/api/versions/1/reopen"); status != http.StatusMethodNotAllowed {
		t.Fatalf("GET reopen status = %d, want 405", status)
	}
}

func TestP4WiringDeliveryAndChainRoutesRegistered(t *testing.T) {
	mux := http.NewServeMux()
	registerDeliveryRoutes(mux, denyAuth, deliveryapi.New(nil))
	registerChainRoutes(mux, denyAuth, chainapi.New(nil))

	for _, path := range []string{
		"/api/versions/1/issues",
		"/api/versions/1/issues/2",
		"/api/versions/1/reports",
		"/api/versions/1/reports/2",
		"/api/versions/1/reports/2/revisions/1",
		"/api/versions/1/reports/2/revisions/1/html",
		"/api/versions/1/exploration-chain",
	} {
		if status := serve(mux, http.MethodGet, path); status != http.StatusUnauthorized {
			t.Fatalf("GET %s status = %d, want 401", path, status)
		}
	}
	for _, path := range []string{
		"/api/versions/1/issues/2/confirm",
		"/api/versions/1/issues/2/dismiss",
		"/api/versions/1/issues/2/reopen",
		"/api/versions/1/issues/2/merge",
		"/api/versions/1/reports/2/regenerate",
	} {
		if status := serve(mux, http.MethodPost, path); status != http.StatusUnauthorized {
			t.Fatalf("POST %s status = %d, want 401", path, status)
		}
	}
}
