package designpg

import (
	"context"
	"errors"
	"fmt"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/jackc/pgx/v5"
)

// executableCaseSelect is the current+confirmed guard of P3 §3.4 invariant 1:
// the case version is the case entity's current version and confirmed, the
// scenario belongs to the requested version and still targets the current
// requirement revision.
const executableCaseSelect = `
	WHERE cv.status = 'confirmed'
	  AND entity.status = 'confirmed'
	  AND entity.current_version_id = cv.id
	  AND entity.scenario_id = scenario.id
	  AND scenario.requirement_version_id = requirement.current_version_id
`

// ReadExecutableCase loads one current+confirmed case version for execution.
func (r *Repository) ReadExecutableCase(ctx context.Context, versionID, caseVersionID int64) (design.ExecutableCase, error) {
	var caseID, requirementID, requirementVersionID int64
	err := r.pool.QueryRow(ctx, `
		SELECT cv.case_id, scenario.requirement_id, requirement.current_version_id
		FROM case_version cv
		JOIN case_entity entity ON entity.id = cv.case_id
		JOIN case_scenario scenario ON scenario.id = entity.scenario_id AND scenario.version_id = $1
		JOIN requirement requirement ON requirement.id = scenario.requirement_id
			AND requirement.version_id = scenario.version_id
		`+executableCaseSelect+`
		  AND cv.id = $2
	`, versionID, caseVersionID).Scan(&caseID, &requirementID, &requirementVersionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return design.ExecutableCase{}, design.ErrNotFound
	}
	if err != nil {
		return design.ExecutableCase{}, fmt.Errorf("read executable case version: %w", err)
	}
	version, err := loadCaseVersion(ctx, r.pool, caseVersionID)
	if err != nil {
		return design.ExecutableCase{}, err
	}
	deriveAssertionSeq(&version)
	return design.ExecutableCase{
		CaseID:               caseID,
		RequirementID:        requirementID,
		RequirementVersionID: requirementVersionID,
		Version:              version,
	}, nil
}

// ListExecutableCases lists every current+confirmed case version of a version.
func (r *Repository) ListExecutableCases(ctx context.Context, versionID int64) ([]design.ExecutableCase, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT cv.id, cv.case_id, scenario.requirement_id, requirement.current_version_id
		FROM case_version cv
		JOIN case_entity entity ON entity.id = cv.case_id
		JOIN case_scenario scenario ON scenario.id = entity.scenario_id AND scenario.version_id = $1
		JOIN requirement requirement ON requirement.id = scenario.requirement_id
			AND requirement.version_id = scenario.version_id
		`+executableCaseSelect+`
		ORDER BY cv.id
	`, versionID)
	if err != nil {
		return nil, fmt.Errorf("list executable case versions: %w", err)
	}
	defer rows.Close()

	type row struct {
		caseVersionID        int64
		caseID               int64
		requirementID        int64
		requirementVersionID int64
	}
	selected := make([]row, 0)
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.caseVersionID, &item.caseID, &item.requirementID, &item.requirementVersionID); err != nil {
			return nil, fmt.Errorf("scan executable case version: %w", err)
		}
		selected = append(selected, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list executable case versions: %w", err)
	}

	cases := make([]design.ExecutableCase, 0, len(selected))
	for _, item := range selected {
		version, err := loadCaseVersion(ctx, r.pool, item.caseVersionID)
		if err != nil {
			return nil, err
		}
		deriveAssertionSeq(&version)
		cases = append(cases, design.ExecutableCase{
			CaseID:               item.caseID,
			RequirementID:        item.requirementID,
			RequirementVersionID: item.requirementVersionID,
			Version:              version,
		})
	}
	return cases, nil
}

// deriveAssertionSeq assigns the 1-based assertion_seq of P3-D-06: within one
// step, ascending case_assertion ID. loadAssertions already orders by
// (step_seq, id), so a single pass is enough.
func deriveAssertionSeq(version *design.CaseVersion) {
	stepSeq := 0
	seq := 0
	for index := range version.Assertions {
		assertion := &version.Assertions[index]
		if assertion.StepSeq != stepSeq {
			stepSeq = assertion.StepSeq
			seq = 0
		}
		seq++
		assertion.AssertionSeq = seq
	}
}
