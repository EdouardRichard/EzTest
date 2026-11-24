package executionpg

import (
	"context"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5"
)

const intentColumns = `id, version_id, case_version_id, sut_ids, environment_fingerprint,
	code_baseline, purpose, status, dedupe_key, attempt_no, created_by, created_at, updated_at`

// intentActiveStatuses is the active set of the partial unique index
// uk_execution_intent_active (open/claimed/running), derived from the domain
// IntentStatus constants. IntentStatus.Active() is the authority; the
// membership is pinned against every domain constant by
// TestIntentDedupActiveSetMatchesDomain so a new/changed active status cannot
// silently drift from the dedup pre-check.
var intentActiveStatuses = statusStrings([]execution.IntentStatus{
	execution.IntentOpen, execution.IntentClaimed, execution.IntentRunning,
})

// IntentRepository implements executionapp.IntentRepository.
type IntentRepository struct{ base }

// Create inserts one execution intent. It runs in a transaction whose first
// lock is the version-scoped advisory lock shared with version close and
// requirement closure. A close/closure that committed while this transaction
// waited is visible to the guards below, so the insert can never land behind a
// stale recheck. Conflicts resolve before the reopen signal (R-G9): a duplicate
// natural key yields ErrDuplicate and a closed requirement yields
// ErrRequirementClosed (both terminal for this insert, never a reopen trigger);
// only a genuinely new, non-conflicting intent on a completed version yields
// ErrVersionCompleted (the intent service reopens and retries through the P4
// reopen path).
//
// The duplicate conflict is PER-VERSION, not global: the natural key embeds
// case_version_id (a case version belongs to exactly one version), so an active
// intent occupying the slot is necessarily this version's own intent. A
// duplicate therefore never collides across versions and never reopens any
// version — the dedup pre-check deliberately scopes by case version rather than
// adding a redundant version_id predicate.
func (r *IntentRepository) Create(ctx context.Context, intent execution.ExecutionIntent) error {
	ctx = contextOrBackground(ctx)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return wrapDB("insert execution intent: begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := r.insertIntentInTx(ctx, tx, intent); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return wrapDB("insert execution intent: commit", err)
	}
	return nil
}

// CreateAndTransition creates the intent and applies the guarded transition in
// the SAME transaction (the version advisory lock and scope guards are shared
// with Create): the intermediate open row is never visible to a concurrent
// planner, and a failed transition rolls the insert back — no orphan open
// intent. The transition keeps the state machine's from->to guard.
func (r *IntentRepository) CreateAndTransition(ctx context.Context, intent execution.ExecutionIntent, from []execution.IntentStatus, to execution.IntentStatus) error {
	ctx = contextOrBackground(ctx)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return wrapDB("insert execution intent: begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := r.insertIntentInTx(ctx, tx, intent); err != nil {
		return err
	}
	ok, err := transitionIntent(ctx, tx, intent.ID, from, to)
	if err != nil {
		return err
	}
	if !ok {
		return execution.ErrStateConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return wrapDB("insert execution intent: commit", err)
	}
	return nil
}

// transitionIntent is the ONE guarded execution_intent status-transition
// statement, shared by Transition (pool) and CreateAndTransition (tx) so the
// from->to guard can never drift between the two copies (constitution
// art.5.3/5.4). It reports whether a row transitioned; the caller maps a miss
// to its own conflict semantics.
func transitionIntent(ctx context.Context, exec sqlExecer, id int64, from []execution.IntentStatus, to execution.IntentStatus) (bool, error) {
	tag, err := exec.Exec(ctx, `
		UPDATE execution_intent SET status = $3, updated_at = now()
		WHERE id = $1 AND status = ANY($2::text[])
	`, id, statusStrings(from), string(to))
	if err != nil {
		return false, wrapDB("transition execution intent", err)
	}
	return tag.RowsAffected() > 0, nil
}

// insertIntentInTx locks the version, resolves conflicts before the reopen
// signal (guardIntentScope, R-G9) and inserts the intent row inside the
// caller's transaction.
func (r *IntentRepository) insertIntentInTx(ctx context.Context, tx pgx.Tx, intent execution.ExecutionIntent) error {
	sutIDs, err := encodeJSONArray("sut_ids", intent.SUTIDs)
	if err != nil {
		return err
	}
	if err := lockVersionTx(ctx, tx, intent.VersionID, "insert execution intent"); err != nil {
		return err
	}
	if err := guardIntentScope(ctx, tx, intent); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO execution_intent (id, version_id, case_version_id, sut_ids,
			environment_fingerprint, code_baseline, purpose, status, dedupe_key, attempt_no, created_by)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8, $9, $10, $11)
	`, r.insertID(intent.ID), intent.VersionID, intent.CaseVersionID, sutIDs,
		intent.EnvironmentFingerprint, intent.CodeBaseline, string(intent.Purpose), string(intent.Status),
		intent.DedupeKey, intent.AttemptNo, nullableID(intent.CreatedBy)); err != nil {
		return wrapDB("insert execution intent", err)
	}
	return nil
}

// guardIntentScope re-checks the intent conflicts and the version/requirement
// state while the version advisory lock is held. It intentionally reads the
// version/design tables directly: only a check in this transaction can see a
// close/closure that committed while the lock wait was in progress. Missing
// rows are allowed (the intent table only carries logical references; the
// service validates the case/version before calling the repository).
//
// ORDERING INVARIANT (v0.2.4-G R-G9, SC-G12): conflicts resolve BEFORE the
// completed-version reopen signal. The service answers ErrVersionCompleted
// with Reopen + retry, so reporting it for an intent that can never be created
// (a duplicate natural key, a closed requirement) would mis-reopen a completed
// version. The check order is therefore:
//  1. dedup — an active intent already occupies the natural-key slot:
//     ErrDuplicate, no reopen. Under the advisory lock this pre-check is atomic
//     with the insert below; the partial unique index stays the backstop.
//  2. closed requirement — terminal conflict: ErrRequirementClosed, no reopen.
//  3. completed version — ONLY a genuinely new, non-conflicting intent reaches
//     this signal: ErrVersionCompleted, which the service loop answers with
//     ReopenIfCompleted + retry (the concurrent-close race recovery).
//
// Version/requirement status values are passed as query parameters from the
// version domain constants (V-0014/V-0017 single source; SQL-inline status
// literals are a second truth even when equal today).
func guardIntentScope(ctx context.Context, tx pgx.Tx, intent execution.ExecutionIntent) error {
	var duplicate bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM execution_intent
			WHERE case_version_id = $1 AND environment_fingerprint = $2
			  AND code_baseline = $3 AND purpose = $4 AND status = ANY($5::text[])
		)
	`, intent.CaseVersionID, intent.EnvironmentFingerprint, intent.CodeBaseline,
		string(intent.Purpose), intentActiveStatuses).Scan(&duplicate); err != nil {
		return wrapDB("check intent duplicate", err)
	}
	if duplicate {
		return execution.ErrDuplicate
	}

	var requirementStatus string
	err := tx.QueryRow(ctx, `
		SELECT r.status
		FROM case_version cv
		JOIN case_entity ce ON ce.id = cv.case_id
		JOIN case_scenario s ON s.id = ce.scenario_id
		JOIN requirement r ON r.id = s.requirement_id
		WHERE cv.id = $1 AND s.version_id = $2
	`, intent.CaseVersionID, intent.VersionID).Scan(&requirementStatus)
	switch {
	case err == nil:
		if requirementStatus == string(versiondomain.RequirementClosed) {
			return execution.ErrRequirementClosed
		}
	case !isNoRows(err):
		return wrapDB("check intent requirement", err)
	}

	var completed bool
	err = tx.QueryRow(ctx, `SELECT status = $2 FROM version WHERE id = $1`,
		intent.VersionID, versiondomain.StatusCompleted).Scan(&completed)
	switch {
	case err == nil:
		if completed {
			return execution.ErrVersionCompleted
		}
	case !isNoRows(err):
		return wrapDB("check intent version", err)
	}
	return nil
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

// GetByID is the cross-context read-port name (executionapp.IntentReader) for the
// same single intent read as Get: it returns the intent a case_run executed under
// with its environment_fingerprint / code_baseline frozen at creation (v0.2.4-G
// per-run actual fingerprint, SC-G5/G6). It delegates to Get so the SELECT and
// scan stay one authority (constitution art.5) — the stored boundary is read back
// verbatim, never recomputed. A missing intent maps to execution.ErrNotFound.
func (r *IntentRepository) GetByID(ctx context.Context, intentID int64) (execution.ExecutionIntent, error) {
	return r.Get(ctx, intentID)
}

// The intent store satisfies the cross-context read port the delivery report
// read model consumes (T8/T9).
var _ executionapp.IntentReader = (*IntentRepository)(nil)

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
		WHERE status = $2 AND ($1::BIGINT <= 0 OR version_id = $1)
		ORDER BY id
	`, versionID, string(execution.IntentOpen))
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
		WHERE status = ANY($2::text[]) AND ($1::BIGINT <= 0 OR version_id = $1)
		ORDER BY id
	`, versionID, intentActiveStatuses)
	if err != nil {
		return nil, wrapDB("list active intents", err)
	}
	defer rows.Close()
	return collectIntents(rows)
}

// Transition applies a guarded status change. A stale row is a no-op.
func (r *IntentRepository) Transition(ctx context.Context, id int64, from []execution.IntentStatus, to execution.IntentStatus) (bool, error) {
	ctx = contextOrBackground(ctx)
	ok, err := transitionIntent(ctx, r.pool, id, from, to)
	if err != nil {
		return false, err
	}
	if ok {
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
		  AND purpose = $4
	`, caseVersionID, fingerprint, baseline, string(execution.PurposeInitial)).Scan(&count)
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
	if err := decodeJSONObject("sut_ids", sutIDsRaw, &intent.SUTIDs); err != nil {
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
