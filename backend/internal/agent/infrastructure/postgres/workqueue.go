package agentpg

import (
	"bytes"
	"context"
	"encoding/json"
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

const workItemColumns = `id, version_id, team, type, kind, goal, target, inputs, expected,
	budget, status, priority, dedupe_key, depends_on, payload, created_by, idempotency_key,
	result, claimed_by, lease_until, lease_epoch, attempts, max_attempts, last_error,
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
		WHERE kind = ''
		  AND type = ANY($1::text[])
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
			WHERE kind = '' AND status = 'open' AND available_at <= now()
			ORDER BY priority DESC, id ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		`).Scan(&id)
	} else {
		err = tx.QueryRow(ctx, `
			SELECT id FROM work_item
			WHERE kind = '' AND status = 'open' AND available_at <= now()
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

// CreateWorkItem inserts one open 0.2.1 item, or returns the row already stored
// for the same (version_id, kind, idempotency_key). A NULL/empty idempotency
// key is exempt from the lookup, so every such call inserts a new row.
func (r *WorkItemRepository) CreateWorkItem(ctx context.Context, in agent.CreateWorkItemParams) (agent.WorkItem, error) {
	in = in.Normalize()
	if err := in.Validate(); err != nil {
		return agent.WorkItem{}, err
	}
	target, inputs, expected, budget, payload, err := in.EncodeObjects()
	if err != nil {
		return agent.WorkItem{}, err
	}

	id := r.ids.Next()
	dedupeKey := in.IdempotencyKey
	if dedupeKey == "" {
		dedupeKey = fmt.Sprintf("v021:%s:%d", in.Kind, id)
	}
	var idempotencyKey *string
	if in.IdempotencyKey != "" {
		key := in.IdempotencyKey
		idempotencyKey = &key
	}

	item, err := scanWorkItem(r.pool.QueryRow(ctx, `
		INSERT INTO work_item (id, version_id, team, type, kind, goal, target, inputs,
			expected, budget, status, priority, dedupe_key, depends_on, payload,
			created_by, idempotency_key, result, max_attempts, available_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb, $9::jsonb, $10::jsonb,
			'open', $11, $12, $13::bigint[], $14::jsonb, $15, $16, '{}'::jsonb, $17,
			now(), now(), now())
		ON CONFLICT DO NOTHING
		RETURNING `+workItemColumns,
		id, in.VersionID, string(in.Team), in.Type, in.Kind, in.Goal,
		string(target), string(inputs), string(expected), string(budget),
		in.Priority, dedupeKey, in.DependsOn, string(payload), in.CreatedBy,
		idempotencyKey, in.MaxAttempts))
	if err == nil {
		return item, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return agent.WorkItem{}, wrapDB("insert 0.2.1 work item", err)
	}
	if idempotencyKey == nil {
		return agent.WorkItem{}, fmt.Errorf("insert 0.2.1 work item: %w", agent.ErrConflict)
	}

	existing, err := scanWorkItem(r.pool.QueryRow(ctx, `
		SELECT `+workItemColumns+` FROM work_item
		WHERE version_id = $1 AND kind = $2 AND idempotency_key = $3
	`, in.VersionID, in.Kind, in.IdempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return agent.WorkItem{}, fmt.Errorf("insert 0.2.1 work item: %w", agent.ErrConflict)
	}
	if err != nil {
		return agent.WorkItem{}, wrapDB("look up work item by idempotency key", err)
	}
	return existing, nil
}

// ClaimWorkItem leases the next runnable 0.2.1 item to owner. Runnable means
// open, retryable failed (attempts below max_attempts), or a claimed/running
// item whose lease expired before now while attempts remain below max_attempts.
// Items whose depends_on set is not fully done are skipped; NULL and {} both
// mean no dependencies. The claim increments lease_epoch and attempts, which
// fences every older owner out of later writes.
//
// attempts is the retry-budget counter; the attempt log number is independent
// of it: it is COALESCE(MAX(attempt), 0) + 1 of the item's attempt history,
// computed under the claim's row lock. A user-initiated RetryJob resets attempts
// to 0 (a fresh budget) without deleting the append-only attempt log, so
// deriving the log number from attempts would reuse attempt 1 and hit
// UNIQUE (work_item_id, attempt). The unique index stays the backstop.
//
// R41: an expired lease at the attempt cap is settled as failed inside the
// claim transaction (0.2.1 has no dead state), so a crashed run can never be
// taken over beyond max_attempts.
func (r *WorkItemRepository) ClaimWorkItem(ctx context.Context, owner string, now time.Time) (*agent.WorkItem, error) {
	return r.claimWorkItem(ctx, owner, nil, now)
}

// ClaimWorkItemForTeams applies ClaimWorkItem to the listed teams only. An
// empty team set claims nothing. The team set is validated by the application
// facade (WorkQueue) before it reaches the repository.
func (r *WorkItemRepository) ClaimWorkItemForTeams(ctx context.Context, owner string, teams []agent.Team, now time.Time) (*agent.WorkItem, error) {
	if len(teams) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(teams))
	for _, team := range teams {
		names = append(names, string(team))
	}
	return r.claimWorkItem(ctx, owner, names, now)
}

// claimWorkItem is the shared 0.2.1 claim path. A nil teams slice claims from
// every team; a non-empty slice adds a team filter to the candidate query. The
// R41 expired-lease settlement stays global: it only closes rows that already
// exhausted their attempt budget, which is correct for every owner.
func (r *WorkItemRepository) claimWorkItem(ctx context.Context, owner string, teams []string, now time.Time) (*agent.WorkItem, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, platform.Invalid("owner", "must not be empty")
	}
	if now.IsZero() {
		now = time.Now()
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("claim 0.2.1 work item: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// R41: an expired lease may only be taken over while attempts remain. At
	// the attempt cap the row is settled as failed (0.2.1 has no dead status)
	// and its abandoned attempt is closed, so it can never be claimed again.
	// This runs in the claim transaction so no separate recovery pass is
	// required for the 0.2.1 queue.
	if _, err := tx.Exec(ctx, `
		UPDATE work_item_attempt
		SET status = 'failed',
		    error = COALESCE(error, 'lease expired: attempts exhausted'),
		    finished_at = $1
		WHERE work_item_id IN (
		        SELECT id FROM work_item
		        WHERE kind <> ''
		          AND status IN ('claimed', 'running')
		          AND lease_until IS NOT NULL
		          AND lease_until < $1
		          AND attempts >= max_attempts
		        FOR UPDATE SKIP LOCKED)
		  AND status IN ('claimed', 'running')
	`, now); err != nil {
		return nil, wrapDB("settle expired 0.2.1 work item attempts", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE work_item
		SET status = 'failed',
		    last_error = COALESCE(last_error, 'lease expired: attempts exhausted'),
		    claimed_by = NULL,
		    lease_until = NULL,
		    updated_at = $1
		WHERE kind <> ''
		  AND status IN ('claimed', 'running')
		  AND lease_until IS NOT NULL
		  AND lease_until < $1
		  AND attempts >= max_attempts
	`, now); err != nil {
		return nil, wrapDB("settle expired 0.2.1 work items", err)
	}

	teamFilter := ""
	args := []any{now}
	if len(teams) > 0 {
		teamFilter = " AND w.team = ANY($2::text[])"
		args = append(args, teams)
	}

	var id int64
	err = tx.QueryRow(ctx, `
		SELECT w.id FROM work_item w
		WHERE w.kind <> ''
		  AND w.available_at <= $1
		  AND (w.status = 'open'
		       OR (w.status = 'failed' AND w.attempts < w.max_attempts)
		       OR (w.status IN ('claimed', 'running')
		           AND w.lease_until IS NOT NULL AND w.lease_until < $1
		           AND w.attempts < w.max_attempts))
		  AND (w.depends_on IS NULL
		       OR cardinality(w.depends_on) = 0
		       OR NOT EXISTS (
		           SELECT 1 FROM unnest(w.depends_on) AS dependency(id)
		           WHERE NOT EXISTS (
		               SELECT 1 FROM work_item done
		               WHERE done.id = dependency.id AND done.status = 'done')))`+
		teamFilter+`
		ORDER BY w.priority DESC, w.id ASC
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`, args...).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("claim 0.2.1 work item: commit empty: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, wrapDB("select claimable 0.2.1 work item", err)
	}

	item, err := scanWorkItem(tx.QueryRow(ctx, `
		UPDATE work_item
		SET status = 'claimed',
		    lease_epoch = lease_epoch + 1,
		    attempts = attempts + 1,
		    claimed_by = $2,
		    lease_until = $3::timestamptz + make_interval(secs => $4),
		    updated_at = $3
		WHERE id = $1
		RETURNING `+workItemColumns,
		id, owner, now, agent.DefaultWorkItemLease.Seconds()))
	if err != nil {
		return nil, wrapDB("claim 0.2.1 work item", err)
	}

	// The log number is MAX(attempt)+1, not the just-incremented budget
	// counter: a manual RetryJob reset attempts to 0 while the append-only
	// history keeps growing. The FOR UPDATE lock taken above makes this read
	// stable; UNIQUE (work_item_id, attempt) remains the backstop.
	var attemptNumber int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(attempt), 0) + 1
		FROM work_item_attempt
		WHERE work_item_id = $1
	`, item.ID).Scan(&attemptNumber); err != nil {
		return nil, wrapDB("select next 0.2.1 work item attempt number", err)
	}

	// Taking over an expired lease abandons the previous attempt. Retries of a
	// failed item leave the already-closed attempt untouched.
	if _, err := tx.Exec(ctx, `
		UPDATE work_item_attempt
		SET status = 'recovered', error = COALESCE(error, 'lease expired'), finished_at = $3
		WHERE work_item_id = $1 AND attempt < $2 AND status IN ('claimed', 'running')
	`, item.ID, attemptNumber, now); err != nil {
		return nil, wrapDB("recover abandoned 0.2.1 work item attempts", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_item_attempt (id, work_item_id, attempt, status, started_at)
		VALUES ($1, $2, $3, 'claimed', $4)
	`, r.ids.Next(), item.ID, attemptNumber, now); err != nil {
		return nil, wrapDB("insert 0.2.1 work item attempt", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("claim 0.2.1 work item: commit: %w", err)
	}
	return &item, nil
}

// RenewLease extends a claimed/running 0.2.1 item's lease to the absolute
// until. A stale epoch reports agent.ErrLeaseLost.
func (r *WorkItemRepository) RenewLease(ctx context.Context, id, epoch int64, until time.Time) error {
	if id <= 0 {
		return platform.Invalid("id", "must be a positive decimal ID")
	}
	if until.IsZero() {
		return platform.Invalid("until", "must not be zero")
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE work_item
		SET lease_until = $3, updated_at = now()
		WHERE id = $1 AND lease_epoch = $2 AND kind <> '' AND status IN ('claimed', 'running')
	`, id, epoch, until)
	if err != nil {
		return wrapDB("renew 0.2.1 work item lease", err)
	}
	if tag.RowsAffected() == 0 {
		return agent.ErrLeaseLost
	}
	return nil
}

// CompleteWorkItem marks a claimed/running 0.2.1 item done and stores result.
// A stale epoch reports agent.ErrLeaseLost and changes nothing.
func (r *WorkItemRepository) CompleteWorkItem(ctx context.Context, id, epoch int64, result json.RawMessage) error {
	if id <= 0 {
		return platform.Invalid("id", "must be a positive decimal ID")
	}
	encoded, err := normalizeResultObject(result)
	if err != nil {
		return err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("complete 0.2.1 work item: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE work_item
		SET status = 'done',
		    result = $3::jsonb,
		    claimed_by = NULL,
		    lease_until = NULL,
		    updated_at = now()
		WHERE id = $1 AND lease_epoch = $2 AND kind <> '' AND status IN ('claimed', 'running')
	`, id, epoch, string(encoded))
	if err != nil {
		return wrapDB("complete 0.2.1 work item", err)
	}
	if tag.RowsAffected() == 0 {
		return agent.ErrLeaseLost
	}
	// The active attempt row is the open one, not attempt = the attempts
	// budget counter: a manual retry (RetryJob) resets attempts while the
	// append-only log keeps counting (see ClaimWorkItem). A claim leaves at
	// most one open attempt row, so the status identifies it.
	if _, err := tx.Exec(ctx, `
		UPDATE work_item_attempt
		SET status = 'done', finished_at = now()
		WHERE work_item_id = $1
		  AND status IN ('claimed', 'running')
	`, id); err != nil {
		return wrapDB("complete 0.2.1 work item attempt", err)
	}
	return tx.Commit(ctx)
}

// FailWorkItem records a failure for a claimed/running 0.2.1 item. The item
// becomes failed and stays retryable by ClaimWorkItem until attempts reaches
// max_attempts. A stale epoch reports agent.ErrLeaseLost.
func (r *WorkItemRepository) FailWorkItem(ctx context.Context, id, epoch int64, reason string) error {
	if id <= 0 {
		return platform.Invalid("id", "must be a positive decimal ID")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("fail 0.2.1 work item: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE work_item
		SET status = 'failed',
		    last_error = $3,
		    claimed_by = NULL,
		    lease_until = NULL,
		    available_at = now(),
		    updated_at = now()
		WHERE id = $1 AND lease_epoch = $2 AND kind <> '' AND status IN ('claimed', 'running')
	`, id, epoch, reason)
	if err != nil {
		return wrapDB("fail 0.2.1 work item", err)
	}
	if tag.RowsAffected() == 0 {
		return agent.ErrLeaseLost
	}
	if _, err := tx.Exec(ctx, `
		UPDATE work_item_attempt
		SET status = 'failed', error = COALESCE(NULLIF($2, ''), error), finished_at = now()
		WHERE work_item_id = $1
		  AND status IN ('claimed', 'running')
	`, id, reason); err != nil {
		return wrapDB("fail 0.2.1 work item attempt", err)
	}
	return tx.Commit(ctx)
}

// FailWorkItemTerminal records a non-retryable failure for a claimed/running
// 0.2.1 item. The frozen schema has no dead status in the 0.2.1 contract, so the
// row is stored failed with attempts set to max_attempts, which makes
// ClaimWorkItem's "failed AND attempts < max_attempts" predicate false forever.
// The append-only attempt log keeps the real attempt number and is closed.
func (r *WorkItemRepository) FailWorkItemTerminal(ctx context.Context, id, epoch int64, reason string) error {
	if id <= 0 {
		return platform.Invalid("id", "must be a positive decimal ID")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("fail 0.2.1 work item terminally: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE work_item
		SET status = 'failed',
		    attempts = max_attempts,
		    last_error = $3,
		    claimed_by = NULL,
		    lease_until = NULL,
		    available_at = now(),
		    updated_at = now()
		WHERE id = $1 AND lease_epoch = $2 AND kind <> '' AND status IN ('claimed', 'running')
	`, id, epoch, reason)
	if err != nil {
		return wrapDB("fail 0.2.1 work item terminally", err)
	}
	if tag.RowsAffected() == 0 {
		return agent.ErrLeaseLost
	}
	if _, err := tx.Exec(ctx, `
		UPDATE work_item_attempt
		SET status = 'failed', error = COALESCE(NULLIF($2, ''), error), finished_at = now()
		WHERE work_item_id = $1
		  AND status IN ('claimed', 'running')
	`, id, reason); err != nil {
		return wrapDB("fail 0.2.1 work item attempt terminally", err)
	}
	return tx.Commit(ctx)
}

func normalizeResultObject(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, platform.Invalid("result", "must be a JSON object")
	}
	return raw, nil
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
		WHERE kind = ''
		  AND status IN ('claimed', 'running')
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

// HasWorkItemKindSince reports whether a version owns a work item whose 0.2.1
// kind (or frozen legacy type) equals kind and whose created_at is at/after
// since. R49 uses it with the version's stage-entered floor so:
//   - a job from an earlier visit to the same stage cannot mask a lost
//     later-cycle job, and
//   - the answer does not depend on the newest-page bound of ListByVersion
//     (a long-lived version can push the legitimate job out of that page).
func (r *WorkItemRepository) HasWorkItemKindSince(ctx context.Context, versionID int64, kind string, since time.Time) (bool, error) {
	if versionID <= 0 {
		return false, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return false, platform.Invalid("kind", "must not be empty")
	}
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM work_item
			WHERE version_id = $1
			  AND (kind = $2 OR type = $2)
			  AND created_at >= $3
		)
	`, versionID, kind, since.UTC()).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check %s work item for version %d: %w", kind, versionID, err)
	}
	return exists, nil
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
	if err := row.Scan(&item.ID, &item.VersionID, &team, &item.Type, &item.Kind, &item.Goal,
		&item.Target, &item.Inputs, &item.Expected, &item.Budget, &status, &item.Priority,
		&item.DedupeKey, &item.DependsOn, &item.Payload, &item.CreatedBy,
		&item.IdempotencyKey, &item.Result, &item.ClaimedBy, &item.LeaseUntil,
		&item.LeaseEpoch, &item.Attempts, &item.MaxAttempts, &item.LastError,
		&item.AvailableAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return agent.WorkItem{}, err
	}
	item.Team = agent.Team(team)
	item.Status = agent.WorkItemStatus(status)
	return item, nil
}

var _ agent.WorkItemRepository = (*WorkItemRepository)(nil)
var _ agent.WorkItemContractRepository = (*WorkItemRepository)(nil)
