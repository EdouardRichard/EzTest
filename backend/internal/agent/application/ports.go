// Package application holds the Agent use cases and the ports they consume
// from the outside world: the LLM gateway, the event sink, the audit sink and
// the clock. The types here are the only LLM/event/audit/clock contract the
// agent context depends on; adapters in infrastructure implement them.
package application

import (
	"context"
	"encoding/json"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// LLMMessage is one message in the conversation sent to the LLM. Parts carries
// text and image parts; ReasoningContent is echoed back on assistant messages
// that had thinking content; ToolCalls/ToolCallID implement the tool protocol.
type LLMMessage struct {
	Role             string
	Parts            []domain.Part
	ReasoningContent string
	ToolCalls        []domain.ToolCall
	ToolCallID       string
}

// LLMTool is one tool exposed to the model, in JSON Schema form.
type LLMTool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// LLMRequest is one streamed completion request.
type LLMRequest struct {
	Model           string
	Messages        []LLMMessage
	Tools           []LLMTool
	MaxTokens       int
	Thinking        string
	ReasoningEffort string
}

// LLMStreamEvent is one normalized stream event: a text/reasoning delta, a
// terminal Done carrying the assembled tool calls and usage, or a terminal
// error in Err.
type LLMStreamEvent struct {
	Delta          string
	ReasoningDelta string
	Done           bool
	ToolCalls      []domain.ToolCall
	FinishReason   string
	Usage          domain.Usage
	Err            error
}

// LLMCaller is one prepared LLM connection: it knows its model and context
// window and can stream a chat request.
type LLMCaller interface {
	Model() string
	ContextWindowTokens() int
	ChatStream(ctx context.Context, req LLMRequest) (<-chan LLMStreamEvent, error)
}

// LLMRuntime prepares a caller for the default LLM configuration, optionally
// overriding the model. Boot failures are reported as domain.ErrLLMNotConfigured
// or domain.ErrToolsUnsupported.
type LLMRuntime interface {
	Prepare(ctx context.Context, modelOverride string) (LLMCaller, error)
}

// ActivityEvent is one agent activity event to publish on the P0 event bus.
type ActivityEvent struct {
	Channel   string
	AgentRole string
	Summary   string
	SessionID int64
	// VersionID is the P3 version the session is scoped to; nil keeps the P1
	// envelope (no version reference).
	VersionID *string
	Detail    map[string]any
}

// EventSink publishes activity events. Publishing a run's activity must never
// fail the run, so callers only log sink errors.
type EventSink interface {
	PublishActivity(ctx context.Context, event ActivityEvent) error
}

// AuditEvent is one audit entry to record.
type AuditEvent struct {
	Action      string
	ActorUserID *int64
	TargetType  string
	TargetID    string
	Metadata    map[string]any
}

// AuditSink records audit entries. Like EventSink, a failure is logged but
// never fails the request.
type AuditSink interface {
	Record(ctx context.Context, event AuditEvent) error
}

// ImageResolver turns a stored image reference ("attachment:<id>") into a data
// URL at LLM call time. The data URL is transient: it must never be persisted
// in agent_messages or logged (D30). Implementations reject references that do
// not name a readable image attachment.
type ImageResolver interface {
	ResolveImage(ctx context.Context, ref string) (dataURL string, err error)
}

// Clock returns the current time. It is injectable so the run loop can be
// tested without sleeping.
type Clock func() time.Time
