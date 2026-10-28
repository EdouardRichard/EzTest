package agent_tool

import (
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// analysisToolOrder is the frozen registration order of spec §9.3/§9.4.
var analysisToolOrder = []string{
	"analysis.case_list",
	"analysis.case_detail",
	"analysis.evidence_meta",
	"analysis.code_read",
	"analysis.candidate_propose",
	"analysis.finish",
	"triage.candidate_list",
	"triage.issue_list",
	"triage.context_read",
	"triage.issue_draft",
	"triage.finish",
}

// TestNewToolsRegistersTheFrozenOrder pins the eleven tools and their order.
func TestNewToolsRegistersTheFrozenOrder(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)
	if len(harness.tools) != len(analysisToolOrder) {
		t.Fatalf("tools = %d, want %d", len(harness.tools), len(analysisToolOrder))
	}
	for index, want := range analysisToolOrder {
		tool := harness.tools[index]
		if tool.Name() != want {
			t.Fatalf("tool[%d] = %q, want %q", index, tool.Name(), want)
		}
		if strings.TrimSpace(tool.Description()) == "" {
			t.Fatalf("tool %q has no description", want)
		}
		schema := string(tool.InputSchema())
		if !strings.Contains(schema, `"type":"object"`) || !strings.Contains(schema, `"additionalProperties":false`) {
			t.Fatalf("tool %q schema must be a closed JSON object schema: %s", want, schema)
		}
	}
}

// TestNewToolsFailsClosed pins that a missing required port disables the whole
// set, mirroring the P5/P6 tool constructors.
func TestNewToolsFailsClosed(t *testing.T) {
	base := func() ToolDeps {
		harness := newToolHarness(domain.AnalysisKindFailure)
		return ToolDeps{
			Runs:        harness.runs,
			Cases:       harness.cases,
			Submissions: harness.submissions,
			Details:     harness.details,
			Evidence:    harness.evidence,
			Code:        harness.code,
		}
	}
	tests := []struct {
		name string
		drop func(*ToolDeps)
	}{
		{"runs", func(deps *ToolDeps) { deps.Runs = nil }},
		{"cases", func(deps *ToolDeps) { deps.Cases = nil }},
		{"submissions", func(deps *ToolDeps) { deps.Submissions = nil }},
		{"details", func(deps *ToolDeps) { deps.Details = nil }},
		{"evidence", func(deps *ToolDeps) { deps.Evidence = nil }},
		{"code", func(deps *ToolDeps) { deps.Code = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := base()
			test.drop(&deps)
			if tools := NewTools(deps); tools != nil {
				t.Fatalf("NewTools with a nil %s port returned %d tools, want nil (fail closed)", test.name, len(tools))
			}
		})
	}
}

// TestValidateToolsChecksEveryProfileAllowlist pins the startup fail-closed
// validation: a missing registered tool is a fatal error naming the tool.
func TestValidateToolsChecksEveryProfileAllowlist(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)
	registry := agentapp.NewToolRegistry(harness.tools...)
	if err := ValidateTools(registry); err != nil {
		t.Fatalf("ValidateTools: %v", err)
	}

	missing := "triage.issue_draft"
	filtered := make([]agentdomain.Tool, 0, len(harness.tools))
	for _, tool := range harness.tools {
		if tool.Name() != missing {
			filtered = append(filtered, tool)
		}
	}
	err := ValidateTools(agentapp.NewToolRegistry(filtered...))
	if err == nil {
		t.Fatal("ValidateTools must fail when a profile tool is not registered")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("ValidateTools error = %q, want it to name %q", err, missing)
	}
}

// TestValidateToolsAllowsTheP7Profiles pins that the two seeded profiles are the
// ones validated: every allowed tool is both in the registry and in the frozen
// spec list.
func TestValidateToolsAllowsTheP7Profiles(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)
	registered := make(map[string]struct{}, len(harness.tools))
	for _, tool := range harness.tools {
		registered[tool.Name()] = struct{}{}
	}
	for _, profile := range domain.AnalysisProfiles() {
		for _, tool := range profile.AllowedTools {
			if _, ok := registered[tool]; !ok {
				t.Fatalf("profile %q allows unregistered tool %q", profile.Key, tool)
			}
		}
	}
}

// TestAnalysisToolsRequireRunScope pins that every analysis tool refuses a call
// outside an analysis run scope with the correctable invalid_arguments code.
func TestAnalysisToolsRequireRunScope(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)
	calls := []struct {
		name string
		args string
	}{
		{"analysis.case_list", `{"batchId":"9"}`},
		{"analysis.case_detail", `{"caseRunId":"101"}`},
		{"analysis.evidence_meta", `{"evidenceIds":["701"]}`},
		{"analysis.code_read", `{"refs":[{"path":"src/cart.ts","startLine":1,"endLine":2}]}`},
		{"analysis.candidate_propose", `{"candidates":[{"caseRunId":"101","title":"t","severity":"major","confidence":"high","evidenceIds":["701"]}]}`},
		{"analysis.finish", `{"summary":"done"}`},
	}
	invocations := []struct {
		name       string
		invocation agentapp.Invocation
	}{
		{"no invocation", agentapp.Invocation{}},
		{"missing analysis run", agentapp.Invocation{SessionID: 100, VersionID: 7, BatchID: 9}},
		{"missing version", agentapp.Invocation{SessionID: 100, BatchID: 9, AnalysisRunID: 11}},
		{"missing batch", agentapp.Invocation{SessionID: 100, VersionID: 7, AnalysisRunID: 11}},
	}
	for _, call := range calls {
		for _, scope := range invocations {
			t.Run(call.name+"/"+scope.name, func(t *testing.T) {
				harness.runs.calls = 0
				_, err := callTool(t, harness.tools, scope.invocation, call.name, call.args)
				requireToolError(t, err, "invalid_arguments")
				if scope.invocation.AnalysisRunID == 0 && harness.runs.calls != 0 {
					t.Fatal("the run store must not be read for a missing scope")
				}
			})
		}
	}
}

// TestAnalysisToolsRejectForeignRun pins the cross-version/kind ownership rule:
// the loaded run must belong to the invocation scope and be the expected kind.
func TestAnalysisToolsRejectForeignRun(t *testing.T) {
	failure := newToolHarness(domain.AnalysisKindFailure)
	foreign := failureRunFixture()
	foreign.VersionID = 8
	failure.runs.run = foreign
	_, err := callTool(t, failure.tools, analysisFailureInvocation, "analysis.case_list", `{"batchId":"9"}`)
	requireToolError(t, err, "invalid_arguments")

	triageOnFailure := newToolHarness(domain.AnalysisKindIssueTriage)
	triageOnFailure.runs.run = failureRunFixture()
	_, err = callTool(t, triageOnFailure.tools, analysisTriageInvocation, "triage.candidate_list", `{}`)
	requireToolError(t, err, "invalid_arguments")

	failureOnTriage := newToolHarness(domain.AnalysisKindFailure)
	failureOnTriage.runs.run = triageRunFixture()
	_, err = callTool(t, failureOnTriage.tools, analysisFailureInvocation, "analysis.case_list", `{"batchId":"9"}`)
	requireToolError(t, err, "invalid_arguments")
}

// TestAnalysisToolsReportMissingRun pins the not_found mapping of the run store.
func TestAnalysisToolsReportMissingRun(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)
	harness.runs.err = deliveryapp.ErrAnalysisRunNotFound
	_, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.case_list", `{"batchId":"9"}`)
	requireToolError(t, err, "not_found")
}

// TestAnalysisToolsRejectCrossBatchArguments pins the batch argument ownership
// check: the model may not address another batch even inside its own version.
func TestAnalysisToolsRejectCrossBatchArguments(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)
	_, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.case_list", `{"batchId":"10"}`)
	requireToolError(t, err, "invalid_arguments")
	if harness.cases.calls != 0 {
		t.Fatalf("failed-case reads = %d, want 0 after a rejected batch argument", harness.cases.calls)
	}
}

// TestSubmissionToolsRetryOnlyWhileRunning pins that a submission against a
// finished run is refused before touching the analysis service.
func TestSubmissionToolsRetryOnlyWhileRunning(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)
	finished := failureRunFixture()
	finished.Status = domain.AnalysisDone
	harness.runs.run = finished

	_, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.candidate_propose",
		`{"candidates":[{"caseRunId":"101","title":"t","severity":"major","confidence":"high","evidenceIds":["701"]}]}`)
	requireToolError(t, err, "run_not_active")
	if harness.submissions.candidateCalls != 0 {
		t.Fatal("the analysis service must not be called for a finished run")
	}
}

// TestToolsRejectMalformedArguments pins the strict JSON object contract:
// unknown fields and non-object arguments are correctable failures.
func TestToolsRejectMalformedArguments(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)
	_, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.case_list", `{"batchId":"9","extra":true}`)
	requireToolError(t, err, "invalid_arguments")
	_, err = callTool(t, harness.tools, analysisFailureInvocation, "analysis.case_list", `[]`)
	requireToolError(t, err, "invalid_arguments")
	_, err = callTool(t, harness.tools, analysisFailureInvocation, "analysis.finish", `{"summary":"x","extra":1}`)
	requireToolError(t, err, "invalid_arguments")
}

// TestAnalysisToolsMatchTheSeededAllowlists pins that the registered names are
// exactly the domain tool constants the profile seeds allow.
func TestAnalysisToolsMatchTheSeededAllowlists(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)
	registered := make(map[string]struct{}, len(harness.tools))
	for _, tool := range harness.tools {
		registered[tool.Name()] = struct{}{}
	}
	for _, name := range domain.FailureAnalysisAllowedTools() {
		if _, ok := registered[name]; !ok {
			t.Fatalf("failure-analysis tool %q is not registered", name)
		}
	}
	for _, name := range domain.IssueTriageAllowedTools() {
		if _, ok := registered[name]; !ok {
			t.Fatalf("issue-triage tool %q is not registered", name)
		}
	}
}

// compile-time guard: the fake reads satisfy the T4 port.
var _ deliveryapp.AnalysisExecutionReader = (*fakeCases)(nil)
