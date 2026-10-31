package executionpg

import (
	"context"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/jackc/pgx/v5"
)

const intentColumns = `id, version_id, case_version_id, version_sut_ids, environment_fingerprint,
	code_baseline, purpose, status, dedupe_key, attempt_no, created_by, created_at, updated_at`

// IntentRepository implements executionapp.IntentRepository.
type IntentRepository struct{ base }

// Create inserts one execution intent.
func (r *IntentRepository) Create(ctx context.Context, intent execution.ExecutionIntent) error {
	ctx = contextOrBackground(ctx)
	sutIDs, err := encodeJSONArray("version_sut_ids", intent.VersionSUTIDs)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO execution_intent (id, version_id, case_version_id, version_sut_ids,
			environment_fingerprint, code_baseline, purpose, status, dedupe_key, attempt_no, created_by)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8, $9, $10, $11)
	`, r.insertID(intent.ID), intent.VersionID, intent.CaseVersionID, sutIDs,
		intent.EnvironmentFingerprint, intent.CodeBaseline, string(intent.Purpose), string(intent.Status),
		intent.DedupeKey, intent.AttemptNo, nullableID(intent.CreatedBy))
	return wrapDB("insert execution intent", err)
}

// Get loads one intent.
func (r *IntentRepository) Get(ctx context.Context, id int64) (execution.ExecutionIntent, error) {
	ctx = contextOrBackground(ctx)
	row, err := scanIntent(r.pool.QueryRow(ctx, `SELECT `+intentColumns+` FROM execution_intent WHERE id = $1`, id))
	if err != nil {
		return execution.ExecutionIntent{}, wrapDB("get execution intent", err)
	}
	return row, nil
}

// List returns one descending-ID page of a version's intents.
func (r *IntentRepository) List(ctx context.Context, versionID int64, status string, limit int, beforeID *int64) ([]execution.ExecutionIntent, bool, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+intentColumns+` FROM execution_intent
		WHERE version_id = $1
		  AND ($2 = '' OR status = $2)
		  AND ($3::BIGINT IS NULL OR id < $3)
		ORDER BY id DESC
		LIMIT $4
	`, versionID, status, beforeID, fetchLimit(limit))
	if err != nil {
		return nil, false, wrapDB("list execution intents", err)
	}
	defer rows.Close()
	items, err := collectIntents(rows)
	if err != nil {
		return nil, false, err
	}
	return pageOfIntents(items, limit)
}

// ListOpen returns open intents. versionID <= 0 means every version.
func (r *IntentRepository) ListOpen(ctx context.Context, versionID int64) ([]execution.ExecutionIntent, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+intentColumns+` FROM execution_intent
		WHERE status = 'open' AND ($1::BIGINT <= 0 OR version_id = $1)
		ORDER BY id
	`, versionID)
	if err != nil {
		return nil, wrapDB("list open intents", err)
	}
	defer rows.Close()
	return collectIntents(rows)
}

// ListActive returns open/claimed/running intents. versionID <= 0 means every
// version.
func (r *IntentRepository) ListActive(ctx context.Context, versionID int64) ([]execution.ExecutionIntent, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+intentColumns+` FROM execution_intent
		WHERE status IN ('open', 'claimed', 'running') AND ($1::BIGINT <= 0 OR version_id = $1)
		ORDER BY id
	`, versionID)
	if err != nil {
		return nil, wrapDB("list active intents", err)
	}
	defer rows.Close()
	return collectIntents(rows)
}

// Transition applies a guarded status change. A stale row is a no-op.
func (r *IntentRepository) Transition(ctx context.Context, id int64, from []execution.IntentStatus, to execution.IntentStatus) (bool, error) {
	ctx = contextOrBackground(ctx)
	tag, err := r.pool.Exec(ctx, `
		UPDATE execution_intent SET status = $3, updated_at = now()
		WHERE id = $1 AND status = ANY($2::text[])
	`, id, statusStrings(from), string(to))
	if err != nil {
		return false, wrapDB("transition execution intent", err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	return false, r.existsOrNotFound(ctx, "execution_intent", id, "transition execution intent")
}

// CountInitialFor counts stored initial intents for the natural key.
func (r *IntentRepository) CountInitialFor(ctx context.Context, caseVersionID int64, fingerprint, baseline string) (int, error) {
	ctx = contextOrBackground(ctx)
	var count int
	err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM execution_intent
		WHERE case_version_id = $1 AND environment_fingerprint = $2 AND code_baseline = $3
		  AND purpose = 'initial'
	`, caseVersionID, fingerprint, baseline).Scan(&count)
	if err != nil {
		return 0, wrapDB("count initial intents", err)
	}
	return count, nil
}

func scanIntent(row rowScanner) (execution.ExecutionIntent, error) {
	var intent execution.ExecutionIntent
	var sutIDsRaw []byte
	var purpose, status string
	var createdBy *int64
	err := row.Scan(&intent.ID, &intent.VersionID, &intent.CaseVersionID, &sutIDsRaw,
		&intent.EnvironmentFingerprint, &intent.CodeBaseline, &purpose, &status,
		&intent.DedupeKey, &intent.AttemptNo, &createdBy, &intent.CreatedAt, &intent.UpdatedAt)
	if err != nil {
		return execution.ExecutionIntent{}, err
	}
	intent.Purpose = execution.Purpose(purpose)
	intent.Status = execution.IntentStatus(status)
	intent.CreatedBy = derefInt64(createdBy)
	if err := decodeJSONObject("version_sut_ids", sutIDsRaw, &intent.VersionSUTIDs); err != nil {
		return execution.ExecutionIntent{}, err
	}
	return intent, nil
}

func collectIntents(rows pgx.Rows) ([]execution.ExecutionIntent, error) {
	items := make([]execution.ExecutionIntent, 0)
	for rows.Next() {
		item, err := scanIntent(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// pageOfIntents trims the sentinel row and reports has_more.
func pageOfIntents(items []execution.ExecutionIntent, limit int) ([]execution.ExecutionIntent, bool, error) {
	limit = effectiveLimit(limit)
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}
