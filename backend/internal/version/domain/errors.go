package domain

import "fmt"

// ErrorKind is the coarse class of a domain error. The HTTP layer maps kinds to
// status codes and carries Reason in the shared error envelope's details.
type ErrorKind string

// Domain error kinds.
const (
	KindInvalid       ErrorKind = "invalid"
	KindNotFound      ErrorKind = "not_found"
	KindConflict      ErrorKind = "conflict"
	KindDependency    ErrorKind = "dependency"
	KindStateConflict ErrorKind = "state_conflict"
	// KindForbidden is an authorization denial (HTTP 403). P3 only returns it
	// from the knowledge edit rule (creator/admin); every route-level admin
	// guard is enforced by the identity middleware before the handler runs.
	KindForbidden ErrorKind = "forbidden"
)

// Error is the version context's domain error. Field names the offending
// request field for invalid errors; Reason is the stable details.reason code
// from spec §7.1.
type Error struct {
	Kind    ErrorKind
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
		return string(e.Kind) + ": " + e.Reason
	}
	return string(e.Kind)
}

// NewInvalid returns a validation error (HTTP 422) for field and reason.
func NewInvalid(field, reason, message string) *Error {
	return &Error{Kind: KindInvalid, Field: field, Reason: reason, Message: message}
}

// NewNotFound returns a missing-resource error (HTTP 404).
func NewNotFound(resource string, id int64) *Error {
	return &Error{
		Kind:    KindNotFound,
		Reason:  "not_found",
		Message: fmt.Sprintf("%s %d not found", resource, id),
	}
}

// NewConflict returns a uniqueness/business conflict (HTTP 409).
func NewConflict(reason, message string) *Error {
	return &Error{Kind: KindConflict, Reason: reason, Message: message}
}

// NewStateConflict returns a failed state transition or compare-and-swap (HTTP
// 409). Reason is the stable details.reason code, for example
// version_state_conflict or requirement_state_conflict.
func NewStateConflict(reason, message string) *Error {
	return &Error{Kind: KindStateConflict, Reason: reason, Message: message}
}

// NewDependency returns a deletion blocked by dependents (HTTP 409).
func NewDependency(reason, message string) *Error {
	return &Error{Kind: KindDependency, Reason: reason, Message: message}
}

// NewForbidden returns an authorization denial (HTTP 403). Reason is the stable
// details.reason code, for example knowledge_forbidden.
func NewForbidden(reason, message string) *Error {
	return &Error{Kind: KindForbidden, Reason: reason, Message: message}
}
