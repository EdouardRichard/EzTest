// Package execution is the P3 execution bounded context. It models intents,
// waves, case runs, four-state verdicts, evidence, executor instances and
// human takeover requests as pure domain types plus deterministic policies
// (aggregation, quota reservation, state machines).
package execution

import "errors"

// Domain persistence/state errors. Repositories translate these from driver
// specific errors so the application layer can map them to the stable API
// envelope without importing pgx.
var (
	// ErrNotFound reports a missing execution row.
	ErrNotFound = errors.New("execution entity not found")
	// ErrDuplicate reports a uniqueness violation (partial unique indexes).
	ErrDuplicate = errors.New("execution entity already exists")
	// ErrStateConflict reports an illegal state transition or stale scope.
	ErrStateConflict = errors.New("execution entity is in an invalid state")
	// ErrVersionCompleted reports that a version close committed before the
	// intent insert took the shared version advisory lock. The intent service
	// reopens the version through the P4 reopen path and retries.
	ErrVersionCompleted = errors.New("execution intent version is completed")
	// ErrRequirementClosed reports that a requirement closure committed before
	// the intent insert took the shared version advisory lock. A closed
	// requirement is terminal for intent creation until it is reopened.
	ErrRequirementClosed = errors.New("execution intent requirement is closed")
	// ErrInvalidVerdict reports an invalid finish candidate.
	ErrInvalidVerdict = errors.New("invalid finish candidate")
)
