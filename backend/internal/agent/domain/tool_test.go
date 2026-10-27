package domain_test

import (
	"encoding/json"
	"errors"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

func TestToolErrorCarriesCodeAndMessage(t *testing.T) {
	var err error = &agent.ToolError{Code: "not_found", Message: "file does not exist"}
	var toolErr *agent.ToolError
	if !errors.As(err, &toolErr) {
		t.Fatal("errors.As did not match *domain.ToolError")
	}
	if toolErr.Code != "not_found" || toolErr.Message != "file does not exist" {
		t.Fatalf("ToolError=%+v", toolErr)
	}
	if !errors.Is(err, err) {
		t.Fatal("ToolError must be comparable through its own error value")
	}
}

func TestToolInfoKeepsRawSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`)
	info := agent.ToolInfo{Name: "test.echo", Description: "echoes text", Schema: schema}
	if info.Name != "test.echo" || info.Description != "echoes text" {
		t.Fatalf("ToolInfo=%+v", info)
	}
	var decoded map[string]any
	if err := json.Unmarshal(info.Schema, &decoded); err != nil {
		t.Fatalf("ToolInfo.Schema is not valid JSON: %v", err)
	}
	if decoded["type"] != "object" {
		t.Fatalf("ToolInfo.Schema lost its shape: %s", info.Schema)
	}
}

func TestToolResultFlagsErrors(t *testing.T) {
	ok := agent.ToolResult{Content: "hello"}
	if ok.IsError {
		t.Fatal("zero ToolResult must not be an error result")
	}
	failed := agent.ToolResult{Content: "boom", IsError: true}
	if !failed.IsError || failed.Content != "boom" {
		t.Fatalf("ToolResult=%+v", failed)
	}
}
