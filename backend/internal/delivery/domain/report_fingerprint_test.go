package delivery

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var reportFingerprintTime = time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)

// sampleReportSnapshot is a fully populated wave report input used to pin the
// canonical fingerprint shape (spec §6.1).
func sampleReportSnapshot() ReportSnapshot {
	waveID := int64(12)
	versionSUTID := int64(31)
	requirementVersionID := int64(101)
	requirementClosureID := int64(55)
	sourceIssueID := int64(4)
	reportID := int64(70)
	return ReportSnapshot{
		Kind:      ReportKindWave,
		VersionID: 7,
		WaveID:    &waveID,
		Requirements: []RequirementSnapshot{
			{ID: 2, VersionID: 7, CurrentVersionID: &requirementVersionID, Title: "需求二", Status: "closed", ClosureID: &requirementClosureID, ClosedAt: &reportFingerprintTime, CloseRound: 1, UpdatedAt: reportFingerprintTime},
			{ID: 1, VersionID: 7, Title: "需求一", Status: "clarified", UpdatedAt: reportFingerprintTime},
		},
		Waves: []WaveSnapshot{
			{
				ID: 12, VersionID: 7, Status: "completed", Trigger: "manual",
				Counts:       WaveCountsSnapshot{Total: 2, Passed: 1, Failed: 1, Evidence: 1},
				ByVersionSUT: []SUTCountsSnapshot{{VersionSUTID: versionSUTID, Total: 2, Passed: 1, Failed: 1}},
				ReportID:     &reportID, StartedAt: &reportFingerprintTime, FinishedAt: &reportFingerprintTime,
			},
			{ID: 11, VersionID: 7, Status: "completed", Trigger: "schedule", Counts: WaveCountsSnapshot{Total: 1, Passed: 1}, FinishedAt: &reportFingerprintTime},
		},
		CaseRuns: []CaseRunSnapshot{
			{
				ID: 21, VersionID: 7, WaveID: 12, CaseVersionID: 201, CaseTitle: "用例二十一",
				VersionSUTID: &versionSUTID, VersionSUTIDs: []int64{32, 31},
				Status: "failed", FailureClass: "application", Summary: "断言失败",
				StepCount: 3, StepFailedCount: 1, AssertionCount: 2, AssertionFailedCount: 1,
				FailedStepSeq: 2, FailedAssertionSeq: 1, Category: "api", Severity: "major",
				Evidence: []EvidenceSnapshot{
					{ID: 41, CaseRunID: 21, VersionSUTID: &versionSUTID, StepSeq: intPtr(2), Kind: "screenshot", SHA256: strings.Repeat("a", 64), SizeBytes: 100},
				},
				StartedAt: &reportFingerprintTime, FinishedAt: &reportFingerprintTime, CreatedAt: reportFingerprintTime,
			},
			{ID: 20, VersionID: 7, WaveID: 12, CaseVersionID: 200, CaseTitle: "用例二十", Status: "passed", CreatedAt: reportFingerprintTime},
		},
		Issues: []IssueSnapshot{
			{
				ID: 3, VersionID: 7, Title: "问题三", Status: IssueStatusOpen, Category: "api", Severity: "major", Source: "analysis",
				SourceIssueIDs: []int64{sourceIssueID, 5}, CaseRunIDs: []int64{21},
				CreatedAt: reportFingerprintTime, UpdatedAt: reportFingerprintTime,
			},
			{ID: 1, VersionID: 7, Title: "问题一", Status: IssueStatusConfirmed, Category: "ui", Severity: "minor", Source: "analysis", CreatedAt: reportFingerprintTime, UpdatedAt: reportFingerprintTime},
		},
		VersionSUTs: []VersionSUTSnapshot{
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
	reverseVersionSUTs(reordered.VersionSUTs)
	reverseCoverageGaps(reordered.Coverage.Gaps)
	reverseInt64s(reordered.CaseRuns[0].VersionSUTIDs)
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
		strings.Contains(string(emptyJSON), `"version_suts":null`) ||
		strings.Contains(string(emptyJSON), `"gaps":null`) {
		t.Fatalf("canonical JSON contains null slices: %s", emptyJSON)
	}
	for _, array := range []string{`"requirements":[]`, `"waves":[]`, `"case_runs":[]`, `"issues":[]`, `"version_suts":[]`, `"gaps":[]`} {
		if !strings.Contains(string(emptyJSON), array) {
			t.Fatalf("canonical JSON is missing the empty array %s: %s", array, emptyJSON)
		}
	}
	withEmptySlices := ReportSnapshot{
		Requirements: []RequirementSnapshot{},
		Waves:        []WaveSnapshot{},
		CaseRuns:     []CaseRunSnapshot{},
		Issues:       []IssueSnapshot{},
		VersionSUTs:  []VersionSUTSnapshot{},
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
	keys := []string{"kind", "version_id", "wave_id", "requirements", "waves", "case_runs", "issues", "version_suts", "coverage"}
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
			ID            int64   `json:"id"`
			VersionSUTIDs []int64 `json:"version_sut_ids"`
		} `json:"case_runs"`
		Issues []struct {
			ID             int64   `json:"id"`
			SourceIssueIDs []int64 `json:"source_issue_ids"`
		} `json:"issues"`
		VersionSUTs []struct {
			ID int64 `json:"id"`
		} `json:"version_suts"`
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
	versionSUTIDs := make([]int64, 0, len(decoded.VersionSUTs))
	for _, row := range decoded.VersionSUTs {
		versionSUTIDs = append(versionSUTIDs, row.ID)
	}
	assertAscending(t, "version_suts", versionSUTIDs)
	assertAscending(t, "case_run version_sut_ids", decoded.CaseRuns[0].VersionSUTIDs)
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
		reverseSUTCounts(rows[index].ByVersionSUT)
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
		reverseInt64s(rows[index].VersionSUTIDs)
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

func reverseVersionSUTs(rows []VersionSUTSnapshot) {
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
