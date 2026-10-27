// Package tools implements the read-only tools available to Agent runs:
// the test fixtures (test.echo / test.fail), the filesystem readers and the
// allow-listed HTTP GET. Tools are registered in the application layer and are
// always guarded by the profile's tool allowlist.
package tools

import (
	"context"
	"encoding/json"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// echoSchema is the JSON Schema of test.echo's arguments.
const echoSchema = `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`

// failSchema is the JSON Schema of test.fail's arguments.
const failSchema = `{"type":"object","properties":{"mode":{"type":"string","enum":["error","timeout","panic"]},"message":{"type":"string"}},"required":["mode"]}`

// failTimeout is how long test.fail's timeout mode waits for the context to
// end. It is deliberately longer than any executor timeout so a real run only
// ever leaves timeout mode through context cancellation.
const failTimeout = 30 * time.Second

// testEcho echoes its text argument unchanged. It exists so the runtime can
// verify the tool pipeline end to end without side effects.
type testEcho struct{}

// NewTestEcho returns the test.echo tool.
func NewTestEcho() domain.Tool { return testEcho{} }

// Name implements domain.Tool.
func (testEcho) Name() string { return "test.echo" }

// Description implements domain.Tool.
func (testEcho) Description() string {
	return "Echo the given text back unchanged. Use it to verify the tool pipeline."
}

// InputSchema implements domain.Tool.
func (testEcho) InputSchema() json.RawMessage { return json.RawMessage(echoSchema) }

// Execute implements domain.Tool.
func (testEcho) Execute(_ context.Context, args json.RawMessage) (domain.ToolResult, error) {
	var in struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return domain.ToolResult{}, &domain.ToolError{Code: "invalid_arguments", Message: "text must be a string"}
	}
	return domain.ToolResult{Content: in.Text}, nil
}

// testFail simulates the three tool failure modes the runtime must isolate:
// a tool-level error, a timeout and a panic.
type testFail struct{}

// NewTestFail returns the test.fail tool.
func NewTestFail() domain.Tool { return testFail{} }

// Name implements domain.Tool.
func (testFail) Name() string { return "test.fail" }

// Description implements domain.Tool.
func (testFail) Description() string {
	return "Simulate a tool failure: mode=error returns a tool error, mode=timeout blocks until the context ends, mode=panic panics."
}

// InputSchema implements domain.Tool.
func (testFail) InputSchema() json.RawMessage { return json.RawMessage(failSchema) }

// Execute implements domain.Tool.
func (testFail) Execute(ctx context.Context, args json.RawMessage) (domain.ToolResult, error) {
	var in struct {
		Mode    string `json:"mode"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return domain.ToolResult{}, &domain.ToolError{Code: "invalid_arguments", Message: "arguments must be an object with a mode"}
	}

	switch in.Mode {
	case "error":
		message := in.Message
		if message == "" {
			message = "test.fail: simulated tool error"
		}
		return domain.ToolResult{Content: message, IsError: true}, nil
	case "timeout":
		select {
		case <-ctx.Done():
			return domain.ToolResult{}, ctx.Err()
		case <-time.After(failTimeout):
			// Safety net: only reached when the executor timeout is set above
			// failTimeout. Report a normal tool-level timeout rather than a
			// success.
			return domain.ToolResult{}, &domain.ToolError{Code: "timeout", Message: "test.fail: timeout mode was not interrupted"}
		}
	case "panic":
		panic("test.fail: simulated panic")
	default:
		return domain.ToolResult{}, &domain.ToolError{
			Code:    "invalid_arguments",
			Message: `mode must be one of "error", "timeout" or "panic"`,
		}
	}
}
