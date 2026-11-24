package deliveryapp

import (
	"sort"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// ACCaseCoverage is one current confirmed case version's AC linkage and the
// terminal verdict of its latest non-superseded run (v0.2.4-G SC-G4). CoveredACs
// are the normalized covered acceptance-criterion ids (the design domain union
// is the single source of normalization); LatestVerdict is the execution terminal
// status, or "" when the case has no run yet.
type ACCaseCoverage struct {
	CaseVersionID int64
	CoveredACs    []string
	LatestVerdict string
}

// BuildACVerificationMatrix derives one requirement's per-AC verification rows
// (v0.2.4-G SC-G4 I1). For each acceptance criterion it gathers the covering
// confirmed cases (those whose covered_acs contains the AC id) and overlays their
// latest verdicts through the domain derivation rule: passed->verified, failed->
// failed, blocked/inconclusive/not-run->unverified, no covering case->uncovered.
// Link coverage (a covering case exists) is deliberately NOT treated as
// acceptance verification. AC ids are matched exactly; the caller normalizes them
// through the design domain union beforehand (no second normalization truth).
func BuildACVerificationMatrix(requirementID int64, criteria []delivery.ACCriterionSnapshot, cases []ACCaseCoverage) []delivery.ACVerificationSnapshot {
	rows := make([]delivery.ACVerificationSnapshot, 0, len(criteria))
	for _, criterion := range criteria {
		covering := make([]int64, 0)
		verdicts := make([]string, 0)
		for _, item := range cases {
			if !acIDSetContains(item.CoveredACs, criterion.ID) {
				continue
			}
			covering = append(covering, item.CaseVersionID)
			verdicts = append(verdicts, item.LatestVerdict)
		}
		sort.Slice(covering, func(i, j int) bool { return covering[i] < covering[j] })
		rows = append(rows, delivery.ACVerificationSnapshot{
			RequirementID:          requirementID,
			ACID:                   criterion.ID,
			Given:                  criterion.Given,
			When:                   criterion.When,
			Then:                   criterion.Then,
			State:                  delivery.DeriveACVerificationState(verdicts),
			CoveringCaseVersionIDs: covering,
		})
	}
	return rows
}

// SummarizeACCoverage aggregates the per-requirement AC verification matrices
// into the version-level AC coverage projection (v0.2.4-G SC-G4) and applies the
// M4 DISPLAY cap. The summary counts (Total/Verified/Unverified/Failed/Uncovered/
// LinkCovered) reflect the FULL AC set; the displayed ByRequirement rows are
// truncated to delivery.MaxReportACs total AC rows with an explicit annotation
// (Truncated/TotalBeforeCap), never silently dropped (art.7.6). Requirements are
// kept in ascending id order; within a requirement the AC rows keep their input
// (criterion) order.
func SummarizeACCoverage(byRequirement []delivery.RequirementACCoverageSnapshot) delivery.ACCoverageSnapshot {
	ordered := append([]delivery.RequirementACCoverageSnapshot(nil), byRequirement...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].RequirementID < ordered[j].RequirementID })
	summary := delivery.ACCoverageSnapshot{}
	for _, requirement := range ordered {
		for _, row := range requirement.ACs {
			summary.Total++
			switch row.State {
			case delivery.ACVerificationVerified:
				summary.Verified++
			case delivery.ACVerificationUnverified:
				summary.Unverified++
			case delivery.ACVerificationFailed:
				summary.Failed++
			case delivery.ACVerificationUncovered:
				summary.Uncovered++
			}
			if len(row.CoveringCaseVersionIDs) > 0 {
				summary.LinkCovered++
			}
		}
	}
	summary.TotalBeforeCap = summary.Total
	// Apply the display cap across requirements: keep whole AC rows until the
	// budget is spent, then stop (a requirement may be partially shown). The
	// counts above already describe the full set, so the reader sees the true
	// totals next to the truncation note. Displayed counts the rows actually
	// kept so the note states the displayed count, not the full Total (fix #2).
	budget := delivery.MaxReportACs
	capped := make([]delivery.RequirementACCoverageSnapshot, 0, len(ordered))
	for _, requirement := range ordered {
		if budget <= 0 {
			break
		}
		rows := requirement.ACs
		if len(rows) > budget {
			rows = rows[:budget]
			summary.Truncated = true
		}
		budget -= len(rows)
		summary.Displayed += len(rows)
		capped = append(capped, delivery.RequirementACCoverageSnapshot{
			RequirementID: requirement.RequirementID,
			ACs:           rows,
		})
	}
	if summary.Total > delivery.MaxReportACs {
		summary.Truncated = true
	}
	summary.ByRequirement = capped
	return summary
}

// acIDSetContains reports whether the normalized AC id set contains id.
func acIDSetContains(ids []string, id string) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}
