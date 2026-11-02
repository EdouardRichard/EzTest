package agentpg

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ agentapp.AgentCenterReader = (*AgentCenterReader)(nil)

// AgentCenterReader is the agent context's P6 read-only source for the Agent
// center catalog, run list and run statistics. It never writes and never reads
// agent_message/context_packet, so prompts and transcripts stay private.
type AgentCenterReader struct {
	pool *pgxpool.Pool
}

// NewAgentCenterReader wraps a pgx pool. A nil pool is a wiring error.
func NewAgentCenterReader(pool *pgxpool.Pool) (*AgentCenterReader, error) {
	if pool == nil {
		return nil, fmt.Errorf("agent center reader: nil pool")
	}
	return &AgentCenterReader{pool: pool}, nil
}

// ListCatalogProfiles returns every agent_profile with its current prompt
// revision metadata in one query (no N+1).
func (r *AgentCenterReader) ListCatalogProfiles(ctx context.Context) ([]agentapp.CatalogProfileRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT p.id, p.key, p.name, p.system_prompt, p.allowed_tools, p.model,
		       p.budgets, p.enabled, p.created_at, p.updated_at,
		       COALESCE(latest.version, 0), latest.created_at
		FROM agent_profile p
		LEFT JOIN LATERAL (
			SELECT version, created_at
			FROM agent_profile_prompt_revision
			WHERE profile_key = p.key
			ORDER BY version DESC
			LIMIT 1
		) latest ON TRUE
		ORDER BY p.key
	`)
	if err != nil {
		return nil, fmt.Errorf("list agent catalog profiles: %w", err)
	}
	defer rows.Close()

	profiles := make([]agentapp.CatalogProfileRow, 0)
	for rows.Next() {
		var row agentapp.CatalogProfileRow
		var toolsRaw, budgetsRaw []byte
		var promptUpdatedAt *time.Time
		if err := rows.Scan(
			&row.Profile.ID, &row.Profile.Key, &row.Profile.Name, &row.Profile.SystemPrompt,
			&toolsRaw, &row.Profile.Model, &budgetsRaw, &row.Profile.Enabled,
			&row.Profile.CreatedAt, &row.Profile.UpdatedAt,
			&row.PromptVersion, &promptUpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan agent catalog profile: %w", err)
		}
		row.Profile.AllowedTools = []string{}
		if len(toolsRaw) > 0 {
			if err := json.Unmarshal(toolsRaw, &row.Profile.AllowedTools); err != nil {
				return nil, fmt.Errorf("decode catalog allowed tools: %w", err)
			}
		}
		if len(budgetsRaw) > 0 {
			if err := json.Unmarshal(budgetsRaw, &row.Profile.Budgets); err != nil {
				return nil, fmt.Errorf("decode catalog budgets: %w", err)
			}
		}
		row.PromptUpdatedAt = promptUpdatedAt
		profiles = append(profiles, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list agent catalog profiles: %w", err)
	}
	return profiles, nil
}

// ProfileRunStats aggregates agent_run by profile_key in one query. A profile
// without runs is simply absent from the map (the caller reports zero stats).
func (r *AgentCenterReader) ProfileRunStats(ctx context.Context) (map[string]agentapp.RunStats, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT profile_key,
		       count(*),
		       count(*) FILTER (WHERE status = 'done'),
		       count(*) FILTER (WHERE status = 'failed'),
		       count(*) FILTER (WHERE status = 'aborted'),
		       count(*) FILTER (WHERE status = 'interrupted'),
		       max(started_at)
		FROM agent_run
		GROUP BY profile_key
	`)
	if err != nil {
		return nil, fmt.Errorf("aggregate agent run stats: %w", err)
	}
	defer rows.Close()

	stats := make(map[string]agentapp.RunStats)
	for rows.Next() {
		var profileKey string
		var aggregate agentapp.RunStats
		if err := rows.Scan(
			&profileKey, &aggregate.RunCount, &aggregate.DoneCount, &aggregate.FailedCount,
			&aggregate.AbortedCount, &aggregate.InterruptedCount, &aggregate.LastRunAt,
		); err != nil {
			return nil, fmt.Errorf("scan agent run stats: %w", err)
		}
		stats[profileKey] = aggregate
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("aggregate agent run stats: %w", err)
	}
	return stats, nil
}

// ListRuns returns one agent_run page joined with work_item for the version and
// work type. The query fetches limit+1 rows so has_more is exact without COUNT.
func (r *AgentCenterReader) ListRuns(ctx context.Context, query agentapp.RunQuery) ([]agentapp.RunRow, bool, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT r.id, r.profile_key, r.work_item_id, w.type, w.version_id,
		       r.conversation_id, r.status, r.terminal_reason, r.turns, r.usage,
		       r.started_at, r.finished_at
		FROM agent_run r
		LEFT JOIN work_item w ON w.id = r.work_item_id
		WHERE ($1 = '' OR r.profile_key = $1)
		  AND ($2 = '' OR r.status = $2)
		  AND ($3::bigint IS NULL OR w.version_id = $3)
		  AND ($4::bigint IS NULL OR r.id < $4)
		ORDER BY r.id DESC
		LIMIT $5
	`, query.ProfileKey, query.Status, query.VersionID, query.BeforeID, query.Limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list agent runs: %w", err)
	}
	defer rows.Close()

	runs := make([]agentapp.RunRow, 0, query.Limit+1)
	for rows.Next() {
		var run agentapp.RunRow
		var usageRaw []byte
		if err := rows.Scan(
			&run.ID, &run.ProfileKey, &run.WorkItemID, &run.WorkType, &run.VersionID,
			&run.ConversationID, &run.Status, &run.TerminalReason, &run.Turns, &usageRaw,
			&run.StartedAt, &run.FinishedAt,
		); err != nil {
			return nil, false, fmt.Errorf("scan agent run: %w", err)
		}
		run.Usage = map[string]int{}
		if len(usageRaw) > 0 {
			if err := json.Unmarshal(usageRaw, &run.Usage); err != nil {
				return nil, false, fmt.Errorf("decode agent run usage: %w", err)
			}
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list agent runs: %w", err)
	}

	hasMore := len(runs) > query.Limit
	if hasMore {
		runs = runs[:query.Limit]
	}
	return runs, hasMore, nil
}
