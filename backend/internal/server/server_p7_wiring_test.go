package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentapi "github.com/EdouardRichard/EzTest/backend/internal/agent/interfaces/httpapi"
	deliveryapi "github.com/EdouardRichard/EzTest/backend/internal/delivery/interfaces/httpapi"
	executionhttp "github.com/EdouardRichard/EzTest/backend/internal/execution/interfaces/httpapi"
	knowledgeapp "github.com/EdouardRichard/EzTest/backend/internal/knowledge/application"
	knowledgeapi "github.com/EdouardRichard/EzTest/backend/internal/knowledge/interfaces/httpapi"
	orchestrationapp "github.com/EdouardRichard/EzTest/backend/internal/orchestration/application"
	orchestrationapi "github.com/EdouardRichard/EzTest/backend/internal/orchestration/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	storageapi "github.com/EdouardRichard/EzTest/backend/internal/storage/interfaces/httpapi"
)

// TestP7WiringNilHandlersDoNotPanic pins the optional-surface contract: a nil
// P7 group and a nil execution handler leave the route table of a P0-P6
// deployment unchanged.
func TestP7WiringNilHandlersDoNotPanic(t *testing.T) {
	mux := http.NewServeMux()
	registerP7Routes(mux, denyAuth, denyAdmin, nil, nil, nil)

	for _, path := range []string{
		"/api/knowledge/items",
		"/api/admin/instances",
		"/api/health/ready",
		"/api/admin/gc/runs",
		"/api/internal/executors/heartbeat",
	} {
		if status := serve(mux, http.MethodGet, path); status != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404 (nil P7 surface)", path, status)
		}
	}
}

// TestP7WiringRoutesAreBehindTheirMiddlewareClass pins every P7 operation to
// its middleware class. Zero-value services are enough: the middleware rejects
// before the handler bodies run.
func TestP7WiringRoutesAreBehindTheirMiddlewareClass(t *testing.T) {
	mux := http.NewServeMux()
	registerP7Routes(mux, denyAuth, denyAdmin,
		executionhttp.New(executionhttp.Deps{}),
		nil, // force the fail-closed internal token middleware
		&P7Handlers{
			Knowledge:     knowledgeapi.New(&knowledgeapp.Service{}),
			Orchestration: orchestrationapi.New(&orchestrationapp.Service{}),
			GC:            storageapi.NewGC(&storageapp.GCService{}),
		})

	authenticated := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/knowledge/items"},
		{http.MethodPost, "/api/knowledge/items"},
		{http.MethodGet, "/api/knowledge/items/1"},
		{http.MethodPatch, "/api/knowledge/items/1"},
		{http.MethodGet, "/api/knowledge/items/1/revisions"},
	}
	for _, tc := range authenticated {
		if status := serve(mux, tc.method, tc.path); status != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401 (authenticated route)", tc.method, tc.path, status)
		}
	}

	admin := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/knowledge/items/1/approve"},
		{http.MethodPost, "/api/knowledge/items/1/reject"},
		{http.MethodPost, "/api/knowledge/items/1/revoke"},
		{http.MethodGet, "/api/admin/instances"},
		{http.MethodPost, "/api/admin/instances/1/drain"},
		{http.MethodPost, "/api/admin/instances/1/activate"},
		{http.MethodPost, "/api/admin/gc/runs"},
		{http.MethodGet, "/api/admin/gc/runs"},
		{http.MethodGet, "/api/admin/gc/runs/1"},
	}
	for _, tc := range admin {
		if status := serve(mux, tc.method, tc.path); status != http.StatusForbidden {
			t.Fatalf("%s %s status = %d, want 403 (admin route)", tc.method, tc.path, status)
		}
	}

	// The readiness probe is public by contract: it reaches the handler, whose
	// zero-value service reports unavailable (503), never 401/403.
	if status := serve(mux, http.MethodGet, "/api/health/ready"); status != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/health/ready status = %d, want 503", status)
	}

	// A nil settings service makes the internal Bearer middleware fail closed.
	for _, path := range []string{
		"/api/internal/executors/register",
		"/api/internal/executors/heartbeat",
	} {
		if status := serve(mux, http.MethodPost, path); status != http.StatusUnauthorized {
			t.Fatalf("POST %s status = %d, want 401 (internal fail-closed)", path, status)
		}
	}
}

// TestP7WiringReportPDFRouteRegistered verifies the P7 delivery PDF download is
// mounted behind the existing bearer middleware by the P4 delivery assembly.
func TestP7WiringReportPDFRouteRegistered(t *testing.T) {
	mux := http.NewServeMux()
	registerDeliveryRoutes(mux, denyAuth, deliveryapi.New(nil))
	if status := serve(mux, http.MethodGet, "/api/versions/1/reports/2/revisions/1/pdf"); status != http.StatusUnauthorized {
		t.Fatalf("GET report PDF status = %d, want 401", status)
	}
}

// jobControlRouteService is the service double of the route-table assertions. A
// job ID of 502 models a rejected lifecycle transition (409); every other ID
// succeeds, so the test can tell a mounted route (200/409) from an unmounted
// one (404).
type jobControlRouteService struct{}

func (jobControlRouteService) Cancel(_ context.Context, _, versionID, jobID int64) (agentapp.JobView, error) {
	if jobID == 502 {
		return agentapp.JobView{}, platform.StateConflict("job cannot be canceled from its current state")
	}
	return agentapp.JobView{
		JobID: jobID, VersionID: versionID, Kind: agent.WorkItemKindExecuteBatch,
		State: agentapp.JobStateCancelled,
	}, nil
}

func (jobControlRouteService) Retry(_ context.Context, _, versionID, jobID int64) (agentapp.JobView, error) {
	if jobID == 502 {
		return agentapp.JobView{}, platform.StateConflict("job cannot be retried from its current state")
	}
	return agentapp.JobView{
		JobID: jobID, VersionID: versionID, Kind: agent.WorkItemKindExecuteBatch,
		State: string(agent.WorkItemOpen),
	}, nil
}

// serveJobControl sends an authenticated POST to a mounted job control route.
func serveJobControl(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, nil)
	request.Header.Set("Authorization", "Bearer ezt_wiring_token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// TestJobControlRoutesMounted pins the Task 60 wiring: the 0.2.1 job control
// routes are absent (404, never a panic) when Deps.Jobs is nil, mounted behind
// the bearer middleware when it is set, reach the application service (200 for
// an accepted action, 409 for a rejected one), and fail closed with 503 when the
// handler has no service.
func TestJobControlRoutesMounted(t *testing.T) {
	paths := []string{
		"/api/jobs/501/cancel",
		"/api/jobs/501/retry",
		"/api/versions/10/jobs/501/cancel",
		"/api/versions/10/jobs/501/retry",
	}

	t.Run("nil Deps.Jobs leaves both route shapes unmounted", func(t *testing.T) {
		handler := New(Deps{Version: "0.2.1-p2-t8"})
		for _, path := range paths {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("POST %s status = %d, want 404 (job control not wired)", path, recorder.Code)
			}
		}
	})

	t.Run("Deps.Jobs mounts both route shapes behind the bearer middleware", func(t *testing.T) {
		handler := New(Deps{
			Version: "0.2.1-p2-t8",
			Jobs:    agentapi.NewJobControlHandler(jobControlRouteService{}),
		})
		for _, path := range paths {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("POST %s status = %d, want 401 (route mounted behind auth)", path, recorder.Code)
			}
		}
	})

	// Mount the table directly so a fake authenticator can inject the actor the
	// handler requires. The New() assertions above prove the Deps.Jobs plumbing
	// and the real bearer middleware; this proves the mounted routes reach the
	// application service instead of answering 404.
	mux := http.NewServeMux()
	registerJobRoutes(mux, RequireAuth(fakeAuthenticator{actor: platform.Actor{ID: 7, Role: "user"}}),
		agentapi.NewJobControlHandler(jobControlRouteService{}))

	if recorder := serveJobControl(t, mux, "/api/jobs/501/cancel"); recorder.Code != http.StatusOK {
		t.Fatalf("POST direct cancel status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if recorder := serveJobControl(t, mux, "/api/versions/10/jobs/501/retry"); recorder.Code != http.StatusOK {
		t.Fatalf("POST version retry status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if recorder := serveJobControl(t, mux, "/api/jobs/502/retry"); recorder.Code != http.StatusConflict {
		t.Fatalf("POST rejected retry status = %d, want 409: %s", recorder.Code, recorder.Body.String())
	}

	// A mounted handler without a service must fail closed (503), never panic.
	mux = http.NewServeMux()
	registerJobRoutes(mux, RequireAuth(fakeAuthenticator{actor: platform.Actor{ID: 7, Role: "user"}}),
		agentapi.NewJobControlHandler(nil))
	if recorder := serveJobControl(t, mux, "/api/jobs/501/cancel"); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST unconfigured service status = %d, want 503: %s", recorder.Code, recorder.Body.String())
	}
}
