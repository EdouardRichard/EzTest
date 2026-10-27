package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// clarificationSessionColumnList is the shared column order of every
// clarification session query and the order scanClarificationSession expects.
const clarificationSessionColumnList = `id, version_id, status, agent_session_id,
	context_summary, summary_updated_at, started_by, started_at, ended_at, ended_reason`

// clarificationQuestionColumnList is the shared column order of every
// clarification question query and the order scanQuestion expects.
const clarificationQuestionColumnList = `id, version_id, requirement_id, session_id, question,
	status, answer, source, answered_via, answered_by, answered_at, created_by,
	created_at, updated_at`

// decisionColumnList is the shared column order of every decision query and the
// order scanDecision expects.
const decisionColumnList = `id, version_id, requirement_id, question_id, topic, decision,
	rationale, status, source, proposed_by, decided_by, decided_at, created_at, updated_at`

// PostgreSQL error codes and constraint names classified by this repository.
const (
	clarificationUniqueViolationCode     = "23505"
	clarificationForeignKeyViolationCode = "23503"
	clarificationCheckViolationCode      = "23514"

	clarificationActiveSessionIndex = "uq_clarification_active"

	clarificationSessionsPrimaryKey  = "clarification_sessions_pkey"
	clarificationQuestionsPrimaryKey = "clarification_questions_pkey"
	decisionsPrimaryKey              = "decisions_pkey"
)

// ClarificationRepository implements domain.ClarificationRepository on a pgx
// pool. Multi-row writes (creating an accepted decision and accepting a
// proposed one, both of which may answer a linked question) run in one
// transaction.
type ClarificationRepository struct {
	pool *pgxpool.Pool
}

// NewClarificationRepository returns a clarification repository backed by pool.
func NewClarificationRepository(pool *pgxpool.Pool) *ClarificationRepository {
	return &ClarificationRepository{pool: pool}
}

var _ domain.ClarificationRepository = (*ClarificationRepository)(nil)

// scanClarificationSession scans one row in clarificationSessionColumnList
// order.
func scanClarificationSession(row requirementRowScanner) (domain.ClarificationSession, error) {
	var session domain.ClarificationSession
	if err := row.Scan(
		&session.ID,
		&session.VersionID,
		&session.Status,
		&session.AgentSessionID,
		&session.ContextSummary,
		&session.SummaryUpdatedAt,
		&session.StartedBy,
		&session.StartedAt,
		&session.EndedAt,
		&session.EndedReason,
	); err != nil {
		return domain.ClarificationSession{}, err
	}
	return session, nil
}

// scanQuestion scans one row in clarificationQuestionColumnList order.
func scanQuestion(row requirementRowScanner) (domain.Question, error) {
	var question domain.Question
	if err := row.Scan(
		&question.ID,
		&question.VersionID,
		&question.RequirementID,
		&question.SessionID,
		&question.Question,
		&question.Status,
		&question.Answer,
		&question.Source,
		&question.AnsweredVia,
		&question.AnsweredBy,
		&question.AnsweredAt,
		&question.CreatedBy,
		&question.CreatedAt,
		&question.UpdatedAt,
	); err != nil {
		return domain.Question{}, err
	}
	return question, nil
}

// scanDecision scans one row in decisionColumnList order.
func scanDecision(row requirementRowScanner) (domain.Decision, error) {
	var decision domain.Decision
	if err := row.Scan(
		&decision.ID,
		&decision.VersionID,
		&decision.RequirementID,
		&decision.QuestionID,
		&decision.Topic,
		&decision.Decision,
		&decision.Rationale,
		&decision.Status,
		&decision.Source,
		&decision.ProposedBy,
		&decision.DecidedBy,
		&decision.DecidedAt,
		&decision.CreatedAt,
		&decision.UpdatedAt,
	); err != nil {
		return domain.Decision{}, err
	}
	return decision, nil
}

// clarificationSessionCreateError translates session insert failures into domain
// errors: a duplicate active session is the partial-index conflict, a missing
// version is a not-found, a duplicate explicit id a conflict.
func clarificationSessionCreateError(err error, versionID int64) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case clarificationUniqueViolationCode:
			if pgErr.ConstraintName == clarificationActiveSessionIndex {
				return domain.NewConflict("clarification_session_active",
					"the version already has an active clarification session")
			}
			return domain.NewConflict("duplicate_key", "a clarification session with this id already exists")
		case clarificationForeignKeyViolationCode:
			return domain.NewNotFound("version", versionID)
		case clarificationCheckViolationCode:
			return domain.NewInvalid("", "invalid_clarification_session",
				"clarification session violates the session constraints")
		}
	}
	return fmt.Errorf("postgres: create clarification session: %w", err)
}

// clarificationQuestionWriteError translates question insert/update failures
// into the stable spec §7.1 reasons: duplicate ids, missing version/requirement/
// session references and the CHECK constraint backstops.
func clarificationQuestionWriteError(err error, operation string, question *domain.Question) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case clarificationUniqueViolationCode:
			if pgErr.ConstraintName == clarificationQuestionsPrimaryKey {
				return domain.NewConflict("duplicate_key", "a clarification question with this id already exists")
			}
			return domain.NewConflict("duplicate_key", "a clarification question already exists")
		case clarificationForeignKeyViolationCode:
			switch pgErr.ConstraintName {
			case "clarification_questions_requirement_id_fkey":
				if question.RequirementID != nil {
					return domain.NewNotFound("requirement", *question.RequirementID)
				}
				return domain.NewNotFound("requirement", 0)
			case "clarification_questions_session_id_fkey":
				if question.SessionID != nil {
					return domain.NewNotFound("clarification session", *question.SessionID)
				}
				return domain.NewNotFound("clarification session", 0)
			default:
				return domain.NewNotFound("version", question.VersionID)
			}
		case clarificationCheckViolationCode:
			switch pgErr.ConstraintName {
			case "clarification_questions_status_check":
				return domain.NewInvalid("status", "invalid_status", "question status is not allowed")
			case "clarification_questions_question_check":
				return domain.NewInvalid("question", "invalid_question", "question text is required")
			case "clarification_questions_answered_via_check":
				return domain.NewInvalid("answeredVia", "invalid_answered_via", "answeredVia must be ui or agent")
			default:
				return domain.NewInvalid("answer", "invalid_answer",
					"an answered question requires a non-empty answer")
			}
		}
	}
	return fmt.Errorf("postgres: %s clarification question: %w", operation, err)
}

// decisionWriteError translates decision insert/update failures into the stable
// spec §7.1 reasons.
func decisionWriteError(err error, operation string, decision *domain.Decision) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case clarificationUniqueViolationCode:
			if pgErr.ConstraintName == decisionsPrimaryKey {
				return domain.NewConflict("duplicate_key", "a decision with this id already exists")
			}
			return domain.NewConflict("duplicate_key", "a decision already exists")
		case clarificationForeignKeyViolationCode:
			switch pgErr.ConstraintName {
			case "decisions_requirement_id_fkey":
				if decision.RequirementID != nil {
					return domain.NewNotFound("requirement", *decision.RequirementID)
				}
				return domain.NewNotFound("requirement", 0)
			case "decisions_question_id_fkey":
				if decision.QuestionID != nil {
					return domain.NewNotFound("question", *decision.QuestionID)
				}
				return domain.NewNotFound("question", 0)
			default:
				return domain.NewNotFound("version", decision.VersionID)
			}
		case clarificationCheckViolationCode:
			switch pgErr.ConstraintName {
			case "decisions_status_check":
				return domain.NewInvalid("status", "invalid_status", "decision status is not allowed")
			case "decisions_topic_check":
				return domain.NewInvalid("topic", "invalid_topic", "decision topic is required")
			default:
				return domain.NewInvalid("decision", "invalid_decision", "decision text is required")
			}
		}
	}
	return fmt.Errorf("postgres: %s decision: %w", operation, err)
}

// CreateSession inserts session with its explicit Snowflake id and writes the
// stored row (server timestamps included) back to it.
func (r *ClarificationRepository) CreateSession(ctx context.Context, session *domain.ClarificationSession) error {
	const query = `INSERT INTO clarification_sessions
		(id, version_id, status, agent_session_id, context_summary, started_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING ` + clarificationSessionColumnList
	created, err := scanClarificationSession(r.pool.QueryRow(ctx, query,
		session.ID, session.VersionID, session.Status, session.AgentSessionID,
		session.ContextSummary, session.StartedBy,
	))
	if err != nil {
		return clarificationSessionCreateError(err, session.VersionID)
	}
	*session = created
	return nil
}

// GetActiveSession returns the version's active session. A version without one
// returns (nil, nil): an inactive version is a normal state, not an error.
func (r *ClarificationRepository) GetActiveSession(ctx context.Context, versionID int64) (*domain.ClarificationSession, error) {
	session, err := scanClarificationSession(r.pool.QueryRow(ctx,
		`SELECT `+clarificationSessionColumnList+`
		 FROM clarification_sessions
		 WHERE version_id = $1 AND status = 'active'
		 ORDER BY id DESC LIMIT 1`, versionID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("postgres: get active clarification session: %w", err)
	}
	return &session, nil
}

// GetSession returns one session by id.
func (r *ClarificationRepository) GetSession(ctx context.Context, id int64) (*domain.ClarificationSession, error) {
	session, err := scanClarificationSession(r.pool.QueryRow(ctx,
		`SELECT `+clarificationSessionColumnList+` FROM clarification_sessions WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.NewNotFound("clarification session", id)
		}
		return nil, fmt.Errorf("postgres: get clarification session: %w", err)
	}
	return &session, nil
}

// CompleteSession completes one active session with a status compare-and-swap.
func (r *ClarificationRepository) CompleteSession(ctx context.Context, id int64, actor int64) (*domain.ClarificationSession, error) {
	completed, err := scanClarificationSession(r.pool.QueryRow(ctx,
		`UPDATE clarification_sessions
		 SET status = 'completed', ended_at = now()
		 WHERE id = $1 AND status = 'active'
		 RETURNING `+clarificationSessionColumnList, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, r.clarificationSessionCompletionConflict(ctx, id)
		}
		return nil, fmt.Errorf("postgres: complete clarification session: %w", err)
	}
	return &completed, nil
}

// clarificationSessionCompletionConflict classifies a zero-row completion: a
// missing row is a not-found, a non-active row the spec's
// clarification_session_not_active conflict.
func (r *ClarificationRepository) clarificationSessionCompletionConflict(ctx context.Context, id int64) error {
	var status string
	if err := r.pool.QueryRow(ctx,
		`SELECT status FROM clarification_sessions WHERE id = $1`, id).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NewNotFound("clarification session", id)
		}
		return fmt.Errorf("postgres: read clarification session status after completion CAS: %w", err)
	}
	return domain.NewStateConflict("clarification_session_not_active",
		fmt.Sprintf("clarification session %d is %s", id, status))
}

// UpdateContextSummary writes the active session's context summary.
func (r *ClarificationRepository) UpdateContextSummary(ctx context.Context, versionID int64, summary string, actor int64) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE clarification_sessions
		 SET context_summary = $2, summary_updated_at = now()
		 WHERE version_id = $1 AND status = 'active'`, versionID, summary)
	if err != nil {
		return fmt.Errorf("postgres: update clarification context summary: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewStateConflict("clarification_session_not_active",
			"the version has no active clarification session")
	}
	return nil
}

// CreateQuestion inserts question with its explicit Snowflake id and writes the
// stored row back to it.
func (r *ClarificationRepository) CreateQuestion(ctx context.Context, question *domain.Question) error {
	const query = `INSERT INTO clarification_questions
		(id, version_id, requirement_id, session_id, question, status, answer,
		 source, answered_via, answered_by, answered_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING ` + clarificationQuestionColumnList
	created, err := scanQuestion(r.pool.QueryRow(ctx, query,
		question.ID, question.VersionID, question.RequirementID, question.SessionID,
		question.Question, question.Status, question.Answer, question.Source,
		question.AnsweredVia, question.AnsweredBy, question.AnsweredAt, question.CreatedBy,
	))
	if err != nil {
		return clarificationQuestionWriteError(err, "create", question)
	}
	*question = created
	return nil
}

// GetQuestion returns one question by id.
func (r *ClarificationRepository) GetQuestion(ctx context.Context, id int64) (*domain.Question, error) {
	question, err := scanQuestion(r.pool.QueryRow(ctx,
		`SELECT `+clarificationQuestionColumnList+` FROM clarification_questions WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.NewNotFound("question", id)
		}
		return nil, fmt.Errorf("postgres: get clarification question: %w", err)
	}
	return &question, nil
}

// ListQuestions returns one page ordered by id ascending. An empty status
// matches every status; a nil RequirementID disables the requirement filter. The
// keyset cursor decodes to the last seen id, so a deleted cursor row still pages
// correctly.
func (r *ClarificationRepository) ListQuestions(ctx context.Context, filter domain.QuestionFilter) ([]domain.Question, bool, error) {
	if filter.Page.Limit <= 0 {
		return nil, false, fmt.Errorf("postgres: list clarification questions: limit must be positive")
	}
	cursorID, err := platform.DecodeCursor(filter.Page.Cursor)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list clarification questions: %w", err)
	}
	const query = `SELECT ` + clarificationQuestionColumnList + `
		FROM clarification_questions
		WHERE version_id = $1
		  AND ($2 = '' OR status = $2)
		  AND ($3::bigint IS NULL OR requirement_id = $3)
		  AND ($4::boolean = false OR id > $5::bigint)
		ORDER BY id ASC
		LIMIT $6`
	rows, err := r.pool.Query(ctx, query,
		filter.VersionID, filter.Status, filter.RequirementID, cursorID != 0, cursorID, filter.Page.Limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list clarification questions: %w", err)
	}
	defer rows.Close()

	questions := make([]domain.Question, 0, filter.Page.Limit+1)
	for rows.Next() {
		question, err := scanQuestion(rows)
		if err != nil {
			return nil, false, fmt.Errorf("postgres: scan clarification question: %w", err)
		}
		questions = append(questions, question)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("postgres: list clarification questions: %w", err)
	}
	more := len(questions) > filter.Page.Limit
	if more {
		questions = questions[:filter.Page.Limit]
	}
	return questions, more, nil
}

// UpdateQuestion rewrites the mutable question fields under the fromStatus
// compare-and-swap and writes the stored row back to question.
func (r *ClarificationRepository) UpdateQuestion(ctx context.Context, question *domain.Question, fromStatus string) error {
	const query = `UPDATE clarification_questions
		SET requirement_id = $3, session_id = $4, question = $5, status = $6,
		    answer = $7, answered_via = $8, answered_by = $9, answered_at = $10,
		    updated_at = now()
		WHERE id = $1 AND status = $2
		RETURNING ` + clarificationQuestionColumnList
	updated, err := scanQuestion(r.pool.QueryRow(ctx, query,
		question.ID, fromStatus, question.RequirementID, question.SessionID,
		question.Question, question.Status, question.Answer, question.AnsweredVia,
		question.AnsweredBy, question.AnsweredAt,
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return r.clarificationQuestionUpdateConflict(ctx, question.ID, fromStatus)
		}
		return clarificationQuestionWriteError(err, "update", question)
	}
	*question = updated
	return nil
}

// clarificationQuestionUpdateConflict classifies a zero-row question CAS.
func (r *ClarificationRepository) clarificationQuestionUpdateConflict(ctx context.Context, id int64, fromStatus string) error {
	var status string
	if err := r.pool.QueryRow(ctx,
		`SELECT status FROM clarification_questions WHERE id = $1`, id).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NewNotFound("question", id)
		}
		return fmt.Errorf("postgres: read clarification question status after update CAS: %w", err)
	}
	return domain.NewStateConflict("clarification_question_state_conflict",
		fmt.Sprintf("question %d is %s, not %s", id, status, fromStatus))
}

// CreateDecision inserts decision and, when the decision is accepted and linked
// to an open question, answers that question in the same transaction (spec
// §6.6/§7.6).
func (r *ClarificationRepository) CreateDecision(ctx context.Context, decision *domain.Decision) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin decision transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	const query = `INSERT INTO decisions
		(id, version_id, requirement_id, question_id, topic, decision, rationale,
		 status, source, proposed_by, decided_by, decided_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING ` + decisionColumnList
	created, err := scanDecision(tx.QueryRow(ctx, query,
		decision.ID, decision.VersionID, decision.RequirementID, decision.QuestionID,
		decision.Topic, decision.Decision, decision.Rationale, decision.Status,
		decision.Source, decision.ProposedBy, decision.DecidedBy, decision.DecidedAt,
	))
	if err != nil {
		return decisionWriteError(err, "create", decision)
	}
	if err := answerLinkedQuestion(ctx, tx, &created); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit decision transaction: %w", err)
	}
	*decision = created
	return nil
}

// GetDecision returns one decision by id.
func (r *ClarificationRepository) GetDecision(ctx context.Context, id int64) (*domain.Decision, error) {
	decision, err := scanDecision(r.pool.QueryRow(ctx,
		`SELECT `+decisionColumnList+` FROM decisions WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.NewNotFound("decision", id)
		}
		return nil, fmt.Errorf("postgres: get decision: %w", err)
	}
	return &decision, nil
}

// ListDecisions returns one page ordered by id ascending with an optional status
// filter and keyset cursor on the last seen id.
func (r *ClarificationRepository) ListDecisions(ctx context.Context, filter domain.DecisionFilter) ([]domain.Decision, bool, error) {
	if filter.Page.Limit <= 0 {
		return nil, false, fmt.Errorf("postgres: list decisions: limit must be positive")
	}
	cursorID, err := platform.DecodeCursor(filter.Page.Cursor)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list decisions: %w", err)
	}
	const query = `SELECT ` + decisionColumnList + `
		FROM decisions
		WHERE version_id = $1
		  AND ($2 = '' OR status = $2)
		  AND ($3::boolean = false OR id > $4::bigint)
		ORDER BY id ASC
		LIMIT $5`
	rows, err := r.pool.Query(ctx, query, filter.VersionID, filter.Status, cursorID != 0, cursorID, filter.Page.Limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list decisions: %w", err)
	}
	defer rows.Close()

	decisions := make([]domain.Decision, 0, filter.Page.Limit+1)
	for rows.Next() {
		decision, err := scanDecision(rows)
		if err != nil {
			return nil, false, fmt.Errorf("postgres: scan decision: %w", err)
		}
		decisions = append(decisions, decision)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("postgres: list decisions: %w", err)
	}
	more := len(decisions) > filter.Page.Limit
	if more {
		decisions = decisions[:filter.Page.Limit]
	}
	return decisions, more, nil
}

// UpdateDecision rewrites the decision status under the fromStatus
// compare-and-swap and, when the new status is accepted and the decision is
// linked to an open question, answers that question in the same transaction.
func (r *ClarificationRepository) UpdateDecision(ctx context.Context, decision *domain.Decision, fromStatus string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin decision transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	const query = `UPDATE decisions
		SET status = $3, rationale = $4, decided_by = $5, decided_at = $6,
		    updated_at = now()
		WHERE id = $1 AND status = $2
		RETURNING ` + decisionColumnList
	updated, err := scanDecision(tx.QueryRow(ctx, query,
		decision.ID, fromStatus, decision.Status, decision.Rationale,
		decision.DecidedBy, decision.DecidedAt,
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return r.decisionUpdateConflict(ctx, tx, decision.ID, fromStatus)
		}
		return decisionWriteError(err, "update", decision)
	}
	if err := answerLinkedQuestion(ctx, tx, &updated); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit decision transaction: %w", err)
	}
	*decision = updated
	return nil
}

// decisionUpdateConflict classifies a zero-row decision CAS inside tx.
func (r *ClarificationRepository) decisionUpdateConflict(ctx context.Context, tx pgx.Tx, id int64, fromStatus string) error {
	var status string
	if err := tx.QueryRow(ctx,
		`SELECT status FROM decisions WHERE id = $1`, id).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NewNotFound("decision", id)
		}
		return fmt.Errorf("postgres: read decision status after update CAS: %w", err)
	}
	return domain.NewStateConflict("decision_state_conflict",
		fmt.Sprintf("decision %d is %s, not %s", id, status, fromStatus))
}

// answerLinkedQuestion answers an accepted decision's linked open question in the
// caller's transaction: the decision text becomes the answer, the decider the
// answerer, via ui (spec §6.6). A question that is no longer open is left as-is.
func answerLinkedQuestion(ctx context.Context, tx pgx.Tx, decision *domain.Decision) error {
	if decision.Status != domain.DecisionAccepted || decision.QuestionID == nil {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE clarification_questions
		SET status = 'answered', answer = $2, answered_via = 'ui',
		    answered_by = $3, answered_at = COALESCE($4::timestamptz, now()),
		    updated_at = now()
		WHERE id = $1 AND status = 'open'`,
		*decision.QuestionID, decision.Decision, decision.DecidedBy, decision.DecidedAt,
	); err != nil {
		return fmt.Errorf("postgres: answer linked clarification question: %w", err)
	}
	return nil
}
