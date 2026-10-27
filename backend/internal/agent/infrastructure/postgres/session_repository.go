package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// sessionListColumns is the session projection of the list query. It is
// scanned before the two joined run projections.
const sessionListColumns = `s.id, s.profile_id, p.key, s.title, s.created_by, s.created_at, s.updated_at, s.last_activity_at`

// SessionRepository implements domain.SessionRepository on a pgx pool.
type SessionRepository struct {
	pool *pgxpool.Pool
}

// NewSessionRepository returns a session repository backed by pool.
func NewSessionRepository(pool *pgxpool.Pool) *SessionRepository {
	return &SessionRepository{pool: pool}
}

// sessionSelect joins the profile so every returned session carries ProfileKey.
const sessionSelect = `SELECT s.id, s.profile_id, p.key, s.title, s.created_by, s.created_at, s.updated_at, s.last_activity_at
	FROM agent_sessions s
	JOIN agent_profiles p ON p.id = s.profile_id`

// scanSession scans one session row in the order Get/Create/UpdateTitle return.
func scanSession(row rowScanner) (agent.Session, error) {
	var session agent.Session
	if err := row.Scan(
		&session.ID,
		&session.ProfileID,
		&session.ProfileKey,
		&session.Title,
		&session.CreatedBy,
		&session.CreatedAt,
		&session.UpdatedAt,
		&session.LastActivityAt,
	); err != nil {
		return agent.Session{}, err
	}
	return session, nil
}

// joinedRun receives the nullable columns of one LEFT JOIN LATERAL run. A NULL
// id means the lateral join produced no row.
type joinedRun struct {
	id              *int64
	sessionID       *int64
	status          *string
	terminalReason  *string
	abortCode       *string
	abortMessage    *string
	errorCode       *string
	errorMessage    *string
	turns           *int
	inputTokens     *int64
	outputTokens    *int64
	reasoningTokens *int64
	lastToolName    *string
	lastToolPending *bool
	startedAt       *time.Time
	endedAt         *time.Time
}

// destinations returns the scan targets of one joined run in runColumnList
// order.
func (j *joinedRun) destinations() []any {
	return []any{
		&j.id,
		&j.sessionID,
		&j.status,
		&j.terminalReason,
		&j.abortCode,
		&j.abortMessage,
		&j.errorCode,
		&j.errorMessage,
		&j.turns,
		&j.inputTokens,
		&j.outputTokens,
		&j.reasoningTokens,
		&j.lastToolName,
		&j.lastToolPending,
		&j.startedAt,
		&j.endedAt,
	}
}

// run converts the joined row to a domain run, or nil when the join matched no
// row.
func (j *joinedRun) run() *agent.Run {
	if j.id == nil {
		return nil
	}
	run := &agent.Run{ID: *j.id}
	if j.sessionID != nil {
		run.SessionID = *j.sessionID
	}
	if j.status != nil {
		run.Status = agent.RunStatus(*j.status)
	}
	if j.terminalReason != nil {
		run.TerminalReason = agent.TerminalReason(*j.terminalReason)
	}
	if j.abortCode != nil {
		run.AbortCode = *j.abortCode
	}
	if j.abortMessage != nil {
		run.AbortMessage = *j.abortMessage
	}
	if j.errorCode != nil {
		run.ErrorCode = *j.errorCode
	}
	if j.errorMessage != nil {
		run.ErrorMessage = *j.errorMessage
	}
	if j.turns != nil {
		run.Turns = *j.turns
	}
	if j.inputTokens != nil {
		run.InputTokens = *j.inputTokens
	}
	if j.outputTokens != nil {
		run.OutputTokens = *j.outputTokens
	}
	if j.reasoningTokens != nil {
		run.ReasoningTokens = *j.reasoningTokens
	}
	if j.lastToolName != nil {
		run.LastToolName = *j.lastToolName
	}
	if j.lastToolPending != nil {
		run.LastToolPending = *j.lastToolPending
	}
	if j.startedAt != nil {
		run.StartedAt = *j.startedAt
	}
	run.EndedAt = j.endedAt
	return run
}

// List returns a page of session summaries ordered by ID descending. The two
// lateral joins fetch the running run and the latest run for each session
// without a second round trip; the profile join supplies ProfileKey.
func (r *SessionRepository) List(ctx context.Context, cursor int64, limit int, profileID *int64) ([]agent.SessionSummary, bool, error) {
	if limit <= 0 {
		return nil, false, fmt.Errorf("postgres: list agent sessions: limit must be positive")
	}
	query := fmt.Sprintf(`SELECT %s, %s, %s
	FROM agent_sessions s
	JOIN agent_profiles p ON p.id = s.profile_id
	LEFT JOIN LATERAL (
		SELECT * FROM agent_runs
		WHERE session_id = s.id AND status = 'running'
		ORDER BY id DESC LIMIT 1
	) ar ON true
	LEFT JOIN LATERAL (
		SELECT * FROM agent_runs
		WHERE session_id = s.id
		ORDER BY id DESC LIMIT 1
	) lr ON true
	WHERE ($1 = 0 OR s.id < $1)
	  AND ($2::bigint IS NULL OR s.profile_id = $2)
	ORDER BY s.id DESC
	LIMIT $3`,
		sessionListColumns, runProjection("ar"), runProjection("lr"))

	rows, err := r.pool.Query(ctx, query, cursor, profileID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list agent sessions: %w", err)
	}
	defer rows.Close()

	summaries := make([]agent.SessionSummary, 0, limit+1)
	for rows.Next() {
		var (
			summary agent.SessionSummary
			active  joinedRun
			last    joinedRun
		)
		destinations := []any{
			&summary.Session.ID,
			&summary.Session.ProfileID,
			&summary.Session.ProfileKey,
			&summary.Session.Title,
			&summary.Session.CreatedBy,
			&summary.Session.CreatedAt,
			&summary.Session.UpdatedAt,
			&summary.Session.LastActivityAt,
		}
		destinations = append(destinations, active.destinations()...)
		destinations = append(destinations, last.destinations()...)
		if err := rows.Scan(destinations...); err != nil {
			return nil, false, fmt.Errorf("postgres: scan agent session: %w", err)
		}
		summary.ActiveRun = active.run()
		summary.LastRun = last.run()
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("postgres: list agent sessions: %w", err)
	}
	hasMore := len(summaries) > limit
	if hasMore {
		summaries = summaries[:limit]
	}
	return summaries, hasMore, nil
}

// Create inserts a session and returns it with ProfileKey. An unknown profile
// is reported as ErrNotFound (the profile_id foreign key rejects it).
func (r *SessionRepository) Create(ctx context.Context, profileID int64, title string, createdBy *int64) (agent.Session, error) {
	const query = `WITH ins AS (
		INSERT INTO agent_sessions (profile_id, title, created_by)
		VALUES ($1, $2, $3)
		RETURNING id, profile_id, title, created_by, created_at, updated_at, last_activity_at
	)
	SELECT ins.id, ins.profile_id, p.key, ins.title, ins.created_by, ins.created_at, ins.updated_at, ins.last_activity_at
	FROM ins
	JOIN agent_profiles p ON p.id = ins.profile_id`
	session, err := scanSession(r.pool.QueryRow(ctx, query, profileID, title, createdBy))
	if err != nil {
		if mapped := mapForeignKeyViolation(err); mapped != nil {
			return agent.Session{}, mapped
		}
		return agent.Session{}, fmt.Errorf("postgres: create agent session: %w", err)
	}
	return session, nil
}

// Get returns one session with ProfileKey.
func (r *SessionRepository) Get(ctx context.Context, id int64) (agent.Session, error) {
	session, err := scanSession(r.pool.QueryRow(ctx, sessionSelect+` WHERE s.id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return agent.Session{}, agent.ErrNotFound
		}
		return agent.Session{}, fmt.Errorf("postgres: get agent session: %w", err)
	}
	return session, nil
}

// UpdateTitle changes the session title and returns the stored row.
func (r *SessionRepository) UpdateTitle(ctx context.Context, id int64, title string) (agent.Session, error) {
	const query = `WITH upd AS (
		UPDATE agent_sessions
		   SET title = $2, updated_at = now()
		 WHERE id = $1
		RETURNING id, profile_id, title, created_by, created_at, updated_at, last_activity_at
	)
	SELECT upd.id, upd.profile_id, p.key, upd.title, upd.created_by, upd.created_at, upd.updated_at, upd.last_activity_at
	FROM upd
	JOIN agent_profiles p ON p.id = upd.profile_id`
	session, err := scanSession(r.pool.QueryRow(ctx, query, id, title))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return agent.Session{}, agent.ErrNotFound
		}
		return agent.Session{}, fmt.Errorf("postgres: update agent session title: %w", err)
	}
	return session, nil
}

// Delete removes a session. Messages and runs cascade in the schema.
func (r *SessionRepository) Delete(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM agent_sessions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("postgres: delete agent session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return agent.ErrNotFound
	}
	return nil
}
