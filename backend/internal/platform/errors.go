// Package platform contains the shared, dependency-free kernel: errors,
// request IDs, pagination, validation, clocks and snowflake IDs.
package platform

import (
	"errors"
	"fmt"
	"net/http"
)

// Error is the stable API error shape. HTTP handlers turn it into the shared
// {@code {"error":{...}}} envelope.
type Error struct {
	Status    int
	Code      string
	Message   string
	Retryable bool
	Details   map[string]any
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// NewError builds an error with an explicit status and code.
func NewError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// Invalid is a 400 for a field-level validation failure.
func Invalid(field, message string) *Error {
	return &Error{
		Status:  http.StatusBadRequest,
		Code:    "invalid_argument",
		Message: message,
		Details: map[string]any{"field": field},
	}
}

// Unauthenticated is a 401.
func Unauthenticated() *Error {
	return NewError(http.StatusUnauthorized, "unauthenticated", "authentication required")
}

// Forbidden is a 403.
func Forbidden() *Error {
	return NewError(http.StatusForbidden, "forbidden", "insufficient permissions")
}

// NotFound is a 404 for a missing entity kind.
func NotFound(kind string) *Error {
	return &Error{
		Status:  http.StatusNotFound,
		Code:    "not_found",
		Message: kind + " not found",
		Details: map[string]any{"kind": kind},
	}
}

// Conflict is a 409 for uniqueness/state conflicts.
func Conflict(message string) *Error {
	return NewError(http.StatusConflict, "conflict", message)
}

// StateConflict is a 409 for invalid state transitions.
func StateConflict(message string) *Error {
	return NewError(http.StatusConflict, "state_conflict", message)
}

// Unavailable is a 503 for a temporary dependency failure.
func Unavailable(message string) *Error {
	return &Error{
		Status:    http.StatusServiceUnavailable,
		Code:      "unavailable",
		Message:   message,
		Retryable: true,
	}
}

// AsError unwraps a *Error or maps an unknown error to a safe 500.
func AsError(err error) *Error {
	var target *Error
	if errors.As(err, &target) {
		return target
	}
	return NewError(http.StatusInternalServerError, "internal", "internal server error")
}
