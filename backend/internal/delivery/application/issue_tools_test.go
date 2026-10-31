package deliveryapp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agenttools "github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// fakeTriagePoster records the delivery team-room finding emitted by a accepted
// triage proposal.
type fakeTriagePoster struct {
	findings []TriageFinding
	err      error
}

func (p *fakeTriagePoster) PostTriageFinding(_ context.Context, finding TriageFinding) error {
	p.findings = append(p.findings, finding)
	return p.err
}

var _ TriagePoster = (*fakeTriagePoster)(nil)

type issueToolsFixture struct {
	issueSvc  *IssueService
	analysis  *AnalysisService
	repo      *fakeIssueRepository
	exec      *fakeIssueExecution
	analysisR *fakeAnalysisRepository
	poster    *fakeTriagePoster
	tools     []agenttools.Tool
}

func newIssueToolsFixture(t *testing.T) *issueToolsFixture {
	t.Helper()
	issues := newIssueServiceFixture()
	analysisRepo := newFakeAnalysisRepository()
	poster := &fakeTriagePoster{}
	analysis := NewAnalysisService(analysisRepo, issues.svc, fakeIssueClock{now: analysisTestNow}).WithTriagePoster(poster)
	waveID := int64(11)
	versionSUTID := int64(9001)
	issues.repo.getIssues[900] = delivery.Issue{
		ID:               900,
		VersionID:        7,
		Title:            "checkout fails",
		Summary:          "order confirmation missing",
		Status:           delivery.IssueStatusOpen,
		Category:         "ui",
		Severity:         "major",
		ExpectedRevision: 1,
		Source:           delivery.IssueCandidateSource,
	}
	issues.repo.getIssues[901] = delivery.Issue{
		ID: 901, VersionID: 7, Title: "duplicate checkout failure", Status: delivery.IssueStatusOpen,
	}
	issues.repo.getIssues[902] = delivery.Issue{
		ID: 902, VersionID: 8, Title: "foreign issue", Status: delivery.IssueStatusOpen,
	}
	mergedInto := int64(900)
	issues.repo.getIssues[903] = delivery.Issue{
		ID: 903, VersionID: 7, Title: "merged issue", Status: delivery.IssueStatusMerged,
		MergedIntoID: &mergedInto,
	}
	issues.repo.refs = []delivery.IssueRef{
		{ID: 1, IssueID: 900, Kind: delivery.IssueRefKindCaseRun, RefID: 3001, Role: delivery.IssueRefRoleSource},
		{ID: 2, IssueID: 900, Kind: delivery.IssueRefKindCaseVersion, RefID: 21, Role: delivery.IssueRefRoleSource},
		{ID: 3, IssueID: 900, Kind: delivery.IssueRefKindWave, RefID: waveID, Role: delivery.IssueRefRoleSource},
		{ID: 4, IssueID: 900, Kind: delivery.IssueRefKindEvidence, RefID: 8001, Role: delivery.IssueRefRoleSource, VersionSUTID: &versionSUTID},
	}
	issues.exec.snapshots[3001] = CaseRunSnapshot{
		ID:                   3001,
		VersionID:            7,
		WaveID:               waveID,
		CaseVersionID:        21,
		RequirementID:        1200,
		CaseTitle:            "checkout submit",
		VersionSUTID:         &versionSUTID,
		VersionSUTIDs:        []int64{versionSUTID},
		Status:               "failed",
		FailureClass:         "application",
		Summary:              "order confirmation missing",
		StepCount:            4,
		StepFailedCount:      1,
		AssertionCount:       6,
		AssertionFailedCount: 1,
		FailedStepSeq:        2,
		FailedAssertionSeq:   1,
		FailedExpected:       "order confirmation visible",
		FailedActual:         "order confirmation missing",
		FailedReason:         "element not found",
		Category:             "ui",
		Severity:             "major",
		EvidenceIDs:          []int64{8001},
	}
	return &issueToolsFixture{
		issueSvc:  issues.svc,
		analysis:  analysis,
		repo:      issues.repo,
		exec:      issues.exec,
		analysisR: analysisRepo,
		poster:    poster,
		tools:     IssueTools(issues.svc, analysis),
	}
}

func issueTool(t *testing.T, toolList []agenttools.Tool, name string) agenttools.Tool {
	t.Helper()
	for _, tool := range toolList {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("tool %q is not registered in %v", name, issueToolNames(toolList))
	return nil
}

func issueToolNames(toolList []agenttools.Tool) []string {
	names := make([]string, 0, len(toolList))
	for _, tool := range toolList {
		names = append(names, tool.Name())
	}
	return names
}

func requireIssueToolError(t *testing.T, err error, code string) *agenttools.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a %s tool error, got nil", code)
	}
	var toolErr *agenttools.Error
	if !errors.As(err, &toolErr) {
		t.Fatalf("error %v is not a *tools.Error", err)
	}
	if toolErr.Code != code {
		t.Fatalf("tool error code = %q, want %q (%v)", toolErr.Code, code, err)
	}
	return toolErr
}

func TestIssueToolsExposeFrozenToolNames(t *testing.T) {
	fixture := newIssueToolsFixture(t)
	if got, want := issueToolNames(fixture.tools), []string{agenttools.ToolIssueRead, agenttools.ToolIssuePropose}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("IssueTools names = %v, want %v", got, want)
	}
	for _, tool := range fixture.tools {
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
}

func TestIssueToolReadLoadsIssueRefsAndCaseRunSummary(t *testing.T) {
	fixture := newIssueToolsFixture(t)
	read := issueTool(t, fixture.tools, agenttools.ToolIssueRead)

	out, err := read.Execute(context.Background(), agenttools.Scope{VersionID: 7, IssueID: 900}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("issue.read: %v", err)
	}
	payload, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("issue.read result = %#v, want a JSON-ready map", out)
	}
	issue, ok := payload["issue"].(map[string]any)
	if !ok {
		t.Fatalf("issue.read issue = %#v", payload["issue"])
	}
	if issue["issue_id"] != "900" || issue["version_id"] != "7" || issue["status"] != "open" ||
		issue["category"] != "ui" || issue["severity"] != "major" {
		t.Fatalf("issue.read issue = %#v", issue)
	}
	refs, ok := payload["refs"].([]map[string]any)
	if !ok || len(refs) != 4 {
		t.Fatalf("issue.read refs = %#v, want 4 refs", payload["refs"])
	}
	caseRuns, ok := payload["case_runs"].([]map[string]any)
	if !ok || len(caseRuns) != 1 {
		t.Fatalf("issue.read case_runs = %#v, want one summary", payload["case_runs"])
	}
	caseRun := caseRuns[0]
	if caseRun["case_run_id"] != "3001" || caseRun["case_version_id"] != "21" ||
		caseRun["status"] != "failed" || caseRun["failure_class"] != "application" ||
		caseRun["summary"] != "order confirmation missing" || caseRun["step_failed_count"] != 1 ||
		caseRun["failed_step_seq"] != 2 {
		t.Fatalf("issue.read case_run = %#v", caseRun)
	}
	if _, exists := payload["conversation"]; exists {
		t.Fatalf("issue.read leaked a conversation section: %#v", payload)
	}

	// An explicit same-version issue is scoped to the injected version.
	out, err = read.Execute(context.Background(), agenttools.Scope{VersionID: 7, IssueID: 900}, json.RawMessage(`{"issue_id":"901"}`))
	if err != nil {
		t.Fatalf("issue.read(901): %v", err)
	}
	if out.(map[string]any)["issue"].(map[string]any)["issue_id"] != "901" {
		t.Fatalf("issue.read(901) = %#v", out)
	}
}

func TestIssueToolReadRejectsOutOfScopeReads(t *testing.T) {
	fixture := newIssueToolsFixture(t)
	read := issueTool(t, fixture.tools, agenttools.ToolIssueRead)

	_, err := read.Execute(context.Background(), agenttools.Scope{VersionID: 7, IssueID: 900}, json.RawMessage(`{"issue_id":"902"}`))
	requireIssueToolError(t, err, agenttools.CodeNotFound)

	_, err = read.Execute(context.Background(), agenttools.Scope{VersionID: 7}, json.RawMessage(`{}`))
	requireIssueToolError(t, err, agenttools.CodeInvalidArgument)

	_, err = read.Execute(context.Background(), agenttools.Scope{IssueID: 900}, json.RawMessage(`{}`))
	requireIssueToolError(t, err, agenttools.CodeInvalidArgument)

	// The model can never inject the version scope through the parameters.
	_, err = read.Execute(context.Background(), agenttools.Scope{VersionID: 7, IssueID: 900}, json.RawMessage(`{"version_id":"8"}`))
	requireIssueToolError(t, err, agenttools.CodeInvalidArgument)
}

func TestIssueToolProposeRejectsInvalidDuplicate(t *testing.T) {
	fixture := newIssueToolsFixture(t)
	propose := issueTool(t, fixture.tools, agenttools.ToolIssuePropose)
	scope := agenttools.Scope{VersionID: 7, IssueID: 900, AgentRunID: 5001}

	cases := map[string]string{
		"self":          `{"duplicate_of_issue_id":"900"}`,
		"cross-version": `{"duplicate_of_issue_id":"902"}`,
		"merged":        `{"duplicate_of_issue_id":"903"}`,
		"unknown":       `{"duplicate_of_issue_id":"404"}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := propose.Execute(context.Background(), scope, json.RawMessage(raw))
			requireIssueToolError(t, err, agenttools.CodeInvalidArgument)
		})
	}
	if len(fixture.repo.updateCalls) != 0 || len(fixture.repo.mergeCalls) != 0 {
		t.Fatalf("issue.propose called an issue status write: update=%v merge=%v", fixture.repo.updateCalls, fixture.repo.mergeCalls)
	}
}

func TestIssueToolProposeWritesAnalysisOutputAndTeamPost(t *testing.T) {
	fixture := newIssueToolsFixture(t)
	propose := issueTool(t, fixture.tools, agenttools.ToolIssuePropose)
	scope := agenttools.Scope{VersionID: 7, IssueID: 900, AgentRunID: 5001}

	out, err := propose.Execute(context.Background(), scope, json.RawMessage(`{
		"classification":"duplicate",
		"duplicate_of_issue_id":"901",
		"merge_reason":"same failing checkout assertion",
		"confidence":0.75
	}`))
	if err != nil {
		t.Fatalf("issue.propose: %v", err)
	}
	payload, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("issue.propose result = %#v, want a JSON-ready map", out)
	}
	if payload["status"] != "done" || payload["issue_id"] != "900" || payload["team_post"] != true {
		t.Fatalf("issue.propose result = %#v", payload)
	}
	if len(fixture.analysisR.runs) != 1 {
		t.Fatalf("analysis runs = %d, want 1", len(fixture.analysisR.runs))
	}
	var stored delivery.AnalysisRun
	for _, run := range fixture.analysisR.runs {
		stored = run
	}
	if stored.Kind != analysisKindTriage || stored.Status != delivery.AnalysisStatusDone {
		t.Fatalf("stored analysis run = %+v", stored)
	}
	if stored.AgentRunID == nil || *stored.AgentRunID != 5001 {
		t.Fatalf("analysis run agent_run_id = %v, want the current run 5001", stored.AgentRunID)
	}
	var output struct {
		IssueID        string  `json:"issue_id"`
		Classification string  `json:"classification"`
		DuplicateOf    string  `json:"duplicate_of_issue_id"`
		MergeReason    string  `json:"merge_reason"`
		Confidence     float64 `json:"confidence"`
	}
	if err := json.Unmarshal(stored.Output, &output); err != nil {
		t.Fatalf("decode analysis output: %v", err)
	}
	if output.IssueID != "900" || output.Classification != "duplicate" ||
		output.DuplicateOf != "901" || output.MergeReason != "same failing checkout assertion" ||
		output.Confidence != 0.75 {
		t.Fatalf("analysis output = %+v", output)
	}
	if len(fixture.poster.findings) != 1 {
		t.Fatalf("team findings = %d, want 1", len(fixture.poster.findings))
	}
	finding := fixture.poster.findings[0]
	if finding.VersionID != 7 || finding.IssueID != 900 || finding.AnalysisRunID != stored.ID ||
		!strings.Contains(finding.Content, "duplicate") || !strings.Contains(finding.Content, "901") {
		t.Fatalf("team finding = %+v", finding)
	}
	if len(fixture.repo.updateCalls) != 0 || len(fixture.repo.mergeCalls) != 0 {
		t.Fatalf("issue.propose changed the issue status: update=%v merge=%v", fixture.repo.updateCalls, fixture.repo.mergeCalls)
	}
	if stored.Status == delivery.AnalysisStatusDone && fixture.repo.getIssues[900].Status != delivery.IssueStatusOpen {
		t.Fatalf("issue.propose changed the issue status to %q", fixture.repo.getIssues[900].Status)
	}
}

func TestIssueToolProposeValidatesFields(t *testing.T) {
	fixture := newIssueToolsFixture(t)
	propose := issueTool(t, fixture.tools, agenttools.ToolIssuePropose)
	scope := agenttools.Scope{VersionID: 7, IssueID: 900}

	cases := map[string]string{
		"unknown classification": `{"classification":"blocker"}`,
		"confidence too high":    `{"confidence":1.5}`,
		"confidence negative":    `{"confidence":-0.1}`,
		"merge reason too long":  `{"classification":"flaky","merge_reason":"` + strings.Repeat("x", 501) + `"}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := propose.Execute(context.Background(), scope, json.RawMessage(raw))
			requireIssueToolError(t, err, agenttools.CodeInvalidArgument)
		})
	}
	// Missing scope target is invalid: the tool never accepts a model-supplied
	// issue target.
	_, err := propose.Execute(context.Background(), agenttools.Scope{VersionID: 7}, json.RawMessage(`{"classification":"flaky"}`))
	requireIssueToolError(t, err, agenttools.CodeInvalidArgument)
}

func TestIssueToolProposeAllowsEmptyProposal(t *testing.T) {
	fixture := newIssueToolsFixture(t)
	propose := issueTool(t, fixture.tools, agenttools.ToolIssuePropose)

	out, err := propose.Execute(context.Background(), agenttools.Scope{VersionID: 7, IssueID: 900}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("issue.propose(empty): %v", err)
	}
	payload := out.(map[string]any)
	if payload["status"] != "done" {
		t.Fatalf("issue.propose(empty) = %#v", payload)
	}
	var stored delivery.AnalysisRun
	for _, run := range fixture.analysisR.runs {
		stored = run
	}
	var output map[string]any
	if err := json.Unmarshal(stored.Output, &output); err != nil {
		t.Fatalf("decode empty proposal output: %v", err)
	}
	if output["issue_id"] != "900" {
		t.Fatalf("empty proposal output = %#v", output)
	}
}
