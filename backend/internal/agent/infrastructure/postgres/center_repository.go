package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// CenterRepository implements the additive P8 center read model
// (domain.CenterReadRepository) on a pgx pool. It only aggregates existing
// profile/session/run rows: no writes, no message content and no prompt text.
type CenterRepository struct {
	pool *pgxpool.Pool
}

// CenterRepository serves the P8 Agent-centre read model.
var _ agent.CenterReadRepository = (*CenterRepository)(nil)

// NewCenterRepository returns the Agent-centre read repository.
func NewCenterRepository(pool *pgxpool.Pool) *CenterRepository {
	return &CenterRepository{pool: pool}
}

// centerStatsQuery aggregates every stored profile exactly once. The LEFT JOINs
// keep profiles without sessions/runs in the result with zero counts, so the
// catalogue reports zeros instead of an absent/demo statistic.
const centerStatsQuery = `SELECT p.id, p.key,
	COUNT(r.id),
	COUNT(r.id) FILTER (WHERE r.status = 'done'),
	COUNT(r.id) FILTER (WHERE r.status IN ('failed', 'aborted', 'interrupted'))
  FROM agent_profile p
  LEFT JOIN agent_session s ON s.profile_id = p.id
  LEFT JOIN agent_run r ON r.session_id = s.id
 GROUP BY p.id, p.key
 ORDER BY p.id`

// ProfileRunStats returns one row per stored profile.
func (r *CenterRepository) ProfileRunStats(ctx context.Context) ([]agent.ProfileRunStats, error) {
	rows, err := r.pool.Query(ctx, centerStatsQuery)
	if err != nil {
		return nil, fmt.Errorf("postgres: load agent run statistics: %w", err)
	}
	defer rows.Close()

	stats := make([]agent.ProfileRunStats, 0)
	for rows.Next() {
		var row agent.ProfileRunStats
		if err := rows.Scan(
			&row.ProfileID,
			&row.ProfileKey,
			&row.RunCount,
			&row.SucceededCount,
			&row.FailedCount,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan agent run statistics: %w", err)
		}
		stats = append(stats, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate agent run statistics: %w", err)
	}
	return stats, nil
}

// centerSessionsQuery projects the newest sessions with their derived status.
// The two lateral joins fetch the active run and the newest terminal run in one
// statement, matching the P1 session-list derivation. Empty role/status
// parameters mean "all".
const centerSessionsQuery = `SELECT s.id, p.key, s.title,
	COALESCE(ar.status, lr.status, 'idle'),
	(ar.id IS NOT NULL),
	s.created_at, s.updated_at, s.last_activity_at
  FROM agent_session s
  JOIN agent_profile p ON p.id = s.profile_id
  LEFT JOIN LATERAL (
	SELECT id, status FROM agent_run
	WHERE session_id = s.id AND status = 'running'
	ORDER BY id DESC LIMIT 1
  ) ar ON true
  LEFT JOIN LATERAL (
	SELECT id, status FROM agent_run
	WHERE session_id = s.id AND status <> 'running'
	ORDER BY id DESC LIMIT 1
  ) lr ON true
 WHERE ($1::text = '' OR p.key = $1::text)
   AND ($2::text = '' OR COALESCE(ar.status, lr.status, 'idle') = $2::text)
 ORDER BY s.last_activity_at DESC, s.id DESC
 LIMIT $3`

// CenterSessions returns the newest sessions matching filter.
func (r *CenterRepository) CenterSessions(ctx context.Context, filter agent.CenterSessionFilter) ([]agent.CenterSessionRow, error) {
	if filter.Limit <= 0 {
		return nil, fmt.Errorf("postgres: load agent center sessions: limit must be positive")
	}
	rows, err := r.pool.Query(ctx, centerSessionsQuery, filter.RoleKey, filter.Status, filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: load agent center sessions: %w", err)
	}
	defer rows.Close()

	sessions := make([]agent.CenterSessionRow, 0)
	for rows.Next() {
		var row agent.CenterSessionRow
		if err := rows.Scan(
			&row.ID,
			&row.ProfileKey,
			&row.Title,
			&row.Status,
			&row.Running,
			&row.CreatedAt,
			&row.UpdatedAt,
			&row.LastActivityAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan agent center session: %w", err)
		}
		sessions = append(sessions, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate agent center sessions: %w", err)
	}
	return sessions, nil
}
