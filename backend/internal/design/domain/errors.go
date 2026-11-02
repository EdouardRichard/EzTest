package design

import "errors"

// Domain sentinel errors. Repositories return these and the application layer
// maps them onto the stable platform error envelope.
var (
	// ErrNotFound reports a missing scenario/case/version/round/scope.
	ErrNotFound = errors.New("design: not found")
	// ErrStateConflict reports an invalid state-machine transition (for example
	// confirming a stale case version).
	ErrStateConflict = errors.New("design: state conflict")
	// ErrDuplicate reports a natural-key or unique-constraint collision.
	ErrDuplicate = errors.New("design: duplicate")
)
