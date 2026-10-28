package agent_tool

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// triageCandidateListResult is the triage.candidate_list response.
type triageCandidateListResult struct {
	VersionID  string `json:"versionId"`
	BatchID    string `json:"batchId"`
	Candidates []struct {
		CaseRunID    string   `json:"caseRunId"`
		CaseNodeID   string   `json:"caseNodeId"`
		Title        string   `json:"title"`
		Severity     string   `json:"severity"`
		Confidence   string   `json:"confidence"`
		FailureClass string   `json:"failureClass"`
		EvidenceIDs  []string `json:"evidenceIds"`
	} `json:"candidates"`
}

// TestTriageCandidateListReturnsRunInputCandidates pins that the triage agent
// reads the failure candidates from its own run input, never from an argument.
func TestTriageCandidateListReturnsRunInputCandidates(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindIssueTriage)

	result, err := callTool(t, harness.tools, analysisTriageInvocation, "triage.candidate_list", `{}`)
	if err != nil {
		t.Fatalf("triage.candidate_list: %v", err)
	}
	var out triageCandidateListResult
	decodeToolResult(t, result, &out)
	if out.VersionID != "7" || out.BatchID != "9" {
		t.Fatalf("scope = %s/%s, want 7/9", out.VersionID, out.BatchID)
	}
	if len(out.Candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(out.Candidates))
	}
	candidate := out.Candidates[0]
	if candidate.CaseRunID != "101" || candidate.CaseNodeID != "201" ||
		candidate.Title != "checkout fails" || candidate.Severity != domain.SeverityMajor ||
		candidate.Confidence != domain.ConfidenceHigh || candidate.FailureClass != domain.FailureClassApplication {
		t.Fatalf("candidate = %+v", candidate)
	}
	if len(candidate.EvidenceIDs) != 2 || candidate.EvidenceIDs[0] != "701" {
		t.Fatalf("evidence ids = %v", candidate.EvidenceIDs)
	}
}

// triageIssueListResult is the triage.issue_list response.
type triageIssueListResult struct {
	Issues []struct {
		IssueID  string `json:"issueId"`
		Title    string `json:"title"`
		Severity string `json:"severity"`
	} `json:"issues"`
}

// TestTriageIssueListReturnsOpenIssueSummaries pins the §9.4 open issue
// projection (id/title/severity; the dedupe key stays invisible).
func TestTriageIssueListReturnsOpenIssueSummaries(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindIssueTriage)

	result, err := callTool(t, harness.tools, analysisTriageInvocation, "triage.issue_list", `{}`)
	if err != nil {
		t.Fatalf("triage.issue_list: %v", err)
	}
	var out triageIssueListResult
	decodeToolResult(t, result, &out)
	if len(out.Issues) != 1 || out.Issues[0].IssueID != "55" ||
		out.Issues[0].Title != "old defect" || out.Issues[0].Severity != domain.SeverityMajor {
		t.Fatalf("issues = %+v", out.Issues)
	}
	if strings.Contains(result.Content, "dedupeKey") {
		t.Fatal("triage.issue_list must not expose dedupe keys (spec §9.4)")
	}
}

// triageContextResult is the triage.context_read response.
type triageContextResult struct {
	Cases []struct {
		CaseRunID      string   `json:"caseRunId"`
		CaseNodeID     string   `json:"caseNodeId"`
		Title          string   `json:"title"`
		FailureClass   string   `json:"failureClass"`
		FailureReason  string   `json:"failureReason"`
		StepCount      int      `json:"stepCount"`
		AssertionCount int      `json:"assertionCount"`
		EvidenceIDs    []string `json:"evidenceIds"`
	} `json:"cases"`
	Evidence []struct {
		EvidenceID string `json:"evidenceId"`
		Kind       string `json:"kind"`
		SHA256     string `json:"sha256"`
		Size       int64  `json:"size"`
		Mime       string `json:"mime"`
	} `json:"evidence"`
	CodeRefs []struct {
		CaseRunID  string `json:"caseRunId"`
		CaseNodeID string `json:"caseNodeId"`
		Path       string `json:"path"`
		Line       int    `json:"line"`
		Reason     string `json:"reason"`
	} `json:"codeRefs"`
	Warnings []string `json:"warnings"`
}

// TestTriageContextReadSummarizes pins the case/evidence/code-location summary of
// triage.context_read.
func TestTriageContextReadSummarizes(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindIssueTriage)

	result, err := callTool(t, harness.tools, analysisTriageInvocation, "triage.context_read",
		`{"caseRunIds":["101"],"evidenceIds":["701"]}`)
	if err != nil {
		t.Fatalf("triage.context_read: %v", err)
	}
	var out triageContextResult
	decodeToolResult(t, result, &out)
	if len(out.Cases) != 1 {
		t.Fatalf("cases = %d, want 1", len(out.Cases))
	}
	summary := out.Cases[0]
	if summary.CaseRunID != "101" || summary.CaseNodeID != "201" || summary.Title != "checkout" ||
		summary.FailureClass != domain.FailureClassApplication || summary.FailureReason != "nil pointer" ||
		summary.StepCount != 2 || summary.AssertionCount != 1 {
		t.Fatalf("case summary = %+v", summary)
	}
	if len(summary.EvidenceIDs) != 2 || summary.EvidenceIDs[0] != "701" {
		t.Fatalf("case evidence ids = %v", summary.EvidenceIDs)
	}
	if len(out.Evidence) != 1 || out.Evidence[0].EvidenceID != "701" || out.Evidence[0].Kind != "screenshot" {
		t.Fatalf("evidence = %+v", out.Evidence)
	}
	if len(out.CodeRefs) != 1 || out.CodeRefs[0].Path != "src/cart.ts" ||
		out.CodeRefs[0].Line != 42 || out.CodeRefs[0].CaseRunID != "101" {
		t.Fatalf("code refs = %+v", out.CodeRefs)
	}
	if harness.details.lastArg != [3]int64{7, 9, 101} {
		t.Fatalf("detail scope = %v", harness.details.lastArg)
	}
	if len(harness.evidence.lastIDs) != 1 || harness.evidence.lastIDs[0] != 701 {
		t.Fatalf("evidence ids read = %v, want [701]", harness.evidence.lastIDs)
	}
}

// TestTriageContextReadRejectsForeignReferences pins ownership: case runs and
// evidence outside the batch's failed application set are refused.
func TestTriageContextReadRejectsForeignReferences(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindIssueTriage)
	for _, args := range []string{
		`{"caseRunIds":["103"]}`,
		`{"caseRunIds":["999"]}`,
		`{"evidenceIds":["999"]}`,
	} {
		_, err := callTool(t, harness.tools, analysisTriageInvocation, "triage.context_read", args)
		requireToolError(t, err, "invalid_arguments")
	}
	caseRunIDs := make([]string, 0, maxContextCaseRunIDs+1)
	for index := 0; index < maxContextCaseRunIDs+1; index++ {
		caseRunIDs = append(caseRunIDs, `"101"`)
	}
	_, err := callTool(t, harness.tools, analysisTriageInvocation, "triage.context_read",
		`{"caseRunIds":[`+strings.Join(caseRunIDs, ",")+`]}`)
	requireToolError(t, err, "invalid_arguments")

	evidenceIDs := make([]string, 0, maxContextEvidenceIDs+1)
	for index := 0; index < maxContextEvidenceIDs+1; index++ {
		evidenceIDs = append(evidenceIDs, `"701"`)
	}
	_, err = callTool(t, harness.tools, analysisTriageInvocation, "triage.context_read",
		`{"evidenceIds":[`+strings.Join(evidenceIDs, ",")+`]}`)
	requireToolError(t, err, "invalid_arguments")
}

// issueDraftResult is the triage.issue_draft response.
type issueDraftResult struct {
	DraftCount int      `json:"draftCount"`
	Warnings   []string `json:"warnings"`
}

// TestTriageIssueDraftInjectsTheDefectContract pins the T4 handoff: the tool
// injects category=defect and failureClass=application, resolves caseNodeId and
// derives the §5.4 dedupe key when the model did not supply one.
func TestTriageIssueDraftInjectsTheDefectContract(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindIssueTriage)
	emptyTriageResult, _ := domain.EncodeTriageResult(nil, nil)
	harness.submissions.draftResult = domain.AnalysisRun{ID: 12, Result: emptyTriageResult}

	result, err := callTool(t, harness.tools, analysisTriageInvocation, "triage.issue_draft",
		`{"issues":[{"title":"checkout fails","description":"nil pointer","reproSteps":"open cart then submit","expected":"order created","actual":"500 error","severity":"major","confidence":"high","caseRunId":"101","evidenceIds":["701"]}]}`)
	if err != nil {
		t.Fatalf("triage.issue_draft: %v", err)
	}
	var out issueDraftResult
	decodeToolResult(t, result, &out)
	if out.DraftCount != 0 {
		t.Fatalf("draftCount = %d, want the scripted service result (0)", out.DraftCount)
	}
	if len(harness.submissions.drafts) != 1 {
		t.Fatalf("submitted drafts = %d, want 1", len(harness.submissions.drafts))
	}
	draft := harness.submissions.drafts[0]
	if draft.Category != domain.CategoryDefect {
		t.Fatalf("category = %q, want injected defect", draft.Category)
	}
	if draft.FailureClass != domain.FailureClassApplication {
		t.Fatalf("failureClass = %q, want injected application", draft.FailureClass)
	}
	if draft.CaseNodeID == nil || *draft.CaseNodeID != 201 {
		t.Fatalf("caseNodeId = %v, want it resolved from the failed-case whitelist", draft.CaseNodeID)
	}
	if draft.CaseRunID == nil || *draft.CaseRunID != 101 {
		t.Fatalf("caseRunId = %v", draft.CaseRunID)
	}
	if !validDedupeKey(draft.DedupeKey) {
		t.Fatalf("dedupeKey = %q, want a derived lowercase sha256", draft.DedupeKey)
	}
	if want := domain.FailureDedupeKey(7, 201, domain.FailureClassApplication, "checkout fails"); draft.DedupeKey != want {
		t.Fatalf("dedupeKey = %q, want the deterministic failure key %q", draft.DedupeKey, want)
	}
}

// TestTriageIssueDraftKeepsAValidDedupeKey pins that an explicitly supplied
// well-formed key is trusted.
func TestTriageIssueDraftKeepsAValidDedupeKey(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindIssueTriage)
	key := strings.Repeat("a", 64)

	if _, err := callTool(t, harness.tools, analysisTriageInvocation, "triage.issue_draft",
		`{"issues":[{"dedupeKey":"`+key+`","title":"t","severity":"minor","confidence":"low","caseRunId":"101","evidenceIds":["701"]}]}`); err != nil {
		t.Fatalf("triage.issue_draft: %v", err)
	}
	if len(harness.submissions.drafts) != 1 || harness.submissions.drafts[0].DedupeKey != key {
		t.Fatalf("draft = %+v, want the supplied dedupe key", harness.submissions.drafts)
	}
}

// TestTriageIssueDraftSanitizesWithWarnings pins the §9.4/§5.4 tolerance rules:
// malformed or foreign fields are stripped or the draft is skipped with a
// warning, and a draft without evidence/code refs is still submitted with a
// warning.
func TestTriageIssueDraftSanitizesWithWarnings(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindIssueTriage)
	emptyTriageResult, _ := domain.EncodeTriageResult(nil, nil)
	harness.submissions.draftResult = domain.AnalysisRun{ID: 12, Result: emptyTriageResult}

	args := `{"issues":[
		{"title":"valid","severity":"major","confidence":"high","caseRunId":"101","evidenceIds":["701"]},
		{"title":"no proof","severity":"minor","confidence":"low","caseRunId":"101"},
		{"title":"foreign evidence","severity":"minor","confidence":"low","caseRunId":"101","evidenceIds":["999"]},
		{"title":"bad ref","severity":"minor","confidence":"low","caseRunId":"101","codeRefs":[{"path":"src/cart.ts","line":0,"reason":"x"}]},
		{"title":"bad merge","severity":"minor","confidence":"low","caseRunId":"101","mergeIntoIssueId":"999"},
		{"title":"open merge","severity":"minor","confidence":"low","caseRunId":"101","mergeIntoIssueId":"55"},
		{"title":"node mismatch","severity":"minor","confidence":"low","caseRunId":"101","caseNodeId":"999"},
		{"title":"no identity","severity":"minor","confidence":"low"},
		{"severity":"minor","confidence":"low","caseRunId":"101","caseNodeId":"201"},
		{"title":"bad confidence","severity":"minor","confidence":"certain","caseRunId":"101","caseNodeId":"201"}
	]}`
	result, err := callTool(t, harness.tools, analysisTriageInvocation, "triage.issue_draft", args)
	if err != nil {
		t.Fatalf("triage.issue_draft: %v", err)
	}
	var out issueDraftResult
	decodeToolResult(t, result, &out)
	if len(harness.submissions.drafts) != 6 {
		t.Fatalf("submitted drafts = %d, want the 6 valid ones", len(harness.submissions.drafts))
	}
	if len(out.Warnings) != 10 {
		t.Fatalf("warnings = %v, want one per stripped field or skipped draft", out.Warnings)
	}
	submitted := harness.submissions.drafts
	// Draft 2 ("no proof") is submitted without evidence/code and warned.
	if len(submitted[1].EvidenceIDs) != 0 || len(submitted[1].CodeRefs) != 0 {
		t.Fatalf("no-proof draft = %+v, want it submitted without evidence", submitted[1])
	}
	// Draft 3 lost its foreign evidence and is still submitted.
	if len(submitted[2].EvidenceIDs) != 0 {
		t.Fatalf("foreign evidence ids = %v, want stripped", submitted[2].EvidenceIDs)
	}
	// Draft 4 lost its malformed code ref.
	if len(submitted[3].CodeRefs) != 0 {
		t.Fatalf("malformed code refs = %v, want stripped", submitted[3].CodeRefs)
	}
	// Draft 5 lost the non-open merge target.
	if submitted[4].MergeIntoIssueID != nil {
		t.Fatalf("merge target = %v, want stripped for a non-open issue", *submitted[4].MergeIntoIssueID)
	}
	// Draft 6 keeps the open merge target.
	if submitted[5].MergeIntoIssueID == nil || *submitted[5].MergeIntoIssueID != 55 {
		t.Fatalf("merge target = %v, want the open issue 55", submitted[5].MergeIntoIssueID)
	}
}

// TestTriageIssueDraftRejectsOverCapSubmission pins the ≤20 draft cap.
func TestTriageIssueDraftRejectsOverCapSubmission(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindIssueTriage)
	issues := make([]string, 0, domain.AnalysisMaxIssueDrafts+1)
	for index := 0; index < domain.AnalysisMaxIssueDrafts+1; index++ {
		issues = append(issues, `{"title":"t","severity":"major","confidence":"high","caseRunId":"101","caseNodeId":"201"}`)
	}
	_, err := callTool(t, harness.tools, analysisTriageInvocation, "triage.issue_draft",
		`{"issues":[`+strings.Join(issues, ",")+`]}`)
	requireToolError(t, err, "invalid_arguments")
}

// TestTriageIssueDraftMapsServiceErrors pins the stable reason mapping.
func TestTriageIssueDraftMapsServiceErrors(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindIssueTriage)
	harness.submissions.draftErr = domain.NewAnalysisNotApplicable("no candidates")
	_, err := callTool(t, harness.tools, analysisTriageInvocation, "triage.issue_draft",
		`{"issues":[{"title":"t","severity":"major","confidence":"high","caseRunId":"101","caseNodeId":"201"}]}`)
	requireToolError(t, err, "not_applicable")
}

// TestTriageFinishAcknowledgesWithoutWriting pins the optional finish tool.
func TestTriageFinishAcknowledgesWithoutWriting(t *testing.T) {
	harness := newToolHarness(domain.AnalysisKindIssueTriage)
	result, err := callTool(t, harness.tools, analysisTriageInvocation, "triage.finish", `{"summary":"one issue drafted"}`)
	if err != nil {
		t.Fatalf("triage.finish: %v", err)
	}
	var out struct {
		Finished bool `json:"finished"`
	}
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatalf("decode finish result: %v", err)
	}
	if !out.Finished {
		t.Fatal("finished = false, want the explicit acknowledgement")
	}
	if harness.submissions.candidateCalls != 0 || harness.submissions.draftCalls != 0 {
		t.Fatal("triage.finish must not write issue rows")
	}
}

// validDedupeKey reports whether key is a lowercase 64-character sha256 shape.
func validDedupeKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	for _, char := range key {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
