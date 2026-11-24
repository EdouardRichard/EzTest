package agentpg

import (
	"context"
	"errors"
	"fmt"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/jackc/pgx/v5"
)

// skill_usage ledger (v0.2.4-C T19, SC-C12): append-only found/miss facts of
// every skill.read, keyed (run_id, seq) for idempotent replay. run_id is a real
// same-context FK to agent_run. There is deliberately no UPDATE/DELETE path.

// skillUsageSeqAttempts bounds the retry on a (run_id, seq) conflict. Tool
// calls within one run are sequential, so a conflict means a concurrent writer
// on the same run won the seq; re-reading MAX(seq) resolves it.
const skillUsageSeqAttempts = 5

// RecordSkillUsage appends one usage fact and returns its per-run seq. The seq
// is assigned server-side as MAX(seq)+1 of the run inside the INSERT; the
// (run_id, seq) unique constraint turns a replayed write into a no-op, and a
// lost race retries against the fresh MAX.
func (r *SkillRepository) RecordSkillUsage(ctx context.Context, runID int64, skillName string, found bool) (int, error) {
	for attempt := 0; attempt < skillUsageSeqAttempts; attempt++ {
		var seq int
		err := r.pool.QueryRow(ctx, `
			INSERT INTO skill_usage (id, run_id, seq, skill_name, found)
			SELECT $1, $2, COALESCE(MAX(seq), 0) + 1, $3, $4
			FROM skill_usage WHERE run_id = $2
			ON CONFLICT (run_id, seq) DO NOTHING
			RETURNING seq`, r.ids.Next(), runID, skillName, found).Scan(&seq)
		if err == nil {
			return seq, nil
		}
		if errors.Is(err, pgx.ErrNoRows) {
			// ON CONFLICT swallowed the insert: another writer took this seq.
			continue
		}
		return 0, fmt.Errorf("record skill usage: %w", err)
	}
	return 0, fmt.Errorf("record skill usage: seq contention after %d attempts", skillUsageSeqAttempts)
}

// ListSkillUsage returns one run's usage facts in seq order (admin read side).
func (r *SkillRepository) ListSkillUsage(ctx context.Context, runID int64) ([]agent.SkillUsage, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, run_id, seq, skill_name, found, created_at
		FROM skill_usage WHERE run_id = $1 ORDER BY seq`, runID)
	if err != nil {
		return nil, fmt.Errorf("list skill usage: %w", err)
	}
	defer rows.Close()
	usage := make([]agent.SkillUsage, 0)
	for rows.Next() {
		var row agent.SkillUsage
		if err := rows.Scan(&row.ID, &row.RunID, &row.Seq, &row.SkillName, &row.Found, &row.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan skill usage: %w", err)
		}
		usage = append(usage, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list skill usage: %w", err)
	}
	return usage, nil
}
