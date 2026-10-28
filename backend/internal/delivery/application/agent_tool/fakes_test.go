package agent_tool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// analysisFailureInvocation is the P1 tool invocation of a failure-analysis run.
var analysisFailureInvocation = agentapp.Invocation{
	SessionID:     100,
	RunID:         200,
	ProfileKey:    "failure-analysis-agent",
	VersionID:     7,
	BatchID:       9,
	AnalysisRunID: 11,
}

// analysisTriageInvocation is the P1 tool invocation of an issue-triage run.
var analysisTriageInvocation = agentapp.Invocation{
	SessionID:     101,
	RunID:         201,
	ProfileKey:    "issue-triage-agent",
	VersionID:     7,
	BatchID:       9,
	AnalysisRunID: 12,
}

// candidateFixture is a valid failure candidate of the fixture batch.
func candidateFixture() domain.FailureCandidate {
	return domain.FailureCandidate{
		CaseRunID:    101,
		CaseNodeID:   201,
		Title:        "checkout fails",
		Description:  "submitting the order returns a nil pointer",
		Severity:     domain.SeverityMajor,
		Confidence:   domain.ConfidenceHigh,
		FailureClass: domain.FailureClassApplication,
		EvidenceIDs:  []int64{701, 702},
		CodeRefs:     []domain.CodeRef{{Path: "src/cart.ts", Line: 42, Reason: "nil deref"}},
	}
}

// failedCasesFixture is the failed-case read of the fixture batch: two
// failed(application) runs and one failed(environment) run T4 never analyzes.
func failedCasesFixture() []deliveryapp.AnalysisFailedCase {
	return []deliveryapp.AnalysisFailedCase{
		{
			CaseRunID: 101, CaseNodeID: 201, FailureClass: domain.FailureClassApplication,
			Title: "checkout", FailureReason: "nil pointer", EvidenceIDs: []int64{701, 702},
		},
		{
			CaseRunID: 102, CaseNodeID: 202, FailureClass: domain.FailureClassApplication,
			Title: "login", FailureReason: "assertion mismatch", EvidenceIDs: []int64{703},
		},
		{
			CaseRunID: 103, CaseNodeID: 203, FailureClass: domain.FailureClassEnvironment,
			Title: "search", FailureReason: "dns failure", EvidenceIDs: []int64{704},
		},
	}
}

// failureRunFixture is the persisted failure-analysis run of the fixture batch.
func failureRunFixture() domain.AnalysisRun {
	return domain.AnalysisRun{
		ID: 11, VersionID: 7, BatchID: 9,
		Kind: domain.AnalysisKindFailure, Status: domain.AnalysisRunning,
		PromptVersion: domain.AnalysisPromptVersionFailure,
		Input:         domain.EncodeFailureInput(7, 9, []int64{101, 102}),
		Result:        domain.EncodeFailureResult([]domain.FailureCandidate{candidateFixture()}, nil),
	}
}

// triageRunFixture is the persisted issue-triage run of the fixture batch: its
// input carries the failure candidates and the open issue summaries.
func triageRunFixture() domain.AnalysisRun {
	return domain.AnalysisRun{
		ID: 12, VersionID: 7, BatchID: 9,
		Kind: domain.AnalysisKindIssueTriage, Status: domain.AnalysisRunning,
		PromptVersion: domain.AnalysisPromptVersionTriage,
		Input: domain.EncodeTriageInput(7, 9,
			[]domain.FailureCandidate{candidateFixture()},
			[]domain.OpenIssue{{IssueID: 55, Title: "old defect", Severity: domain.SeverityMajor}}),
	}
}

// fakeRuns is an in-memory analysis run reader.
type fakeRuns struct {
	run    domain.AnalysisRun
	err    error
	calls  int
	lastID int64
}

func (f *fakeRuns) GetByID(_ context.Context, analysisID int64) (domain.AnalysisRun, error) {
	f.calls++
	f.lastID = analysisID
	if f.err != nil {
		return domain.AnalysisRun{}, f.err
	}
	return f.run, nil
}

// fakeCases is the T4 execution reader double.
type fakeCases struct {
	cases  []deliveryapp.AnalysisFailedCase
	err    error
	calls  int
	limit  int
	lastID [2]int64
}

func (f *fakeCases) ListFailedCases(_ context.Context, versionID, batchID int64, limit int) ([]deliveryapp.AnalysisFailedCase, error) {
	f.calls++
	f.limit = limit
	f.lastID = [2]int64{versionID, batchID}
	if f.err != nil {
		return nil, f.err
	}
	return f.cases, nil
}

// fakeSubmission records the structured submissions of the analysis service.
type fakeSubmission struct {
	candidates     []domain.FailureCandidate
	drafts         []domain.IssueDraft
	candidateCalls int
	draftCalls     int
	lastAnalysisID int64
	candidateErr   error
	draftErr       error
	// candidateResult/draftResult are the runs returned after a submission.
	// Tests that assert the accepted count set their Result document.
	candidateResult domain.AnalysisRun
	draftResult     domain.AnalysisRun
	warnings        []string
}

func (f *fakeSubmission) ProposeFailureCandidates(_ context.Context, analysisID int64, candidates []domain.FailureCandidate) (domain.AnalysisRun, []string, error) {
	f.candidateCalls++
	f.lastAnalysisID = analysisID
	f.candidates = append(f.candidates, candidates...)
	if f.candidateErr != nil {
		return domain.AnalysisRun{}, nil, f.candidateErr
	}
	return f.candidateResult, f.warnings, nil
}

func (f *fakeSubmission) ProposeIssueDrafts(_ context.Context, analysisID int64, drafts []domain.IssueDraft) (domain.AnalysisRun, []string, error) {
	f.draftCalls++
	f.lastAnalysisID = analysisID
	f.drafts = append(f.drafts, drafts...)
	if f.draftErr != nil {
		return domain.AnalysisRun{}, nil, f.draftErr
	}
	return f.draftResult, f.warnings, nil
}

// fakeDetails is the case detail reader double.
type fakeDetails struct {
	detail  CaseDetail
	found   bool
	err     error
	calls   int
	lastArg [3]int64
}

func (f *fakeDetails) CaseDetail(_ context.Context, versionID, batchID, caseRunID int64) (CaseDetail, bool, error) {
	f.calls++
	f.lastArg = [3]int64{versionID, batchID, caseRunID}
	if f.err != nil {
		return CaseDetail{}, false, f.err
	}
	return f.detail, f.found, nil
}

// fakeEvidence is the evidence metadata reader double.
type fakeEvidence struct {
	metas   []EvidenceMeta
	err     error
	calls   int
	lastID  [2]int64
	lastIDs []int64
}

func (f *fakeEvidence) EvidenceMeta(_ context.Context, versionID, batchID int64, evidenceIDs []int64) ([]EvidenceMeta, error) {
	f.calls++
	f.lastID = [2]int64{versionID, batchID}
	f.lastIDs = append([]int64(nil), evidenceIDs...)
	if f.err != nil {
		return nil, f.err
	}
	// The real adapter only returns rows for the requested ids; mirror that so
	// the tools cannot accidentally widen the ownership-filtered set.
	requested := make(map[int64]struct{}, len(evidenceIDs))
	for _, id := range evidenceIDs {
		requested[id] = struct{}{}
	}
	metas := make([]EvidenceMeta, 0, len(evidenceIDs))
	for _, meta := range f.metas {
		if _, ok := requested[meta.EvidenceID]; ok {
			metas = append(metas, meta)
		}
	}
	return metas, nil
}

// codeCall is one normalized analysis.code_read request.
type codeCall struct {
	versionID int64
	path      string
	startLine int
	endLine   int
}

// fakeCode is the commit-pinned code reader double.
type fakeCode struct {
	windows map[string]CodeWindow
	errs    map[string]error
	calls   []codeCall
}

func (f *fakeCode) ReadCode(_ context.Context, versionID int64, path string, startLine, endLine int) (CodeWindow, bool, error) {
	f.calls = append(f.calls, codeCall{versionID: versionID, path: path, startLine: startLine, endLine: endLine})
	if err := f.errs[path]; err != nil {
		return CodeWindow{}, false, err
	}
	window, ok := f.windows[path]
	if !ok {
		return CodeWindow{}, false, nil
	}
	return window, true, nil
}

// toolHarness is one wired tool set plus its doubles.
type toolHarness struct {
	runs        *fakeRuns
	cases       *fakeCases
	submissions *fakeSubmission
	details     *fakeDetails
	evidence    *fakeEvidence
	code        *fakeCode
	tools       []agentdomain.Tool
}

// detailFixture is the fixture case detail of case run 101.
func detailFixture() CaseDetail {
	return CaseDetail{
		CaseRunID: 101, CaseNodeID: 201, Title: "checkout",
		Status: "failed", FailureClass: domain.FailureClassApplication, FailureReason: "nil pointer",
		Steps: []CaseDetailStep{
			{Seq: 1, Action: "open cart", Expected: "cart shown", Actual: "cart shown", Status: "passed", EvidenceCount: 1},
			{Seq: 2, Action: "submit order", Expected: "order created", Actual: "500 error", Status: "failed", EvidenceCount: 1},
		},
		Assertions: []CaseDetailAssertion{
			{AssertionID: 301, Kind: "ui", Target: "order", Expected: "order created", Actual: "internal error", Verdict: "failed", FailureReason: "mismatch"},
		},
		EvidenceIDs: []int64{701, 702},
	}
}

// newToolHarness wires NewTools to in-memory fakes. kind selects the persisted
// run fixture (failure or issue_triage).
func newToolHarness(kind string) *toolHarness {
	run := failureRunFixture()
	if kind == domain.AnalysisKindIssueTriage {
		run = triageRunFixture()
	}
	harness := &toolHarness{
		runs:        &fakeRuns{run: run},
		cases:       &fakeCases{cases: failedCasesFixture()},
		submissions: &fakeSubmission{},
		details:     &fakeDetails{detail: detailFixture(), found: true},
		evidence: &fakeEvidence{metas: []EvidenceMeta{
			{EvidenceID: 701, Kind: "screenshot", SHA256: "sha701", SizeBytes: 111, Mime: "image/png"},
			{EvidenceID: 702, Kind: "http", SHA256: "sha702", SizeBytes: 222, Mime: "application/json"},
			{EvidenceID: 703, Kind: "screenshot", SHA256: "sha703", SizeBytes: 333, Mime: "image/png"},
			{EvidenceID: 704, Kind: "http", SHA256: "sha704", SizeBytes: 444, Mime: "application/json"},
		}},
		code: &fakeCode{windows: map[string]CodeWindow{
			"src/cart.ts": {Path: "src/cart.ts", StartLine: 40, EndLine: 50, Content: "func submit() {\n  cart.submit()\n}"},
		}},
	}
	harness.tools = NewTools(ToolDeps{
		Runs:        harness.runs,
		Cases:       harness.cases,
		Submissions: harness.submissions,
		Details:     harness.details,
		Evidence:    harness.evidence,
		Code:        harness.code,
	})
	return harness
}

// callTool executes one registered tool under invocation.
func callTool(t *testing.T, tools []agentdomain.Tool, invocation agentapp.Invocation, name, args string) (agentdomain.ToolResult, error) {
	t.Helper()
	for _, tool := range tools {
		if tool.Name() != name {
			continue
		}
		ctx := agentapp.WithToolInvocation(context.Background(), invocation)
		return tool.Execute(ctx, json.RawMessage(args))
	}
	t.Fatalf("tool %q is not registered", name)
	return agentdomain.ToolResult{}, nil
}

// requireToolError asserts err is a *domain.ToolError with code and returns it.
func requireToolError(t *testing.T, err error, code string) *agentdomain.ToolError {
	t.Helper()
	var toolErr *agentdomain.ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("error = %v, want *agentdomain.ToolError", err)
	}
	if toolErr.Code != code {
		t.Fatalf("tool error code = %q, want %q (%s)", toolErr.Code, code, toolErr.Message)
	}
	return toolErr
}

// decodeToolResult decodes one successful tool result into out.
func decodeToolResult(t *testing.T, result agentdomain.ToolResult, out any) {
	t.Helper()
	if result.IsError {
		t.Fatalf("tool result is an error: %s", result.Content)
	}
	if err := json.Unmarshal([]byte(result.Content), out); err != nil {
		t.Fatalf("decode tool result %q: %v", result.Content, err)
	}
}
