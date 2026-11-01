package server

import (
	"net/http"
	"testing"

	deliveryapi "github.com/EdouardRichard/EzTest/backend/internal/delivery/interfaces/httpapi"
	executionhttp "github.com/EdouardRichard/EzTest/backend/internal/execution/interfaces/httpapi"
	knowledgeapp "github.com/EdouardRichard/EzTest/backend/internal/knowledge/application"
	knowledgeapi "github.com/EdouardRichard/EzTest/backend/internal/knowledge/interfaces/httpapi"
	orchestrationapp "github.com/EdouardRichard/EzTest/backend/internal/orchestration/application"
	orchestrationapi "github.com/EdouardRichard/EzTest/backend/internal/orchestration/interfaces/httpapi"
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
