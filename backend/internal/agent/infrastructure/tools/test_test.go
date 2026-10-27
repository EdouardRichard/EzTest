package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// testToolSchema is the subset of JSON Schema the test tools are pinned to.
type testToolSchema struct {
	Type       string                        `json:"type"`
	Properties map[string]testToolSchemaProp `json:"properties"`
	Required   []string                      `json:"required"`
}

// testToolSchemaProp is one property of a test tool's input schema.
type testToolSchemaProp struct {
	Type string   `json:"type"`
	Enum []string `json:"enum"`
}

func TestTestToolsEchoEchoesText(t *testing.T) {
	echo := NewTestEcho()
	if got := echo.Name(); got != "test.echo" {
		t.Fatalf("Name()=%q want test.echo", got)
	}
	if echo.Description() == "" {
		t.Fatal("Description() is empty")
	}

	result, err := echo.Execute(context.Background(), json.RawMessage(`{"text":"hello 运行时"}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.IsError {
		t.Fatalf("IsError=true, content=%q", result.Content)
	}
	if result.Content != "hello 运行时" {
		t.Fatalf("Content=%q want the input text unchanged", result.Content)
	}
}

func TestTestToolsEchoRejectsNonStringText(t *testing.T) {
	echo := NewTestEcho()
	_, err := echo.Execute(context.Background(), json.RawMessage(`{"text":12}`))
	if err == nil {
		t.Fatal("Execute accepted a non-string text argument")
	}
	var toolErr *domain.ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("err=%v want *domain.ToolError", err)
	}
	if toolErr.Code != "invalid_arguments" {
		t.Fatalf("ToolError.Code=%q want invalid_arguments", toolErr.Code)
	}
}

func TestTestToolsExposeObjectSchemas(t *testing.T) {
	var echoSchema testToolSchema
	if err := json.Unmarshal(NewTestEcho().InputSchema(), &echoSchema); err != nil {
		t.Fatalf("test.echo schema is not JSON: %v", err)
	}
	if echoSchema.Type != "object" {
		t.Fatalf("test.echo schema type=%q want object", echoSchema.Type)
	}
	if got := echoSchema.Properties["text"].Type; got != "string" {
		t.Fatalf("test.echo text type=%q want string", got)
	}
	if len(echoSchema.Required) != 1 || echoSchema.Required[0] != "text" {
		t.Fatalf("test.echo required=%v want [text]", echoSchema.Required)
	}

	var failSchema testToolSchema
	if err := json.Unmarshal(NewTestFail().InputSchema(), &failSchema); err != nil {
		t.Fatalf("test.fail schema is not JSON: %v", err)
	}
	if failSchema.Type != "object" {
		t.Fatalf("test.fail schema type=%q want object", failSchema.Type)
	}
	mode := failSchema.Properties["mode"]
	if mode.Type != "string" {
		t.Fatalf("test.fail mode type=%q want string", mode.Type)
	}
	wantModes := []string{"error", "timeout", "panic"}
	if len(mode.Enum) != len(wantModes) {
		t.Fatalf("test.fail mode enum=%v want %v", mode.Enum, wantModes)
	}
	for i, want := range wantModes {
		if mode.Enum[i] != want {
			t.Fatalf("test.fail mode enum=%v want %v", mode.Enum, wantModes)
		}
	}
	if got := failSchema.Properties["message"].Type; got != "string" {
		t.Fatalf("test.fail message type=%q want string", got)
	}
	if len(failSchema.Required) != 1 || failSchema.Required[0] != "mode" {
		t.Fatalf("test.fail required=%v want [mode]", failSchema.Required)
	}
}

func TestTestToolsFailErrorModeReturnsToolResultError(t *testing.T) {
	fail := NewTestFail()
	if got := fail.Name(); got != "test.fail" {
		t.Fatalf("Name()=%q want test.fail", got)
	}
	if fail.Description() == "" {
		t.Fatal("Description() is empty")
	}

	result, err := fail.Execute(context.Background(), json.RawMessage(`{"mode":"error","message":"boom"}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("IsError=false, content=%q", result.Content)
	}
	if !strings.Contains(result.Content, "boom") {
		t.Fatalf("Content=%q want it to carry the message", result.Content)
	}

	defaultResult, err := fail.Execute(context.Background(), json.RawMessage(`{"mode":"error"}`))
	if err != nil {
		t.Fatalf("Execute without message returned error: %v", err)
	}
	if !defaultResult.IsError || defaultResult.Content == "" {
		t.Fatalf("default error mode result=%+v want an IsError result with content", defaultResult)
	}
}

func TestTestToolsFailTimeoutRespectsContext(t *testing.T) {
	fail := NewTestFail()

	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		started := time.Now()
		_, err := fail.Execute(ctx, json.RawMessage(`{"mode":"timeout"}`))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err=%v want context.DeadlineExceeded", err)
		}
		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Fatalf("timeout mode took %s, it must stop when the context ends", elapsed)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := fail.Execute(ctx, json.RawMessage(`{"mode":"timeout"}`))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v want context.Canceled", err)
		}
	})
}

func TestTestToolsFailPanics(t *testing.T) {
	fail := NewTestFail()
	panicked := false
	func() {
		defer func() { panicked = recover() != nil }()
		_, _ = fail.Execute(context.Background(), json.RawMessage(`{"mode":"panic"}`))
	}()
	if !panicked {
		t.Fatal("panic mode did not panic")
	}
}

func TestTestToolsFailRejectsUnknownMode(t *testing.T) {
	fail := NewTestFail()
	for _, args := range []string{`{"mode":"explode"}`, `{}`, `{"mode":`} {
		_, err := fail.Execute(context.Background(), json.RawMessage(args))
		if err == nil {
			t.Fatalf("Execute(%s) returned no error", args)
		}
		var toolErr *domain.ToolError
		if !errors.As(err, &toolErr) {
			t.Fatalf("Execute(%s) err=%v want *domain.ToolError", args, err)
		}
		if toolErr.Code != "invalid_arguments" {
			t.Fatalf("Execute(%s) ToolError.Code=%q want invalid_arguments", args, toolErr.Code)
		}
	}
}
