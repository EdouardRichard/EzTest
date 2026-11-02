package deliveryapp

import (
	"errors"
	"fmt"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// Application-level re-exports of the delivery domain sentinels. Consumers of
// the use-case layer classify failures through these names (and through
// errors.Is), so the HTTP layer never has to import a repository adapter or
// inspect driver errors.
var (
	// ErrInvalidArgument reports a malformed issue command or candidate input.
	ErrInvalidArgument = delivery.ErrInvalidArgument
	// ErrStateConflict reports an illegal issue lifecycle transition, a stale
	// expected_revision, or an invalid merge target/cycle.
	ErrStateConflict = delivery.ErrStateConflict
	// ErrNotFound reports a missing issue.
	ErrNotFound = delivery.ErrNotFound
)

// Stable state-conflict reasons of spec §5.2 (API details.reason subcodes).
const (
	// IssueConflictNotOpen marks a confirm/dismiss of an issue that is not open.
	IssueConflictNotOpen = "issue_not_open"
	// IssueConflictNotReopenable marks a reopen of an issue that is not
	// confirmed or dismissed (merged is terminal in P4).
	IssueConflictNotReopenable = "issue_not_reopenable"
)

// IssueStateConflictError is an illegal issue transition with its stable
// details.reason subcode. It unwraps to ErrStateConflict so callers can map it
// to a 409 while preserving the reason for the API envelope.
type IssueStateConflictError struct {
	Reason string
}

// Error implements error.
func (e *IssueStateConflictError) Error() string {
	if e == nil || e.Reason == "" {
		return "delivery: issue state conflict"
	}
	return "delivery: issue state conflict: " + e.Reason
}

// Unwrap exposes ErrStateConflict to errors.Is.
func (e *IssueStateConflictError) Unwrap() error { return delivery.ErrStateConflict }

// invalidArgumentf builds an ErrInvalidArgument with operation context.
func invalidArgumentf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", delivery.ErrInvalidArgument, fmt.Sprintf(format, args...))
}

// mapIssueRepositoryError adds operation context to unexpected repository
// failures and passes the known delivery sentinels (including *MergeError and
// *IssueStateConflictError) through unchanged so errors.Is/errors.As keep
// working at the API boundary.
func mapIssueRepositoryError(op string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, delivery.ErrInvalidArgument),
		errors.Is(err, delivery.ErrStateConflict),
		errors.Is(err, delivery.ErrNotFound):
		return err
	default:
		return fmt.Errorf("%s: %w", op, err)
	}
}

// issueServiceNotConfigured reports a missing consumer port. It is a wiring
// bug, not a business failure.
func issueServiceNotConfigured(kind string) error {
	return errors.New("issue service is missing its " + kind)
}
