package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// runColumnList is the column list shared by every run query and the scan
// order scanRun (and the joined-run scan of the session list) expects.
const runColumnList = `id, session_id, status, terminal_reason, abort_code, abort_message, error_code, error_message, turns, input_tokens, output_tokens, reasoning_tokens, last_tool_name, last_tool_pending, started_at, ended_at`

// runProjection returns runColumnList qualified with a table alias, so a query
// can join agent_runs more than once and still share the column order.
func runProjection(alias string) string {
	prefix := alias + "."
	return prefix + strings.ReplaceAll(runColumnList, ", ", ", "+prefix)
}

// RunRepository implements domain.RunRepository on a pgx pool.
type RunRepository struct {
	pool *pgxpool.Pool
}

// NewRunRepository returns a run repository backed by pool.
func NewRunRepository(pool *pgxpool.Pool) *RunRepository {
	return &RunRepository{pool: pool}
}

// scanRun scans one row in runColumnList order.
func scanRun(row rowScanner) (agent.Run, error) {
	var (
		run            agent.Run
		status         string
		terminalReason *string
		abortCode      *string
		abortMessage   *string
		errorCode      *string
		errorMessage   *string
		lastToolName   *string
	)
	if err := row.Scan(
		&run.ID,
		&run.SessionID,
		&status,
		&terminalReason,
		&abortCode,
		&abortMessage,
		&errorCode,
		&errorMessage,
		&run.Turns,
		&run.InputTokens,
		&run.OutputTokens,
		&run.ReasoningTokens,
		&lastToolName,
		&run.LastToolPending,
		&run.StartedAt,
		&run.EndedAt,
	); err != nil {
		return agent.Run{}, err
	}
	run.Status = agent.RunStatus(status)
	if terminalReason != nil {
		run.TerminalReason = agent.TerminalReason(*terminalReason)
	}
	if abortCode != nil {
		run.AbortCode = *abortCode
	}
	if abortMessage != nil {
		run.AbortMessage = *abortMessage
	}
	if errorCode != nil {
		run.ErrorCode = *errorCode
	}
	if errorMessage != nil {
		run.ErrorMessage = *errorMessage
	}
	if lastToolName != nil {
		run.LastToolName = *lastToolName
	}
	return run, nil
}

// CreateTurn inserts the running run, its user message and the session's title
// and activity refresh in one CTE statement, so a unique-index conflict rolls
// the whole turn back. titlePreview must already be truncated by the caller.
func (r *RunRepository) CreateTurn(ctx context.Context, sessionID int64, content []agent.Part, titlePreview string) (agent.TurnIDs, error) {
	encoded, err := encodeParts(content)
	if err != nil {
		return agent.TurnIDs{}, fmt.Errorf("postgres: encode user message content: %w", err)
	}
	const query = `WITH ins_run AS (
  INSERT INTO agent_runs (session_id, status) VALUES ($1, 'running') RETURNING id
), ins_msg AS (
  INSERT INTO agent_messages (session_id, run_id, role, content)
  SELECT $1, ins_run.id, 'user', $2::jsonb FROM ins_run RETURNING id
), upd AS (
  UPDATE agent_sessions
     SET last_activity_at = now(), updated_at = now(),
         title = CASE WHEN title = '' THEN $3 ELSE title END
   WHERE id = $1
)
SELECT ins_run.id, ins_msg.id FROM ins_run, ins_msg`
	var ids agent.TurnIDs
	if err := r.pool.QueryRow(ctx, query, sessionID, encoded, titlePreview).Scan(&ids.RunID, &ids.UserMessageID); err != nil {
		if mapped := mapRunInsertViolation(err); mapped != nil {
			return agent.TurnIDs{}, mapped
		}
		return agent.TurnIDs{}, fmt.Errorf("postgres: create agent turn: %w", err)
	}
	return ids, nil
}

// Get returns one run by ID.
func (r *RunRepository) Get(ctx context.Context, id int64) (agent.Run, error) {
	run, err := scanRun(r.pool.QueryRow(ctx,
		`SELECT `+runColumnList+` FROM agent_runs WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return agent.Run{}, agent.ErrNotFound
		}
		return agent.Run{}, fmt.Errorf("postgres: get agent run: %w", err)
	}
	return run, nil
}

// Active returns the session's running run. The partial unique index ensures
// there is at most one.
func (r *RunRepository) Active(ctx context.Context, sessionID int64) (agent.Run, error) {
	run, err := scanRun(r.pool.QueryRow(ctx,
		`SELECT `+runColumnList+` FROM agent_runs WHERE session_id = $1 AND status = 'running' ORDER BY id DESC LIMIT 1`,
		sessionID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return agent.Run{}, agent.ErrNotFound
		}
		return agent.Run{}, fmt.Errorf("postgres: get active agent run: %w", err)
	}
	return run, nil
}

// Latest returns the session's newest terminal run. A running run is reported
// by Active instead, so it is never returned here.
func (r *RunRepository) Latest(ctx context.Context, sessionID int64) (agent.Run, error) {
	run, err := scanRun(r.pool.QueryRow(ctx,
		`SELECT `+runColumnList+` FROM agent_runs WHERE session_id = $1 AND status <> 'running' ORDER BY id DESC LIMIT 1`,
		sessionID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return agent.Run{}, agent.ErrNotFound
		}
		return agent.Run{}, fmt.Errorf("postgres: get latest agent run: %w", err)
	}
	return run, nil
}

// UpdateProgress records the completed turns and their accumulated usage.
func (r *RunRepository) UpdateProgress(ctx context.Context, runID int64, turns int, usage agent.Usage) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE agent_runs
		    SET turns = $2, input_tokens = $3, output_tokens = $4, reasoning_tokens = $5
		  WHERE id = $1`,
		runID, turns, usage.PromptTokens, usage.CompletionTokens, usage.ReasoningTokens)
	if err != nil {
		return fmt.Errorf("postgres: update agent run progress: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return agent.ErrNotFound
	}
	return nil
}

// Finish writes the terminal fields and ended_at. The result is validated
// before the write because status is NOT NULL and terminal_reason has a CHECK
// constraint.
func (r *RunRepository) Finish(ctx context.Context, runID int64, result agent.RunResult) error {
	if err := result.Validate(); err != nil {
		return err
	}
	var terminalReason any
	if result.TerminalReason != "" {
		terminalReason = string(result.TerminalReason)
	}
	tag, err := r.pool.Exec(ctx,
		`UPDATE agent_runs
		    SET status = $2, terminal_reason = $3, abort_code = $4, abort_message = $5,
		        error_code = $6, error_message = $7, turns = $8, input_tokens = $9,
		        output_tokens = $10, reasoning_tokens = $11, last_tool_name = $12,
		        last_tool_pending = $13, ended_at = now()
		  WHERE id = $1`,
		runID,
		string(result.Status),
		terminalReason,
		nullIfEmpty(result.AbortCode),
		nullIfEmpty(result.AbortMessage),
		nullIfEmpty(result.ErrorCode),
		nullIfEmpty(result.ErrorMessage),
		result.Turns,
		result.Usage.PromptTokens,
		result.Usage.CompletionTokens,
		result.Usage.ReasoningTokens,
		nullIfEmpty(result.LastToolName),
		result.LastToolPending,
	)
	if err != nil {
		return fmt.Errorf("postgres: finish agent run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return agent.ErrNotFound
	}
	return nil
}

// MarkRunningInterrupted is the process-restart recovery hook: every run still
// marked running belongs to a process that is gone, because a run is only
// created and finished by the live in-process runner.
func (r *RunRepository) MarkRunningInterrupted(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE agent_runs
		    SET status = 'interrupted', terminal_reason = 'interrupted', ended_at = now()
		  WHERE status = 'running'`)
	if err != nil {
		return 0, fmt.Errorf("postgres: mark running agent runs interrupted: %w", err)
	}
	return tag.RowsAffected(), nil
}
