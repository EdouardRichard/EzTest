package deliveryapp

import (
	"strconv"
	"testing"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// TestACVerificationCoverageMatrix pins the SC-G4 I1 derivation rule: link
// coverage (an AC has a covering confirmed case) is NOT acceptance verification.
// The per-AC state overlays the covering cases' latest terminal verdict:
// passed->verified, failed->failed, blocked/inconclusive/not-run->unverified,
// no covering case->uncovered. A requirement whose ACs are 100% link-covered but
// whose covering runs are blocked must NOT read as verified.
func TestACVerificationCoverageMatrix(t *testing.T) {
	criteria := []delivery.ACCriterionSnapshot{
		{ID: "AC-1", Given: "g1", When: "w1", Then: "t1"},
		{ID: "AC-2", Given: "g2", When: "w2", Then: "t2"},
		{ID: "AC-3", Given: "g3", When: "w3", Then: "t3"},
		{ID: "AC-4", Given: "g4", When: "w4", Then: "t4"},
	}
	cases := []ACCaseCoverage{
		// AC-1 covered by a passed run -> verified.
		{CaseVersionID: 3001, CoveredACs: []string{"AC-1"}, LatestVerdict: string(execution.CaseRunPassed)},
		// AC-2 covered only by a blocked run -> unverified (NOT verified).
		{CaseVersionID: 3002, CoveredACs: []string{"AC-2"}, LatestVerdict: string(execution.CaseRunBlocked)},
		// AC-3 covered by a failed run -> failed.
		{CaseVersionID: 3003, CoveredACs: []string{"AC-3"}, LatestVerdict: string(execution.CaseRunFailed)},
		// AC-4 has no covering case -> uncovered.
	}
	matrix := BuildACVerificationMatrix(101, criteria, cases)
	if len(matrix) != 4 {
		t.Fatalf("matrix len = %d, want 4 (one row per AC)", len(matrix))
	}
	byAC := make(map[string]delivery.ACVerificationSnapshot, len(matrix))
	for _, row := range matrix {
		if row.RequirementID != 101 {
			t.Fatalf("row requirement = %d, want 101", row.RequirementID)
		}
		byAC[row.ACID] = row
	}
	want := map[string]string{
		"AC-1": delivery.ACVerificationVerified,
		"AC-2": delivery.ACVerificationUnverified,
		"AC-3": delivery.ACVerificationFailed,
		"AC-4": delivery.ACVerificationUncovered,
	}
	for ac, state := range want {
		row, ok := byAC[ac]
		if !ok {
			t.Fatalf("missing AC row %s", ac)
		}
		if row.State != state {
			t.Errorf("AC %s state = %q, want %q", ac, row.State, state)
		}
	}
	// g/w/t text travels with each row so a human reads the criterion itself.
	if byAC["AC-1"].Given != "g1" || byAC["AC-1"].When != "w1" || byAC["AC-1"].Then != "t1" {
		t.Errorf("AC-1 g/w/t = %q/%q/%q, want g1/w1/t1", byAC["AC-1"].Given, byAC["AC-1"].When, byAC["AC-1"].Then)
	}
	// The covering case version is recorded for traceability.
	if len(byAC["AC-1"].CoveringCaseVersionIDs) != 1 || byAC["AC-1"].CoveringCaseVersionIDs[0] != 3001 {
		t.Errorf("AC-1 covering cases = %v, want [3001]", byAC["AC-1"].CoveringCaseVersionIDs)
	}
	if len(byAC["AC-4"].CoveringCaseVersionIDs) != 0 {
		t.Errorf("AC-4 covering cases = %v, want none (uncovered)", byAC["AC-4"].CoveringCaseVersionIDs)
	}
}

// TestACVerificationLinkCoverageNotMisreadAsPassed is the Review Focus #2 guard:
// 100% link coverage (every AC has a covering case) but every covering run is
// blocked yields ZERO verified ACs. A human must not misread link coverage as
// acceptance-passed.
func TestACVerificationLinkCoverageNotMisreadAsPassed(t *testing.T) {
	criteria := []delivery.ACCriterionSnapshot{
		{ID: "AC-1"}, {ID: "AC-2"},
	}
	cases := []ACCaseCoverage{
		{CaseVersionID: 3001, CoveredACs: []string{"AC-1", "AC-2"}, LatestVerdict: string(execution.CaseRunBlocked)},
	}
	matrix := BuildACVerificationMatrix(101, criteria, cases)
	verified := 0
	linkCovered := 0
	for _, row := range matrix {
		if row.State == delivery.ACVerificationVerified {
			verified++
		}
		if len(row.CoveringCaseVersionIDs) > 0 {
			linkCovered++
		}
		if row.State != delivery.ACVerificationUnverified {
			t.Errorf("AC %s state = %q, want unverified (blocked run verifies nothing)", row.ACID, row.State)
		}
	}
	if linkCovered != 2 {
		t.Fatalf("link covered = %d, want 2 (100%% link coverage)", linkCovered)
	}
	if verified != 0 {
		t.Fatalf("verified = %d, want 0 (100%% link but blocked must NOT read as passed)", verified)
	}
}

// TestACVerificationMatrixDisplayCap pins the M4 display cap (SC-G4): a matrix
// larger than delivery.MaxReportACs is explicitly truncated with an annotation
// (Truncated/TotalBeforeCap), never silently dropped, while the summary counts
// still reflect the FULL AC set (art.7.6).
func TestACVerificationMatrixDisplayCap(t *testing.T) {
	total := delivery.MaxReportACs + 1
	criteria := make([]delivery.ACCriterionSnapshot, 0, total)
	for i := 0; i < total; i++ {
		criteria = append(criteria, delivery.ACCriterionSnapshot{ID: "AC-" + strconv.Itoa(i+1)})
	}
	// No covering cases: every AC is uncovered.
	matrix := BuildACVerificationMatrix(101, criteria, nil)
	summary := SummarizeACCoverage([]delivery.RequirementACCoverageSnapshot{{RequirementID: 101, ACs: matrix}})
	if summary.Total != total || summary.Uncovered != total {
		t.Fatalf("summary total/uncovered = %d/%d, want %d/%d (full set counts)", summary.Total, summary.Uncovered, total, total)
	}
	if !summary.Truncated {
		t.Fatal("summary.Truncated = false, want true (matrix exceeds the display cap)")
	}
	if summary.TotalBeforeCap != total {
		t.Fatalf("TotalBeforeCap = %d, want %d", summary.TotalBeforeCap, total)
	}
	displayed := 0
	for _, requirement := range summary.ByRequirement {
		displayed += len(requirement.ACs)
	}
	if displayed != delivery.MaxReportACs {
		t.Fatalf("displayed AC rows = %d, want capped at %d", displayed, delivery.MaxReportACs)
	}
}

// TestACVerificationMatrixUnderCapNotTruncated pins that a matrix within the cap
// is shown in full with no truncation annotation.
func TestACVerificationMatrixUnderCapNotTruncated(t *testing.T) {
	criteria := []delivery.ACCriterionSnapshot{{ID: "AC-1"}, {ID: "AC-2"}}
	matrix := BuildACVerificationMatrix(101, criteria, nil)
	summary := SummarizeACCoverage([]delivery.RequirementACCoverageSnapshot{{RequirementID: 101, ACs: matrix}})
	if summary.Truncated {
		t.Fatal("summary.Truncated = true, want false (matrix within the cap)")
	}
	if summary.Total != 2 || summary.TotalBeforeCap != 2 {
		t.Fatalf("total/TotalBeforeCap = %d/%d, want 2/2", summary.Total, summary.TotalBeforeCap)
	}
	if summary.Displayed != 2 {
		t.Fatalf("Displayed = %d, want 2 (within the cap every row is displayed)", summary.Displayed)
	}
}

// TestACVerificationMatrixDisplayCapStatesDisplayedCount pins final fix wave #2
// (七.6): the truncation note must state the DISPLAYED count, not the full Total.
// SummarizeACCoverage derives Displayed from the rows it actually keeps, so a
// 501-AC matrix capped at 500 reports Displayed=500 < Total=501 — the note never
// reads "显示 501 条，截断前共 501 条" above a 500-row table. The count is derived
// THROUGH SummarizeACCoverage (not hand-set), proving the production path.
func TestACVerificationMatrixDisplayCapStatesDisplayedCount(t *testing.T) {
	total := delivery.MaxReportACs + 1
	criteria := make([]delivery.ACCriterionSnapshot, 0, total)
	for i := 0; i < total; i++ {
		criteria = append(criteria, delivery.ACCriterionSnapshot{ID: "AC-" + strconv.Itoa(i+1)})
	}
	matrix := BuildACVerificationMatrix(101, criteria, nil)
	summary := SummarizeACCoverage([]delivery.RequirementACCoverageSnapshot{{RequirementID: 101, ACs: matrix}})
	if !summary.Truncated || summary.TotalBeforeCap != total || summary.Total != total {
		t.Fatalf("summary = %+v, want truncated full set of %d", summary, total)
	}
	if summary.Displayed != delivery.MaxReportACs {
		t.Fatalf("Displayed = %d, want %d (the actual kept row count, not the full Total %d)",
			summary.Displayed, delivery.MaxReportACs, summary.Total)
	}
	if summary.Displayed >= summary.Total {
		t.Fatalf("Displayed = %d must be < Total = %d when truncated (the note would otherwise be self-contradictory)",
			summary.Displayed, summary.Total)
	}
	// The displayed count equals the rows actually carried after the cap.
	kept := 0
	for _, requirement := range summary.ByRequirement {
		kept += len(requirement.ACs)
	}
	if summary.Displayed != kept {
		t.Fatalf("Displayed = %d, want the kept row count %d", summary.Displayed, kept)
	}
}

// TestACVerificationMatrixDisplayCapAcrossRequirements pins Displayed when the
// cap spans several requirements (a requirement may be partially shown): the
// displayed count is the sum of kept rows across all requirements, not Total.
func TestACVerificationMatrixDisplayCapAcrossRequirements(t *testing.T) {
	// Two requirements whose combined AC count exceeds the cap by 3.
	first := delivery.MaxReportACs - 2
	second := 5
	build := func(requirementID int64, count int) delivery.RequirementACCoverageSnapshot {
		criteria := make([]delivery.ACCriterionSnapshot, 0, count)
		for i := 0; i < count; i++ {
			criteria = append(criteria, delivery.ACCriterionSnapshot{ID: "AC-" + strconv.Itoa(i+1)})
		}
		return delivery.RequirementACCoverageSnapshot{
			RequirementID: requirementID,
			ACs:           BuildACVerificationMatrix(requirementID, criteria, nil),
		}
	}
	summary := SummarizeACCoverage([]delivery.RequirementACCoverageSnapshot{build(101, first), build(102, second)})
	if summary.Total != first+second || !summary.Truncated {
		t.Fatalf("summary = %+v, want truncated total %d", summary, first+second)
	}
	if summary.Displayed != delivery.MaxReportACs {
		t.Fatalf("Displayed = %d, want %d across requirements", summary.Displayed, delivery.MaxReportACs)
	}
}

// TestACVerificationStateAggregation pins the multi-case aggregation rule: any
// covering passed run verifies the AC; else any failed run marks it failed; else
// (blocked/inconclusive/not-run) unverified. A not-run covering case contributes
// no verdict and never verifies.
func TestACVerificationStateAggregation(t *testing.T) {
	tests := []struct {
		name     string
		verdicts []string
		want     string
	}{
		{"no covering case", nil, delivery.ACVerificationUncovered},
		{"empty verdicts slice", []string{}, delivery.ACVerificationUncovered},
		{"single passed", []string{string(execution.CaseRunPassed)}, delivery.ACVerificationVerified},
		{"passed wins over failed", []string{string(execution.CaseRunFailed), string(execution.CaseRunPassed)}, delivery.ACVerificationVerified},
		{"failed without pass", []string{string(execution.CaseRunFailed)}, delivery.ACVerificationFailed},
		{"blocked only", []string{string(execution.CaseRunBlocked)}, delivery.ACVerificationUnverified},
		{"inconclusive only", []string{string(execution.CaseRunInconclusive)}, delivery.ACVerificationUnverified},
		{"not run covering case", []string{""}, delivery.ACVerificationUnverified},
		{"failed plus blocked", []string{string(execution.CaseRunBlocked), string(execution.CaseRunFailed)}, delivery.ACVerificationFailed},
		{"skipped is not verification", []string{string(execution.CaseRunSkipped)}, delivery.ACVerificationUnverified},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := delivery.DeriveACVerificationState(tc.verdicts); got != tc.want {
				t.Fatalf("DeriveACVerificationState(%v) = %q, want %q", tc.verdicts, got, tc.want)
			}
		})
	}
}
