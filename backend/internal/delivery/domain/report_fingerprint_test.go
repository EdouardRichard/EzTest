package delivery

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

var reportFingerprintTime = time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)

// sampleReportSnapshot is a fully populated wave report input used to pin the
// canonical fingerprint shape (spec §6.1).
func sampleReportSnapshot() ReportSnapshot {
	waveID := int64(12)
	sutID := int64(31)
	requirementVersionID := int64(101)
	requirementClosureID := int64(55)
	sourceIssueID := int64(4)
	reportID := int64(70)
	return ReportSnapshot{
		Kind:      ReportKindWave,
		VersionID: 7,
		WaveID:    &waveID,
		// Version-level closure classification (R-G8): the sample is a normal
		// (non-waived) closure; the sensitivity matrix flips it to waived.
		ClosureKind: version.ClosureKindNormal,
		Unverified:  false,
		Requirements: []RequirementSnapshot{
			{ID: 2, VersionID: 7, CurrentVersionID: &requirementVersionID, Title: "需求二", Status: "closed", ClosureID: &requirementClosureID, ClosedAt: &reportFingerprintTime, CloseRound: 1, UpdatedAt: reportFingerprintTime, ACHash: strings.Repeat("d", 64)},
			{ID: 1, VersionID: 7, Title: "需求一", Status: "clarified", UpdatedAt: reportFingerprintTime, ACHash: strings.Repeat("e", 64)},
		},
		Waves: []WaveSnapshot{
			{
				ID: 12, VersionID: 7, Status: "completed", Trigger: "manual",
				Counts:   WaveCountsSnapshot{Total: 2, Passed: 1, Failed: 1, Evidence: 1},
				BySUT:    []SUTCountsSnapshot{{SUTID: sutID, Total: 2, Passed: 1, Failed: 1}},
				ReportID: &reportID, StartedAt: &reportFingerprintTime, FinishedAt: &reportFingerprintTime,
			},
			{ID: 11, VersionID: 7, Status: "completed", Trigger: "schedule", Counts: WaveCountsSnapshot{Total: 1, Passed: 1}, FinishedAt: &reportFingerprintTime},
		},
		CaseRuns: []CaseRunSnapshot{
			{
				ID: 21, VersionID: 7, WaveID: 12, CaseVersionID: 201, CaseTitle: "用例二十一",
				SUTID: &sutID, SUTIDs: []int64{32, 31},
				Status: "failed", FailureClass: "application", Summary: "断言失败",
				StepCount: 3, StepFailedCount: 1, AssertionCount: 2, AssertionFailedCount: 1,
				FailedStepSeq: 2, FailedAssertionSeq: 1, Category: "api", Severity: "major",
				// Per-run ACTUAL execution fingerprint (R-G1): the frozen intent
				// fingerprint the run really executed under, not the wave carrier.
				EnvironmentFingerprint: strings.Repeat("b", 64),
				CodeBaseline:           strings.Repeat("c", 64),
				Evidence: []EvidenceSnapshot{
					{ID: 41, CaseRunID: 21, SUTID: &sutID, StepSeq: intPtr(2), Kind: "screenshot", SHA256: strings.Repeat("a", 64), SizeBytes: 100},
				},
				StartedAt: &reportFingerprintTime, FinishedAt: &reportFingerprintTime, CreatedAt: reportFingerprintTime,
			},
			{ID: 20, VersionID: 7, WaveID: 12, CaseVersionID: 200, CaseTitle: "用例二十", Status: "passed",
				EnvironmentFingerprint: strings.Repeat("b", 64), CodeBaseline: strings.Repeat("c", 64),
				CreatedAt: reportFingerprintTime},
		},
		Issues: []IssueSnapshot{
			{
				ID: 3, VersionID: 7, Title: "问题三", Status: IssueStatusOpen, Category: "api", Severity: "major", Source: "analysis",
				DedupeKey:      strings.Repeat("f", 32),
				SourceIssueIDs: []int64{sourceIssueID, 5}, CaseRunIDs: []int64{21},
				CreatedAt: reportFingerprintTime, UpdatedAt: reportFingerprintTime,
			},
			{ID: 1, VersionID: 7, Title: "问题一", Status: IssueStatusConfirmed, Category: "ui", Severity: "minor", Source: "analysis", DedupeKey: strings.Repeat("0", 32), CreatedAt: reportFingerprintTime, UpdatedAt: reportFingerprintTime},
		},
		SUTs: []SUTSnapshot{
			{ID: 32, SUTID: 301, SUTName: "Web", EnvironmentID: 9, EnvironmentName: "staging"},
			{ID: 31, SUTID: 300, SUTName: "API", EnvironmentID: 8, EnvironmentName: "staging"},
		},
		Coverage: CoverageSnapshot{
			RequirementsTotal: 2, RequirementsCovered: 1, CaseVersionsTotal: 3, CaseVersionsCovered: 2,
			Gaps: []CoverageGapSnapshot{
				{RequirementID: 2, CaseVersionID: 202, Reason: "no_run"},
				{RequirementID: 1, CaseVersionID: 199, Reason: "no_case"},
			},
		},
		GeneratedAt: reportFingerprintTime,
	}
}

func intPtr(value int) *int { return &value }

func TestReportFingerprintStableForSameSnapshot(t *testing.T) {
	snapshot := sampleReportSnapshot()
	first := Fingerprint(snapshot)
	second := Fingerprint(snapshot)
	if first == "" {
		t.Fatal("Fingerprint returned an empty hash")
	}
	if first != second {
		t.Fatalf("Fingerprint is not stable: %q != %q", first, second)
	}
	if len(first) != 64 {
		t.Fatalf("fingerprint length = %d, want 64", len(first))
	}
	if first != strings.ToLower(first) {
		t.Fatalf("fingerprint is not lowercase hex: %q", first)
	}
	if _, err := hex.DecodeString(first); err != nil {
		t.Fatalf("fingerprint is not hex: %v", err)
	}

	firstJSON, err := CanonicalJSON(snapshot)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	secondJSON, err := CanonicalJSON(snapshot)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("CanonicalJSON is not stable:\n%s\n%s", firstJSON, secondJSON)
	}
	if !json.Valid(firstJSON) {
		t.Fatalf("CanonicalJSON is not valid JSON: %s", firstJSON)
	}
}

func TestReportFingerprintChangesWithIssueStatus(t *testing.T) {
	before := Fingerprint(sampleReportSnapshot())
	changed := sampleReportSnapshot()
	changed.Issues[0].Status = IssueStatusConfirmed
	if after := Fingerprint(changed); after == before {
		t.Fatalf("fingerprint did not change when an issue status changed: %q", after)
	}
}

func TestReportFingerprintIgnoresInputOrder(t *testing.T) {
	base := sampleReportSnapshot()
	reordered := sampleReportSnapshot()
	reverseRequirements(reordered.Requirements)
	reverseWaves(reordered.Waves)
	reverseCaseRuns(reordered.CaseRuns)
	reverseIssues(reordered.Issues)
	reverseSUTs(reordered.SUTs)
	reverseCoverageGaps(reordered.Coverage.Gaps)
	reverseInt64s(reordered.CaseRuns[0].SUTIDs)
	reverseInt64s(reordered.Issues[0].SourceIssueIDs)
	reverseInt64s(reordered.Issues[0].CaseRunIDs)
	if got, want := Fingerprint(reordered), Fingerprint(base); got != want {
		t.Fatalf("fingerprint depends on input order: %q != %q", got, want)
	}
}

func TestReportFingerprintEmptySnapshotStable(t *testing.T) {
	empty := ReportSnapshot{}
	if first, second := Fingerprint(empty), Fingerprint(ReportSnapshot{}); first != second {
		t.Fatalf("empty snapshot fingerprint is not stable: %q != %q", first, second)
	}
	emptyJSON, err := CanonicalJSON(empty)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	if strings.Contains(string(emptyJSON), `"requirements":null`) ||
		strings.Contains(string(emptyJSON), `"waves":null`) ||
		strings.Contains(string(emptyJSON), `"case_runs":null`) ||
		strings.Contains(string(emptyJSON), `"issues":null`) ||
		strings.Contains(string(emptyJSON), `"suts":null`) ||
		strings.Contains(string(emptyJSON), `"gaps":null`) {
		t.Fatalf("canonical JSON contains null slices: %s", emptyJSON)
	}
	for _, array := range []string{`"requirements":[]`, `"waves":[]`, `"case_runs":[]`, `"issues":[]`, `"suts":[]`, `"gaps":[]`} {
		if !strings.Contains(string(emptyJSON), array) {
			t.Fatalf("canonical JSON is missing the empty array %s: %s", array, emptyJSON)
		}
	}
	withEmptySlices := ReportSnapshot{
		Requirements: []RequirementSnapshot{},
		Waves:        []WaveSnapshot{},
		CaseRuns:     []CaseRunSnapshot{},
		Issues:       []IssueSnapshot{},
		SUTs:         []SUTSnapshot{},
		Coverage:     CoverageSnapshot{Gaps: []CoverageGapSnapshot{}},
	}
	if got, want := Fingerprint(withEmptySlices), Fingerprint(ReportSnapshot{}); got != want {
		t.Fatalf("nil and empty slices fingerprint differently: %q != %q", got, want)
	}
}

func TestReportFingerprintExcludesGeneratedAt(t *testing.T) {
	base := sampleReportSnapshot()
	later := sampleReportSnapshot()
	later.GeneratedAt = base.GeneratedAt.Add(48 * time.Hour)
	if got, want := Fingerprint(later), Fingerprint(base); got != want {
		t.Fatalf("generated_at participates in the fingerprint: %q != %q", got, want)
	}
	canonical, err := CanonicalJSON(base)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	if strings.Contains(string(canonical), "generated_at") {
		t.Fatalf("canonical JSON unexpectedly contains generated_at: %s", canonical)
	}
}

func TestReportFingerprintCanonicalJSONOrder(t *testing.T) {
	canonical, err := CanonicalJSON(sampleReportSnapshot())
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	keys := []string{"kind", "version_id", "wave_id", "requirements", "waves", "case_runs", "issues", "suts", "coverage"}
	previous := -1
	for _, key := range keys {
		index := bytes.Index(canonical, []byte(`"`+key+`":`))
		if index < 0 {
			t.Fatalf("canonical JSON is missing field %q: %s", key, canonical)
		}
		if index <= previous {
			t.Fatalf("canonical JSON field %q is out of order: %s", key, canonical)
		}
		previous = index
	}

	var decoded struct {
		Requirements []struct {
			ID int64 `json:"id"`
		} `json:"requirements"`
		Waves []struct {
			ID int64 `json:"id"`
		} `json:"waves"`
		CaseRuns []struct {
			ID     int64   `json:"id"`
			SUTIDs []int64 `json:"sut_ids"`
		} `json:"case_runs"`
		Issues []struct {
			ID             int64   `json:"id"`
			SourceIssueIDs []int64 `json:"source_issue_ids"`
		} `json:"issues"`
		SUTs []struct {
			ID int64 `json:"id"`
		} `json:"suts"`
		Coverage struct {
			Gaps []struct {
				RequirementID int64 `json:"requirement_id"`
				CaseVersionID int64 `json:"case_version_id"`
			} `json:"gaps"`
		} `json:"coverage"`
	}
	if err := json.Unmarshal(canonical, &decoded); err != nil {
		t.Fatalf("unmarshal canonical JSON: %v", err)
	}
	requirementIDs := make([]int64, 0, len(decoded.Requirements))
	for _, row := range decoded.Requirements {
		requirementIDs = append(requirementIDs, row.ID)
	}
	assertAscending(t, "requirements", requirementIDs)
	waveIDs := make([]int64, 0, len(decoded.Waves))
	for _, row := range decoded.Waves {
		waveIDs = append(waveIDs, row.ID)
	}
	assertAscending(t, "waves", waveIDs)
	caseRunIDs := make([]int64, 0, len(decoded.CaseRuns))
	for _, row := range decoded.CaseRuns {
		caseRunIDs = append(caseRunIDs, row.ID)
	}
	assertAscending(t, "case_runs", caseRunIDs)
	issueIDs := make([]int64, 0, len(decoded.Issues))
	for _, row := range decoded.Issues {
		issueIDs = append(issueIDs, row.ID)
	}
	assertAscending(t, "issues", issueIDs)
	sutIDs := make([]int64, 0, len(decoded.SUTs))
	for _, row := range decoded.SUTs {
		sutIDs = append(sutIDs, row.ID)
	}
	assertAscending(t, "suts", sutIDs)
	assertAscending(t, "case_run sut_ids", decoded.CaseRuns[0].SUTIDs)
	assertAscending(t, "issue source_issue_ids", decoded.Issues[0].SourceIssueIDs)
	for i := 1; i < len(decoded.Coverage.Gaps); i++ {
		previousGap := decoded.Coverage.Gaps[i-1]
		currentGap := decoded.Coverage.Gaps[i]
		if previousGap.RequirementID > currentGap.RequirementID ||
			(previousGap.RequirementID == currentGap.RequirementID && previousGap.CaseVersionID > currentGap.CaseVersionID) {
			t.Fatalf("coverage gaps are not sorted: %+v", decoded.Coverage.Gaps)
		}
	}
}

// TestFingerprintUpdatedAtAloneDoesNotChangeFingerprint pins R-G3 (P22):
// updated_at is a mutable row-modification timestamp, not a conclusion input.
// Touching it alone (requirement or issue) must never change the fingerprint,
// otherwise every unrelated row update mass-marks published reports stale.
func TestFingerprintUpdatedAtAloneDoesNotChangeFingerprint(t *testing.T) {
	base := Fingerprint(sampleReportSnapshot())
	changed := sampleReportSnapshot()
	changed.Requirements[0].UpdatedAt = reportFingerprintTime.Add(time.Hour)
	changed.Requirements[1].UpdatedAt = reportFingerprintTime.Add(time.Hour)
	changed.Issues[0].UpdatedAt = reportFingerprintTime.Add(time.Hour)
	changed.Issues[1].UpdatedAt = reportFingerprintTime.Add(time.Hour)
	if after := Fingerprint(changed); after != base {
		t.Fatalf("updated_at alone changed the fingerprint: %q != %q", after, base)
	}
}

// TestFingerprintSensitivityMatrix mutates each PRINCIPAL conclusion carrier of
// the redefined P22 payload (spec SC-G5, M2): each listed member changed alone
// must change the fingerprint, while updated_at alone (a mutable
// row-modification timestamp, R-G3) must not. Inputs are synthetic snapshots:
// the read-model population of the new carriers is W3 (T8/T9) scope; these
// subcases prove the canonical payload CONSUMES the listed carriers.
//
// This matrix does NOT claim to enumerate every field of the payload struct.
// Members not individually mutated here are: wave.report_id, the sut.* context
// rows, the coverage totals, most wave.counts columns, and most by_sut columns.
// Their canonical presence is structural (they are copied verbatim into the
// payload), and this file leaves their exact sensitivity outside the pinned set
// so a future maintainer knows where the matrix boundary is. Adding a mutation
// for any of them is welcome; do not read their absence as "not hashed".
func TestFingerprintSensitivityMatrix(t *testing.T) {
	otherTime := reportFingerprintTime.Add(time.Hour)
	otherID := int64(999)
	cases := []struct {
		name       string
		mutate     func(*ReportSnapshot)
		wantChange bool
	}{
		// --- version-level closure classification (R-G8) ---
		{"version closure_kind", func(s *ReportSnapshot) { s.ClosureKind = version.ClosureKindWaived }, true},
		{"version unverified", func(s *ReportSnapshot) { s.Unverified = true }, true},
		// --- case_run verdict / failure conclusion ---
		{"case_run verdict(status)", func(s *ReportSnapshot) { s.CaseRuns[0].Status = "passed" }, true},
		{"case_run failure_class", func(s *ReportSnapshot) { s.CaseRuns[0].FailureClass = "environment" }, true},
		{"case_run terminal_cause", func(s *ReportSnapshot) { s.CaseRuns[0].TerminalCause = TerminalCauseExecutorCapability }, true},
		{"case_run triage_classification", func(s *ReportSnapshot) { s.CaseRuns[0].TriageClassification = TriageClassificationFlaky }, true},
		{"case_run summary", func(s *ReportSnapshot) { s.CaseRuns[0].Summary = "断言失败（改）" }, true},
		{"case_run step_count", func(s *ReportSnapshot) { s.CaseRuns[0].StepCount = 4 }, true},
		{"case_run step_failed_count", func(s *ReportSnapshot) { s.CaseRuns[0].StepFailedCount = 2 }, true},
		{"case_run assertion_count", func(s *ReportSnapshot) { s.CaseRuns[0].AssertionCount = 3 }, true},
		{"case_run assertion_failed_count", func(s *ReportSnapshot) { s.CaseRuns[0].AssertionFailedCount = 2 }, true},
		{"case_run failed_step_seq", func(s *ReportSnapshot) { s.CaseRuns[0].FailedStepSeq = 3 }, true},
		{"case_run failed_assertion_seq", func(s *ReportSnapshot) { s.CaseRuns[0].FailedAssertionSeq = 2 }, true},
		{"case_run category", func(s *ReportSnapshot) { s.CaseRuns[0].Category = "ui" }, true},
		{"case_run severity", func(s *ReportSnapshot) { s.CaseRuns[0].Severity = "minor" }, true},
		{"case_run case_title", func(s *ReportSnapshot) { s.CaseRuns[0].CaseTitle = "用例二十一（改）" }, true},
		{"case_run sut_id (singular)", func(s *ReportSnapshot) { s.CaseRuns[0].SUTID = &otherID }, true},
		{"case_run case_version_id", func(s *ReportSnapshot) { s.CaseRuns[0].CaseVersionID = 202 }, true},
		{"case_run sut_ids", func(s *ReportSnapshot) { s.CaseRuns[0].SUTIDs = []int64{31} }, true},
		{"case_run finished_at", func(s *ReportSnapshot) { s.CaseRuns[0].FinishedAt = &otherTime }, true},
		{"case_run started_at", func(s *ReportSnapshot) { s.CaseRuns[0].StartedAt = &otherTime }, true},
		{"case_run created_at", func(s *ReportSnapshot) { s.CaseRuns[0].CreatedAt = otherTime }, true},
		// --- per-run ACTUAL environment fingerprint / code baseline (R-G1) ---
		{"case_run environment_fingerprint", func(s *ReportSnapshot) { s.CaseRuns[0].EnvironmentFingerprint = strings.Repeat("1", 64) }, true},
		{"case_run code_baseline", func(s *ReportSnapshot) { s.CaseRuns[0].CodeBaseline = strings.Repeat("2", 64) }, true},
		// --- evidence digest members (R-G2) ---
		{"evidence sha256 (digest content)", func(s *ReportSnapshot) { s.CaseRuns[0].Evidence[0].SHA256 = strings.Repeat("9", 64) }, true},
		{"evidence count (row added)", func(s *ReportSnapshot) {
			s.CaseRuns[0].Evidence = append(s.CaseRuns[0].Evidence, EvidenceSnapshot{ID: 42, CaseRunID: 21, Kind: "console", SHA256: strings.Repeat("8", 64), SizeBytes: 10})
		}, true},
		{"evidence dropped_count", func(s *ReportSnapshot) { s.CaseRuns[0].EvidenceDroppedCount = 1 }, true},
		// --- issue conclusion inputs ---
		{"issue status", func(s *ReportSnapshot) { s.Issues[0].Status = IssueStatusConfirmed }, true},
		{"issue severity", func(s *ReportSnapshot) { s.Issues[0].Severity = "minor" }, true},
		{"issue category", func(s *ReportSnapshot) { s.Issues[0].Category = "ui" }, true},
		{"issue triage_status", func(s *ReportSnapshot) { s.Issues[0].TriageStatus = string(AnalysisStatusDone) }, true},
		{"issue triage_classification", func(s *ReportSnapshot) { s.Issues[0].TriageClassification = TriageClassificationFlaky }, true},
		{"issue dedupe_key", func(s *ReportSnapshot) { s.Issues[0].DedupeKey = strings.Repeat("7", 32) }, true},
		{"issue merged_into_id", func(s *ReportSnapshot) { s.Issues[0].MergedIntoID = &otherID }, true},
		{"issue source_issue_ids", func(s *ReportSnapshot) { s.Issues[0].SourceIssueIDs = []int64{4} }, true},
		{"issue case_run_ids", func(s *ReportSnapshot) { s.Issues[0].CaseRunIDs = []int64{21, 20} }, true},
		{"issue title", func(s *ReportSnapshot) { s.Issues[0].Title = "问题三（改）" }, true},
		{"issue created_at", func(s *ReportSnapshot) { s.Issues[0].CreatedAt = otherTime }, true},
		// --- requirement closure / AC hash ---
		{"requirement status", func(s *ReportSnapshot) { s.Requirements[1].Status = "closed" }, true},
		{"requirement closure_id", func(s *ReportSnapshot) { s.Requirements[1].ClosureID = &otherID }, true},
		{"requirement closed_at", func(s *ReportSnapshot) { s.Requirements[1].ClosedAt = &otherTime }, true},
		{"requirement close_round", func(s *ReportSnapshot) { s.Requirements[0].CloseRound = 2 }, true},
		{"requirement reopen_count", func(s *ReportSnapshot) { s.Requirements[0].ReopenCount = 1 }, true},
		{"requirement waived", func(s *ReportSnapshot) { s.Requirements[1].Waived = true }, true},
		{"requirement ac_hash", func(s *ReportSnapshot) { s.Requirements[0].ACHash = strings.Repeat("3", 64) }, true},
		{"requirement current_version_id", func(s *ReportSnapshot) { s.Requirements[1].CurrentVersionID = &otherID }, true},
		{"requirement title", func(s *ReportSnapshot) { s.Requirements[0].Title = "需求二（改）" }, true},
		// --- AC/execution coverage projection ---
		{"coverage requirements_covered", func(s *ReportSnapshot) { s.Coverage.RequirementsCovered = 2 }, true},
		{"coverage case_versions_covered", func(s *ReportSnapshot) { s.Coverage.CaseVersionsCovered = 3 }, true},
		{"coverage gap reason", func(s *ReportSnapshot) { s.Coverage.Gaps[0].Reason = "execution_flaky_coverage" }, true},
		{"coverage gap added", func(s *ReportSnapshot) {
			s.Coverage.Gaps = append(s.Coverage.Gaps, CoverageGapSnapshot{RequirementID: 1, CaseVersionID: 198, Reason: "no_run"})
		}, true},
		// --- wave / SUT context members ---
		{"wave status", func(s *ReportSnapshot) { s.Waves[0].Status = "failed" }, true},
		{"wave trigger", func(s *ReportSnapshot) { s.Waves[0].Trigger = "schedule" }, true},
		{"wave counts passed", func(s *ReportSnapshot) { s.Waves[0].Counts.Passed = 2 }, true},
		{"wave started_at", func(s *ReportSnapshot) { s.Waves[0].StartedAt = &otherTime }, true},
		{"wave finished_at", func(s *ReportSnapshot) { s.Waves[0].FinishedAt = &otherTime }, true},
		{"wave by_sut failed", func(s *ReportSnapshot) { s.Waves[0].BySUT[0].Failed = 2 }, true},
		{"sut name", func(s *ReportSnapshot) { s.SUTs[0].SUTName = "Web（改）" }, true},
		// --- non-conclusion inputs (R-G3): must NOT change the fingerprint ---
		{"requirement updated_at alone", func(s *ReportSnapshot) {
			s.Requirements[0].UpdatedAt = otherTime
			s.Requirements[1].UpdatedAt = otherTime
		}, false},
		{"issue updated_at alone", func(s *ReportSnapshot) {
			s.Issues[0].UpdatedAt = otherTime
			s.Issues[1].UpdatedAt = otherTime
		}, false},
		{"generated_at alone", func(s *ReportSnapshot) { s.GeneratedAt = otherTime }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := Fingerprint(sampleReportSnapshot())
			mutated := sampleReportSnapshot()
			tc.mutate(&mutated)
			after := Fingerprint(mutated)
			if after == "" {
				t.Fatal("fingerprint is empty after mutation")
			}
			if tc.wantChange && after == base {
				t.Fatalf("conclusion input %q changed but fingerprint did not: %q", tc.name, after)
			}
			if !tc.wantChange && after != base {
				t.Fatalf("non-conclusion input %q changed the fingerprint: %q != %q", tc.name, after, base)
			}
		})
	}

	// updated_at must be absent from the canonical payload entirely (R-G3).
	canonical, err := CanonicalJSON(sampleReportSnapshot())
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	if bytes.Contains(canonical, []byte(`"updated_at"`)) {
		t.Fatalf("canonical JSON still contains updated_at: %s", canonical)
	}
}

// TestEvidenceDigestBounded pins R-G2 (SC-G5/G7): the fingerprint carries a
// per-run sorted sha256 SET digest + count + dropped_count, so the canonical
// payload is O(run count), never O(evidence count). Any evidence content
// change still changes the digest (causal completeness, art.4.6) — the digest
// is complete, not a capped sample.
func TestEvidenceDigestBounded(t *testing.T) {
	evidenceRows := func(n int) []EvidenceSnapshot {
		rows := make([]EvidenceSnapshot, 0, n)
		for i := 0; i < n; i++ {
			rows = append(rows, EvidenceSnapshot{
				ID: int64(1000 + i), CaseRunID: 21, Kind: "screenshot",
				SHA256: fmt.Sprintf("%064x", i+1), SizeBytes: int64(100 + i),
			})
		}
		return rows
	}
	snapshotWith := func(n int) ReportSnapshot {
		snapshot := sampleReportSnapshot()
		snapshot.CaseRuns = snapshot.CaseRuns[:1]
		snapshot.CaseRuns[0].Evidence = evidenceRows(n)
		snapshot.CaseRuns[0].EvidenceDroppedCount = 3
		return snapshot
	}

	const smallN, largeN = 10, 2000
	smallJSON, err := CanonicalJSON(snapshotWith(smallN))
	if err != nil {
		t.Fatalf("CanonicalJSON small: %v", err)
	}
	largeJSON, err := CanonicalJSON(snapshotWith(largeN))
	if err != nil {
		t.Fatalf("CanonicalJSON large: %v", err)
	}

	// Bounded: growing evidence 10 -> 2000 grows the payload only by the count
	// digits, never by per-evidence rows.
	if growth := len(largeJSON) - len(smallJSON); growth > 8 {
		t.Fatalf("canonical payload grew %d bytes for %d extra evidence rows; want O(run count)", growth, largeN-smallN)
	}
	for _, forbidden := range []string{`"sha256":`, `"step_seq":`, `"assertion_seq":`, `"size_bytes":`} {
		if bytes.Contains(largeJSON, []byte(forbidden)) {
			t.Fatalf("canonical payload still carries per-evidence field %s", forbidden)
		}
	}

	decodeDigest := func(canonical []byte) struct {
		SHA256SetDigest string `json:"sha256_set_digest"`
		Count           int    `json:"count"`
		DroppedCount    int    `json:"dropped_count"`
	} {
		var decoded struct {
			CaseRuns []struct {
				EvidenceDigest struct {
					SHA256SetDigest string `json:"sha256_set_digest"`
					Count           int    `json:"count"`
					DroppedCount    int    `json:"dropped_count"`
				} `json:"evidence_digest"`
			} `json:"case_runs"`
		}
		if err := json.Unmarshal(canonical, &decoded); err != nil {
			t.Fatalf("unmarshal canonical: %v", err)
		}
		if len(decoded.CaseRuns) != 1 {
			t.Fatalf("case_runs = %d, want 1", len(decoded.CaseRuns))
		}
		return decoded.CaseRuns[0].EvidenceDigest
	}
	digest := decodeDigest(largeJSON)
	if digest.Count != largeN || digest.DroppedCount != 3 {
		t.Fatalf("evidence digest count/dropped = %d/%d, want %d/3", digest.Count, digest.DroppedCount, largeN)
	}
	if len(digest.SHA256SetDigest) != 64 {
		t.Fatalf("sha256_set_digest = %q, want 64 hex chars", digest.SHA256SetDigest)
	}
	if _, err := hex.DecodeString(digest.SHA256SetDigest); err != nil {
		t.Fatalf("sha256_set_digest is not hex: %v", err)
	}

	// Causal completeness at scale: changing ONE sha out of 2000 (the last
	// row — a capped sample would miss it) changes the fingerprint.
	tailChanged := snapshotWith(largeN)
	tailChanged.CaseRuns[0].Evidence[largeN-1].SHA256 = strings.Repeat("9", 64)
	if Fingerprint(tailChanged) == Fingerprint(snapshotWith(largeN)) {
		t.Fatal("changing one evidence sha out of 2000 did not change the fingerprint (capped digest?)")
	}

	// Input order never matters (sorted set).
	reordered := snapshotWith(largeN)
	reverseEvidence(reordered.CaseRuns[0].Evidence)
	if Fingerprint(reordered) != Fingerprint(snapshotWith(largeN)) {
		t.Fatal("evidence input order changed the fingerprint")
	}

	// SET semantics: duplicate contents collapse in the digest, but the row
	// count keeps multiplicity, so adding a duplicate-content row is still a
	// fingerprint change (the evidence population changed).
	dup := snapshotWith(1)
	dup.CaseRuns[0].Evidence = append(dup.CaseRuns[0].Evidence, dup.CaseRuns[0].Evidence[0])
	dup.CaseRuns[0].Evidence[1].ID = 9999
	singleDigest := decodeDigest(mustCanonical(t, snapshotWith(1)))
	dupDigest := decodeDigest(mustCanonical(t, dup))
	if singleDigest.SHA256SetDigest != dupDigest.SHA256SetDigest {
		t.Fatalf("duplicate content changed the set digest: %q != %q", singleDigest.SHA256SetDigest, dupDigest.SHA256SetDigest)
	}
	if dupDigest.Count != 2 {
		t.Fatalf("duplicate row count = %d, want 2", dupDigest.Count)
	}
	if Fingerprint(dup) == Fingerprint(snapshotWith(1)) {
		t.Fatal("adding a duplicate-content evidence row did not change the fingerprint")
	}
}

// TestPerRunActualFingerprintIncluded pins R-G1 (art.4.6): the fingerprint
// consumes the ACTUAL per-run environment fingerprint / code baseline of EVERY
// run (the frozen intent fingerprint the run executed under), not only the
// first run and not a wave-level carrier value.
func TestPerRunActualFingerprintIncluded(t *testing.T) {
	base := sampleReportSnapshot()
	baseFP := Fingerprint(base)
	for _, runIndex := range []int{0, 1} {
		changedFP := sampleReportSnapshot()
		changedFP.CaseRuns[runIndex].EnvironmentFingerprint = strings.Repeat("1", 64)
		if Fingerprint(changedFP) == baseFP {
			t.Fatalf("run[%d] environment_fingerprint change did not change the fingerprint", runIndex)
		}
		changedBaseline := sampleReportSnapshot()
		changedBaseline.CaseRuns[runIndex].CodeBaseline = strings.Repeat("2", 64)
		if Fingerprint(changedBaseline) == baseFP {
			t.Fatalf("run[%d] code_baseline change did not change the fingerprint", runIndex)
		}
	}

	// The canonical payload carries the per-run values under case_runs.
	var decoded struct {
		CaseRuns []struct {
			ID                     int64  `json:"id"`
			EnvironmentFingerprint string `json:"environment_fingerprint"`
			CodeBaseline           string `json:"code_baseline"`
		} `json:"case_runs"`
	}
	if err := json.Unmarshal(mustCanonical(t, base), &decoded); err != nil {
		t.Fatalf("unmarshal canonical: %v", err)
	}
	if len(decoded.CaseRuns) != len(base.CaseRuns) {
		t.Fatalf("case_runs = %d, want %d", len(decoded.CaseRuns), len(base.CaseRuns))
	}
	for i, run := range decoded.CaseRuns {
		if run.EnvironmentFingerprint != base.CaseRuns[i].EnvironmentFingerprint ||
			run.CodeBaseline != base.CaseRuns[i].CodeBaseline {
			t.Fatalf("canonical run[%d] fp/baseline = %q/%q, want %q/%q", i,
				run.EnvironmentFingerprint, run.CodeBaseline,
				base.CaseRuns[i].EnvironmentFingerprint, base.CaseRuns[i].CodeBaseline)
		}
	}
}

func mustCanonical(t *testing.T, snapshot ReportSnapshot) []byte {
	t.Helper()
	canonical, err := CanonicalJSON(snapshot)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	return canonical
}

func TestReportFingerprintChangesWithIssueTitle(t *testing.T) {
	before := Fingerprint(sampleReportSnapshot())
	changed := sampleReportSnapshot()
	changed.Issues[0].Title = "问题三（已修改）"
	if after := Fingerprint(changed); after == before {
		t.Fatalf("fingerprint did not change when an issue title changed: %q", after)
	}
}

func assertAscending(t *testing.T, name string, ids []int64) {
	t.Helper()
	for i := 1; i < len(ids); i++ {
		if ids[i-1] > ids[i] {
			t.Fatalf("%s are not sorted by id ascending: %v", name, ids)
		}
	}
}

func reverseRequirements(rows []RequirementSnapshot) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

func reverseWaves(rows []WaveSnapshot) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	for index := range rows {
		reverseSUTCounts(rows[index].BySUT)
	}
}

func reverseSUTCounts(rows []SUTCountsSnapshot) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

func reverseCaseRuns(rows []CaseRunSnapshot) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	for index := range rows {
		reverseInt64s(rows[index].SUTIDs)
		reverseEvidence(rows[index].Evidence)
	}
}

func reverseEvidence(rows []EvidenceSnapshot) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

func reverseIssues(rows []IssueSnapshot) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	for index := range rows {
		reverseInt64s(rows[index].SourceIssueIDs)
		reverseInt64s(rows[index].CaseRunIDs)
	}
}

func reverseSUTs(rows []SUTSnapshot) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

func reverseCoverageGaps(rows []CoverageGapSnapshot) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

func reverseInt64s(values []int64) {
	for i, j := 0, len(values)-1; i < j; i, j = i+1, j-1 {
		values[i], values[j] = values[j], values[i]
	}
}
