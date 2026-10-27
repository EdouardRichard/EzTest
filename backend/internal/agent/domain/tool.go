package domain

import (
	"context"
	"encoding/json"
)

// Tool is one capability the model may invoke. Implementations are registered
// in the application layer's tool registry and exposed to the HTTP layer as
// ToolInfo.
type Tool interface {
	Name() string
	Description() string
	// InputSchema returns the JSON Schema object describing the arguments.
	InputSchema() json.RawMessage
	// Execute runs the tool. A tool-level failure is reported as a ToolResult
	// with IsError set (the run continues); the error return is reserved for
	// infrastructure failures.
	Execute(ctx context.Context, args json.RawMessage) (ToolResult, error)
}

// ToolResult is what a tool reports back to the model. IsError marks a tool
// level failure such as invalid arguments, not a failed run.
type ToolResult struct {
	Content string
	IsError bool
}

// ToolError is a typed tool failure. Code is one of the tool error codes from
// the design spec §8.1, for example "invalid_arguments", "denied" or
// "not_found".
type ToolError struct {
	Code    string
	Message string
}

// Error implements error. The executor type-asserts *ToolError to map the tool
// failure to its execution status; Message is the model-facing description.
func (e *ToolError) Error() string {
	if e == nil {
		return "agent: tool error"
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return "agent: tool error: " + e.Code
	}
	return "agent: tool error"
}

// ToolInfo is the read model exposed by GET /api/agents/tools.
type ToolInfo struct {
	Name        string
	Description string
	Schema      json.RawMessage
}
