package versionapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	agenttools "github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// p4Tool returns the named tool from a version-steward group.
func p4Tool(t *testing.T, toolList []agenttools.Tool, name string) agenttools.Tool {
	t.Helper()
	for _, tool := range toolList {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("tool %q is not registered in %v", name, toolNames(toolList))
	return nil
}

func toolNames(toolList []agenttools.Tool) []string {
	names := make([]string, 0, len(toolList))
	for _, tool := range toolList {
		names = append(names, tool.Name())
	}
	sort.Strings(names)
	return names
}

// toolError asserts err is a model-visible *tools.Error and returns it.
func toolError(t *testing.T, err error) *agenttools.Error {
	t.Helper()
	if err == nil {
		t.Fatal("expected a tool error, got nil")
	}
	var toolErr *agenttools.Error
	if !errors.As(err, &toolErr) {
		t.Fatalf("error %v is not a *tools.Error", err)
	}
	return toolErr
}

func TestP4ToolsFactoryExposesFrozenStewardTools(t *testing.T) {
	closureEnv := newClosureEnv(t)
	closeEnv := newVersionCloseEnv(t)

	toolList := versionapp.VersionTools(closureEnv.svc, closeEnv.svc)
	want := []string{"requirement.goal_met", "version.close_propose", "version.close_read"}
	if got := toolNames(toolList); !reflect.DeepEqual(got, want) {
		t.Fatalf("VersionTools names = %v, want %v", got, want)
	}
	for _, tool := range toolList {
		if tool.Description() == "" {
			t.Fatalf("tool %q has an empty description", tool.Name())
		}
		schema := string(tool.Parameters())
		if strings.Contains(schema, "version_id") || strings.Contains(schema, "versionId") {
			t.Fatalf("tool %q schema exposes the injected version scope: %s", tool.Name(), schema)
		}
		var decoded map[string]any
		if err := json.Unmarshal(tool.Parameters(), &decoded); err != nil {
			t.Fatalf("tool %q schema is not a JSON object: %v", tool.Name(), err)
		}
		if decoded["additionalProperties"] != false {
			t.Fatalf("tool %q schema must reject unknown parameters: %s", tool.Name(), schema)
		}
	}

	// The model can never inject the version scope: unknown properties are
	// rejected by the strict decoder.
	readTool := p4Tool(t, toolList, "version.close_read")
	_, err := readTool.Execute(context.Background(), agenttools.Scope{VersionID: 701}, json.RawMessage(`{"version_id":"999"}`))
	if toolErr := toolError(t, err); toolErr.Code != agenttools.CodeInvalidArgument {
		t.Fatalf("version.close_read scope injection error = %#v, want invalid_argument", toolErr)
	}
	proposeTool := p4Tool(t, toolList, "version.close_propose")
	_, err = proposeTool.Execute(context.Background(), agenttools.Scope{VersionID: 701}, json.RawMessage(`{"reason":"done","version_id":"999"}`))
	if toolErr := toolError(t, err); toolErr.Code != agenttools.CodeInvalidArgument {
		t.Fatalf("version.close_propose scope injection error = %#v, want invalid_argument", toolErr)
	}

	// version.close_propose requires a reason in the frozen schema.
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(proposeTool.Parameters(), &schema); err != nil {
		t.Fatalf("decode version.close_propose schema: %v", err)
	}
	if !reflect.DeepEqual(schema.Required, []string{"reason"}) {
		t.Fatalf("version.close_propose required = %v, want [reason]", schema.Required)
	}
}

func TestP4ToolsRequirementGoalMetClosesScopedRequirement(t *testing.T) {
	env := newClosureEnv(t)
	requirement := seedClosureRequirement(env, 1001, 701, 1002, version.RequirementClarified)
	configurePassingClosure(env, requirement.ID)

	tool := p4Tool(t, versionapp.VersionTools(env.svc, nil), "requirement.goal_met")
	out, err := tool.Execute(context.Background(), agenttools.Scope{
		VersionID:            701,
		RequirementID:        1001,
		RequirementVersionID: 1002,
		AgentRunID:           9001,
	}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("requirement.goal_met: %v", err)
	}
	payload, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("requirement.goal_met result = %#v, want a JSON-ready map", out)
	}
	if payload["requirement_id"] != "1001" || payload["requirement_version_id"] != "1002" {
		t.Fatalf("requirement.goal_met ids = %#v", payload)
	}
	if payload["close_round"] != 1 {
		t.Fatalf("close_round = %#v, want 1", payload["close_round"])
	}
	if payload["closed_at"] != "2026-10-31T12:00:00Z" {
		t.Fatalf("closed_at = %#v", payload["closed_at"])
	}
	checks, ok := payload["checks"].(map[string]bool)
	if !ok || len(checks) != 8 {
		t.Fatalf("checks = %#v, want the eight closure checks", payload["checks"])
	}
	if !checks["requirement_state"] || !checks["no_open_work"] {
		t.Fatalf("checks = %#v", checks)
	}
	stored := env.requirements.requirements[requirement.ID]
	if stored.Status != version.RequirementClosed || stored.ClosedByRunID == nil || *stored.ClosedByRunID != 9001 {
		t.Fatalf("stored requirement = %#v, want closed by agent run 9001", stored)
	}
}

func TestP4ToolsRequirementGoalMetPredicateFailureReturnsFailedChecks(t *testing.T) {
	env := newClosureEnv(t)
	requirement := seedClosureRequirement(env, 1001, 701, 1002, version.RequirementClarified)
	configurePassingClosure(env, requirement.ID)
	// One foreign open work item: no_open_work is the only failing check.
	env.work.counts[requirement.ID] = versionapp.OpenWorkItemCounts{Total: 2, RequirementClose: 1}

	tool := p4Tool(t, versionapp.VersionTools(env.svc, nil), "requirement.goal_met")
	_, err := tool.Execute(context.Background(), agenttools.Scope{
		VersionID: 701, RequirementID: requirement.ID, RequirementVersionID: 1002,
	}, json.RawMessage(`{}`))
	toolErr := toolError(t, err)
	if toolErr.Code != agenttools.CodeStateConflict {
		t.Fatalf("predicate failure code = %q, want state_conflict", toolErr.Code)
	}
	failed, ok := toolErr.Details["failed"].(map[string]int)
	if !ok {
		t.Fatalf("predicate failure details = %#v, want failed counts", toolErr.Details)
	}
	if failed["no_open_work"] != 1 {
		t.Fatalf("failed = %#v, want no_open_work=1", failed)
	}
	if stored := env.requirements.requirements[requirement.ID]; stored.Status != version.RequirementClarified {
		t.Fatalf("requirement status = %q, want clarified after a rejected proposal", stored.Status)
	}
}

func TestP4ToolsRequirementGoalMetRejectsCrossVersionScope(t *testing.T) {
	env := newClosureEnv(t)
	requirement := seedClosureRequirement(env, 1001, 701, 1002, version.RequirementClarified)
	configurePassingClosure(env, requirement.ID)

	tool := p4Tool(t, versionapp.VersionTools(env.svc, nil), "requirement.goal_met")
	_, err := tool.Execute(context.Background(), agenttools.Scope{
		VersionID: 999, RequirementID: requirement.ID, RequirementVersionID: 1002,
	}, json.RawMessage(`{}`))
	if toolErr := toolError(t, err); toolErr.Code != agenttools.CodeNotFound {
		t.Fatalf("cross-version scope error = %#v, want not_found", toolErr)
	}
	if stored := env.requirements.requirements[requirement.ID]; stored.Status != version.RequirementClarified {
		t.Fatalf("cross-version proposal changed the requirement: %#v", stored)
	}
}

func TestP4ToolsRequirementGoalMetRejectsMissingScope(t *testing.T) {
	env := newClosureEnv(t)

	tool := p4Tool(t, versionapp.VersionTools(env.svc, nil), "requirement.goal_met")
	_, err := tool.Execute(context.Background(), agenttools.Scope{VersionID: 701}, json.RawMessage(`{}`))
	if toolErr := toolError(t, err); toolErr.Code != agenttools.CodeInvalidArgument {
		t.Fatalf("missing requirement scope error = %#v, want invalid_argument", toolErr)
	}
}

func TestP4ToolsRequirementGoalMetRejectsForeignTaskRequirement(t *testing.T) {
	env := newClosureEnv(t)
	target := seedClosureRequirement(env, 1001, 701, 1002, version.RequirementClarified)
	configurePassingClosure(env, target.ID)
	other := seedClosureRequirement(env, 1002, 701, 1003, version.RequirementClarified)
	configurePassingClosure(env, other.ID)

	tool := p4Tool(t, versionapp.VersionTools(env.svc, nil), "requirement.goal_met")
	_, err := tool.Execute(context.Background(), agenttools.Scope{
		VersionID: 701, RequirementID: target.ID, RequirementVersionID: 1002,
	}, json.RawMessage(`{"requirement_id":"1002"}`))
	if toolErr := toolError(t, err); toolErr.Code != agenttools.CodeInvalidArgument {
		t.Fatalf("foreign task requirement error = %#v, want invalid_argument", toolErr)
	}
	if stored := env.requirements.requirements[other.ID]; stored.Status != version.RequirementClarified {
		t.Fatalf("foreign requirement was modified: %#v", stored)
	}
}

func TestP4ToolsRequirementGoalMetRejectsStaleTaskRevision(t *testing.T) {
	env := newClosureEnv(t)
	requirement := seedClosureRequirement(env, 1001, 701, 1002, version.RequirementClarified)
	configurePassingClosure(env, requirement.ID)

	tool := p4Tool(t, versionapp.VersionTools(env.svc, nil), "requirement.goal_met")
	_, err := tool.Execute(context.Background(), agenttools.Scope{
		VersionID: 701, RequirementID: requirement.ID, RequirementVersionID: 9999,
	}, json.RawMessage(`{}`))
	if toolErr := toolError(t, err); toolErr.Code != agenttools.CodeStateConflict {
		t.Fatalf("stale revision error = %#v, want state_conflict", toolErr)
	}
	if stored := env.requirements.requirements[requirement.ID]; stored.Status != version.RequirementClarified {
		t.Fatalf("stale task changed the requirement: %#v", stored)
	}
}

func TestP4ToolsRequirementGoalMetRejectsUnknownRequirement(t *testing.T) {
	env := newClosureEnv(t)

	tool := p4Tool(t, versionapp.VersionTools(env.svc, nil), "requirement.goal_met")
	_, err := tool.Execute(context.Background(), agenttools.Scope{VersionID: 701}, json.RawMessage(`{"requirement_id":"424242"}`))
	if toolErr := toolError(t, err); toolErr.Code != agenttools.CodeNotFound {
		t.Fatalf("unknown requirement error = %#v, want not_found", toolErr)
	}
}

func TestP4ToolsVersionCloseReadReturnsEightChecks(t *testing.T) {
	env := newVersionCloseEnv(t)
	configurePassingVersionClose(env, 701)

	readTool := p4Tool(t, versionapp.VersionTools(nil, env.svc), "version.close_read")
	out, err := readTool.Execute(context.Background(), agenttools.Scope{VersionID: 701}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("version.close_read: %v", err)
	}
	payload, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("version.close_read result = %#v, want a JSON-ready map", out)
	}
	if payload["version_id"] != "701" || payload["passed"] != true {
		t.Fatalf("version.close_read payload = %#v", payload)
	}
	checks, ok := payload["checks"].(map[string]bool)
	if !ok || len(checks) != 8 {
		t.Fatalf("checks = %#v, want the eight version close conditions", payload["checks"])
	}
	for _, name := range []string{
		"version_running", "no_blocking_work_items", "no_active_intents",
		"no_open_human_requests", "no_active_waves", "no_inflight_case_runs",
		"requirements_closed", "version_report_published",
	} {
		if !checks[name] {
			t.Fatalf("check %q = false in a fully passing predicate: %#v", name, checks)
		}
	}
	failed, ok := payload["failed"].([]string)
	if !ok || len(failed) != 0 {
		t.Fatalf("failed = %#v, want an empty list", payload["failed"])
	}

	// A blocked close reports the failing condition names.
	env.work.counts = versionapp.BlockingWorkItemCounts{Total: 3, VersionClose: 1}
	out, err = readTool.Execute(context.Background(), agenttools.Scope{VersionID: 701}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("version.close_read blocked: %v", err)
	}
	payload = out.(map[string]any)
	if payload["passed"] != false {
		t.Fatalf("passed = %#v, want false", payload["passed"])
	}
	failed = payload["failed"].([]string)
	if !reflect.DeepEqual(failed, []string{"no_blocking_work_items"}) {
		t.Fatalf("failed = %v, want [no_blocking_work_items]", failed)
	}
}

func TestP4ToolsVersionCloseReadRequiresInjectedScope(t *testing.T) {
	env := newVersionCloseEnv(t)

	readTool := p4Tool(t, versionapp.VersionTools(nil, env.svc), "version.close_read")
	_, err := readTool.Execute(context.Background(), agenttools.Scope{}, json.RawMessage(`{}`))
	if toolErr := toolError(t, err); toolErr.Code != agenttools.CodeInvalidArgument {
		t.Fatalf("missing version scope error = %#v, want invalid_argument", toolErr)
	}
}

func TestP4ToolsVersionCloseProposeClosesScopedVersion(t *testing.T) {
	env := newVersionCloseEnv(t)
	configurePassingVersionClose(env, 701)

	proposeTool := p4Tool(t, versionapp.VersionTools(nil, env.svc), "version.close_propose")
	out, err := proposeTool.Execute(context.Background(), agenttools.Scope{
		VersionID: 701, AgentRunID: 9001,
	}, json.RawMessage(`{"reason":"all delivery work is done"}`))
	if err != nil {
		t.Fatalf("version.close_propose: %v", err)
	}
	payload, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("version.close_propose result = %#v, want a JSON-ready map", out)
	}
	if payload["version_id"] != "701" || payload["status"] != "completed" {
		t.Fatalf("version.close_propose payload = %#v", payload)
	}
	if payload["close_reason"] != "all delivery work is done" || payload["closed_at"] != "2026-10-31T12:00:00Z" {
		t.Fatalf("version.close_propose close fields = %#v", payload)
	}
	if payload["close_run_id"] != "9001" {
		t.Fatalf("close_run_id = %#v, want the injected agent run", payload["close_run_id"])
	}
	stored := env.versions.versions[701]
	if stored.Status != version.StatusCompleted || stored.CloseReason != "all delivery work is done" {
		t.Fatalf("stored version = %#v, want completed", stored)
	}
}

func TestP4ToolsVersionCloseProposePredicateFailureReturnsFailedChecks(t *testing.T) {
	env := newVersionCloseEnv(t)
	configurePassingVersionClose(env, 701)
	env.requirements.items = []version.Requirement{{ID: 1001, VersionID: 701, Status: version.RequirementClarified}}

	proposeTool := p4Tool(t, versionapp.VersionTools(nil, env.svc), "version.close_propose")
	_, err := proposeTool.Execute(context.Background(), agenttools.Scope{VersionID: 701}, json.RawMessage(`{"reason":"try it"}`))
	toolErr := toolError(t, err)
	if toolErr.Code != agenttools.CodeStateConflict {
		t.Fatalf("predicate failure code = %q, want state_conflict", toolErr.Code)
	}
	failed, ok := toolErr.Details["failed"].(map[string]int)
	if !ok || failed["requirements_closed"] != 1 {
		t.Fatalf("failed = %#v, want requirements_closed=1", toolErr.Details)
	}
	if stored := env.versions.versions[701]; stored.Status != version.StatusRunning {
		t.Fatalf("rejected proposal changed the version: %#v", stored)
	}
}

func TestP4ToolsVersionCloseProposeValidatesReason(t *testing.T) {
	env := newVersionCloseEnv(t)
	configurePassingVersionClose(env, 701)
	proposeTool := p4Tool(t, versionapp.VersionTools(nil, env.svc), "version.close_propose")
	ctx := context.Background()
	scope := agenttools.Scope{VersionID: 701}

	for _, params := range []string{`{}`, `{"reason":"   "}`, `{}`} {
		_, err := proposeTool.Execute(ctx, scope, json.RawMessage(params))
		toolErr := toolError(t, err)
		if toolErr.Code != agenttools.CodeInvalidArgument {
			t.Fatalf("params %s error = %#v, want invalid_argument", params, toolErr)
		}
	}
	_, err := proposeTool.Execute(ctx, scope, json.RawMessage(`{"reason":"`+strings.Repeat("x", version.VersionCloseReasonMax+1)+`"}`))
	if toolErr := toolError(t, err); toolErr.Code != agenttools.CodeInvalidArgument {
		t.Fatalf("overlong reason error = %#v, want invalid_argument", toolErr)
	}
	if stored := env.versions.versions[701]; stored.Status != version.StatusRunning {
		t.Fatalf("invalid proposals changed the version: %#v", stored)
	}
}
