package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakeP3ExecutionPort records every P3 execution tool delegation.
type fakeP3ExecutionPort struct {
	browserAction string
	browserArgs   json.RawMessage
	httpArgs      json.RawMessage
	dbArgs        json.RawMessage
	schemaArgs    json.RawMessage

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
	browserErr  error
	stepErr     error
	coverageErr error
}

func (f *fakeP3ExecutionPort) StepRecord(_ context.Context, scope Scope, in StepRecordInput) (any, error) {
	f.scope = scope
	f.step = in
	return map[string]any{"step": true}, f.stepErr
}

func (f *fakeP3ExecutionPort) AssertionRecord(_ context.Context, scope Scope, in AssertionRecordInput) (any, error) {
	f.scope = scope
	f.assertion = in
	return map[string]any{"assertion": true}, nil
}

func (f *fakeP3ExecutionPort) EvidenceText(_ context.Context, _ Scope, in EvidenceTextInput) (any, error) {
	f.evidence = in
	return map[string]any{"evidence": true}, nil
}

func (f *fakeP3ExecutionPort) CaseFinish(_ context.Context, scope Scope, in CaseFinishInput) (any, error) {
	f.scope = scope
	f.finish = in
	return map[string]any{"finish": true}, nil
}

func (f *fakeP3ExecutionPort) Wait(_ context.Context, _ Scope, in WaitInput) (any, error) {
	f.wait = in
	return map[string]any{"waited_seconds": in.Seconds}, nil
}

func (f *fakeP3ExecutionPort) HumanRequest(_ context.Context, scope Scope, in HumanRequestInput) (any, error) {
	f.scope = scope
	f.human = in
	return map[string]any{"status": "open"}, nil
}

func (f *fakeP3ExecutionPort) Browser(_ context.Context, scope Scope, action string, args json.RawMessage) (any, error) {
	f.scope = scope
	f.browserAction = action
	f.browserArgs = args
	return map[string]any{"ok": true}, f.browserErr
}

func (f *fakeP3ExecutionPort) HTTP(_ context.Context, _ Scope, args json.RawMessage) (any, error) {
	f.httpArgs = args
	return map[string]any{"status": 200}, nil
}

func (f *fakeP3ExecutionPort) DBQuery(_ context.Context, _ Scope, args json.RawMessage) (any, error) {
	f.dbArgs = args
	return map[string]any{"rows": []any{}}, nil
}

func (f *fakeP3ExecutionPort) SchemaRead(_ context.Context, _ Scope, args json.RawMessage) (any, error) {
	f.schemaArgs = args
	return map[string]any{"tables": []any{}}, nil
}

func (f *fakeP3ExecutionPort) AccountCredentials(_ context.Context, _ Scope, in AccountCredentialsInput) (any, error) {
	f.creds = in
	return map[string]any{"username": "u", "password": "p"}, nil
}

func (f *fakeP3ExecutionPort) ResultRead(_ context.Context, _ Scope, in ResultReadInput) (any, error) {
	f.result = in
	return map[string]any{"results": []any{}}, nil
}

func (f *fakeP3ExecutionPort) EvidenceRead(_ context.Context, _ Scope, in EvidenceReadInput) (any, error) {
	f.evidenceR = in
	return map[string]any{"text": ""}, nil
}

func (f *fakeP3ExecutionPort) CoverageRead(_ context.Context, _ Scope) (any, error) {
	return map[string]any{"requirements": []any{}}, nil
}

func (f *fakeP3ExecutionPort) FailureSubmit(_ context.Context, _ Scope, in FailureSubmitInput) (any, error) {
	f.failure = in
	return map[string]any{"ok": true}, nil
}

func (f *fakeP3ExecutionPort) CoverageSubmit(_ context.Context, _ Scope, in CoverageSubmitInput) (any, error) {
	f.coverage = in
	return map[string]any{"ok": true}, f.coverageErr
}

func p3Scope() Scope {
	return Scope{
		VersionID: 10, WaveID: 2001, CaseRunID: 3001, IntentID: 1001,
		CaseVersionID: 41, DispatchEpoch: 3, DispatchToken: "dispatch-token",
		AgentRunID: 9001,
	}
}

func p3Registry(port ExecutionPort) *Registry {
	return NewP3Registry(Deps{Execution: port})
}

func executeP3(t *testing.T, registry *Registry, name, raw string) (any, error) {
	t.Helper()
	return registry.Execute(context.Background(), p3Scope(), name, json.RawMessage(raw))
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

func TestP3ToolSchemasExcludeVersionID(t *testing.T) {
	registry := p3Registry(&fakeP3ExecutionPort{})
	names := P3ToolNames()
	if len(names) < 20 {
		t.Fatalf("P3ToolNames() = %d tools", len(names))
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

func TestP3RegistryExtendsP2AndP1(t *testing.T) {
	registry := p3Registry(&fakeP3ExecutionPort{})
	for _, name := range append(P1ToolNames(), ToolDesignGraphRead, ToolCaseGraphPropose) {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("P3 registry is missing inherited tool %q", name)
		}
	}
}

func TestP3BrowserToolsDelegateAndValidate(t *testing.T) {
	port := &fakeP3ExecutionPort{}
	registry := p3Registry(port)

	if _, err := executeP3(t, registry, ToolBrowserGoto, `{"version_sut_id":"7","account_role":"admin","url":"http://127.0.0.1:13335/login"}`); err != nil {
		t.Fatalf("browser.goto: %v", err)
	}
	if port.browserAction != "goto" {
		t.Fatalf("browser action = %q", port.browserAction)
	}
	var args map[string]any
	if err := json.Unmarshal(port.browserArgs, &args); err != nil {
		t.Fatalf("browser args are not JSON: %v", err)
	}
	if args["url"] != "http://127.0.0.1:13335/login" || args["version_sut_id"] != "7" {
		t.Fatalf("browser args = %v", args)
	}
	if port.scope.CaseRunID != 3001 {
		t.Fatalf("scope = %+v", port.scope)
	}

	if _, err := executeP3(t, registry, ToolBrowserGoto, `{"url":""}`); err == nil {
		t.Fatalf("browser.goto accepted an empty url")
	} else {
		requireToolError(t, err, CodeInvalidArgument)
	}
	if _, err := executeP3(t, registry, ToolBrowserClick, `{"target":{"text":"登录"}}`); err != nil {
		t.Fatalf("browser.click: %v", err)
	}
	if port.browserAction != "click" {
		t.Fatalf("browser action = %q", port.browserAction)
	}
	if _, err := executeP3(t, registry, ToolBrowserClick, `{}`); err == nil {
		t.Fatalf("browser.click accepted a call without target")
	}
	if _, err := executeP3(t, registry, ToolBrowserFill, `{"target":{"selector":"#user"},"value":"alice"}`); err != nil {
		t.Fatalf("browser.fill: %v", err)
	}
	if _, err := executeP3(t, registry, ToolBrowserFill, `{"target":{"selector":"#pwd"}}`); err == nil {
		t.Fatalf("browser.fill accepted a call without value")
	}
	if _, err := executeP3(t, registry, ToolBrowserWaitFor, `{"timeout_ms":70000}`); err == nil {
		t.Fatalf("browser.wait_for accepted timeout_ms over 60000")
	}
	if _, err := executeP3(t, registry, ToolBrowserScreenshot, `{"full_page":true}`); err != nil {
		t.Fatalf("browser.screenshot: %v", err)
	}
	// Scope IDs cannot be smuggled through tool parameters.
	_, err := executeP3(t, registry, ToolBrowserGoto, `{"url":"http://x","version_id":"9"}`)
	requireToolError(t, err, CodeInvalidArgument)
}

func TestP3ExecutionLifecycleToolsDelegate(t *testing.T) {
	port := &fakeP3ExecutionPort{}
	registry := p3Registry(port)

	if _, err := executeP3(t, registry, ToolExecutionStepRecord, `{"seq":1,"status":"passed","observed":"ok","version_sut_id":"7"}`); err != nil {
		t.Fatalf("step_record: %v", err)
	}
	if port.step.Seq != 1 || port.step.Status != "passed" || port.step.Observed != "ok" ||
		port.step.VersionSUTID == nil || *port.step.VersionSUTID != 7 {
		t.Fatalf("step input = %+v", port.step)
	}
	if _, err := executeP3(t, registry, ToolExecutionStepRecord, `{"seq":1,"status":"bogus"}`); err == nil {
		t.Fatalf("step_record accepted an unknown status")
	}
	if _, err := executeP3(t, registry, ToolExecutionStepRecord, `{"seq":1,"status":"passed","version_id":"9"}`); err == nil {
		t.Fatalf("step_record accepted a version_id override")
	} else {
		requireToolError(t, err, CodeInvalidArgument)
	}

	if _, err := executeP3(t, registry, ToolExecutionAssertionRecord, `{"step_seq":1,"assertion_seq":1,"verdict":"failed","expected":"伪造","actual":"x","reason":"mismatch"}`); err != nil {
		t.Fatalf("assertion_record: %v", err)
	}
	if port.assertion.StepSeq != 1 || port.assertion.Verdict != "failed" || port.assertion.Expected != "伪造" {
		t.Fatalf("assertion input = %+v", port.assertion)
	}
	if _, err := executeP3(t, registry, ToolExecutionAssertionRecord, `{"step_seq":1,"assertion_seq":1,"verdict":"passed","extra":true}`); err == nil {
		t.Fatalf("assertion_record accepted an unknown field")
	}

	if _, err := executeP3(t, registry, ToolExecutionEvidence, `{"kind":"text","text":"hello","step_seq":1}`); err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if port.evidence.Kind != "text" || port.evidence.Text != "hello" || port.evidence.StepSeq == nil || *port.evidence.StepSeq != 1 {
		t.Fatalf("evidence input = %+v", port.evidence)
	}

	if _, err := executeP3(t, registry, ToolExecutionWait, `{"seconds":120,"reason":"等待 120s 窗口"}`); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if port.wait.Seconds != 120 || port.wait.Reason != "等待 120s 窗口" {
		t.Fatalf("wait input = %+v", port.wait)
	}
	for _, seconds := range []string{"0", "301"} {
		if _, err := executeP3(t, registry, ToolExecutionWait, `{"seconds":`+seconds+`}`); err == nil {
			t.Fatalf("wait accepted %s seconds", seconds)
		}
	}

	if _, err := executeP3(t, registry, ToolExecutionCaseFinish, `{"verdict":"failed","summary":"断言不符","failure_class":"application"}`); err != nil {
		t.Fatalf("case_finish: %v", err)
	}
	if port.finish.Verdict != "failed" || port.finish.FailureClass != "application" {
		t.Fatalf("finish input = %+v", port.finish)
	}
	if _, err := executeP3(t, registry, ToolExecutionCaseFinish, `{"verdict":"passed","failure_class":"application"}`); err == nil {
		t.Fatalf("case_finish accepted passed with a failure class")
	}

	if _, err := executeP3(t, registry, ToolHumanRequest, `{"kind":"login_takeover","reason":"需要 2FA","account_role":"admin"}`); err != nil {
		t.Fatalf("human.request: %v", err)
	}
	if port.human.Kind != "login_takeover" || port.human.Reason != "需要 2FA" {
		t.Fatalf("human input = %+v", port.human)
	}

	if _, err := executeP3(t, registry, ToolAccountCredentials, `{"version_sut_id":"7","role":"admin"}`); err != nil {
		t.Fatalf("account.credentials: %v", err)
	}
	if port.creds.Role != "admin" || port.creds.VersionSUTID == nil || *port.creds.VersionSUTID != 7 {
		t.Fatalf("credentials input = %+v", port.creds)
	}

	if _, err := executeP3(t, registry, ToolHTTPRequest, `{"path":"/api/orders","method":"GET"}`); err != nil {
		t.Fatalf("http.request: %v", err)
	}
	if !strings.Contains(string(port.httpArgs), "/api/orders") {
		t.Fatalf("http args = %s", port.httpArgs)
	}
	if _, err := executeP3(t, registry, ToolDBQuery, `{"sql":"select 1","max_rows":10}`); err != nil {
		t.Fatalf("db.query: %v", err)
	}
	if !strings.Contains(string(port.dbArgs), "select 1") {
		t.Fatalf("db args = %s", port.dbArgs)
	}
	if _, err := executeP3(t, registry, ToolDatabaseSchemaRead, `{"schema":"public"}`); err != nil {
		t.Fatalf("database.schema_read: %v", err)
	}
	if !strings.Contains(string(port.schemaArgs), "public") {
		t.Fatalf("schema args = %s", port.schemaArgs)
	}
}

func TestP3AnalysisToolsDelegate(t *testing.T) {
	port := &fakeP3ExecutionPort{}
	registry := p3Registry(port)

	if _, err := executeP3(t, registry, ToolExecutionResultRead, `{"case_run_id":"3001"}`); err != nil {
		t.Fatalf("execution.result_read: %v", err)
	}
	if port.result.CaseRunID == nil || *port.result.CaseRunID != 3001 {
		t.Fatalf("result input = %+v", port.result)
	}
	if _, err := executeP3(t, registry, ToolExecutionEvidenceRead, `{"evidence_id":"77","offset":5,"max_chars":100}`); err != nil {
		t.Fatalf("execution.evidence_read: %v", err)
	}
	if port.evidenceR.EvidenceID != 77 || port.evidenceR.Offset != 5 || port.evidenceR.MaxChars != 100 {
		t.Fatalf("evidence read input = %+v", port.evidenceR)
	}
	if _, err := executeP3(t, registry, ToolExecutionCoverageRead, `{}`); err != nil {
		t.Fatalf("execution.coverage_read: %v", err)
	}
	if _, err := executeP3(t, registry, ToolAnalysisFailureSubmit, `{"case_run_id":"3001","failure_class":"application","category":"ui","severity":"major","summary":"按钮文案不符"}`); err != nil {
		t.Fatalf("analysis.failure_submit: %v", err)
	}
	if port.failure.CaseRunID != 3001 || port.failure.FailureClass != "application" || port.failure.Category != "ui" {
		t.Fatalf("failure input = %+v", port.failure)
	}
	if _, err := executeP3(t, registry, ToolAnalysisCoverageSubmit, `{"requirement_id":"501","reason":"execution_not_run","evidence_case_run_ids":["3001"]}`); err != nil {
		t.Fatalf("analysis.coverage_submit: %v", err)
	}
	if port.coverage.RequirementID != 501 || len(port.coverage.EvidenceCaseRunIDs) != 1 || port.coverage.EvidenceCaseRunIDs[0] != 3001 {
		t.Fatalf("coverage input = %+v", port.coverage)
	}
}

func TestP3ToolPortErrorMapping(t *testing.T) {
	port := &fakeP3ExecutionPort{
		stepErr: NewError(CodeLeaseLost, "case_run lease was taken over"),
	}
	registry := p3Registry(port)
	var err error
	if _, err = executeP3(t, registry, ToolExecutionStepRecord, `{"seq":1,"status":"passed"}`); err == nil {
		t.Fatalf("expected the port error to surface")
	}
	requireToolError(t, err, CodeLeaseLost)

	port.browserErr = NewError(CodeNotFound, "version_sut 7 not found")
	_, err = executeP3(t, registry, ToolBrowserSnapshot, `{}`)
	requireToolError(t, err, CodeNotFound)

	// A missing execution port is an internal configuration error, not a tool
	// business error, so it fails the run instead of being retried by the model.
	registry = NewP3Registry(Deps{})
	if _, err = executeP3(t, registry, ToolExecutionWait, `{"seconds":1}`); err == nil {
		t.Fatalf("expected a missing execution port to fail")
	} else {
		var toolErr *Error
		if errors.As(err, &toolErr) {
			t.Fatalf("missing port must not be a tool error: %+v", toolErr)
		}
	}
}
