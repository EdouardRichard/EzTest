package adapters

// Task 122 (Release-T10): the delivery report must honor the platform issue
// triage classification=expected_failure (release PO FR-113-03: run status
// failed/application, but step 4 was never executed because the agent token
// budget really ran out; assertion_failed=0/step_failed=0, triage
// expected_failure). Such a run is not a product defect: the report projection
// must not present it as application, while the raw P3 execution fact stays
// available. A real application failure (no expected_failure triage) must stay
// exactly as before.

import (
	"context"
	"testing"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// withExpectedFailureTriage marks the fixture issue as a finished
// expected_failure triage.
func withExpectedFailureTriage(deps Deps, issueID int64) Deps {
	deps.Triage = &fakeTriageRuns{runs: map[int64]delivery.AnalysisRun{
		issueID: finishedTriageRun(issueID, delivery.TriageClassificationExpectedFailure),
	}}
	return deps
}

// TestExpectedFailureNotProductDefect pins the classification projection: the
// raw P3 failure_class stays application (the execution fact is preserved), but
// the report-visible classification of the run is expected_failure because the
// platform triage classified the issue as expected_failure. The issue keeps its
// stored open/minor row but is projected as expected_failure, and coverage is
// not faked: the run is still not a pass.
func TestExpectedFailureNotProductDefect(t *testing.T) {
	deps := withExpectedFailureTriage(newReportFixtureDeps(t), 8001)
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}

	var failed *delivery.CaseRunSnapshot
	for index := range snapshot.CaseRuns {
		if snapshot.CaseRuns[index].ID == 5001 {
			failed = &snapshot.CaseRuns[index]
		}
	}
	if failed == nil {
		t.Fatalf("report snapshot is missing case run 5001: %+v", snapshot.CaseRuns)
	}
	if failed.FailureClass != "application" {
		t.Fatalf("raw failure class = %q, want the preserved execution fact application", failed.FailureClass)
	}
	if failed.TriageClassification != delivery.TriageClassificationExpectedFailure {
		t.Fatalf("triage classification = %q, want expected_failure", failed.TriageClassification)
	}
	if got := failed.EffectiveFailureClass(); got != delivery.FailureClassExpectedFailure {
		t.Fatalf("report-visible failure class = %q, want expected_failure (not application)", got)
	}

	var triaged *delivery.IssueSnapshot
	for index := range snapshot.Issues {
		if snapshot.Issues[index].ID == 8001 {
			triaged = &snapshot.Issues[index]
		}
	}
	if triaged == nil {
		t.Fatalf("report snapshot is missing issue 8001: %+v", snapshot.Issues)
	}
	if triaged.TriageStatus != string(delivery.AnalysisStatusDone) ||
		triaged.TriageClassification != delivery.TriageClassificationExpectedFailure {
		t.Fatalf("issue triage projection = %q/%q, want done/expected_failure",
			triaged.TriageStatus, triaged.TriageClassification)
	}
	if !triaged.ExpectedFailureTriage() {
		t.Fatalf("issue %d does not expose the expected_failure triage", triaged.ID)
	}

	// No fake pass: the expected failure stays an uncovered case version. Its
	// gap reason remains execution_failed_coverage (Task 121: a budget-stopped
	// run is a genuine non-pass until a rerun observes a result).
	coverage := snapshot.Coverage
	if coverage.CaseVersionsCovered != 1 || coverage.CaseVersionsTotal != 2 {
		t.Fatalf("coverage = %+v, want 1/2 covered (no fake pass)", coverage)
	}
	if len(coverage.Gaps) != 1 || coverage.Gaps[0].CaseVersionID != 3001 ||
		coverage.Gaps[0].Reason != "execution_failed_coverage" {
		t.Fatalf("coverage gaps = %+v, want the genuine execution_failed_coverage gap", coverage.Gaps)
	}
}
