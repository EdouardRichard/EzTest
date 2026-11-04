package llmapp_test

// P4-T4 (R42/D24): the LLM application layer classifies upstream failures
// through application-level sentinels and never switches models. These tests
// exercise the real service over the real infrastructure adapters so the
// retry/classification contract is proven end to end (spec §5.6/§7.4).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	domain "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	llminfra "github.com/EdouardRichard/EzTest/backend/internal/llm/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

/* ------------------------------ fakes ----------------------------------- */

// errorRepo is the minimal llm.Repository used by the classification tests: a
// single default configuration with an explicit default model.
type errorRepo struct {
	cfg    domain.Config
	models []domain.Model
}

func (r *errorRepo) ListConfigs(context.Context) ([]domain.Config, error) {
	return []domain.Config{r.cfg}, nil
}

func (r *errorRepo) GetConfig(_ context.Context, id int64) (domain.Config, error) {
	if id == r.cfg.ID {
		return r.cfg, nil
	}
	return domain.Config{}, domain.ErrConfigNotFound
}

func (r *errorRepo) CreateConfig(context.Context, domain.Config) (domain.Config, error) {
	return domain.Config{}, errors.New("not used")
}

func (r *errorRepo) UpdateConfig(context.Context, domain.Config) (domain.Config, error) {
	return domain.Config{}, errors.New("not used")
}

func (r *errorRepo) DeleteConfig(context.Context, int64) error { return errors.New("not used") }

func (r *errorRepo) ListModels(context.Context) ([]domain.Model, error) { return r.models, nil }

func (r *errorRepo) ListModelsByConfig(_ context.Context, configID int64) ([]domain.Model, error) {
	if configID != r.cfg.ID {
		return nil, nil
	}
	return r.models, nil
}

func (r *errorRepo) UpsertModels(context.Context, int64, []domain.Model, time.Time) ([]domain.Model, error) {
	return nil, errors.New("not used")
}

func (r *errorRepo) InsertModel(context.Context, domain.Model) (domain.Model, error) {
	return domain.Model{}, errors.New("not used")
}

func (r *errorRepo) UpdateModel(context.Context, domain.Model) (domain.Model, error) {
	return domain.Model{}, errors.New("not used")
}

func (r *errorRepo) DeleteModel(context.Context, int64) error { return errors.New("not used") }

type errorIDs struct {
	mu   sync.Mutex
	next int64
}

func (g *errorIDs) Next() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next++
	return g.next
}

type errorClock struct{ at time.Time }

func (c errorClock) Now() time.Time { return c.at }

// stubChatClient is the ChatClient port fake used by the classification-only
// tests (no HTTP upstream involved).
type stubChatClient struct {
	mu       sync.Mutex
	calls    int
	response llmapp.ChatResponse
	err      error
}

func (f *stubChatClient) Chat(context.Context, domain.Config, llmapp.ChatRequest) (llmapp.ChatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.response, f.err
}

func (f *stubChatClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

/* ------------------------------ helpers --------------------------------- */

func errorTestConfig(protocol domain.Protocol, baseURL string) domain.Config {
	return domain.Config{
		ID:             7,
		Name:           "primary",
		Provider:       domain.ProviderOpenAI,
		Protocol:       string(protocol),
		BaseURL:        baseURL,
		APIKey:         "sk-classify-secret",
		Headers:        map[string]string{},
		TimeoutSeconds: 5,
		MaxRetries:     2,
		IsDefault:      true,
		Status:         domain.StatusActive,
	}
}

func errorTestModel(id int64, model string, supportsReasoning bool) domain.Model {
	return domain.Model{
		ID:            id,
		ConfigID:      7,
		Model:         model,
		DisplayName:   model,
		ContextWindow: 64000,
		MaxOutput:     4096,
		Source:        domain.SourceCatalog,
		Status:        domain.ModelStatusActive,
		Capabilities:  domain.Capabilities{ContextWindow: 64000, SupportsReasoning: supportsReasoning},
	}
}

func newErrorTestService(cfg domain.Config, models []domain.Model, chat llmapp.ChatClient) *llmapp.Service {
	return llmapp.New(
		&errorRepo{cfg: cfg, models: models},
		nil,
		&errorIDs{},
		errorClock{at: time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)},
		nil,
		nil,
		llmapp.WithChatClient(chat),
	)
}

func platformError(t *testing.T, err error) *platform.Error {
	t.Helper()
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v (%T) is not a platform error", err, err)
	}
	return apiErr
}

// countingServer returns an httptest server that answers every request with
// status/body and records the decoded "model" field of each request.
func countingServer(t *testing.T, status int, body string, requests *[]string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var payload struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(raw, &payload)
		mu.Lock()
		*requests = append(*requests, payload.Model)
		mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

/* ------------------------------- tests ---------------------------------- */

// TestNoAutoFailover pins D24: a 400 capability/protocol rejection is never
// retried and never triggers a different model; a 429 is retried exactly
// cfg.MaxRetries times against the same model.
func TestNoAutoFailover(t *testing.T) {
	t.Run("400 capability is not retried and never fails over", func(t *testing.T) {
		requests := make([]string, 0, 4)
		server := countingServer(t, http.StatusBadRequest,
			`{"error":{"message":"this model does not support tools","code":"invalid_request_error"}}`, &requests)
		defer server.Close()

		cfg := errorTestConfig(domain.ProtocolOpenAIChatCompletions, server.URL)
		models := []domain.Model{
			errorTestModel(11, "capability-model", false),
			errorTestModel(12, "fallback-model", false),
		}
		cfg.DefaultModelID = 11
		service := newErrorTestService(cfg, models, llminfra.NewClient())

		_, err := service.Chat(context.Background(), llmapp.ChatRequest{
			Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
		})
		if err == nil {
			t.Fatal("Chat succeeded on an HTTP 400 capability rejection")
		}
		if got := len(requests); got != 1 {
			t.Fatalf("upstream calls = %d, want exactly 1 (no retry, no failover); models=%v", got, requests)
		}
		if requests[0] != "capability-model" {
			t.Fatalf("requested model = %q, want the resolved model (auto failover is forbidden, D24)", requests[0])
		}
		apiErr := platformError(t, err)
		if apiErr.Status != http.StatusBadRequest || apiErr.Retryable {
			t.Fatalf("error = %#v, want a non-retryable 400", apiErr)
		}
		if apiErr.Code != llmapp.CodeLLMCapabilityMismatch {
			t.Fatalf("code = %q, want %q", apiErr.Code, llmapp.CodeLLMCapabilityMismatch)
		}
		if !llmapp.IsProtocol(err) || llmapp.IsTransient(err) {
			t.Fatalf("classification = protocol:%v transient:%v, want protocol only",
				llmapp.IsProtocol(err), llmapp.IsTransient(err))
		}
		if !strings.Contains(apiErr.Message, "检查模型能力/协议") {
			t.Fatalf("message = %q, want an actionable model/protocol hint", apiErr.Message)
		}
	})

	t.Run("429 is retried twice against the same model", func(t *testing.T) {
		requests := make([]string, 0, 4)
		server := countingServer(t, http.StatusTooManyRequests,
			`{"error":{"message":"rate limited"}}`, &requests)
		defer server.Close()

		cfg := errorTestConfig(domain.ProtocolOpenAIChatCompletions, server.URL)
		models := []domain.Model{
			errorTestModel(11, "capability-model", false),
			errorTestModel(12, "fallback-model", false),
		}
		cfg.DefaultModelID = 11
		service := newErrorTestService(cfg, models, llminfra.NewClient())

		_, err := service.Chat(context.Background(), llmapp.ChatRequest{
			Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
		})
		if err == nil {
			t.Fatal("Chat succeeded on HTTP 429")
		}
		if got := len(requests); got != 3 {
			t.Fatalf("upstream calls = %d, want 3 (1 attempt + 2 retries)", got)
		}
		for _, model := range requests {
			if model != "capability-model" {
				t.Fatalf("retry used model %q, want the same model (auto failover is forbidden, D24)", model)
			}
		}
		apiErr := platformError(t, err)
		if apiErr.Status != http.StatusServiceUnavailable || !apiErr.Retryable {
			t.Fatalf("error = %#v, want a retryable 503", apiErr)
		}
		if apiErr.Code != llmapp.CodeAIUnavailable {
			t.Fatalf("code = %q, want %q", apiErr.Code, llmapp.CodeAIUnavailable)
		}
		if !llmapp.IsTransient(err) || llmapp.IsProtocol(err) {
			t.Fatalf("classification = protocol:%v transient:%v, want transient only",
				llmapp.IsProtocol(err), llmapp.IsTransient(err))
		}
	})
}

// TestProtocolClassifiedThroughService pins the Responses 4xx path: it used to
// surface as a generic retryable 503 because the application could not see the
// infrastructure ProtocolError (R25). It must now be a non-retryable typed 400.
func TestProtocolClassifiedThroughService(t *testing.T) {
	requests := make([]string, 0, 4)
	server := countingServer(t, http.StatusBadRequest,
		`{"error":{"message":"invalid parameter: temperature","code":"invalid_request_error"}}`, &requests)
	defer server.Close()

	cfg := errorTestConfig(domain.ProtocolOpenAIResponses, server.URL)
	models := []domain.Model{errorTestModel(11, "responses-model", false)}
	cfg.DefaultModelID = 11
	service := newErrorTestService(cfg, models, llminfra.NewClient())

	_, err := service.Chat(context.Background(), llmapp.ChatRequest{
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("Chat succeeded on a Responses HTTP 400")
	}
	if got := len(requests); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
	apiErr := platformError(t, err)
	if apiErr.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (not a generic 503): %#v", apiErr.Status, apiErr)
	}
	if apiErr.Retryable {
		t.Fatalf("retryable = true, want false for a protocol error")
	}
	if apiErr.Code != llmapp.CodeLLMProtocolError {
		t.Fatalf("code = %q, want %q", apiErr.Code, llmapp.CodeLLMProtocolError)
	}
	if !llmapp.IsProtocol(err) {
		t.Fatal("IsProtocol = false, want true")
	}
	if !strings.Contains(apiErr.Message, "invalid parameter: temperature") {
		t.Fatalf("message = %q, want the redacted provider message", apiErr.Message)
	}
	assertNoClassificationSecret(t, apiErr, "sk-classify-secret")
}

// TestProtocolSubclassesClassifiedThroughService pins the 4xx sub-classes: an
// unknown model stays a non-retryable 400 with the invalid_model code and a
// rejected credential is reported as an auth failure, never as a retryable
// outage (spec §5.6/§7.4).
func TestProtocolSubclassesClassifiedThroughService(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantCode string
	}{
		{
			name:     "unknown model",
			status:   http.StatusNotFound,
			body:     `{"error":{"message":"model \"gpt-missing\" does not exist"}}`,
			wantCode: llmapp.CodeInvalidModel,
		},
		{
			name:     "rejected credential",
			status:   http.StatusUnauthorized,
			body:     `{"error":{"message":"invalid api key"}}`,
			wantCode: llmapp.CodeLLMAuthFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requests := make([]string, 0, 2)
			server := countingServer(t, tc.status, tc.body, &requests)
			defer server.Close()

			cfg := errorTestConfig(domain.ProtocolOpenAIResponses, server.URL)
			models := []domain.Model{errorTestModel(11, "responses-model", false)}
			cfg.DefaultModelID = 11
			service := newErrorTestService(cfg, models, llminfra.NewClient())

			_, err := service.Chat(context.Background(), llmapp.ChatRequest{
				Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
			})
			if err == nil {
				t.Fatalf("Chat succeeded on HTTP %d", tc.status)
			}
			if got := len(requests); got != 1 {
				t.Fatalf("upstream calls = %d, want 1", got)
			}
			apiErr := platformError(t, err)
			if apiErr.Status != http.StatusBadRequest || apiErr.Retryable {
				t.Fatalf("error = %#v, want a non-retryable 400", apiErr)
			}
			if apiErr.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", apiErr.Code, tc.wantCode)
			}
			if !llmapp.IsProtocol(err) || llmapp.IsTransient(err) {
				t.Fatalf("classification = protocol:%v transient:%v", llmapp.IsProtocol(err), llmapp.IsTransient(err))
			}
		})
	}
}

// TestTransientClassifiedThroughService pins the opposite class: a Responses
// 429 is retried cfg.MaxRetries times and maps to the retryable ai_unavailable.
func TestTransientClassifiedThroughService(t *testing.T) {
	requests := make([]string, 0, 4)
	server := countingServer(t, http.StatusTooManyRequests,
		`{"error":{"message":"slow down"}}`, &requests)
	defer server.Close()

	cfg := errorTestConfig(domain.ProtocolOpenAIResponses, server.URL)
	models := []domain.Model{errorTestModel(11, "responses-model", false)}
	cfg.DefaultModelID = 11
	service := newErrorTestService(cfg, models, llminfra.NewClient())

	_, err := service.Chat(context.Background(), llmapp.ChatRequest{
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("Chat succeeded on a Responses HTTP 429")
	}
	if got := len(requests); got != 3 {
		t.Fatalf("upstream calls = %d, want 3 (1 attempt + 2 configured retries)", got)
	}
	apiErr := platformError(t, err)
	if apiErr.Status != http.StatusServiceUnavailable || !apiErr.Retryable {
		t.Fatalf("error = %#v, want a retryable 503", apiErr)
	}
	if !llmapp.IsTransient(err) {
		t.Fatal("IsTransient = false, want true")
	}
}

// TestErrNotImplementedNonRetryable pins the fail-closed protocol adapter: an
// implemented-in-future capability must not be retried and must not be reported
// as a transient outage.
func TestErrNotImplementedNonRetryable(t *testing.T) {
	gateway := &stubChatClient{err: fmt.Errorf("llm responses streaming is not implemented: %w", llmapp.ErrNotImplemented)}
	cfg := errorTestConfig(domain.ProtocolOpenAIChatCompletions, "http://127.0.0.1:1/v1")
	models := []domain.Model{errorTestModel(11, "fake-llm", false)}
	cfg.DefaultModelID = 11
	service := newErrorTestService(cfg, models, gateway)

	_, err := service.Chat(context.Background(), llmapp.ChatRequest{
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("Chat succeeded on ErrNotImplemented")
	}
	apiErr := platformError(t, err)
	if apiErr.Status != http.StatusBadRequest || apiErr.Retryable {
		t.Fatalf("error = %#v, want a non-retryable 400", apiErr)
	}
	if apiErr.Code != llmapp.CodeLLMProtocolError {
		t.Fatalf("code = %q, want %q", apiErr.Code, llmapp.CodeLLMProtocolError)
	}
	if !llmapp.IsProtocol(err) {
		t.Fatal("IsProtocol = false, want true")
	}
	if gateway.callCount() != 1 {
		t.Fatalf("gateway calls = %d, want 1", gateway.callCount())
	}
}

// TestChatRejectsReasoningWithoutCapability pins R42: the direct Chat path used
// by the summarizer must apply the same reasoning gate as ResolveModel, so an
// auto+high config on a non-reasoning model cannot send reasoning parameters.
func TestChatRejectsReasoningWithoutCapability(t *testing.T) {
	gateway := &stubChatClient{response: llmapp.ChatResponse{Content: "ok"}}
	cfg := errorTestConfig(domain.ProtocolOpenAIChatCompletions, "http://127.0.0.1:1/v1")
	cfg.ThinkingType = domain.ThinkingTypeAuto
	cfg.ReasoningEffort = domain.ReasoningEffortHigh
	models := []domain.Model{errorTestModel(11, "plain-model", false)}
	cfg.DefaultModelID = 11
	service := newErrorTestService(cfg, models, gateway)

	_, err := service.Chat(context.Background(), llmapp.ChatRequest{
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("Chat accepted a non-reasoning model with auto+high reasoning")
	}
	apiErr := platformError(t, err)
	if apiErr.Status != http.StatusBadRequest || apiErr.Retryable {
		t.Fatalf("error = %#v, want a non-retryable 400", apiErr)
	}
	if apiErr.Code != llmapp.CodeLLMCapabilityMismatch {
		t.Fatalf("code = %q, want %q", apiErr.Code, llmapp.CodeLLMCapabilityMismatch)
	}
	if gateway.callCount() != 0 {
		t.Fatalf("gateway calls = %d, want 0 (rejected before sending reasoning params)", gateway.callCount())
	}

	// Negative control: a reasoning-capable model keeps working on the same path.
	reasoningCfg := cfg
	reasoningCfg.DefaultModelID = 21
	reasoningModels := []domain.Model{errorTestModel(21, "reasoning-model", true)}
	reasoningService := newErrorTestService(reasoningCfg, reasoningModels, gateway)
	if _, err := reasoningService.Chat(context.Background(), llmapp.ChatRequest{
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("Chat with a reasoning-capable model: %v", err)
	}
}

func assertNoClassificationSecret(t *testing.T, value any, secret string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("secret %q leaked in %s", secret, raw)
	}
}
