package adapters

import (
	"context"
	"testing"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// fakeJudgments serves the design judge read report (verdict/findings/
// review_state) the report consumes for the case/AC coverage annotation
// (v0.2.4-G SC-G4 I3). calls counts reads so the read-side existence mutation
// proof can assert the production read path is actually exercised.
type fakeJudgments struct {
	report designapp.JudgmentReportView
	err    error
	calls  int
}

func (f *fakeJudgments) ListJudgments(_ context.Context, _ int64, _ *int64) (designapp.JudgmentReportView, error) {
	f.calls++
	return f.report, f.err
}

// newCoverageFixture builds a minimal version read fixture with an AC set,
// per-case covered_acs linkage and terminal runs, so the AC verification matrix
// and judge consumption can be asserted end-to-end through VersionSnapshot.
//
// Requirement 101 has AC-1/AC-2/AC-3. Case 3001 covers AC-1 (passed run 5001 ->
// verified); case 3002 covers AC-2 (blocked run 5002 -> unverified); AC-3 has no
// covering case (uncovered). Link coverage is 2/3, verification is 1 verified.
func newCoverageFixture() (Deps, *fakeJudgments) {
	waves := &fakeWaves{waves: []execution.RunWave{
		{ID: 11, VersionID: 7, Status: execution.WaveDone, Trigger: execution.WaveTriggerManual,
			Counts: execution.WaveCounts{Total: 2, Passed: 1, Blocked: 1, BySUT: map[string]execution.SUTCounts{}}},
	}}
	runs := &fakeCaseRuns{runs: []execution.CaseRun{
		{ID: 5001, WaveID: 11, CaseVersionID: 3001, Status: execution.CaseRunPassed,
			Summary: execution.CaseRunSummary{StepTotal: 1, AssertionTotal: 1}},
		{ID: 5002, WaveID: 11, CaseVersionID: 3002, Status: execution.CaseRunBlocked,
			Summary: execution.CaseRunSummary{StepTotal: 1}},
	}}
	designCases := &fakeDesign{cases: map[int64][]designapp.ExecutableCaseView{7: {
		{CaseID: 201, CaseVersionID: 3001, RequirementID: 101, RequirementVersionID: 1001, Title: "Case A", CoveredACs: []string{"AC-1"}},
		{CaseID: 202, CaseVersionID: 3002, RequirementID: 101, RequirementVersionID: 1001, Title: "Case B", CoveredACs: []string{"AC-2"}},
	}}}
	requirements := &fakeRequirements{
		views: map[int64][]versionapp.RequirementView{7: {
			{ID: 101, CurrentVersionID: 1001, Title: "Requirement A", Status: "clarified"},
		}},
		details: map[int64]versionapp.RequirementDetail{
			101: {CurrentVersion: versionapp.RequirementVersionView{
				ID: 1001,
				AcceptanceCriteria: []versionapp.RequirementAcceptanceCriterionView{
					{ID: "AC-1", Given: "g1", When: "w1", Then: "t1"},
					{ID: "AC-2", Given: "g2", When: "w2", Then: "t2"},
					{ID: "AC-3", Given: "g3", When: "w3", Then: "t3"},
				},
			}},
		},
	}
	versions := &fakeVersions{views: map[int64]versionapp.VersionView{7: {ID: 7, Title: "Version", Status: "running"}}}
	judgments := &fakeJudgments{report: designapp.JudgmentReportView{Cases: []designapp.JudgmentCaseJudgmentView{
		{CaseID: 201, CurrentVersionID: 3001, ReviewState: design.ReviewStatePass,
			Judgment: &design.Judgment{CaseID: 201, CaseVersionID: 3001, RequirementID: 101, Verdict: design.VerdictPass,
				Findings:         []design.JudgmentFinding{{Dimension: "coverage", Severity: design.SeverityMinor, Message: "ok"}},
				ACCoverageMatrix: map[string]any{"AC-1": []any{"3001"}}}},
		{CaseID: 202, CurrentVersionID: 3002, ReviewState: design.ReviewStateReworking,
			Judgment: &design.Judgment{CaseID: 202, CaseVersionID: 3002, RequirementID: 101, Verdict: design.VerdictFail,
				Findings: []design.JudgmentFinding{{Dimension: "assertion", Severity: design.SeverityBlocker, Message: "missing then"}}}},
	}}}
	deps := Deps{
		Versions:     versions,
		Requirements: requirements,
		Design:       designCases,
		Waves:        waves,
		CaseRuns:     runs,
		Results:      &fakeResults{},
		SUTContexts:  &fakeSUTContexts{},
		Judgments:    judgments,
	}
	return deps, judgments
}

// TestACVerificationCoverageMatrix asserts the report read model produces the
// per-AC verification matrix (SC-G4 I1): verified/unverified/uncovered states
// with g/w/t text, derived from covered_acs + the covering runs' terminal verdict.
func TestACVerificationCoverageMatrix(t *testing.T) {
	deps, _ := newCoverageFixture()
	reader := NewReportDataReader(deps)
	snapshot, err := reader.VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	ac := snapshot.Coverage.ACVerification
	if ac.Total != 3 {
		t.Fatalf("AC total = %d, want 3", ac.Total)
	}
	if ac.Verified != 1 || ac.Unverified != 1 || ac.Uncovered != 1 || ac.Failed != 0 {
		t.Fatalf("AC counts verified/unverified/uncovered/failed = %d/%d/%d/%d, want 1/1/1/0",
			ac.Verified, ac.Unverified, ac.Uncovered, ac.Failed)
	}
	if ac.LinkCovered != 2 {
		t.Fatalf("AC link covered = %d, want 2 (AC-1,AC-2 have covering cases; AC-3 none)", ac.LinkCovered)
	}
	if len(ac.ByRequirement) != 1 || ac.ByRequirement[0].RequirementID != 101 {
		t.Fatalf("AC by requirement = %+v, want one row for 101", ac.ByRequirement)
	}
	byAC := make(map[string]delivery.ACVerificationSnapshot)
	for _, row := range ac.ByRequirement[0].ACs {
		byAC[row.ACID] = row
	}
	if byAC["AC-1"].State != delivery.ACVerificationVerified {
		t.Errorf("AC-1 state = %q, want verified (passed run)", byAC["AC-1"].State)
	}
	if byAC["AC-2"].State != delivery.ACVerificationUnverified {
		t.Errorf("AC-2 state = %q, want unverified (blocked run)", byAC["AC-2"].State)
	}
	if byAC["AC-3"].State != delivery.ACVerificationUncovered {
		t.Errorf("AC-3 state = %q, want uncovered (no covering case)", byAC["AC-3"].State)
	}
	if byAC["AC-1"].Then != "t1" {
		t.Errorf("AC-1 then = %q, want t1 (g/w/t text travels with the row)", byAC["AC-1"].Then)
	}
}

// TestJudgeFactsConsumed asserts the report presents the design judge verdict/
// findings/review_state (SC-G4 I3) at the case coverage area, consumed through
// the cross-context read port. It is also the read-side existence mutation guard:
// deleting the judge consumption in the read model leaves Judgments empty and
// fails this test (RED), restoring it returns GREEN.
func TestJudgeFactsConsumed(t *testing.T) {
	deps, judgments := newCoverageFixture()
	reader := NewReportDataReader(deps)
	snapshot, err := reader.VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	if judgments.calls == 0 {
		t.Fatal("the judge read port was never called; the report does not consume judge facts")
	}
	if len(snapshot.Judgments) != 2 {
		t.Fatalf("snapshot.Judgments len = %d, want 2 (one per current case)", len(snapshot.Judgments))
	}
	byCase := make(map[int64]delivery.JudgmentSnapshot)
	for _, j := range snapshot.Judgments {
		byCase[j.CaseID] = j
	}
	pass := byCase[201]
	if pass.ReviewState != string(design.ReviewStatePass) || pass.Verdict != design.VerdictPass {
		t.Errorf("case 201 judge = review_state %q verdict %q, want pass/pass", pass.ReviewState, pass.Verdict)
	}
	if len(pass.Findings) != 1 || pass.Findings[0].Message != "ok" {
		t.Errorf("case 201 findings = %+v, want one 'ok' finding", pass.Findings)
	}
	// The judge's candidate ac_coverage_matrix is consumed for presentation
	// (SC-G4), distinct from the confirmed covered_acs verification truth.
	if got := pass.ACCoverageMatrix["AC-1"]; len(got) != 1 || got[0] != "3001" {
		t.Errorf("case 201 judge ac_coverage_matrix[AC-1] = %v, want [3001]", got)
	}
	fail := byCase[202]
	if fail.ReviewState != string(design.ReviewStateReworking) || fail.Verdict != design.VerdictFail {
		t.Errorf("case 202 judge = review_state %q verdict %q, want reworking/fail", fail.ReviewState, fail.Verdict)
	}
	if len(fail.Findings) != 1 || fail.Findings[0].Severity != design.SeverityBlocker {
		t.Errorf("case 202 findings = %+v, want one blocker finding", fail.Findings)
	}
}

// TestJudgeFactsAbsentWithoutPort pins graceful degradation: an unwired judge
// port (older wirings) yields no judge annotation rather than failing the report.
// The production wiring always installs the port (DoD no unwired code).
func TestJudgeFactsAbsentWithoutPort(t *testing.T) {
	deps, _ := newCoverageFixture()
	deps.Judgments = nil
	reader := NewReportDataReader(deps)
	snapshot, err := reader.VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	if len(snapshot.Judgments) != 0 {
		t.Fatalf("snapshot.Judgments len = %d, want 0 without a judge port", len(snapshot.Judgments))
	}
}

// TestJudgeACCoverageMatrixDiscardsDisclosed pins fix-round minor #5 (art.7.6):
// malformed judge matrix entries (a non-array AC value, or a non-string/empty
// case id) are counted as discarded, never silently dropped — the presentation
// may be smaller than the judge submitted and the count discloses it.
func TestJudgeACCoverageMatrixDiscardsDisclosed(t *testing.T) {
	raw := map[string]any{
		"AC-1": []any{"3001", "", 42}, // "" and 42 are malformed -> 2 dropped
		"AC-2": "not-an-array",        // non-array AC value -> 1 dropped
		"AC-3": []any{"3002"},         // well-formed
	}
	matrix, dropped := judgeACCoverageMatrix(raw)
	if dropped != 3 {
		t.Fatalf("dropped = %d, want 3 (2 malformed case ids + 1 non-array AC value)", dropped)
	}
	if got := matrix["AC-1"]; len(got) != 1 || got[0] != "3001" {
		t.Errorf("AC-1 = %v, want [3001] (malformed ids discarded)", got)
	}
	if _, ok := matrix["AC-2"]; ok {
		t.Errorf("AC-2 (non-array value) must be discarded, got %v", matrix["AC-2"])
	}
	if got := matrix["AC-3"]; len(got) != 1 || got[0] != "3002" {
		t.Errorf("AC-3 = %v, want [3002]", got)
	}
	// A nil/empty matrix yields nil and zero dropped.
	if m, d := judgeACCoverageMatrix(nil); m != nil || d != 0 {
		t.Errorf("nil matrix = %v dropped %d, want nil/0", m, d)
	}
}
