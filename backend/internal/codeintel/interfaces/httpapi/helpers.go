package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

const (
	// reasonGitUnavailable is the design spec §7.1 reason for an operational
	// Git/repository failure (503 unavailable, retryable; ruling R19).
	reasonGitUnavailable = "git_unavailable"
	// reasonQueueFull is the design spec §7.1/§6.8 reason of a full worker
	// queue (429 rate_limited, retryable).
	reasonQueueFull = "analysis_queue_full"
	// reasonInvalidRequest marks a missing required query parameter (400).
	reasonInvalidRequest = "invalid_request"
	// reasonInvalidCursor marks an unparsable opaque pagination cursor (400).
	reasonInvalidCursor = "invalid_cursor"
	// reasonInvalidLimit marks a limit outside 1-100 (400).
	reasonInvalidLimit = "invalid_limit"
	// reasonInvalidLine marks a malformed startLine/endLine query value (400).
	reasonInvalidLine = "invalid_line"
	// reasonInvalidID marks a malformed decimal path id (422).
	reasonInvalidID = "invalid_id"
)

// dataEnvelope is the success shape for a single resource ("data": ...).
type dataEnvelope struct {
	Data any `json:"data"`
}

// pageEnvelope is the success shape of a cursor-paged list (design spec §7.1).
type pageEnvelope struct {
	Data       any    `json:"data"`
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}

// WriteData writes {"data": data} with the given status code.
func WriteData(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, dataEnvelope{Data: data})
}

// WritePage writes {"data": items, "next_cursor": next, "has_more": hasMore}.
func WritePage(w http.ResponseWriter, items any, next string, hasMore bool) {
	writeJSON(w, http.StatusOK, pageEnvelope{Data: items, NextCursor: next, HasMore: hasMore})
}

// writeJSON writes payload as JSON with the shared content type. Encoding
// errors cannot be repaired after the header is written and the payloads here
// are plain DTO structs, so they are ignored like the P2/P3 handlers do.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// writeInternalError writes the generic 500 envelope. The underlying error is
// never echoed: it may carry repository paths or Git output.
func writeInternalError(w http.ResponseWriter, r *http.Request) {
	platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "internal server error")
}

// requestError is a transport-level request failure (a missing or malformed
// query parameter). It carries its own HTTP status because it is not a domain
// validation error: spec §7.1 declares 422 for domain validation while an
// unparsable parameter is a 400.
type requestError struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
}

// Error implements error.
func (e *requestError) Error() string { return e.Message }

// newBadRequest builds a 400 invalid_argument request error with details.
func newBadRequest(field, reason, message string) *requestError {
	details := map[string]any{"message": message}
	if field != "" {
		details["field"] = field
	}
	if reason != "" {
		details["reason"] = reason
	}
	return &requestError{
		Status:  http.StatusBadRequest,
		Code:    platform.CodeInvalidArgument,
		Message: message,
		Details: details,
	}
}

// WriteServiceError maps a service or transport error onto the shared error
// envelope (design spec §7.1):
//
//	requestError              -> its own status (400 invalid_argument)
//	queue full                -> 429 rate_limited (retryable) BEFORE the
//	                             generic conflict mapping
//	domain invalid            -> 422 invalid_argument
//	domain not found          -> 404 not_found
//	domain conflict           -> 409 conflict
//	non-domain (Git/storage)  -> 503 unavailable git_unavailable (retryable)
//
// Domain errors carry details.field/reason/message; the 503 never echoes the
// underlying Git or filesystem detail. Anything with an unknown domain kind is
// an internal failure whose message is never leaked.
func WriteServiceError(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		writeInternalError(w, r)
		return
	}
	var requestErr *requestError
	if errors.As(err, &requestErr) && requestErr != nil {
		platform.WriteError(w, r, requestErr.Status, requestErr.Code, requestErr.Message,
			platform.WithDetails(requestErr.Details))
		return
	}

	// A full worker queue is wrapped in a domain conflict; it must map to 429
	// before the generic conflict branch (design spec §6.8).
	if isQueueFull(err) {
		platform.WriteError(w, r, http.StatusTooManyRequests, platform.CodeRateLimited,
			"the analysis queue is full",
			platform.WithDetails(map[string]any{"reason": reasonQueueFull}),
			platform.WithRetryable(true))
		return
	}

	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr == nil {
		// Operational Git/repository failures stay non-domain so they surface as
		// a retryable 503 git_unavailable instead of a 422 verdict (R19).
		platform.WriteError(w, r, http.StatusServiceUnavailable, platform.CodeUnavailable,
			"code repository is temporarily unavailable",
			platform.WithDetails(map[string]any{"reason": reasonGitUnavailable}),
			platform.WithRetryable(true))
		return
	}

	var status int
	var code string
	switch {
	case errors.Is(err, domain.ErrInvalid):
		status, code = http.StatusUnprocessableEntity, platform.CodeInvalidArgument
	case errors.Is(err, domain.ErrNotFound):
		status, code = http.StatusNotFound, platform.CodeNotFound
	case errors.Is(err, domain.ErrConflict):
		status, code = http.StatusConflict, platform.CodeConflict
	default:
		writeInternalError(w, r)
		return
	}

	message := domainErr.Message
	if message == "" {
		message = http.StatusText(status)
	}
	details := map[string]any{"message": message}
	if domainErr.Field != "" {
		details["field"] = domainErr.Field
	}
	if domainErr.Reason != "" {
		details["reason"] = domainErr.Reason
	}
	platform.WriteError(w, r, status, code, message, platform.WithDetails(details))
}

// isQueueFull reports the retryable full-queue condition. The index/analysis
// services keep application.ErrQueueFull in the chain and the
// analysis_queue_full domain reason, so either signal classifies the failure.
func isQueueFull(err error) bool {
	return errors.Is(err, application.ErrQueueFull) || domain.Reason(err) == reasonQueueFull
}

// ParseQueryLimit parses the shared limit query parameter: 1-100, defaulting to
// 20 when absent (design spec §7.1). Values outside the range are a 400.
func ParseQueryLimit(r *http.Request) (int, error) {
	limit, err := platform.ParseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		return 0, newBadRequest("limit", reasonInvalidLimit,
			"limit must be an integer between 1 and 100")
	}
	return limit, nil
}

// pathCursor reads a raw path cursor (tree/diff: the last path of the previous
// page). The value is opaque to the API but bounded so a hostile cursor cannot
// exceed the domain path bound.
func pathCursor(r *http.Request) (string, error) {
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if len(cursor) > domain.MaxPathChars {
		return "", newBadRequest("cursor", reasonInvalidCursor, "cursor is not a valid pagination cursor")
	}
	return cursor, nil
}

// opaqueCursor reads an opaque id cursor (facts/search keyset pagination) and
// validates its encoding so a malformed value is a 400 instead of a repository
// failure.
func opaqueCursor(r *http.Request) (string, error) {
	cursor := r.URL.Query().Get("cursor")
	if _, err := platform.DecodeCursor(cursor); err != nil {
		return "", newBadRequest("cursor", reasonInvalidCursor, "cursor is not a valid pagination cursor")
	}
	return cursor, nil
}

// ParseOptionalPositiveInt parses one optional positive integer query parameter
// (startLine/endLine). An absent value is 0, which the application service reads
// as "from the first line" / "to the end of the file".
func ParseOptionalPositiveInt(r *http.Request, name string) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, newBadRequest(name, reasonInvalidLine, name+" must be a positive integer")
	}
	return value, nil
}

// requiredQuery returns one required query parameter or a 400 naming it.
func requiredQuery(r *http.Request, name string) (string, error) {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" {
		return "", newBadRequest(name, reasonInvalidRequest, name+" is required")
	}
	return value, nil
}

// PathID parses a positive decimal {name} path value. Codeintel ids are 64-bit
// decimals serialized as strings in URLs, while repoId is a decimal number; both
// are parsed from the raw path value and never through a JSON number.
func PathID(r *http.Request, name string) (int64, error) {
	return ParseDecimalID(r.PathValue(name), name)
}

// ParseDecimalID parses one positive decimal id string. Malformed values are a
// 422 invalid_id domain validation error, matching the P3 path-id rule.
func ParseDecimalID(raw, field string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, domain.NewInvalid(field, reasonInvalidID, field+" must be a positive decimal id")
	}
	return id, nil
}

// ActorID returns the authenticated platform user's id. The server's global
// middleware always stores a user on these routes; an absent one yields 0 so a
// wiring mistake cannot panic (the application services accept the zero actor).
func ActorID(ctx context.Context) int64 {
	user, ok := identityhttp.UserFromContext(ctx)
	if !ok {
		return 0
	}
	return user.ID
}

// DecodeJSON decodes a request body with the shared strict decoder (unknown
// fields and trailing data rejected) under the given size limit. Decoding
// failures are a 400 invalid_argument request error.
func DecodeJSON(r *http.Request, dst any, maxBytes int64) error {
	if err := platform.DecodeJSON(r, dst, maxBytes); err != nil {
		if errors.Is(err, platform.ErrBodyTooLarge) {
			return newBadRequest("body", "body_too_large", "request body is too large")
		}
		return newBadRequest("body", "invalid_json", "request body is not a valid JSON object")
	}
	return nil
}
