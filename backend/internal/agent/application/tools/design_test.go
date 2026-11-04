package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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

// TestGraphProposeNormalizesRequirementSourceRef pins the S3 failure fix: the
// model may cite the requirement id it was scoped with as a requirement source
// even though the immutable source must be the current requirement version.
// The tool resolves exactly that unambiguous reference; a source pointing at
// another requirement is never rewritten (the design context rejects it).
func TestGraphProposeNormalizesRequirementSourceRef(t *testing.T) {
	port := &fakeDesignPort{proposeResult: map[string]any{"case_version_ids": []string{"7001"}}}
	registry := NewP2Registry(Deps{Design: port})
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
	registry := NewP2Registry(Deps{Design: &fakeDesignPort{}})
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
	registry = NewP2Registry(Deps{Design: port})
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
	registry := NewP2Registry(Deps{Design: port})
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
		{"empty step expected", scope, strings.Replace(validGraphProposalJSON(), `"expected": "显示表单"`, `"expected": ""`, 1), "cases[0].steps[0].expected"},
		{"empty assertion expected", scope, strings.Replace(validGraphProposalJSON(), `"expected": "表单可见"`, `"expected": ""`, 1), "cases[0].assertions[0].expected"},
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

// --- v0.22 E-1 graph_propose contract (Task 33) ----------------------------

// graphProposalWithDBAssertion turns the first (ui) assertion of the shared
// fixture into a db assertion, optionally carrying a database_id.
func graphProposalWithDBAssertion(t *testing.T, databaseID string) string {
	t.Helper()
	const uiAssertion = `{"step_seq": 1, "kind": "ui", "target": "登录表单", "expected": "表单可见"}`
	dbAssertion := `{"step_seq": 1, "kind": "db", "target": "订单表", "expected": "1 行"}`
	payload := strings.Replace(validGraphProposalJSON(), uiAssertion, dbAssertion, 1)
	if payload == validGraphProposalJSON() {
		t.Fatal("test fixture did not rewrite the first assertion to db")
	}
	if databaseID != "" {
		withID := strings.Replace(dbAssertion, `"expected": "1 行"`,
			`"expected": "1 行", "database_id": "`+databaseID+`"`, 1)
		payload = strings.Replace(payload, dbAssertion, withID, 1)
	}
	return payload
}

// fakeDatabaseResource implements ResourcePort plus the additive
// DatabaseLister surface the sut.database_list tool and graph_propose consume.
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

// TestGraphProposeMissingDatabaseIDRetryable pins R-E1-3: a db assertion that
// omits database_id is a retryable field error whose path and allowed values
// tell the model exactly how to self-correct, and nothing is written.
func TestGraphProposeMissingDatabaseIDRetryable(t *testing.T) {
	port := &fakeDesignPort{proposeResult: map[string]any{"graph_hash": "after"}}
	resource := boundDatabaseResource()
	registry := NewP2Registry(Deps{Design: port, Resource: resource})
	scope := Scope{VersionID: 7, RequirementID: 501, RequirementVersionID: 700, AgentRunID: 77}

	_, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, graphProposalWithDBAssertion(t, ""))
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument {
		t.Fatalf("code = %q, want %q (%v)", toolErr.Code, CodeInvalidArgument, err)
	}
	if !toolErr.Retryable {
		t.Fatalf("missing database_id must be retryable: %+v", toolErr)
	}
	wantField := "cases[0].assertions[0].database_id"
	if toolErr.Field != wantField {
		t.Fatalf("field = %q, want %q", toolErr.Field, wantField)
	}
	if field, _ := toolErr.Details["field"].(string); field != wantField {
		t.Fatalf("details.field = %#v, want %q", toolErr.Details["field"], wantField)
	}
	if strings.Join(toolErr.Allowed, ",") != "1501,1502" {
		t.Fatalf("allowed = %#v, want the bound database ids", toolErr.Allowed)
	}
	if !strings.Contains(toolErr.Hint, "sut.database_list") {
		t.Fatalf("hint = %q, want it to point at sut.database_list", toolErr.Hint)
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
	if got, _ := body["allowed"].([]string); len(got) != 2 || got[0] != "1501" {
		t.Fatalf("payload allowed = %#v", body["allowed"])
	}
	if hint, _ := body["hint"].(string); !strings.Contains(hint, "sut.database_list") {
		t.Fatalf("payload hint = %#v", body["hint"])
	}
	t.Logf("missing database_id error payload: %s", testJSON(t, toolErr.Payload()))
}

// TestGraphProposeRejectsUnboundDatabaseID pins R-E1-2: only databases of the
// bound environment are legal; the model cannot invent an id.
func TestGraphProposeRejectsUnboundDatabaseID(t *testing.T) {
	port := &fakeDesignPort{proposeResult: map[string]any{"graph_hash": "after"}}
	registry := NewP2Registry(Deps{Design: port, Resource: boundDatabaseResource()})
	scope := Scope{VersionID: 7, RequirementID: 501, RequirementVersionID: 700}

	_, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, graphProposalWithDBAssertion(t, "9999"))
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
		t.Fatalf("unbound database error = %+v", toolErr)
	}
	if toolErr.Field != "cases[0].assertions[0].database_id" {
		t.Fatalf("field = %q", toolErr.Field)
	}
	if strings.Join(toolErr.Allowed, ",") != "1501,1502" {
		t.Fatalf("allowed = %#v", toolErr.Allowed)
	}
	if port.proposeCalls != 0 {
		t.Fatalf("unbound database reached the design port")
	}
}

// TestGraphProposeAcceptsBoundDatabaseID is the positive half of R-E1-2: a
// database_id from the environment's database list passes validation.
func TestGraphProposeAcceptsBoundDatabaseID(t *testing.T) {
	port := &fakeDesignPort{proposeResult: map[string]any{"graph_hash": "after"}}
	registry := NewP2Registry(Deps{Design: port, Resource: boundDatabaseResource()})
	scope := Scope{VersionID: 7, RequirementID: 501, RequirementVersionID: 700}

	if _, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, graphProposalWithDBAssertion(t, "1501")); err != nil {
		t.Fatalf("bound database proposal: %v", err)
	}
	if port.proposeCalls != 1 {
		t.Fatalf("propose calls = %d, want 1", port.proposeCalls)
	}
	if got := port.proposeInput.Cases[0].Assertions[0].DatabaseID; got != 1501 {
		t.Fatalf("database id = %d, want 1501", got)
	}
	if got := port.proposeInput.Cases[0].Assertions[0].Kind; got != "db" {
		t.Fatalf("assertion kind = %q, want db", got)
	}
	t.Logf("db assertion accepted: case_version proposal assertions[0].database_id=%d", port.proposeInput.Cases[0].Assertions[0].DatabaseID)
}

// TestGraphProposeRejectsUnknownFieldWithSuggestion pins R-E1-1/R-E1-4: typos
// are rejected (aliases stay disabled) and the error names the nearest legal
// field so the model can self-correct.
func TestGraphProposeRejectsUnknownFieldWithSuggestion(t *testing.T) {
	port := &fakeDesignPort{proposeResult: map[string]any{"graph_hash": "after"}}
	registry := NewP2Registry(Deps{Design: port, Resource: boundDatabaseResource()})
	scope := Scope{VersionID: 7, RequirementID: 501, RequirementVersionID: 700}

	payload := strings.Replace(validGraphProposalJSON(), `"target_page_id": "9101"`, `"tartget_page_id": "9101"`, 1)
	if payload == validGraphProposalJSON() {
		t.Fatal("fixture did not inject the typo")
	}
	_, err := executeDesignTool(t, registry, scope, ToolCaseGraphPropose, payload)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
		t.Fatalf("unknown field error = %+v", toolErr)
	}
	wantField := "cases[0].steps[0].tartget_page_id"
	if toolErr.Field != wantField {
		t.Fatalf("field = %q, want %q", toolErr.Field, wantField)
	}
	if !strings.Contains(toolErr.Message, "target_page_id") {
		t.Fatalf("message = %q, want the nearest legal field", toolErr.Message)
	}
	if suggestion, _ := toolErr.Details["suggestion"].(string); suggestion != "target_page_id" {
		t.Fatalf("suggestion = %#v, want target_page_id", toolErr.Details["suggestion"])
	}
	if !containsString(toolErr.Allowed, "target_page_id") {
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
	registry := NewP2Registry(Deps{Design: &fakeDesignPort{}, Resource: boundDatabaseResource()})
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
	for _, field := range []string{"ref", "scenario_ref", "title", "priority", "steps", "assertions", "sources"} {
		raw, ok := caseProperties[field].(map[string]any)
		if !ok || strings.TrimSpace(stringValueOf(raw["description"])) == "" {
			t.Fatalf("case field %q is missing a description", field)
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

	assertionsSchema, _ := caseProperties["assertions"].(map[string]any)
	assertionItems, _ := assertionsSchema["items"].(map[string]any)
	assertionProperties, _ := assertionItems["properties"].(map[string]any)
	if enum := enumOf(t, assertionProperties["kind"]); strings.Join(enum, ",") != "ui,api,db,text,visual" {
		t.Fatalf("assertion kind enum = %#v", assertionProperties["kind"])
	}
	if _, ok := assertionItems["anyOf"]; !ok {
		if _, single := assertionItems["oneOf"]; !single {
			t.Fatalf("assertion branches are not expressed with anyOf/oneOf: %#v", assertionItems)
		}
	}
	if strings.TrimSpace(stringValueOf(assertionProperties["database_id"].(map[string]any)["description"])) == "" {
		t.Fatalf("assertion database_id has no description")
	}

	examples, _ := schema["examples"].([]any)
	if len(examples) < 2 {
		t.Fatalf("examples = %d, want at least 2 complete examples", len(examples))
	}
	encoded, _ := json.Marshal(examples)
	if !strings.Contains(string(encoded), `"database_id"`) {
		t.Fatalf("examples never show a db assertion: %s", encoded)
	}

	description := definitions[0].Description
	for _, want := range []string{"unknown", "database_id", "sut.database_list", "proposal_id", "base_revision"} {
		if !strings.Contains(description, want) {
			t.Fatalf("%s description lacks %q:\n%s", ToolCaseGraphPropose, want, description)
		}
	}
}

// TestGraphProposeProposalIDIsIdempotent pins R-E1-5: a duplicate submission
// with the same proposal_id returns the first result and writes nothing again.
func TestGraphProposeProposalIDIsIdempotent(t *testing.T) {
	port := &fakeDesignPort{proposeResult: map[string]any{
		"graph_hash": "after", "case_version_ids": []string{"7001"},
	}}
	registry := NewP2Registry(Deps{Design: port})
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
	registry := NewP2Registry(Deps{Design: port})
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
	registry := NewP2Registry(Deps{Design: port})
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
	registry := NewP2Registry(Deps{Design: port})
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
