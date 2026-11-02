package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// fakeDesignPort implements DesignPort for the P2 tool tests.
type fakeDesignPort struct {
	readVersionID     int64
	readRequirementID int64
	readCaseID        int64
	readCalls         int
	readResult        any
	readErr           error

	proposeVersionID int64
	proposeActorID   int64
	proposeRunID     int64
	proposeInput     GraphProposal
	proposeCalls     int
	proposeResult    any
	proposeErr       error

	reviewVersionID     int64
	reviewRequirementID int64
	reviewRunID         int64
	reviewInput         ReviewInput
	reviewCalls         int
	reviewResult        any
	reviewErr           error

	dataPlanVersionID     int64
	dataPlanCaseVersionID int64
	dataPlanRunID         int64
	dataPlanInput         DataPlanInput
	dataPlanCalls         int
	dataPlanResult        any
	dataPlanErr           error
}

func (f *fakeDesignPort) ReadGraph(_ context.Context, versionID, requirementID, caseID int64) (any, error) {
	f.readCalls++
	f.readVersionID = versionID
	f.readRequirementID = requirementID
	f.readCaseID = caseID
	return f.readResult, f.readErr
}

func (f *fakeDesignPort) ProposeGraph(_ context.Context, versionID int64, in GraphProposal, actorID, runID int64) (any, error) {
	f.proposeCalls++
	f.proposeVersionID = versionID
	f.proposeInput = in
	f.proposeActorID = actorID
	f.proposeRunID = runID
	return f.proposeResult, f.proposeErr
}

func (f *fakeDesignPort) SubmitReview(_ context.Context, versionID, requirementID int64, runID int64, in ReviewInput) (any, error) {
	f.reviewCalls++
	f.reviewVersionID = versionID
	f.reviewRequirementID = requirementID
	f.reviewRunID = runID
	f.reviewInput = in
	return f.reviewResult, f.reviewErr
}

func (f *fakeDesignPort) SubmitDataPlan(_ context.Context, versionID, caseVersionID int64, runID int64, in DataPlanInput) (any, error) {
	f.dataPlanCalls++
	f.dataPlanVersionID = versionID
	f.dataPlanCaseVersionID = caseVersionID
	f.dataPlanRunID = runID
	f.dataPlanInput = in
	return f.dataPlanResult, f.dataPlanErr
}

func validGraphProposalJSON() string {
	return `{
  "requirement_id": "501",
  "requirement_version_id": "700",
  "scenarios": [
    {"ref": "s1", "name": "正常登录", "description": "手机号密码登录", "version_sut_ids": ["9001"], "page_ids": ["9101"], "api_ids": ["9201"], "sort_order": 0}
  ],
  "cases": [
    {
      "ref": "c1",
      "scenario_ref": "s1",
      "title": "账号密码登录成功",
      "description": "使用有效账号登录",
      "preconditions": "已注册账号",
      "test_data": {"phone": "13800000000"},
      "priority": "P1",
      "default_version_sut_id": "9001",
      "version_sut_ids": ["9001"],
      "page_ids": ["9101"],
      "api_ids": ["9201"],
      "steps": [
        {"seq": 1, "action": "打开登录页", "expected": "显示表单", "target_version_sut_id": "9001", "target_page_id": "9101", "account_role": "admin", "data": {}},
        {"seq": 2, "action": "提交账号密码", "expected": "登录成功", "target_api_id": "9201", "data": {}}
      ],
      "assertions": [
        {"step_seq": 1, "kind": "ui", "target": "登录表单", "expected": "表单可见"},
        {"step_seq": 2, "kind": "api", "target": "POST /login", "expected": "200"}
      ],
      "sources": [
        {"kind": "requirement", "ref_id": "700", "path": "", "line": null, "sha": ""}
      ]
    }
  ]
}`
}

func executeDesignTool(t *testing.T, registry *Registry, scope Scope, name, payload string) (any, error) {
	t.Helper()
	return registry.Execute(context.Background(), scope, name, json.RawMessage(payload))
}

func TestDesignToolsRegisteredAndCaseConfirmAbsent(t *testing.T) {
	registry := NewP2Registry(Deps{Design: &fakeDesignPort{}})
	for _, name := range []string{ToolDesignGraphRead, ToolCaseGraphPropose, ToolDesignReviewSubmit, ToolDesignDataPlanSubmit} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("design tool %q is not registered", name)
		}
	}
	for _, forbidden := range []string{
		"case.confirm",
		"design.scenario_propose",
		"design.case_propose",
		"design.case_update",
		"design.scenario_propose",
	} {
		if _, ok := registry.Get(forbidden); ok {
			t.Fatalf("tool %q must never be registered as a model tool", forbidden)
		}
	}

	// Registering the P2 set must not change the frozen P1 registration order.
	p1 := NewP1Registry(Deps{})
	if names := p1.Names(); len(names) != len(P1ToolNames()) {
		t.Fatalf("P1 registry names = %v", names)
	}
}

func TestDesignSchemasDoNotExposeScope(t *testing.T) {
	registry := NewP2Registry(Deps{Design: &fakeDesignPort{}})
	definitions := registry.Definitions([]string{
		ToolDesignGraphRead, ToolCaseGraphPropose, ToolDesignReviewSubmit, ToolDesignDataPlanSubmit,
	})
	if len(definitions) != 4 {
		t.Fatalf("definitions = %d", len(definitions))
	}
	for _, definition := range definitions {
		raw := strings.ToLower(string(definition.Parameters))
		// requirement_version_id is a legitimate business ID; only a bare
		// version_id property would leak the injected version scope.
		if strings.Contains(raw, `"version_id"`) || strings.Contains(raw, `"versionid"`) {
			t.Fatalf("tool %s schema leaks a version scope parameter: %s", definition.Name, definition.Parameters)
		}
		var schema map[string]any
		if err := json.Unmarshal(definition.Parameters, &schema); err != nil {
			t.Fatalf("tool %s schema is not JSON: %v", definition.Name, err)
		}
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Fatalf("tool %s schema = %s", definition.Name, definition.Parameters)
		}
		if strings.TrimSpace(definition.Description) == "" {
			t.Fatalf("tool %s has no description", definition.Name)
		}
	}
}

func TestGraphReadToolUsesInjectedScope(t *testing.T) {
	port := &fakeDesignPort{readResult: map[string]any{"graph_hash": "hash"}}
	registry := NewP2Registry(Deps{Design: port})

	result, err := executeDesignTool(t, registry, Scope{VersionID: 10, RequirementID: 501}, ToolDesignGraphRead, `{}`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if port.readCalls != 1 || port.readVersionID != 10 || port.readRequirementID != 501 || port.readCaseID != 0 {
		t.Fatalf("read call = %#v", port)
	}
	if got := result.(map[string]any)["graph_hash"]; got != "hash" {
		t.Fatalf("result = %#v", result)
	}

	if _, err := executeDesignTool(t, registry, Scope{VersionID: 10, RequirementID: 501}, ToolDesignGraphRead, `{"case_id":"7001"}`); err != nil {
		t.Fatalf("case read: %v", err)
	}
	if port.readCaseID != 7001 || port.readRequirementID != 501 {
		t.Fatalf("case read call = %#v", port)
	}

	// A requirement-scoped run cannot read another requirement.
	_, err = executeDesignTool(t, registry, Scope{VersionID: 10, RequirementID: 501}, ToolDesignGraphRead, `{"requirement_id":"999"}`)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument {
		t.Fatalf("cross-scope read code = %q", toolErr.Code)
	}
	// The model cannot inject a version id.
	_, err = executeDesignTool(t, registry, Scope{VersionID: 10}, ToolDesignGraphRead, `{"version_id":"99"}`)
	if toolErr = asToolError(t, err); toolErr.Code != CodeInvalidArgument {
		t.Fatalf("version injection code = %q", toolErr.Code)
	}
}

func TestGraphProposeToolValidatesScopeAndPayload(t *testing.T) {
	port := &fakeDesignPort{proposeResult: map[string]any{"graph_hash": "after"}}
	registry := NewP2Registry(Deps{Design: port})
	scope := Scope{VersionID: 10, RequirementID: 501, RequirementVersionID: 700, AgentRunID: 77}

	if _, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, validGraphProposalJSON()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if port.proposeCalls != 1 || port.proposeVersionID != 10 || port.proposeActorID != 0 || port.proposeRunID != 77 {
		t.Fatalf("propose call = %#v", port)
	}
	proposal := port.proposeInput
	if proposal.RequirementID != 501 || proposal.RequirementVersionID != 700 {
		t.Fatalf("proposal ids = %#v", proposal)
	}
	if len(proposal.Scenarios) != 1 || proposal.Scenarios[0].Ref != "s1" || proposal.Scenarios[0].Name != "正常登录" {
		t.Fatalf("scenarios = %#v", proposal.Scenarios)
	}
	if len(proposal.Scenarios[0].VersionSUTIDs) != 1 || proposal.Scenarios[0].VersionSUTIDs[0] != 9001 {
		t.Fatalf("scenario suts = %#v", proposal.Scenarios[0].VersionSUTIDs)
	}
	if len(proposal.Cases) != 1 {
		t.Fatalf("cases = %#v", proposal.Cases)
	}
	proposedCase := proposal.Cases[0]
	if proposedCase.ScenarioRef != "s1" || proposedCase.Title != "账号密码登录成功" || proposedCase.Priority != "P1" {
		t.Fatalf("case = %#v", proposedCase)
	}
	if proposedCase.DefaultVersionSUTID != 9001 || len(proposedCase.VersionSUTIDs) != 1 {
		t.Fatalf("case targets = %#v", proposedCase)
	}
	if len(proposedCase.Steps) != 2 || proposedCase.Steps[1].TargetAPIID != 9201 {
		t.Fatalf("steps = %#v", proposedCase.Steps)
	}
	if len(proposedCase.Assertions) != 2 || proposedCase.Assertions[1].Kind != "api" {
		t.Fatalf("assertions = %#v", proposedCase.Assertions)
	}
	if len(proposedCase.Sources) != 1 || proposedCase.Sources[0].RefID != "700" {
		t.Fatalf("sources = %#v", proposedCase.Sources)
	}

	invalid := []struct {
		name    string
		scope   Scope
		payload string
		field   string
	}{
		{"requirement scope mismatch", scope, strings.Replace(validGraphProposalJSON(), `"requirement_id": "501"`, `"requirement_id": "999"`, 1), "requirement_id"},
		{"requirement version scope mismatch", scope, strings.Replace(validGraphProposalJSON(), `"requirement_version_id": "700"`, `"requirement_version_id": "701"`, 1), "requirement_version_id"},
		{"bad base hash", scope, strings.Replace(validGraphProposalJSON(), `"requirement_version_id": "700",`, `"requirement_version_id": "700", "base_graph_hash": "not-a-hash",`, 1), "base_graph_hash"},
		{"no scenarios", scope, `{"requirement_id":"501","requirement_version_id":"700","scenarios":[],"cases":[]}`, "scenarios"},
		{"empty scenario ref", scope, strings.Replace(validGraphProposalJSON(), `"ref": "s1"`, `"ref": ""`, 1), "scenarios[0].ref"},
		{"unknown scenario ref", scope, strings.Replace(validGraphProposalJSON(), `"scenario_ref": "s1"`, `"scenario_ref": "s9"`, 1), "cases[0].scenario_ref"},
		{"non contiguous steps", scope, strings.Replace(validGraphProposalJSON(), `"seq": 2`, `"seq": 3`, 1), "cases[0].steps[1].seq"},
		{"bad assertion step", scope, strings.Replace(validGraphProposalJSON(), `"step_seq": 2`, `"step_seq": 9`, 1), "cases[0].assertions[1].step_seq"},
		{"default sut outside case suts", scope, strings.Replace(validGraphProposalJSON(), `"default_version_sut_id": "9001"`, `"default_version_sut_id": "9999"`, 1), "cases[0].default_version_sut_id"},
		{"invalid priority", scope, strings.Replace(validGraphProposalJSON(), `"priority": "P1"`, `"priority": "P9"`, 1), "cases[0].priority"},
		{"db assertion without database", scope, strings.Replace(validGraphProposalJSON(), `"kind": "api"`, `"kind": "db"`, 1), "cases[0].assertions[1].database_id"},
		{"invalid assertion kind", scope, strings.Replace(validGraphProposalJSON(), `"kind": "ui"`, `"kind": "bogus"`, 1), "cases[0].assertions[0].kind"},
		{"missing requirement id", Scope{VersionID: 10}, `{"requirement_version_id":"700","scenarios":[{"ref":"s1","name":"n"}],"cases":[]}`, "requirement_id"},
	}
	for _, testCase := range invalid {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := executeDesignTool(t, registry, testCase.scope, ToolCaseGraphPropose, testCase.payload)
			toolErr := asToolError(t, err)
			if toolErr.Code != CodeInvalidArgument {
				t.Fatalf("code = %q (%v)", toolErr.Code, err)
			}
			if field, _ := toolErr.Details["field"].(string); field != testCase.field {
				t.Fatalf("field = %q, want %q", field, testCase.field)
			}
		})
	}

	if _, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, `{"requirement_id":"501","requirement_version_id":"700","scenarios":[{"ref":"s1","name":"n"}],"cases":[]}`); err == nil {
		t.Fatal("case-less proposal accepted")
	} else if toolErr := asToolError(t, err); toolErr.Code != CodeInvalidArgument {
		t.Fatalf("case-less code = %q", toolErr.Code)
	}
}

func TestReviewSubmitToolValidatesAndScopes(t *testing.T) {
	port := &fakeDesignPort{reviewResult: map[string]any{"posted": true, "count": 1}}
	registry := NewP2Registry(Deps{Design: port})
	scope := Scope{VersionID: 10, RequirementID: 501, AgentRunID: 77}

	payload := `{"summary":"整体可用","findings":[{"severity":"minor","message":"补充异常步骤断言"}]}`
	if _, err := executeDesignTool(t, registry, scope, ToolDesignReviewSubmit, payload); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if port.reviewCalls != 1 || port.reviewVersionID != 10 || port.reviewRequirementID != 501 || port.reviewRunID != 77 {
		t.Fatalf("review call = %#v", port)
	}
	if port.reviewInput.Summary != "整体可用" || len(port.reviewInput.Findings) != 1 ||
		port.reviewInput.Findings[0].Severity != "minor" {
		t.Fatalf("review input = %#v", port.reviewInput)
	}

	invalid := []struct {
		name    string
		scope   Scope
		payload string
		field   string
	}{
		{"bad severity", scope, `{"summary":"x","findings":[{"severity":"critical","message":"x"}]}`, "findings[0].severity"},
		{"empty message", scope, `{"summary":"x","findings":[{"severity":"minor","message":""}]}`, "findings[0].message"},
		{"no requirement scope", Scope{VersionID: 10}, `{"summary":"x","findings":[{"severity":"minor","message":"x"}]}`, "requirement_id"},
		{"oversized summary", scope, `{"summary":"` + strings.Repeat("长", 4001) + `","findings":[]}`, "summary"},
	}
	for _, testCase := range invalid {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := executeDesignTool(t, registry, testCase.scope, ToolDesignReviewSubmit, testCase.payload)
			toolErr := asToolError(t, err)
			if toolErr.Code != CodeInvalidArgument {
				t.Fatalf("code = %q (%v)", toolErr.Code, err)
			}
			if field, _ := toolErr.Details["field"].(string); field != testCase.field {
				t.Fatalf("field = %q, want %q", field, testCase.field)
			}
		})
	}
}

func TestDataPlanSubmitToolUsesCaseVersionScope(t *testing.T) {
	port := &fakeDesignPort{dataPlanResult: map[string]any{"case_id": "7001", "revision": 2}}
	registry := NewP2Registry(Deps{Design: port})

	payload := `{"case_version_id":"8001","preconditions":"已登录","test_data":{"account":"admin"}}`
	if _, err := executeDesignTool(t, registry, Scope{VersionID: 10, AgentRunID: 77}, ToolDesignDataPlanSubmit, payload); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if port.dataPlanCalls != 1 || port.dataPlanVersionID != 10 || port.dataPlanCaseVersionID != 8001 || port.dataPlanRunID != 77 {
		t.Fatalf("data plan call = %#v", port)
	}
	if port.dataPlanInput.Preconditions == nil || *port.dataPlanInput.Preconditions != "已登录" {
		t.Fatalf("preconditions = %#v", port.dataPlanInput.Preconditions)
	}
	if string(port.dataPlanInput.TestData) == "" {
		t.Fatalf("test data = %#v", port.dataPlanInput.TestData)
	}

	// A case-version-scoped run cannot submit for another version.
	_, err := executeDesignTool(t, registry, Scope{VersionID: 10, CaseVersionID: 8002}, ToolDesignDataPlanSubmit, payload)
	if toolErr := asToolError(t, err); toolErr.Code != CodeInvalidArgument {
		t.Fatalf("cross-scope code = %q", toolErr.Code)
	}
	_, err = executeDesignTool(t, registry, Scope{VersionID: 10}, ToolDesignDataPlanSubmit, `{"preconditions":"x"}`)
	if toolErr := asToolError(t, err); toolErr.Code != CodeInvalidArgument {
		t.Fatalf("missing case version code = %q", toolErr.Code)
	}

	// A nil port is an internal configuration failure, not a model-visible error.
	unwired := NewP2Registry(Deps{})
	_, err = executeDesignTool(t, unwired, Scope{VersionID: 10}, ToolDesignDataPlanSubmit, payload)
	if err == nil {
		t.Fatal("unwired design port executed")
	}
	if _, isToolError := err.(*Error); isToolError {
		t.Fatalf("missing dependency surfaced as a model-visible error: %v", err)
	}
}
