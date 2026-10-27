package application

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agenttools "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/tools"
)

// executorTestTool is a programmable domain.Tool used to pin the executor
// pipeline: it counts calls and can return any result, error or panic.
type executorTestTool struct {
	toolName string
	calls    int
	lastArgs json.RawMessage
	run      func(ctx context.Context, args json.RawMessage) (domain.ToolResult, error)
}

// Name implements domain.Tool.
func (t *executorTestTool) Name() string {
	if t.toolName == "" {
		return "stub"
	}
	return t.toolName
}

// Description implements domain.Tool.
func (t *executorTestTool) Description() string { return "stub tool for executor tests" }

// InputSchema implements domain.Tool.
func (t *executorTestTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

// Execute implements domain.Tool.
func (t *executorTestTool) Execute(ctx context.Context, args json.RawMessage) (domain.ToolResult, error) {
	t.calls++
	t.lastArgs = args
	if t.run != nil {
		return t.run(ctx, args)
	}
	return domain.ToolResult{Content: "ok"}, nil
}

// executorTestAudit captures the audit entries the executor records.
type executorTestAudit struct {
	mu     sync.Mutex
	events []AuditEvent
	err    error
}

// Record implements AuditSink.
func (a *executorTestAudit) Record(_ context.Context, event AuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, event)
	return a.err
}

// recorded returns a copy of the captured events.
func (a *executorTestAudit) recorded() []AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]AuditEvent(nil), a.events...)
}

// silenceToolLogs discards the executor's audit/panic log lines so test output
// stays clean.
func silenceToolLogs(t *testing.T) {
	t.Helper()
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
}

// assertToolAudit checks the exact audit contract: one agent.tool.invoked entry
// carrying only the five non-sensitive metadata fields.
func assertToolAudit(t *testing.T, event AuditEvent, tool, status string, runID, sessionID int64) {
	t.Helper()
	if event.Action != "agent.tool.invoked" {
		t.Fatalf("audit action=%q want agent.tool.invoked", event.Action)
	}
	if len(event.Metadata) != 5 {
		t.Fatalf("audit metadata=%v want exactly tool,status,duration_ms,run_id,session_id", event.Metadata)
	}
	for _, key := range []string{"tool", "status", "duration_ms", "run_id", "session_id"} {
		if _, ok := event.Metadata[key]; !ok {
			t.Fatalf("audit metadata is missing %q: %v", key, event.Metadata)
		}
	}
	if got := event.Metadata["tool"]; got != tool {
		t.Fatalf("metadata[tool]=%v want %q", got, tool)
	}
	if got := event.Metadata["status"]; got != status {
		t.Fatalf("metadata[status]=%v want %q", got, status)
	}
	if got, ok := event.Metadata["duration_ms"].(int64); !ok || got < 0 {
		t.Fatalf("metadata[duration_ms]=%v want a non-negative int64", event.Metadata["duration_ms"])
	}
	if got, ok := event.Metadata["run_id"].(int64); !ok || got != runID {
		t.Fatalf("metadata[run_id]=%v want %d", event.Metadata["run_id"], runID)
	}
	if got, ok := event.Metadata["session_id"].(int64); !ok || got != sessionID {
		t.Fatalf("metadata[session_id]=%v want %d", event.Metadata["session_id"], sessionID)
	}
}

func TestRegistrySpecsRespectsAllowlistAndOrder(t *testing.T) {
	reg := NewToolRegistry(agenttools.NewTestEcho(), agenttools.NewTestFail())
	specs := reg.Specs([]string{"test.echo", "unknown", "test.fail"})
	if len(specs) != 2 {
		t.Fatalf("len(Specs)=%d want 2 (unknown names are ignored): %+v", len(specs), specs)
	}
	if specs[0].Name != "test.echo" || specs[1].Name != "test.fail" {
		t.Fatalf("Specs order=%q,%q want allowlist order test.echo,test.fail", specs[0].Name, specs[1].Name)
	}
	for _, spec := range specs {
		if spec.Description == "" {
			t.Fatalf("tool %q spec has an empty description", spec.Name)
		}
		var schema map[string]any
		if err := json.Unmarshal(spec.Parameters, &schema); err != nil {
			t.Fatalf("tool %q Parameters is not JSON: %v", spec.Name, err)
		}
		if schema == nil {
			t.Fatalf("tool %q Parameters is not a JSON object", spec.Name)
		}
		if schema["type"] != "object" {
			t.Fatalf("tool %q schema type=%v want object", spec.Name, schema["type"])
		}
	}
}

func TestRegistryGetNamesAndToolInfos(t *testing.T) {
	echo := agenttools.NewTestEcho()
	fail := agenttools.NewTestFail()
	reg := NewToolRegistry(fail, echo)

	if names := reg.Names(); len(names) != 2 || names[0] != "test.fail" || names[1] != "test.echo" {
		t.Fatalf("Names()=%v want registration order [test.fail test.echo]", names)
	}
	tool, ok := reg.Get("test.echo")
	if !ok || tool == nil || tool.Name() != "test.echo" {
		t.Fatalf("Get(test.echo)=%v,%v", tool, ok)
	}
	if tool, ok := reg.Get("unknown"); ok || tool != nil {
		t.Fatalf("Get(unknown)=%v,%v want nil,false", tool, ok)
	}

	infos := reg.ToolInfos()
	if len(infos) != 2 {
		t.Fatalf("len(ToolInfos)=%d want 2", len(infos))
	}
	if infos[0].Name != "test.fail" || infos[1].Name != "test.echo" {
		t.Fatalf("ToolInfos order=%q,%q want registration order", infos[0].Name, infos[1].Name)
	}
	if string(infos[1].Schema) != string(echo.InputSchema()) {
		t.Fatalf("ToolInfos schema=%s want the tool schema unchanged", infos[1].Schema)
	}
}

func TestExecutorDeniesUnlistedToolWithoutAuditLeak(t *testing.T) {
	const marker = "super-secret-marker-42"
	if ActionToolInvoked != "agent.tool.invoked" {
		t.Fatalf("ActionToolInvoked=%q want agent.tool.invoked", ActionToolInvoked)
	}

	stub := &executorTestTool{
		toolName: "test.echo",
		run: func(context.Context, json.RawMessage) (domain.ToolResult, error) {
			return domain.ToolResult{Content: marker}, nil
		},
	}
	audit := &executorTestAudit{}
	exec := NewToolExecutor(NewToolRegistry(stub), audit, time.Second)

	got := exec.Execute(context.Background(), ToolCallInput{
		RunID:     7,
		SessionID: 9,
		Call:      domain.ToolCall{ID: "call_1", Name: "test.echo", Arguments: `{"text":"` + marker + `"}`},
		Allowed:   nil,
	})
	if got.Status != ToolStatusDenied {
		t.Fatalf("Status=%q want %q", got.Status, ToolStatusDenied)
	}
	if !got.Result.IsError || got.Result.Content == "" {
		t.Fatalf("denied Result=%+v want IsError with a model-facing message", got.Result)
	}
	if stub.calls != 0 {
		t.Fatalf("tool was executed %d times, the allowlist must stop it", stub.calls)
	}

	events := audit.recorded()
	if len(events) != 1 {
		t.Fatalf("audit entries=%d want 1", len(events))
	}
	assertToolAudit(t, events[0], "test.echo", ToolStatusDenied, 7, 9)

	encoded, err := json.Marshal(events[0].Metadata)
	if err != nil {
		t.Fatalf("marshal audit metadata: %v", err)
	}
	if strings.Contains(string(encoded), marker) {
		t.Fatalf("audit metadata leaks the call arguments/result: %s", encoded)
	}
	for _, forbidden := range []string{"args", "arguments", "result", "content"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("audit metadata contains the forbidden key %q: %s", forbidden, encoded)
		}
	}
}

func TestExecutorMapsInvalidJSONArguments(t *testing.T) {
	cases := map[string]string{
		"truncated object": `{"text":`,
		"array":            `[1,2]`,
		"null":             `null`,
		"scalar":           `"hello"`,
	}
	for name, arguments := range cases {
		t.Run(name, func(t *testing.T) {
			stub := &executorTestTool{toolName: "test.echo"}
			audit := &executorTestAudit{}
			exec := NewToolExecutor(NewToolRegistry(stub), audit, time.Second)

			got := exec.Execute(context.Background(), ToolCallInput{
				RunID:     1,
				SessionID: 2,
				Call:      domain.ToolCall{ID: "call_1", Name: "test.echo", Arguments: arguments},
				Allowed:   []string{"test.echo"},
			})
			if got.Status != ToolStatusError {
				t.Fatalf("Status=%q want %q", got.Status, ToolStatusError)
			}
			if got.ErrorCode != "invalid_arguments" {
				t.Fatalf("ErrorCode=%q want invalid_arguments", got.ErrorCode)
			}
			if !got.Result.IsError || got.Result.Content == "" {
				t.Fatalf("Result=%+v want IsError with a model-facing message", got.Result)
			}
			if stub.calls != 0 {
				t.Fatalf("tool was executed %d times with invalid arguments", stub.calls)
			}
			if events := audit.recorded(); len(events) != 1 {
				t.Fatalf("audit entries=%d want 1", len(events))
			} else {
				assertToolAudit(t, events[0], "test.echo", ToolStatusError, 1, 2)
			}
		})
	}
}

func TestExecutorTimesOut(t *testing.T) {
	audit := &executorTestAudit{}
	exec := NewToolExecutor(NewToolRegistry(agenttools.NewTestFail()), audit, 20*time.Millisecond)

	got := exec.Execute(context.Background(), ToolCallInput{
		RunID:     3,
		SessionID: 4,
		Call:      domain.ToolCall{ID: "call_1", Name: "test.fail", Arguments: `{"mode":"timeout"}`},
		Allowed:   []string{"test.fail"},
	})
	if got.Status != ToolStatusTimeout {
		t.Fatalf("Status=%q want %q", got.Status, ToolStatusTimeout)
	}
	if got.ErrorCode != "timeout" {
		t.Fatalf("ErrorCode=%q want timeout", got.ErrorCode)
	}
	if !got.Result.IsError {
		t.Fatalf("Result=%+v want IsError", got.Result)
	}
	if got.DurationMS <= 0 {
		t.Fatalf("DurationMS=%d want the elapsed time", got.DurationMS)
	}
	events := audit.recorded()
	if len(events) != 1 {
		t.Fatalf("audit entries=%d want 1", len(events))
	}
	assertToolAudit(t, events[0], "test.fail", ToolStatusTimeout, 3, 4)
}

func TestExecutorRecoversPanic(t *testing.T) {
	silenceToolLogs(t)
	audit := &executorTestAudit{}
	exec := NewToolExecutor(NewToolRegistry(agenttools.NewTestFail()), audit, time.Second)

	got := exec.Execute(context.Background(), ToolCallInput{
		RunID:     5,
		SessionID: 6,
		Call:      domain.ToolCall{ID: "call_1", Name: "test.fail", Arguments: `{"mode":"panic"}`},
		Allowed:   []string{"test.fail"},
	})
	if got.Status != ToolStatusError {
		t.Fatalf("Status=%q want %q", got.Status, ToolStatusError)
	}
	if got.ErrorCode != "internal" {
		t.Fatalf("ErrorCode=%q want internal", got.ErrorCode)
	}
	if !got.Result.IsError || got.Result.Content == "" {
		t.Fatalf("Result=%+v want IsError with a model-facing message", got.Result)
	}
	events := audit.recorded()
	if len(events) != 1 {
		t.Fatalf("audit entries=%d want 1", len(events))
	}
	assertToolAudit(t, events[0], "test.fail", ToolStatusError, 5, 6)
}

func TestExecutorTreatsEmptyArgumentsAsObject(t *testing.T) {
	for name, arguments := range map[string]string{"empty": "", "blank": "   "} {
		t.Run(name, func(t *testing.T) {
			stub := &executorTestTool{toolName: "stub"}
			exec := NewToolExecutor(NewToolRegistry(stub), nil, time.Second)

			got := exec.Execute(context.Background(), ToolCallInput{
				Call:    domain.ToolCall{ID: "call_1", Name: "stub", Arguments: arguments},
				Allowed: []string{"stub"},
			})
			if got.Status != ToolStatusOK {
				t.Fatalf("Status=%q want %q (content=%q)", got.Status, ToolStatusOK, got.Result.Content)
			}
			if got.Result.IsError {
				t.Fatalf("Result=%+v want a successful result", got.Result)
			}
			if string(stub.lastArgs) != "{}" {
				t.Fatalf("tool received %q want {} for empty arguments", stub.lastArgs)
			}
		})
	}
}

func TestExecutorMapsToolErrorCode(t *testing.T) {
	stub := &executorTestTool{
		toolName: "file.read",
		run: func(context.Context, json.RawMessage) (domain.ToolResult, error) {
			return domain.ToolResult{}, &domain.ToolError{Code: "not_found", Message: "README.md does not exist"}
		},
	}
	exec := NewToolExecutor(NewToolRegistry(stub), &executorTestAudit{}, time.Second)

	got := exec.Execute(context.Background(), ToolCallInput{
		Call:    domain.ToolCall{ID: "call_1", Name: "file.read", Arguments: `{"path":"README.md"}`},
		Allowed: []string{"file.read"},
	})
	if got.Status != ToolStatusError {
		t.Fatalf("Status=%q want %q", got.Status, ToolStatusError)
	}
	if got.ErrorCode != "not_found" {
		t.Fatalf("ErrorCode=%q want the ToolError code not_found", got.ErrorCode)
	}
	if !got.Result.IsError || !strings.Contains(got.Result.Content, "does not exist") {
		t.Fatalf("Result=%+v want the ToolError message for the model", got.Result)
	}
}

func TestExecutorMarksToolResultError(t *testing.T) {
	stub := &executorTestTool{
		toolName: "file.read",
		run: func(context.Context, json.RawMessage) (domain.ToolResult, error) {
			return domain.ToolResult{Content: "boom", IsError: true}, nil
		},
	}
	exec := NewToolExecutor(NewToolRegistry(stub), &executorTestAudit{}, time.Second)

	got := exec.Execute(context.Background(), ToolCallInput{
		Call:    domain.ToolCall{ID: "call_1", Name: "file.read", Arguments: `{}`},
		Allowed: []string{"file.read"},
	})
	if got.Status != ToolStatusError {
		t.Fatalf("Status=%q want %q for an IsError result", got.Status, ToolStatusError)
	}
	if !got.Result.IsError || got.Result.Content != "boom" {
		t.Fatalf("Result=%+v want the tool result preserved", got.Result)
	}
}

func TestExecutorMapsCancelledExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	exec := NewToolExecutor(NewToolRegistry(agenttools.NewTestFail()), &executorTestAudit{}, time.Second)

	got := exec.Execute(ctx, ToolCallInput{
		RunID:     8,
		SessionID: 9,
		Call:      domain.ToolCall{ID: "call_1", Name: "test.fail", Arguments: `{"mode":"timeout"}`},
		Allowed:   []string{"test.fail"},
	})
	if got.Status != ToolStatusCancelled {
		t.Fatalf("Status=%q want %q", got.Status, ToolStatusCancelled)
	}
	if got.ErrorCode != "cancelled" {
		t.Fatalf("ErrorCode=%q want cancelled", got.ErrorCode)
	}
	if !got.Result.IsError {
		t.Fatalf("Result=%+v want IsError", got.Result)
	}
}

func TestExecutorRejectsSuccessAfterCancellation(t *testing.T) {
	// The tool ignores the context and returns a result anyway; the run was
	// cancelled while it was in flight, so the execution is not a success.
	stub := &executorTestTool{toolName: "test.echo"}
	exec := NewToolExecutor(NewToolRegistry(stub), nil, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := exec.Execute(ctx, ToolCallInput{
		Call:    domain.ToolCall{ID: "call_1", Name: "test.echo", Arguments: `{"text":"hi"}`},
		Allowed: []string{"test.echo"},
	})
	if got.Status != ToolStatusCancelled {
		t.Fatalf("Status=%q want %q for a cancelled run", got.Status, ToolStatusCancelled)
	}
	if got.ErrorCode != "cancelled" {
		t.Fatalf("ErrorCode=%q want cancelled", got.ErrorCode)
	}
	if !got.Result.IsError {
		t.Fatalf("Result=%+v want IsError", got.Result)
	}
}

func TestExecutorRejectsUnregisteredAllowedTool(t *testing.T) {
	exec := NewToolExecutor(NewToolRegistry(), &executorTestAudit{}, time.Second)

	got := exec.Execute(context.Background(), ToolCallInput{
		Call:    domain.ToolCall{ID: "call_1", Name: "test.echo", Arguments: `{"text":"hi"}`},
		Allowed: []string{"test.echo"},
	})
	if got.Status != ToolStatusError {
		t.Fatalf("Status=%q want %q", got.Status, ToolStatusError)
	}
	if got.ErrorCode != "not_found" {
		t.Fatalf("ErrorCode=%q want not_found", got.ErrorCode)
	}
	if !got.Result.IsError || got.Result.Content == "" {
		t.Fatalf("Result=%+v want IsError with a model-facing message", got.Result)
	}
}

func TestExecutorAuditsSuccessfulInvocation(t *testing.T) {
	const marker = "audit-marker-7"
	stub := &executorTestTool{
		toolName: "test.echo",
		run: func(context.Context, json.RawMessage) (domain.ToolResult, error) {
			return domain.ToolResult{Content: marker}, nil
		},
	}
	audit := &executorTestAudit{}
	exec := NewToolExecutor(NewToolRegistry(stub), audit, time.Second)

	got := exec.Execute(context.Background(), ToolCallInput{
		RunID:     11,
		SessionID: 12,
		Call:      domain.ToolCall{ID: "call_1", Name: "test.echo", Arguments: `{"text":"` + marker + `"}`},
		Allowed:   []string{"test.echo"},
	})
	if got.Status != ToolStatusOK || got.Result.IsError {
		t.Fatalf("execution=%+v want a successful result", got)
	}
	if got.ErrorCode != "" {
		t.Fatalf("ErrorCode=%q want empty on success", got.ErrorCode)
	}
	events := audit.recorded()
	if len(events) != 1 {
		t.Fatalf("audit entries=%d want 1", len(events))
	}
	assertToolAudit(t, events[0], "test.echo", ToolStatusOK, 11, 12)
	encoded, err := json.Marshal(events[0].Metadata)
	if err != nil {
		t.Fatalf("marshal audit metadata: %v", err)
	}
	if strings.Contains(string(encoded), marker) {
		t.Fatalf("audit metadata leaks the arguments/result: %s", encoded)
	}
}

func TestExecutorIgnoresAuditFailure(t *testing.T) {
	silenceToolLogs(t)
	stub := &executorTestTool{toolName: "test.echo"}
	audit := &executorTestAudit{err: errors.New("audit store is down")}
	exec := NewToolExecutor(NewToolRegistry(stub), audit, time.Second)

	got := exec.Execute(context.Background(), ToolCallInput{
		Call:    domain.ToolCall{ID: "call_1", Name: "test.echo", Arguments: `{"text":"hi"}`},
		Allowed: []string{"test.echo"},
	})
	if got.Status != ToolStatusOK || got.Result.IsError {
		t.Fatalf("execution=%+v want the tool result even when auditing fails", got)
	}
	if len(audit.recorded()) != 1 {
		t.Fatalf("audit was not attempted")
	}
}
