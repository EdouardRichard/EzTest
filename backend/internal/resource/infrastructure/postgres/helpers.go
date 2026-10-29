// Package postgres implements the resource context's repository ports with
// PostgreSQL. Every repository in this package shares the error translation,
// nullable-column and JSONB helpers in this file.
package postgres

import (
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

const (
	// uniqueViolationCode is PostgreSQL's SQLSTATE for unique_violation.
	uniqueViolationCode = "23505"
	// foreignKeyViolationCode is PostgreSQL's SQLSTATE for
	// foreign_key_violation.
	foreignKeyViolationCode = "23503"
)

// rowScanner is the subset of pgx.Row and pgx.Rows used to scan one row.
type rowScanner interface {
	Scan(dest ...any) error
}

// mapNotFound translates pgx.ErrNoRows into the domain's not-found error for
// the resource named by the caller; any other error is returned unchanged.
func mapNotFound(err error, resource string, id int64) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NewNotFound(resource, id)
	}
	return err
}

// mapWriteError translates the constraint violations every resource write can
// raise: a unique violation becomes a 409 duplicate_key conflict and a foreign
// key violation becomes a 409 dependency error. Any other error is returned
// unchanged. duplicateMessage is the client-facing conflict message.
func mapWriteError(err error, duplicateMessage string) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case uniqueViolationCode:
		return domain.NewConflict("duplicate_key", duplicateMessage)
	case foreignKeyViolationCode:
		return domain.NewDependency("has_dependencies", "a referenced resource is missing or is still in use")
	default:
		return err
	}
}

// mapLeaseConflict translates the uq_session_active_lease unique violation into
// a lease conflict. Any other error is returned unchanged.
func mapLeaseConflict(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode {
		return domain.NewLeaseConflict("session already has an active lease")
	}
	return err
}

// isUniqueViolation reports whether err is a PostgreSQL unique violation.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode
}

// nullIfEmpty maps the domain's empty string to SQL NULL, for nullable text
// columns such as failure reasons or terminal timestamps.
func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// encodeJSON marshals value for a JSONB parameter. A nil value becomes SQL
// NULL; a non-nil value is returned as a string so pgx can send it to a JSONB
// column without any extra type registration.
func encodeJSON(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}
