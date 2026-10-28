package application_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// timeoutTool implements domain.Tool and domain.ToolWithTimeout. Run is called
// with the executor-provided context so a test can observe the effective
// deadline or wait for it to expire.
type timeoutTool struct {
	toolName string
	timeout  time.Duration
	run      func(ctx context.Context) (domain.ToolResult, error)
}

// Name implements domain.Tool.
func (t *timeoutTool) Name() string {
	if t.toolName == "" {
		return "test.timeout"
	}
	return t.toolName
}

// Description implements domain.Tool.
func (t *timeoutTool) Description() string { return "tool with a per-tool timeout" }

// InputSchema implements domain.Tool.
func (t *timeoutTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

// Execute implements domain.Tool.
func (t *timeoutTool) Execute(ctx context.Context, _ json.RawMessage) (domain.ToolResult, error) {
	if t.run != nil {
		return t.run(ctx)
	}
	return domain.ToolResult{Content: "ok"}, nil
}

// Timeout implements domain.ToolWithTimeout.
func (t *timeoutTool) Timeout() time.Duration { return t.timeout }

// plainTimeoutTool intentionally does not implement domain.ToolWithTimeout: it
// must keep using the executor's global timeout.
type plainTimeoutTool struct {
	run func(ctx context.Context) (domain.ToolResult, error)
}

// Name implements domain.Tool.
func (t *plainTimeoutTool) Name() string { return "test.plain" }

// Description implements domain.Tool.
func (t *plainTimeoutTool) Description() string { return "tool without a per-tool timeout" }

// InputSchema implements domain.Tool.
func (t *plainTimeoutTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

// Execute implements domain.Tool.
func (t *plainTimeoutTool) Execute(ctx context.Context, _ json.RawMessage) (domain.ToolResult, error) {
	if t.run != nil {
		return t.run(ctx)
	}
	return domain.ToolResult{Content: "ok"}, nil
}

// waitForCancel blocks until the executor timeout cancels the context and
// reports the cancellation as the tool error.
func waitForCancel(ctx context.Context) (domain.ToolResult, error) {
	<-ctx.Done()
	return domain.ToolResult{}, ctx.Err()
}

// TestToolTimeoutOverridesGlobal pins the P6 rule: a tool that implements
// ToolWithTimeout uses its own duration even when the global executor timeout is
// much shorter.
func TestToolTimeoutOverridesGlobal(t *testing.T) {
	tool := &timeoutTool{toolName: "test.timeout", timeout: 250 * time.Millisecond, run: waitForCancel}
	exec := application.NewToolExecutor(application.NewToolRegistry(tool), nil, 20*time.Millisecond)

	started := time.Now()
	got := exec.Execute(context.Background(), application.ToolCallInput{
		Call:    domain.ToolCall{ID: "call_1", Name: tool.Name(), Arguments: `{}`},
		Allowed: []string{tool.Name()},
	})
	elapsed := time.Since(started)

	if got.Status != application.ToolStatusTimeout {
		t.Fatalf("status=%q want %q (execution=%+v)", got.Status, application.ToolStatusTimeout, got)
	}
	if elapsed < 120*time.Millisecond {
		t.Fatalf("tool timed out after %s: the global 20ms timeout won over the tool's 250ms timeout", elapsed)
	}
}

// TestToolWithoutTimeoutUsesGlobal pins the compatibility rule: tools that do
// not implement ToolWithTimeout keep the global executor timeout.
func TestToolWithoutTimeoutUsesGlobal(t *testing.T) {
	tool := &plainTimeoutTool{run: waitForCancel}
	exec := application.NewToolExecutor(application.NewToolRegistry(tool), nil, 25*time.Millisecond)

	started := time.Now()
	got := exec.Execute(context.Background(), application.ToolCallInput{
		Call:    domain.ToolCall{ID: "call_1", Name: tool.Name(), Arguments: `{}`},
		Allowed: []string{tool.Name()},
	})
	elapsed := time.Since(started)

	if got.Status != application.ToolStatusTimeout {
		t.Fatalf("status=%q want %q (execution=%+v)", got.Status, application.ToolStatusTimeout, got)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("tool timed out after %s, want the global 25ms timeout", elapsed)
	}
}

// TestToolTimeoutZeroUsesGlobal pins that a non-positive per-tool timeout means
// "no override", not "disable the timeout".
func TestToolTimeoutZeroUsesGlobal(t *testing.T) {
	tool := &timeoutTool{toolName: "test.timeout", timeout: 0, run: waitForCancel}
	exec := application.NewToolExecutor(application.NewToolRegistry(tool), nil, 25*time.Millisecond)

	got := exec.Execute(context.Background(), application.ToolCallInput{
		Call:    domain.ToolCall{ID: "call_1", Name: tool.Name(), Arguments: `{}`},
		Allowed: []string{tool.Name()},
	})
	if got.Status != application.ToolStatusTimeout {
		t.Fatalf("status=%q want %q (execution=%+v)", got.Status, application.ToolStatusTimeout, got)
	}
}

// TestToolTimeoutCappedAtSixtySeconds pins the 60s ceiling on a per-tool
// timeout. The tool records the remaining deadline instead of waiting for it.
func TestToolTimeoutCappedAtSixtySeconds(t *testing.T) {
	var remaining time.Duration
	var hasDeadline bool
	tool := &timeoutTool{
		toolName: "test.timeout",
		timeout:  5 * time.Minute,
		run: func(ctx context.Context) (domain.ToolResult, error) {
			deadline, ok := ctx.Deadline()
			hasDeadline = ok
			if ok {
				remaining = time.Until(deadline)
			}
			return domain.ToolResult{Content: "ok"}, nil
		},
	}
	// A short global timeout proves the tool value (not the global value) is
	// what gets capped.
	exec := application.NewToolExecutor(application.NewToolRegistry(tool), nil, 10*time.Millisecond)

	got := exec.Execute(context.Background(), application.ToolCallInput{
		Call:    domain.ToolCall{ID: "call_1", Name: tool.Name(), Arguments: `{}`},
		Allowed: []string{tool.Name()},
	})
	if got.Status != application.ToolStatusOK {
		t.Fatalf("status=%q want %q (execution=%+v)", got.Status, application.ToolStatusOK, got)
	}
	if !hasDeadline {
		t.Fatal("tool context has no deadline, want the capped per-tool timeout")
	}
	if remaining > 60*time.Second || remaining < 55*time.Second {
		t.Fatalf("remaining deadline=%s want the per-tool timeout capped at 60s", remaining)
	}
}

// TestToolInvocationCarriesBatchAndCaseRun pins the P6 additive fields: the
// executor forwards BatchID and CaseRunID to the tool unchanged (zero when the
// caller does not set them).
func TestToolInvocationCarriesBatchAndCaseRun(t *testing.T) {
	var seen application.Invocation
	tool := &invocationTestTool{
		toolName: "test.echo",
		execute: func(ctx context.Context, _ json.RawMessage) (domain.ToolResult, error) {
			seen, _ = application.InvocationFromContext(ctx)
			return domain.ToolResult{Content: "ok"}, nil
		},
	}
	exec := application.NewToolExecutor(application.NewToolRegistry(tool), nil, time.Second)

	got := exec.Execute(context.Background(), application.ToolCallInput{
		RunID:     7,
		SessionID: 9,
		BatchID:   41,
		CaseRunID: 42,
		Call:      domain.ToolCall{ID: "call_1", Name: "test.echo", Arguments: `{}`},
		Allowed:   []string{"test.echo"},
	})
	if got.Status != application.ToolStatusOK {
		t.Fatalf("status=%q want %q (execution=%+v)", got.Status, application.ToolStatusOK, got)
	}
	if seen.BatchID != 41 || seen.CaseRunID != 42 {
		t.Fatalf("invocation batch=%d case_run=%d want 41/42", seen.BatchID, seen.CaseRunID)
	}
}

// TestInvocationRoundTripKeepsP6Fields pins "carried through ctx unchanged": a
// value stored by WithToolInvocation comes back untouched.
func TestInvocationRoundTripKeepsP6Fields(t *testing.T) {
	actor := int64(42)
	want := application.Invocation{
		SessionID:   9,
		RunID:       7,
		ActorUserID: &actor,
		ProfileKey:  "execution-agent",
		BatchID:     41,
		CaseRunID:   42,
	}
	got, ok := application.InvocationFromContext(application.WithToolInvocation(context.Background(), want))
	if !ok {
		t.Fatal("InvocationFromContext returned ok=false for a context built by WithToolInvocation")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("invocation=%+v want %+v", got, want)
	}
}

// TestInvocationCarriesNoCredentialsOrPrompts pins the P6 review focus:
// Invocation is run-scoped identity only. Adding a prompt, credential, secret or
// payload field to it must fail this test rather than leak into tool contexts.
func TestInvocationCarriesNoCredentialsOrPrompts(t *testing.T) {
	want := []string{"SessionID", "RunID", "ActorUserID", "ProfileKey", "BatchID", "CaseRunID"}
	typ := reflect.TypeOf(application.Invocation{})
	got := make([]string, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		got[i] = typ.Field(i).Name
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Invocation fields=%v want exactly %v (never carry credentials, prompts or payloads)", got, want)
	}
}
