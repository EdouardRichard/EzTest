package adapters

// Task 118 (Release-T6): the delivery report must honor the platform issue
// triage. A failed(application) case_run whose issue triage is done with
// classification=flaky is automation flakiness, not a product defect: the
// report projection must not present it as application, and the coverage gap
// must not be counted as a product coverage failure. A real application
// failure (no flaky triage) must stay exactly as before.

import (
	"context"
	"encoding/json"
	"testing"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// fakeTriageRuns implements TriageReader for the report read-model tests.
type fakeTriageRuns struct {
	runs map[int64]delivery.AnalysisRun
}

func (f *fakeTriageRuns) LatestByIssue(_ context.Context, issueID int64) (delivery.AnalysisRun, bool, error) {
	run, ok := f.runs[issueID]
	return run, ok, nil
}

// finishedTriageRun builds one finished triage analysis run with the proposal
// JSON the issue API projects.
func finishedTriageRun(issueID int64, classification string) delivery.AnalysisRun {
	output, err := json.Marshal(map[string]any{"classification": classification})
	if err != nil {
		panic(err)
	}
	return delivery.AnalysisRun{
		ID:        9100 + issueID,
		VersionID: 7,
		Kind:      "triage",
		Status:    delivery.AnalysisStatusDone,
		Output:    output,
		CreatedAt: fixtureTime,
		UpdatedAt: fixtureAfter,
	}
}

// newReportFixtureDeps returns a fresh report read-model fixture.
func newReportFixtureDeps(t *testing.T) Deps {
	t.Helper()
	deps, _ := newReadModelFixture()
	return deps
}

func withFlakyTriage(deps Deps, issueID int64) Deps {
	deps.Triage = &fakeTriageRuns{runs: map[int64]delivery.AnalysisRun{
		issueID: finishedTriageRun(issueID, delivery.TriageClassificationFlaky),
	}}
	return deps
}

// TestFlakyTriageNotReportedAsApplication pins the classification projection:
// the raw P3 failure_class stays application (the execution fact is preserved),
// but the report-visible classification of the run is automation because the
// platform triage classified the issue as flaky.
func TestFlakyTriageNotReportedAsApplication(t *testing.T) {
	deps := withFlakyTriage(newReportFixtureDeps(t), 8001)
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
	if failed.TriageClassification != delivery.TriageClassificationFlaky {
		t.Fatalf("triage classification = %q, want flaky", failed.TriageClassification)
	}
	if got := failed.EffectiveFailureClass(); got != delivery.FailureClassAutomation {
		t.Fatalf("report-visible failure class = %q, want automation (not application)", got)
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
		triaged.TriageClassification != delivery.TriageClassificationFlaky {
		t.Fatalf("issue triage projection = %q/%q, want done/flaky",
			triaged.TriageStatus, triaged.TriageClassification)
	}
}

// TestCoverageNotPollutedByFlaky pins the coverage projection: the flaky
// failure stays uncovered (a pass may never be invented) but its gap reason is
// execution_flaky_coverage, not the product-execution failure reason, and the
// per-requirement product-failure counter stays zero.
func TestCoverageNotPollutedByFlaky(t *testing.T) {
	deps := withFlakyTriage(newReportFixtureDeps(t), 8001)
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	coverage := snapshot.Coverage
	if coverage.CaseVersionsCovered != 1 || coverage.CaseVersionsTotal != 2 {
		t.Fatalf("coverage = %+v, want 1/2 covered (a flaky failure is not a pass)", coverage)
	}
	if len(coverage.Gaps) != 1 {
		t.Fatalf("gaps = %+v, want the one uncovered case version 3001", coverage.Gaps)
	}
	gap := coverage.Gaps[0]
	if gap.CaseVersionID != 3001 || gap.Reason != delivery.CoverageGapExecutionFlaky {
		t.Fatalf("gap = %+v, want case 3001 / %s", gap, delivery.CoverageGapExecutionFlaky)
	}
	if len(coverage.ByRequirement) != 1 {
		t.Fatalf("per-requirement coverage = %+v, want one requirement row", coverage.ByRequirement)
	}
	row := coverage.ByRequirement[0]
	if row.CaseVersionsFailedCoverage != 0 {
		t.Fatalf("flaky failure polluted the product failed-coverage counter: %+v", row)
	}
	if row.CaseVersionsFlakyCoverage != 1 {
		t.Fatalf("flaky coverage count = %d, want 1: %+v", row.CaseVersionsFlakyCoverage, row)
	}
	if row.CaseVersionsCovered+row.CaseVersionsNotRun+row.CaseVersionsFailedCoverage+row.CaseVersionsFlakyCoverage != row.CaseVersionsTotal {
		t.Fatalf("per-requirement coverage does not add up: %+v", row)
	}
}

// TestRealApplicationStillApplication pins that only a done flaky triage
// downgrades the report classification: a real application failure (with a
// non-flaky triage classification or without triage at all) stays an
// application product defect and keeps its product coverage-failure gap.
func TestRealApplicationStillApplication(t *testing.T) {
	deps := newReportFixtureDeps(t)
	deps.Triage = &fakeTriageRuns{runs: map[int64]delivery.AnalysisRun{
		8001: finishedTriageRun(8001, "ui"),
	}}
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
	if failed.TriageClassification != "" {
		t.Fatalf("non-flaky triage changed the run triage classification: %+v", failed)
	}
	if got := failed.EffectiveFailureClass(); got != "application" {
		t.Fatalf("real application failure classified as %q, want application", got)
	}

	if len(snapshot.Coverage.Gaps) != 1 || snapshot.Coverage.Gaps[0].Reason != "execution_failed_coverage" {
		t.Fatalf("coverage gaps = %+v, want the product execution_failed_coverage gap", snapshot.Coverage.Gaps)
	}
	row := snapshot.Coverage.ByRequirement[0]
	if row.CaseVersionsFailedCoverage != 1 || row.CaseVersionsFlakyCoverage != 0 {
		t.Fatalf("per-requirement coverage = %+v, want one product failed-coverage gap and no flaky gap", row)
	}
}
