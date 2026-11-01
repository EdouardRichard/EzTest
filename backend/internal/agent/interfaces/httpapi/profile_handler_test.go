package agentapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentapi "github.com/EdouardRichard/EzTest/backend/internal/agent/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
)

/* ------------------------------- stubs ---------------------------------- */

type stubProfileService struct {
	list      []agentapp.ProfileView
	detail    agentapp.ProfileDetail
	view      agentapp.ProfileView
	version   int
	changed   bool
	revisions []agentapp.PromptRevision
	hasMore   bool
	rendered  string
	tools     []agentapp.AgentToolView
	err       error

	listCalls   int
	getCalls    int
	patchCalls  int
	saveCalls   int
	resetCalls  int
	restoreCalls int
	revCalls    int
	previewCalls int
	toolsCalls  int

	lastActor    platform.Actor
	lastKey      string
	lastPatch    agentapp.PatchInput
	lastSave     agentapp.SavePromptInput
	lastVersion  int
	lastLimit    int
	lastBefore   *int
	lastTemplate string
	lastSample   map[string]string
}

func (s *stubProfileService) List(context.Context) ([]agentapp.ProfileView, error) {
	s.listCalls++
	return s.list, s.err
}

func (s *stubProfileService) Get(_ context.Context, key string) (agentapp.ProfileDetail, error) {
	s.getCalls++
	s.lastKey = key
	return s.detail, s.err
}

func (s *stubProfileService) Patch(_ context.Context, actor platform.Actor, key string, input agentapp.PatchInput) (agentapp.ProfileView, bool, error) {
	s.patchCalls++
	s.lastActor = actor
	s.lastKey = key
	s.lastPatch = input
	return s.view, s.changed, s.err
}

func (s *stubProfileService) SavePrompt(_ context.Context, actor platform.Actor, key string, input agentapp.SavePromptInput) (agentapp.ProfileView, int, bool, error) {
	s.saveCalls++
	s.lastActor = actor
	s.lastKey = key
	s.lastSave = input
	return s.view, s.version, s.changed, s.err
}

func (s *stubProfileService) ResetPrompt(_ context.Context, actor platform.Actor, key string) (agentapp.ProfileView, int, bool, error) {
	s.resetCalls++
	s.lastActor = actor
	s.lastKey = key
	return s.view, s.version, s.changed, s.err
}

func (s *stubProfileService) RestorePrompt(_ context.Context, actor platform.Actor, key string, version int) (agentapp.ProfileView, int, bool, error) {
	s.restoreCalls++
	s.lastActor = actor
	s.lastKey = key
	s.lastVersion = version
	return s.view, s.version, s.changed, s.err
}

func (s *stubProfileService) Revisions(_ context.Context, key string, limit int, beforeVersion *int) ([]agentapp.PromptRevision, bool, error) {
	s.revCalls++
	s.lastKey = key
	s.lastLimit = limit
	s.lastBefore = beforeVersion
	return s.revisions, s.hasMore, s.err
}

func (s *stubProfileService) Preview(_ context.Context, key, tmpl string, sample map[string]string) (string, error) {
	s.previewCalls++
	s.lastKey = key
	s.lastTemplate = tmpl
	s.lastSample = sample
	return s.rendered, s.err
}

func (s *stubProfileService) Tools() ([]agentapp.AgentToolView, error) {
	s.toolsCalls++
	return s.tools, s.err
}

var _ agentapi.ProfileService = (*stubProfileService)(nil)

/* ------------------------------- helpers -------------------------------- */

func profileAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Anon") != "" {
			httpx.Error(w, r, platform.Unauthenticated())
			return
		}
		role := r.Header.Get("X-Test-Role")
		if role == "" {
			role = "admin"
		}
		actor := platform.Actor{ID: 42, Role: role}
		next.ServeHTTP(w, r.WithContext(platform.WithActor(r.Context(), actor)))
	})
}

func newProfileServer(t *testing.T, service *stubProfileService) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	agentapi.RegisterProfileRoutes(mux, profileAuth, agentapi.NewProfileHandler(service))
	return mux
}

func profileRequest(t *testing.T, handler http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func profileViewFixture() agentapp.ProfileView {
	model := "deepseek-flash"
	created := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	return agentapp.ProfileView{
		ID: 101, Key: agentapp.ProfileKeyClarificationHost, Name: "澄清 Host",
		SystemPrompt: "[profile:clarification_host] prompt", AllowedTools: []string{"requirement.read"},
		Model: &model, Budgets: agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 300, HardSeconds: 600},
		Enabled: true, PromptVersion: 3, CreatedAt: created, UpdatedAt: created,
	}
}

func profileMutationFixture() (agentapp.ProfileView, int, bool) {
	return profileViewFixture(), 4, true
}

/* -------------------------------- list/get ------------------------------ */

func TestListAgentProfilesEndpoint(t *testing.T) {
	service := &stubProfileService{list: []agentapp.ProfileView{profileViewFixture()}}
	handler := newProfileServer(t, service)
	recorder := profileRequest(t, handler, http.MethodGet, "/api/agents/profiles", "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	payload := decodeAgentCenterBody(t, recorder)
	data := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("data = %#v", data)
	}
	view := data[0].(map[string]any)
	if view["id"] != "101" || view["key"] != agentapp.ProfileKeyClarificationHost {
		t.Fatalf("view = %#v", view)
	}
	if view["system_prompt"] != "[profile:clarification_host] prompt" {
		t.Fatalf("prompt = %#v", view["system_prompt"])
	}
	if view["model"] != "deepseek-flash" {
		t.Fatalf("model = %#v", view["model"])
	}
	if view["prompt_version"].(float64) != 3 {
		t.Fatalf("prompt version = %#v", view["prompt_version"])
	}
	tools := view["allowed_tools"].([]any)
	if len(tools) != 1 || tools[0] != "requirement.read" {
		t.Fatalf("allowed tools = %#v", tools)
	}
}

func TestGetAgentProfileEndpoint(t *testing.T) {
	detail := agentapp.ProfileDetail{
		Profile:             profileViewFixture(),
		DefaultSystemPrompt: "[profile:clarification_host] default",
		Variables: []agentapp.PromptVariable{{
			Name: "Now", Description: "当前时间", Example: "2026-11-01T12:00:00Z", Source: "runtime",
		}},
		PromptVersion: 3,
	}
	service := &stubProfileService{detail: detail}
	handler := newProfileServer(t, service)
	recorder := profileRequest(t, handler, http.MethodGet, "/api/agents/profiles/clarification_host", "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	data := decodeAgentCenterBody(t, recorder)["data"].(map[string]any)
	if data["default_system_prompt"] != "[profile:clarification_host] default" {
		t.Fatalf("default = %#v", data["default_system_prompt"])
	}
	if data["prompt_version"].(float64) != 3 {
		t.Fatalf("version = %#v", data["prompt_version"])
	}
	variables := data["variables"].([]any)
	variable := variables[0].(map[string]any)
	if variable["name"] != "Now" || variable["source"] != "runtime" || variable["example"] == "" {
		t.Fatalf("variable = %#v", variable)
	}
	profile := data["profile"].(map[string]any)
	if profile["key"] != agentapp.ProfileKeyClarificationHost {
		t.Fatalf("profile = %#v", profile)
	}
}

func TestProfileEndpointsRejectInvalidKeyAndMissingAuth(t *testing.T) {
	service := &stubProfileService{}
	handler := newProfileServer(t, service)

	recorder := profileRequest(t, handler, http.MethodGet, "/api/agents/profiles/Bad%20Key", "", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid key status = %d", recorder.Code)
	}
	if service.getCalls != 0 {
		t.Fatalf("service calls = %d", service.getCalls)
	}

	recorder = profileRequest(t, handler, http.MethodGet, "/api/agents/profiles", "", map[string]string{"X-Test-Anon": "1"})
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", recorder.Code)
	}

	recorder = profileRequest(t, handler, http.MethodGet, "/api/agents/profiles", "", map[string]string{"X-Test-Role": "user"})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

/* --------------------------------- patch -------------------------------- */

func TestPatchAgentProfileEndpoint(t *testing.T) {
	service := &stubProfileService{view: profileViewFixture(), changed: true}
	handler := newProfileServer(t, service)
	body := `{"model":null,"budgets":{"max_turns":9,"soft_seconds":100,"hard_seconds":200},"allowed_tools":["requirement.read","requirement.read"],"enabled":false,"name":"ignored"}`
	recorder := profileRequest(t, handler, http.MethodPatch, "/api/agents/profiles/clarification_host", body, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	payload := decodeAgentCenterBody(t, recorder)
	if payload["changed"] != true {
		t.Fatalf("changed = %#v", payload["changed"])
	}
	data := payload["data"].(map[string]any)
	if data["id"] != "101" {
		t.Fatalf("data = %#v", data)
	}
	input := service.lastPatch
	if !input.SetModel || input.Model != nil {
		t.Fatalf("model input = %#v", input)
	}
	if input.Budgets == nil || input.Budgets.MaxTurns != 9 {
		t.Fatalf("budgets = %#v", input.Budgets)
	}
	if input.AllowedTools == nil || len(*input.AllowedTools) != 2 {
		t.Fatalf("allowed tools = %#v", input.AllowedTools)
	}
	if service.lastActor.ID != 42 || !service.lastActor.IsAdmin() {
		t.Fatalf("actor = %#v", service.lastActor)
	}
}

func TestPatchAgentProfileAbsentModel(t *testing.T) {
	service := &stubProfileService{view: profileViewFixture()}
	handler := newProfileServer(t, service)
	body := `{"budgets":{"max_turns":3,"soft_seconds":0,"hard_seconds":0}}`
	recorder := profileRequest(t, handler, http.MethodPatch, "/api/agents/profiles/clarification_host", body, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if service.lastPatch.SetModel {
		t.Fatal("absent model was treated as explicit")
	}
	if service.lastPatch.Budgets == nil || service.lastPatch.Budgets.MaxTurns != 3 {
		t.Fatalf("budgets = %#v", service.lastPatch.Budgets)
	}
}

func TestPatchAgentProfileRejectsNonStringModel(t *testing.T) {
	service := &stubProfileService{}
	handler := newProfileServer(t, service)
	recorder := profileRequest(t, handler, http.MethodPatch, "/api/agents/profiles/clarification_host", `{"model":123}`, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	details := decodeAgentCenterBody(t, recorder)["error"].(map[string]any)["details"].(map[string]any)
	if details["field"] != "model" || service.patchCalls != 0 {
		t.Fatalf("details = %#v calls = %d", details, service.patchCalls)
	}
}

/* --------------------------------- prompts ------------------------------ */

func TestSaveAgentPromptEndpoint(t *testing.T) {
	view, version, changed := profileMutationFixture()
	service := &stubProfileService{view: view, version: version, changed: changed}
	handler := newProfileServer(t, service)
	body := `{"system_prompt":"[profile:clarification_host] {{.VersionTitle}}","note":"manual","expected_version":3}`
	recorder := profileRequest(t, handler, http.MethodPut, "/api/agents/profiles/clarification_host/prompt", body, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	data := decodeAgentCenterBody(t, recorder)["data"].(map[string]any)
	if data["version"].(float64) != 4 || data["changed"] != true {
		t.Fatalf("data = %#v", data)
	}
	if data["profile"].(map[string]any)["key"] != agentapp.ProfileKeyClarificationHost {
		t.Fatalf("profile = %#v", data["profile"])
	}
	input := service.lastSave
	if input.SystemPrompt != "[profile:clarification_host] {{.VersionTitle}}" || input.Note != "manual" {
		t.Fatalf("input = %#v", input)
	}
	if input.ExpectedVersion == nil || *input.ExpectedVersion != 3 {
		t.Fatalf("expected version = %v", input.ExpectedVersion)
	}
}

func TestSaveAgentPromptRejectsMissingBody(t *testing.T) {
	service := &stubProfileService{}
	handler := newProfileServer(t, service)
	for _, body := range []string{`{}`, `{"system_prompt":""}`, `not-json`} {
		recorder := profileRequest(t, handler, http.MethodPut, "/api/agents/profiles/clarification_host/prompt", body, nil)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %q status = %d", body, recorder.Code)
		}
	}
	if service.saveCalls != 0 {
		t.Fatalf("service calls = %d", service.saveCalls)
	}
}

func TestResetAgentPromptEndpoint(t *testing.T) {
	view, version, changed := profileMutationFixture()
	service := &stubProfileService{view: view, version: version, changed: changed}
	handler := newProfileServer(t, service)
	recorder := profileRequest(t, handler, http.MethodPost, "/api/agents/profiles/clarification_host/prompt/reset", "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	data := decodeAgentCenterBody(t, recorder)["data"].(map[string]any)
	if data["version"].(float64) != 4 {
		t.Fatalf("data = %#v", data)
	}
	if service.resetCalls != 1 || service.lastKey != agentapp.ProfileKeyClarificationHost {
		t.Fatalf("calls=%d key=%q", service.resetCalls, service.lastKey)
	}
}

func TestRestoreAgentPromptEndpoint(t *testing.T) {
	view, version, changed := profileMutationFixture()
	service := &stubProfileService{view: view, version: version, changed: changed}
	handler := newProfileServer(t, service)
	recorder := profileRequest(t, handler, http.MethodPost, "/api/agents/profiles/clarification_host/prompt/restore", `{"version":2}`, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if service.restoreCalls != 1 || service.lastVersion != 2 {
		t.Fatalf("calls=%d version=%d", service.restoreCalls, service.lastVersion)
	}

	for _, body := range []string{`{}`, `{"version":0}`, `{"version":"2"}`} {
		recorder := profileRequest(t, handler, http.MethodPost, "/api/agents/profiles/clarification_host/prompt/restore", body, nil)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %q status = %d", body, recorder.Code)
		}
	}
	if service.restoreCalls != 1 {
		t.Fatalf("invalid bodies reached the service: %d", service.restoreCalls)
	}
}

func TestPromptRevisionsEndpoint(t *testing.T) {
	actorID := int64(7)
	service := &stubProfileService{
		revisions: []agentapp.PromptRevision{
			{ID: 1, ProfileKey: agentapp.ProfileKeyClarificationHost, Version: 3, SystemPrompt: "v3", Source: agent.PromptSourceAdmin, ActorID: &actorID, CreatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
			{ID: 2, ProfileKey: agentapp.ProfileKeyClarificationHost, Version: 2, SystemPrompt: "v2", Note: "note", Source: agent.PromptSourceReset},
		},
		hasMore: true,
	}
	handler := newProfileServer(t, service)
	recorder := profileRequest(t, handler, http.MethodGet, "/api/agents/profiles/clarification_host/prompt/revisions?limit=2&before_version=5", "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	payload := decodeAgentCenterBody(t, recorder)
	if payload["has_more"] != true || payload["next_cursor"] != "2" {
		t.Fatalf("envelope = %#v", payload)
	}
	items := payload["data"].([]any)
	first := items[0].(map[string]any)
	if first["version"].(float64) != 3 || first["source"] != "admin" || first["actor_id"] != "7" {
		t.Fatalf("revision = %#v", first)
	}
	if _, ok := first["system_prompt"]; !ok {
		t.Fatal("revision misses system_prompt")
	}
	if service.lastLimit != 2 || service.lastBefore == nil || *service.lastBefore != 5 {
		t.Fatalf("limit=%d before=%v", service.lastLimit, service.lastBefore)
	}

	for _, query := range []string{"?limit=0", "?limit=101", "?before_version=0", "?before_version=abc"} {
		recorder := profileRequest(t, handler, http.MethodGet, "/api/agents/profiles/clarification_host/prompt/revisions"+query, "", nil)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("query %q status = %d", query, recorder.Code)
		}
	}
}

func TestPromptPreviewEndpoint(t *testing.T) {
	service := &stubProfileService{rendered: "[profile:clarification_host] rendered"}
	handler := newProfileServer(t, service)
	recorder := profileRequest(t, handler, http.MethodPost, "/api/agents/profiles/clarification_host/prompt/preview",
		`{"system_prompt":"[profile:clarification_host] {{.VersionTitle}}","sample":{"VersionTitle":"预览"}}`, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	data := decodeAgentCenterBody(t, recorder)["data"].(map[string]any)
	if data["rendered"] != "[profile:clarification_host] rendered" {
		t.Fatalf("data = %#v", data)
	}
	if service.lastTemplate != "[profile:clarification_host] {{.VersionTitle}}" {
		t.Fatalf("template = %q", service.lastTemplate)
	}
	if service.lastSample["VersionTitle"] != "预览" {
		t.Fatalf("sample = %#v", service.lastSample)
	}

	// Empty body previews the current body.
	recorder = profileRequest(t, handler, http.MethodPost, "/api/agents/profiles/clarification_host/prompt/preview", `{}`, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("empty preview status = %d", recorder.Code)
	}
	if service.lastTemplate != "" || service.lastSample != nil {
		t.Fatalf("empty preview = %q/%#v", service.lastTemplate, service.lastSample)
	}
}

func TestListAgentToolsEndpoint(t *testing.T) {
	service := &stubProfileService{tools: []agentapp.AgentToolView{{
		Name: "requirement.read", Description: "读取需求", Parameters: json.RawMessage(`{"type":"object"}`),
	}}}
	handler := newProfileServer(t, service)
	recorder := profileRequest(t, handler, http.MethodGet, "/api/agents/tools", "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	items := decodeAgentCenterBody(t, recorder)["data"].([]any)
	tool := items[0].(map[string]any)
	if tool["name"] != "requirement.read" || tool["description"] != "读取需求" {
		t.Fatalf("tool = %#v", tool)
	}
	parameters := tool["parameters"].(map[string]any)
	if parameters["type"] != "object" {
		t.Fatalf("parameters = %#v", parameters)
	}
}

func TestProfileErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "not found", err: platform.NotFound("agent_profile"), status: 404, code: "not_found"},
		{name: "conflict", err: platform.StateConflict("version conflict"), status: 409, code: "state_conflict"},
		{name: "invalid", err: platform.Invalid("model", "bad"), status: 400, code: "invalid_argument"},
		{name: "internal", err: context.Canceled, status: 500, code: "internal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := &stubProfileService{err: tc.err}
			handler := newProfileServer(t, service)
			recorder := profileRequest(t, handler, http.MethodGet, "/api/agents/profiles/clarification_host", "", nil)
			if recorder.Code != tc.status {
				t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
			}
			code := decodeAgentCenterBody(t, recorder)["error"].(map[string]any)["code"]
			if tc.code != "" && code != tc.code {
				t.Fatalf("code = %#v", code)
			}
		})
	}
}

func TestRegisterProfileRoutesNilHandlerIsSafe(t *testing.T) {
	mux := http.NewServeMux()
	agentapi.RegisterProfileRoutes(mux, profileAuth, nil)
	agentapi.RegisterProfileRoutes(nil, profileAuth, agentapi.NewProfileHandler(nil))
}

func TestProfileNilServiceIsUnavailable(t *testing.T) {
	handler := agentapi.NewProfileHandler(nil)
	mux := http.NewServeMux()
	agentapi.RegisterProfileRoutes(mux, profileAuth, handler)
	recorder := profileRequest(t, mux, http.MethodGet, "/api/agents/profiles", "", nil)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
}
