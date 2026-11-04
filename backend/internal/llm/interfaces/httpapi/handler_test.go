package llmapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

const secretKey = "sk-super-secret"

type stubService struct {
	configs    []llm.Config
	config     llm.Config
	models     []llm.Model
	model      llm.Model
	testResult llmapp.TestResult
	err        error

	calls     []string
	lastInput llmapp.ConfigInput
	lastID    int64
	lastModel int64
	lastActor int64
	lastAdd   llmapp.ModelInput
	lastPatch llmapp.ModelPatch
}

func (s *stubService) ListConfigs(context.Context) ([]llm.Config, error) {
	s.calls = append(s.calls, "list_configs")
	return s.configs, s.err
}

func (s *stubService) GetConfig(_ context.Context, id int64) (llm.Config, error) {
	s.calls = append(s.calls, "get_config")
	s.lastID = id
	if s.err != nil {
		return llm.Config{}, s.err
	}
	return s.config, nil
}

func (s *stubService) CreateConfig(_ context.Context, cmd llmapp.ConfigInput, actorID int64) (llm.Config, error) {
	s.calls = append(s.calls, "create_config")
	s.lastInput = cmd
	s.lastActor = actorID
	if s.err != nil {
		return llm.Config{}, s.err
	}
	return s.config, nil
}

func (s *stubService) UpdateConfig(_ context.Context, id int64, cmd llmapp.ConfigInput, actorID int64) (llm.Config, error) {
	s.calls = append(s.calls, "update_config")
	s.lastID = id
	s.lastInput = cmd
	s.lastActor = actorID
	if s.err != nil {
		return llm.Config{}, s.err
	}
	return s.config, nil
}

func (s *stubService) DeleteConfig(_ context.Context, id int64, actorID int64) error {
	s.calls = append(s.calls, "delete_config")
	s.lastID = id
	s.lastActor = actorID
	return s.err
}

func (s *stubService) TestConfig(_ context.Context, id int64) (llmapp.TestResult, error) {
	s.calls = append(s.calls, "test_config")
	s.lastID = id
	if s.err != nil {
		return llmapp.TestResult{}, s.err
	}
	return s.testResult, nil
}

func (s *stubService) RefreshModels(_ context.Context, id int64, actorID int64) ([]llm.Model, error) {
	s.calls = append(s.calls, "refresh_models")
	s.lastID = id
	s.lastActor = actorID
	if s.err != nil {
		return nil, s.err
	}
	return s.models, nil
}

func (s *stubService) AddModel(_ context.Context, configID int64, input llmapp.ModelInput, actorID int64) (llm.Model, error) {
	s.calls = append(s.calls, "add_model")
	s.lastAdd = input
	s.lastID = configID
	s.lastActor = actorID
	if s.err != nil {
		return llm.Model{}, s.err
	}
	return s.model, nil
}

func (s *stubService) UpdateModel(_ context.Context, configID, modelID int64, patch llmapp.ModelPatch, actorID int64) (llm.Model, error) {
	s.calls = append(s.calls, "update_model")
	s.lastID = configID
	s.lastModel = modelID
	s.lastPatch = patch
	s.lastActor = actorID
	if s.err != nil {
		return llm.Model{}, s.err
	}
	return s.model, nil
}

func (s *stubService) DeleteModel(_ context.Context, modelID int64, actorID int64) error {
	s.calls = append(s.calls, "delete_model")
	s.lastModel = modelID
	s.lastActor = actorID
	return s.err
}

func (s *stubService) ListModels(context.Context) ([]llm.Model, error) {
	s.calls = append(s.calls, "list_models")
	return s.models, s.err
}

func newTestServer(t *testing.T) (http.Handler, *stubService) {
	t.Helper()
	stub := &stubService{}
	handler := llmapi.New(stub)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/admin/llm/configs", handler.ListConfigs)
	mux.HandleFunc("POST /api/admin/llm/configs", handler.CreateConfig)
	mux.HandleFunc("GET /api/admin/llm/configs/{id}", handler.GetConfig)
	mux.HandleFunc("PATCH /api/admin/llm/configs/{id}", handler.UpdateConfig)
	mux.HandleFunc("DELETE /api/admin/llm/configs/{id}", handler.DeleteConfig)
	mux.HandleFunc("POST /api/admin/llm/configs/{id}/test", handler.TestConfig)
	mux.HandleFunc("POST /api/admin/llm/configs/{id}/refresh-models", handler.RefreshModels)
	mux.HandleFunc("POST /api/admin/llm/configs/{id}/models", handler.AddModel)
	mux.HandleFunc("PATCH /api/admin/llm/configs/{id}/models/{modelId}", handler.UpdateModel)
	mux.HandleFunc("DELETE /api/admin/llm/configs/{id}/models/{modelId}", handler.DeleteModel)
	mux.HandleFunc("GET /api/admin/llm/models", handler.ListModels)
	return mux, stub
}

func call(t *testing.T, handler http.Handler, method, path, body string, actor *platform.Actor) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if actor != nil {
		request = request.WithContext(platform.WithActor(request.Context(), *actor))
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	return payload
}

func dataBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	payload := decodeBody(t, recorder)
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("response data is not an object: %s", recorder.Body.String())
	}
	return data
}

func dataList(t *testing.T, recorder *httptest.ResponseRecorder) []any {
	t.Helper()
	payload := decodeBody(t, recorder)
	data, ok := payload["data"].([]any)
	if !ok {
		t.Fatalf("response data is not an array: %s", recorder.Body.String())
	}
	return data
}

func sampleConfig() llm.Config {
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	createdBy := int64(42)
	return llm.Config{
		ID:             101,
		Name:           "Primary",
		Provider:       llm.ProviderOpenAI,
		Protocol:       llm.ProtocolOpenAI,
		BaseURL:        "http://127.0.0.1:13334/v1",
		APIKey:         secretKey,
		Headers:        map[string]string{"X-Org": "team-a"},
		TimeoutSeconds: 60,
		MaxRetries:     2,
		IsDefault:      true,
		Status:         llm.StatusActive,
		CreatedBy:      &createdBy,
		CreatedAt:      at,
		UpdatedAt:      at,
	}
}

func sampleModel() llm.Model {
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	return llm.Model{
		ID: 201, ConfigID: 101, Model: "gpt-4o", DisplayName: "gpt-4o",
		ContextWindow: 128000, MaxOutput: 16384, SupportsTools: true, SupportsVision: true,
		Source: llm.SourceCatalog, Status: llm.ModelStatusActive, CreatedAt: at, UpdatedAt: at,
	}
}

func adminActor() *platform.Actor {
	return &platform.Actor{ID: 7, Role: "admin"}
}

func TestListConfigsMasksAPIKey(t *testing.T) {
	handler, stub := newTestServer(t)
	empty := sampleConfig()
	empty.ID = 102
	empty.Name = "Secondary"
	empty.APIKey = ""
	empty.CreatedBy = nil
	stub.configs = []llm.Config{sampleConfig(), empty}

	recorder := call(t, handler, http.MethodGet, "/api/admin/llm/configs", "", adminActor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), secretKey) {
		t.Fatalf("api key leaked: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), `"api_key"`) {
		t.Fatalf("response exposes api_key: %s", recorder.Body.String())
	}
	items := dataList(t, recorder)
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	first := items[0].(map[string]any)
	if first["id"] != "101" || first["name"] != "Primary" {
		t.Fatalf("first = %#v", first)
	}
	if first["api_key_set"] != true {
		t.Fatalf("api_key_set = %#v", first["api_key_set"])
	}
	if second := items[1].(map[string]any); second["api_key_set"] != false {
		t.Fatalf("empty api_key_set = %#v", second["api_key_set"])
	}
	payload := decodeBody(t, recorder)
	if payload["has_more"] != false {
		t.Fatalf("has_more = %#v", payload["has_more"])
	}
}

func TestCreateConfigParsesInputAndMasksKey(t *testing.T) {
	handler, stub := newTestServer(t)
	stub.config = sampleConfig()

	body := `{
		"name": "Primary",
		"base_url": "http://127.0.0.1:13334/v1",
		"api_key": "` + secretKey + `",
		"headers": {"X-Org": "team-a"},
		"timeout_seconds": 30,
		"max_retries": 1,
		"is_default": true,
		"status": "active"
	}`
	recorder := call(t, handler, http.MethodPost, "/api/admin/llm/configs", body, adminActor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), secretKey) {
		t.Fatalf("api key leaked: %s", recorder.Body.String())
	}
	if stub.lastActor != 7 {
		t.Fatalf("actor = %d", stub.lastActor)
	}
	if stub.lastInput.Name == nil || *stub.lastInput.Name != "Primary" {
		t.Fatalf("name = %#v", stub.lastInput.Name)
	}
	if stub.lastInput.APIKey == nil || *stub.lastInput.APIKey != secretKey {
		t.Fatalf("api key was not forwarded")
	}
	if stub.lastInput.TimeoutSeconds == nil || *stub.lastInput.TimeoutSeconds != 30 {
		t.Fatalf("timeout = %#v", stub.lastInput.TimeoutSeconds)
	}
	if stub.lastInput.MaxRetries == nil || *stub.lastInput.MaxRetries != 1 {
		t.Fatalf("retries = %#v", stub.lastInput.MaxRetries)
	}
	if stub.lastInput.IsDefault == nil || !*stub.lastInput.IsDefault {
		t.Fatalf("is_default = %#v", stub.lastInput.IsDefault)
	}
	if stub.lastInput.Headers == nil || (*stub.lastInput.Headers)["X-Org"] != "team-a" {
		t.Fatalf("headers = %#v", stub.lastInput.Headers)
	}
	data := dataBody(t, recorder)
	if data["api_key_set"] != true || data["id"] != "101" {
		t.Fatalf("data = %#v", data)
	}

	bad := call(t, handler, http.MethodPost, "/api/admin/llm/configs", `{"name": `, adminActor())
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d", bad.Code)
	}
	unauthorized := call(t, handler, http.MethodPost, "/api/admin/llm/configs", `{}`, nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("missing actor status = %d", unauthorized.Code)
	}
}

func TestGetConfigValidatesPathAndMasksKey(t *testing.T) {
	handler, stub := newTestServer(t)
	stub.config = sampleConfig()

	recorder := call(t, handler, http.MethodGet, "/api/admin/llm/configs/101", "", adminActor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), secretKey) {
		t.Fatalf("api key leaked: %s", recorder.Body.String())
	}
	if data := dataBody(t, recorder); data["api_key_set"] != true {
		t.Fatalf("data = %#v", data)
	}

	invalid := call(t, handler, http.MethodGet, "/api/admin/llm/configs/abc", "", adminActor())
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid id status = %d", invalid.Code)
	}
	// The valid GET lists configs plus their catalog rows; an invalid path id
	// must not reach the service at all.
	if calls := len(stub.calls); calls != 2 {
		t.Fatalf("service called for invalid id: %#v", stub.calls)
	}

	stub.err = platform.NotFound("llm config")
	missing := call(t, handler, http.MethodGet, "/api/admin/llm/configs/999", "", adminActor())
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", missing.Code)
	}
	if payload := decodeBody(t, missing); payload["error"].(map[string]any)["code"] != "not_found" {
		t.Fatalf("error = %#v", payload["error"])
	}
}

func TestUpdateConfigSendsPartialPatch(t *testing.T) {
	handler, stub := newTestServer(t)
	stub.config = sampleConfig()

	recorder := call(t, handler, http.MethodPatch, "/api/admin/llm/configs/101", `{"name":"Renamed"}`, adminActor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.lastID != 101 || stub.lastActor != 7 {
		t.Fatalf("id/actor = %d/%d", stub.lastID, stub.lastActor)
	}
	if stub.lastInput.Name == nil || *stub.lastInput.Name != "Renamed" {
		t.Fatalf("name = %#v", stub.lastInput.Name)
	}
	if stub.lastInput.BaseURL != nil || stub.lastInput.APIKey != nil || stub.lastInput.IsDefault != nil {
		t.Fatalf("unset fields were not left nil: %#v", stub.lastInput)
	}

	stub.err = platform.Conflict("llm config name already exists")
	conflict := call(t, handler, http.MethodPatch, "/api/admin/llm/configs/101", `{"name":"Primary"}`, adminActor())
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d", conflict.Code)
	}
}

func TestDeleteConfigReturnsDeletedFlag(t *testing.T) {
	handler, stub := newTestServer(t)

	recorder := call(t, handler, http.MethodDelete, "/api/admin/llm/configs/101", "", adminActor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	data := dataBody(t, recorder)
	if data["id"] != "101" || data["deleted"] != true {
		t.Fatalf("data = %#v", data)
	}
	if stub.lastID != 101 || stub.lastActor != 7 {
		t.Fatalf("id/actor = %d/%d", stub.lastID, stub.lastActor)
	}

	stub.err = platform.NotFound("llm config")
	missing := call(t, handler, http.MethodDelete, "/api/admin/llm/configs/999", "", adminActor())
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", missing.Code)
	}
}

func TestTestConfigReturnsProbeResult(t *testing.T) {
	handler, stub := newTestServer(t)
	stub.testResult = llmapp.TestResult{
		OK:        true,
		LatencyMS: 42,
		Models: []llmapp.RemoteModel{
			{ID: "gpt-4o", ContextWindow: 128000, MaxOutput: 16384, SupportsTools: true, SupportsVision: true},
		},
	}

	recorder := call(t, handler, http.MethodPost, "/api/admin/llm/configs/101/test", "", adminActor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	data := dataBody(t, recorder)
	if data["ok"] != true || data["latency_ms"] != float64(42) {
		t.Fatalf("data = %#v", data)
	}
	models := data["models"].([]any)
	if len(models) != 1 {
		t.Fatalf("models = %#v", models)
	}
	first := models[0].(map[string]any)
	if first["id"] != "gpt-4o" || first["context_window"] != float64(128000) || first["supports_tools"] != true {
		t.Fatalf("model = %#v", first)
	}

	stub.err = platform.Unavailable("llm provider request timed out")
	failure := call(t, handler, http.MethodPost, "/api/admin/llm/configs/101/test", "", adminActor())
	if failure.Code != http.StatusServiceUnavailable {
		t.Fatalf("failure status = %d", failure.Code)
	}
}

func TestRefreshModelsReturnsCatalog(t *testing.T) {
	handler, stub := newTestServer(t)
	stub.models = []llm.Model{sampleModel()}

	recorder := call(t, handler, http.MethodPost, "/api/admin/llm/configs/101/refresh-models", "", adminActor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	items := dataList(t, recorder)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	first := items[0].(map[string]any)
	if first["id"] != "201" || first["config_id"] != "101" || first["model"] != "gpt-4o" {
		t.Fatalf("model = %#v", first)
	}
	if first["source"] != "catalog" || first["supports_vision"] != true {
		t.Fatalf("model = %#v", first)
	}
	if stub.lastID != 101 || stub.lastActor != 7 {
		t.Fatalf("id/actor = %d/%d", stub.lastID, stub.lastActor)
	}

	stub.err = platform.Unavailable("llm provider returned HTTP 500")
	failure := call(t, handler, http.MethodPost, "/api/admin/llm/configs/101/refresh-models", "", adminActor())
	if failure.Code != http.StatusServiceUnavailable {
		t.Fatalf("failure status = %d", failure.Code)
	}
}

func TestAddModelParsesFlags(t *testing.T) {
	handler, stub := newTestServer(t)
	stub.model = sampleModel()

	body := `{"model":"gpt-4o","context_window":128000,"max_output":16384,"supports_tools":true,"supports_vision":false,"supports_reasoning":true,"supports_prompt_cache":true}`
	recorder := call(t, handler, http.MethodPost, "/api/admin/llm/configs/101/models", body, adminActor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	want := llmapp.ModelInput{
		Model: "gpt-4o", ContextWindow: 128000, MaxOutput: 16384,
		SupportsTools: true, SupportsVision: false,
		SupportsReasoning: true, SupportsPromptCache: true,
	}
	if stub.lastAdd != want {
		t.Fatalf("add input = %#v, want %#v", stub.lastAdd, want)
	}
	if stub.lastID != 101 || stub.lastActor != 7 {
		t.Fatalf("id/actor = %d/%d", stub.lastID, stub.lastActor)
	}
	if data := dataBody(t, recorder); data["model"] != "gpt-4o" || data["source"] != "catalog" {
		t.Fatalf("data = %#v", data)
	}

	bad := call(t, handler, http.MethodPost, "/api/admin/llm/configs/101/models", `[]`, adminActor())
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad body status = %d", bad.Code)
	}
}

// TestUpdateModelEndpoint pins the capability-editor write path: PATCH accepts
// the five capability fields plus deprecated, maps them onto the partial patch
// without inventing values for omitted fields, and surfaces the service's
// typed 404 for unknown model IDs.
func TestUpdateModelEndpoint(t *testing.T) {
	handler, stub := newTestServer(t)
	model := sampleModel()
	model.Capabilities = llm.Capabilities{
		SupportsTools:       true,
		SupportsVision:      false,
		SupportsReasoning:   true,
		SupportsPromptCache: true,
		ContextWindow:       64000,
	}
	stub.model = model

	body := `{"context_window":64000,"supports_tools":true,"supports_vision":false,"supports_reasoning":true,"supports_prompt_cache":true,"deprecated":false}`
	recorder := call(t, handler, http.MethodPatch, "/api/admin/llm/configs/101/models/201", body, adminActor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.lastID != 101 || stub.lastModel != 201 || stub.lastActor != 7 {
		t.Fatalf("ids/actor = %d/%d/%d", stub.lastID, stub.lastModel, stub.lastActor)
	}
	patch := stub.lastPatch
	if patch.ContextWindow == nil || *patch.ContextWindow != 64000 {
		t.Fatalf("context_window = %#v", patch.ContextWindow)
	}
	if patch.SupportsReasoning == nil || !*patch.SupportsReasoning {
		t.Fatalf("supports_reasoning = %#v", patch.SupportsReasoning)
	}
	if patch.SupportsPromptCache == nil || !*patch.SupportsPromptCache {
		t.Fatalf("supports_prompt_cache = %#v", patch.SupportsPromptCache)
	}
	if patch.SupportsVision == nil || *patch.SupportsVision {
		t.Fatalf("supports_vision = %#v", patch.SupportsVision)
	}
	if patch.Deprecated == nil || *patch.Deprecated {
		t.Fatalf("deprecated = %#v", patch.Deprecated)
	}
	data := dataBody(t, recorder)
	capabilities, _ := data["capabilities"].(map[string]any)
	if capabilities["supports_reasoning"] != true || data["deprecated"] != false {
		t.Fatalf("data = %#v", data)
	}

	// Omitted fields stay nil so a one-checkbox edit does not reset the rest.
	partial := call(t, handler, http.MethodPatch, "/api/admin/llm/configs/101/models/201", `{"supports_reasoning":false}`, adminActor())
	if partial.Code != http.StatusOK {
		t.Fatalf("partial status = %d, body = %s", partial.Code, partial.Body.String())
	}
	if stub.lastPatch.SupportsReasoning == nil || *stub.lastPatch.SupportsReasoning {
		t.Fatalf("partial supports_reasoning = %#v", stub.lastPatch.SupportsReasoning)
	}
	if stub.lastPatch.ContextWindow != nil || stub.lastPatch.SupportsTools != nil || stub.lastPatch.Deprecated != nil {
		t.Fatalf("omitted fields became non-nil: %#v", stub.lastPatch)
	}

	invalidID := call(t, handler, http.MethodPatch, "/api/admin/llm/configs/101/models/not-an-id", `{}`, adminActor())
	if invalidID.Code != http.StatusBadRequest {
		t.Fatalf("invalid id status = %d", invalidID.Code)
	}
	badBody := call(t, handler, http.MethodPatch, "/api/admin/llm/configs/101/models/201", `{"context_window":"big"}`, adminActor())
	if badBody.Code != http.StatusBadRequest {
		t.Fatalf("bad body status = %d", badBody.Code)
	}

	stub.err = platform.NotFound("llm model")
	missing := call(t, handler, http.MethodPatch, "/api/admin/llm/configs/101/models/999", `{"supports_tools":true}`, adminActor())
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", missing.Code)
	}
	payload := decodeBody(t, missing)
	apiErr, _ := payload["error"].(map[string]any)
	if apiErr["code"] != "not_found" {
		t.Fatalf("error = %#v", apiErr)
	}
}

func TestDeleteModelValidatesIDs(t *testing.T) {
	handler, stub := newTestServer(t)

	recorder := call(t, handler, http.MethodDelete, "/api/admin/llm/configs/101/models/201", "", adminActor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	data := dataBody(t, recorder)
	if data["id"] != "201" || data["deleted"] != true {
		t.Fatalf("data = %#v", data)
	}
	if stub.lastModel != 201 || stub.lastActor != 7 {
		t.Fatalf("model/actor = %d/%d", stub.lastModel, stub.lastActor)
	}

	invalid := call(t, handler, http.MethodDelete, "/api/admin/llm/configs/101/models/not-an-id", "", adminActor())
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d", invalid.Code)
	}

	stub.err = platform.NotFound("llm model")
	missing := call(t, handler, http.MethodDelete, "/api/admin/llm/configs/101/models/999", "", adminActor())
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", missing.Code)
	}
}

func TestListModelsReturnsAllModels(t *testing.T) {
	handler, stub := newTestServer(t)
	stub.models = []llm.Model{sampleModel()}

	recorder := call(t, handler, http.MethodGet, "/api/admin/llm/models", "", adminActor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	items := dataList(t, recorder)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	first := items[0].(map[string]any)
	if first["id"] != "201" || first["created_at"] != "2026-10-31T12:00:00Z" {
		t.Fatalf("model = %#v", first)
	}
}

// TestLLMConfigSettingsAPI pins the 0.2.1 settings surface added by Task 44:
// protocol (canonical values, legacy passthrough), thinking_type,
// reasoning_effort, default_model_id and the embedded models[] capability
// flags. It also pins the typed 400s: unknown enums/protocols are rejected at
// the HTTP boundary, and an invalid default model surfaces the service's typed
// 400 unchanged.
func TestLLMConfigSettingsAPI(t *testing.T) {
	t.Run("exposes protocol thinking effort default model and capabilities", func(t *testing.T) {
		handler, stub := newTestServer(t)
		cfg := sampleConfig()
		cfg.Protocol = string(llm.ProtocolOpenAIResponses)
		cfg.ThinkingType = llm.ThinkingTypeEnabled
		cfg.ReasoningEffort = llm.ReasoningEffortXHigh
		cfg.DefaultModelID = 201
		stub.config = cfg
		stub.configs = []llm.Config{cfg}

		active := sampleModel()
		active.SupportsVision = false
		active.Capabilities = llm.Capabilities{
			SupportsTools:       true,
			SupportsVision:      false,
			SupportsReasoning:   true,
			SupportsPromptCache: true,
			ContextWindow:       64000,
		}
		deprecated := sampleModel()
		deprecated.ID = 202
		deprecated.Model = "gpt-4o-old"
		deprecated.DisplayName = "gpt-4o-old"
		deprecated.Deprecated = true
		stub.models = []llm.Model{active, deprecated}

		recorder := call(t, handler, http.MethodGet, "/api/admin/llm/configs/101", "", adminActor())
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), secretKey) || strings.Contains(recorder.Body.String(), `"api_key"`) {
			t.Fatalf("response leaked the api key: %s", recorder.Body.String())
		}

		data := dataBody(t, recorder)
		if data["protocol"] != string(llm.ProtocolOpenAIResponses) {
			t.Fatalf("protocol = %#v", data["protocol"])
		}
		if data["thinking_type"] != llm.ThinkingTypeEnabled || data["reasoning_effort"] != llm.ReasoningEffortXHigh {
			t.Fatalf("thinking settings = %#v/%#v", data["thinking_type"], data["reasoning_effort"])
		}
		if data["default_model_id"] != "201" {
			t.Fatalf("default_model_id = %#v", data["default_model_id"])
		}

		models, ok := data["models"].([]any)
		if !ok || len(models) != 2 {
			t.Fatalf("models = %#v", data["models"])
		}
		first, _ := models[0].(map[string]any)
		capabilities, ok := first["capabilities"].(map[string]any)
		if !ok {
			t.Fatalf("capabilities = %#v", first["capabilities"])
		}
		if capabilities["supports_tools"] != true ||
			capabilities["supports_vision"] != false ||
			capabilities["supports_reasoning"] != true ||
			capabilities["supports_prompt_cache"] != true ||
			capabilities["context_window"] != float64(64000) {
			t.Fatalf("capabilities = %#v", capabilities)
		}
		if first["deprecated"] != false {
			t.Fatalf("active model deprecated = %#v", first["deprecated"])
		}
		second, _ := models[1].(map[string]any)
		if second["deprecated"] != true {
			t.Fatalf("deprecated model flag = %#v", second["deprecated"])
		}

		listed := call(t, handler, http.MethodGet, "/api/admin/llm/configs", "", adminActor())
		if listed.Code != http.StatusOK {
			t.Fatalf("list status = %d, body = %s", listed.Code, listed.Body.String())
		}
		items := dataList(t, listed)
		if len(items) != 1 {
			t.Fatalf("configs = %#v", items)
		}
		item, _ := items[0].(map[string]any)
		listedModels, _ := item["models"].([]any)
		if item["thinking_type"] != llm.ThinkingTypeEnabled || len(listedModels) != 2 {
			t.Fatalf("listed config = %#v", item)
		}
	})

	t.Run("keeps legacy protocols visible", func(t *testing.T) {
		handler, stub := newTestServer(t)
		cfg := sampleConfig()
		cfg.Protocol = llm.ProtocolOpenAICompatible
		stub.config = cfg

		recorder := call(t, handler, http.MethodGet, "/api/admin/llm/configs/101", "", adminActor())
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		data := dataBody(t, recorder)
		if data["protocol"] != llm.ProtocolOpenAICompatible {
			t.Fatalf("legacy protocol was rewritten: %#v", data["protocol"])
		}
		// Fields written before the 0.2.1 columns existed read as schema defaults.
		if data["thinking_type"] != llm.DefaultThinkingType || data["reasoning_effort"] != llm.DefaultReasoningEffort {
			t.Fatalf("effective defaults = %#v/%#v", data["thinking_type"], data["reasoning_effort"])
		}
		if _, present := data["default_model_id"]; present {
			t.Fatalf("unset default_model_id should be omitted: %#v", data["default_model_id"])
		}
	})

	t.Run("create and update accept the new settings", func(t *testing.T) {
		handler, stub := newTestServer(t)
		cfg := sampleConfig()
		cfg.Protocol = string(llm.ProtocolOpenAIResponses)
		cfg.ThinkingType = llm.ThinkingTypeDisabled
		cfg.ReasoningEffort = llm.ReasoningEffortLow
		cfg.DefaultModelID = 201
		stub.config = cfg

		createBody := `{
			"name": "Primary",
			"base_url": "http://127.0.0.1:13334/v1",
			"protocol": "openai_chat_completions",
			"thinking_type": "disabled",
			"reasoning_effort": "low",
			"default_model_id": "201",
			"status": "active"
		}`
		recorder := call(t, handler, http.MethodPost, "/api/admin/llm/configs", createBody, adminActor())
		if recorder.Code != http.StatusOK {
			t.Fatalf("create status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if stub.lastInput.Protocol == nil || *stub.lastInput.Protocol != string(llm.ProtocolOpenAIChatCompletions) {
			t.Fatalf("protocol = %#v", stub.lastInput.Protocol)
		}
		if stub.lastInput.ThinkingType == nil || *stub.lastInput.ThinkingType != llm.ThinkingTypeDisabled {
			t.Fatalf("thinking_type = %#v", stub.lastInput.ThinkingType)
		}
		if stub.lastInput.ReasoningEffort == nil || *stub.lastInput.ReasoningEffort != llm.ReasoningEffortLow {
			t.Fatalf("reasoning_effort = %#v", stub.lastInput.ReasoningEffort)
		}
		if stub.lastInput.DefaultModelID == nil || *stub.lastInput.DefaultModelID != 201 {
			t.Fatalf("default_model_id = %#v", stub.lastInput.DefaultModelID)
		}
		if data := dataBody(t, recorder); data["default_model_id"] != "201" || data["protocol"] != string(llm.ProtocolOpenAIResponses) {
			t.Fatalf("create response = %#v", data)
		}

		updateBody := `{"thinking_type":"enabled","reasoning_effort":"max","default_model_id":"202"}`
		update := call(t, handler, http.MethodPatch, "/api/admin/llm/configs/101", updateBody, adminActor())
		if update.Code != http.StatusOK {
			t.Fatalf("update status = %d, body = %s", update.Code, update.Body.String())
		}
		if stub.lastInput.ThinkingType == nil || *stub.lastInput.ThinkingType != llm.ThinkingTypeEnabled {
			t.Fatalf("updated thinking_type = %#v", stub.lastInput.ThinkingType)
		}
		if stub.lastInput.ReasoningEffort == nil || *stub.lastInput.ReasoningEffort != llm.ReasoningEffortMax {
			t.Fatalf("updated reasoning_effort = %#v", stub.lastInput.ReasoningEffort)
		}
		if stub.lastInput.DefaultModelID == nil || *stub.lastInput.DefaultModelID != 202 {
			t.Fatalf("updated default_model_id = %#v", stub.lastInput.DefaultModelID)
		}
	})

	t.Run("rejects invalid enums and protocols with a typed 400", func(t *testing.T) {
		handler, stub := newTestServer(t)
		cases := []struct {
			name  string
			body  string
			field string
		}{
			{"thinking_type", `{"thinking_type":"sometimes"}`, "thinking_type"},
			{"reasoning_effort", `{"reasoning_effort":"extreme"}`, "reasoning_effort"},
			{"protocol", `{"protocol":"anthropic"}`, "protocol"},
			{"default_model_id", `{"default_model_id":"not-an-id"}`, "default_model_id"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				stub.calls = nil
				recorder := call(t, handler, http.MethodPost, "/api/admin/llm/configs", tc.body, adminActor())
				if recorder.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
				}
				payload := decodeBody(t, recorder)
				apiErr, _ := payload["error"].(map[string]any)
				if apiErr["code"] != "invalid_argument" {
					t.Fatalf("error = %#v", apiErr)
				}
				details, _ := apiErr["details"].(map[string]any)
				if details["field"] != tc.field {
					t.Fatalf("details = %#v", details)
				}
				if len(stub.calls) != 0 {
					t.Fatalf("service called for invalid input: %#v", stub.calls)
				}
			})
		}
	})

	t.Run("maps a missing default model to a typed 400", func(t *testing.T) {
		handler, stub := newTestServer(t)
		stub.err = platform.Invalid("default_model_id", "must reference a model of this LLM configuration")

		recorder := call(t, handler, http.MethodPatch, "/api/admin/llm/configs/101", `{"default_model_id":"999"}`, adminActor())
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		payload := decodeBody(t, recorder)
		apiErr, _ := payload["error"].(map[string]any)
		if apiErr["code"] != "invalid_argument" {
			t.Fatalf("error = %#v", apiErr)
		}
		if details, _ := apiErr["details"].(map[string]any); details["field"] != "default_model_id" {
			t.Fatalf("error details = %#v", details)
		}
	})
}
