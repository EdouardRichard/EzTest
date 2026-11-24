package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// fakeDesignPort implements DesignPort for the P2 tool tests.
type fakeDesignPort struct {
	readVersionID     int64
	readRequirementID int64
	readCaseID        int64
	readCalls         int
	readResult        any
	readResults       []any
	readErr           error

	proposeVersionID int64
	proposeActorID   int64
	proposeRunID     int64
	proposeInput     GraphProposal
	proposeInputs    []GraphProposal
	proposeCalls     int
	proposeResult    any
	proposeErr       error
	proposeErrs      []error

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

	judgmentVersionID     int64
	judgmentRequirementID int64
	judgmentRunID         int64
	judgmentInputs        []JudgmentInput
	judgmentCalls         int
	judgmentResult        any
	judgmentErr           error
}

func (f *fakeDesignPort) ReadGraph(_ context.Context, versionID, requirementID, caseID int64) (any, error) {
	f.readCalls++
	f.readVersionID = versionID
	f.readRequirementID = requirementID
	f.readCaseID = caseID
	if len(f.readResults) > 0 {
		result := f.readResults[0]
		f.readResults = f.readResults[1:]
		return result, f.readErr
	}
	return f.readResult, f.readErr
}

func (f *fakeDesignPort) ProposeGraph(_ context.Context, versionID int64, in GraphProposal, actorID, runID int64) (any, error) {
	f.proposeCalls++
	f.proposeVersionID = versionID
	f.proposeInput = in
	f.proposeInputs = append(f.proposeInputs, in)
	f.proposeActorID = actorID
	f.proposeRunID = runID
	if len(f.proposeErrs) > 0 {
		err := f.proposeErrs[0]
		f.proposeErrs = f.proposeErrs[1:]
		return nil, err
	}
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

func (f *fakeDesignPort) SubmitJudgment(_ context.Context, versionID, requirementID, runID int64, in JudgmentInput) (any, error) {
	f.judgmentCalls++
	f.judgmentVersionID = versionID
	f.judgmentRequirementID = requirementID
	f.judgmentRunID = runID
	f.judgmentInputs = append(f.judgmentInputs, in)
	return f.judgmentResult, f.judgmentErr
}

// validGraphProposalJSON is the v0.2.4-B natural-language case contract: the
// case states sut_description / preconditions / covered_acs, steps carry a prose
// target_description, and no resource ids appear anywhere.
func validGraphProposalJSON() string {
	return `{
  "requirement_id": "501",
  "requirement_version_id": "700",
  "scenarios": [
    {"ref": "s1", "name": "正常登录", "description": "手机号密码登录", "sort_order": 0}
  ],
  "cases": [
    {
      "ref": "c1",
      "scenario_ref": "s1",
      "title": "账号密码登录成功",
      "description": "使用有效账号登录",
      "sut_description": "商城前台登录页与登录接口。",
      "preconditions": "已注册账号",
      "covered_acs": ["AC-1", "AC-2"],
      "test_data": {"phone": "13800000000"},
      "priority": "P1",
      "steps": [
        {"seq": 1, "action": "打开登录页", "expected": "显示表单", "target_description": "登录页的手机号输入框", "account_role": "admin", "data": {}},
        {"seq": 2, "action": "提交账号密码", "expected": "登录成功", "target_description": "POST /api/login 接口", "data": {}}
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

// TestGraphProposeNormalizesRequirementSourceRef pins the S3 failure fix: the
// model may cite the requirement id it was scoped with as a requirement source
// even though the immutable source must be the current requirement version.
// The tool resolves exactly that unambiguous reference; a source pointing at
// another requirement is never rewritten (the design context rejects it).
func TestGraphProposeNormalizesRequirementSourceRef(t *testing.T) {
	port := &fakeDesignPort{proposeResult: map[string]any{"case_version_ids": []string{"7001"}}}
	registry := NewDesignRegistry(Deps{Design: port})
	scope := Scope{VersionID: 10, RequirementID: 501, RequirementVersionID: 700, AgentRunID: 77}

	sameRequirement := strings.Replace(validGraphProposalJSON(), `"ref_id": "700"`, `"ref_id": "501"`, 1)
	if _, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, sameRequirement); err != nil {
		t.Fatalf("propose with the requirement id as source: %v", err)
	}
	if got := port.proposeInput.Cases[0].Sources[0].RefID; got != "700" {
		t.Fatalf("normalized requirement source = %q, want the requirement version 700", got)
	}

	foreignRequirement := strings.Replace(validGraphProposalJSON(), `"ref_id": "700"`, `"ref_id": "999"`, 1)
	if _, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, foreignRequirement); err != nil {
		t.Fatalf("propose with a foreign requirement source: %v", err)
	}
	if got := port.proposeInput.Cases[0].Sources[0].RefID; got != "999" {
		t.Fatalf("foreign requirement source = %q, want it passed through unchanged", got)
	}
}

// TestDesignToolSchemaSources pins the product-owner failure "unknown field
// sources". The model legitimately attaches provenance either per case or once
// for the whole proposal; both shapes must be declared in the schema and
// accepted by the executor. A top-level list is the default source set of every
// case that does not declare its own.
func TestDesignToolSchemaSources(t *testing.T) {
	registry := NewDesignRegistry(Deps{Design: &fakeDesignPort{}})
	definitions := registry.Definitions([]string{ToolCaseGraphPropose})
	if len(definitions) != 1 {
		t.Fatalf("definitions = %d", len(definitions))
	}
	var schema map[string]any
	if err := json.Unmarshal(definitions[0].Parameters, &schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	properties, _ := schema["properties"].(map[string]any)
	if _, ok := properties["sources"]; !ok {
		t.Fatalf("top-level sources is missing from the case.graph_propose schema: %s", definitions[0].Parameters)
	}
	casesSchema, _ := properties["cases"].(map[string]any)
	caseItems, _ := casesSchema["items"].(map[string]any)
	caseProperties, _ := caseItems["properties"].(map[string]any)
	if _, ok := caseProperties["sources"]; !ok {
		t.Fatalf("case-level sources is missing from the case.graph_propose schema")
	}

	port := &fakeDesignPort{proposeResult: map[string]any{"graph_hash": "after"}}
	registry = NewDesignRegistry(Deps{Design: port})
	scope := Scope{VersionID: 10, RequirementID: 501, RequirementVersionID: 700, AgentRunID: 77}
	withoutCaseSources := strings.Replace(validGraphProposalJSON(), `,
      "sources": [
        {"kind": "requirement", "ref_id": "700", "path": "", "line": null, "sha": ""}
      ]`, "", 1)
	if withoutCaseSources == validGraphProposalJSON() {
		t.Fatal("test fixture did not remove the case-level sources")
	}
	withTopLevelSources := strings.Replace(withoutCaseSources, `"scenarios": [`,
		`"sources": [{"kind": "requirement", "ref_id": "501"}],
  "scenarios": [`, 1)
	if _, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, withTopLevelSources); err != nil {
		t.Fatalf("propose with top-level sources: %v", err)
	}
	if port.proposeCalls != 1 || len(port.proposeInput.Cases) != 1 {
		t.Fatalf("propose = %#v", port.proposeInput)
	}
	sources := port.proposeInput.Cases[0].Sources
	if len(sources) != 1 || sources[0].Kind != "requirement" || sources[0].RefID != "700" {
		t.Fatalf("top-level sources were not inherited: %#v", sources)
	}
}

// TestDesignToolConvergesAfterGraphConflict pins the S4 repeat-conflict fix:
// a stale base_graph_hash must be repaired server-side by re-reading the graph
// and retrying, instead of forcing the model to spend another turn (and its
// remaining budget) on a read the tool can do itself.
func TestDesignToolConvergesAfterGraphConflict(t *testing.T) {
	freshHash := strings.Repeat("a", 64)
	port := &fakeDesignPort{
		proposeErrs:   []error{platform.StateConflict("the design graph changed since it was read; re-read the graph and retry")},
		proposeResult: map[string]any{"graph_hash": "after", "case_version_ids": []string{"7001"}},
		readResults:   []any{map[string]any{"graph_hash": freshHash}},
	}
	registry := NewDesignRegistry(Deps{Design: port})
	scope := Scope{VersionID: 10, RequirementID: 501, RequirementVersionID: 700, AgentRunID: 77}
	staleHash := strings.Repeat("0", 64)
	payload := strings.Replace(validGraphProposalJSON(), `"requirement_version_id": "700",`,
		`"requirement_version_id": "700", "base_graph_hash": "`+staleHash+`",`, 1)

	result, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, payload)
	if err != nil {
		t.Fatalf("propose did not converge: %v", err)
	}
	if port.proposeCalls != 2 {
		t.Fatalf("propose calls = %d, want 2 (initial + converge retry)", port.proposeCalls)
	}
	if port.readCalls != 1 || port.readRequirementID != 501 {
		t.Fatalf("converge read = calls %d requirement %d", port.readCalls, port.readRequirementID)
	}
	if got := port.proposeInputs[1].BaseGraphHash; got != freshHash {
		t.Fatalf("retry base hash = %q, want the re-read hash %q", got, freshHash)
	}
	if got, _ := result.(map[string]any)["graph_hash"].(string); got != "after" {
		t.Fatalf("result = %#v", result)
	}
}

func TestDesignToolsRegisteredAndCaseConfirmAbsent(t *testing.T) {
	registry := NewDesignRegistry(Deps{Design: &fakeDesignPort{}})
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
	base := NewBaseRegistry(Deps{})
	if names := base.Names(); len(names) != len(BaseToolNames()) {
		t.Fatalf("P1 registry names = %v", names)
	}
}

func TestDesignSchemasDoNotExposeScope(t *testing.T) {
	registry := NewDesignRegistry(Deps{Design: &fakeDesignPort{}})
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
	registry := NewDesignRegistry(Deps{Design: port})

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
	registry := NewDesignRegistry(Deps{Design: port})
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
	if len(proposal.Cases) != 1 {
		t.Fatalf("cases = %#v", proposal.Cases)
	}
	proposedCase := proposal.Cases[0]
	if proposedCase.ScenarioRef != "s1" || proposedCase.Title != "账号密码登录成功" || proposedCase.Priority != "P1" {
		t.Fatalf("case = %#v", proposedCase)
	}
	// v0.2.4-B contract elements must reach the port verbatim (covered_acs
	// normalized sorted-unique by the tool).
	if proposedCase.SutDescription != "商城前台登录页与登录接口。" {
		t.Fatalf("sut_description = %q", proposedCase.SutDescription)
	}
	if proposedCase.Preconditions != "已注册账号" {
		t.Fatalf("preconditions = %q", proposedCase.Preconditions)
	}
	if strings.Join(proposedCase.CoveredACs, ",") != "AC-1,AC-2" {
		t.Fatalf("covered_acs = %v, want [AC-1 AC-2]", proposedCase.CoveredACs)
	}
	if len(proposedCase.Steps) != 2 || proposedCase.Steps[1].TargetDescription != "POST /api/login 接口" {
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
		{"missing sut_description", scope, strings.Replace(validGraphProposalJSON(), `"sut_description": "商城前台登录页与登录接口。",`, ``, 1), "cases[0].sut_description"},
		{"blank sut_description", scope, strings.Replace(validGraphProposalJSON(), `"sut_description": "商城前台登录页与登录接口。"`, `"sut_description": "   "`, 1), "cases[0].sut_description"},
		{"missing preconditions", scope, strings.Replace(validGraphProposalJSON(), `"preconditions": "已注册账号",`, ``, 1), "cases[0].preconditions"},
		{"blank preconditions", scope, strings.Replace(validGraphProposalJSON(), `"preconditions": "已注册账号"`, `"preconditions": "  "`, 1), "cases[0].preconditions"},
		{"missing covered_acs", scope, strings.Replace(validGraphProposalJSON(), `"covered_acs": ["AC-1", "AC-2"],`, ``, 1), "cases[0].covered_acs"},
		{"empty covered_acs", scope, strings.Replace(validGraphProposalJSON(), `"covered_acs": ["AC-1", "AC-2"]`, `"covered_acs": []`, 1), "cases[0].covered_acs"},
		{"malformed covered_acs", scope, strings.Replace(validGraphProposalJSON(), `"covered_acs": ["AC-1", "AC-2"]`, `"covered_acs": ["1"]`, 1), "cases[0].covered_acs"},
		{"invalid priority", scope, strings.Replace(validGraphProposalJSON(), `"priority": "P1"`, `"priority": "P9"`, 1), "cases[0].priority"},
		{"db assertion without sut_id", scope, strings.Replace(validGraphProposalJSON(), `"kind": "api"`, `"kind": "db"`, 1), "cases[0].assertions[1].sut_id"},
		{"empty step expected", scope, strings.Replace(validGraphProposalJSON(), `"expected": "显示表单"`, `"expected": ""`, 1), "cases[0].steps[0].expected"},
		{"empty assertion expected", scope, strings.Replace(validGraphProposalJSON(), `"expected": "表单可见"`, `"expected": ""`, 1), "cases[0].assertions[0].expected"},
		{"invalid assertion kind", scope, strings.Replace(validGraphProposalJSON(), `"kind": "ui"`, `"kind": "bogus"`, 1), "cases[0].assertions[0].kind"},
		// The account_role bound must match the design gate's AccountRoleMax, or a
		// role the tool accepts dies one hop later as a design 400 and the model
		// loses the precise tool-level field error this early gate exists to give.
		// 65 chars is one past design.AccountRoleMax (64); the schema used to
		// publish 200 here and the parser used to allow it.
		{"over-long account_role", scope, strings.Replace(validGraphProposalJSON(),
			`"account_role": "admin"`, `"account_role": "`+strings.Repeat("r", design.AccountRoleMax+1)+`"`, 1),
			"cases[0].steps[0].account_role"},
		{"missing requirement id", Scope{VersionID: 10}, `{"requirement_version_id":"700","scenarios":[{"ref":"s1","name":"n"}],"cases":[]}`, "requirement_id"},
		// The retired resource-id fields are unknown fields now and must be
		// rejected, not silently ignored: a model still sending the pre-B shape
		// has to be told, not quietly half-applied.
		{"retired default_version_sut_id", scope, strings.Replace(validGraphProposalJSON(), `"priority": "P1",`, `"priority": "P1", "default_version_sut_id": "9001",`, 1), "cases[0].default_version_sut_id"},
		{"retired version_sut_ids", scope, strings.Replace(validGraphProposalJSON(), `"priority": "P1",`, `"priority": "P1", "version_sut_ids": ["9001"],`, 1), "cases[0].version_sut_ids"},
		{"retired step target_page_id", scope, strings.Replace(validGraphProposalJSON(), `"account_role": "admin",`, `"target_page_id": "9101", "account_role": "admin",`, 1), "cases[0].steps[0].target_page_id"},
		{"retired assertion database_id", scope, strings.Replace(validGraphProposalJSON(), `"kind": "api"`, `"kind": "api", "database_id": "1501"`, 1), "cases[0].assertions[1].database_id"},
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
	registry := NewDesignRegistry(Deps{Design: port})
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
	registry := NewDesignRegistry(Deps{Design: port})

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
	unwired := NewDesignRegistry(Deps{})
	_, err = executeDesignTool(t, unwired, Scope{VersionID: 10}, ToolDesignDataPlanSubmit, payload)
	if err == nil {
		t.Fatal("unwired design port executed")
	}
	if _, isToolError := err.(*Error); isToolError {
		t.Fatalf("missing dependency surfaced as a model-visible error: %v", err)
	}
}

// --- v0.22 E-1 graph_propose contract (Task 33) ----------------------------

// graphProposalWithDBAssertion turns the first (ui) assertion of the shared
// fixture into a db assertion, optionally carrying a sut_id.
func graphProposalWithDBAssertion(t *testing.T, sutID string) string {
	t.Helper()
	const uiAssertion = `{"step_seq": 1, "kind": "ui", "target": "登录表单", "expected": "表单可见"}`
	dbAssertion := `{"step_seq": 1, "kind": "db", "target": "订单表", "expected": "1 行"}`
	payload := strings.Replace(validGraphProposalJSON(), uiAssertion, dbAssertion, 1)
	if payload == validGraphProposalJSON() {
		t.Fatal("test fixture did not rewrite the first assertion to db")
	}
	if sutID != "" {
		withID := strings.Replace(dbAssertion, `"expected": "1 行"`,
			`"expected": "1 行", "sut_id": "`+sutID+`"`, 1)
		payload = strings.Replace(payload, dbAssertion, withID, 1)
	}
	return payload
}

// fakeDatabaseResource implements ResourcePort plus the additive DatabaseLister
// surface behind the sut.database_list tool. graph_propose no longer reads it:
// under the v0.2.4-B contract a db assertion names a SUT and the rule is
// presence-only (Lead ruling 6), so there is no bound-inventory ownership check
// left to feed. The fake stays because the resource tools still use the port.
type fakeDatabaseResource struct {
	databases []DatabaseSummary
	err       error
	versionID int64
}

func (f *fakeDatabaseResource) ListSUTs(context.Context, int64) ([]SUTSummary, error) {
	return nil, nil
}

func (f *fakeDatabaseResource) ListPages(context.Context, int64, int64) ([]ResourcePage, error) {
	return nil, nil
}

func (f *fakeDatabaseResource) ListDatabases(_ context.Context, versionID, _ int64) ([]DatabaseSummary, error) {
	f.versionID = versionID
	return f.databases, f.err
}

func boundDatabaseResource() *fakeDatabaseResource {
	return &fakeDatabaseResource{databases: []DatabaseSummary{
		{ID: "1501", Name: "order_db", Kind: "postgres", ReadOnly: true},
		{ID: "1502", Name: "audit_db", Kind: "postgres", ReadOnly: false},
	}}
}

// TestGraphProposeMissingSUTIDRetryable pins R-E1-3 on the renamed field: a db
// assertion that omits sut_id is a retryable field error whose path and hint tell
// the model exactly how to self-correct, and nothing is written.
func TestGraphProposeMissingSUTIDRetryable(t *testing.T) {
	port := &fakeDesignPort{proposeResult: map[string]any{"graph_hash": "after"}}
	registry := NewDesignRegistry(Deps{Design: port, Resource: boundDatabaseResource()})
	scope := Scope{VersionID: 7, RequirementID: 501, RequirementVersionID: 700, AgentRunID: 77}

	_, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, graphProposalWithDBAssertion(t, ""))
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument {
		t.Fatalf("code = %q, want %q (%v)", toolErr.Code, CodeInvalidArgument, err)
	}
	if !toolErr.Retryable {
		t.Fatalf("missing sut_id must be retryable: %+v", toolErr)
	}
	wantField := "cases[0].assertions[0].sut_id"
	if toolErr.Field != wantField {
		t.Fatalf("field = %q, want %q", toolErr.Field, wantField)
	}
	if field, _ := toolErr.Details["field"].(string); field != wantField {
		t.Fatalf("details.field = %#v, want %q", toolErr.Details["field"], wantField)
	}
	if !strings.Contains(toolErr.Hint, "sut.list") {
		t.Fatalf("hint = %q, want it to point at sut.list", toolErr.Hint)
	}
	if strings.Contains(toolErr.Message, "database_id") {
		t.Fatalf("message still teaches the retired database_id: %q", toolErr.Message)
	}
	if port.proposeCalls != 0 {
		t.Fatalf("rejected proposal reached the design port: %#v", port.proposeInput)
	}
	// The error must be discoverable through the wire payload too.
	payload := toolErr.Payload()
	body, _ := payload["error"].(map[string]any)
	if body["code"] != CodeInvalidArgument || body["retryable"] != true ||
		body["field"] != wantField {
		t.Fatalf("error payload = %#v", body)
	}
	if hint, _ := body["hint"].(string); !strings.Contains(hint, "sut.list") {
		t.Fatalf("payload hint = %#v", body["hint"])
	}
	t.Logf("missing sut_id error payload: %s", testJSON(t, toolErr.Payload()))
}

// TestGraphProposeAcceptsAnySUTID pins Lead ruling 6 at the tool boundary: the db
// rule is PRESENCE only. The retired R-E1-2 inventory check rejected a
// database_id that was not bound to the version's environment; there is no
// resource scope left to check against, so any positive sut_id is accepted and
// the design domain stays the only authority. A regression here means the
// ownership group was re-introduced through the tool layer.
func TestGraphProposeAcceptsAnySUTID(t *testing.T) {
	port := &fakeDesignPort{proposeResult: map[string]any{"graph_hash": "after"}}
	resource := boundDatabaseResource()
	registry := NewDesignRegistry(Deps{Design: port, Resource: resource})
	scope := Scope{VersionID: 7, RequirementID: 501, RequirementVersionID: 700}

	// 424242 is in no inventory the tool could read.
	if _, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, graphProposalWithDBAssertion(t, "424242")); err != nil {
		t.Fatalf("an arbitrary sut_id must be accepted (presence-only rule): %v", err)
	}
	if port.proposeCalls != 1 {
		t.Fatalf("propose calls = %d, want 1", port.proposeCalls)
	}
	if got := port.proposeInput.Cases[0].Assertions[0].SUTID; got != 424242 {
		t.Fatalf("sut_id = %d, want 424242", got)
	}
	if got := port.proposeInput.Cases[0].Assertions[0].Kind; got != "db" {
		t.Fatalf("assertion kind = %q, want db", got)
	}
	if resource.versionID != 0 {
		t.Fatalf("the tool read the database inventory (%d); the ownership check must stay retired", resource.versionID)
	}
}

// TestGraphProposeIgnoresSUTIDForNonDBKinds pins that sut_id is only required by
// kind=db: a ui/api/text assertion may carry one or not.
func TestGraphProposeIgnoresSUTIDForNonDBKinds(t *testing.T) {
	port := &fakeDesignPort{proposeResult: map[string]any{"graph_hash": "after"}}
	registry := NewDesignRegistry(Deps{Design: port})
	scope := Scope{VersionID: 7, RequirementID: 501, RequirementVersionID: 700}

	if _, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, validGraphProposalJSON()); err != nil {
		t.Fatalf("ui/api assertions without sut_id must be accepted: %v", err)
	}
	for _, assertion := range port.proposeInput.Cases[0].Assertions {
		if assertion.SUTID != 0 {
			t.Fatalf("non-db assertion carried sut_id %d: %+v", assertion.SUTID, assertion)
		}
	}
}

// TestGraphProposeRejectsUnknownFieldWithSuggestion pins R-E1-1/R-E1-4: typos
// are rejected (aliases stay disabled) and the error names the nearest legal
// field so the model can self-correct.
func TestGraphProposeRejectsUnknownFieldWithSuggestion(t *testing.T) {
	port := &fakeDesignPort{proposeResult: map[string]any{"graph_hash": "after"}}
	registry := NewDesignRegistry(Deps{Design: port, Resource: boundDatabaseResource()})
	scope := Scope{VersionID: 7, RequirementID: 501, RequirementVersionID: 700}

	payload := strings.Replace(validGraphProposalJSON(), `"target_description": "登录页的手机号输入框"`, `"tartget_description": "登录页的手机号输入框"`, 1)
	if payload == validGraphProposalJSON() {
		t.Fatal("fixture did not inject the typo")
	}
	_, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, payload)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
		t.Fatalf("unknown field error = %+v", toolErr)
	}
	wantField := "cases[0].steps[0].tartget_description"
	if toolErr.Field != wantField {
		t.Fatalf("field = %q, want %q", toolErr.Field, wantField)
	}
	if !strings.Contains(toolErr.Message, "target_description") {
		t.Fatalf("message = %q, want the nearest legal field", toolErr.Message)
	}
	if suggestion, _ := toolErr.Details["suggestion"].(string); suggestion != "target_description" {
		t.Fatalf("suggestion = %#v, want target_description", toolErr.Details["suggestion"])
	}
	if !containsString(toolErr.Allowed, "target_description") {
		t.Fatalf("allowed = %#v, want the legal step fields", toolErr.Allowed)
	}
	if port.proposeCalls != 0 {
		t.Fatalf("unknown field was silently applied: %#v", port.proposeInput)
	}
}

// TestGraphProposeSchemaIsSelfDescribing pins R-E1-1: the model sees field
// descriptions, enums, assertion branches, examples (including db) and the
// explicit reject-unknown-fields contract.
func TestGraphProposeSchemaIsSelfDescribing(t *testing.T) {
	registry := NewDesignRegistry(Deps{Design: &fakeDesignPort{}, Resource: boundDatabaseResource()})
	definitions := registry.Definitions([]string{ToolCaseGraphPropose})
	if len(definitions) != 1 {
		t.Fatalf("definitions = %d", len(definitions))
	}
	var schema map[string]any
	if err := json.Unmarshal(definitions[0].Parameters, &schema); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	properties, _ := schema["properties"].(map[string]any)
	for _, field := range []string{
		"requirement_id", "requirement_version_id", "proposal_id", "base_revision", "scenarios", "cases", "sources",
	} {
		raw, ok := properties[field].(map[string]any)
		if !ok {
			t.Fatalf("top-level %q is missing from the schema", field)
		}
		if description, _ := raw["description"].(string); strings.TrimSpace(description) == "" {
			t.Fatalf("%q has no description", field)
		}
	}

	casesSchema, _ := properties["cases"].(map[string]any)
	caseItems, _ := casesSchema["items"].(map[string]any)
	caseProperties, _ := caseItems["properties"].(map[string]any)
	for _, field := range []string{
		"ref", "scenario_ref", "title", "sut_description", "preconditions",
		"covered_acs", "priority", "steps", "assertions", "sources",
	} {
		raw, ok := caseProperties[field].(map[string]any)
		if !ok || strings.TrimSpace(stringValueOf(raw["description"])) == "" {
			t.Fatalf("case field %q is missing a description", field)
		}
	}
	// The three v0.2.4-B contract elements must be machine-required, not merely
	// documented, or a model can omit them and only find out from a 400.
	caseRequired := stringListOf(t, caseItems["required"])
	for _, field := range []string{"sut_description", "preconditions", "covered_acs"} {
		if !containsString(caseRequired, field) {
			t.Fatalf("case schema required = %#v, want it to include %q", caseItems["required"], field)
		}
	}
	for _, gone := range []string{"default_version_sut_id", "version_sut_ids", "page_ids", "api_ids"} {
		if _, ok := caseProperties[gone]; ok {
			t.Fatalf("case schema still advertises the retired field %q", gone)
		}
		if containsString(caseRequired, gone) {
			t.Fatalf("case schema still requires the retired field %q", gone)
		}
	}
	if priorityEnum := enumOf(t, caseProperties["priority"]); strings.Join(priorityEnum, ",") != "P0,P1,P2,P3" {
		// json.Unmarshal renders enum entries as []any; enumOf normalizes to strings.
		t.Fatalf("priority enum = %#v", caseProperties["priority"])
	}

	stepsSchema, _ := caseProperties["steps"].(map[string]any)
	stepItems, _ := stepsSchema["items"].(map[string]any)
	stepProperties, _ := stepItems["properties"].(map[string]any)
	if strings.TrimSpace(stringValueOf(stepProperties["action"].(map[string]any)["description"])) == "" {
		t.Fatalf("step action has no description")
	}
	if strings.TrimSpace(stringValueOf(stepProperties["target_description"].(map[string]any)["description"])) == "" {
		t.Fatalf("step target_description has no description")
	}
	// The published bound must equal the design gate's bound. It drifted once
	// (schema said 200, design enforced AccountRoleMax=64), which let a 70-char
	// role clear the tool and die one hop later as a design 400 -- losing exactly
	// the precise field error this early gate exists to give.
	accountRole, _ := stepProperties["account_role"].(map[string]any)
	if maxLength, _ := accountRole["maxLength"].(float64); int(maxLength) != design.AccountRoleMax {
		t.Fatalf("published account_role maxLength = %v, want design.AccountRoleMax (%d)",
			accountRole["maxLength"], design.AccountRoleMax)
	}
	for _, gone := range []string{"target_version_sut_id", "target_page_id", "target_api_id"} {
		if _, ok := stepProperties[gone]; ok {
			t.Fatalf("step schema still advertises the retired field %q", gone)
		}
	}

	assertionsSchema, _ := caseProperties["assertions"].(map[string]any)
	assertionItems, _ := assertionsSchema["items"].(map[string]any)
	assertionProperties, _ := assertionItems["properties"].(map[string]any)
	if enum := enumOf(t, assertionProperties["kind"]); strings.Join(enum, ",") != "ui,api,db,text" {
		t.Fatalf("assertion kind enum = %#v", assertionProperties["kind"])
	}
	if _, ok := assertionItems["anyOf"]; !ok {
		if _, single := assertionItems["oneOf"]; !single {
			t.Fatalf("assertion branches are not expressed with anyOf/oneOf: %#v", assertionItems)
		}
	}
	if strings.TrimSpace(stringValueOf(assertionProperties["sut_id"].(map[string]any)["description"])) == "" {
		t.Fatalf("assertion sut_id has no description")
	}
	for _, gone := range []string{"target_version_sut_id", "target_api_id", "database_id"} {
		if _, ok := assertionProperties[gone]; ok {
			t.Fatalf("assertion schema still advertises the retired field %q", gone)
		}
	}

	// The scenario schema lost its resource arrays too.
	scenariosSchema, _ := properties["scenarios"].(map[string]any)
	scenarioItems, _ := scenariosSchema["items"].(map[string]any)
	scenarioProperties, _ := scenarioItems["properties"].(map[string]any)
	for _, gone := range []string{"version_sut_ids", "page_ids", "api_ids"} {
		if _, ok := scenarioProperties[gone]; ok {
			t.Fatalf("scenario schema still advertises the retired field %q", gone)
		}
	}

	examples, _ := schema["examples"].([]any)
	if len(examples) < 2 {
		t.Fatalf("examples = %d, want at least 2 complete examples", len(examples))
	}
	encoded, _ := json.Marshal(examples)
	if !strings.Contains(string(encoded), `"sut_id"`) {
		t.Fatalf("examples never show a db assertion: %s", encoded)
	}
	for _, want := range []string{`"sut_description"`, `"covered_acs"`, `"preconditions"`, `"target_description"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("examples never show %s: %s", want, encoded)
		}
	}
	for _, gone := range []string{`"version_sut_ids"`, `"default_version_sut_id"`, `"target_page_id"`, `"target_api_id"`, `"database_id"`, `"target_version_sut_id"`} {
		if strings.Contains(string(encoded), gone) {
			t.Fatalf("examples still model the retired field %s: %s", gone, encoded)
		}
	}

	description := definitions[0].Description
	for _, want := range []string{"unknown", "sut_id", "sut_description", "covered_acs", "preconditions", "target_description", "proposal_id", "base_revision"} {
		if !strings.Contains(description, want) {
			t.Fatalf("%s description lacks %q:\n%s", ToolCaseGraphPropose, want, description)
		}
	}
	for _, gone := range []string{"database_id", "sut.database_list", "target_page_id", "version_sut"} {
		if strings.Contains(description, gone) && !strings.Contains(description, "已全部移除") {
			t.Fatalf("%s description still teaches the retired %q:\n%s", ToolCaseGraphPropose, gone, description)
		}
	}
}

// stringListOf normalizes a JSON-schema "required" array to []string.
func stringListOf(t *testing.T, raw any) []string {
	t.Helper()
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	values := make([]string, 0, len(list))
	for _, item := range list {
		values = append(values, stringValueOf(item))
	}
	return values
}

// TestGraphProposeSchemaTeachesNoRetiredResourceIDs is the negative guard the
// ui-assertion/target_page_id fix generalized: the tool schema, its descriptions
// and its embedded examples must not advertise ANY field the parser rejects. A
// schema that contradicts its own parser instructs the model to send a payload
// that is guaranteed to fail, and additionalProperties:false means it fails hard.
// TestGraphProposeHintsPointAtSourcesThatCarryTheValue pins the round-5 finding:
// a retry hint is only useful if the source it names actually carries the value.
// The covered_acs hint used to name design.graph_read, whose payload has no
// requirement AC ids at all (RequirementGraphView has no AC field, and the adapter
// emits none) -- the only AC-shaped data there is each existing case card's own
// covered_acs, which is empty for a requirement with no cases yet, i.e. exactly
// when the author needs the set. requirement.read is the detail path that carries
// ac_ids, and it is granted to RoleCaseAuthor.
func TestGraphProposeHintsPointAtSourcesThatCarryTheValue(t *testing.T) {
	port := &fakeDesignPort{proposeResult: map[string]any{"graph_hash": "after"}}
	registry := NewDesignRegistry(Deps{Design: port, Resource: boundDatabaseResource()})
	scope := Scope{VersionID: 7, RequirementID: 501, RequirementVersionID: 700}

	t.Run("covered_acs hint", func(t *testing.T) {
		payload := strings.Replace(validGraphProposalJSON(),
			`"covered_acs": ["AC-1", "AC-2"]`, `"covered_acs": ["1"]`, 1)
		if payload == validGraphProposalJSON() {
			t.Fatal("fixture did not inject the malformed covered_acs")
		}
		_, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, payload)
		toolErr := asToolError(t, err)
		if toolErr.Field != "cases[0].covered_acs" {
			t.Fatalf("field = %q, want cases[0].covered_acs", toolErr.Field)
		}
		if !strings.Contains(toolErr.Hint, "requirement.read") {
			t.Fatalf("hint must name requirement.read, the source that carries ac_ids: %q", toolErr.Hint)
		}
		if !strings.Contains(toolErr.Hint, "ac_ids") {
			t.Fatalf("hint must name the ac_ids field the model should read: %q", toolErr.Hint)
		}
		if !strings.Contains(toolErr.Hint, "requirement_id") {
			t.Fatalf("requirement.read is the detail path; the hint must say to pass requirement_id: %q", toolErr.Hint)
		}
		// design.graph_read must not be offered as an AC-id source.
		//
		// The check is on the POSITIVE SOURCE CLAUSE IN ISOLATION -- the text up to
		// the first sentence break -- not on the whole hint. An earlier revision
		// tested `Contains(hint, "graph_read") && !Contains(hint, "Do not use
		// design.graph_read")`, which is self-defeating: that warning sentence is
		// part of the hint it guards, so the second condition was always false and
		// the assertion could never fire. A partial revert that re-added
		// "design.graph_read or" as a source while keeping the warning produced a
		// self-contradictory hint and stayed green. Isolating the clause makes the
		// guard independent of whether the warning survives.
		positiveClause := toolErr.Hint
		if idx := strings.Index(positiveClause, ". "); idx >= 0 {
			positiveClause = positiveClause[:idx]
		}
		if !strings.Contains(positiveClause, "requirement.read") {
			t.Fatalf("the hint's source clause does not name requirement.read: %q", positiveClause)
		}
		if strings.Contains(positiveClause, "graph_read") {
			t.Fatalf("the hint offers design.graph_read as an AC-id source, and it carries none: %q", positiveClause)
		}
		// Belt and braces: no phrasing anywhere in the hint may present graph_read
		// as the thing to copy ids FROM.
		if strings.Contains(toolErr.Hint, "from design.graph_read") {
			t.Fatalf("hint tells the model to copy ids from design.graph_read: %q", toolErr.Hint)
		}
		if port.proposeCalls != 0 {
			t.Fatal("a rejected proposal reached the design port")
		}
	})

	t.Run("sut_id hint", func(t *testing.T) {
		_, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, graphProposalWithDBAssertion(t, ""))
		toolErr := asToolError(t, err)
		if toolErr.Field != "cases[0].assertions[0].sut_id" {
			t.Fatalf("field = %q", toolErr.Field)
		}
		// sut.list is the primary source and really returns a decimal sut_id; the
		// pack's per-row sut_id is the secondary one (round 4). Both are named.
		if !strings.Contains(toolErr.Hint, "sut.list") {
			t.Fatalf("hint must name sut.list: %q", toolErr.Hint)
		}
		if !strings.Contains(toolErr.Hint, "context.version_pack") {
			t.Fatalf("hint should also name the pack's per-row sut_id: %q", toolErr.Hint)
		}
		if strings.Contains(toolErr.Hint, "database_id") {
			t.Fatalf("hint still teaches the retired database_id: %q", toolErr.Hint)
		}
	})
}

// TestGraphProposeSchemaExamplesActuallyParse is the guard against the schema
// contradicting its own parser -- the exact failure mode where the schema and
// examples advertise one contract while parseGraphProposal enforces another, so
// the model is instructed to send a payload that is guaranteed to be rejected.
//
// Every embedded example is fed through the real tool. This test fails whenever
// the two halves drift, in either direction.
func TestGraphProposeSchemaExamplesActuallyParse(t *testing.T) {
	port := &fakeDesignPort{proposeResult: map[string]any{"graph_hash": "after"}}
	registry := NewDesignRegistry(Deps{Design: port, Resource: boundDatabaseResource()})
	definitions := registry.Definitions([]string{ToolCaseGraphPropose})
	if len(definitions) != 1 {
		t.Fatalf("definitions = %d", len(definitions))
	}
	var schema map[string]any
	if err := json.Unmarshal(definitions[0].Parameters, &schema); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	examples, _ := schema["examples"].([]any)
	if len(examples) == 0 {
		t.Fatal("schema carries no examples")
	}
	scope := Scope{VersionID: 10, RequirementID: 501, RequirementVersionID: 700, AgentRunID: 77}
	for index, rawExample := range examples {
		payload, err := json.Marshal(rawExample)
		if err != nil {
			t.Fatalf("examples[%d] is not serializable: %v", index, err)
		}
		port.proposeCalls = 0
		if _, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, string(payload)); err != nil {
			t.Fatalf("examples[%d] is rejected by the tool's own parser -- the schema contradicts the parser: %v\npayload: %s",
				index, err, payload)
		}
		if port.proposeCalls != 1 {
			t.Fatalf("examples[%d] did not reach the design port", index)
		}
		// Each example must carry the required v0.2.4-B contract elements, so a
		// passing parse proves the example is a usable template rather than a
		// minimal stub that happens to slip through.
		mapped := port.proposeInput
		if len(mapped.Cases) == 0 {
			t.Fatalf("examples[%d] produced no cases", index)
		}
		item := mapped.Cases[0]
		if strings.TrimSpace(item.SutDescription) == "" || strings.TrimSpace(item.Preconditions) == "" || len(item.CoveredACs) == 0 {
			t.Fatalf("examples[%d] omits a required contract element: %+v", index, item)
		}
	}
}

func TestGraphProposeSchemaTeachesNoRetiredResourceIDs(t *testing.T) {
	registry := NewDesignRegistry(Deps{Design: &fakeDesignPort{}, Resource: boundDatabaseResource()})
	definitions := registry.Definitions([]string{ToolCaseGraphPropose})
	if len(definitions) != 1 {
		t.Fatalf("definitions = %d", len(definitions))
	}
	encoded := string(definitions[0].Parameters)
	for _, gone := range []string{
		"target_page_id", "target_api_id", "target_version_sut_id",
		"default_version_sut_id", "version_sut_ids", "database_id",
	} {
		if strings.Contains(encoded, `"`+gone+`"`) {
			t.Fatalf("schema advertises the retired field %q, which the parser rejects: %s", gone, encoded)
		}
	}
	// page_ids/api_ids remain legal nowhere in this payload; the only surviving
	// "ids" list is covered_acs.
	for _, gone := range []string{`"page_ids"`, `"api_ids"`} {
		if strings.Contains(encoded, gone) {
			t.Fatalf("schema advertises the retired field %s: %s", gone, encoded)
		}
	}
}

// TestGraphProposeSchemaDBBranchRequiresSUTID pins the machine-checkable half of
// the db branch contract: the anyOf branch for kind=db requires the sut_id
// property instead of only describing it.
func TestGraphProposeSchemaDBBranchRequiresSUTID(t *testing.T) {
	registry := NewDesignRegistry(Deps{Design: &fakeDesignPort{}, Resource: boundDatabaseResource()})
	definitions := registry.Definitions([]string{ToolCaseGraphPropose})
	if len(definitions) != 1 {
		t.Fatalf("definitions = %d", len(definitions))
	}
	var schema map[string]any
	if err := json.Unmarshal(definitions[0].Parameters, &schema); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	properties, _ := schema["properties"].(map[string]any)
	casesSchema, _ := properties["cases"].(map[string]any)
	caseItems, _ := casesSchema["items"].(map[string]any)
	caseProperties, _ := caseItems["properties"].(map[string]any)
	assertionsSchema, _ := caseProperties["assertions"].(map[string]any)
	assertionItems, _ := assertionsSchema["items"].(map[string]any)

	var dbBranch map[string]any
	branches, _ := assertionItems["anyOf"].([]any)
	for _, rawBranch := range branches {
		branch, _ := rawBranch.(map[string]any)
		branchProperties, _ := branch["properties"].(map[string]any)
		if strings.Join(enumOf(t, branchProperties["kind"]), ",") == "db" {
			dbBranch = branch
			break
		}
	}
	if dbBranch == nil {
		t.Fatalf("assertion schema has no kind=db anyOf branch: %#v", assertionItems["anyOf"])
	}
	if _, ok := dbBranch["properties"].(map[string]any)["sut_id"]; !ok {
		t.Fatalf("db branch does not describe sut_id: %#v", dbBranch)
	}
	required, _ := dbBranch["required"].([]any)
	for _, rawRequired := range required {
		if stringValueOf(rawRequired) == "sut_id" {
			return
		}
	}
	t.Fatalf("db branch required = %#v, want sut_id", dbBranch["required"])
}

// TestGraphProposeDatabaseInventoryIsNotConsulted pins the retirement of the
// R-E1-2 inventory check (Lead ruling 6: a db assertion's sut_id is checked for
// PRESENCE only, ownership is gone). Two behaviours that used to exist must stay
// gone: rejecting an id absent from a successfully-read (even empty) inventory,
// and deferring to the design context when no lister is wired. Both are replaced
// by one rule -- any positive sut_id is passed through untouched.
func TestGraphProposeDatabaseInventoryIsNotConsulted(t *testing.T) {
	tests := []struct {
		name     string
		deps     Deps
		wantRead bool
	}{
		{"no lister wired", Deps{Design: &fakeDesignPort{proposeResult: map[string]any{"graph_hash": "after"}}}, false},
		{"empty inventory", Deps{
			Design:   &fakeDesignPort{proposeResult: map[string]any{"graph_hash": "after"}},
			Resource: &fakeDatabaseResource{},
		}, false},
		{"populated inventory", Deps{
			Design:   &fakeDesignPort{proposeResult: map[string]any{"graph_hash": "after"}},
			Resource: boundDatabaseResource(),
		}, false},
		{"lister failure", Deps{
			Design:   &fakeDesignPort{proposeResult: map[string]any{"graph_hash": "after"}},
			Resource: &fakeDatabaseResource{err: errors.New("resource unavailable")},
		}, false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			port, _ := testCase.deps.Design.(*fakeDesignPort)
			registry := NewDesignRegistry(testCase.deps)
			// 424242 is in none of the inventories above.
			if _, err := executeDesignTool(t, registry,
				Scope{VersionID: 7, RequirementID: 501, RequirementVersionID: 700},
				ToolCaseGraphPropose, graphProposalWithDBAssertion(t, "424242")); err != nil {
				t.Fatalf("an arbitrary sut_id must be accepted (presence-only rule): %v", err)
			}
			if port.proposeCalls != 1 {
				t.Fatalf("propose calls = %d, want the id passed through", port.proposeCalls)
			}
			if got := port.proposeInput.Cases[0].Assertions[0].SUTID; got != 424242 {
				t.Fatalf("sut_id = %d, want 424242 passed through", got)
			}
			if resource, ok := testCase.deps.Resource.(*fakeDatabaseResource); ok && testCase.wantRead == false {
				if resource.versionID != 0 {
					t.Fatalf("the tool consulted the database inventory (version %d); the ownership check must stay retired", resource.versionID)
				}
			}
		})
	}
}

// TestGraphProposeWrongTypedValuesCarryJSONFieldPath pins the fix for decoder
// fallbacks: a wrong-typed value is a model-fixable argument error, so it must
// name the JSON field path (with array indices where resolvable), stay
// retryable and never reach the design port. Previously every decoder error
// collapsed to field=parameters.
func TestGraphProposeWrongTypedValuesCarryJSONFieldPath(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		field   string
	}{
		{
			"base_revision string",
			strings.Replace(validGraphProposalJSON(), `"requirement_version_id": "700",`,
				`"requirement_version_id": "700", "base_revision": "3",`, 1),
			"base_revision",
		},
		{
			"proposal_id number",
			strings.Replace(validGraphProposalJSON(), `"requirement_version_id": "700",`,
				`"requirement_version_id": "700", "proposal_id": 123,`, 1),
			"proposal_id",
		},
		{
			"nested step seq string",
			strings.Replace(validGraphProposalJSON(), `"seq": 1,`, `"seq": "1",`, 1),
			"cases[0].steps[0].seq",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			port := &fakeDesignPort{proposeResult: map[string]any{"graph_hash": "after"}}
			registry := NewDesignRegistry(Deps{Design: port})
			_, err := executeDesignTool(t, registry,
				Scope{VersionID: 7, RequirementID: 501, RequirementVersionID: 700},
				ToolCaseGraphPropose, testCase.payload)
			toolErr := asToolError(t, err)
			if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
				t.Fatalf("wrong-typed value error = %+v, want retryable invalid_argument", toolErr)
			}
			if toolErr.Field != testCase.field {
				t.Fatalf("field = %q, want %q (message=%q)", toolErr.Field, testCase.field, toolErr.Message)
			}
			if strings.TrimSpace(toolErr.Hint) == "" {
				t.Fatalf("hint is empty for a wrong-typed value: %+v", toolErr)
			}
			if port.proposeCalls != 0 {
				t.Fatalf("wrong-typed payload reached the design port: %#v", port.proposeInput)
			}
		})
	}
}

// TestGraphProposeRejectsBlankProposalID pins the fix for silently ignoring an
// explicitly supplied empty/space proposal_id: the idempotency key must be
// either absent or a valid stable id, never a blank value that silently
// disables replay protection.
func TestGraphProposeRejectsBlankProposalID(t *testing.T) {
	for _, value := range []string{`""`, `"   "`} {
		t.Run("value="+value, func(t *testing.T) {
			port := &fakeDesignPort{proposeResult: map[string]any{"graph_hash": "after"}}
			registry := NewDesignRegistry(Deps{Design: port})
			payload := strings.Replace(validGraphProposalJSON(), `"requirement_version_id": "700",`,
				`"requirement_version_id": "700", "proposal_id": `+value+`,`, 1)
			_, err := executeDesignTool(t, registry,
				Scope{VersionID: 7, RequirementID: 501, RequirementVersionID: 700},
				ToolCaseGraphPropose, payload)
			toolErr := asToolError(t, err)
			if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
				t.Fatalf("blank proposal_id error = %+v, want retryable invalid_argument", toolErr)
			}
			if toolErr.Field != "proposal_id" {
				t.Fatalf("field = %q, want proposal_id", toolErr.Field)
			}
			if port.proposeCalls != 0 {
				t.Fatalf("blank proposal_id proposal reached the design port")
			}
		})
	}
}

// TestGraphProposeProposalIDIsIdempotent pins R-E1-5: a duplicate submission
// with the same proposal_id returns the first result and writes nothing again.
func TestGraphProposeProposalIDIsIdempotent(t *testing.T) {
	port := &fakeDesignPort{proposeResult: map[string]any{
		"graph_hash": "after", "case_version_ids": []string{"7001"},
	}}
	registry := NewDesignRegistry(Deps{Design: port})
	scope := Scope{VersionID: 7, RequirementID: 501, RequirementVersionID: 700}
	payload := strings.Replace(validGraphProposalJSON(), `"requirement_version_id": "700",`,
		`"requirement_version_id": "700", "proposal_id": "prop-1",`, 1)

	first, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, payload)
	if err != nil {
		t.Fatalf("first propose: %v", err)
	}
	second, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, payload)
	if err != nil {
		t.Fatalf("duplicate propose: %v", err)
	}
	if port.proposeCalls != 1 {
		t.Fatalf("propose calls = %d, want 1 (duplicate proposal_id must be idempotent)", port.proposeCalls)
	}
	firstHash, _ := first.(map[string]any)["graph_hash"].(string)
	secondHash, _ := second.(map[string]any)["graph_hash"].(string)
	if firstHash != "after" || secondHash != firstHash {
		t.Fatalf("results = %#v / %#v, want the identical cached result", first, second)
	}
	t.Logf("duplicate proposal_id replayed: propose_calls=%d result_graph_hash=%s", port.proposeCalls, firstHash)
}

// TestGraphProposeRejectsReusedProposalIDForDifferentPayload pins the other
// side of R-E1-5: a proposal_id identifies one payload; replaying it for a
// different proposal is a non-retryable conflict, never a silent overwrite.
func TestGraphProposeRejectsReusedProposalIDForDifferentPayload(t *testing.T) {
	port := &fakeDesignPort{proposeResult: map[string]any{"graph_hash": "after"}}
	registry := NewDesignRegistry(Deps{Design: port})
	scope := Scope{VersionID: 7, RequirementID: 501, RequirementVersionID: 700}
	payload := strings.Replace(validGraphProposalJSON(), `"requirement_version_id": "700",`,
		`"requirement_version_id": "700", "proposal_id": "prop-1",`, 1)
	if _, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, payload); err != nil {
		t.Fatalf("first propose: %v", err)
	}

	changed := strings.Replace(payload, `"title": "账号密码登录成功"`, `"title": "账号密码登录成功（改）"`, 1)
	_, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, changed)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeStateConflict || toolErr.Retryable {
		t.Fatalf("reused proposal_id error = %+v, want a non-retryable conflict", toolErr)
	}
	if toolErr.Field != "proposal_id" {
		t.Fatalf("field = %q, want proposal_id", toolErr.Field)
	}
	if port.proposeCalls != 1 {
		t.Fatalf("changed payload was applied under the old proposal_id: calls=%d", port.proposeCalls)
	}
}

// TestGraphProposeStaleBaseRevisionConflict pins R-E1-5: a stale base_revision
// is a non-retryable typed conflict with the expected/current revisions.
func TestGraphProposeStaleBaseRevisionConflict(t *testing.T) {
	port := &fakeDesignPort{
		proposeResult: map[string]any{"graph_hash": "after"},
		readResult: map[string]any{
			"graph_hash": strings.Repeat("a", 64),
			"requirements": []any{
				map[string]any{"requirement_id": "501", "revision": 3},
			},
		},
	}
	registry := NewDesignRegistry(Deps{Design: port})
	scope := Scope{VersionID: 7, RequirementID: 501, RequirementVersionID: 700}
	payload := strings.Replace(validGraphProposalJSON(), `"requirement_version_id": "700",`,
		`"requirement_version_id": "700", "base_revision": 2,`, 1)

	_, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, payload)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeStateConflict {
		t.Fatalf("code = %q, want %q (%v)", toolErr.Code, CodeStateConflict, err)
	}
	if toolErr.Retryable {
		t.Fatalf("stale base_revision must not be blindly retryable: %+v", toolErr)
	}
	if toolErr.Field != "base_revision" {
		t.Fatalf("field = %q, want base_revision", toolErr.Field)
	}
	if toolErr.Details["current_revision"] != 3 || toolErr.Details["base_revision"] != 2 {
		t.Fatalf("details = %#v", toolErr.Details)
	}
	if !strings.Contains(toolErr.Hint, "re-read") {
		t.Fatalf("hint = %q", toolErr.Hint)
	}
	if port.proposeCalls != 0 {
		t.Fatalf("stale proposal reached the design port")
	}
}

// TestGraphProposeMatchingBaseRevisionProceeds proves the stale guard does not
// block a proposal whose base_revision matches the current revision.
func TestGraphProposeMatchingBaseRevisionProceeds(t *testing.T) {
	port := &fakeDesignPort{
		proposeResult: map[string]any{"graph_hash": "after"},
		readResult: map[string]any{
			"graph_hash": strings.Repeat("a", 64),
			"requirements": []any{
				map[string]any{"requirement_id": "501", "revision": 3},
			},
		},
	}
	registry := NewDesignRegistry(Deps{Design: port})
	scope := Scope{VersionID: 7, RequirementID: 501, RequirementVersionID: 700}
	payload := strings.Replace(validGraphProposalJSON(), `"requirement_version_id": "700",`,
		`"requirement_version_id": "700", "base_revision": 3,`, 1)

	if _, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, payload); err != nil {
		t.Fatalf("matching base_revision: %v", err)
	}
	if port.proposeCalls != 1 {
		t.Fatalf("propose calls = %d, want 1", port.proposeCalls)
	}
}

// --- schema helpers --------------------------------------------------------

func stringValueOf(value any) string {
	text, _ := value.(string)
	return text
}

func enumOf(t *testing.T, raw any) []string {
	t.Helper()
	schema, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	items, _ := schema["enum"].([]any)
	values := make([]string, 0, len(items))
	for _, item := range items {
		values = append(values, stringValueOf(item))
	}
	return values
}
