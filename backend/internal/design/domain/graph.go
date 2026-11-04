package design

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// graphHashPayload is the canonical projection of one version's design graph.
// Identity of the graph nodes plus current-version content hashes are enough to
// detect any write the model should re-read before proposing again.
type graphHashPayload struct {
	Requirements []graphHashRequirement `json:"requirements"`
}

type graphHashRequirement struct {
	RequirementID        int64               `json:"requirement_id"`
	RequirementVersionID int64               `json:"requirement_version_id"`
	Scenarios            []graphHashScenario `json:"scenarios"`
}

type graphHashScenario struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	SortOrder int             `json:"sort_order"`
	Cases     []graphHashCase `json:"cases"`
}

type graphHashCase struct {
	CaseID           int64  `json:"case_id"`
	CurrentVersionID int64  `json:"current_version_id"`
	Status           string `json:"status"`
	ContentHash      string `json:"content_hash"`
}

// CaseRunVerdict is the design projection of one case's latest execution
// result (spec §9.3). The design context never imports the execution context:
// it consumes the execution read port and maps the case_run status onto this
// local four-state enum.
type CaseRunVerdict string

const (
	CaseRunVerdictPassed       CaseRunVerdict = "passed"
	CaseRunVerdictFailed       CaseRunVerdict = "failed"
	CaseRunVerdictBlocked      CaseRunVerdict = "blocked"
	CaseRunVerdictInconclusive CaseRunVerdict = "inconclusive"
)

// CaseRunVerdictFromStatus maps a case_run status onto the four-state design
// verdict. Process states (pending/running/paused/skipped/interrupted/
// superseded) and unknown values carry no verdict, so they are inconclusive.
func CaseRunVerdictFromStatus(status string) CaseRunVerdict {
	switch verdict := CaseRunVerdict(status); verdict {
	case CaseRunVerdictPassed, CaseRunVerdictFailed, CaseRunVerdictBlocked, CaseRunVerdictInconclusive:
		return verdict
	default:
		return CaseRunVerdictInconclusive
	}
}

// ComputeGraphHash returns the stable sha256 of a design graph. Ordering from
// the database is normalized so the same graph always hashes identically.
func ComputeGraphHash(requirements []RequirementGraph) (string, error) {
	payload := graphHashPayload{Requirements: make([]graphHashRequirement, 0, len(requirements))}
	for _, requirement := range requirements {
		entry := graphHashRequirement{
			RequirementID:        requirement.Requirement.ID,
			RequirementVersionID: requirement.Requirement.CurrentVersionID,
			Scenarios:            make([]graphHashScenario, 0, len(requirement.Scenarios)),
		}
		for _, scenario := range requirement.Scenarios {
			scenarioEntry := graphHashScenario{
				ID:        scenario.Scenario.ID,
				Name:      scenario.Scenario.Name,
				SortOrder: scenario.Scenario.SortOrder,
				Cases:     make([]graphHashCase, 0, len(scenario.Cases)),
			}
			for _, item := range scenario.Cases {
				caseVersionID := item.Case.CurrentVersionID
				if caseVersionID == nil {
					caseVersionID = &item.Current.ID
				}
				scenarioEntry.Cases = append(scenarioEntry.Cases, graphHashCase{
					CaseID:           item.Case.ID,
					CurrentVersionID: *caseVersionID,
					Status:           item.Current.Status,
					ContentHash:      item.Current.ContentHash,
				})
			}
			sort.SliceStable(scenarioEntry.Cases, func(i, j int) bool {
				return scenarioEntry.Cases[i].CaseID < scenarioEntry.Cases[j].CaseID
			})
			entry.Scenarios = append(entry.Scenarios, scenarioEntry)
		}
		sort.SliceStable(entry.Scenarios, func(i, j int) bool {
			left, right := entry.Scenarios[i], entry.Scenarios[j]
			if left.SortOrder != right.SortOrder {
				return left.SortOrder < right.SortOrder
			}
			return left.ID < right.ID
		})
		payload.Requirements = append(payload.Requirements, entry)
	}
	sort.SliceStable(payload.Requirements, func(i, j int) bool {
		return payload.Requirements[i].RequirementID < payload.Requirements[j].RequirementID
	})
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
