package domain

import "fmt"

// Error kinds. The HTTP layer maps Kind to a status code and carries Reason in
// the shared error envelope's details.reason (spec §7.11).
const (
	KindInvalid       = "invalid"
	KindNotFound      = "not_found"
	KindConflict      = "conflict"
	KindStateConflict = "state_conflict"
)

// Stable details.reason values of the delivery context (spec §7.11). They are
// machine-readable and must not change.
const (
	// ReasonInvalidArgument marks a rejected request field (HTTP 422).
	ReasonInvalidArgument = "invalid_argument"
	// ReasonIssueStateConflict marks a failed issue state transition or CAS
	// revision mismatch (HTTP 409).
	ReasonIssueStateConflict = "issue_state_conflict"
	// ReasonIssueDismissReasonRequired marks a dismissal without a reason
	// (HTTP 422).
	ReasonIssueDismissReasonRequired = "issue_dismiss_reason_required"
	// ReasonIssueRefInvalid marks a missing, foreign or malformed issue
	// reference (HTTP 422).
	ReasonIssueRefInvalid = "issue_ref_invalid"
)

// Error is the delivery context's domain error. Field names the offending
// request field for invalid errors; Reason is the stable details.reason value.
type Error struct {
	Kind    string
	Reason  string
	Field   string
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
		return e.Kind + ": " + e.Reason
	}
	return e.Kind
}

// NewInvalid returns a validation error (HTTP 422) for field and reason.
func NewInvalid(field, reason, message string) *Error {
	return &Error{Kind: KindInvalid, Field: field, Reason: reason, Message: message}
}

// NewNotFound returns a missing-resource error (HTTP 404). The reason is
// "<resource>_not_found" so it matches spec §7.11 (for example
// issue_not_found).
func NewNotFound(resource string, id int64) *Error {
	return &Error{
		Kind:    KindNotFound,
		Reason:  resource + "_not_found",
		Message: fmt.Sprintf("%s %d not found", resource, id),
	}
}

// NewConflict returns a uniqueness or business conflict (HTTP 409).
func NewConflict(reason, message string) *Error {
	return &Error{Kind: KindConflict, Reason: reason, Message: message}
}

// NewStateConflict returns a failed state transition or compare-and-swap
// (HTTP 409), for example issue_state_conflict.
func NewStateConflict(reason, message string) *Error {
	return &Error{Kind: KindStateConflict, Reason: reason, Message: message}
}
