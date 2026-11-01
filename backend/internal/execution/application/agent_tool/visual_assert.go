// Package agenttool exposes the P5 execution-side agent tools. The tools only
// validate parameters and delegate to the execution application services; the
// runtime injects the case_run scope out of band, so the JSON schema never
// contains version/case IDs.
package agenttool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
)

// ToolExecutionVisualAssert is the P5 case-executor visual tool.
const ToolExecutionVisualAssert = "execution.visual_assert"

// VisualAssertPort is the execution service surface consumed by the tool.
// CaseRunner satisfies it.
type VisualAssertPort interface {
	VisualAssert(ctx context.Context, scope tools.Scope, assertionID int64) (any, error)
}

// VisualAssertTool implements tools.Tool for execution.visual_assert
// (contracts/agent-profiles-tools.md §8.3).
type VisualAssertTool struct {
	port VisualAssertPort
}

// NewVisualAssertTool wires the visual assertion tool.
func NewVisualAssertTool(port VisualAssertPort) *VisualAssertTool {
	return &VisualAssertTool{port: port}
}

// Name implements tools.Tool.
func (t *VisualAssertTool) Name() string { return ToolExecutionVisualAssert }

// Description implements tools.Tool.
func (t *VisualAssertTool) Description() string {
	return "Run one configured visual assertion of the current case (capture actual, compare against the approved baseline) and return classification/reason/diff/evidence IDs. Replay-safe: a second call returns the recorded result."
}

// Parameters implements tools.Tool. The scope (case_run/case_version/version)
// is injected by the runtime and is not part of the schema.
func (t *VisualAssertTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"assertionId":{"type":"string","description":"decimal visual_assertion ID from the task context","pattern":"^[0-9]+$"}},"required":["assertionId"],"additionalProperties":false}`)
}

// Execute implements tools.Tool.
func (t *VisualAssertTool) Execute(ctx context.Context, scope tools.Scope, raw json.RawMessage) (any, error) {
	if scope.CaseRunID <= 0 {
		return nil, tools.Invalid("scope", "execution.visual_assert requires the injected case_run scope")
	}
	if t.port == nil {
		return nil, errors.New("execution.visual_assert is not configured")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		trimmed = []byte(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var args struct {
		AssertionID string `json:"assertionId"`
	}
	if err := decoder.Decode(&args); err != nil {
		return nil, tools.Invalid("parameters", "invalid parameters: "+err.Error())
	}
	trimmedID := strings.TrimSpace(args.AssertionID)
	parsed, err := strconv.ParseInt(trimmedID, 10, 64)
	if err != nil || parsed <= 0 {
		return nil, tools.Invalid("assertionId", "must be a positive decimal ID")
	}
	return t.port.VisualAssert(ctx, scope, parsed)
}
