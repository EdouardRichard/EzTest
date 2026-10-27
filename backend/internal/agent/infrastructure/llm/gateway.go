// Package agentllm adapts the llm context's gateway to the Agent runtime's LLM
// port. The agent application layer defines LLMCaller/LLMRuntime in terms of its
// own request/stream types; this adapter maps them to and from the llm
// context's ChatRequest/StreamEvent and preserves the public llm_* error codes
// as a CodedError so the runner can persist the precise run error_code.
package agentllm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	llm "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// defaultContextWindowTokens is used when the model catalog has no entry (or no
// context window) for the model the run will use.
const defaultContextWindowTokens = 100000

// ConfigSource is the slice of the LLM configuration use case the gateway needs:
// the default configuration, resolved once per run (design spec §7.4).
type ConfigSource interface {
	GetDefault(ctx context.Context) (llm.Config, error)
}

// Gateway prepares one LLM caller per run from the default configuration. The
// caller keeps the configuration's client for the whole run, so a mid-run
// administrator change only affects the next run.
type Gateway struct {
	source   ConfigSource
	factory  llmapp.ClientFactory
	resolver agentapp.ImageResolver
}

// NewGateway returns a gateway reading the default configuration from source and
// building clients with factory. resolver is optional for P1 compatibility:
// text-only runs never need it, while a request carrying an image reference is
// rejected with a readable error when it is missing. P3 wires the attachment
// resolver here; the data URL it returns is transient (D30).
func NewGateway(source ConfigSource, factory llmapp.ClientFactory, resolver ...agentapp.ImageResolver) *Gateway {
	var imageResolver agentapp.ImageResolver
	if len(resolver) > 0 {
		imageResolver = resolver[0]
	}
	return &Gateway{source: source, factory: factory, resolver: imageResolver}
}

// Prepare resolves the default configuration and returns the caller for one run.
// modelOverride (the profile's model) wins over the configuration's model when
// non-empty. P1 Agent runs require the chat_completions protocol because only
// that adapter implements tool streaming: a responses default is
// domain.ErrToolsUnsupported and an unusable default is
// domain.ErrLLMNotConfigured.
func (g *Gateway) Prepare(ctx context.Context, modelOverride string) (agentapp.LLMCaller, error) {
	cfg, err := g.source.GetDefault(ctx)
	if err != nil {
		return nil, agentdomain.ErrLLMNotConfigured
	}
	if cfg.Protocol != llm.ProtocolChat {
		return nil, agentdomain.ErrToolsUnsupported
	}
	model := strings.TrimSpace(cfg.Model)
	if override := strings.TrimSpace(modelOverride); override != "" {
		model = override
	}
	return &caller{
		model:         model,
		contextWindow: contextWindowFor(cfg.ModelCatalog, model),
		client:        g.factory(cfg),
		resolver:      g.resolver,
		multimodal:    cfg.MultimodalEnabled,
	}, nil
}

// contextWindowFor returns the catalog context window for model, falling back to
// defaultContextWindowTokens when the catalog has no usable entry.
func contextWindowFor(catalog []llm.ModelInfo, model string) int {
	for _, info := range catalog {
		if info.ID != model || info.ContextWindow == nil {
			continue
		}
		if *info.ContextWindow > 0 {
			return int(*info.ContextWindow)
		}
	}
	return defaultContextWindowTokens
}

// caller is one prepared LLM connection: it exposes the model and context
// window to the runner and streams mapped chat requests.
type caller struct {
	model         string
	contextWindow int
	client        llmapp.Client
	// resolver turns persisted image references into transient data URLs at
	// call time; multimodal reflects the configuration's image capability.
	resolver   agentapp.ImageResolver
	multimodal bool
}

// Model implements agentapp.LLMCaller.
func (c *caller) Model() string { return c.model }

// ContextWindowTokens implements agentapp.LLMCaller.
func (c *caller) ContextWindowTokens() int { return c.contextWindow }

// ChatStream implements agentapp.LLMCaller: it resolves any image reference into
// a transient data URL, forwards the mapped request and streams mapped events.
// Upstream and in-stream errors are wrapped so the runner can read their llm_*
// code; a stream that closes without a terminal Done or Err event is reported as
// llm_invalid_response.
func (c *caller) ChatStream(ctx context.Context, req agentapp.LLMRequest) (<-chan agentapp.LLMStreamEvent, error) {
	messages, err := c.resolveImageRefs(ctx, req.Messages)
	if err != nil {
		return nil, err
	}
	req.Messages = messages
	upstream, err := c.client.ChatStream(ctx, toChatRequest(req))
	if err != nil {
		return nil, wrapLLMError(err)
	}
	events := make(chan agentapp.LLMStreamEvent)
	go forwardStream(ctx, upstream, events, wireToInternalNames(req.Tools))
	return events, nil
}

// resolveImageRefs replaces every persisted image reference with the data URL
// the resolver produces for this call. Text-only requests are returned
// untouched, so the P1 path keeps its exact behavior.
//
// The caller's messages and parts are copied before the substitution: a request
// value is reused by the runner for its history, which must never hold a data
// URL (D30). The configuration's multimodal capability is checked only when a
// reference is actually present.
func (c *caller) resolveImageRefs(ctx context.Context, messages []agentapp.LLMMessage) ([]agentapp.LLMMessage, error) {
	hasRefs := false
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.ImageRef != "" {
				hasRefs = true
				break
			}
		}
		if hasRefs {
			break
		}
	}
	if !hasRefs {
		return messages, nil
	}
	if !c.multimodal {
		return nil, badRequestError("llm: the default configuration has multimodal disabled; an image reference cannot be sent")
	}
	if c.resolver == nil {
		return nil, badRequestError("llm: image resolver is not configured")
	}

	resolved := make([]agentapp.LLMMessage, len(messages))
	copy(resolved, messages)
	for index, message := range messages {
		parts := make([]agentdomain.Part, len(message.Parts))
		copy(parts, message.Parts)
		for partIndex, part := range parts {
			if part.ImageRef == "" {
				continue
			}
			dataURL, err := c.resolver.ResolveImage(ctx, part.ImageRef)
			if err != nil {
				return nil, fmt.Errorf("llm: resolve image reference %q: %w", part.ImageRef, err)
			}
			parts[partIndex] = agentdomain.Part{Type: "image_url", ImageURL: dataURL}
		}
		resolved[index].Parts = parts
	}
	return resolved, nil
}

// badRequestError is a request the gateway refuses before reaching the upstream.
// It carries the public llm_bad_request code so the runner persists the precise
// cause in agent_runs.
func badRequestError(message string) error {
	return &codedError{code: platform.CodeLLMBadRequest, err: errors.New(message)}
}

// wireToolName maps an internal tool name to the provider-safe wire name.
// OpenAI-compatible upstreams only accept ^[a-zA-Z0-9_-]+$ in a function name,
// so the dotted internal contract (file.read, http.get, ...) is sent with
// underscores. The internal names stay unchanged for the agent runtime.
func wireToolName(name string) string {
	return strings.ReplaceAll(name, ".", "_")
}

// wireToInternalNames builds the inbound mapping from the request's tools, so a
// wire tool-call name is reported back to the agent caller under its internal
// name. Nil when the request carries no tools.
func wireToInternalNames(tools []agentapp.LLMTool) map[string]string {
	if len(tools) == 0 {
		return nil
	}
	names := make(map[string]string, len(tools))
	for _, tool := range tools {
		names[wireToolName(tool.Name)] = tool.Name
	}
	return names
}

// internalToolName maps a wire tool-call name back to the internal name. An
// unknown wire name is returned unchanged so the executor can deny or fail it
// through its normal path.
func internalToolName(wireToInternal map[string]string, name string) string {
	if internal, ok := wireToInternal[name]; ok {
		return internal
	}
	return name
}

// toChatRequest maps the agent request onto the llm gateway request. Stream is
// always true: the Agent runtime only uses streaming completions.
func toChatRequest(req agentapp.LLMRequest) llmapp.ChatRequest {
	var messages []llmapp.Message
	if len(req.Messages) > 0 {
		messages = make([]llmapp.Message, 0, len(req.Messages))
	}
	for _, message := range req.Messages {
		var parts []llmapp.Part
		if len(message.Parts) > 0 {
			parts = make([]llmapp.Part, 0, len(message.Parts))
			for _, part := range message.Parts {
				parts = append(parts, llmapp.Part{
					Type:     part.Type,
					Text:     part.Text,
					ImageURL: part.ImageURL,
				})
			}
		}
		var toolCalls []llmapp.ToolCall
		if len(message.ToolCalls) > 0 {
			toolCalls = make([]llmapp.ToolCall, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				toolCalls = append(toolCalls, llmapp.ToolCall{
					ID:        call.ID,
					Name:      wireToolName(call.Name),
					Arguments: call.Arguments,
				})
			}
		}
		messages = append(messages, llmapp.Message{
			Role:             message.Role,
			Content:          parts,
			ReasoningContent: message.ReasoningContent,
			ToolCalls:        toolCalls,
			ToolCallID:       message.ToolCallID,
		})
	}

	var tools []llmapp.ToolSpec
	if len(req.Tools) > 0 {
		tools = make([]llmapp.ToolSpec, 0, len(req.Tools))
		for _, tool := range req.Tools {
			tools = append(tools, llmapp.ToolSpec{
				Name:        wireToolName(tool.Name),
				Description: tool.Description,
				Parameters:  tool.Parameters,
			})
		}
	}

	return llmapp.ChatRequest{
		Model:           req.Model,
		Messages:        messages,
		Tools:           tools,
		Stream:          true,
		MaxTokens:       req.MaxTokens,
		Thinking:        req.Thinking,
		ReasoningEffort: req.ReasoningEffort,
	}
}

// fromStreamEvent maps one upstream stream event to the agent port. Wire tool
// names are mapped back to their internal names; a stream error is wrapped so
// its llm_* code survives.
func fromStreamEvent(event llmapp.StreamEvent, wireToInternal map[string]string) agentapp.LLMStreamEvent {
	var toolCalls []agentdomain.ToolCall
	if len(event.ToolCalls) > 0 {
		toolCalls = make([]agentdomain.ToolCall, 0, len(event.ToolCalls))
		for _, call := range event.ToolCalls {
			toolCalls = append(toolCalls, agentdomain.ToolCall{
				ID:        call.ID,
				Name:      internalToolName(wireToInternal, call.Name),
				Arguments: call.Arguments,
			})
		}
	}
	return agentapp.LLMStreamEvent{
		Delta:          event.Delta,
		ReasoningDelta: event.ReasoningDelta,
		Done:           event.Done,
		ToolCalls:      toolCalls,
		FinishReason:   event.FinishReason,
		Usage: agentdomain.Usage{
			PromptTokens:     event.Usage.PromptTokens,
			CompletionTokens: event.Usage.CompletionTokens,
			ReasoningTokens:  event.Usage.ReasoningTokens,
			TotalTokens:      event.Usage.TotalTokens,
		},
		Err: wrapLLMError(event.Err),
	}
}

// forwardStream forwards mapped events until the upstream channel closes or the
// run context is cancelled, synthesizing the terminal error when the upstream
// ended without one. wireToInternal maps inbound wire tool names back to the
// internal contract.
func forwardStream(ctx context.Context, source <-chan llmapp.StreamEvent, out chan<- agentapp.LLMStreamEvent, wireToInternal map[string]string) {
	defer close(out)
	terminal := false
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-source:
			if !ok {
				if terminal {
					return
				}
				select {
				case out <- agentapp.LLMStreamEvent{Err: invalidResponseError()}:
				case <-ctx.Done():
				}
				return
			}
			if event.Done || event.Err != nil {
				terminal = true
			}
			select {
			case out <- fromStreamEvent(event, wireToInternal):
			case <-ctx.Done():
				return
			}
		}
	}
}

// codedError carries a stable public error code alongside the upstream cause, so
// the runner can persist it as agent_runs.error_code (agentapp.CodedError).
type codedError struct {
	code string
	err  error
}

// Error implements error.
func (e *codedError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return "llm: " + e.code
}

// Unwrap exposes the typed upstream error for errors.Is/errors.As.
func (e *codedError) Unwrap() error { return e.err }

// ErrorCode returns the public llm_* code.
func (e *codedError) ErrorCode() string { return e.code }

// wrapLLMError wraps a typed upstream failure so it implements
// agentapp.CodedError. Plain errors are returned unchanged: the runner already
// maps them to the generic llm_upstream_error.
func wrapLLMError(err error) error {
	if err == nil {
		return nil
	}
	llmErr, ok := llmapp.AsLLMError(err)
	if !ok {
		return err
	}
	code := strings.TrimSpace(llmErr.Code)
	if code == "" {
		return err
	}
	return &codedError{code: code, err: err}
}

// invalidResponseError is the terminal error for a stream that ended without a
// Done or Err event.
func invalidResponseError() error {
	return &codedError{
		code: platform.CodeLLMInvalidResponse,
		err:  errors.New("llm: stream ended without a terminal event"),
	}
}
