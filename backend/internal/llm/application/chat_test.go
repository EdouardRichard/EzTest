package llmapp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

type chatCall struct {
	cfg llm.Config
	req ChatRequest
}

type fakeChatGateway struct {
	mu       sync.Mutex
	calls    []chatCall
	response ChatResponse
	err      error
}

func (f *fakeChatGateway) Chat(_ context.Context, cfg llm.Config, req ChatRequest) (ChatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, chatCall{cfg: cfg, req: req})
	if f.err != nil {
		return ChatResponse{}, f.err
	}
	return f.response, nil
}

func (f *fakeChatGateway) lastCall(t *testing.T) chatCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("chat gateway was not called")
	}
	return f.calls[len(f.calls)-1]
}

func (f *fakeChatGateway) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakeChatSettings struct {
	values map[string]int64
	err    error
}

func (f *fakeChatSettings) Int(_ context.Context, key string) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	value, ok := f.values[key]
	if !ok {
		return 0, platform.NotFound("setting")
	}
	return value, nil
}

const chatTestKey = "sk-chat-super-secret"

func chatTestConfig(id int64, isDefault bool) llm.Config {
	status := llm.StatusActive
	return llm.Config{
		ID:             id,
		Name:           fmt.Sprintf("config-%d", id),
		Provider:       llm.ProviderOpenAI,
		Protocol:       llm.ProtocolOpenAI,
		BaseURL:        "http://127.0.0.1:13334/v1",
		APIKey:         chatTestKey,
		Headers:        map[string]string{},
		TimeoutSeconds: 45,
		MaxRetries:     1,
		IsDefault:      isDefault,
		Status:         status,
	}
}

func chatTestModel(id, configID int64, name string, contextWindow, maxOutput int) llm.Model {
	return llm.Model{
		ID:            id,
		ConfigID:      configID,
		Model:         name,
		DisplayName:   name,
		ContextWindow: contextWindow,
		MaxOutput:     maxOutput,
		Source:        llm.SourceCatalog,
		Status:        llm.ModelStatusActive,
	}
}

func newChatTestService(t *testing.T, gateway ChatClient, settings SettingsReader) (*Service, *fakeRepo) {
	t.Helper()
	repo := newFakeRepo()
	opts := []Option{WithChatClient(gateway)}
	if settings != nil {
		opts = append(opts, WithSettingsReader(settings))
	}
	svc := New(repo, &fakeClient{}, &seqIDs{}, fixedClock{at: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)}, nil, nil, opts...)
	return svc, repo
}

func seedSingleConfig(t *testing.T, repo *fakeRepo) {
	t.Helper()
	repo.configs[7] = chatTestConfig(7, true)
	repo.models[11] = chatTestModel(11, 7, "fake-llm", 64000, 8192)
	repo.models[12] = chatTestModel(12, 7, "fake-llm-mini", 32000, 4096)
}

func TestChatSelectsDefaultConfigAndFirstCatalogModel(t *testing.T) {
	gateway := &fakeChatGateway{response: ChatResponse{Content: "你好", FinishReason: "stop"}}
	svc, repo := newChatTestService(t, gateway, nil)
	seedSingleConfig(t, repo)
	repo.configs[9] = chatTestConfig(9, false)

	response, err := svc.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "帮我做一个登录"}},
		Tools:    []ToolDefinition{{Name: "requirement.draft_create", Description: "create", Parameters: []byte(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if response.Content != "你好" || response.FinishReason != "stop" {
		t.Fatalf("response = %#v", response)
	}
	call := gateway.lastCall(t)
	if call.cfg.ID != 7 {
		t.Fatalf("config id = %d, want default config 7", call.cfg.ID)
	}
	if call.req.Model != "fake-llm" {
		t.Fatalf("model = %q, want first catalog model", call.req.Model)
	}
	if call.req.MaxOutputTokens != 8192 {
		t.Fatalf("max output tokens = %d, want catalog max_output", call.req.MaxOutputTokens)
	}
	if len(call.req.Tools) != 1 || call.req.Tools[0].Name != "requirement.draft_create" {
		t.Fatalf("tools = %#v", call.req.Tools)
	}
}

func TestChatUsesRequestedCatalogModel(t *testing.T) {
	gateway := &fakeChatGateway{response: ChatResponse{Content: "ok"}}
	svc, repo := newChatTestService(t, gateway, nil)
	seedSingleConfig(t, repo)

	if _, err := svc.Chat(context.Background(), ChatRequest{
		Model:    "fake-llm-mini",
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	call := gateway.lastCall(t)
	if call.req.Model != "fake-llm-mini" || call.req.MaxOutputTokens != 4096 {
		t.Fatalf("model/max tokens = %q/%d", call.req.Model, call.req.MaxOutputTokens)
	}
}

func TestResolveModelReturnsCatalogMetadata(t *testing.T) {
	gateway := &fakeChatGateway{}
	svc, repo := newChatTestService(t, gateway, nil)
	seedSingleConfig(t, repo)
	repo.models[13] = llm.Model{ID: 13, ConfigID: 7, Model: "tool-model", ContextWindow: 128000, MaxOutput: 16384, SupportsTools: true, Status: llm.ModelStatusActive}

	info, err := svc.ResolveModel(context.Background(), "tool-model")
	if err != nil {
		t.Fatalf("ResolveModel: %v", err)
	}
	want := ModelInfo{ConfigID: 7, Model: "tool-model", ContextWindow: 128000, MaxOutput: 16384, SupportsTools: true}
	if info != want {
		t.Fatalf("info = %#v, want %#v", info, want)
	}

	info, err = svc.ResolveModel(context.Background(), "")
	if err != nil {
		t.Fatalf("ResolveModel default: %v", err)
	}
	if info.Model != "fake-llm" || info.ContextWindow != 64000 || info.MaxOutput != 8192 || info.SupportsTools {
		t.Fatalf("default info = %#v", info)
	}
}

func TestChatWithoutDefaultConfigIsAIUnavailable(t *testing.T) {
	gateway := &fakeChatGateway{}
	svc, repo := newChatTestService(t, gateway, nil)
	repo.configs[9] = chatTestConfig(9, false)
	repo.models[11] = chatTestModel(11, 9, "fake-llm", 64000, 8192)

	_, err := svc.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
	apiErr := apiError(t, err)
	if apiErr.Code != "ai_unavailable" || apiErr.Status != 503 || !apiErr.Retryable {
		t.Fatalf("error = %#v", apiErr)
	}
	if gateway.callCount() != 0 {
		t.Fatalf("gateway calls = %d, want 0", gateway.callCount())
	}
}

func TestChatWithDisabledDefaultConfigIsAIUnavailable(t *testing.T) {
	gateway := &fakeChatGateway{}
	svc, repo := newChatTestService(t, gateway, nil)
	cfg := chatTestConfig(7, true)
	cfg.Status = llm.StatusDisabled
	repo.configs[7] = cfg
	repo.models[11] = chatTestModel(11, 7, "fake-llm", 64000, 8192)

	if _, err := svc.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}}); err == nil {
		t.Fatal("Chat succeeded with a disabled default config")
	} else if apiErr := apiError(t, err); apiErr.Code != "ai_unavailable" {
		t.Fatalf("error = %#v", apiErr)
	}
}

func TestChatWithoutCatalogModelsIsAIUnavailable(t *testing.T) {
	gateway := &fakeChatGateway{}
	svc, repo := newChatTestService(t, gateway, nil)
	repo.configs[7] = chatTestConfig(7, true)

	if _, err := svc.ResolveModel(context.Background(), ""); err == nil {
		t.Fatal("ResolveModel succeeded without catalog models")
	} else if apiErr := apiError(t, err); apiErr.Code != "ai_unavailable" {
		t.Fatalf("error = %#v", apiErr)
	}
}

func TestChatUnknownRequestedModelIsAIUnavailable(t *testing.T) {
	gateway := &fakeChatGateway{}
	svc, repo := newChatTestService(t, gateway, nil)
	seedSingleConfig(t, repo)

	if _, err := svc.Chat(context.Background(), ChatRequest{Model: "missing-model", Messages: []ChatMessage{{Role: "user", Content: "hi"}}}); err == nil {
		t.Fatal("Chat succeeded with an unknown model")
	} else if apiErr := apiError(t, err); apiErr.Code != "ai_unavailable" {
		t.Fatalf("error = %#v", apiErr)
	}
	if gateway.callCount() != 0 {
		t.Fatalf("gateway calls = %d, want 0", gateway.callCount())
	}
}

func TestChatWithoutGatewayIsAIUnavailable(t *testing.T) {
	svc, repo := newChatTestService(t, nil, nil)
	seedSingleConfig(t, repo)

	if _, err := svc.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}}); err == nil {
		t.Fatal("Chat succeeded without a chat gateway")
	} else if apiErr := apiError(t, err); apiErr.Code != "ai_unavailable" {
		t.Fatalf("error = %#v", apiErr)
	}
}

func TestChatAppliesSettingsTimeoutAndRetries(t *testing.T) {
	gateway := &fakeChatGateway{response: ChatResponse{Content: "ok"}}
	settings := &fakeChatSettings{values: map[string]int64{
		"llm.request_timeout_seconds": 25,
		"llm.max_retries":             4,
	}}
	svc, repo := newChatTestService(t, gateway, settings)
	seedSingleConfig(t, repo)

	if _, err := svc.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	call := gateway.lastCall(t)
	if call.cfg.TimeoutSeconds != 25 || call.cfg.MaxRetries != 4 {
		t.Fatalf("timeout/retries = %d/%d, want settings values", call.cfg.TimeoutSeconds, call.cfg.MaxRetries)
	}
}

func TestChatFallsBackToConfigTimeoutAndRetriesWhenSettingsUnavailable(t *testing.T) {
	gateway := &fakeChatGateway{response: ChatResponse{Content: "ok"}}
	svc, repo := newChatTestService(t, gateway, &fakeChatSettings{err: errors.New("settings unavailable")})
	seedSingleConfig(t, repo)

	if _, err := svc.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	call := gateway.lastCall(t)
	if call.cfg.TimeoutSeconds != 45 || call.cfg.MaxRetries != 1 {
		t.Fatalf("timeout/retries = %d/%d, want config fallback", call.cfg.TimeoutSeconds, call.cfg.MaxRetries)
	}
}

func TestChatMapsContextOverflowError(t *testing.T) {
	gateway := &fakeChatGateway{err: fmt.Errorf("upstream 400: %w", ErrContextOverflow)}
	svc, repo := newChatTestService(t, gateway, nil)
	seedSingleConfig(t, repo)

	_, err := svc.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
	apiErr := apiError(t, err)
	if apiErr.Code != "context_overflow" || apiErr.Status != 503 {
		t.Fatalf("error = %#v", apiErr)
	}
	if !IsContextOverflow(err) {
		t.Fatal("IsContextOverflow = false")
	}
}

func TestChatGatewayFailureIsRedactedAIUnavailable(t *testing.T) {
	gateway := &fakeChatGateway{err: errors.New("provider rejected " + chatTestKey)}
	svc, repo := newChatTestService(t, gateway, nil)
	seedSingleConfig(t, repo)

	_, err := svc.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
	apiErr := apiError(t, err)
	if apiErr.Code != "ai_unavailable" || !apiErr.Retryable {
		t.Fatalf("error = %#v", apiErr)
	}
	assertNoSecret(t, apiErr, chatTestKey)
	if IsContextOverflow(err) {
		t.Fatal("generic failure classified as context overflow")
	}
}

func TestChatDefaultMaxOutputWhenCatalogOmitsIt(t *testing.T) {
	gateway := &fakeChatGateway{response: ChatResponse{Content: "ok"}}
	svc, repo := newChatTestService(t, gateway, nil)
	repo.configs[7] = chatTestConfig(7, true)
	repo.models[11] = chatTestModel(11, 7, "bare-model", 0, 0)

	if _, err := svc.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	call := gateway.lastCall(t)
	if call.req.MaxOutputTokens != DefaultChatMaxOutputTokens {
		t.Fatalf("max output tokens = %d, want %d", call.req.MaxOutputTokens, DefaultChatMaxOutputTokens)
	}
}
