// Package postgres implements the P5 mindmap context's repository ports with
// PostgreSQL. Every write supplies the application-generated Snowflake id
// (D29): the DDL has no sequence or default. Ids, timestamps and JSONB
// documents are persisted exactly as the caller supplies them in the
// fully-populated entity; only the bulk status rewrites fall back to the
// database clock because their port carries no entity to read a timestamp
// from.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// uniqueViolationCode is PostgreSQL's SQLSTATE for unique_violation.
const uniqueViolationCode = "23505"

// querier is the subset of pgxpool.Pool and pgx.Tx that the repositories use.
// A repository built on the pool runs every statement on its own pooled
// connection; one built by GraphTx runs inside the materialization
// transaction. Both concrete types satisfy this interface.
type querier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// rowScanner is the subset of pgx.Row and pgx.Rows used to scan one row.
type rowScanner interface {
	Scan(dest ...any) error
}

// nullableInt renders an optional bigint binding column; nil becomes SQL NULL.
func nullableInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

// nullableString renders an optional text binding column; nil becomes SQL NULL.
func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

// nullableTime renders an optional timestamptz binding column; nil becomes SQL
// NULL.
func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}

// jsonParam renders an optional JSONB document as a query parameter. The
// statements cast the parameter to jsonb explicitly; an absent document is SQL
// NULL.
func jsonParam(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

// isUniqueViolation reports whether err is a PostgreSQL unique violation.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode
}

// uniqueConstraint returns the violated unique constraint or index name, or ""
// when err is not a unique violation. The caller maps the name to the domain
// reason of spec §7.4.
func uniqueConstraint(err error) string {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != uniqueViolationCode {
		return ""
	}
	return pgErr.ConstraintName
}

// notFound builds the resource-scoped not-found error of spec §7.4, for example
// node_not_found.
func notFound(resource string, id int64) error {
	return domain.NewNotFound(resource, id)
}

// revisionConflict is the 409 returned by a failed case_node revision CAS (spec
// §6.5).
func revisionConflict(nodeID int64) error {
	return domain.NewStateConflict("case_revision_conflict",
		fmt.Sprintf("node %d revision does not match", nodeID))
}

// withTx runs fn inside a transaction. When q already is one (a repository
// bound by GraphTx), fn joins that transaction instead of opening a nested one;
// otherwise the repository owns the transaction so a multi-statement rewrite is
// still atomic.
func withTx(ctx context.Context, q querier, fn func(querier) error) error {
	if tx, ok := q.(pgx.Tx); ok {
		return fn(tx)
	}
	pool, ok := q.(*pgxpool.Pool)
	if !ok {
		return fn(q)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin transaction: %w", err)
	}
	defer func() {
		// A commit already closed the transaction; this rollback is then a
		// no-op. WithoutCancel keeps the cleanup working when the caller's
		// context was cancelled.
		_ = tx.Rollback(context.WithoutCancel(ctx))
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit transaction: %w", err)
	}
	return nil
}
