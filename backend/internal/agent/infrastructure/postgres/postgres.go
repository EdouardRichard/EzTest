// Package agentpg is the PostgreSQL adapter for the agent runtime bounded
// context. It implements the domain repository ports for the execution task
// board, the version-scoped team room and the agent runtime records.
//
// P1 assumes a single API instance; the repository still enforces lease_epoch
// fencing so a stale owner can never overwrite a newer lease.
package agentpg

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type rowScanner interface {
	Scan(dest ...any) error
}

// wrapDB maps the shared PostgreSQL failures onto domain sentinel errors.
func wrapDB(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return agent.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return agent.ErrConflict
		case "23503": // foreign_key_violation
			return agent.ErrNotFound
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}

// encodeJSONObject renders a value as a JSON object, defaulting to {}.
func encodeJSONObject(field string, value any) (string, error) {
	if value == nil {
		return "{}", nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode %s: %w", field, err)
	}
	return string(raw), nil
}

// encodeJSONArray renders a value as a JSON array, defaulting to [].
func encodeJSONArray(field string, value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode %s: %w", field, err)
	}
	return string(raw), nil
}

func timeOrNow(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value
}
