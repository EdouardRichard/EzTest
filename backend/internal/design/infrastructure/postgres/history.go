package designpg

import (
	"context"
	"fmt"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

// ReadCaseVersionsByIDs loads case versions by ID regardless of their
// current/confirmation status, scoped to one version through the case's
// scenario. It is the historical reader of the exploration chain (spec §8.1):
// superseded rows keep their title. The query is version scoped, so a case
// version of another version is never returned; missing IDs are absent from
// the result.
func (r *Repository) ReadCaseVersionsByIDs(ctx context.Context, versionID int64, caseVersionIDs []int64) ([]design.CaseVersionSummary, error) {
	summaries := make([]design.CaseVersionSummary, 0, len(caseVersionIDs))
	if versionID <= 0 || len(caseVersionIDs) == 0 {
		return summaries, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT cv.id, cv.revision, cv.status, cv.title, cv.created_at, cc.confirmed_at
		FROM case_version cv
		JOIN case_entity ce ON ce.id = cv.case_id
		JOIN case_scenario s ON s.id = ce.scenario_id
		LEFT JOIN case_confirmation cc ON cc.case_version_id = cv.id
		WHERE s.version_id = $1 AND cv.id = ANY($2)
		ORDER BY cv.id
	`, versionID, caseVersionIDs)
	if err != nil {
		return nil, fmt.Errorf("read historical case versions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var summary design.CaseVersionSummary
		if err := rows.Scan(
			&summary.ID, &summary.Revision, &summary.Status, &summary.Title,
			&summary.CreatedAt, &summary.ConfirmedAt,
		); err != nil {
			return nil, fmt.Errorf("scan historical case version: %w", err)
		}
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan historical case versions: %w", err)
	}
	return summaries, nil
}
