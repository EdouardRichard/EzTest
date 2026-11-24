package delivery

import "testing"

// TestIssueCandidateStructuredFill pins SC-G3: the deterministic candidate
// carries the structured failure elements from the failed assertion detail
// (expected/actual/reason) alongside the narrative Summary.
func TestIssueCandidateStructuredFill(t *testing.T) {
	issue, _, err := NewIssueCandidate(CandidateInput{
		VersionID:      7,
		CaseRunID:      5001,
		CaseVersionID:  3001,
		CaseTitle:      "导出订单",
		StepSeq:        1,
		AssertionSeq:   1,
		FailureClass:   "application",
		Category:       "api",
		Severity:       "major",
		FailedExpected: "200",
		FailedActual:   "500",
		FailedReason:   "boom",
	})
	if err != nil {
		t.Fatalf("NewIssueCandidate: %v", err)
	}
	if issue.FailedExpected != "200" || issue.FailedActual != "500" || issue.FailedReason != "boom" {
		t.Fatalf("structured fields = %q/%q/%q, want the assertion detail", issue.FailedExpected, issue.FailedActual, issue.FailedReason)
	}
	if issue.Summary == "" {
		t.Fatal("Summary narrative must stay populated")
	}
}
