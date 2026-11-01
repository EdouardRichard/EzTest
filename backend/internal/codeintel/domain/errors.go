package codeintel

import "errors"

// Domain errors. The application layer maps them to stable HTTP codes:
// ErrNotFound→404, ErrConflict→409, ErrInvalid→422, ErrUnavailable→503.
var (
	ErrNotFound    = errors.New("codeintel: not found")
	ErrConflict    = errors.New("codeintel: conflict")
	ErrInvalid     = errors.New("codeintel: invalid")
	ErrUnavailable = errors.New("codeintel: unavailable")
)
