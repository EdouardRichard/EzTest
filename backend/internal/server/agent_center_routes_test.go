package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/config"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
)

// stubAgentCenter is a minimal agenthttp.CenterReader for the route tests.
type stubAgentCenter struct{}

// Catalog returns the 13-role directory with no stored profiles (the route test
// only asserts readability, not the mapping).
func (stubAgentCenter) Catalog(context.Context) (agentapp.CenterCatalog, error) {
	roles := make([]agentapp.CenterRoleView, 0, 13)
	for _, role := range agentapp.CenterRoles() {
		view := agentapp.CenterRoleView{
			Order: role.Order, Role: role.Role, Name: role.Name, Phase: role.Phase,
			Mode: role.Mode, Note: role.Note, AllowedTools: []string{},
		}
		if role.ProfileKey != "" {
			key := role.ProfileKey
			view.ProfileKey = &key
		}
		roles = append(roles, view)
	}
	return agentapp.CenterCatalog{Roles: roles}, nil
}

// Sessions returns an empty read-only page.
func (stubAgentCenter) Sessions(context.Context, agentapp.CenterSessionQuery) (agentapp.CenterSessionPage, error) {
	return agentapp.CenterSessionPage{Items: []agentapp.CenterSessionItem{}}, nil
}

// Quality returns the pre-Task-23 null latest metrics.
func (stubAgentCenter) Quality(context.Context) (agentapp.CenterQuality, error) {
	return agentapp.CenterQuality{}, nil
}

// centerTestDeps wires the Agent services with a center reader and the
// admin/user bearer tokens.
func centerTestDeps() Dependencies {
	admin := identity.User{ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive}
	regular := identity.User{ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive}
	return Dependencies{
		Auth: tokenMapAuth{"ezt_admin": admin, "ezt_user": regular},
		Agent: &AgentDependencies{
			Profiles: stubAgentProfiles{},
			Sessions: stubAgentSessions{},
			Runs:     stubAgentRuns{},
			Tools:    stubAgentTools{},
			Center:   stubAgentCenter{},
		},
	}
}

// TestAgentCenterRoutesReadableByRegularUsers proves the read-only center is
// mounted outside the admin guard: a regular user gets 200 on every read, while
// anonymous callers still get 401 and the profile write stays admin-only.
func TestAgentCenterRoutesReadableByRegularUsers(t *testing.T) {
	h := New(config.Config{Env: "test"}, testLogger(), centerTestDeps())

	for _, path := range []string{
		"/api/agent-center/catalog",
		"/api/agent-center/sessions",
		"/api/agent-center/quality",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, agentRequest(http.MethodGet, path, "ezt_user"))
		if rec.Code != http.StatusOK {
			t.Fatalf("regular user GET %s = %d body=%s, want 200", path, rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), `"data"`) {
			t.Fatalf("GET %s body = %s, want the data envelope", path, rec.Body)
		}

		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, agentRequest(http.MethodGet, path, ""))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous GET %s = %d body=%s, want 401", path, rec.Code, rec.Body)
		}
	}

	// The configuration write keeps the P1 admin-only rule (D60).
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, agentRequest(http.MethodPatch, "/api/agents/profiles/1", "ezt_user"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("regular user PATCH profile = %d body=%s, want 403", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	adminPatch := httptest.NewRequest(http.MethodPatch, "/api/agents/profiles/1", strings.NewReader(`{"enabled":true}`))
	adminPatch.Header.Set("Authorization", "Bearer ezt_admin")
	h.ServeHTTP(rec, adminPatch)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("admin PATCH profile = %d body=%s, want the stub's 404 (guard passed)", rec.Code, rec.Body)
	}
}

// TestAgentCenterCatalogListsAllRolesForRegularUsers replays the catalog as a
// regular user and pins the 13-row contract at the HTTP boundary.
func TestAgentCenterCatalogListsAllRolesForRegularUsers(t *testing.T) {
	h := New(config.Config{Env: "test"}, testLogger(), centerTestDeps())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, agentRequest(http.MethodGet, "/api/agent-center/catalog", "ezt_user"))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET catalog = %d body=%s, want 200", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if got := strings.Count(body, `"role":`); got != 13 {
		t.Errorf("catalog role count = %d body=%s, want 13", got, body)
	}
	if strings.Contains(body, "system_prompt") {
		t.Errorf("catalog leaks system_prompt: %s", body)
	}
}

// TestAgentCenterRoutesAbsentWithoutReader proves the center routes are not
// mounted when the composition root did not wire the read model.
func TestAgentCenterRoutesAbsentWithoutReader(t *testing.T) {
	admin := identity.User{ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive}
	deps := Dependencies{
		Auth: tokenMapAuth{"ezt_admin": admin},
		Agent: &AgentDependencies{
			Profiles: stubAgentProfiles{},
			Sessions: stubAgentSessions{},
			Runs:     stubAgentRuns{},
			Tools:    stubAgentTools{},
		},
	}
	h := New(config.Config{Env: "test"}, testLogger(), deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, agentRequest(http.MethodGet, "/api/agent-center/catalog", "ezt_admin"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("catalog without center reader = %d body=%s, want 404", rec.Code, rec.Body)
	}
}

// TestAgentCenterCatalogRoleKeysMatchProfiles pins the cross-context profile
// keys the read model resolves at runtime.
func TestAgentCenterCatalogRoleKeysMatchProfiles(t *testing.T) {
	keys := map[string]bool{}
	for _, role := range agentapp.CenterRoles() {
		if role.ProfileKey != "" {
			keys[role.ProfileKey] = true
		}
	}
	for _, key := range []string{
		"clarification-dialog", "requirement-analysis", "context-recon",
		"app-exploration", "case-design", "case-review", "case-data-plan",
		"execution-agent", "supplement-agent", "failure-analysis-agent",
		"issue-triage-agent",
	} {
		if !keys[key] {
			t.Errorf("catalog is missing profile key %q", key)
		}
	}
	if len(keys) != 11 {
		t.Errorf("profile role count = %d, want 11", len(keys))
	}
}
