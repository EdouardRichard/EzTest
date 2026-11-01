package server

import (
	"net/http"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentapi "github.com/EdouardRichard/EzTest/backend/internal/agent/interfaces/httpapi"
	deliveryapi "github.com/EdouardRichard/EzTest/backend/internal/delivery/interfaces/httpapi"
	qualityapp "github.com/EdouardRichard/EzTest/backend/internal/quality/application"
	qualityapi "github.com/EdouardRichard/EzTest/backend/internal/quality/interfaces/httpapi"
	searchapp "github.com/EdouardRichard/EzTest/backend/internal/search/application"
	searchapi "github.com/EdouardRichard/EzTest/backend/internal/search/interfaces/httpapi"
	workspaceapp "github.com/EdouardRichard/EzTest/backend/internal/workspace/application"
	workspaceapi "github.com/EdouardRichard/EzTest/backend/internal/workspace/interfaces/httpapi"
)

// TestP6WiringRoutes pins every P6 operation to its middleware class. The
// handlers use zero-value services: the assertions only exercise registration
// and middleware ordering, never the service bodies.
func TestP6WiringRoutes(t *testing.T) {
	mux := http.NewServeMux()
	registerP6Routes(mux, denyAuth, denyAdmin, &P6Handlers{
		Quality:     qualityapi.New(&qualityapp.Service{}),
		Search:      searchapi.New(&searchapp.Service{}),
		Todos:       workspaceapi.New(&workspaceapp.Service{}),
		Global:      deliveryapi.NewGlobal(nil, nil),
		AgentCenter: agentapi.NewAgentCenterHandler(&agentapp.AgentCenterService{}),
		Profiles:    agentapi.NewProfileHandler(&agentapp.ProfileAdminService{}),
	})

	authenticated := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/search?q=x"},
		{http.MethodGet, "/api/todos"},
		{http.MethodGet, "/api/quality/overview"},
		{http.MethodGet, "/api/issues"},
		{http.MethodGet, "/api/reports"},
		{http.MethodGet, "/api/agent-center/catalog"},
		{http.MethodGet, "/api/agent-center/runs"},
		{http.MethodGet, "/api/agent-center/quality"},
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
		{http.MethodGet, "/api/quality/eval-runs"},
		{http.MethodPost, "/api/quality/eval-runs"},
		{http.MethodGet, "/api/quality/eval-runs/1"},
		{http.MethodGet, "/api/agents/profiles"},
		{http.MethodGet, "/api/agents/profiles/clarification_host"},
		{http.MethodPatch, "/api/agents/profiles/clarification_host"},
		{http.MethodPut, "/api/agents/profiles/clarification_host/prompt"},
		{http.MethodPost, "/api/agents/profiles/clarification_host/prompt/reset"},
		{http.MethodPost, "/api/agents/profiles/clarification_host/prompt/restore"},
		{http.MethodGet, "/api/agents/profiles/clarification_host/prompt/revisions"},
		{http.MethodPost, "/api/agents/profiles/clarification_host/prompt/preview"},
		{http.MethodGet, "/api/agents/tools"},
	}
	for _, tc := range admin {
		if status := serve(mux, tc.method, tc.path); status != http.StatusForbidden {
			t.Fatalf("%s %s status = %d, want 403 (admin route)", tc.method, tc.path, status)
		}
	}
}
