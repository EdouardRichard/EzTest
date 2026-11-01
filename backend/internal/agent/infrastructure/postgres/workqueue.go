package agentpg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const workItemColumns = `id, version_id, team, type, status, priority, dedupe_key, depends_on,
	payload, claimed_by, lease_until, lease_epoch, attempts, max_attempts, last_error,
	available_at, created_at, updated_at`

const attemptColumns = `id, work_item_id, attempt, status, error, started_at, finished_at`

const (
	defaultClaimLimit = 50
	maxClaimLimit     = 500
)

// WorkItemRepository stores work_item and work_item_attempt rows.
type WorkItemRepository struct {
	pool *pgxpool.Pool
	ids  platformid.Generator
}

// NewWorkItemRepository wires the task board repository.
func NewWorkItemRepository(pool *pgxpool.Pool, ids platformid.Generator) *WorkItemRepository {
	return &WorkItemRepository{pool: pool, ids: ids}
}

// Ensure inserts one open item or returns the row already stored for the same
// (version_id, type, dedupe_key) tuple.
func (r *WorkItemRepository) Ensure(ctx context.Context, in agent.NewWorkItem) (agent.WorkItem, bool, error) {
	if err := in.Validate(); err != nil {
		return agent.WorkItem{}, false, err
	}
	payload, err := in.EncodePayload()
	if err != nil {
		return agent.WorkItem{}, false, err
	}
	maxAttempts := in.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = agent.DefaultMaxAttempts
	}
	team := agent.Team(strings.TrimSpace(in.Team))
	workType := strings.TrimSpace(in.Type)
	dedupeKey := strings.TrimSpace(in.DedupeKey)

	item, err := scanWorkItem(r.pool.QueryRow(ctx, `
		INSERT INTO work_item (id, version_id, team, type, status, priority, dedupe_key,
			payload, max_attempts, available_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'open', $5, $6, $7::jsonb, $8, now(), now(), now())
		ON CONFLICT (version_id, type, dedupe_key) DO NOTHING
		RETURNING `+workItemColumns,
		r.ids.Next(), in.VersionID, string(team), workType, in.Priority, dedupeKey,
		string(payload), maxAttempts))
	if err == nil {
		return item, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return agent.WorkItem{}, false, wrapDB("insert work item", err)
	}

	existing, err := scanWorkItem(r.pool.QueryRow(ctx, `
		SELECT `+workItemColumns+` FROM work_item
		WHERE version_id = $1 AND type = $2 AND dedupe_key = $3
	`, in.VersionID, workType, dedupeKey))
	if err != nil {
		return agent.WorkItem{}, false, wrapDB("look up work item by dedupe key", err)
	}
	return existing, false, nil
}

// Claim leases the next available open item to claimant.
func (r *WorkItemRepository) Claim(ctx context.Context, claimant string, lease time.Duration) (agent.WorkItem, bool, error) {
	claimant = strings.TrimSpace(claimant)
	if claimant == "" {
		return agent.WorkItem{}, false, platform.Invalid("claimant", "must not be empty")
	}
	if lease <= 0 {
		lease = 30 * time.Second
	}
	return r.claim(ctx, claimant, nil, lease)
}

// ClaimForTeams leases the next available open item whose team is in teams.
// An empty team set claims nothing; an unknown team is rejected before the
// transaction starts.
func (r *WorkItemRepository) ClaimForTeams(ctx context.Context, claimant string, teams []agent.Team, lease time.Duration) (agent.WorkItem, bool, error) {
	claimant = strings.TrimSpace(claimant)
	if claimant == "" {
		return agent.WorkItem{}, false, platform.Invalid("claimant", "must not be empty")
	}
	if lease <= 0 {
		lease = 30 * time.Second
	}
	names := make([]string, 0, len(teams))
	for _, team := range teams {
		if !team.Valid() {
			return agent.WorkItem{}, false, platform.Invalid("teams", "must contain only clarification, design, execution, delivery, steward, code, ops")
		}
		names = append(names, string(team))
	}
	if len(names) == 0 {
		return agent.WorkItem{}, false, nil
	}
	return r.claim(ctx, claimant, names, lease)
}

// ClaimForTypes leases the next item whose type is in types. Unlike Claim it
// also takes over claimed/running items whose lease expired, so a crashed
// specialized worker (e.g. the report PDF renderer) does not strand its item
// until the periodic recovery pass. An empty normalized type set claims
// nothing.
func (r *WorkItemRepository) ClaimForTypes(ctx context.Context, claimant string, types []string, lease time.Duration) (agent.WorkItem, bool, error) {
	claimant = strings.TrimSpace(claimant)
	if claimant == "" {
		return agent.WorkItem{}, false, platform.Invalid("claimant", "must not be empty")
	}
	if lease <= 0 {
		lease = 30 * time.Second
	}
	names := make([]string, 0, len(types))
	seen := make(map[string]struct{}, len(types))
	for _, workType := range types {
		workType = strings.TrimSpace(workType)
		if workType == "" {
			continue
		}
		if _, ok := seen[workType]; ok {
			continue
		}
		seen[workType] = struct{}{}
		names = append(names, workType)
	}
	if len(names) == 0 {
		return agent.WorkItem{}, false, nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return agent.WorkItem{}, false, fmt.Errorf("claim work item by type: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id int64
	err = tx.QueryRow(ctx, `
		SELECT id FROM work_item
		WHERE type = ANY($1::text[])
		  AND ((status = 'open' AND available_at <= now())
		       OR (status IN ('claimed', 'running')
		           AND lease_until IS NOT NULL AND lease_until < now()))
		ORDER BY priority DESC, id ASC
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`, names).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return agent.WorkItem{}, false, fmt.Errorf("claim work item by type: commit empty: %w", err)
		}
		return agent.WorkItem{}, false, nil
	}
	if err != nil {
		return agent.WorkItem{}, false, wrapDB("select claimable work item by type", err)
	}

	item, err := scanWorkItem(tx.QueryRow(ctx, `
		UPDATE work_item
		SET status = 'claimed',
		    lease_epoch = lease_epoch + 1,
		    attempts = attempts + 1,
		    claimed_by = $2,
		    lease_until = now() + make_interval(secs => $3),
		    updated_at = now()
		WHERE id = $1
		RETURNING `+workItemColumns,
		id, claimant, lease.Seconds()))
	if err != nil {
		return agent.WorkItem{}, false, wrapDB("claim work item by type", err)
	}
	// When taking over an expired lease, close the abandoned attempt rows
	// before appending this claim's attempt.
	if _, err := tx.Exec(ctx, `
		UPDATE work_item_attempt
		SET status = 'recovered', error = COALESCE(error, 'lease expired'), finished_at = now()
		WHERE work_item_id = $1 AND attempt < $2 AND status IN ('claimed', 'running')
	`, item.ID, item.Attempts); err != nil {
		return agent.WorkItem{}, false, wrapDB("recover abandoned work item attempts", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_item_attempt (id, work_item_id, attempt, status, started_at)
		VALUES ($1, $2, $3, 'claimed', now())
	`, r.ids.Next(), item.ID, item.Attempts); err != nil {
		return agent.WorkItem{}, false, wrapDB("insert work item attempt", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return agent.WorkItem{}, false, fmt.Errorf("claim work item by type: commit: %w", err)
	}
	return item, true, nil
}

// claim is the shared FOR UPDATE SKIP LOCKED lease path. A nil teams slice
// claims from every team; a non-empty slice filters with team = ANY(...).
func (r *WorkItemRepository) claim(ctx context.Context, claimant string, teams []string, lease time.Duration) (agent.WorkItem, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return agent.WorkItem{}, false, fmt.Errorf("claim work item: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id int64
	if len(teams) == 0 {
		err = tx.QueryRow(ctx, `
			SELECT id FROM work_item
			WHERE status = 'open' AND available_at <= now()
			ORDER BY priority DESC, id ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		`).Scan(&id)
	} else {
		err = tx.QueryRow(ctx, `
			SELECT id FROM work_item
			WHERE status = 'open' AND available_at <= now()
			  AND team = ANY($1::text[])
			ORDER BY priority DESC, id ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		`, teams).Scan(&id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return agent.WorkItem{}, false, fmt.Errorf("claim work item: commit empty: %w", err)
		}
		return agent.WorkItem{}, false, nil
	}
	if err != nil {
		return agent.WorkItem{}, false, wrapDB("select claimable work item", err)
	}

	item, err := scanWorkItem(tx.QueryRow(ctx, `
		UPDATE work_item
		SET status = 'claimed',
		    lease_epoch = lease_epoch + 1,
		    attempts = attempts + 1,
		    claimed_by = $2,
		    lease_until = now() + make_interval(secs => $3),
		    updated_at = now()
		WHERE id = $1
		RETURNING `+workItemColumns,
		id, claimant, lease.Seconds()))
	if err != nil {
		return agent.WorkItem{}, false, wrapDB("claim work item", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_item_attempt (id, work_item_id, attempt, status, started_at)
		VALUES ($1, $2, $3, 'claimed', now())
	`, r.ids.Next(), item.ID, item.Attempts); err != nil {
		return agent.WorkItem{}, false, wrapDB("insert work item attempt", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return agent.WorkItem{}, false, fmt.Errorf("claim work item: commit: %w", err)
	}
	return item, true, nil
}

// Get loads one work item, or agent.ErrNotFound.
func (r *WorkItemRepository) Get(ctx context.Context, id int64) (agent.WorkItem, error) {
	item, err := scanWorkItem(r.pool.QueryRow(ctx, `
		SELECT `+workItemColumns+` FROM work_item WHERE id = $1
	`, id))
	if err != nil {
		return agent.WorkItem{}, wrapDB("get work item", err)
	}
	return item, nil
}

// MarkRunning moves a claimed item to running.
func (r *WorkItemRepository) MarkRunning(ctx context.Context, id, epoch int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mark work item running: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE work_item SET status = 'running', updated_at = now()
		WHERE id = $1 AND lease_epoch = $2 AND status = 'claimed'
	`, id, epoch)
	if err != nil {
		return wrapDB("mark work item running", err)
	}
	if tag.RowsAffected() == 0 {
		return agent.ErrLeaseLost
	}
	if _, err := tx.Exec(ctx, `
		UPDATE work_item_attempt SET status = 'running'
		WHERE work_item_id = $1
		  AND attempt = (SELECT attempts FROM work_item WHERE id = $1)
		  AND status = 'claimed'
	`, id); err != nil {
		return wrapDB("mark work item attempt running", err)
	}
	return tx.Commit(ctx)
}

// Renew extends the lease of a claimed/running item.
func (r *WorkItemRepository) Renew(ctx context.Context, id, epoch int64, lease time.Duration) error {
	if lease <= 0 {
		lease = 30 * time.Second
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE work_item
		SET lease_until = now() + make_interval(secs => $3), updated_at = now()
		WHERE id = $1 AND lease_epoch = $2 AND status IN ('claimed', 'running')
	`, id, epoch, lease.Seconds())
	if err != nil {
		return wrapDB("renew work item lease", err)
	}
	if tag.RowsAffected() == 0 {
		return agent.ErrLeaseLost
	}
	return nil
}

// Complete finishes a claimed/running item.
func (r *WorkItemRepository) Complete(ctx context.Context, id, epoch int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("complete work item: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE work_item
		SET status = 'done', claimed_by = NULL, lease_until = NULL, updated_at = now()
		WHERE id = $1 AND lease_epoch = $2 AND status IN ('claimed', 'running')
	`, id, epoch)
	if err != nil {
		return wrapDB("complete work item", err)
	}
	if tag.RowsAffected() == 0 {
		return agent.ErrLeaseLost
	}
	if _, err := tx.Exec(ctx, `
		UPDATE work_item_attempt
		SET status = 'done', finished_at = now()
		WHERE work_item_id = $1
		  AND attempt = (SELECT attempts FROM work_item WHERE id = $1)
		  AND status IN ('claimed', 'running')
	`, id); err != nil {
		return wrapDB("complete work item attempt", err)
	}
	return tx.Commit(ctx)
}

// MarkWaitingHuman parks a running item for a human takeover (P3 §6.8). The
// lease is released; resolving the request creates a fresh execute.case item.
func (r *WorkItemRepository) MarkWaitingHuman(ctx context.Context, id, epoch int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mark work item waiting_human: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE work_item
		SET status = 'waiting_human', claimed_by = NULL, lease_until = NULL,
		    last_error = 'waiting_human', updated_at = now()
		WHERE id = $1 AND lease_epoch = $2 AND status IN ('claimed', 'running')
	`, id, epoch)
	if err != nil {
		return wrapDB("mark work item waiting_human", err)
	}
	if tag.RowsAffected() == 0 {
		return agent.ErrLeaseLost
	}
	if _, err := tx.Exec(ctx, `
		UPDATE work_item_attempt
		SET status = 'done', finished_at = now()
		WHERE work_item_id = $1
		  AND attempt = (SELECT attempts FROM work_item WHERE id = $1)
		  AND status IN ('claimed', 'running')
	`, id); err != nil {
		return wrapDB("mark work item attempt waiting_human", err)
	}
	return tx.Commit(ctx)
}

// Finish closes a waiting_human/pending item without a failure (the human
// takeover was resolved and a new execute.case item was created).
func (r *WorkItemRepository) Finish(ctx context.Context, id int64, reason string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("finish work item: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE work_item
		SET status = 'done', claimed_by = NULL, lease_until = NULL,
		    last_error = $2, updated_at = now()
		WHERE id = $1 AND status IN ('waiting_human', 'pending', 'open', 'claimed', 'running')
	`, id, reason)
	if err != nil {
		return wrapDB("finish work item", err)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE work_item_attempt
		SET status = 'done', finished_at = now()
		WHERE work_item_id = $1
		  AND attempt = (SELECT attempts FROM work_item WHERE id = $1)
		  AND status IN ('claimed', 'running')
	`, id); err != nil {
		return wrapDB("finish work item attempt", err)
	}
	return tx.Commit(ctx)
}

// Fail records a failure and either re-opens the item (retry backoff) or marks
// it dead when not retryable or the attempt budget is exhausted.
func (r *WorkItemRepository) Fail(ctx context.Context, id, epoch int64, message string, retryable bool, backoff time.Duration) error {
	if backoff < 0 {
		backoff = 0
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("fail work item: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	var attempts int
	err = tx.QueryRow(ctx, `
		UPDATE work_item
		SET status = CASE WHEN $3 AND attempts < max_attempts THEN 'open' ELSE 'dead' END,
		    last_error = $4,
		    claimed_by = NULL,
		    lease_until = NULL,
		    available_at = CASE WHEN $3 AND attempts < max_attempts
		                        THEN now() + make_interval(secs => $5)
		                        ELSE now() END,
		    updated_at = now()
		WHERE id = $1 AND lease_epoch = $2 AND status IN ('claimed', 'running')
		RETURNING status, attempts
	`, id, epoch, retryable, message, backoff.Seconds()).Scan(&status, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return agent.ErrLeaseLost
	}
	if err != nil {
		return wrapDB("fail work item", err)
	}

	attemptStatus := "failed"
	if status == string(agent.WorkItemDead) {
		attemptStatus = string(agent.AttemptDead)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE work_item_attempt
		SET status = $3, error = COALESCE(NULLIF($4, ''), error), finished_at = now()
		WHERE work_item_id = $1 AND attempt = $2 AND status IN ('claimed', 'running')
	`, id, attempts, attemptStatus, message); err != nil {
		return wrapDB("fail work item attempt", err)
	}
	return tx.Commit(ctx)
}

// RecoverExpired re-opens expired claimed/running items and marks exhausted
// ones dead. It returns the number of affected rows.
func (r *WorkItemRepository) RecoverExpired(ctx context.Context) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("recover work items: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	type expiredItem struct {
		id          int64
		attempts    int
		maxAttempts int
	}
	rows, err := tx.Query(ctx, `
		SELECT id, attempts, max_attempts FROM work_item
		WHERE status IN ('claimed', 'running')
		  AND lease_until IS NOT NULL
		  AND lease_until < now()
		ORDER BY id
		FOR UPDATE SKIP LOCKED
	`)
	if err != nil {
		return 0, wrapDB("select expired work items", err)
	}
	expired := make([]expiredItem, 0)
	for rows.Next() {
		var item expiredItem
		if err := rows.Scan(&item.id, &item.attempts, &item.maxAttempts); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan expired work item: %w", err)
		}
		expired = append(expired, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("list expired work items: %w", err)
	}
	rows.Close()
	if len(expired) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return 0, fmt.Errorf("recover work items: commit empty: %w", err)
		}
		return 0, nil
	}

	for _, item := range expired {
		status := "open"
		attemptStatus := "recovered"
		if item.attempts >= item.maxAttempts {
			status = "dead"
			attemptStatus = "dead"
		}
		if _, err := tx.Exec(ctx, `
			UPDATE work_item
			SET status = $2,
			    last_error = CASE WHEN $2 = 'dead' AND last_error IS NULL THEN 'lease expired' ELSE last_error END,
			    claimed_by = NULL,
			    lease_until = NULL,
			    updated_at = now()
			WHERE id = $1
		`, item.id, status); err != nil {
			return 0, wrapDB("recover work item", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE work_item_attempt
			SET status = $3, error = COALESCE(error, 'lease expired'), finished_at = now()
			WHERE work_item_id = $1 AND attempt = $2 AND status IN ('claimed', 'running')
		`, item.id, item.attempts, attemptStatus); err != nil {
			return 0, wrapDB("recover work item attempt", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("recover work items: commit: %w", err)
	}
	return int64(len(expired)), nil
}

// ListByVersion returns a version's work items newest first.
func (r *WorkItemRepository) ListByVersion(ctx context.Context, versionID int64, limit int) ([]agent.WorkItem, error) {
	if limit <= 0 {
		limit = defaultClaimLimit
	}
	if limit > maxClaimLimit {
		limit = maxClaimLimit
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+workItemColumns+` FROM work_item
		WHERE version_id = $1
		ORDER BY id DESC
		LIMIT $2
	`, versionID, limit)
	if err != nil {
		return nil, fmt.Errorf("list work items: %w", err)
	}
	defer rows.Close()

	items := make([]agent.WorkItem, 0)
	for rows.Next() {
		item, err := scanWorkItem(rows)
		if err != nil {
			return nil, fmt.Errorf("scan work item: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list work items: %w", err)
	}
	return items, nil
}

// ListAttempts returns the attempt log of one work item, oldest first.
func (r *WorkItemRepository) ListAttempts(ctx context.Context, workItemID int64) ([]agent.WorkItemAttempt, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+attemptColumns+` FROM work_item_attempt
		WHERE work_item_id = $1
		ORDER BY attempt ASC, id ASC
	`, workItemID)
	if err != nil {
		return nil, fmt.Errorf("list work item attempts: %w", err)
	}
	defer rows.Close()

	attempts := make([]agent.WorkItemAttempt, 0)
	for rows.Next() {
		var attempt agent.WorkItemAttempt
		var status string
		if err := rows.Scan(&attempt.ID, &attempt.WorkItemID, &attempt.Attempt, &status,
			&attempt.Error, &attempt.StartedAt, &attempt.FinishedAt); err != nil {
			return nil, fmt.Errorf("scan work item attempt: %w", err)
		}
		attempt.Status = agent.AttemptStatus(status)
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list work item attempts: %w", err)
	}
	return attempts, nil
}

func scanWorkItem(row rowScanner) (agent.WorkItem, error) {
	var item agent.WorkItem
	var team, status string
	if err := row.Scan(&item.ID, &item.VersionID, &team, &item.Type, &status, &item.Priority,
		&item.DedupeKey, &item.DependsOn, &item.Payload, &item.ClaimedBy, &item.LeaseUntil,
		&item.LeaseEpoch, &item.Attempts, &item.MaxAttempts, &item.LastError,
		&item.AvailableAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return agent.WorkItem{}, err
	}
	item.Team = agent.Team(team)
	item.Status = agent.WorkItemStatus(status)
	return item, nil
}

var _ agent.WorkItemRepository = (*WorkItemRepository)(nil)
