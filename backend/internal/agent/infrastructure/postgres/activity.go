package agentpg

import (
	"context"
	"fmt"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// agent_activity fact ledger (v0.2.4-D T10, SC-D5 / Q04=A-lite): coarse,
// idempotent facts of a run's tool calls and compaction boundaries, keyed
// (run_id, seq). run_id is a logical agent-context reference (agent_0008 DB-6).
//
// The rows are FACTS, not current state: the runtime writes them before deriving
// any event, and the read side below is the production reader that keeps the
// table wired (constitution 五.7/五.9, DoD "no unwired code").

const activityColumns = `id, run_id, seq, type, title, started_at, ended_at,
	payload, error, created_at, updated_at`

// InsertActivity writes one fact row idempotently. The first write of a
// (run_id, seq) inserts it; a later write of the same key folds into the existing
// row:
//
//   - started_at, id and created_at are preserved — the first recorded start is
//     the fact's start;
//   - ended_at and error only ever move forward (COALESCE), so a replayed start
//     cannot erase a recorded outcome;
//   - payload keys accumulate (jsonb ||), so the end of a call adds its duration
//     without dropping the start's tool identity.
//
// The unique constraint uk_agent_activity_run_seq is the authority behind this
// behaviour; the ON CONFLICT target names the same key.
func (r *RuntimeRepository) InsertActivity(ctx context.Context, activity agent.Activity) error {
	if err := activity.Validate(); err != nil {
		return err
	}
	if activity.ID <= 0 {
		activity.ID = r.ids.Next()
	}
	payload, err := encodeJSONObject("activity payload", activity.Payload)
	if err != nil {
		return err
	}
	startedAt := timeOrNow(activity.StartedAt)
	var activityError *string
	if activity.Error != "" {
		activityError = &activity.Error
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO agent_activity (id, run_id, seq, type, title, started_at, ended_at,
			payload, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9)
		ON CONFLICT (run_id, seq) DO UPDATE
		SET type = EXCLUDED.type,
		    title = EXCLUDED.title,
		    ended_at = COALESCE(EXCLUDED.ended_at, agent_activity.ended_at),
		    error = COALESCE(EXCLUDED.error, agent_activity.error),
		    payload = agent_activity.payload || EXCLUDED.payload,
		    updated_at = now()
	`, activity.ID, activity.RunID, activity.Seq, string(activity.Type), activity.Title,
		startedAt, activity.EndedAt, payload, activityError)
	if err != nil {
		return wrapDB("insert agent activity", err)
	}
	return nil
}

// ListRunActivity returns one run's whole fact ledger in seq order.
//
// Disposition (T10 review #2): this is a CONCRETE repository read helper used by
// the ledger's integration tests, deliberately NOT part of the
// agent.RuntimeRepository port. The runtime never reads its own activity facts,
// so a port method would have no production caller; the production read is
// AgentCenterReader.RunActivity over the same queryRunActivity below (one SQL,
// no second source).
func (r *RuntimeRepository) ListRunActivity(ctx context.Context, runID int64) ([]agent.Activity, error) {
	return queryRunActivity(ctx, r.pool, runID)
}

// queryRunActivity is the ONE agent_activity read of the agent context, shared
// by the runtime repository and the admin-only Agent Center activity read. It
// reads without a page limit: a hidden default page would silently truncate the
// audit view (constitution 七.6).
func queryRunActivity(ctx context.Context, pool *pgxpool.Pool, runID int64) ([]agent.Activity, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+activityColumns+` FROM agent_activity
		WHERE run_id = $1
		ORDER BY seq ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("list agent activity: %w", err)
	}
	defer rows.Close()
	activities := make([]agent.Activity, 0)
	for rows.Next() {
		var (
			row     agent.Activity
			kind    string
			payload map[string]any
			failure *string
		)
		if err := rows.Scan(&row.ID, &row.RunID, &row.Seq, &kind, &row.Title,
			&row.StartedAt, &row.EndedAt, &payload, &failure,
			&row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan agent activity: %w", err)
		}
		row.Type = agent.ActivityType(kind)
		row.Payload = payload
		if row.Payload == nil {
			row.Payload = map[string]any{}
		}
		if failure != nil {
			row.Error = *failure
		}
		activities = append(activities, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list agent activity: %w", err)
	}
	return activities, nil
}
