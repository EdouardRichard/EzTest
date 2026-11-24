package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakeExecutionPort records every P3 execution tool delegation.
type fakeExecutionPort struct {
	httpArgs   json.RawMessage
	dbArgs     json.RawMessage
	schemaArgs json.RawMessage

	step      StepRecordInput
	assertion AssertionRecordInput
	evidence  EvidenceTextInput
	finish    CaseFinishInput
	wait      WaitInput
	human     HumanRequestInput
	creds     AccountCredentialsInput
	result    ResultReadInput
	evidenceR EvidenceReadInput
	failure   FailureSubmitInput
	coverage  CoverageSubmitInput

	scope       Scope
	dbErr       error
	stepErr     error
	coverageErr error
}

func (f *fakeExecutionPort) StepRecord(_ context.Context, scope Scope, in StepRecordInput) (any, error) {
	f.scope = scope
	f.step = in
	return map[string]any{"step": true}, f.stepErr
}

func (f *fakeExecutionPort) AssertionRecord(_ context.Context, scope Scope, in AssertionRecordInput) (any, error) {
	f.scope = scope
	f.assertion = in
	return map[string]any{"assertion": true}, nil
}

func (f *fakeExecutionPort) EvidenceText(_ context.Context, _ Scope, in EvidenceTextInput) (any, error) {
	f.evidence = in
	return map[string]any{"evidence": true}, nil
}

func (f *fakeExecutionPort) CaseFinish(_ context.Context, scope Scope, in CaseFinishInput) (any, error) {
	f.scope = scope
	f.finish = in
	return map[string]any{"finish": true}, nil
}

func (f *fakeExecutionPort) Wait(_ context.Context, _ Scope, in WaitInput) (any, error) {
	f.wait = in
	return map[string]any{"waited_seconds": in.Seconds}, nil
}

func (f *fakeExecutionPort) HumanRequest(_ context.Context, scope Scope, in HumanRequestInput) (any, error) {
	f.scope = scope
	f.human = in
	return map[string]any{"status": "open"}, nil
}

func (f *fakeExecutionPort) HTTP(_ context.Context, _ Scope, args json.RawMessage) (any, error) {
	f.httpArgs = args
	return map[string]any{"status": 200}, nil
}

func (f *fakeExecutionPort) DBQuery(_ context.Context, _ Scope, args json.RawMessage) (any, error) {
	f.dbArgs = args
	return map[string]any{"rows": []any{}}, f.dbErr
}

func (f *fakeExecutionPort) SchemaRead(_ context.Context, _ Scope, args json.RawMessage) (any, error) {
	f.schemaArgs = args
	return map[string]any{"tables": []any{}}, nil
}

func (f *fakeExecutionPort) AccountCredentials(_ context.Context, _ Scope, in AccountCredentialsInput) (any, error) {
	f.creds = in
	return map[string]any{"username": "u", "password": "p"}, nil
}

func (f *fakeExecutionPort) ResultRead(_ context.Context, _ Scope, in ResultReadInput) (any, error) {
	f.result = in
	return map[string]any{"results": []any{}}, nil
}

func (f *fakeExecutionPort) EvidenceRead(_ context.Context, _ Scope, in EvidenceReadInput) (any, error) {
	f.evidenceR = in
	return map[string]any{"text": ""}, nil
}

func (f *fakeExecutionPort) CoverageRead(_ context.Context, _ Scope) (any, error) {
	return map[string]any{"requirements": []any{}}, nil
}

func (f *fakeExecutionPort) FailureSubmit(_ context.Context, _ Scope, in FailureSubmitInput) (any, error) {
	f.failure = in
	return map[string]any{"ok": true}, nil
}

func (f *fakeExecutionPort) CoverageSubmit(_ context.Context, _ Scope, in CoverageSubmitInput) (any, error) {
	f.coverage = in
	return map[string]any{"ok": true}, f.coverageErr
}

func executionScope() Scope {
	return Scope{
		VersionID: 10, WaveID: 2001, CaseRunID: 3001, IntentID: 1001,
		CaseVersionID: 41, DispatchEpoch: 3, DispatchToken: "dispatch-token",
		AgentRunID: 9001,
	}
}

func executionRegistry(port ExecutionPort) *Registry {
	return NewExecutionRegistry(Deps{Execution: port})
}

func executeTool(t *testing.T, registry *Registry, name, raw string) (any, error) {
	t.Helper()
	return registry.Execute(context.Background(), executionScope(), name, json.RawMessage(raw))
}

func requireToolError(t *testing.T, err error, code string) {
	t.Helper()
	var toolErr *Error
	if !errors.As(err, &toolErr) {
		t.Fatalf("error %v is not a *tools.Error", err)
	}
	if toolErr.Code != code {
		t.Fatalf("tool error code = %q, want %q", toolErr.Code, code)
	}
}

// TestExecutionRegistryRetiresBrowserDomain pins the v0.2.4-C T13 removal contract plus
// the T15 browser-domain re-introduction: the 13 browser.* atomic-action tools
// are gone from the execution registry and the admin catalog, while browser.cli
// (the official playwright CLI passthrough) is the one legitimate browser-domain
// tool. The assertion derives from the capability-domain authority (ToolDomain)
// rather than a hand-copied name list, so it still catches any retired atomic
// action sneaking back into the fixed-action surface.
func TestExecutionRegistryRetiresBrowserDomain(t *testing.T) {
	registries := map[string]*Registry{
		"P3 execution":  executionRegistry(&fakeExecutionPort{}),
		"admin catalog": AdminCatalogRegistry(),
	}
	for label, registry := range registries {
		for _, name := range registry.Names() {
			if ToolDomain(name) == "browser" && name != ToolBrowserCLI {
				t.Fatalf("%s registry still registers retired browser-domain tool %q", label, name)
			}
		}
	}
	// 28 frozen P3 names minus the 13 retired browser actions.
	if names := CaseExecutionToolNames(); len(names) != 15 {
		t.Fatalf("CaseExecutionToolNames() = %d tools (%v), want 15 after the browser.* removal", len(names), names)
	}
	for _, name := range CaseExecutionToolNames() {
		if ToolDomain(name) == "browser" {
			t.Fatalf("CaseExecutionToolNames still lists browser-domain tool %q", name)
		}
	}
}

func TestExecutionToolSchemasExcludeVersionID(t *testing.T) {
	registry := executionRegistry(&fakeExecutionPort{})
	names := CaseExecutionToolNames()
	if len(names) < 15 {
		t.Fatalf("CaseExecutionToolNames() = %d tools", len(names))
	}
	for _, name := range names {
		definitions := registry.Definitions([]string{name})
		if len(definitions) != 1 {
			t.Fatalf("tool %q is not registered", name)
		}
		schema := string(definitions[0].Parameters)
		for _, forbidden := range []string{"version_id", "versionId", "agent_run_id"} {
			if strings.Contains(schema, forbidden) {
				t.Fatalf("tool %q schema exposes %q: %s", name, forbidden, schema)
			}
		}
		var object map[string]any
		if err := json.Unmarshal(definitions[0].Parameters, &object); err != nil {
			t.Fatalf("tool %q schema is not JSON: %v", name, err)
		}
		if object["type"] != "object" || object["additionalProperties"] != false {
			t.Fatalf("tool %q schema is not a closed object: %s", name, schema)
		}
	}
}

func TestExecutionRegistryExtendsDesignAndBase(t *testing.T) {
	registry := executionRegistry(&fakeExecutionPort{})
	for _, name := range append(BaseToolNames(), ToolDesignGraphRead, ToolCaseGraphPropose) {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("P3 registry is missing inherited tool %q", name)
		}
	}
}

func TestExecutionLifecycleToolsDelegate(t *testing.T) {
	port := &fakeExecutionPort{}
	registry := executionRegistry(port)

	if _, err := executeTool(t, registry, ToolExecutionStepRecord, `{"seq":1,"status":"passed","observed":"ok","sut_id":"7"}`); err != nil {
		t.Fatalf("step_record: %v", err)
	}
	if port.step.Seq != 1 || port.step.Status != "passed" || port.step.Observed != "ok" ||
		port.step.VersionSUTID == nil || *port.step.VersionSUTID != 7 {
		t.Fatalf("step input = %+v", port.step)
	}
	if _, err := executeTool(t, registry, ToolExecutionStepRecord, `{"seq":1,"status":"bogus"}`); err == nil {
		t.Fatalf("step_record accepted an unknown status")
	}
	if _, err := executeTool(t, registry, ToolExecutionStepRecord, `{"seq":1,"status":"passed","version_id":"9"}`); err == nil {
		t.Fatalf("step_record accepted a version_id override")
	} else {
		requireToolError(t, err, CodeInvalidArgument)
	}

	if _, err := executeTool(t, registry, ToolExecutionAssertionRecord, `{"step_seq":1,"assertion_seq":1,"verdict":"failed","expected":"伪造","actual":"x","reason":"mismatch"}`); err != nil {
		t.Fatalf("assertion_record: %v", err)
	}
	if port.assertion.StepSeq != 1 || port.assertion.Verdict != "failed" || port.assertion.Expected != "伪造" {
		t.Fatalf("assertion input = %+v", port.assertion)
	}
	if _, err := executeTool(t, registry, ToolExecutionAssertionRecord, `{"step_seq":1,"assertion_seq":1,"verdict":"passed","extra":true}`); err == nil {
		t.Fatalf("assertion_record accepted an unknown field")
	}

	if _, err := executeTool(t, registry, ToolExecutionEvidence, `{"kind":"text","text":"hello","step_seq":1}`); err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if port.evidence.Kind != "text" || port.evidence.Text != "hello" || port.evidence.StepSeq == nil || *port.evidence.StepSeq != 1 {
		t.Fatalf("evidence input = %+v", port.evidence)
	}

	if _, err := executeTool(t, registry, ToolExecutionWait, `{"seconds":120,"reason":"等待 120s 窗口"}`); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if port.wait.Seconds != 120 || port.wait.Reason != "等待 120s 窗口" {
		t.Fatalf("wait input = %+v", port.wait)
	}
	for _, seconds := range []string{"0", "301"} {
		if _, err := executeTool(t, registry, ToolExecutionWait, `{"seconds":`+seconds+`}`); err == nil {
			t.Fatalf("wait accepted %s seconds", seconds)
		}
	}

	if _, err := executeTool(t, registry, ToolExecutionCaseFinish, `{"verdict":"failed","summary":"断言不符","failure_class":"application"}`); err != nil {
		t.Fatalf("case_finish: %v", err)
	}
	if port.finish.Verdict != "failed" || port.finish.FailureClass != "application" {
		t.Fatalf("finish input = %+v", port.finish)
	}
	if _, err := executeTool(t, registry, ToolExecutionCaseFinish, `{"verdict":"passed","failure_class":"application"}`); err == nil {
		t.Fatalf("case_finish accepted passed with a failure class")
	}

	if _, err := executeTool(t, registry, ToolHumanRequest, `{"kind":"login_takeover","reason":"需要 2FA","account_role":"admin"}`); err != nil {
		t.Fatalf("human.request: %v", err)
	}
	if port.human.Kind != "login_takeover" || port.human.Reason != "需要 2FA" {
		t.Fatalf("human input = %+v", port.human)
	}

	if _, err := executeTool(t, registry, ToolAccountCredentials, `{"sut_id":"7","role":"admin"}`); err != nil {
		t.Fatalf("account.credentials: %v", err)
	}
	if port.creds.Role != "admin" || port.creds.VersionSUTID == nil || *port.creds.VersionSUTID != 7 {
		t.Fatalf("credentials input = %+v", port.creds)
	}

	if _, err := executeTool(t, registry, ToolHTTPRequest, `{"path":"/api/orders","method":"GET"}`); err != nil {
		t.Fatalf("http.request: %v", err)
	}
	if !strings.Contains(string(port.httpArgs), "/api/orders") {
		t.Fatalf("http args = %s", port.httpArgs)
	}
	if _, err := executeTool(t, registry, ToolDBQuery, `{"sql":"select 1","max_rows":10}`); err != nil {
		t.Fatalf("db.query: %v", err)
	}
	if !strings.Contains(string(port.dbArgs), "select 1") {
		t.Fatalf("db args = %s", port.dbArgs)
	}
	if _, err := executeTool(t, registry, ToolDatabaseSchemaRead, `{"schema":"public"}`); err != nil {
		t.Fatalf("database.schema_read: %v", err)
	}
	if !strings.Contains(string(port.schemaArgs), "public") {
		t.Fatalf("schema args = %s", port.schemaArgs)
	}
}

func TestExecutionAnalysisToolsDelegate(t *testing.T) {
	port := &fakeExecutionPort{}
	registry := executionRegistry(port)

	if _, err := executeTool(t, registry, ToolExecutionResultRead, `{"case_run_id":"3001"}`); err != nil {
		t.Fatalf("execution.result_read: %v", err)
	}
	if port.result.CaseRunID == nil || *port.result.CaseRunID != 3001 {
		t.Fatalf("result input = %+v", port.result)
	}
	if _, err := executeTool(t, registry, ToolExecutionEvidenceRead, `{"evidence_id":"77","offset":5,"max_chars":100}`); err != nil {
		t.Fatalf("execution.evidence_read: %v", err)
	}
	if port.evidenceR.EvidenceID != 77 || port.evidenceR.Offset != 5 || port.evidenceR.MaxChars != 100 {
		t.Fatalf("evidence read input = %+v", port.evidenceR)
	}
	if _, err := executeTool(t, registry, ToolExecutionCoverageRead, `{}`); err != nil {
		t.Fatalf("execution.coverage_read: %v", err)
	}
	if _, err := executeTool(t, registry, ToolAnalysisFailureSubmit, `{"case_run_id":"3001","failure_class":"application","category":"ui","severity":"major","summary":"按钮文案不符"}`); err != nil {
		t.Fatalf("analysis.failure_submit: %v", err)
	}
	if port.failure.CaseRunID != 3001 || port.failure.FailureClass != "application" || port.failure.Category != "ui" {
		t.Fatalf("failure input = %+v", port.failure)
	}
	if _, err := executeTool(t, registry, ToolAnalysisCoverageSubmit, `{"requirement_id":"501","reason":"execution_not_run","evidence_case_run_ids":["3001"]}`); err != nil {
		t.Fatalf("analysis.coverage_submit: %v", err)
	}
	if port.coverage.RequirementID != 501 || len(port.coverage.EvidenceCaseRunIDs) != 1 || port.coverage.EvidenceCaseRunIDs[0] != 3001 {
		t.Fatalf("coverage input = %+v", port.coverage)
	}
}

func TestExecutionToolPortErrorMapping(t *testing.T) {
	port := &fakeExecutionPort{
		stepErr: NewError(CodeLeaseLost, "case_run lease was taken over"),
	}
	registry := executionRegistry(port)
	var err error
	if _, err = executeTool(t, registry, ToolExecutionStepRecord, `{"seq":1,"status":"passed"}`); err == nil {
		t.Fatalf("expected the port error to surface")
	}
	requireToolError(t, err, CodeLeaseLost)

	port.dbErr = NewError(CodeNotFound, "version_sut 7 not found")
	_, err = executeTool(t, registry, ToolDBQuery, `{"sql":"select 1"}`)
	requireToolError(t, err, CodeNotFound)

	// A missing execution port is an internal configuration error, not a tool
	// business error, so it fails the run instead of being retried by the model.
	registry = NewExecutionRegistry(Deps{})
	if _, err = executeTool(t, registry, ToolExecutionWait, `{"seconds":1}`); err == nil {
		t.Fatalf("expected a missing execution port to fail")
	} else {
		var toolErr *Error
		if errors.As(err, &toolErr) {
			t.Fatalf("missing port must not be a tool error: %+v", toolErr)
		}
	}
}

func TestNoArgToolSchemasSerializePropertiesAsObject(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal(schemaObject(nil), &document); err != nil {
		t.Fatalf("schemaObject: %v", err)
	}
	properties, ok := document["properties"].(map[string]any)
	if !ok || len(properties) != 0 {
		t.Fatalf("properties = %#v, want empty object (provider rejects null)", document["properties"])
	}
}
