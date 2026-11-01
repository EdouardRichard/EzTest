package visualpg

import (
	"context"

	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
)

// DiffRepository implements visualapp.DiffRepository.
type DiffRepository struct{ base }

const diffColumns = `d.id, d.visual_assertion_id, d.case_run_id, d.assertion_result_id,
	d.baseline_id, d.actual_evidence_id, d.diff_evidence_id, d.diff_ratio::float8,
	d.threshold::float8, d.classification, d.reason, d.environment_match, d.width, d.height,
	d.mismatch_boxes, d.created_at, d.updated_at`

// Insert appends one comparison. The unique assertion_result_id makes replay
// idempotent: the existing row is returned with created=false.
func (r *DiffRepository) Insert(ctx context.Context, diff visual.Diff) (visual.Diff, bool, error) {
	ctx = contextOrBackground(ctx)
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO visual_diff (id, visual_assertion_id, case_run_id, assertion_result_id,
			baseline_id, actual_evidence_id, diff_evidence_id, diff_ratio, threshold,
			classification, reason, environment_match, width, height, mismatch_boxes,
			created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::jsonb,
			$16, $17)
		ON CONFLICT (assertion_result_id) DO NOTHING
	`, diff.ID, diff.VisualAssertionID, diff.CaseRunID, diff.AssertionResultID, diff.BaselineID,
		diff.ActualEvidenceID, diff.DiffEvidenceID, diff.DiffRatio, diff.Threshold,
		string(diff.Classification), diff.Reason, diff.EnvironmentMatch, diff.Width,
		diff.Height, jsonOrEmptyArray(diff.MismatchBoxes), diff.CreatedAt, diff.UpdatedAt)
	if err != nil {
		return visual.Diff{}, false, wrapDB("insert diff", err)
	}
	if tag.RowsAffected() > 0 {
		return diff, true, nil
	}
	existing, found, err := r.ByAssertionResult(ctx, diff.AssertionResultID)
	if err != nil {
		return visual.Diff{}, false, err
	}
	if !found {
		return visual.Diff{}, false, visual.ErrStateConflict
	}
	return existing, false, nil
}

// Get returns one comparison.
func (r *DiffRepository) Get(ctx context.Context, id int64) (visual.Diff, error) {
	ctx = contextOrBackground(ctx)
	row := r.pool.QueryRow(ctx, `
		SELECT `+diffColumns+` FROM visual_diff d WHERE d.id = $1
	`, id)
	diff, err := scanDiff(row)
	if err != nil {
		return visual.Diff{}, wrapDB("get diff", err)
	}
	return diff, nil
}

// List returns one descending-ID page of the comparisons of a version.
func (r *DiffRepository) List(ctx context.Context, versionID int64, filter visual.DiffFilter) ([]visual.Diff, bool, error) {
	ctx = contextOrBackground(ctx)
	limit := filter.NormalizedLimit()
	rows, err := r.pool.Query(ctx, `
		SELECT `+diffColumns+`
		FROM visual_diff d JOIN visual_assertion a ON a.id = d.visual_assertion_id
		WHERE a.version_id = $1
		  AND ($2::bigint IS NULL OR d.case_run_id = $2)
		  AND ($3::bigint IS NULL OR a.case_version_id = $3)
		  AND ($4::text IS NULL OR d.classification = $4)
		  AND ($5::bigint IS NULL OR d.id < $5)
		ORDER BY d.id DESC LIMIT $6
	`, versionID, filter.CaseRunID, filter.CaseVersionID, nullableString(filter.Classification), filter.BeforeID, limit+1)
	if err != nil {
		return nil, false, wrapDB("list diffs", err)
	}
	defer rows.Close()
	list := make([]visual.Diff, 0, limit)
	for rows.Next() {
		diff, scanErr := scanDiff(rows)
		if scanErr != nil {
			return nil, false, wrapDB("scan diff", scanErr)
		}
		list = append(list, diff)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(list) > limit
	if hasMore {
		list = list[:limit]
	}
	return list, hasMore, nil
}

// ByAssertionResult returns the comparison of one assertion result.
func (r *DiffRepository) ByAssertionResult(ctx context.Context, assertionResultID int64) (visual.Diff, bool, error) {
	ctx = contextOrBackground(ctx)
	row := r.pool.QueryRow(ctx, `
		SELECT `+diffColumns+` FROM visual_diff d WHERE d.assertion_result_id = $1
	`, assertionResultID)
	diff, err := scanDiff(row)
	if err != nil {
		if err == visual.ErrNotFound {
			return visual.Diff{}, false, nil
		}
		return visual.Diff{}, false, wrapDB("get diff by assertion result", err)
	}
	return diff, true, nil
}

func scanDiff(row rowScanner) (visual.Diff, error) {
	var diff visual.Diff
	var classification string
	var boxes []byte
	err := row.Scan(&diff.ID, &diff.VisualAssertionID, &diff.CaseRunID, &diff.AssertionResultID,
		&diff.BaselineID, &diff.ActualEvidenceID, &diff.DiffEvidenceID, &diff.DiffRatio,
		&diff.Threshold, &classification, &diff.Reason, &diff.EnvironmentMatch,
		&diff.Width, &diff.Height, &boxes, &diff.CreatedAt, &diff.UpdatedAt)
	if err != nil {
		return visual.Diff{}, err
	}
	diff.Classification = visual.Classification(classification)
	diff.MismatchBoxes = jsonOrEmptyArray(boxes)
	return diff, nil
}

func jsonOrEmptyArray(raw []byte) []byte {
	if len(raw) == 0 {
		return []byte(`[]`)
	}
	return raw
}

func nullableString(value visual.Classification) *string {
	if value == "" {
		return nil
	}
	text := string(value)
	return &text
}
