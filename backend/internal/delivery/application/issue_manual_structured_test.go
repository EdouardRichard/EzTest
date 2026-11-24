package deliveryapp

import (
	"context"
	"testing"
)

// TestManualIssueStructuredFill (#12): a manual issue referencing a case run
// carries the run's structured failure detail (expected/actual/reason) through
// resolveManualIssueInput -> NewManualIssue, the same assertion-derived elements
// as a candidate issue (SC-G3).
func TestManualIssueStructuredFill(t *testing.T) {
	fixture := newIssueServiceFixture()
	seedManualCaseRun(fixture)
	snapshot := fixture.exec.snapshots[11]
	snapshot.FailedExpected = "200"
	snapshot.FailedActual = "500"
	snapshot.FailedReason = "boom"
	fixture.exec.snapshots[11] = snapshot
	fixture.repo.manualResult = manualIssueCreated()

	if _, err := fixture.svc.CreateManual(context.Background(), manualIssueCreateInput(), 9); err != nil {
		t.Fatalf("CreateManual: %v", err)
	}
	if len(fixture.repo.manualCalls) != 1 {
		t.Fatalf("CreateManual calls = %d, want 1", len(fixture.repo.manualCalls))
	}
	issue := fixture.repo.manualCalls[0].Issue
	if issue.FailedExpected != "200" || issue.FailedActual != "500" || issue.FailedReason != "boom" {
		t.Fatalf("manual structured fields = %q/%q/%q, want the linked run's assertion detail",
			issue.FailedExpected, issue.FailedActual, issue.FailedReason)
	}
}
