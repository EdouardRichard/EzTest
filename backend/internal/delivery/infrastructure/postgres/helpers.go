package deliverypg

import (
	"context"
	"errors"
	"fmt"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// rowScanner is satisfied by pgx.Row and pgx.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// execer is satisfied by *pgxpool.Pool and pgx.Tx.
type execer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// ErrNotFound reports a missing delivery_issue row. It wraps both the delivery
// domain sentinel and pgx.ErrNoRows: callers classify a miss with
// errors.Is(err, delivery.ErrNotFound) without importing this adapter, while
// errors.Is(err, pgx.ErrNoRows) keeps working. The adapter-local name is kept
// for compatibility with earlier call sites.
var ErrNotFound = fmt.Errorf("%w: %w", delivery.ErrNotFound, pgx.ErrNoRows)

// wrapDB adds operation context and maps pgx's empty result onto ErrNotFound.
func wrapDB(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, ErrNotFound)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// isNoRows reports pgx's empty-result sentinel.
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// effectiveLimit bounds one descending-ID page. The delivery list endpoints use
// the same 20/100 defaults as the other contexts.
func effectiveLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > 100 {
		return 100
	}
	return limit
}

// actorKind returns the actor kind or the caller's default when unset.
func actorKind(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// statusValue renders an optional status for a nullable TEXT parameter.
func statusValue(status *delivery.IssueStatus) any {
	if status == nil {
		return nil
	}
	return string(*status)
}

// actorIDPtr renders a logical identity parameter, treating non-positive IDs as
// absent (system writes have no actor).
func actorIDPtr(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

// contextOrBackground keeps the repository usable from callers that pass a nil
// context, matching the execution repository convention.
func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// nowUTC is the single timestamp source of the adapter. Business time (events,
// audit) is supplied by the application clock; row timestamps are infrastructure
// metadata.
func nowUTC() time.Time { return time.Now().UTC() }
