package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintelapi "github.com/EdouardRichard/EzTest/backend/internal/codeintel/interfaces/httpapi"
	resourceapi "github.com/EdouardRichard/EzTest/backend/internal/resource/interfaces/httpapi"
	visualapi "github.com/EdouardRichard/EzTest/backend/internal/visual/interfaces/httpapi"
)

// denyAdmin is the admin guard used by the wiring assertions: the route exists
// and the admin middleware runs before the handler/service.
func denyAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
}

// p5CodeIntelHandlers builds the handler set with zero-value services: the
// wiring assertions only exercise route registration and middleware ordering.
func p5CodeIntelHandlers() *CodeIntelHandlers {
	return &CodeIntelHandlers{
		Index: codeintelapi.New(
			&codeintelapp.IndexService{}, &codeintelapp.QueryService{},
			codeintelapi.WithAnalysis(&codeintelapp.AnalysisService{}),
		),
		Anchors:   codeintelapi.NewAnchorHandler(&codeintelapp.AnchorService{}),
		Reviews:   codeintelapi.NewReviewHandler(&codeintelapp.ReviewService{}),
		Impact:    codeintelapi.NewImpactHandler(&codeintelapp.ImpactService{}),
		Proposals: codeintelapi.NewProposalHandler(&codeintelapp.ProposalService{}),
	}
}

// TestP5WiringCodeIntelRoutesAreBehindAuthentication pins every spec §4.2
// code path (#1-#25) to the authenticated middleware.
func TestP5WiringCodeIntelRoutesAreBehindAuthentication(t *testing.T) {
	mux := http.NewServeMux()
	registerCodeIntelRoutes(mux, denyAuth, p5CodeIntelHandlers())

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/versions/1/code/index"},
		{http.MethodGet, "/api/versions/1/code/repos"},
		{http.MethodGet, "/api/code/repos/2/index-runs/latest"},
		{http.MethodGet, "/api/code/repos/2/index-runs/3"},
		{http.MethodGet, "/api/code/repos/2/tree"},
		{http.MethodGet, "/api/code/repos/2/file"},
		{http.MethodGet, "/api/code/repos/2/diff"},
		{http.MethodGet, "/api/code/repos/2/diff/file"},
		{http.MethodGet, "/api/code/repos/2/facts"},
		{http.MethodGet, "/api/code/repos/2/search"},
		{http.MethodGet, "/api/code/repos/2/commits/abc"},
		{http.MethodGet, "/api/versions/1/code/changes"},
		{http.MethodPost, "/api/versions/1/code/analyses"},
		{http.MethodGet, "/api/versions/1/code/analyses/latest"},
		{http.MethodGet, "/api/versions/1/code/analyses/3"},
		{http.MethodPatch, "/api/versions/1/code/analyses/3/candidates/4"},
		{http.MethodGet, "/api/versions/1/code/impact"},
		{http.MethodGet, "/api/versions/1/code/anchors"},
		{http.MethodPost, "/api/versions/1/code/anchors"},
		{http.MethodPost, "/api/versions/1/code/anchors/5/validate"},
		{http.MethodDelete, "/api/versions/1/code/anchors/5"},
		{http.MethodGet, "/api/versions/1/code/facts/6/reviews"},
		{http.MethodPost, "/api/versions/1/code/facts/6/reviews"},
		{http.MethodPost, "/api/versions/1/code/page-api-proposals"},
		{http.MethodGet, "/api/versions/1/code/page-api-proposals"},
	}
	for _, tc := range cases {
		if status := serve(mux, tc.method, tc.path); status != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401 (route registered behind auth)", tc.method, tc.path, status)
		}
	}

	// A route that only exists inside the authenticated group must not be
	// reachable with another method (405, not 404).
	if status := serve(mux, http.MethodGet, "/api/versions/1/code/index"); status != http.StatusMethodNotAllowed {
		t.Fatalf("GET code index status = %d, want 405", status)
	}
}

// TestP5WiringVisualRoutesUseRoleMiddleware pins the visual paths (#30-#38):
// configuration/read routes require login, baseline approval requires admin.
func TestP5WiringVisualRoutesUseRoleMiddleware(t *testing.T) {
	mux := http.NewServeMux()
	registerVisualRoutes(mux, denyAuth, denyAdmin, visualapi.New(visualapi.Deps{}))

	authenticated := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/versions/1/visual/assertions"},
		{http.MethodPut, "/api/versions/1/visual/assertions/2"},
		{http.MethodGet, "/api/versions/1/visual/assertions/3"},
		{http.MethodGet, "/api/versions/1/visual/assertions/3/baselines"},
		{http.MethodGet, "/api/versions/1/visual/diffs"},
		{http.MethodGet, "/api/versions/1/visual/diffs/4"},
		{http.MethodGet, "/api/versions/1/visual/overview"},
		{http.MethodGet, "/api/quality/visual-metrics"},
	}
	for _, tc := range authenticated {
		if status := serve(mux, tc.method, tc.path); status != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401", tc.method, tc.path, status)
		}
	}

	if status := serve(mux, http.MethodPost, "/api/versions/1/visual/assertions/3/baselines"); status != http.StatusForbidden {
		t.Fatalf("baseline approval status = %d, want 403 (admin middleware)", status)
	}
}

// TestP5WiringResourceProposalConfirmRejectAreAdmin pins the four P5 resource
// confirmation routes (#26-#29) to the admin middleware.
func TestP5WiringResourceProposalConfirmRejectAreAdmin(t *testing.T) {
	mux := http.NewServeMux()
	registerResourceRoutes(mux, denyAuth, denyAdmin, resourceapi.New(nil))

	for _, path := range []string{
		"/api/resources/pages/1/confirm",
		"/api/resources/pages/1/reject",
		"/api/resources/apis/2/confirm",
		"/api/resources/apis/2/reject",
	} {
		if status := serve(mux, http.MethodPost, path); status != http.StatusForbidden {
			t.Fatalf("POST %s status = %d, want 403 (admin middleware)", path, status)
		}
	}
}

// TestP5WiringServerNilOptionalDeps pins that a P5 deployment can start with
// only the base wiring and that the P5 surfaces are registered when provided.
func TestP5WiringServerNilOptionalDeps(t *testing.T) {
	handler := New(Deps{Version: "0.2.0-p8"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("health status = %d, want 503 (no db in this wiring test)", rec.Code)
	}
	var body struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if body.Data.Version != "0.2.0-p8" {
		t.Fatalf("reported API version = %q, want 0.2.0-p8", body.Data.Version)
	}

	// P5 is optional: without the constructed surfaces the paths stay
	// unmounted, exactly like the P4 delivery/chain handlers.
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/versions/1/code/repos"},
		{http.MethodGet, "/api/versions/1/visual/overview"},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want 404 (optional P5 surface not wired)", tc.method, tc.path, rec.Code)
		}
	}
}

// TestP5WiringServerMountsP5Deps pins that the Deps fields are actually read by
// New: a non-nil handler set makes the paths routable (401 without a bearer
// instead of 404) and a nil set keeps them unmounted.
func TestP5WiringServerMountsP5Deps(t *testing.T) {
	handler := New(Deps{
		Version:   "0.2.0-p8",
		CodeIntel: p5CodeIntelHandlers(),
		Visual:    visualapi.New(visualapi.Deps{}),
	})

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/versions/1/code/anchors"},
		{http.MethodGet, "/api/versions/1/code/impact"},
		{http.MethodGet, "/api/versions/1/visual/assertions"},
		{http.MethodGet, "/api/quality/visual-metrics"},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401 (P5 Deps mounted)", tc.method, tc.path, rec.Code)
		}
	}
}
