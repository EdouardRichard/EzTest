package designpg

import (
	"context"
	"fmt"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ designapp.QueryReader = (*QueryReader)(nil)

// QueryReader is the design context's P6 read-only source. It only reads the
// design-owned case tables plus the requirement projection the design context
// already uses for the pending-design count; the version title is resolved by
// the consumer through the version application port, never by a cross-context
// join.
type QueryReader struct {
	pool *pgxpool.Pool
}

// NewQueryReader wraps a pgx pool. A nil pool is a wiring error.
func NewQueryReader(pool *pgxpool.Pool) (*QueryReader, error) {
	if pool == nil {
		return nil, fmt.Errorf("design query reader: nil pool")
	}
	return &QueryReader{pool: pool}, nil
}

// CountCaseConfirmation returns current confirmed case versions over current
// case versions in draft+confirmed+stale, for one version when versionID is
// non-nil and globally otherwise. Cases without a current version are outside
// the denominator.
func (r *QueryReader) CountCaseConfirmation(ctx context.Context, versionID *int64) (int, int, error) {
	var num, denom int
	if err := r.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE cv.status = 'confirmed'),
			count(*) FILTER (WHERE cv.status IN ('draft', 'confirmed', 'stale'))
		FROM case_entity ce
		JOIN case_version cv ON cv.id = ce.current_version_id
		JOIN case_scenario s ON s.id = ce.scenario_id
		WHERE $1::bigint IS NULL OR s.version_id = $1
	`, versionID).Scan(&num, &denom); err != nil {
		return 0, 0, fmt.Errorf("count case confirmation: %w", err)
	}
	return num, denom, nil
}

// SearchCases returns current case-version title matches, newest first. The
// historical revisions of a case never match: only the current revision is
// read.
func (r *QueryReader) SearchCases(ctx context.Context, q string, limit int) ([]designapp.CaseSearchRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT ce.id, cv.id, s.version_id, cv.title, cv.status, ce.updated_at
		FROM case_entity ce
		JOIN case_version cv ON cv.id = ce.current_version_id
		JOIN case_scenario s ON s.id = ce.scenario_id
		WHERE cv.title ILIKE '%' || $1 || '%'
		ORDER BY ce.updated_at DESC, ce.id DESC
		LIMIT $2
	`, q, limit)
	if err != nil {
		return nil, fmt.Errorf("search cases: %w", err)
	}
	defer rows.Close()

	items := make([]designapp.CaseSearchRow, 0)
	for rows.Next() {
		var item designapp.CaseSearchRow
		if err := rows.Scan(&item.CaseID, &item.CaseVersionID, &item.VersionID,
			&item.Title, &item.Status, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan case search row: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan case search rows: %w", err)
	}
	return items, nil
}

// ListDraftCases returns the current draft cases, newest first.
func (r *QueryReader) ListDraftCases(ctx context.Context, limit int) ([]designapp.DraftCaseRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT ce.id, s.version_id, cv.title, ce.created_at
		FROM case_entity ce
		JOIN case_version cv ON cv.id = ce.current_version_id
		JOIN case_scenario s ON s.id = ce.scenario_id
		WHERE ce.status = 'draft'
		ORDER BY ce.created_at DESC, ce.id DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list draft cases: %w", err)
	}
	defer rows.Close()

	items := make([]designapp.DraftCaseRow, 0)
	for rows.Next() {
		var item designapp.DraftCaseRow
		if err := rows.Scan(&item.CaseID, &item.VersionID,
			&item.Title, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan draft case: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan draft cases: %w", err)
	}
	return items, nil
}

// PendingDesignCounts returns, for every requested version, the number of
// clarified requirements without an active design scope. Versions with no
// pending requirement are omitted from the map. This mirrors the existing
// design-context pending count so the version reader can build design_pending
// todos without joining the design tables.
func (r *QueryReader) PendingDesignCounts(ctx context.Context, versionIDs []int64) (map[int64]int, error) {
	counts := make(map[int64]int, len(versionIDs))
	if len(versionIDs) == 0 {
		return counts, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT r.version_id, count(*)
		FROM requirement r
		WHERE r.version_id = ANY($1)
		  AND r.status = 'clarified'
		  AND NOT EXISTS (
		      SELECT 1 FROM design_scope ds
		      WHERE ds.version_id = r.version_id
		        AND ds.requirement_id = r.id
		        AND ds.status = 'active'
		  )
		GROUP BY r.version_id
	`, versionIDs)
	if err != nil {
		return nil, fmt.Errorf("count pending design: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var versionID int64
		var count int
		if err := rows.Scan(&versionID, &count); err != nil {
			return nil, fmt.Errorf("scan pending design count: %w", err)
		}
		counts[versionID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan pending design counts: %w", err)
	}
	return counts, nil
}
