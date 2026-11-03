package agentpg

// Task 46 (P2-T6): persistence for the job cancel/retry control API. These
// methods extend *WorkItemRepository without changing the frozen WorkItem and
// WorkItemContractRepository ports, so the P1–P8 workers and their fakes keep
// compiling.
//
// The frozen work_item schema has no 'cancelled' status (adding one would need
// a migration, which this task forbids). A canceled row is therefore stored as
// failed with attempts at max_attempts — the same terminal shape
// ClaimWorkItem uses at the attempt cap — which is what makes it unclaimable.
// The application projects it back to state=cancelled via last_error.

import (
	"context"
	"errors"
	"strings"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/jackc/pgx/v5"
)

// The adapter implements the Task 46 application port. QueryReader already
// imports agentapp in this package, so this assertion introduces no new
// dependency cycle.
var _ agentapp.JobControlRepository = (*WorkItemRepository)(nil)

// GetJob loads one work item, scoped to versionID when it is positive. A
// missing or foreign row is agent.ErrNotFound.
func (r *WorkItemRepository) GetJob(ctx context.Context, versionID, id int64) (agent.WorkItem, error) {
	if id <= 0 {
		return agent.WorkItem{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	if versionID < 0 {
		return agent.WorkItem{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	query := `SELECT ` + workItemColumns + ` FROM work_item WHERE id = $1`
	args := []any{id}
	if versionID > 0 {
		query += ` AND version_id = $2`
		args = append(args, versionID)
	}
	item, err := scanWorkItem(r.pool.QueryRow(ctx, query, args...))
	if err != nil {
		return agent.WorkItem{}, wrapDB("get job", err)
	}
	return item, nil
}

// CancelJob atomically cancels one open/claimed/running work item. It fences on
// (id, lease_epoch), releases the lease, increments the epoch (so the old owner
// can no longer write back), exhausts the attempt budget (so the dispatcher can
// never claim the canceled row) and closes the active attempt.
//
// A missing/foreign row is agent.ErrNotFound; a stale epoch or a state that is
// no longer open/claimed/running is agent.ErrConflict.
func (r *WorkItemRepository) CancelJob(
	ctx context.Context,
	versionID, id, leaseEpoch int64,
	reason string,
	now time.Time,
) (agent.WorkItem, error) {
	if id <= 0 {
		return agent.WorkItem{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	if versionID < 0 {
		return agent.WorkItem{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if leaseEpoch < 0 {
		return agent.WorkItem{}, platform.Invalid("lease_epoch", "must be non-negative")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return agent.WorkItem{}, platform.Invalid("reason", "must not be empty")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return agent.WorkItem{}, wrapDB("cancel job: begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := scanWorkItem(tx.QueryRow(ctx,
		scopedJobSelect(`FOR UPDATE`, versionID),
		jobScopeArgs(id, versionID)...,
	))
	if err != nil {
		return agent.WorkItem{}, wrapDB("cancel job: lock", err)
	}
	if current.LeaseEpoch != leaseEpoch || !jobStatusCancelable(current.Status) {
		return agent.WorkItem{}, agent.ErrConflict
	}
	// The attempt row to close is the one the claims recorded before this
	// update exhausts the budget.
	activeAttempt := current.Attempts

	args := []any{id, leaseEpoch, reason, now}
	versionFilter := ""
	if versionID > 0 {
		versionFilter = ` AND version_id = $5`
		args = append(args, versionID)
	}
	updated, err := scanWorkItem(tx.QueryRow(ctx, `
		UPDATE work_item
		SET status = 'failed',
		    last_error = $3,
		    claimed_by = NULL,
		    lease_until = NULL,
		    lease_epoch = lease_epoch + 1,
		    attempts = GREATEST(attempts, max_attempts),
		    available_at = $4,
		    updated_at = $4
		WHERE id = $1
		  AND lease_epoch = $2
		  AND status IN ('open', 'claimed', 'running')`+versionFilter+`
		RETURNING `+workItemColumns,
		args...))
	if err != nil {
		// The row was locked above, so a zero-row update means the guarded
		// state changed under us, not that the job vanished.
		if errors.Is(err, pgx.ErrNoRows) {
			return agent.WorkItem{}, agent.ErrConflict
		}
		return agent.WorkItem{}, wrapDB("cancel job: update", err)
	}

	if activeAttempt > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE work_item_attempt
			SET status = 'failed',
			    error = $2,
			    finished_at = $3
			WHERE work_item_id = $1
			  AND attempt = $4
			  AND status IN ('claimed', 'running')
		`, id, reason, now, activeAttempt); err != nil {
			return agent.WorkItem{}, wrapDB("cancel job: close attempt", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return agent.WorkItem{}, wrapDB("cancel job: commit", err)
	}
	return updated, nil
}

// RetryJob atomically re-opens one failed work item below its attempt cap:
// status open, lease cleared, last_error cleared, available_at=now and attempts
// reset to 0. The reset grants the user-initiated retry a fresh attempt budget;
// max_attempts is preserved, and the dispatcher retry path (ClaimWorkItem)
// consumes that new budget from 0. Nothing else about the job changes (no
// automatic model switch).
//
// A missing/foreign row is agent.ErrNotFound; a non-failed row or an exhausted
// budget is agent.ErrConflict.
func (r *WorkItemRepository) RetryJob(
	ctx context.Context,
	versionID, id int64,
	now time.Time,
) (agent.WorkItem, error) {
	if id <= 0 {
		return agent.WorkItem{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	if versionID < 0 {
		return agent.WorkItem{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return agent.WorkItem{}, wrapDB("retry job: begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := scanWorkItem(tx.QueryRow(ctx,
		scopedJobSelect(`FOR UPDATE`, versionID),
		jobScopeArgs(id, versionID)...,
	))
	if err != nil {
		return agent.WorkItem{}, wrapDB("retry job: lock", err)
	}
	if current.Status != agent.WorkItemFailed || current.Attempts >= current.MaxAttempts {
		return agent.WorkItem{}, agent.ErrConflict
	}

	args := []any{id, now}
	versionFilter := ""
	if versionID > 0 {
		versionFilter = ` AND version_id = $3`
		args = append(args, versionID)
	}
	updated, err := scanWorkItem(tx.QueryRow(ctx, `
		UPDATE work_item
		SET status = 'open',
		    attempts = 0,
		    claimed_by = NULL,
		    lease_until = NULL,
		    last_error = NULL,
		    available_at = $2,
		    updated_at = $2
		WHERE id = $1
		  AND status = 'failed'
		  AND attempts < max_attempts`+versionFilter+`
		RETURNING `+workItemColumns,
		args...))
	if err != nil {
		// The row was locked above, so a zero-row update means the guarded
		// state changed under us, not that the job vanished.
		if errors.Is(err, pgx.ErrNoRows) {
			return agent.WorkItem{}, agent.ErrConflict
		}
		return agent.WorkItem{}, wrapDB("retry job: update", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return agent.WorkItem{}, wrapDB("retry job: commit", err)
	}
	return updated, nil
}

// scopedJobSelect renders the row-lock read of one job. The caller appends the
// lock clause (FOR UPDATE) so the same scope is used for both control actions.
func scopedJobSelect(lock string, versionID int64) string {
	query := `SELECT ` + workItemColumns + ` FROM work_item WHERE id = $1`
	if versionID > 0 {
		query += ` AND version_id = $2`
	}
	return query + " " + lock
}

func jobScopeArgs(id, versionID int64) []any {
	args := []any{id}
	if versionID > 0 {
		args = append(args, versionID)
	}
	return args
}

func jobStatusCancelable(status agent.WorkItemStatus) bool {
	switch status {
	case agent.WorkItemOpen, agent.WorkItemClaimed, agent.WorkItemRunning:
		return true
	default:
		return false
	}
}
