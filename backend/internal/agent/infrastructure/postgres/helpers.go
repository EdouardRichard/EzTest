// Package postgres implements the Agent repository ports with PostgreSQL. All
// queries are parameterized, use the caller's context and map the domain's
// not-found condition from pgx.ErrNoRows; run creation additionally maps the
// running-run partial unique index to domain.ErrRunActive.
package postgres

import (
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

const (
	// uniqueViolationCode is PostgreSQL's SQLSTATE for unique_violation.
	uniqueViolationCode = "23505"
	// foreignKeyViolationCode is PostgreSQL's SQLSTATE for foreign_key_violation.
	foreignKeyViolationCode = "23503"
)

// rowScanner is the subset of pgx.Row and pgx.Rows used to scan one row.
type rowScanner interface {
	Scan(dest ...any) error
}

// mapRunInsertViolation translates the constraint violations CreateTurn can
// raise. A unique violation means another running run already exists for the
// session (the partial unique index uq_agent_runs_running); a foreign key
// violation means the session does not exist.
func mapRunInsertViolation(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil
	}
	switch pgErr.Code {
	case uniqueViolationCode:
		return agent.ErrRunActive
	case foreignKeyViolationCode:
		return agent.ErrNotFound
	default:
		return nil
	}
}

// mapForeignKeyViolation translates a foreign key violation into ErrNotFound.
// It returns nil when err is something else.
func mapForeignKeyViolation(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != foreignKeyViolationCode {
		return nil
	}
	return agent.ErrNotFound
}

// nullIfEmpty maps the domain's empty string to SQL NULL, used by the nullable
// terminal columns of agent_run.
func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// encodeParts marshals message parts to the JSONB text stored in
// agent_message.content. A nil slice is stored as an empty JSON array, never
// as SQL NULL, because the column is NOT NULL.
func encodeParts(parts []agent.Part) (string, error) {
	if parts == nil {
		parts = []agent.Part{}
	}
	encoded, err := json.Marshal(parts)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// encodeToolCalls marshals tool calls to JSONB. No calls means SQL NULL, which
// distinguishes a plain assistant message from one that requested tools.
func encodeToolCalls(calls []agent.ToolCall) (any, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(calls)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

// encodeUsage marshals usage to JSONB, or SQL NULL when absent.
func encodeUsage(usage *agent.Usage) (any, error) {
	if usage == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(usage)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}
