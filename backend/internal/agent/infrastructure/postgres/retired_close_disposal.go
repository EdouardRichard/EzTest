package agentpg

import (
	"context"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// DisposeWorkItemsByTypes terminalizes every claimable work_item row of the given
// legacy types (GD-17 / SC-G10 existing-row disposal). A row is claimable when it
// is open/claimed/running/waiting_human, or failed but still below its attempt cap
// (the retry path could resurrect it). Each affected row is set to failed at the
// attempt cap with the disposal reason in last_error, so no claim path picks it up
// again and the terminalization is attributable (七.6). Rows already done/dead or
// failed-at-cap are untouched, which makes the sweep idempotent.
//
// Statuses are passed as query parameters derived from the domain constants (no
// inline SQL status literal — single source of truth, B invariant).
//
// Premise: this is a single-instance STARTUP sweep run before the workers start,
// so it does not bump lease_epoch — no live claimant can hold a disposed row at
// that point. A multi-instance deployment would need fencing here (multi-replica
// orchestration is deferred to 0.3+).
func (r *WorkItemRepository) DisposeWorkItemsByTypes(ctx context.Context, types []string, reason string) (int64, error) {
	if len(types) == 0 {
		return 0, nil
	}
	claimable := []string{
		string(agent.WorkItemOpen),
		string(agent.WorkItemClaimed),
		string(agent.WorkItemRunning),
		string(agent.WorkItemWaitingHuman),
	}
	failed := string(agent.WorkItemFailed)
	tag, err := r.pool.Exec(ctx, `
		UPDATE work_item
		SET status = $2, attempts = max_attempts, last_error = $3, updated_at = now()
		WHERE type = ANY($1)
		  AND (status = ANY($4) OR (status = $5 AND attempts < max_attempts))
	`, types, failed, reason, claimable, failed)
	if err != nil {
		return 0, wrapDB("dispose retired work items", err)
	}
	return tag.RowsAffected(), nil
}
