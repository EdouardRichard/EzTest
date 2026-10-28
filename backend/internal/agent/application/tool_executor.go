package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// ActionToolInvoked is the audit action recorded for every tool invocation.
// The entry carries the tool name, status, duration and run/session ids only;
// call arguments and results are never part of the audit trail.
const ActionToolInvoked = "agent.tool.invoked"

// Tool execution statuses reported by ToolExecutor and in the tool_result
// activity detail.
const (
	ToolStatusOK        = "ok"
	ToolStatusError     = "error"
	ToolStatusTimeout   = "timeout"
	ToolStatusCancelled = "cancelled"
	ToolStatusDenied    = "denied"
)

// Tool error codes the executor produces itself. Tools report their own codes
// as *domain.ToolError; the executor maps a ToolError code to the matching
// status when one exists.
const (
	toolErrorCodeInvalidArguments = "invalid_arguments"
	toolErrorCodeDenied           = "denied"
	toolErrorCodeTimeout          = "timeout"
	toolErrorCodeCancelled        = "cancelled"
	toolErrorCodeInternal         = "internal"
	toolErrorCodeNotFound         = "not_found"
)

// maxToolExecutionTimeout is the hard ceiling on a per-tool timeout. The
// executor's global timeout comes from EZTEST_AGENT_TOOL_TIMEOUT_MS (already
// bounded by config); a tool's own ToolWithTimeout value is capped here so a
// tool cannot hold a run open arbitrarily long (design spec §5.6).
const maxToolExecutionTimeout = 60 * time.Second

// ToolRegistry holds the tools available to Agent runs. It is read-only after
// construction, so a single registry can serve concurrent runs.
type ToolRegistry struct {
	tools map[string]domain.Tool
	// order preserves the registration order for Names, Specs and ToolInfos.
	order []string
}

// NewToolRegistry returns a registry containing tools. The first registration
// of a name wins; a nil or unnamed tool is ignored.
func NewToolRegistry(tools ...domain.Tool) *ToolRegistry {
	registry := &ToolRegistry{tools: make(map[string]domain.Tool, len(tools))}
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		name := tool.Name()
		if name == "" {
			continue
		}
		if _, exists := registry.tools[name]; exists {
			continue
		}
		registry.tools[name] = tool
		registry.order = append(registry.order, name)
	}
	return registry
}

// Get returns the tool registered under name.
func (r *ToolRegistry) Get(name string) (domain.Tool, bool) {
	if r == nil {
		return nil, false
	}
	tool, ok := r.tools[name]
	return tool, ok
}

// Names returns the registered tool names in registration order.
func (r *ToolRegistry) Names() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.order...)
}

// Specs returns the LLM tool descriptions for allowed, in allowlist order.
// Names that are not registered are ignored, so a stale profile cannot break
// a run.
func (r *ToolRegistry) Specs(allowed []string) []LLMTool {
	if r == nil {
		return nil
	}
	specs := make([]LLMTool, 0, len(allowed))
	seen := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		tool, ok := r.tools[name]
		if !ok {
			continue
		}
		seen[name] = struct{}{}
		specs = append(specs, LLMTool{
			Name:        name,
			Description: tool.Description(),
			Parameters:  tool.InputSchema(),
		})
	}
	return specs
}

// ToolInfos returns the read model exposed by GET /api/agents/tools, in
// registration order.
func (r *ToolRegistry) ToolInfos() []domain.ToolInfo {
	if r == nil {
		return nil
	}
	infos := make([]domain.ToolInfo, 0, len(r.order))
	for _, name := range r.order {
		tool := r.tools[name]
		infos = append(infos, domain.ToolInfo{
			Name:        name,
			Description: tool.Description(),
			Schema:      tool.InputSchema(),
		})
	}
	return infos
}

// ToolCallInput is one tool invocation requested by the model.
type ToolCallInput struct {
	RunID, SessionID int64
	// BatchID and CaseRunID are the P6 execution scope of the run. They ride
	// along in the Invocation context unchanged and stay zero for P1–P5 runs.
	BatchID, CaseRunID int64
	ActorUserID        *int64
	// ProfileKey is the key of the profile that owns the run. The executor
	// forwards it in the Invocation context together with the run, session and
	// actor identity.
	ProfileKey string
	Call       domain.ToolCall
	// Allowed is the profile's tool allowlist; a call outside it is denied
	// without executing the tool.
	Allowed []string
}

// ToolExecution is the outcome of one tool invocation: the result handed back
// to the model, the status for the tool_result activity, the measured duration
// and the internal error code (empty on success).
type ToolExecution struct {
	Result     domain.ToolResult
	Status     string
	DurationMS int64
	ErrorCode  string
}

// ToolExecutor runs tool calls through the whitelist, argument validation,
// per-call timeout, panic recovery and audit pipeline.
type ToolExecutor struct {
	registry *ToolRegistry
	audit    AuditSink
	timeout  time.Duration
}

// NewToolExecutor returns an executor. A non-positive timeout disables the
// per-tool timeout (the caller's context is still honoured).
func NewToolExecutor(registry *ToolRegistry, audit AuditSink, timeout time.Duration) *ToolExecutor {
	return &ToolExecutor{registry: registry, audit: audit, timeout: timeout}
}

// Execute runs one tool call. It never panics: a panicking tool is reported as
// an internal tool error so one broken tool cannot bring the process down.
// Every invocation, including denied and invalid ones, is audited with the
// tool call's status and duration; audit failures are logged, never surfaced.
func (e *ToolExecutor) Execute(ctx context.Context, in ToolCallInput) ToolExecution {
	started := time.Now()
	execution := e.run(ctx, in)
	execution.DurationMS = time.Since(started).Milliseconds()
	e.recordAudit(ctx, in, execution)
	return execution
}

// run is Execute's body behind the panic boundary.
func (e *ToolExecutor) run(ctx context.Context, in ToolCallInput) (execution ToolExecution) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.ErrorContext(ctx, "agent: tool panicked", "tool", in.Call.Name, "panic", recovered)
			execution = toolExecutionFailure(ToolStatusError, toolErrorCodeInternal,
				fmt.Sprintf("tool %q panicked while executing", in.Call.Name))
		}
	}()

	name := in.Call.Name
	if !toolAllowed(in.Allowed, name) {
		return toolExecutionFailure(ToolStatusDenied, toolErrorCodeDenied,
			fmt.Sprintf("tool %q is not allowed", name))
	}
	tool, ok := e.registry.Get(name)
	if !ok {
		return toolExecutionFailure(ToolStatusError, toolErrorCodeNotFound,
			fmt.Sprintf("tool %q is not registered", name))
	}
	args, err := normalizeToolArguments(in.Call.Arguments)
	if err != nil {
		return toolExecutionFailure(ToolStatusError, toolErrorCodeInvalidArguments, err.Error())
	}
	return e.invoke(ctx, tool, args, in)
}

// invoke calls the tool under the executor timeout and maps its outcome. The
// invocation context is attached first so the tool always sees the run-scoped
// identity the executor was given; a value a caller left in ctx is replaced.
func (e *ToolExecutor) invoke(ctx context.Context, tool domain.Tool, args json.RawMessage, in ToolCallInput) ToolExecution {
	toolCtx := WithToolInvocation(ctx, Invocation{
		SessionID:   in.SessionID,
		RunID:       in.RunID,
		ActorUserID: in.ActorUserID,
		ProfileKey:  in.ProfileKey,
		BatchID:     in.BatchID,
		CaseRunID:   in.CaseRunID,
	})
	if timeout := e.toolTimeout(tool); timeout > 0 {
		var cancel context.CancelFunc
		toolCtx, cancel = context.WithTimeout(toolCtx, timeout)
		defer cancel()
	}

	result, err := tool.Execute(toolCtx, args)
	if err != nil {
		return toolErrorExecution(tool.Name(), err)
	}
	// A tool that ignores the context may still return after the run was
	// aborted or the deadline passed; the execution is not a success then.
	if toolCtxErr := toolCtx.Err(); toolCtxErr != nil {
		return toolErrorExecution(tool.Name(), toolCtxErr)
	}
	if result.IsError {
		return ToolExecution{Result: result, Status: ToolStatusError}
	}
	return ToolExecution{Result: result, Status: ToolStatusOK}
}

// toolTimeout returns the timeout for one invocation. A tool that implements
// domain.ToolWithTimeout and reports a positive duration overrides the global
// timeout, capped at maxToolExecutionTimeout. Every other tool keeps the
// executor's global timeout.
func (e *ToolExecutor) toolTimeout(tool domain.Tool) time.Duration {
	if withTimeout, ok := tool.(domain.ToolWithTimeout); ok {
		if timeout := withTimeout.Timeout(); timeout > 0 {
			if timeout > maxToolExecutionTimeout {
				return maxToolExecutionTimeout
			}
			return timeout
		}
	}
	return e.timeout
}

// toolAllowed reports whether name is in the profile's allowlist.
func toolAllowed(allowed []string, name string) bool {
	for _, candidate := range allowed {
		if candidate == name {
			return true
		}
	}
	return false
}

// normalizeToolArguments validates the model-supplied arguments and turns empty
// arguments into the empty JSON object. Arguments must be a JSON object; the
// schema itself is enforced by the individual tools.
func normalizeToolArguments(raw string) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace([]byte(raw))
	if len(trimmed) == 0 {
		return json.RawMessage("{}"), nil
	}
	if !json.Valid(trimmed) {
		return nil, errors.New("invalid_arguments: arguments are not valid JSON")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		return nil, errors.New("invalid_arguments: arguments must be a JSON object")
	}
	return json.RawMessage(trimmed), nil
}

// toolErrorExecution maps a tool or context error to an execution record.
func toolErrorExecution(name string, err error) ToolExecution {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return toolExecutionFailure(ToolStatusTimeout, toolErrorCodeTimeout,
			fmt.Sprintf("tool %q timed out", name))
	case errors.Is(err, context.Canceled):
		return toolExecutionFailure(ToolStatusCancelled, toolErrorCodeCancelled,
			fmt.Sprintf("tool %q was cancelled", name))
	}

	var toolErr *domain.ToolError
	if errors.As(err, &toolErr) {
		code := toolErr.Code
		if code == "" {
			code = toolErrorCodeInternal
		}
		return ToolExecution{
			Result:    domain.ToolResult{Content: toolErr.Error(), IsError: true},
			Status:    statusForToolErrorCode(code),
			ErrorCode: code,
		}
	}

	content := err.Error()
	if content == "" {
		content = fmt.Sprintf("tool %q failed", name)
	}
	return toolExecutionFailure(ToolStatusError, toolErrorCodeInternal, content)
}

// statusForToolErrorCode returns the execution status matching a tool error
// code; tool-level failures without a dedicated status are plain errors.
func statusForToolErrorCode(code string) string {
	switch code {
	case toolErrorCodeTimeout:
		return ToolStatusTimeout
	case toolErrorCodeCancelled:
		return ToolStatusCancelled
	case toolErrorCodeDenied:
		return ToolStatusDenied
	default:
		return ToolStatusError
	}
}

// toolExecutionFailure builds an IsError execution record with its model-facing
// message.
func toolExecutionFailure(status, code, content string) ToolExecution {
	return ToolExecution{
		Result:    domain.ToolResult{Content: content, IsError: true},
		Status:    status,
		ErrorCode: code,
	}
}

// recordAudit appends the agent.tool.invoked audit entry. Auditing must never
// fail the run, so a sink error is only logged. The metadata deliberately
// carries no call arguments and no result content.
func (e *ToolExecutor) recordAudit(ctx context.Context, in ToolCallInput, execution ToolExecution) {
	if e.audit == nil {
		return
	}
	event := AuditEvent{
		Action:      ActionToolInvoked,
		ActorUserID: in.ActorUserID,
		Metadata: map[string]any{
			"tool":        in.Call.Name,
			"status":      execution.Status,
			"duration_ms": execution.DurationMS,
			"run_id":      in.RunID,
			"session_id":  in.SessionID,
		},
	}
	if err := e.audit.Record(ctx, event); err != nil {
		slog.ErrorContext(ctx, "agent: record tool audit entry", "tool", in.Call.Name, "error", err)
	}
}
