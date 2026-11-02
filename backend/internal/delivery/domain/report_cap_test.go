package delivery

import (
	"reflect"
	"testing"
)

// capTestSnapshot builds a snapshot whose capped lists exceed the spec §6.1
// limits and arrive in a non-ID order, so capping must sort before slicing.
func capTestSnapshot() ReportSnapshot {
	snapshot := ReportSnapshot{
		Kind:      ReportKindVersion,
		VersionID: 9,
		Requirements: []RequirementSnapshot{
			{ID: 2, VersionID: 9, Title: "需求二", Status: "closed"},
			{ID: 1, VersionID: 9, Title: "需求一", Status: "obsolete"},
		},
		VersionSUTs: []VersionSUTSnapshot{{ID: 31, SUTID: 300, SUTName: "API"}},
	}
	for id := int64(MaxReportIssues + 2); id >= 1; id-- {
		snapshot.Issues = append(snapshot.Issues, IssueSnapshot{ID: id, VersionID: 9, Title: "issue"})
	}
	for id := int64(MaxReportWaves + 1); id >= 1; id-- {
		snapshot.Waves = append(snapshot.Waves, WaveSnapshot{ID: id, VersionID: 9, Status: "done"})
	}
	for id := int64(MaxReportCaseRuns + 3); id >= 1; id-- {
		snapshot.CaseRuns = append(snapshot.CaseRuns, CaseRunSnapshot{ID: id, VersionID: 9, WaveID: 1})
	}
	return snapshot
}

// TestCapReportSnapshotCapsFirstNByID asserts the §6.1 caps: issues 500, waves
// 200 and case_runs 2000, first N by ascending ID, with a visible truncation
// marker per capped list.
func TestCapReportSnapshotCapsFirstNByID(t *testing.T) {
	snapshot := capTestSnapshot()
	capped := CapReportSnapshot(snapshot)

	if len(capped.Issues) != MaxReportIssues {
		t.Fatalf("issues = %d, want %d", len(capped.Issues), MaxReportIssues)
	}
	if len(capped.Waves) != MaxReportWaves {
		t.Fatalf("waves = %d, want %d", len(capped.Waves), MaxReportWaves)
	}
	if len(capped.CaseRuns) != MaxReportCaseRuns {
		t.Fatalf("case_runs = %d, want %d", len(capped.CaseRuns), MaxReportCaseRuns)
	}
	for index, issue := range capped.Issues {
		if want := int64(index + 1); issue.ID != want {
			t.Fatalf("issues[%d].ID = %d, want %d (first by ascending ID)", index, issue.ID, want)
		}
	}
	for index, wave := range capped.Waves {
		if want := int64(index + 1); wave.ID != want {
			t.Fatalf("waves[%d].ID = %d, want %d", index, wave.ID, want)
		}
	}
	for index, run := range capped.CaseRuns {
		if want := int64(index + 1); run.ID != want {
			t.Fatalf("case_runs[%d].ID = %d, want %d", index, run.ID, want)
		}
	}
	wantSections := []string{ReportTruncationIssues, ReportTruncationWaves, ReportTruncationCaseRuns}
	if !reflect.DeepEqual(capped.TruncatedSections, wantSections) {
		t.Fatalf("TruncatedSections = %v, want %v", capped.TruncatedSections, wantSections)
	}
	if len(capped.Requirements) != 2 || len(capped.VersionSUTs) != 1 {
		t.Fatalf("uncapped lists changed: requirements=%d version_suts=%d", len(capped.Requirements), len(capped.VersionSUTs))
	}
	if len(snapshot.Issues) != MaxReportIssues+2 {
		t.Fatalf("CapReportSnapshot mutated the input issues: %d", len(snapshot.Issues))
	}
}

// TestCapReportSnapshotLeavesSmallSnapshotUntouched asserts an in-budget
// snapshot is returned unchanged with no truncation marker.
func TestCapReportSnapshotLeavesSmallSnapshotUntouched(t *testing.T) {
	snapshot := ReportSnapshot{
		Kind: ReportKindWave, VersionID: 9,
		Issues:   []IssueSnapshot{{ID: 1}},
		Waves:    []WaveSnapshot{{ID: 2}},
		CaseRuns: []CaseRunSnapshot{{ID: 3}},
	}
	capped := CapReportSnapshot(snapshot)
	if !reflect.DeepEqual(capped, snapshot) {
		t.Fatalf("in-budget snapshot changed: %+v -> %+v", snapshot, capped)
	}
	if len(capped.TruncatedSections) != 0 {
		t.Fatalf("TruncatedSections = %v, want empty", capped.TruncatedSections)
	}
}

// TestCapReportSnapshotPreservesReaderTruncation keeps truncation markers set
// by a ReportDataReader that already capped a list at the boundary: the service
// cannot infer overflow from an exactly-capped list.
func TestCapReportSnapshotPreservesReaderTruncation(t *testing.T) {
	snapshot := ReportSnapshot{
		Kind: ReportKindVersion, VersionID: 9,
		Issues:            []IssueSnapshot{{ID: 1}},
		TruncatedSections: []string{ReportTruncationIssues},
	}
	capped := CapReportSnapshot(snapshot)
	if !reflect.DeepEqual(capped.TruncatedSections, []string{ReportTruncationIssues}) {
		t.Fatalf("TruncatedSections = %v, want [issues]", capped.TruncatedSections)
	}
}
