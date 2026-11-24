package main

// v0.2.4-H T10 (spec §3.6 H-F5/H-F6): the TARGETED design.requirement work-item
// source of ONE requirement version, and the two readers built on it.
//
// One source, two consumers, one row set:
//   - the coverage-repair generation counter (H-F5): EnsureCoverageRepair used to
//     derive its generation key from the capped newest-500 ListByVersion page.
//     On a busy version the older authoring generations fall out of that page,
//     the counter undercounts, and the derived key COLLIDES with a terminal
//     generation — Ensure answers created=false with no error and the
//     requirement is never repaired again (the DEV-E2/E3 stranding family).
//   - the author-batch settle reader (H-F6): the design planner's judge predicate
//     asks whether the authoring batch of the requirement version still owes
//     work. The same capped page would answer "no batch" for a busy version and
//     release the judge onto a partial draft set.
//
// Both read through authorBatchSQLSource: version + type + payload
// requirement_version_id, NO LIMIT — the same shape as judgeGenerationSQLSource
// and judgeReworkSQLSource.

import (
	"context"
	"fmt"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/jackc/pgx/v5/pgxpool"
)

// authorBatchWorkItemSource is the narrow, TARGETED row source both readers
// need: every design.requirement work item of one requirement version,
// regardless of status.
type authorBatchWorkItemSource interface {
	AuthorItemsForRequirementVersion(ctx context.Context, versionID, requirementVersionID int64) ([]agent.WorkItem, error)
}

// authorBatchSQLSource reads the design.requirement rows of one requirement
// version directly from work_item (read-only, the same composition-root SQL
// source pattern as judgeGenerationSQLSource). The (version_id, type) prefix
// rides the uk_work_item_dedupe index; payload->>'requirement_version_id'
// normalizes JSON string and number IDs to the same decimal text. Authoring
// items are 0.2.1 TYPE-path rows (kind is empty); the kind guard keeps the
// WorkTypeForItem convention without giving up the index prefix. NO LIMIT
// window: every authoring generation of the requirement version is returned.
// attempts/max_attempts distinguish a retryable failure from an exhausted one;
// updated_at is the batch's last-progress clock.
type authorBatchSQLSource struct {
	pool *pgxpool.Pool
}

var _ authorBatchWorkItemSource = (*authorBatchSQLSource)(nil)

func (s *authorBatchSQLSource) AuthorItemsForRequirementVersion(ctx context.Context, versionID, requirementVersionID int64) ([]agent.WorkItem, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("author batch work-item SQL source is not configured")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, type, kind, status, attempts, max_attempts, payload, created_at, updated_at
		FROM work_item
		WHERE version_id = $1 AND type = $2 AND (kind = '' OR kind = $2)
		  AND payload->>'requirement_version_id' = $3
		ORDER BY created_at DESC, id DESC
	`, versionID, designapp.TaskTypeDesignRequirement, platformid.Format(requirementVersionID))
	if err != nil {
		return nil, fmt.Errorf("read design authoring work items: %w", err)
	}
	defer rows.Close()
	items := make([]agent.WorkItem, 0)
	for rows.Next() {
		var item agent.WorkItem
		if err := rows.Scan(&item.ID, &item.Type, &item.Kind, &item.Status,
			&item.Attempts, &item.MaxAttempts, &item.Payload, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan design authoring work item: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read design authoring work items: %w", err)
	}
	return items, nil
}

// authorBatchReader adapts the targeted work-item rows to the design context's
// neutral AuthorBatchReader port. The design context never reads work_item
// itself (AGENTS §2.1); this composition-root adapter is the only place that
// knows both contexts.
type authorBatchReader struct {
	source authorBatchWorkItemSource
}

var _ designapp.AuthorBatchReader = (*authorBatchReader)(nil)

// AuthorBatchState reports the settle state of the requirement version's
// authoring batch. A nil source fails closed: silently answering "no batch"
// would release the judge onto a draft set the author is still extending.
func (r *authorBatchReader) AuthorBatchState(ctx context.Context, versionID, requirementVersionID int64) (designapp.AuthorBatchState, error) {
	if r == nil || r.source == nil {
		return "", fmt.Errorf("author batch reader is not configured")
	}
	items, err := r.source.AuthorItemsForRequirementVersion(ctx, versionID, requirementVersionID)
	if err != nil {
		return "", err
	}
	return authorBatchState(items, requirementVersionID), nil
}

// AuthorBatchStallDeadline returns the earliest moment a lease-holding row of
// the batch would count as stalled (zero = no stall clock is running). A nil
// source fails closed for the same reason as AuthorBatchState.
func (r *authorBatchReader) AuthorBatchStallDeadline(ctx context.Context, versionID, requirementVersionID int64) (time.Time, error) {
	if r == nil || r.source == nil {
		return time.Time{}, fmt.Errorf("author batch reader is not configured")
	}
	items, err := r.source.AuthorItemsForRequirementVersion(ctx, versionID, requirementVersionID)
	if err != nil {
		return time.Time{}, err
	}
	return authorBatchStallDeadline(items, requirementVersionID)
}

// authorBatchState derives the settle state from the requirement version's
// authoring rows. "Terminal" is designWorkItemTerminal — the SAME predicate the
// rework escalation budget and both generation counters use, so there is one
// work-item lifecycle truth rather than a second switch.
func authorBatchState(items []agent.WorkItem, requirementVersionID int64) designapp.AuthorBatchState {
	found := false
	for _, item := range items {
		if !isAuthoringGenerationOf(item, requirementVersionID) {
			continue
		}
		found = true
		if !designWorkItemTerminal(item) {
			return designapp.AuthorBatchInFlight
		}
	}
	if !found {
		return designapp.AuthorBatchNoBatch
	}
	return designapp.AuthorBatchAllTerminal
}

// authorBatchStallDeadline returns the EARLIEST stall deadline among the batch's
// lease-holding in-flight rows, or the zero time when no stall clock runs.
//
// Only claimed/running rows carry a progress clock (review I1): work_item's
// Renew statement is guarded by workItemLeasedStatuses = {claimed, running}
// (agent/infrastructure/postgres/workqueue.go), so updated_at advances ONLY while
// a lease is held. A queued row — open on first claim, or open again after a
// retryable failure waits out its backoff and a free worker — and a row parked
// for a human have a frozen clock and an unbounded, entirely normal wait.
// Treating that frozen clock as "stalled" stamped judge_exhausted over drafts
// whose author had simply not been scheduled yet, and the escalation outranks a
// later pass, so every such draft then needed a manual override.
//
// Each leased row's allowance scales with its OWN max_attempts (the fact the
// reader already carries: queue default 3, domain bound 10, DB CHECK up to 20),
// never with the platform default alone.
//
// A leased row with no progress timestamp at all is a data-integrity error and
// fails closed — it is NOT reported as "no clock", which would silently defer
// forever (constitution VII.6).
func authorBatchStallDeadline(items []agent.WorkItem, requirementVersionID int64) (time.Time, error) {
	var earliest time.Time
	for _, item := range items {
		if !isAuthoringGenerationOf(item, requirementVersionID) || designWorkItemTerminal(item) {
			continue
		}
		if !workItemHoldsLease(item) {
			continue
		}
		if item.UpdatedAt.IsZero() {
			return time.Time{}, fmt.Errorf(
				"authoring work item %d holds a lease but has no progress timestamp", item.ID)
		}
		deadline := item.UpdatedAt.Add(authorBatchRowStallWindow(item.MaxAttempts))
		if earliest.IsZero() || deadline.Before(earliest) {
			earliest = deadline
		}
	}
	return earliest, nil
}

// workItemHoldsLease reports whether the row is in a status that holds an active
// lease — the same status set the Renew statement guards on, derived from the
// agent domain constants (never re-typed literals). Only these rows have a live
// last-progress clock.
func workItemHoldsLease(item agent.WorkItem) bool {
	return item.Status == agent.WorkItemClaimed || item.Status == agent.WorkItemRunning
}

// authorBatchRowStallWindow scales the per-attempt allowance by the row's own
// attempt cap. A non-positive cap (a legacy or hand-written row) falls back to
// the queue default rather than to a zero window, which would escalate
// instantly.
func authorBatchRowStallWindow(maxAttempts int) time.Duration {
	if maxAttempts <= 0 {
		maxAttempts = agentapp.DefaultMaxAttempts
	}
	return time.Duration(maxAttempts) * designapp.AuthorBatchStallWindowPerAttempt
}

// isAuthoringGenerationOf reports whether a work item is a design.requirement
// generation of exactly this requirement version. An unparsable payload never
// matches (the same treatment coverageRepairDedupeKey gives it).
func isAuthoringGenerationOf(item agent.WorkItem, requirementVersionID int64) bool {
	if requirementVersionID <= 0 || agentapp.WorkTypeForItem(item) != designapp.TaskTypeDesignRequirement {
		return false
	}
	payload, err := agentapp.ParseWorkItemPayload(item.Payload)
	return err == nil && payload.RequirementVersionID == requirementVersionID
}
