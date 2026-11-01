package agentapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentpkg "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentapi "github.com/EdouardRichard/EzTest/backend/internal/agent/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
)

/* ------------------------------- stubs ---------------------------------- */

type stubAgentCenterService struct {
	catalog    agentapp.Catalog
	runs       []agentapp.RunView
	hasMore    bool
	quality    agentapp.QualityView
	err        error
	lastFilter agentapp.RunFilter
	runCalls   int
	catCalls   int
	qualCalls  int
}

func (s *stubAgentCenterService) Catalog(_ context.Context) (agentapp.Catalog, error) {
	s.catCalls++
	return s.catalog, s.err
}

func (s *stubAgentCenterService) Runs(_ context.Context, filter agentapp.RunFilter) ([]agentapp.RunView, bool, error) {
	s.runCalls++
	s.lastFilter = filter
	return s.runs, s.hasMore, s.err
}

func (s *stubAgentCenterService) Quality(_ context.Context) (agentapp.QualityView, error) {
	s.qualCalls++
	return s.quality, s.err
}

var _ agentapi.AgentCenterService = (*stubAgentCenterService)(nil)

/* ------------------------------- helpers -------------------------------- */

func agentCenterAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Anon") != "" {
			httpx.Error(w, r, platform.Unauthenticated())
			return
		}
		actor := platform.Actor{ID: 42, Role: r.Header.Get("X-Test-Role")}
		next.ServeHTTP(w, r.WithContext(platform.WithActor(r.Context(), actor)))
	})
}

func newAgentCenterServer(t *testing.T, service *stubAgentCenterService) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	agentapi.RegisterAgentCenterRoutes(mux, agentCenterAuth, agentapi.NewAgentCenterHandler(service))
	return mux
}

func agentCenterRequest(t *testing.T, handler http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeAgentCenterBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body %q: %v", recorder.Body.String(), err)
	}
	return payload
}

func agentCenterCatalogFixture() agentapp.Catalog {
	promptUpdated := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 11, 1, 9, 5, 0, 0, time.UTC)
	model := "deepseek-flash"
	return agentapp.Catalog{
		Profiles: []agentapp.CatalogProfile{{
			ProfileKey:        agentapp.ProfileKeyClarificationHost,
			Name:              "澄清 Host",
			Team:              "clarification",
			Order:             1,
			Mode:              "agent",
			Model:             &model,
			Enabled:           true,
			Budgets:           agentProfilesBudgets(),
			AllowedToolsCount: 9,
			PromptVersion:     3,
			PromptUpdatedAt:   &promptUpdated,
			UpdatedAt:         updated,
			Stats: agentapp.RunStats{
				RunCount: 3, DoneCount: 2, FailedCount: 1,
			},
		}},
		Teams: []agentapp.AgentTeam{
			{Team: "clarification", Label: "澄清", ProfileKeys: []string{agentapp.ProfileKeyClarificationHost}},
			{Team: "code", Label: "代码", ProfileKeys: []string{}, Note: "确定性 worker，无 agent profile"},
		},
	}
}

func TestAgentCenterCatalogEndpoint(t *testing.T) {
	service := &stubAgentCenterService{catalog: agentCenterCatalogFixture()}
	handler := newAgentCenterServer(t, service)

	recorder := agentCenterRequest(t, handler, "/api/agent-center/catalog", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	payload := decodeAgentCenterBody(t, recorder)
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("data = %#v", payload["data"])
	}
	profiles, ok := data["profiles"].([]any)
	if !ok || len(profiles) != 1 {
		t.Fatalf("profiles = %#v", data["profiles"])
	}
	profile := profiles[0].(map[string]any)
	if profile["profile_key"] != agentapp.ProfileKeyClarificationHost {
		t.Fatalf("profile = %#v", profile)
	}
	if profile["prompt_version"].(float64) != 3 {
		t.Fatalf("prompt_version = %#v", profile["prompt_version"])
	}
	if profile["allowed_tools_count"].(float64) != 9 {
		t.Fatalf("allowed_tools_count = %#v", profile["allowed_tools_count"])
	}
	stats := profile["stats"].(map[string]any)
	if stats["success_rate"].(float64) != 2.0/3.0 {
		t.Fatalf("success_rate = %#v", stats["success_rate"])
	}
	if stats["last_run_at"] != nil {
		t.Fatalf("last_run_at = %#v, want null", stats["last_run_at"])
	}
	if _, ok := data["teams"].([]any); !ok {
		t.Fatalf("teams = %#v", data["teams"])
	}
	body := strings.ToLower(recorder.Body.String())
	for _, forbidden := range []string{"system_prompt", "transcript"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("catalog response leaks %q: %s", forbidden, body)
		}
	}
}

func TestAgentCenterRunsEndpointParsesFilters(t *testing.T) {
	versionID := int64(7)
	started := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	service := &stubAgentCenterService{
		runs: []agentapp.RunView{{
			ID: 31, ProfileKey: agentapp.ProfileKeyCaseExecutor,
			VersionID: &versionID, VersionTitle: "订单系统 Q4",
			Status: "done", Turns: 4, Usage: map[string]int{"input_tokens": 10},
			StartedAt: &started,
		}},
		hasMore: true,
	}
	handler := newAgentCenterServer(t, service)

	recorder := agentCenterRequest(t, handler,
		"/api/agent-center/runs?profile_key=case_executor&status=done&version_id=7&before=99&limit=5", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	payload := decodeAgentCenterBody(t, recorder)
	if payload["has_more"] != true || payload["next_cursor"] != "31" {
		t.Fatalf("envelope = %#v", payload)
	}
	runs := payload["data"].([]any)
	run := runs[0].(map[string]any)
	if run["id"] != "31" || run["profile_key"] != "case_executor" {
		t.Fatalf("run = %#v", run)
	}
	if run["work_item_id"] != nil || run["version_id"] != "7" {
		t.Fatalf("nullable ids = %#v", run)
	}
	if run["version_title"] != "订单系统 Q4" || run["status"] != "done" {
		t.Fatalf("run fields = %#v", run)
	}
	if run["started_at"] != "2026-11-01T09:00:00Z" {
		t.Fatalf("started_at = %#v", run["started_at"])
	}
	if _, ok := run["messages"]; ok {
		t.Fatal("runs response exposes messages")
	}

	filter := service.lastFilter
	if filter.ProfileKey != "case_executor" || filter.Status != "done" {
		t.Fatalf("filter = %#v", filter)
	}
	if filter.VersionID == nil || *filter.VersionID != 7 {
		t.Fatalf("version filter = %#v", filter.VersionID)
	}
	if filter.BeforeID == nil || *filter.BeforeID != 99 {
		t.Fatalf("before = %#v", filter.BeforeID)
	}
	if filter.Limit != 5 {
		t.Fatalf("limit = %d", filter.Limit)
	}
}

func TestAgentCenterRunsEndpointRejectsInvalidFilters(t *testing.T) {
	cases := []struct{ name, query, field string }{
		{name: "status", query: "?status=exploded", field: "status"},
		{name: "profile key", query: "?profile_key=NOT_A_KEY", field: "profile_key"},
		{name: "limit", query: "?limit=101", field: "limit"},
		{name: "limit zero", query: "?limit=0", field: "limit"},
		{name: "version", query: "?version_id=abc", field: "version_id"},
		{name: "before", query: "?before=-1", field: "before"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := &stubAgentCenterService{}
			handler := newAgentCenterServer(t, service)
			recorder := agentCenterRequest(t, handler, "/api/agent-center/runs"+tc.query, nil)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			payload := decodeAgentCenterBody(t, recorder)
			details := payload["error"].(map[string]any)["details"].(map[string]any)
			if details["field"] != tc.field {
				t.Fatalf("details = %#v", details)
			}
			if service.runCalls != 0 {
				t.Fatalf("service calls = %d", service.runCalls)
			}
		})
	}
}

func TestAgentCenterQualityEndpoint(t *testing.T) {
	created := time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC)
	service := &stubAgentCenterService{quality: agentapp.QualityView{
		Latest: &agentapp.EvalSummary{
			ID: 9, Mode: "real_llm", Status: "failed", CreatedAt: created,
			MetricsCount: 7, MetricsPassed: 6,
		},
		RunStats: agentapp.RunStats{RunCount: 2, DoneCount: 1, FailedCount: 1},
	}}
	handler := newAgentCenterServer(t, service)

	recorder := agentCenterRequest(t, handler, "/api/agent-center/quality", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	payload := decodeAgentCenterBody(t, recorder)
	data := payload["data"].(map[string]any)
	latest := data["latest"].(map[string]any)
	if latest["id"] != "9" || latest["metrics_count"].(float64) != 7 || latest["metrics_passed"].(float64) != 6 {
		t.Fatalf("latest = %#v", latest)
	}
	stats := data["run_stats"].(map[string]any)
	if stats["run_count"].(float64) != 2 || stats["success_rate"].(float64) != 0.5 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestAgentCenterQualityEndpointNullLatest(t *testing.T) {
	service := &stubAgentCenterService{}
	handler := newAgentCenterServer(t, service)
	recorder := agentCenterRequest(t, handler, "/api/agent-center/quality", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	data := decodeAgentCenterBody(t, recorder)["data"].(map[string]any)
	if data["latest"] != nil {
		t.Fatalf("latest = %#v, want null", data["latest"])
	}
}

func TestAgentCenterEndpointsRequireAuthentication(t *testing.T) {
	service := &stubAgentCenterService{}
	handler := newAgentCenterServer(t, service)
	for _, path := range []string{
		"/api/agent-center/catalog",
		"/api/agent-center/runs",
		"/api/agent-center/quality",
	} {
		recorder := agentCenterRequest(t, handler, path, map[string]string{"X-Test-Anon": "1"})
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d", path, recorder.Code)
		}
	}
}

func TestAgentCenterServiceErrorIsMapped(t *testing.T) {
	service := &stubAgentCenterService{err: platform.Unavailable("reader down")}
	handler := newAgentCenterServer(t, service)
	recorder := agentCenterRequest(t, handler, "/api/agent-center/catalog", nil)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestRegisterAgentCenterRoutesNilHandlerIsSafe(t *testing.T) {
	mux := http.NewServeMux()
	agentapi.RegisterAgentCenterRoutes(mux, agentCenterAuth, nil)
	agentapi.RegisterAgentCenterRoutes(nil, agentCenterAuth, agentapi.NewAgentCenterHandler(&stubAgentCenterService{}))
}

func TestAgentCenterNilServiceIsUnavailable(t *testing.T) {
	handler := agentapi.NewAgentCenterHandler(nil)
	mux := http.NewServeMux()
	agentapi.RegisterAgentCenterRoutes(mux, agentCenterAuth, handler)
	recorder := agentCenterRequest(t, mux, "/api/agent-center/catalog", nil)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestAgentCenterUnknownErrorDoesNotLeak(t *testing.T) {
	service := &stubAgentCenterService{err: errors.New("secret db detail")}
	handler := newAgentCenterServer(t, service)
	recorder := agentCenterRequest(t, handler, "/api/agent-center/quality", nil)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "secret db detail") {
		t.Fatalf("body leaks internal detail: %s", recorder.Body.String())
	}
}

func agentProfilesBudgets() agentpkg.ProfileBudgets {
	return agentpkg.ProfileBudgets{MaxTurns: 12, SoftSeconds: 300, HardSeconds: 600}
}
