package codeintelllm

import (
	"context"
	"errors"
	"strings"
	"testing"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	llm "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
)

// fakeConfigSource replays the default LLM configuration, or its absence.
type fakeConfigSource struct {
	cfg   llm.Config
	err   error
	calls int
}

// GetDefault implements ConfigSource.
func (s *fakeConfigSource) GetDefault(context.Context) (llm.Config, error) {
	s.calls++
	return s.cfg, s.err
}

// fakeClient records chat requests and replays a fixed response.
type fakeClient struct {
	requests []llmapp.ChatRequest
	response llmapp.ChatResponse
	err      error
}

// Chat implements llmapp.Client.
func (c *fakeClient) Chat(_ context.Context, req llmapp.ChatRequest) (llmapp.ChatResponse, error) {
	c.requests = append(c.requests, req)
	if c.err != nil {
		return llmapp.ChatResponse{}, c.err
	}
	return c.response, nil
}

// ChatStream implements llmapp.Client; codeintel only uses non-streaming Chat.
func (c *fakeClient) ChatStream(context.Context, llmapp.ChatRequest) (<-chan llmapp.StreamEvent, error) {
	return nil, errors.New("codeintel: ChatStream must not be called")
}

// ListModels implements llmapp.Client.
func (c *fakeClient) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}

// chatConfig is a usable chat_completions default configuration.
func chatConfig() llm.Config {
	return llm.Config{
		ID:        1,
		Name:      "default",
		Protocol:  llm.ProtocolChat,
		BaseURL:   "http://llm.invalid",
		Model:     "deepseek-flash",
		APIKey:    "sk-test",
		IsDefault: true,
	}
}

// newTestGateway builds a gateway over src and client.
func newTestGateway(t *testing.T, src ConfigSource, client llmapp.Client) *Gateway {
	t.Helper()
	gateway, err := NewLLMGateway(LLMGatewayConfig{
		ConfigService: src,
		ClientFactory: func(llm.Config) llmapp.Client { return client },
	})
	if err != nil {
		t.Fatalf("NewLLMGateway() error = %v", err)
	}
	return gateway
}

// TestGatewayRequiresDefaultConfig proves a missing default configuration makes
// the gateway unavailable so the caller can degrade the analysis instead of
// retrying to the timeout (design spec §6.2).
func TestGatewayRequiresDefaultConfig(t *testing.T) {
	_, err := NewLLMGateway(LLMGatewayConfig{
		ConfigService: &fakeConfigSource{err: llm.ErrConfigNotFound},
		ClientFactory: func(llm.Config) llmapp.Client { return &fakeClient{} },
	})
	if err == nil {
		t.Fatal("NewLLMGateway() error = nil, want error")
	}
	if !errors.Is(err, codeintelapp.ErrLLMNotConfigured) {
		t.Fatalf("errors.Is(err, ErrLLMNotConfigured) = false (err = %v)", err)
	}
	if !errors.Is(err, llm.ErrConfigNotFound) {
		t.Fatalf("errors.Is(err, llm.ErrConfigNotFound) = false (err = %v)", err)
	}
	if strings.Contains(err.Error(), "sk-") {
		t.Fatalf("gateway error leaks a key: %v", err)
	}

	if _, err := NewLLMGateway(LLMGatewayConfig{}); !errors.Is(err, codeintelapp.ErrLLMNotConfigured) {
		t.Fatalf("NewLLMGateway(zero config) error = %v, want ErrLLMNotConfigured", err)
	}
}

// TestGatewayModelAndCompleteCallShape proves the adapter exposes the
// configured model and calls Chat with a system + user chat-completions
// request.
func TestGatewayModelAndCompleteCallShape(t *testing.T) {
	cfg := chatConfig()
	response := llmapp.ChatResponse{Text: `{"changeSummary":"x","facts":[],"edges":[]}`}
	client := &fakeClient{response: response}
	source := &fakeConfigSource{cfg: cfg}

	var built []llm.Config
	gateway, err := NewLLMGateway(LLMGatewayConfig{
		ConfigService: source,
		ClientFactory: func(got llm.Config) llmapp.Client {
			built = append(built, got)
			return client
		},
	})
	if err != nil {
		t.Fatalf("NewLLMGateway() error = %v", err)
	}
	if gateway.Model() != "deepseek-flash" {
		t.Fatalf("Model() = %q, want deepseek-flash", gateway.Model())
	}
	if len(built) != 1 || built[0].Model != "deepseek-flash" {
		t.Fatalf("ClientFactory calls = %+v, want one call with the default config", built)
	}

	text, err := gateway.Complete(context.Background(), "SYSTEM-PROMPT", "USER-PROMPT")
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if text != response.Text {
		t.Fatalf("Complete() = %q, want %q", text, response.Text)
	}
	if len(client.requests) != 1 {
		t.Fatalf("Chat calls = %d, want 1", len(client.requests))
	}
	request := client.requests[0]
	if request.Stream {
		t.Fatal("Chat request must not stream")
	}
	if request.Model != "deepseek-flash" {
		t.Fatalf("Chat request model = %q", request.Model)
	}
	if len(request.Messages) != 2 {
		t.Fatalf("Chat request messages = %d, want system + user", len(request.Messages))
	}
	system, user := request.Messages[0], request.Messages[1]
	if system.Role != "system" || len(system.Content) != 1 || system.Content[0].Type != "text" || system.Content[0].Text != "SYSTEM-PROMPT" {
		t.Fatalf("system message = %+v", system)
	}
	if user.Role != "user" || len(user.Content) != 1 || user.Content[0].Type != "text" || user.Content[0].Text != "USER-PROMPT" {
		t.Fatalf("user message = %+v", user)
	}
}

// TestGatewayRejectsUnsupportedProtocol proves a default configuration that
// cannot serve chat completions is treated as unavailable (design spec §6.2:
// 协议不可用 -> degraded).
func TestGatewayRejectsUnsupportedProtocol(t *testing.T) {
	source := &fakeConfigSource{cfg: llm.Config{
		Model:    "deepseek-flash",
		Protocol: llm.ProtocolResponses,
	}}

	_, err := NewLLMGateway(LLMGatewayConfig{
		ConfigService: source,
		ClientFactory: func(llm.Config) llmapp.Client { return &fakeClient{} },
	})
	if !errors.Is(err, codeintelapp.ErrLLMNotConfigured) {
		t.Fatalf("NewLLMGateway(responses) error = %v, want ErrLLMNotConfigured", err)
	}
}

// TestGatewayPropagatesClientErrorWithoutPrompt proves upstream errors are
// returned without embedding the prompt or code content.
func TestGatewayPropagatesClientErrorWithoutPrompt(t *testing.T) {
	client := &fakeClient{err: errors.New("upstream unavailable")}
	gateway := newTestGateway(t, &fakeConfigSource{cfg: chatConfig()}, client)

	_, err := gateway.Complete(context.Background(), "SECRET SYSTEM PROMPT", "SECRET USER CONTENT")
	if err == nil {
		t.Fatal("Complete() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "upstream unavailable") {
		t.Fatalf("Complete() error = %v, want the wrapped cause", err)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("Complete() error leaks prompt content: %v", err)
	}
}
