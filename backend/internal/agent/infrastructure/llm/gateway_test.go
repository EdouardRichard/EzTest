package agentllm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	llm "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// fakeConfigSource returns a fixed default LLM configuration.
type fakeConfigSource struct {
	cfg   llm.Config
	err   error
	calls int
}

func (s *fakeConfigSource) GetDefault(context.Context) (llm.Config, error) {
	s.calls++
	return s.cfg, s.err
}

// fakeLLMClient records stream requests and replays a fixed event list. It is a
// faithful llmapp.Client for the adapter tests.
type fakeLLMClient struct {
	requests  []llmapp.ChatRequest
	streamErr error
	events    []llmapp.StreamEvent
}

func (c *fakeLLMClient) Chat(context.Context, llmapp.ChatRequest) (llmapp.ChatResponse, error) {
	return llmapp.ChatResponse{}, nil
}

func (c *fakeLLMClient) ChatStream(_ context.Context, req llmapp.ChatRequest) (<-chan llmapp.StreamEvent, error) {
	c.requests = append(c.requests, req)
	if c.streamErr != nil {
		return nil, c.streamErr
	}
	events := make(chan llmapp.StreamEvent, len(c.events))
	for _, event := range c.events {
		events <- event
	}
	close(events)
	return events, nil
}

func (c *fakeLLMClient) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}

// chatConfig is a valid default chat_completions configuration.
func chatConfig() llm.Config {
	return llm.Config{
		ID:        1,
		Name:      "default",
		Protocol:  llm.ProtocolChat,
		Model:     "deepseek-flash",
		APIKey:    "sk-test",
		IsDefault: true,
	}
}

// int64Ptr returns a pointer to v, used for the catalog's optional fields.
func int64Ptr(v int64) *int64 { return &v }

// prepare is a test helper: it builds the gateway and calls Prepare.
func prepare(t *testing.T, src ConfigSource, client llmapp.Client, override string) (agentapp.LLMCaller, error) {
	t.Helper()
	gateway := NewGateway(src, func(llm.Config) llmapp.Client { return client })
	return gateway.Prepare(context.Background(), override)
}

// drain collects every event from a stream.
func drain(t *testing.T, stream <-chan agentapp.LLMStreamEvent) []agentapp.LLMStreamEvent {
	t.Helper()
	var events []agentapp.LLMStreamEvent
	for event := range stream {
		events = append(events, event)
	}
	return events
}

// fakeImageResolver records every reference it is asked to resolve and returns
// the configured transient data URL.
type fakeImageResolver struct {
	mu      sync.Mutex
	refs    []string
	dataURL string
	err     error
}

func (r *fakeImageResolver) ResolveImage(_ context.Context, ref string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refs = append(r.refs, ref)
	if r.err != nil {
		return "", r.err
	}
	return r.dataURL, nil
}

func (r *fakeImageResolver) resolvedRefs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.refs...)
}

// imageRefRequest is the multimodal request the gateway tests start from.
func imageRefRequest() agentapp.LLMRequest {
	return agentapp.LLMRequest{
		Model: "deepseek-flash",
		Messages: []agentapp.LLMMessage{{
			Role: "user",
			Parts: []agentdomain.Part{
				{Type: "text", Text: "look"},
				{Type: "image_url", ImageRef: "attachment:42"},
			},
		}},
	}
}

func TestGatewayResolvesImageRefs(t *testing.T) {
	const dataURL = "data:image/png;base64,iVBORw0KGgo="
	resolver := &fakeImageResolver{dataURL: dataURL}
	client := &fakeLLMClient{events: []llmapp.StreamEvent{{Done: true}}}
	cfg := chatConfig()
	cfg.MultimodalEnabled = true
	caller, err := NewGateway(&fakeConfigSource{cfg: cfg}, func(llm.Config) llmapp.Client {
		return client
	}, resolver).Prepare(context.Background(), "")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	request := imageRefRequest()
	stream, err := caller.ChatStream(context.Background(), request)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	drain(t, stream)

	if refs := resolver.resolvedRefs(); len(refs) != 1 || refs[0] != "attachment:42" {
		t.Fatalf("resolved refs=%+v want [attachment:42]", refs)
	}
	if len(client.requests) != 1 {
		t.Fatalf("upstream requests=%d want 1", len(client.requests))
	}
	parts := client.requests[0].Messages[0].Content
	if len(parts) != 2 {
		t.Fatalf("upstream parts=%+v want text + image", parts)
	}
	if parts[0].Type != "text" || parts[0].Text != "look" {
		t.Fatalf("text part=%+v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL != dataURL {
		t.Fatalf("image part=%+v want resolved data URL", parts[1])
	}
	// The caller's request is not mutated: persisted content keeps only the
	// reference (D30), the data URL lives only inside this call.
	if request.Messages[0].Parts[1].ImageRef != "attachment:42" ||
		request.Messages[0].Parts[1].ImageURL != "" {
		t.Fatalf("ChatStream mutated the caller's request: %+v", request.Messages[0].Parts[1])
	}
}

func TestGatewayRejectsImagesWhenMultimodalDisabled(t *testing.T) {
	resolver := &fakeImageResolver{dataURL: "data:image/png;base64,AAAA"}
	client := &fakeLLMClient{events: []llmapp.StreamEvent{{Done: true}}}
	cfg := chatConfig() // MultimodalEnabled defaults to false.
	caller, err := NewGateway(&fakeConfigSource{cfg: cfg}, func(llm.Config) llmapp.Client {
		return client
	}, resolver).Prepare(context.Background(), "")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	_, err = caller.ChatStream(context.Background(), imageRefRequest())
	if err == nil {
		t.Fatal("ChatStream accepted an image reference while multimodal is disabled")
	}
	var coded agentapp.CodedError
	if !errors.As(err, &coded) {
		t.Fatalf("err=%v does not implement agentapp.CodedError", err)
	}
	if coded.ErrorCode() != platform.CodeLLMBadRequest {
		t.Fatalf("ErrorCode()=%q want %q", coded.ErrorCode(), platform.CodeLLMBadRequest)
	}
	if refs := resolver.resolvedRefs(); len(refs) != 0 {
		t.Fatalf("the resolver was called with multimodal disabled: %+v", refs)
	}
	if len(client.requests) != 0 {
		t.Fatalf("an image request reached the upstream with multimodal disabled: %+v", client.requests)
	}
}

func TestGatewayWithoutResolverFailsImageRef(t *testing.T) {
	client := &fakeLLMClient{events: []llmapp.StreamEvent{{Done: true}}}
	cfg := chatConfig()
	cfg.MultimodalEnabled = true
	caller, err := NewGateway(&fakeConfigSource{cfg: cfg}, func(llm.Config) llmapp.Client {
		return client
	}).Prepare(context.Background(), "")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	_, err = caller.ChatStream(context.Background(), imageRefRequest())
	if err == nil {
		t.Fatal("ChatStream accepted an image reference without a resolver")
	}
	if !strings.Contains(err.Error(), "resolver") {
		t.Fatalf("err=%q is not a readable resolver error", err)
	}
	if len(client.requests) != 0 {
		t.Fatalf("an unresolved image request reached the upstream: %+v", client.requests)
	}

	// The P1 text-only path is unchanged: no resolver is fine without images.
	textOnly := agentapp.LLMRequest{
		Model: "deepseek-flash",
		Messages: []agentapp.LLMMessage{{
			Role:  "user",
			Parts: []agentdomain.Part{{Type: "text", Text: "hello"}},
		}},
	}
	stream, err := caller.ChatStream(context.Background(), textOnly)
	if err != nil {
		t.Fatalf("text-only ChatStream without a resolver: %v", err)
	}
	drain(t, stream)
	if len(client.requests) != 1 {
		t.Fatalf("upstream requests=%d want 1 after the text-only call", len(client.requests))
	}
}

func TestPrepareRejectsUnavailableDefault(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"missing default", llm.ErrConfigNotFound},
		{"store failure", errors.New("database is down")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeConfigSource{err: tc.err}
			gateway := NewGateway(src, func(llm.Config) llmapp.Client {
				t.Fatal("a client must not be built when the default configuration is unavailable")
				return nil
			})
			_, err := gateway.Prepare(context.Background(), "")
			if !errors.Is(err, agentdomain.ErrLLMNotConfigured) {
				t.Fatalf("Prepare err=%v want agentdomain.ErrLLMNotConfigured", err)
			}
		})
	}
}

func TestPrepareRejectsResponsesProtocol(t *testing.T) {
	cfg := chatConfig()
	cfg.Protocol = llm.ProtocolResponses
	gateway := NewGateway(&fakeConfigSource{cfg: cfg}, func(llm.Config) llmapp.Client {
		t.Fatal("a client must not be built for the responses protocol")
		return nil
	})
	_, err := gateway.Prepare(context.Background(), "")
	if !errors.Is(err, agentdomain.ErrToolsUnsupported) {
		t.Fatalf("Prepare err=%v want agentdomain.ErrToolsUnsupported", err)
	}
}

func TestPrepareResolvesModelAndContextWindow(t *testing.T) {
	catalog := []llm.ModelInfo{
		{ID: "deepseek-flash", ContextWindow: int64Ptr(1000000)},
		{ID: "deepseek-reasoner", ContextWindow: int64Ptr(65536)},
		{ID: "no-window"},
	}
	cases := []struct {
		name       string
		override   string
		catalog    []llm.ModelInfo
		wantModel  string
		wantWindow int
	}{
		{
			name:       "configuration model",
			catalog:    catalog,
			wantModel:  "deepseek-flash",
			wantWindow: 1000000,
		},
		{
			name:       "profile model override",
			override:   "deepseek-reasoner",
			catalog:    catalog,
			wantModel:  "deepseek-reasoner",
			wantWindow: 65536,
		},
		{
			name:       "model missing from catalog",
			catalog:    catalog,
			override:   "deepseek-chat",
			wantModel:  "deepseek-chat",
			wantWindow: 100000,
		},
		{
			name:       "catalog entry without a context window",
			catalog:    catalog,
			override:   "no-window",
			wantModel:  "no-window",
			wantWindow: 100000,
		},
		{
			name:       "empty catalog",
			wantModel:  "deepseek-flash",
			wantWindow: 100000,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := chatConfig()
			cfg.ModelCatalog = tc.catalog
			client := &fakeLLMClient{}
			caller, err := prepare(t, &fakeConfigSource{cfg: cfg}, client, tc.override)
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			if caller.Model() != tc.wantModel {
				t.Fatalf("Model()=%q want %q", caller.Model(), tc.wantModel)
			}
			if caller.ContextWindowTokens() != tc.wantWindow {
				t.Fatalf("ContextWindowTokens()=%d want %d", caller.ContextWindowTokens(), tc.wantWindow)
			}
		})
	}
}

func TestChatStreamMapsRequest(t *testing.T) {
	client := &fakeLLMClient{events: []llmapp.StreamEvent{{Done: true}}}
	caller, err := prepare(t, &fakeConfigSource{cfg: chatConfig()}, client, "")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	request := agentapp.LLMRequest{
		Model: "deepseek-flash",
		Messages: []agentapp.LLMMessage{
			{Role: "system", Parts: []agentdomain.Part{{Type: "text", Text: "you are a test agent"}}},
			{Role: "user", Parts: []agentdomain.Part{
				{Type: "text", Text: "hello"},
				{Type: "image_url", ImageURL: "data:image/png;base64,AAAA"},
			}},
			{
				Role:             "assistant",
				ReasoningContent: "I should echo",
				ToolCalls: []agentdomain.ToolCall{{
					ID: "call_1", Name: "test.echo", Arguments: `{"text":"hello"}`,
				}},
			},
			{Role: "tool", Parts: []agentdomain.Part{{Type: "text", Text: "hello"}}, ToolCallID: "call_1"},
		},
		Tools: []agentapp.LLMTool{{
			Name:        "test.echo",
			Description: "Echo text",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		}},
		MaxTokens:       32768,
		Thinking:        "enabled",
		ReasoningEffort: "max",
	}
	stream, err := caller.ChatStream(context.Background(), request)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	drain(t, stream)

	if len(client.requests) != 1 {
		t.Fatalf("upstream requests=%d want 1", len(client.requests))
	}
	got := client.requests[0]
	if !got.Stream {
		t.Fatal("ChatStream must request a streaming completion")
	}
	if got.Model != "deepseek-flash" || got.MaxTokens != 32768 ||
		got.Thinking != "enabled" || got.ReasoningEffort != "max" {
		t.Fatalf("request=%+v", got)
	}
	if len(got.Messages) != 4 {
		t.Fatalf("messages=%d want 4", len(got.Messages))
	}
	if got.Messages[0].Role != "system" || len(got.Messages[0].Content) != 1 ||
		got.Messages[0].Content[0].Text != "you are a test agent" {
		t.Fatalf("system message=%+v", got.Messages[0])
	}
	if len(got.Messages[1].Content) != 2 ||
		got.Messages[1].Content[0].Type != "text" || got.Messages[1].Content[0].Text != "hello" ||
		got.Messages[1].Content[1].Type != "image_url" ||
		got.Messages[1].Content[1].ImageURL != "data:image/png;base64,AAAA" {
		t.Fatalf("user message=%+v", got.Messages[1])
	}
	assistant := got.Messages[2]
	if assistant.ReasoningContent != "I should echo" || len(assistant.ToolCalls) != 1 {
		t.Fatalf("assistant message=%+v", assistant)
	}
	if assistant.ToolCalls[0].ID != "call_1" || assistant.ToolCalls[0].Name != "test_echo" ||
		assistant.ToolCalls[0].Arguments != `{"text":"hello"}` {
		t.Fatalf("assistant tool call=%+v", assistant.ToolCalls[0])
	}
	if got.Messages[3].ToolCallID != "call_1" {
		t.Fatalf("tool message=%+v", got.Messages[3])
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "test_echo" ||
		got.Tools[0].Description != "Echo text" ||
		string(got.Tools[0].Parameters) != `{"type":"object"}` {
		t.Fatalf("tools=%+v", got.Tools)
	}
}

func TestChatStreamMapsEvents(t *testing.T) {
	client := &fakeLLMClient{events: []llmapp.StreamEvent{
		{ReasoningDelta: "thinking..."},
		{Delta: "hello"},
		{
			Done:         true,
			ToolCalls:    []llmapp.ToolCall{{ID: "call_1", Name: "file.read", Arguments: `{"path":"README.md"}`}},
			FinishReason: "tool_calls",
			Usage:        llmapp.Usage{PromptTokens: 10, CompletionTokens: 5, ReasoningTokens: 3, TotalTokens: 18},
		},
	}}
	caller, err := prepare(t, &fakeConfigSource{cfg: chatConfig()}, client, "")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	stream, err := caller.ChatStream(context.Background(), agentapp.LLMRequest{Model: "deepseek-flash"})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	events := drain(t, stream)
	if len(events) != 3 {
		t.Fatalf("events=%d want 3: %+v", len(events), events)
	}
	if events[0].ReasoningDelta != "thinking..." || events[0].Done || events[0].Err != nil {
		t.Fatalf("reasoning event=%+v", events[0])
	}
	if events[1].Delta != "hello" || events[1].Done || events[1].Err != nil {
		t.Fatalf("text event=%+v", events[1])
	}
	done := events[2]
	if !done.Done || done.FinishReason != "tool_calls" {
		t.Fatalf("done event=%+v", done)
	}
	if len(done.ToolCalls) != 1 || done.ToolCalls[0] != (agentdomain.ToolCall{
		ID: "call_1", Name: "file.read", Arguments: `{"path":"README.md"}`,
	}) {
		t.Fatalf("done tool calls=%+v", done.ToolCalls)
	}
	wantUsage := agentdomain.Usage{PromptTokens: 10, CompletionTokens: 5, ReasoningTokens: 3, TotalTokens: 18}
	if done.Usage != wantUsage {
		t.Fatalf("done usage=%+v want %+v", done.Usage, wantUsage)
	}
}

func TestChatStreamSendsWireToolNames(t *testing.T) {
	client := &fakeLLMClient{events: []llmapp.StreamEvent{{Done: true}}}
	caller, err := prepare(t, &fakeConfigSource{cfg: chatConfig()}, client, "")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	stream, err := caller.ChatStream(context.Background(), agentapp.LLMRequest{
		Model: "deepseek-flash",
		Tools: []agentapp.LLMTool{
			{Name: "file.read", Description: "Read a file"},
			{Name: "http.get", Description: "Fetch a URL"},
		},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	drain(t, stream)

	if len(client.requests) != 1 {
		t.Fatalf("upstream requests=%d want 1", len(client.requests))
	}
	tools := client.requests[0].Tools
	if len(tools) != 2 || tools[0].Name != "file_read" || tools[1].Name != "http_get" {
		t.Fatalf("wire tool names=%+v want file_read,http_get", tools)
	}
}

func TestChatStreamSendsWireToolCallHistoryNames(t *testing.T) {
	client := &fakeLLMClient{events: []llmapp.StreamEvent{{Done: true}}}
	caller, err := prepare(t, &fakeConfigSource{cfg: chatConfig()}, client, "")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	stream, err := caller.ChatStream(context.Background(), agentapp.LLMRequest{
		Model: "deepseek-flash",
		Messages: []agentapp.LLMMessage{
			{
				Role: "assistant",
				ToolCalls: []agentdomain.ToolCall{{
					ID: "call_1", Name: "file.read", Arguments: `{"path":"README.md"}`,
				}},
			},
			{Role: "tool", Parts: []agentdomain.Part{{Type: "text", Text: "# EzTest"}}, ToolCallID: "call_1"},
		},
		Tools: []agentapp.LLMTool{{Name: "file.read"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	drain(t, stream)

	if len(client.requests) != 1 {
		t.Fatalf("upstream requests=%d want 1", len(client.requests))
	}
	assistant := client.requests[0].Messages[0]
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].Name != "file_read" {
		t.Fatalf("wire assistant tool calls=%+v want file_read", assistant.ToolCalls)
	}
}

func TestChatStreamMapsWireToolCallNamesBack(t *testing.T) {
	client := &fakeLLMClient{events: []llmapp.StreamEvent{{
		Done:         true,
		FinishReason: "tool_calls",
		ToolCalls:    []llmapp.ToolCall{{ID: "call_1", Name: "file_read", Arguments: `{"path":"README.md"}`}},
	}}}
	caller, err := prepare(t, &fakeConfigSource{cfg: chatConfig()}, client, "")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	stream, err := caller.ChatStream(context.Background(), agentapp.LLMRequest{
		Model: "deepseek-flash",
		Tools: []agentapp.LLMTool{{Name: "file.read"}, {Name: "http.get"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	events := drain(t, stream)
	if len(events) != 1 || !events[0].Done {
		t.Fatalf("events=%+v want one done event", events)
	}
	if len(events[0].ToolCalls) != 1 || events[0].ToolCalls[0] != (agentdomain.ToolCall{
		ID: "call_1", Name: "file.read", Arguments: `{"path":"README.md"}`,
	}) {
		t.Fatalf("internal tool calls=%+v want file.read", events[0].ToolCalls)
	}
}

func TestChatStreamLeavesUnknownWireToolCallNamesUnchanged(t *testing.T) {
	client := &fakeLLMClient{events: []llmapp.StreamEvent{{
		Done:      true,
		ToolCalls: []llmapp.ToolCall{{ID: "call_1", Name: "unknown_tool", Arguments: `{}`}},
	}}}
	caller, err := prepare(t, &fakeConfigSource{cfg: chatConfig()}, client, "")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	stream, err := caller.ChatStream(context.Background(), agentapp.LLMRequest{
		Model: "deepseek-flash",
		Tools: []agentapp.LLMTool{{Name: "file.read"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	events := drain(t, stream)
	if len(events) != 1 || len(events[0].ToolCalls) != 1 {
		t.Fatalf("events=%+v want one tool call", events)
	}
	if events[0].ToolCalls[0].Name != "unknown_tool" {
		t.Fatalf("unknown wire name=%q want unchanged", events[0].ToolCalls[0].Name)
	}
}

func TestChatStreamWrapsOpenErrors(t *testing.T) {
	upstream := llmapp.NewLLMError(platform.CodeLLMRateLimited, "rate limited", true, 429, nil)
	client := &fakeLLMClient{streamErr: upstream}
	caller, err := prepare(t, &fakeConfigSource{cfg: chatConfig()}, client, "")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	_, err = caller.ChatStream(context.Background(), agentapp.LLMRequest{Model: "deepseek-flash"})
	if err == nil {
		t.Fatal("ChatStream returned no error")
	}
	var coded agentapp.CodedError
	if !errors.As(err, &coded) {
		t.Fatalf("err=%v does not implement agentapp.CodedError", err)
	}
	if coded.ErrorCode() != platform.CodeLLMRateLimited {
		t.Fatalf("ErrorCode()=%q want %q", coded.ErrorCode(), platform.CodeLLMRateLimited)
	}
	if _, ok := llmapp.AsLLMError(err); !ok {
		t.Fatalf("wrapped error %v lost the llmapp.LLMError cause", err)
	}
}

func TestChatStreamWrapsStreamErrors(t *testing.T) {
	upstream := llmapp.NewLLMError(platform.CodeLLMUpstreamError, "upstream failed", false, 500, nil)
	client := &fakeLLMClient{events: []llmapp.StreamEvent{
		{Delta: "partial"},
		{Err: upstream},
	}}
	caller, err := prepare(t, &fakeConfigSource{cfg: chatConfig()}, client, "")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	stream, err := caller.ChatStream(context.Background(), agentapp.LLMRequest{Model: "deepseek-flash"})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	events := drain(t, stream)
	if len(events) != 2 {
		t.Fatalf("events=%d want 2: %+v", len(events), events)
	}
	if events[0].Delta != "partial" {
		t.Fatalf("first event=%+v", events[0])
	}
	if events[1].Err == nil {
		t.Fatalf("terminal event has no error: %+v", events[1])
	}
	var coded agentapp.CodedError
	if !errors.As(events[1].Err, &coded) {
		t.Fatalf("stream error %v does not implement agentapp.CodedError", events[1].Err)
	}
	if coded.ErrorCode() != platform.CodeLLMUpstreamError {
		t.Fatalf("ErrorCode()=%q want %q", coded.ErrorCode(), platform.CodeLLMUpstreamError)
	}
}

func TestChatStreamReportsMissingTerminalEvent(t *testing.T) {
	client := &fakeLLMClient{events: []llmapp.StreamEvent{{Delta: "partial"}}}
	caller, err := prepare(t, &fakeConfigSource{cfg: chatConfig()}, client, "")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	stream, err := caller.ChatStream(context.Background(), agentapp.LLMRequest{Model: "deepseek-flash"})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	events := drain(t, stream)
	if len(events) != 2 {
		t.Fatalf("events=%d want delta + synthesized error: %+v", len(events), events)
	}
	if events[0].Delta != "partial" {
		t.Fatalf("first event=%+v", events[0])
	}
	if events[1].Err == nil {
		t.Fatalf("stream ended without a synthesized error: %+v", events[1])
	}
	var coded agentapp.CodedError
	if !errors.As(events[1].Err, &coded) {
		t.Fatalf("synthesized error %v does not implement agentapp.CodedError", events[1].Err)
	}
	if coded.ErrorCode() != platform.CodeLLMInvalidResponse {
		t.Fatalf("ErrorCode()=%q want %q", coded.ErrorCode(), platform.CodeLLMInvalidResponse)
	}
}

// blockingLLMClient returns a stream that only ends when its context is
// cancelled, modelling an upstream that is still streaming when the run aborts.
type blockingLLMClient struct {
	started chan struct{}
}

func (c *blockingLLMClient) Chat(context.Context, llmapp.ChatRequest) (llmapp.ChatResponse, error) {
	return llmapp.ChatResponse{}, nil
}

func (c *blockingLLMClient) ChatStream(ctx context.Context, _ llmapp.ChatRequest) (<-chan llmapp.StreamEvent, error) {
	events := make(chan llmapp.StreamEvent)
	go func() {
		defer close(events)
		if c.started != nil {
			close(c.started)
		}
		<-ctx.Done()
	}()
	return events, nil
}

func (c *blockingLLMClient) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}

func TestChatStreamStopsOnCancellation(t *testing.T) {
	client := &blockingLLMClient{started: make(chan struct{})}
	caller, err := prepare(t, &fakeConfigSource{cfg: chatConfig()}, client, "")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := caller.ChatStream(ctx, agentapp.LLMRequest{Model: "deepseek-flash"})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	<-client.started
	cancel()

	select {
	case event, ok := <-stream:
		if ok {
			t.Fatalf("stream produced event %+v after cancellation", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not close after the run context was cancelled")
	}
}

// stalledLLMClient returns a stream that never closes, modelling an upstream
// adapter that ignores cancellation. The agent adapter must still release its
// forwarder when the run context ends, or every aborted run would leak one.
type stalledLLMClient struct {
	events chan llmapp.StreamEvent
}

func (c *stalledLLMClient) Chat(context.Context, llmapp.ChatRequest) (llmapp.ChatResponse, error) {
	return llmapp.ChatResponse{}, nil
}

func (c *stalledLLMClient) ChatStream(context.Context, llmapp.ChatRequest) (<-chan llmapp.StreamEvent, error) {
	return c.events, nil
}

func (c *stalledLLMClient) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}

func TestChatStreamReleasesStalledUpstreamOnCancellation(t *testing.T) {
	client := &stalledLLMClient{events: make(chan llmapp.StreamEvent)}
	caller, err := prepare(t, &fakeConfigSource{cfg: chatConfig()}, client, "")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := caller.ChatStream(ctx, agentapp.LLMRequest{Model: "deepseek-flash"})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	cancel()

	select {
	case event, ok := <-stream:
		if ok {
			t.Fatalf("stream produced event %+v after cancellation", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a stalled upstream kept the adapter stream open after cancellation")
	}
}
