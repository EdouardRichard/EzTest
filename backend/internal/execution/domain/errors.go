package domain

import "errors"

// ReasonError is the execution context's stable, machine-readable error. Code
// and Reason are the stable spec §6.4 identifiers; Message is human-readable;
// Retryable marks transient conditions; Details is carried verbatim in the
// shared error envelope's details object.
type ReasonError struct {
	Code      string
	Reason    string
	Message   string
	Retryable bool
	Details   map[string]any
}

// NewReasonError builds a ReasonError carrying all of its fields.
func NewReasonError(code, reason, message string, retryable bool, details map[string]any) *ReasonError {
	return &ReasonError{
		Code:      code,
		Reason:    reason,
		Message:   message,
		Retryable: retryable,
		Details:   details,
	}
}

// Error implements error. It returns code + message so logs stay diagnosable
// while Reason remains the stable machine-readable identifier.
func (e *ReasonError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// Sentinel errors mark the execution context's state conflicts. The
// application layer maps them to ReasonError values with a spec §6.4 reason.
var (
	ErrBatchActive         = errors.New("execution: version already has an active batch")
	ErrAlreadyPaused       = errors.New("execution: batch is already paused")
	ErrNotPaused           = errors.New("execution: batch is not paused")
	ErrBatchFinished       = errors.New("execution: batch is already finished")
	ErrCaseRunTerminal     = errors.New("execution: case run is already terminal")
	ErrBaselineChanged     = errors.New("execution: case baseline changed")
	ErrInvalidCaseIDs      = errors.New("execution: invalid case ids")
	ErrNoCases             = errors.New("execution: no cases selected")
	ErrTooManyCases        = errors.New("execution: too many cases selected")
	ErrEnvironmentRequired = errors.New("execution: environment is required")
)
