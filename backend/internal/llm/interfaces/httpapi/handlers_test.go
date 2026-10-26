package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	identityapp "github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	llm "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// capturingRecorder keeps audit entries so tests can assert the test endpoint's
// success/failure entries without touching a database.
type capturingRecorder struct {
	entries []auditdomain.Entry
}

func (r *capturingRecorder) Record(_ context.Context, entry auditdomain.Entry) error {
	r.entries = append(r.entries, entry)
	return nil
}

func (r *capturingRecorder) List(context.Context, int, int64) ([]auditdomain.Entry, bool, error) {
	return r.entries, false, nil
}

// fakeConfigService is an in-memory ConfigService. It mirrors the production
// service's partial-update semantics so the handler DTOs can be asserted.
type fakeConfigService struct {
	configs map[int64]llm.Config
	nextID  int64
}

func newFakeConfigService(configs ...llm.Config) *fakeConfigService {
	svc := &fakeConfigService{configs: make(map[int64]llm.Config)}
	for _, cfg := range configs {
		svc.configs[cfg.ID] = cfg
		if cfg.ID > svc.nextID {
			svc.nextID = cfg.ID
		}
	}
	return svc
}

func (s *fakeConfigService) Create(_ context.Context, params llm.CreateParams) (llm.Config, error) {
	if err := params.Validate(); err != nil {
		return llm.Config{}, err
	}
	for _, cfg := range s.configs {
		if cfg.Name == params.Name {
			return llm.Config{}, llm.ErrNameTaken
		}
	}
	if params.IsDefault {
		for id, cfg := range s.configs {
			cfg.IsDefault = false
			s.configs[id] = cfg
		}
	}
	s.nextID++
	cfg := llm.Config{
		ID:                s.nextID,
		Name:              params.Name,
		Protocol:          params.Protocol,
		BaseURL:           params.BaseURL,
		Model:             params.Model,
		APIKey:            params.APIKey,
		KeyVersion:        1,
		Headers:           params.Headers,
		RetryCount:        params.RetryCount,
		RetryIntervalMS:   params.RetryIntervalMS,
		TimeoutMS:         params.TimeoutMS,
		StreamEnabled:     params.StreamEnabled,
		MultimodalEnabled: params.MultimodalEnabled,
		IsDefault:         params.IsDefault,
		ModelCatalog:      []llm.ModelInfo{},
	}
	if cfg.Headers == nil {
		cfg.Headers = map[string]string{}
	}
	s.configs[cfg.ID] = cfg
	return cfg, nil
}

func (s *fakeConfigService) Update(_ context.Context, id int64, params llm.UpdateParams) (llm.Config, error) {
	if err := params.Validate(); err != nil {
		return llm.Config{}, err
	}
	cfg, ok := s.configs[id]
	if !ok {
		return llm.Config{}, llm.ErrConfigNotFound
	}
	if params.Name != nil {
		cfg.Name = *params.Name
	}
	if params.Protocol != nil {
		cfg.Protocol = *params.Protocol
	}
	if params.BaseURL != nil {
		cfg.BaseURL = *params.BaseURL
	}
	if params.Model != nil {
		cfg.Model = *params.Model
	}
	if params.APIKey != nil {
		cfg.APIKey = *params.APIKey
	}
	if params.Headers != nil {
		cfg.Headers = *params.Headers
	}
	if params.RetryCount != nil {
		cfg.RetryCount = *params.RetryCount
	}
	if params.RetryIntervalMS != nil {
		cfg.RetryIntervalMS = *params.RetryIntervalMS
	}
	if params.TimeoutMS != nil {
		cfg.TimeoutMS = *params.TimeoutMS
	}
	if params.StreamEnabled != nil {
		cfg.StreamEnabled = *params.StreamEnabled
	}
	if params.MultimodalEnabled != nil {
		cfg.MultimodalEnabled = *params.MultimodalEnabled
	}
	if params.IsDefault != nil {
		if *params.IsDefault {
			for otherID, other := range s.configs {
				other.IsDefault = false
				s.configs[otherID] = other
			}
		}
		cfg.IsDefault = *params.IsDefault
	}
	if params.ModelCatalog != nil {
		cfg.ModelCatalog = *params.ModelCatalog
		now := time.Now().UTC()
		cfg.CatalogFetchedAt = &now
	}
	if params.CatalogFetchedAt != nil {
		cfg.CatalogFetchedAt = params.CatalogFetchedAt
	}
	s.configs[id] = cfg
	return cfg, nil
}

func (s *fakeConfigService) Delete(_ context.Context, id int64) error {
	if _, ok := s.configs[id]; !ok {
		return llm.ErrConfigNotFound
	}
	delete(s.configs, id)
	return nil
}

func (s *fakeConfigService) Get(_ context.Context, id int64) (llm.Config, error) {
	cfg, ok := s.configs[id]
	if !ok {
		return llm.Config{}, llm.ErrConfigNotFound
	}
	return cfg, nil
}

func (s *fakeConfigService) List(context.Context) ([]llm.Config, error) {
	ids := make([]int64, 0, len(s.configs))
	for id := range s.configs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	configs := make([]llm.Config, 0, len(ids))
	for _, id := range ids {
		configs = append(configs, s.configs[id])
	}
	return configs, nil
}

func (s *fakeConfigService) GetDefault(context.Context) (llm.Config, error) {
	for _, cfg := range s.configs {
		if cfg.IsDefault {
			return cfg, nil
		}
	}
	return llm.Config{}, llm.ErrConfigNotFound
}

// fakeLLMClient records the last request and returns scripted results.
type fakeLLMClient struct {
	chatFn     func(ctx context.Context, req llmapp.ChatRequest) (llmapp.ChatResponse, error)
	streamFn   func(ctx context.Context, req llmapp.ChatRequest) (<-chan llmapp.StreamEvent, error)
	modelsFn   func(ctx context.Context) ([]llm.ModelInfo, error)
	lastChat   llmapp.ChatRequest
	chatCalls  int
	modelCalls int
}

func (c *fakeLLMClient) Chat(ctx context.Context, req llmapp.ChatRequest) (llmapp.ChatResponse, error) {
	c.chatCalls++
	c.lastChat = req
	if c.chatFn != nil {
		return c.chatFn(ctx, req)
	}
	return llmapp.ChatResponse{Text: "ok"}, nil
}

func (c *fakeLLMClient) ChatStream(ctx context.Context, req llmapp.ChatRequest) (<-chan llmapp.StreamEvent, error) {
	c.lastChat = req
	if c.streamFn != nil {
		return c.streamFn(ctx, req)
	}
	events := make(chan llmapp.StreamEvent, 1)
	events <- llmapp.StreamEvent{Done: true}
	close(events)
	return events, nil
}

func (c *fakeLLMClient) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	c.modelCalls++
	if c.modelsFn != nil {
		return c.modelsFn(ctx)
	}
	return []llm.ModelInfo{}, nil
}

// fakeFactory records the configuration it was asked to build a client for.
type fakeFactory struct {
	client  *fakeLLMClient
	lastCfg llm.Config
	calls   int
}

func (f *fakeFactory) build(cfg llm.Config) llmapp.Client {
	f.calls++
	f.lastCfg = cfg
	return f.client
}

// testAuth authenticates a fixed set of bearer tokens for the admin guard.
type testAuth struct {
	tokens map[string]identity.User
}

func (a testAuth) Login(context.Context, string, string) (identityapp.LoginResult, error) {
	return identityapp.LoginResult{}, identity.ErrUnauthenticated
}

func (a testAuth) Authenticate(_ context.Context, rawToken string) (identity.User, error) {
	user, ok := a.tokens[rawToken]
	if !ok {
		return identity.User{}, identity.ErrUnauthenticated
	}
	return user, nil
}

func (a testAuth) Logout(context.Context, string) error { return nil }

func defaultTestConfig() llm.Config {
	return llm.Config{
		ID:                1,
		Name:              "default",
		Protocol:          llm.ProtocolChat,
		BaseURL:           "https://api.deepseek.com",
		Model:             "deepseek-flash",
		APIKey:            "sk-secret",
		KeyVersion:        1,
		Headers:           map[string]string{"X-Test": "header-secret"},
		RetryCount:        2,
		RetryIntervalMS:   500,
		TimeoutMS:         60000,
		StreamEnabled:     true,
		MultimodalEnabled: true,
		IsDefault:         true,
		ModelCatalog:      []llm.ModelInfo{},
	}
}

// newAdminTestHandler mounts the LLM routes behind the production guards: the
// global bearer-token middleware and the per-route RequireAdmin.
func newAdminTestHandler(t *testing.T, svc ConfigService, factory *fakeFactory, recorder auditapp.Recorder) http.Handler {
	t.Helper()
	handler := NewHandler(svc, factory.build, recorder)
	handler.imagePath = filepath.Join("..", "..", "..", "..", "testdata", "pixel.png")
	mux := http.NewServeMux()
	RegisterRoutes(mux, handler, identityhttp.RequireAdmin)
	admin := identity.User{ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive}
	regular := identity.User{ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive}
	return identityhttp.AuthMiddleware(testAuth{tokens: map[string]identity.User{
		"ezt_admin": admin,
		"ezt_user":  regular,
	}})(mux)
}

func doLLMRequest(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	var req *http.Request
	if reader == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, reader)
	}
	req.Header.Set("X-Request-ID", "req_llm_test_1234")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func errorCodeOf(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	return payload.Error.Code
}

// TestLLMConfigAdminOnlyAndTestEndpoint is the brief's end-to-end handler test:
// the admin sees masked configs, the test endpoint returns text and normalized
// SSE, catalog refresh stores context_window, and a regular user is forbidden.
func TestLLMConfigAdminOnlyAndTestEndpoint(t *testing.T) {
	svc := newFakeConfigService(defaultTestConfig())
	client := &fakeLLMClient{}
	factory := &fakeFactory{client: client}
	recorder := &capturingRecorder{}
	h := newAdminTestHandler(t, svc, factory, recorder)

	// GET configs masks secrets: never the plaintext key or header values.
	rec := doLLMRequest(t, h, http.MethodGet, "/api/admin/llm/configs", "ezt_admin", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list=%d body=%s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, secret := range []string{"sk-secret", "header-secret"} {
		if strings.Contains(body, secret) {
			t.Fatalf("GET configs leaked %q: %s", secret, body)
		}
	}
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list %q: %v", rec.Body, err)
	}
	if len(list.Data) != 1 {
		t.Fatalf("configs=%d want 1", len(list.Data))
	}
	item := list.Data[0]
	if _, ok := item["api_key"]; ok {
		t.Fatalf("response exposes api_key: %v", item)
	}
	if _, ok := item["headers"]; ok {
		t.Fatalf("response exposes headers: %v", item)
	}
	if item["has_api_key"] != true || item["key_version"] != float64(1) {
		t.Fatalf("masking fields=%v", item)
	}
	headerNames, ok := item["header_names"].([]any)
	if !ok || len(headerNames) != 1 || headerNames[0] != "X-Test" {
		t.Fatalf("header_names=%v want [X-Test]", item["header_names"])
	}
	if item["protocol"] != "chat_completions" || item["base_url"] != "https://api.deepseek.com" ||
		item["model"] != "deepseek-flash" || item["is_default"] != true {
		t.Fatalf("config fields=%v", item)
	}

	// Admin-only: a regular user gets 403, an anonymous caller 401.
	if rec := doLLMRequest(t, h, http.MethodGet, "/api/admin/llm/configs", "ezt_user", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("regular user list=%d want 403 body=%s", rec.Code, rec.Body)
	}
	rec = doLLMRequest(t, h, http.MethodGet, "/api/admin/llm/configs", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list=%d want 401 body=%s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("WWW-Authenticate=%q", got)
	}

	// Catalog refresh calls ListModels and persists context_window.
	client.modelsFn = func(context.Context) ([]llm.ModelInfo, error) {
		return []llm.ModelInfo{{ID: "deepseek-flash", ContextWindow: int64Ptr(1048576)}}, nil
	}
	rec = doLLMRequest(t, h, http.MethodPost, "/api/admin/llm/configs/1/catalog/refresh", "ezt_admin", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog refresh=%d body=%s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"context_window":1048576`) {
		t.Fatalf("catalog refresh body=%s", rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"catalog_fetched_at":"`) {
		t.Fatalf("catalog refresh did not stamp catalog_fetched_at: %s", rec.Body)
	}
	if client.modelCalls != 1 {
		t.Fatalf("ListModels calls=%d want 1", client.modelCalls)
	}

	// Non-streaming test with the built-in image returns the text.
	client.chatFn = func(context.Context, llmapp.ChatRequest) (llmapp.ChatResponse, error) {
		return llmapp.ChatResponse{Text: "a pixel", Usage: llmapp.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3}}, nil
	}
	rec = doLLMRequest(t, h, http.MethodPost, "/api/admin/llm/configs/1/test", "ezt_admin", `{"image":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("test=%d body=%s", rec.Code, rec.Body)
	}
	var testResult struct {
		Data struct {
			Text    string       `json:"text"`
			Usage   llmapp.Usage `json:"usage"`
			Latency int64        `json:"latency_ms"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &testResult); err != nil {
		t.Fatalf("decode test result %q: %v", rec.Body, err)
	}
	if testResult.Data.Text != "a pixel" || testResult.Data.Usage.TotalTokens != 3 {
		t.Fatalf("test result=%+v", testResult.Data)
	}
	if len(client.lastChat.Messages) != 1 || len(client.lastChat.Messages[0].Content) != 2 {
		t.Fatalf("image request parts=%+v", client.lastChat.Messages)
	}
	if client.lastChat.Messages[0].Content[1].Type != "image_url" ||
		!strings.HasPrefix(client.lastChat.Messages[0].Content[1].ImageURL, "data:image/png;base64,") {
		t.Fatalf("image part=%+v", client.lastChat.Messages[0].Content[1])
	}

	// Streaming test returns the normalized llm.delta/llm.done frames.
	client.streamFn = func(context.Context, llmapp.ChatRequest) (<-chan llmapp.StreamEvent, error) {
		events := make(chan llmapp.StreamEvent, 3)
		events <- llmapp.StreamEvent{Delta: "he"}
		events <- llmapp.StreamEvent{Delta: "llo"}
		events <- llmapp.StreamEvent{Done: true, Usage: llmapp.Usage{TotalTokens: 3}}
		close(events)
		return events, nil
	}
	rec = doLLMRequest(t, h, http.MethodPost, "/api/admin/llm/configs/1/test", "ezt_admin", `{"stream":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("stream test=%d body=%s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("stream Content-Type=%q", ct)
	}
	sse := rec.Body.String()
	for _, want := range []string{"event: llm.delta", "data: {\"delta\":\"he\"}", "data: {\"delta\":\"llo\"}", "event: llm.done"} {
		if !strings.Contains(sse, want) {
			t.Fatalf("stream body missing %q: %s", want, sse)
		}
	}

	// Both test calls are audited without secrets.
	var testEntries []auditdomain.Entry
	for _, entry := range recorder.entries {
		if strings.HasPrefix(entry.Action, "llm.test_") {
			testEntries = append(testEntries, entry)
		}
	}
	if len(testEntries) != 2 {
		t.Fatalf("test audit entries=%d want 2: %+v", len(testEntries), recorder.entries)
	}
	for _, entry := range testEntries {
		rendered := fmt.Sprint(entry.Metadata)
		if strings.Contains(rendered, "sk-secret") || strings.Contains(rendered, "header-secret") || strings.Contains(rendered, "base64") {
			t.Fatalf("audit metadata leaks secrets: %v", entry.Metadata)
		}
	}
	if testEntries[0].Action != auditapp.ActionLLMTestSucceeded || testEntries[1].Action != auditapp.ActionLLMTestSucceeded {
		t.Fatalf("test audit actions=%q/%q", testEntries[0].Action, testEntries[1].Action)
	}
}

func TestLLMConfigCreateUpdateDeleteAndDefaults(t *testing.T) {
	svc := newFakeConfigService(defaultTestConfig())
	factory := &fakeFactory{client: &fakeLLMClient{}}
	h := newAdminTestHandler(t, svc, factory, &capturingRecorder{})

	// Omitted timing/feature fields must resolve to the documented defaults.
	rec := doLLMRequest(t, h, http.MethodPost, "/api/admin/llm/configs", "ezt_admin",
		`{"name":" second ","protocol":"chat_completions","base_url":"https://api.deepseek.com","model":"deepseek-flash"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create=%d body=%s", rec.Code, rec.Body)
	}
	var created struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create %q: %v", rec.Body, err)
	}
	for field, want := range map[string]any{
		"retry_count":        float64(2),
		"retry_interval_ms":  float64(500),
		"timeout_ms":         float64(60000),
		"stream_enabled":     true,
		"multimodal_enabled": true,
		"is_default":         false,
		"has_api_key":        false,
	} {
		if created.Data[field] != want {
			t.Fatalf("created %s=%v want %v: %s", field, created.Data[field], want, rec.Body)
		}
	}
	if headerNames, ok := created.Data["header_names"].([]any); !ok || len(headerNames) != 0 {
		t.Fatalf("header_names=%v want []", created.Data["header_names"])
	}

	// Explicit zero is valid for retry_count and must be preserved.
	rec = doLLMRequest(t, h, http.MethodPost, "/api/admin/llm/configs", "ezt_admin",
		`{"name":"zero","protocol":"chat_completions","base_url":"https://api.deepseek.com","model":"x","retry_count":0,"stream_enabled":false}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("zero create=%d body=%s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"retry_count":0`) || !strings.Contains(rec.Body.String(), `"stream_enabled":false`) {
		t.Fatalf("explicit zero not preserved: %s", rec.Body)
	}

	// PATCH without api_key/headers keeps both; providing them replaces them.
	rec = doLLMRequest(t, h, http.MethodPatch, "/api/admin/llm/configs/1", "ezt_admin", `{"model":"deepseek-pro"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch=%d body=%s", rec.Code, rec.Body)
	}
	cfg, err := svc.Get(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "deepseek-pro" || cfg.APIKey != "sk-secret" || cfg.Headers["X-Test"] != "header-secret" {
		t.Fatalf("omitted patch fields changed: %+v", cfg)
	}
	rec = doLLMRequest(t, h, http.MethodPatch, "/api/admin/llm/configs/1", "ezt_admin",
		`{"api_key":"sk-new","headers":{"X-New":"value"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace patch=%d body=%s", rec.Code, rec.Body)
	}
	cfg, _ = svc.Get(context.Background(), 1)
	if cfg.APIKey != "sk-new" || len(cfg.Headers) != 1 || cfg.Headers["X-New"] != "value" {
		t.Fatalf("provided patch fields not replaced: %+v", cfg)
	}
	rec = doLLMRequest(t, h, http.MethodPatch, "/api/admin/llm/configs/1", "ezt_admin", `{"api_key":""}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"has_api_key":false`) {
		t.Fatalf("clear api key=%d body=%s", rec.Code, rec.Body)
	}

	// Delete removes the config and then reports 404.
	if rec := doLLMRequest(t, h, http.MethodDelete, "/api/admin/llm/configs/1", "ezt_admin", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete=%d body=%s", rec.Code, rec.Body)
	}
	if _, err := svc.Get(context.Background(), 1); err == nil {
		t.Fatal("config still exists after DELETE")
	}
	if rec := doLLMRequest(t, h, http.MethodDelete, "/api/admin/llm/configs/1", "ezt_admin", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete=%d want 404 body=%s", rec.Code, rec.Body)
	}
}

func TestLLMTestEndpointConflictsAndFailureAudit(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*llm.Config)
		body   string
		want   int
		code   string
	}{
		{name: "stream disabled", mutate: func(cfg *llm.Config) { cfg.StreamEnabled = false }, body: `{"stream":true}`, want: http.StatusBadRequest, code: platform.CodeInvalidArgument},
		{name: "multimodal disabled", mutate: func(cfg *llm.Config) { cfg.MultimodalEnabled = false }, body: `{"image":true}`, want: http.StatusBadRequest, code: platform.CodeInvalidArgument},
		{name: "missing key", mutate: func(cfg *llm.Config) { cfg.APIKey = "" }, body: `{}`, want: http.StatusServiceUnavailable, code: platform.CodeLLMNotConfigured},
		{name: "invalid body", mutate: func(*llm.Config) {}, body: `{"prompt":`, want: http.StatusBadRequest, code: platform.CodeInvalidArgument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultTestConfig()
			tc.mutate(&cfg)
			svc := newFakeConfigService(cfg)
			client := &fakeLLMClient{}
			h := newAdminTestHandler(t, svc, &fakeFactory{client: client}, &capturingRecorder{})
			rec := doLLMRequest(t, h, http.MethodPost, "/api/admin/llm/configs/1/test", "ezt_admin", tc.body)
			if rec.Code != tc.want {
				t.Fatalf("code=%d want %d body=%s", rec.Code, tc.want, rec.Body)
			}
			if got := errorCodeOf(t, rec.Body.Bytes()); got != tc.code {
				t.Fatalf("error code=%q want %q: %s", got, tc.code, rec.Body)
			}
			if client.chatCalls != 0 {
				t.Fatalf("invalid test request reached the client %d times", client.chatCalls)
			}
		})
	}

	// A real upstream failure is mapped to the llm_* envelope and audited.
	svc := newFakeConfigService(defaultTestConfig())
	client := &fakeLLMClient{chatFn: func(context.Context, llmapp.ChatRequest) (llmapp.ChatResponse, error) {
		return llmapp.ChatResponse{}, llmapp.NewLLMError(platform.CodeLLMUpstreamError, "llm upstream error", true, 502, nil)
	}}
	recorder := &capturingRecorder{}
	h := newAdminTestHandler(t, svc, &fakeFactory{client: client}, recorder)
	rec := doLLMRequest(t, h, http.MethodPost, "/api/admin/llm/configs/1/test", "ezt_admin", `{"prompt":"hi"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("failure status=%d want 502 body=%s", rec.Code, rec.Body)
	}
	var payload struct {
		Error struct {
			Code      string         `json:"code"`
			Retryable bool           `json:"retryable"`
			Details   map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode failure %q: %v", rec.Body, err)
	}
	if payload.Error.Code != platform.CodeLLMUpstreamError || !payload.Error.Retryable {
		t.Fatalf("failure envelope=%+v", payload.Error)
	}
	if payload.Error.Details["upstream_status"] != float64(502) {
		t.Fatalf("details=%v", payload.Error.Details)
	}
	if len(recorder.entries) != 1 || recorder.entries[0].Action != auditapp.ActionLLMTestFailed {
		t.Fatalf("failure audit=%+v", recorder.entries)
	}
	if recorder.entries[0].Metadata["error_code"] != platform.CodeLLMUpstreamError {
		t.Fatalf("failure metadata=%v", recorder.entries[0].Metadata)
	}
}

func TestLLMConfigValidationErrors(t *testing.T) {
	svc := newFakeConfigService(defaultTestConfig())
	h := newAdminTestHandler(t, svc, &fakeFactory{client: &fakeLLMClient{}}, &capturingRecorder{})

	rec := doLLMRequest(t, h, http.MethodPost, "/api/admin/llm/configs", "ezt_admin", `{"name":"","protocol":"chat_completions","base_url":"not-a-url","model":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid create=%d body=%s", rec.Code, rec.Body)
	}
	if got := errorCodeOf(t, rec.Body.Bytes()); got != platform.CodeInvalidArgument {
		t.Fatalf("code=%q", got)
	}
	if rec := doLLMRequest(t, h, http.MethodPost, "/api/admin/llm/configs/999/test", "ezt_admin", `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown config test=%d want 404 body=%s", rec.Code, rec.Body)
	}
	if rec := doLLMRequest(t, h, http.MethodPatch, "/api/admin/llm/configs/abc", "ezt_admin", `{"model":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id=%d want 400 body=%s", rec.Code, rec.Body)
	}
}

func int64Ptr(value int64) *int64 { return &value }
