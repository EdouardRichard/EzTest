package domain

import (
	"reflect"
	"testing"
)

func TestBuildCoverageMarksUncoveredAndStale(t *testing.T) {
	versionID := int64(7)
	nodes := []CaseNode{
		{ID: 1, VersionID: versionID, Kind: NodeKindCase},
		{ID: 2, VersionID: versionID, Kind: NodeKindScenario},
		{ID: 3, VersionID: 99, Kind: NodeKindCase},
	}
	// Requirement 100 and 200 are confirmed, 300 was reopened (stale), and the
	// scenario source and the other-version source must not count as coverage.
	confirmed := map[int64]bool{100: true, 200: true, 300: false}
	sources := []CaseSource{
		{ID: 10, VersionID: versionID, NodeID: 1, RefKind: RefRequirement, RefID: int64Ptr(100)},
		{ID: 11, VersionID: versionID, NodeID: 1, RefKind: RefRequirement, RefID: int64Ptr(300)},
		{ID: 12, VersionID: versionID, NodeID: 2, RefKind: RefRequirement, RefID: int64Ptr(200)},
		{ID: 13, VersionID: 99, NodeID: 3, RefKind: RefRequirement, RefID: int64Ptr(200)},
	}

	coverage := BuildCoverage(versionID, confirmed, nodes, sources)

	if want := []int64{100, 200, 300}; !reflect.DeepEqual(coverage.RequirementIDs, want) {
		t.Errorf("RequirementIDs = %v, want %v", coverage.RequirementIDs, want)
	}
	if want := []int64{200}; !reflect.DeepEqual(coverage.UncoveredRequirementIDs, want) {
		t.Errorf("UncoveredRequirementIDs = %v, want %v", coverage.UncoveredRequirementIDs, want)
	}
	if want := []int64{300}; !reflect.DeepEqual(coverage.StaleRequirementIDs, want) {
		t.Errorf("StaleRequirementIDs = %v, want %v", coverage.StaleRequirementIDs, want)
	}
	if want := []CoverageCell{{RequirementID: 100, CaseIDs: []int64{1}}}; !reflect.DeepEqual(coverage.Matrix, want) {
		t.Errorf("Matrix = %+v, want %+v", coverage.Matrix, want)
	}
}

func TestBuildCoverageDedupesCaseIDs(t *testing.T) {
	versionID := int64(7)
	nodes := []CaseNode{
		{ID: 2, VersionID: versionID, Kind: NodeKindCase},
		{ID: 1, VersionID: versionID, Kind: NodeKindCase},
	}
	confirmed := map[int64]bool{100: true}
	sources := []CaseSource{
		// Two source rows for the same case/requirement must collapse to one
		// case id (a source may legitimately repeat via ref_key).
		{ID: 10, VersionID: versionID, NodeID: 2, RefKind: RefRequirement, RefID: int64Ptr(100)},
		{ID: 11, VersionID: versionID, NodeID: 2, RefKind: RefRequirement, RefID: int64Ptr(100)},
		{ID: 12, VersionID: versionID, NodeID: 1, RefKind: RefRequirement, RefID: int64Ptr(100)},
	}

	coverage := BuildCoverage(versionID, confirmed, nodes, sources)

	if want := []CoverageCell{{RequirementID: 100, CaseIDs: []int64{1, 2}}}; !reflect.DeepEqual(coverage.Matrix, want) {
		t.Errorf("Matrix = %+v, want %+v", coverage.Matrix, want)
	}
	if len(coverage.UncoveredRequirementIDs) != 0 {
		t.Errorf("UncoveredRequirementIDs = %v, want none", coverage.UncoveredRequirementIDs)
	}
	if len(coverage.StaleRequirementIDs) != 0 {
		t.Errorf("StaleRequirementIDs = %v, want none", coverage.StaleRequirementIDs)
	}
}
