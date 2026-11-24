package executionpg

import (
	"context"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// SUTResolutionRepository persists the append-only sut_resolution fact (v0.2.4-F
// P16, SC-F2). It exposes only Append/List — never Update/Delete (append-only
// fact, constitution art.5.7; the version_fact / sut_registration precedent).
// Resolving the same case_run again appends a second row (latest wins); there is
// deliberately no unique owner key.
type SUTResolutionRepository struct{ base }

var _ execution.SUTResolutionRepository = (*SUTResolutionRepository)(nil)

const sutResolutionColumns = `id, case_run_id, sut_ids, resolution_method,
	touched_page_ids, touched_api_ids, ambiguous, created_at, created_by`

// AppendSUTResolution inserts one resolution fact. The BIGINT[] id arrays are
// logical references into the resource context (no cross-context FK); the
// application validates their existence before appending (server-truth). The
// case_run_id is a NOT NULL within-context reference guarded by a real FK.
func (r *SUTResolutionRepository) AppendSUTResolution(ctx context.Context, resolution execution.SUTResolution) (execution.SUTResolution, error) {
	resolution.Normalize()
	if err := resolution.Validate(); err != nil {
		return execution.SUTResolution{}, err
	}
	resolution.ID = r.insertID(resolution.ID)
	row := r.pool.QueryRow(ctx, `
		INSERT INTO sut_resolution (id, case_run_id, sut_ids, resolution_method, touched_page_ids, touched_api_ids, ambiguous, created_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, COALESCE($8, now()), $9)
		RETURNING created_at
	`, resolution.ID, resolution.CaseRunID, resolution.SUTIDs, resolution.ResolutionMethod,
		resolution.TouchedPageIDs, resolution.TouchedAPIIDs, resolution.Ambiguous, nullableTime(resolution.CreatedAt), resolution.CreatedBy)
	if err := row.Scan(&resolution.CreatedAt); err != nil {
		return execution.SUTResolution{}, wrapDB("append sut resolution", err)
	}
	return resolution, nil
}

// ListSUTResolutions returns one case_run's resolution facts in append order
// (id ascending); the caller takes the latest as the effective resolution. It is
// the production read path of the fact (the resolution service's idempotency
// check and the knowledge-dimension projection).
func (r *SUTResolutionRepository) ListSUTResolutions(ctx context.Context, caseRunID int64) ([]execution.SUTResolution, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+sutResolutionColumns+`
		FROM sut_resolution
		WHERE case_run_id = $1
		ORDER BY id ASC
	`, caseRunID)
	if err != nil {
		return nil, wrapDB("list sut resolutions", err)
	}
	defer rows.Close()
	items := make([]execution.SUTResolution, 0)
	for rows.Next() {
		var item execution.SUTResolution
		if err := rows.Scan(&item.ID, &item.CaseRunID, &item.SUTIDs, &item.ResolutionMethod,
			&item.TouchedPageIDs, &item.TouchedAPIIDs, &item.Ambiguous, &item.CreatedAt, &item.CreatedBy); err != nil {
			return nil, wrapDB("scan sut resolution", err)
		}
		item.Normalize()
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list sut resolutions", err)
	}
	return items, nil
}
