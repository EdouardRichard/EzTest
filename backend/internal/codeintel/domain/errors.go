package domain

import "errors"

// Sentinel errors classify codeintel domain failures. Callers classify with
// errors.Is and read the stable details.reason with Reason.
var (
	// ErrInvalid marks a rejected input or state, mapped to HTTP 422.
	ErrInvalid = errors.New("codeintel: invalid")
	// ErrNotFound marks a missing resource, mapped to HTTP 404.
	ErrNotFound = errors.New("codeintel: not found")
	// ErrConflict marks a uniqueness or in-progress conflict, mapped to HTTP
	// 409.
	ErrConflict = errors.New("codeintel: conflict")
)

// Error couples a sentinel Kind with the stable details.reason code from
// design spec §7.1 plus an optional request field.
type Error struct {
	Kind    error
	Field   string
	Reason  string
	Message string
}

// Error implements error.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Reason != "" {
		return e.Kind.Error() + ": " + e.Reason
	}
	return e.Kind.Error()
}

// Unwrap exposes the sentinel so errors.Is works.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Kind
}

// NewInvalid returns a validation error (HTTP 422) for field and reason.
func NewInvalid(field, reason, message string) *Error {
	return &Error{Kind: ErrInvalid, Field: field, Reason: reason, Message: message}
}

// NewNotFound returns a missing-resource error (HTTP 404).
func NewNotFound(reason, message string) *Error {
	return &Error{Kind: ErrNotFound, Reason: reason, Message: message}
}

// NewConflict returns a uniqueness or in-progress error (HTTP 409).
func NewConflict(reason, message string) *Error {
	return &Error{Kind: ErrConflict, Reason: reason, Message: message}
}

// Reason returns the details.reason code carried by err, or "" when err has
// none.
func Reason(err error) string {
	var domainErr *Error
	if errors.As(err, &domainErr) {
		return domainErr.Reason
	}
	return ""
}
