// Package platform holds the cross-cutting HTTP conventions shared by every
// EzTest context: the error envelope, cursor pagination, JSON body decoding and
// request IDs. It contains no business logic.
package platform

import (
	"context"
	"encoding/json"
	"net/http"
)

// RequestIDHeader is the HTTP header that carries the request ID on requests
// and responses.
const RequestIDHeader = "X-Request-ID"

// API error codes from the design's §9.1 and §8.5. Handlers must use these
// constants instead of ad-hoc strings so clients can rely on a closed set.
const (
	CodeInvalidArgument     = "invalid_argument"
	CodeUnauthenticated     = "unauthenticated"
	CodeForbidden           = "forbidden"
	CodeNotFound            = "not_found"
	CodeConflict            = "conflict"
	CodeIdempotencyConflict = "idempotency_conflict"
	CodeRateLimited         = "rate_limited"
	CodeInternal            = "internal"
	CodeUnavailable         = "unavailable"
	CodeStorageCorrupted    = "storage_corrupted"

	CodeLLMNotConfigured   = "llm_not_configured"
	CodeLLMAuthFailed      = "llm_auth_failed"
	CodeLLMRateLimited     = "llm_rate_limited"
	CodeLLMBadRequest      = "llm_bad_request"
	CodeLLMUpstreamError   = "llm_upstream_error"
	CodeLLMTimeout         = "llm_timeout"
	CodeLLMInvalidResponse = "llm_invalid_response"
)

// APIError is the JSON shape of one error inside the shared envelope:
//
//	{"error":{"code":...,"message":...,"request_id":...,"retryable":...,"details":{}}}
type APIError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details"`
}

type errorEnvelope struct {
	Error APIError `json:"error"`
}

// ErrorOption customises an APIError written by WriteError.
type ErrorOption func(*APIError)

// WithDetails attaches non-sensitive, structured error details.
func WithDetails(details map[string]any) ErrorOption {
	return func(err *APIError) { err.Details = details }
}

// WithRetryable marks whether the caller may safely retry the failed request.
func WithRetryable(retryable bool) ErrorOption {
	return func(err *APIError) { err.Retryable = retryable }
}

// WriteError writes the shared JSON error envelope. The request ID comes from
// the request context first and from the response header as a fallback, so the
// envelope, the X-Request-ID header and the logs always agree.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string, opts ...ErrorOption) {
	apiErr := APIError{
		Code:    code,
		Message: message,
		Details: map[string]any{},
	}
	if r != nil {
		apiErr.RequestID = RequestID(r.Context())
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&apiErr)
		}
	}
	if apiErr.RequestID == "" {
		apiErr.RequestID = w.Header().Get(RequestIDHeader)
	}
	if apiErr.Details == nil {
		apiErr.Details = map[string]any{}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if apiErr.RequestID != "" {
		w.Header().Set(RequestIDHeader, apiErr.RequestID)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: apiErr})
}

// requestIDContextKey is the private context key that carries the request ID.
type requestIDContextKey struct{}

// WithRequestID stores id in ctx as the request ID for this request.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDContextKey{}, id)
}

// RequestID returns the request ID stored by WithRequestID, or "" when the
// context carries none.
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}
