package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/config"
	eventinginfra "github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
)

// stubAgentProfiles is a minimal agenthttp.ProfileService.
type stubAgentProfiles struct{}

func (stubAgentProfiles) List(context.Context) ([]agentdomain.AgentProfile, error) {
	return nil, nil
}

func (stubAgentProfiles) Get(context.Context, int64) (agentdomain.AgentProfile, error) {
	return agentdomain.AgentProfile{}, agentdomain.ErrNotFound
}

func (stubAgentProfiles) Update(context.Context, int64, agentdomain.UpdateParams, *int64) (agentdomain.AgentProfile, error) {
	return agentdomain.AgentProfile{}, agentdomain.ErrNotFound
}

// stubAgentSessions is a minimal agenthttp.SessionService.
type stubAgentSessions struct{}

func (stubAgentSessions) List(context.Context, int64, int, *int64) ([]agentdomain.SessionSummary, bool, error) {
	return nil, false, nil
}

func (stubAgentSessions) Create(context.Context, int64, string, *int64) (agentdomain.Session, error) {
	return agentdomain.Session{}, agentdomain.ErrNotFound
}

func (stubAgentSessions) Get(context.Context, int64) (agentdomain.SessionDetail, error) {
	return agentdomain.SessionDetail{}, agentdomain.ErrNotFound
}

func (stubAgentSessions) UpdateTitle(context.Context, int64, string) (agentdomain.Session, error) {
	return agentdomain.Session{}, agentdomain.ErrNotFound
}

func (stubAgentSessions) Delete(context.Context, int64, *int64) error {
	return agentdomain.ErrNotFound
}

func (stubAgentSessions) Message(context.Context, int64, int64) (agentdomain.Message, error) {
	return agentdomain.Message{}, agentdomain.ErrNotFound
}

// stubAgentRuns is a minimal agenthttp.RunControl.
type stubAgentRuns struct{}

func (stubAgentRuns) Start(context.Context, int64, string, *int64) (agentdomain.StartResult, error) {
	return agentdomain.StartResult{}, agentdomain.ErrNotFound
}

func (stubAgentRuns) Abort(context.Context, int64, *int64) (agentdomain.AbortResult, error) {
	return agentdomain.AbortResult{}, agentdomain.ErrNotFound
}

// stubAgentTools is a minimal agenthttp.ToolCatalog.
type stubAgentTools struct{}

func (stubAgentTools) Infos() []agentdomain.ToolInfo {
	return []agentdomain.ToolInfo{{Name: "test.echo", Description: "Echo text"}}
}

// agentTestDeps wires the fake Agent services and the admin/user bearer tokens.
func agentTestDeps() (Dependencies, identity.User, identity.User) {
	admin := identity.User{ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive}
	regular := identity.User{ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive}
	return Dependencies{
		Auth: tokenMapAuth{"ezt_admin": admin, "ezt_user": regular},
		Agent: &AgentDependencies{
			Profiles: stubAgentProfiles{},
			Sessions: stubAgentSessions{},
			Runs:     stubAgentRuns{},
			Tools:    stubAgentTools{},
		},
	}, admin, regular
}

// agentRequest builds a request with an optional bearer token.
func agentRequest(method, path, token string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(`{"message":"hi"}`))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

// TestAgentRoutesRequireAdmin proves every /api/agents route is mounted behind
// the global bearer auth and the admin guard: admins reach the handlers, regular
// users get the shared 403 envelope and anonymous callers get 401.
func TestAgentRoutesRequireAdmin(t *testing.T) {
	deps, _, _ := agentTestDeps()
	h := New(config.Config{Env: "test"}, testLogger(), deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, agentRequest(http.MethodGet, "/api/agents/profiles", "ezt_admin"))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin GET profiles=%d body=%s want 200", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Fatalf("admin list body=%s want an empty data array", rec.Body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, agentRequest(http.MethodGet, "/api/agents/tools", "ezt_admin"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "test.echo") {
		t.Fatalf("admin GET tools=%d body=%s", rec.Code, rec.Body)
	}

	// Every route, not just the readings, carries the admin guard.
	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/agents/profiles"},
		{http.MethodGet, "/api/agents/profiles/1"},
		{http.MethodPatch, "/api/agents/profiles/1"},
		{http.MethodGet, "/api/agents/tools"},
		{http.MethodGet, "/api/agents/sessions"},
		{http.MethodPost, "/api/agents/sessions"},
		{http.MethodGet, "/api/agents/sessions/1"},
		{http.MethodPatch, "/api/agents/sessions/1"},
		{http.MethodDelete, "/api/agents/sessions/1"},
		{http.MethodPost, "/api/agents/sessions/1/messages"},
		{http.MethodPost, "/api/agents/sessions/1/stop"},
		{http.MethodGet, "/api/agents/sessions/1/messages/2"},
	}
	for _, route := range routes {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, agentRequest(route.method, route.path, "ezt_user"))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s as user=%d body=%s want 403", route.method, route.path, rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), `"code":"forbidden"`) {
			t.Fatalf("%s %s body=%s want the forbidden envelope", route.method, route.path, rec.Body)
		}

		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, agentRequest(route.method, route.path, ""))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s anonymous=%d body=%s want 401", route.method, route.path, rec.Code, rec.Body)
		}
	}
}

// TestAgentRoutesAbsentWithoutDeps proves the Agent routes are not mounted when
// the composition root did not wire the Agent services.
func TestAgentRoutesAbsentWithoutDeps(t *testing.T) {
	admin := identity.User{ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive}
	h := New(config.Config{Env: "test"}, testLogger(), Dependencies{
		Auth: tokenMapAuth{"ezt_admin": admin},
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, agentRequest(http.MethodGet, "/api/agents/profiles", "ezt_admin"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET agents/profiles without deps=%d body=%s want 404", rec.Code, rec.Body)
	}
}

// TestAgentChannelGuard proves the /api/stream and /api/stream/events handlers
// only expose `agent:` channels to admins, while the P0 `system` channel keeps
// its previous behaviour for every authenticated user.
func TestAgentChannelGuard(t *testing.T) {
	deps, _, _ := agentTestDeps()
	deps.Events = stubEventService{}
	deps.EventReader = stubEventReader{}
	deps.EventHub = eventinginfra.NewHub()
	h := New(config.Config{Env: "test"}, testLogger(), deps)

	// Snapshot: a regular user may read the system channel but not an agent one.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, agentRequest(http.MethodGet, "/api/stream/events?channel=system", "ezt_user"))
	if rec.Code != http.StatusOK {
		t.Fatalf("user system snapshot=%d body=%s want 200", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, agentRequest(http.MethodGet, "/api/stream/events?channel=agent:1", "ezt_user"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("user agent snapshot=%d body=%s want 403", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"code":"forbidden"`) {
		t.Fatalf("user agent snapshot body=%s want the forbidden envelope", rec.Body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, agentRequest(http.MethodGet, "/api/stream/events?channel=agent:1", "ezt_admin"))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin agent snapshot=%d body=%s want 200", rec.Code, rec.Body)
	}

	// SSE: the guard rejects before the stream opens.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, agentRequest(http.MethodGet, "/api/stream?channel=agent:1", "ezt_user"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("user agent stream=%d body=%s want 403", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, agentRequest(http.MethodGet, "/api/stream?channel=agent:1", ""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous agent stream=%d body=%s want 401", rec.Code, rec.Body)
	}

	// An admin can open both the agent and the system stream.
	srv := httptest.NewServer(h)
	defer srv.Close()
	for _, path := range []string{"/api/stream?channel=agent:1", "/api/stream?channel=system"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer ezt_admin")
		resp, err := srv.Client().Do(req)
		if err != nil {
			cancel()
			t.Fatalf("open %s: %v", path, err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			cancel()
			t.Fatalf("admin %s=%d want 200", path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
			resp.Body.Close()
			cancel()
			t.Fatalf("%s Content-Type=%q want text/event-stream", path, ct)
		}
		resp.Body.Close()
		cancel()
	}
}
