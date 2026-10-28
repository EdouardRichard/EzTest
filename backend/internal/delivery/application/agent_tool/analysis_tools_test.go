package agent_tool

import (
	"errors"
	"strings"
	"testing"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// errTestRead is a reader transport failure used by the code-read test.
var errTestRead = errors.New("git read failed")

// caseListResult is the analysis.case_list response.
type caseListResult struct {
	BatchID   string `json:"batchId"`
	Truncated bool   `json:"truncated"`
	Cases     []struct {
		CaseRunID     string `json:"caseRunId"`
		CaseNodeID    string `json:"caseNodeId"`
		Title         string `json:"title"`
		FailureClass  string `json:"failureClass"`
		FailureReason string `json:"failureReason"`
		EvidenceCount int    `json:"evidenceCount"`
	} `json:"cases"`
}

// TestCaseListReturnsFailedApplicationCases pins the analysis.case_list contract:
// only failed(application) cases of the run's batch, with an evidence count and
// no evidence body.
func TestCaseListReturnsFailedApplicationCases(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)

	result, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.case_list", `{"batchId":"9"}`)
	if err != nil {
		t.Fatalf("analysis.case_list: %v", err)
	}
	var out caseListResult
	decodeToolResult(t, result, &out)

	if out.BatchID != "9" {
		t.Fatalf("batchId = %q, want 9", out.BatchID)
	}
	if len(out.Cases) != 2 {
		t.Fatalf("cases = %d, want the two failed(application) runs (environment excluded)", len(out.Cases))
	}
	first := out.Cases[0]
	if first.CaseRunID != "101" || first.CaseNodeID != "201" || first.Title != "checkout" ||
		first.FailureClass != domain.FailureClassApplication || first.FailureReason != "nil pointer" ||
		first.EvidenceCount != 2 {
		t.Fatalf("first case = %+v", first)
	}
	if out.Truncated {
		t.Fatal("truncated = true, want false for two cases")
	}
	if harness.cases.limit != domain.AnalysisMaxFailureCandidates+1 {
		t.Fatalf("failed-case limit = %d, want %d", harness.cases.limit, domain.AnalysisMaxFailureCandidates+1)
	}
	if harness.cases.lastID != [2]int64{7, 9} {
		t.Fatalf("failed-case scope = %v, want version 7 batch 9", harness.cases.lastID)
	}
}

// TestCaseListTruncatesAtTheCap pins the §5.6 20 case cap.
func TestCaseListTruncatesAtTheCap(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)
	cases := make([]deliveryapp.AnalysisFailedCase, 0, 25)
	for index := 0; index < 25; index++ {
		cases = append(cases, deliveryapp.AnalysisFailedCase{
			CaseRunID: int64(1000 + index), CaseNodeID: int64(2000 + index),
			FailureClass: domain.FailureClassApplication, Title: "case", EvidenceIDs: []int64{int64(7000 + index)},
		})
	}
	harness.cases.cases = cases

	result, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.case_list", `{"batchId":"9"}`)
	if err != nil {
		t.Fatalf("analysis.case_list: %v", err)
	}
	var out caseListResult
	decodeToolResult(t, result, &out)
	if len(out.Cases) != domain.AnalysisMaxFailureCandidates {
		t.Fatalf("cases = %d, want the %d cap", len(out.Cases), domain.AnalysisMaxFailureCandidates)
	}
	if !out.Truncated {
		t.Fatal("truncated = false, want true when the batch has more failed cases")
	}
}

// caseDetailResult is the analysis.case_detail response.
type caseDetailResult struct {
	CaseRunID     string `json:"caseRunId"`
	CaseNodeID    string `json:"caseNodeId"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	FailureClass  string `json:"failureClass"`
	FailureReason string `json:"failureReason"`
	Steps         []struct {
		Seq           int    `json:"seq"`
		Action        string `json:"action"`
		Expected      string `json:"expected"`
		Actual        string `json:"actual"`
		Status        string `json:"status"`
		EvidenceCount int    `json:"evidenceCount"`
	} `json:"steps"`
	Assertions []struct {
		AssertionID   string `json:"assertionId"`
		Kind          string `json:"kind"`
		Target        string `json:"target"`
		Expected      string `json:"expected"`
		Actual        string `json:"actual"`
		Verdict       string `json:"verdict"`
		FailureReason string `json:"failureReason"`
	} `json:"assertions"`
	EvidenceIDs []string `json:"evidenceIds"`
}

// TestCaseDetailReturnsTheFailedCase pins the steps/assertions/evidence metadata
// projection of analysis.case_detail.
func TestCaseDetailReturnsTheFailedCase(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)

	result, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.case_detail", `{"caseRunId":"101"}`)
	if err != nil {
		t.Fatalf("analysis.case_detail: %v", err)
	}
	var out caseDetailResult
	decodeToolResult(t, result, &out)
	if out.CaseRunID != "101" || out.CaseNodeID != "201" || out.Status != "failed" ||
		out.FailureClass != domain.FailureClassApplication || out.FailureReason != "nil pointer" {
		t.Fatalf("case detail = %+v", out)
	}
	if len(out.Steps) != 2 || out.Steps[1].Action != "submit order" || out.Steps[1].Actual != "500 error" ||
		out.Steps[1].EvidenceCount != 1 {
		t.Fatalf("steps = %+v", out.Steps)
	}
	if len(out.Assertions) != 1 || out.Assertions[0].AssertionID != "301" ||
		out.Assertions[0].Verdict != "failed" {
		t.Fatalf("assertions = %+v", out.Assertions)
	}
	if len(out.EvidenceIDs) != 2 || out.EvidenceIDs[0] != "701" || out.EvidenceIDs[1] != "702" {
		t.Fatalf("evidence ids = %v", out.EvidenceIDs)
	}
	if harness.details.lastArg != [3]int64{7, 9, 101} {
		t.Fatalf("detail scope = %v, want 7/9/101", harness.details.lastArg)
	}
}

// TestCaseDetailRejectsForeignCaseRun pins case-run ownership: a case run that
// is not a failed(application) run of the batch is refused.
func TestCaseDetailRejectsForeignCaseRun(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)
	for _, caseRunID := range []string{"103", "999"} {
		_, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.case_detail", `{"caseRunId":"`+caseRunID+`"}`)
		requireToolError(t, err, "invalid_arguments")
	}
	if harness.details.calls != 0 {
		t.Fatalf("detail reads = %d, want 0 after rejected ownership", harness.details.calls)
	}
}

// TestCaseDetailReportsMissingRow pins the not_found mapping when the whitelisted
// case run row vanished.
func TestCaseDetailReportsMissingRow(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)
	harness.details.found = false
	_, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.case_detail", `{"caseRunId":"101"}`)
	requireToolError(t, err, "not_found")
}

// evidenceMetaResult is the analysis.evidence_meta response.
type evidenceMetaResult struct {
	Evidence []struct {
		EvidenceID string `json:"evidenceId"`
		Kind       string `json:"kind"`
		SHA256     string `json:"sha256"`
		Size       int64  `json:"size"`
		Mime       string `json:"mime"`
	} `json:"evidence"`
	Warnings []string `json:"warnings"`
}

// TestEvidenceMetaFiltersForeignAndMalformedIDs pins the evidence ownership rule
// (spec §5.6): only evidence of the failed application cases is readable, foreign
// and malformed ids become warnings, duplicates are collapsed.
func TestEvidenceMetaFiltersForeignAndMalformedIDs(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)

	result, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.evidence_meta",
		`{"evidenceIds":["701","999","wat","702","701"]}`)
	if err != nil {
		t.Fatalf("analysis.evidence_meta: %v", err)
	}
	var out evidenceMetaResult
	decodeToolResult(t, result, &out)
	if len(out.Evidence) != 2 || out.Evidence[0].EvidenceID != "701" || out.Evidence[1].EvidenceID != "702" {
		t.Fatalf("evidence = %+v, want 701 and 702", out.Evidence)
	}
	if out.Evidence[0].Kind != "screenshot" || out.Evidence[0].SHA256 != "sha701" ||
		out.Evidence[0].Size != 111 || out.Evidence[0].Mime != "image/png" {
		t.Fatalf("first evidence metadata = %+v", out.Evidence[0])
	}
	if len(out.Warnings) != 2 {
		t.Fatalf("warnings = %v, want one per foreign and malformed id", out.Warnings)
	}
	if len(harness.evidence.lastIDs) != 2 ||
		harness.evidence.lastIDs[0] != 701 || harness.evidence.lastIDs[1] != 702 {
		t.Fatalf("reader ids = %v, want [701 702]", harness.evidence.lastIDs)
	}
}

// TestEvidenceMetaBoundsTheRequest pins the per-call id cap and the empty-list
// fast path.
func TestEvidenceMetaBoundsTheRequest(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)
	if _, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.evidence_meta", `{"evidenceIds":[]}`); err != nil {
		t.Fatalf("empty evidence meta must succeed: %v", err)
	}
	if harness.evidence.calls != 0 {
		t.Fatal("an empty evidence request must not read the evidence store")
	}

	ids := make([]string, 0, maxEvidenceMetaIDs+1)
	for index := 0; index < maxEvidenceMetaIDs+1; index++ {
		ids = append(ids, `"701"`)
	}
	_, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.evidence_meta",
		`{"evidenceIds":[`+strings.Join(ids, ",")+`]}`)
	requireToolError(t, err, "invalid_arguments")
}

// codeReadResult is the analysis.code_read response.
type codeReadResult struct {
	Files []struct {
		Path      string `json:"path"`
		StartLine int    `json:"startLine"`
		EndLine   int    `json:"endLine"`
		Content   string `json:"content"`
		Truncated bool   `json:"truncated"`
	} `json:"files"`
	Warnings []string `json:"warnings"`
}

// TestCodeReadEnforcesTheDocumentedCaps pins the at most 10 files / 80 lines per
// file contract of spec §9.3.
func TestCodeReadEnforcesTheDocumentedCaps(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)
	ref := `{"path":"src/cart.ts","startLine":1,"endLine":2}`
	refs := make([]string, 0, maxCodeReadRefs+1)
	for index := 0; index < maxCodeReadRefs+1; index++ {
		refs = append(refs, ref)
	}
	_, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.code_read", `{"refs":[`+strings.Join(refs, ",")+`]}`)
	requireToolError(t, err, "invalid_arguments")

	tests := []string{
		`{"refs":[{"path":"src/cart.ts","startLine":1,"endLine":81}]}`,
		`{"refs":[{"path":"src/cart.ts","startLine":0,"endLine":5}]}`,
		`{"refs":[{"path":"src/cart.ts","startLine":9,"endLine":5}]}`,
		`{"refs":[{"path":"","startLine":1,"endLine":5}]}`,
		`{"refs":[]}`,
	}
	for _, args := range tests {
		_, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.code_read", args)
		requireToolError(t, err, "invalid_arguments")
	}
	if len(harness.code.calls) != 0 {
		t.Fatal("the code reader must not be called for a rejected request")
	}
}

// TestCodeReadReturnsBoundedWindows pins the success projection and the
// version-scoped read.
func TestCodeReadReturnsBoundedWindows(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)

	result, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.code_read",
		`{"refs":[{"path":"src/cart.ts","startLine":40,"endLine":50},{"path":"src/cart.ts","startLine":40,"endLine":45}]}`)
	if err != nil {
		t.Fatalf("analysis.code_read: %v", err)
	}
	var out codeReadResult
	decodeToolResult(t, result, &out)
	if len(out.Files) != 2 {
		t.Fatalf("files = %d, want 2", len(out.Files))
	}
	if out.Files[0].Path != "src/cart.ts" || out.Files[0].StartLine != 40 || out.Files[0].EndLine != 50 ||
		!strings.Contains(out.Files[0].Content, "cart.submit()") {
		t.Fatalf("first window = %+v", out.Files[0])
	}
	if len(harness.code.calls) != 2 || harness.code.calls[0].versionID != 7 ||
		harness.code.calls[0].startLine != 40 || harness.code.calls[0].endLine != 50 {
		t.Fatalf("code reader calls = %+v", harness.code.calls)
	}
	if len(out.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", out.Warnings)
	}
}

// TestCodeReadWarnsOnMissingPath pins that a path outside the version repository
// is a warning, not a fatal failure, and that reader errors stay fatal.
func TestCodeReadWarnsOnMissingPath(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)

	result, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.code_read",
		`{"refs":[{"path":"src/cart.ts","startLine":40,"endLine":50},{"path":"src/gone.ts","startLine":1,"endLine":5}]}`)
	if err != nil {
		t.Fatalf("analysis.code_read: %v", err)
	}
	var out codeReadResult
	decodeToolResult(t, result, &out)
	if len(out.Files) != 1 || len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "src/gone.ts") {
		t.Fatalf("files/warnings = %+v/%v", out.Files, out.Warnings)
	}

	harness.code.errs = map[string]error{"src/boom.ts": errTestRead}
	_, err = callTool(t, harness.tools, analysisFailureInvocation, "analysis.code_read",
		`{"refs":[{"path":"src/boom.ts","startLine":1,"endLine":5}]}`)
	requireToolError(t, err, "internal")
}

// TestCodeReadTruncatesTheTotalPayload pins the per-call 64 KiB output bound.
func TestCodeReadTruncatesTheTotalPayload(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)
	harness.code.windows["src/big.ts"] = CodeWindow{
		Path: "src/big.ts", StartLine: 1, EndLine: 80,
		Content: strings.Repeat("a", maxCodeReadBytes+1024),
	}

	result, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.code_read",
		`{"refs":[{"path":"src/big.ts","startLine":1,"endLine":80}]}`)
	if err != nil {
		t.Fatalf("analysis.code_read: %v", err)
	}
	var out codeReadResult
	decodeToolResult(t, result, &out)
	if len(out.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(out.Files))
	}
	if len(out.Files[0].Content) != maxCodeReadBytes || !out.Files[0].Truncated {
		t.Fatalf("content bytes = %d truncated=%v, want the %d byte cap flagged",
			len(out.Files[0].Content), out.Files[0].Truncated, maxCodeReadBytes)
	}
	if len(out.Warnings) != 1 {
		t.Fatalf("warnings = %v, want the truncation warning", out.Warnings)
	}
}

// candidateProposeResult is the analysis.candidate_propose response.
type candidateProposeResult struct {
	CandidateCount int      `json:"candidateCount"`
	Warnings       []string `json:"warnings"`
}

// TestCandidateProposeInjectsTheFailureContract pins the T4 handoff: the tool
// injects failureClass=application, resolves caseNodeId from the failed-case
// whitelist and forwards evidence/code refs to the analysis service.
func TestCandidateProposeInjectsTheFailureContract(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)
	submitted := domain.FailureCandidate{
		CaseRunID: 101, CaseNodeID: 201, Title: "checkout fails", Severity: domain.SeverityMajor,
		Confidence: domain.ConfidenceHigh, FailureClass: domain.FailureClassApplication, EvidenceIDs: []int64{701},
	}
	harness.submissions.candidateResult = domain.AnalysisRun{
		ID: 11, Result: domain.EncodeFailureResult([]domain.FailureCandidate{submitted}, nil),
	}

	result, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.candidate_propose",
		`{"candidates":[{"caseRunId":"101","title":"checkout fails","description":"nil pointer","severity":"major","confidence":"high","evidenceIds":["701"]}]}`)
	if err != nil {
		t.Fatalf("analysis.candidate_propose: %v", err)
	}
	var out candidateProposeResult
	decodeToolResult(t, result, &out)
	if out.CandidateCount != 1 {
		t.Fatalf("candidateCount = %d, want 1", out.CandidateCount)
	}
	if len(out.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", out.Warnings)
	}
	if harness.submissions.candidateCalls != 1 || harness.submissions.lastAnalysisID != 11 {
		t.Fatalf("submission calls/id = %d/%d, want 1/11", harness.submissions.candidateCalls, harness.submissions.lastAnalysisID)
	}
	if len(harness.submissions.candidates) != 1 {
		t.Fatalf("submitted candidates = %d, want 1", len(harness.submissions.candidates))
	}
	candidate := harness.submissions.candidates[0]
	if candidate.FailureClass != domain.FailureClassApplication {
		t.Fatalf("failureClass = %q, want injected application", candidate.FailureClass)
	}
	if candidate.CaseNodeID != 201 {
		t.Fatalf("caseNodeId = %d, want it resolved from the failed-case whitelist", candidate.CaseNodeID)
	}
	if len(candidate.EvidenceIDs) != 1 || candidate.EvidenceIDs[0] != 701 {
		t.Fatalf("evidence ids = %v, want [701]", candidate.EvidenceIDs)
	}
}

// TestCandidateProposeDropsInvalidCandidatesWithWarnings pins the §5.6 tolerance
// rule: invalid candidates never fail the whole submission, they produce
// warnings and are skipped.
func TestCandidateProposeDropsInvalidCandidatesWithWarnings(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)
	harness.submissions.candidateResult = domain.AnalysisRun{
		ID: 11, Result: domain.EncodeFailureResult([]domain.FailureCandidate{candidateFixture()}, nil),
	}

	args := `{"candidates":[
		{"caseRunId":"101","title":"valid","severity":"major","confidence":"high","evidenceIds":["701"]},
		{"caseRunId":"103","title":"environment","severity":"major","confidence":"high","evidenceIds":["704"]},
		{"caseRunId":"102","title":"wrong class","severity":"major","confidence":"high","failureClass":"environment","evidenceIds":["703"]},
		{"caseRunId":"101","title":"no proof","severity":"major","confidence":"high"},
		{"caseRunId":"101","title":"bad severity","severity":"critical","confidence":"high","evidenceIds":["701"]},
		{"caseRunId":"101","caseNodeId":"999","title":"bad node","severity":"major","confidence":"high","evidenceIds":["701"]},
		{"caseRunId":"101","title":"duplicate","severity":"major","confidence":"high","evidenceIds":["701"]}
	]}`
	result, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.candidate_propose", args)
	if err != nil {
		t.Fatalf("analysis.candidate_propose: %v", err)
	}
	var out candidateProposeResult
	decodeToolResult(t, result, &out)
	if out.CandidateCount != 1 {
		t.Fatalf("candidateCount = %d, want the single valid candidate", out.CandidateCount)
	}
	if len(out.Warnings) != 6 {
		t.Fatalf("warnings = %v, want one per dropped candidate", out.Warnings)
	}
	if len(harness.submissions.candidates) != 1 || harness.submissions.candidates[0].Title != "valid" {
		t.Fatalf("submitted = %+v, want only the valid candidate", harness.submissions.candidates)
	}
}

// TestCandidateProposeSurfacesServiceWarnings pins that the analysis service's
// own validation warnings reach the model.
func TestCandidateProposeSurfacesServiceWarnings(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)
	harness.submissions.warnings = []string{"candidate 1: duplicate candidate for case_run 101"}

	result, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.candidate_propose",
		`{"candidates":[{"caseRunId":"101","title":"valid","severity":"major","confidence":"high","evidenceIds":["701"]}]}`)
	if err != nil {
		t.Fatalf("analysis.candidate_propose: %v", err)
	}
	var out candidateProposeResult
	decodeToolResult(t, result, &out)
	if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "duplicate") {
		t.Fatalf("warnings = %v, want the service warning", out.Warnings)
	}
}

// TestCandidateProposeRejectsOverCapSubmission pins the 20 candidate cap.
func TestCandidateProposeRejectsOverCapSubmission(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)
	candidates := make([]string, 0, domain.AnalysisMaxFailureCandidates+1)
	for index := 0; index < domain.AnalysisMaxFailureCandidates+1; index++ {
		candidates = append(candidates, `{"caseRunId":"101","title":"t","severity":"major","confidence":"high","evidenceIds":["701"]}`)
	}
	_, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.candidate_propose",
		`{"candidates":[`+strings.Join(candidates, ",")+`]}`)
	requireToolError(t, err, "invalid_arguments")
}

// TestCandidateProposeMapsAnalysisErrors pins the stable reason mapping.
func TestCandidateProposeMapsAnalysisErrors(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)
	harness.submissions.candidateErr = domain.NewAnalysisNotApplicable("no application failure")
	_, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.candidate_propose",
		`{"candidates":[{"caseRunId":"101","title":"valid","severity":"major","confidence":"high","evidenceIds":["701"]}]}`)
	requireToolError(t, err, "not_applicable")
}

// TestAnalysisFinishAcknowledgesWithoutWriting pins the optional finish tool: it
// ends the agent turn but never writes issues or bypasses T4 materialization.
func TestAnalysisFinishAcknowledgesWithoutWriting(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindFailure)
	result, err := callTool(t, harness.tools, analysisFailureInvocation, "analysis.finish", `{"summary":"done: one candidate"}`)
	if err != nil {
		t.Fatalf("analysis.finish: %v", err)
	}
	var out struct {
		Finished bool `json:"finished"`
	}
	decodeToolResult(t, result, &out)
	if !out.Finished {
		t.Fatal("finished = false, want the explicit acknowledgement")
	}
	if harness.submissions.candidateCalls != 0 || harness.submissions.draftCalls != 0 {
		t.Fatal("analysis.finish must not write issue or candidate rows")
	}

	long := strings.Repeat("x", maxFinishSummaryRunes+1)
	_, err = callTool(t, harness.tools, analysisFailureInvocation, "analysis.finish", `{"summary":"`+long+`"}`)
	requireToolError(t, err, "invalid_arguments")
}
