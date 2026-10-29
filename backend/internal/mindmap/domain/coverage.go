package domain

import "sort"

// CoverageCell is one row of the requirement coverage matrix: the confirmed
// requirement and the case nodes covering it, both ascending.
type CoverageCell struct {
	RequirementID int64
	CaseIDs       []int64
}

// Coverage is the requirement <-> case coverage view rendered by GET /mindmap
// (spec §7.3). RequirementIDs lists every confirmed requirement of the version;
// StaleRequirementIDs lists requirements still referenced by a case but no
// longer confirmed (reopened); Matrix contains only covered requirements.
type Coverage struct {
	RequirementIDs          []int64
	UncoveredRequirementIDs []int64
	StaleRequirementIDs     []int64
	Matrix                  []CoverageCell
}

// BuildCoverage computes the coverage matrix of spec §6.2. confirmed maps every
// requirement of the version to whether it is still confirmed (clarified). Only
// requirement sources attached to case nodes of versionID count: scenario
// sources and sources of other versions are ignored, so a scenario never covers
// a requirement.
func BuildCoverage(versionID int64, confirmed map[int64]bool, nodes []CaseNode, sources []CaseSource) Coverage {
	nodesByID := make(map[int64]CaseNode, len(nodes))
	for _, node := range nodes {
		nodesByID[node.ID] = node
	}

	covered := make(map[int64]map[int64]struct{})
	stale := make(map[int64]struct{})
	for _, source := range sources {
		if source.VersionID != versionID || source.RefKind != RefRequirement || source.RefID == nil {
			continue
		}
		node, ok := nodesByID[source.NodeID]
		if !ok || node.VersionID != versionID || node.Kind != NodeKindCase {
			continue
		}
		requirementID := *source.RefID
		if !confirmed[requirementID] {
			stale[requirementID] = struct{}{}
			continue
		}
		if covered[requirementID] == nil {
			covered[requirementID] = make(map[int64]struct{})
		}
		covered[requirementID][node.ID] = struct{}{}
	}

	requirementIDs := make([]int64, 0, len(confirmed))
	for requirementID := range confirmed {
		requirementIDs = append(requirementIDs, requirementID)
	}
	sort.Slice(requirementIDs, func(i, j int) bool { return requirementIDs[i] < requirementIDs[j] })

	coverage := Coverage{
		RequirementIDs:          requirementIDs,
		UncoveredRequirementIDs: make([]int64, 0),
		StaleRequirementIDs:     make([]int64, 0, len(stale)),
		Matrix:                  make([]CoverageCell, 0, len(covered)),
	}
	for _, requirementID := range requirementIDs {
		if confirmed[requirementID] && len(covered[requirementID]) == 0 {
			coverage.UncoveredRequirementIDs = append(coverage.UncoveredRequirementIDs, requirementID)
		}
	}
	for requirementID := range stale {
		coverage.StaleRequirementIDs = append(coverage.StaleRequirementIDs, requirementID)
	}
	sort.Slice(coverage.StaleRequirementIDs, func(i, j int) bool {
		return coverage.StaleRequirementIDs[i] < coverage.StaleRequirementIDs[j]
	})

	matrixIDs := make([]int64, 0, len(covered))
	for requirementID := range covered {
		matrixIDs = append(matrixIDs, requirementID)
	}
	sort.Slice(matrixIDs, func(i, j int) bool { return matrixIDs[i] < matrixIDs[j] })
	for _, requirementID := range matrixIDs {
		caseIDs := make([]int64, 0, len(covered[requirementID]))
		for caseID := range covered[requirementID] {
			caseIDs = append(caseIDs, caseID)
		}
		sort.Slice(caseIDs, func(i, j int) bool { return caseIDs[i] < caseIDs[j] })
		coverage.Matrix = append(coverage.Matrix, CoverageCell{RequirementID: requirementID, CaseIDs: caseIDs})
	}
	return coverage
}
