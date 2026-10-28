package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// dataEnvelope is the success shape for a single resource.
type dataEnvelope struct {
	Data any `json:"data"`
}

// pageEnvelope is the success shape for a cursor-paged list.
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
// are plain structs, so they are ignored like the P2 handlers do.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// requestError is a transport-level request failure (a malformed limit, cursor
// or multipart body). It carries its own HTTP status because it is not a domain
// validation error: spec §7.1 declares 400 for those parameters, while domain
// validation is 422.
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
// envelope:
//
//	requestError                  -> its own status (400 invalid_argument)
//	UnclarifiedRequirementsError  -> 409 conflict + unclarified details
//	invalid                       -> 422 invalid_argument
//	not_found                     -> 404 not_found
//	conflict/dependency/state     -> 409 conflict
//
// Domain errors carry details.field, details.reason and details.message (spec
// §7.1); anything that is not a known error is an internal failure whose
// message is never leaked to the client.
func WriteServiceError(w http.ResponseWriter, r *http.Request, err error) {
	var requestErr *requestError
	if errors.As(err, &requestErr) && requestErr != nil {
		platform.WriteError(w, r, requestErr.Status, requestErr.Code, requestErr.Message,
			platform.WithDetails(requestErr.Details))
		return
	}

	// The enter-design soft gate wraps its domain error and adds the warning
	// ids/count to details (spec §7.2).
	var unclarifiedErr *versionapp.UnclarifiedRequirementsError
	if errors.As(err, &unclarifiedErr) && unclarifiedErr != nil {
		details := unclarifiedErr.Details()
		message := "unclarified requirements or open questions must be acknowledged"
		details["reason"] = "unclarified_requirements"
		details["message"] = message
		platform.WriteError(w, r, http.StatusConflict, platform.CodeConflict, message,
			platform.WithDetails(details))
		return
	}

	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr == nil {
		platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "internal server error")
		return
	}

	var status int
	var code string
	switch domainErr.Kind {
	case domain.KindInvalid:
		status, code = http.StatusUnprocessableEntity, platform.CodeInvalidArgument
	case domain.KindNotFound:
		status, code = http.StatusNotFound, platform.CodeNotFound
	case domain.KindConflict, domain.KindDependency, domain.KindStateConflict:
		status, code = http.StatusConflict, platform.CodeConflict
	default:
		platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "internal server error")
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

// ParsePage parses the shared limit/cursor query parameters into a domain page.
// limit must be 1-100 (default 20); cursor is opaque and validated here so a
// malformed value is a 400 instead of a repository failure. The cursor string
// itself is passed through untouched to the repository.
func ParsePage(r *http.Request) (domain.Page, error) {
	limit, err := ParseQueryLimit(r)
	if err != nil {
		return domain.Page{}, err
	}
	cursor := r.URL.Query().Get("cursor")
	if _, err := platform.DecodeCursor(cursor); err != nil {
		return domain.Page{}, newBadRequest("cursor", "invalid_cursor",
			"cursor is not a valid pagination cursor")
	}
	return domain.Page{Limit: limit, Cursor: cursor}, nil
}

// ParseQueryLimit parses the limit query parameter alone, for the endpoints that
// page with a raw numeric cursor (P1 message ids, event sequence numbers) instead
// of the opaque cursor of ParsePage.
func ParseQueryLimit(r *http.Request) (int, error) {
	limit, err := platform.ParseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		return 0, newBadRequest("limit", "invalid_limit",
			"limit must be an integer between 1 and 100")
	}
	return limit, nil
}

// ParseBeforeID parses the optional raw P1 message id of ?before_id=. The value
// is deliberately not a P3 snowflake string: clarification messages are P1
// agent_message and page backwards by their numeric ids (spec §7.5). An empty
// value means "the newest page".
func ParseBeforeID(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("before_id"))
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 0 {
		return 0, newBadRequest("before_id", "invalid_before_id",
			"before_id must be a non-negative decimal message id")
	}
	return id, nil
}

// ParseBeforeSeq parses the optional ?before_seq= upper bound of the version
// event snapshot. An empty value means the newest events.
func ParseBeforeSeq(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("before_seq"))
	if raw == "" {
		return 0, nil
	}
	seq, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || seq < 0 {
		return 0, newBadRequest("before_seq", "invalid_before_seq",
			"before_seq must be a non-negative decimal sequence number")
	}
	return seq, nil
}

// ParseOptionalQueryID parses one optional decimal-id filter query parameter.
// An empty value means "no filter"; a malformed or non-positive value is a 400.
func ParseOptionalQueryID(r *http.Request, name string) (*int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return nil, newBadRequest(name, "invalid_id", name+" must be a positive decimal id")
	}
	return &id, nil
}

// versionChannel returns the event channel of one version (spec §6.11): every
// P3 event is published on channel=version:{id}.
func versionChannel(versionID int64) string {
	return "version:" + strconv.FormatInt(versionID, 10)
}

// PathID parses a positive decimal {name} path value. P3 ids are 64-bit
// snowflakes serialized as decimal strings, so they are parsed from the raw
// path value and never through a JSON number.
func PathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, domain.NewInvalid(name, "invalid_id", name+" must be a positive decimal id")
	}
	return id, nil
}

// ActorID returns the authenticated platform user's id. P3 routes always run
// behind the identity middleware; an absent user yields 0 so a wiring mistake
// does not panic (services accept the zero actor).
func ActorID(ctx context.Context) int64 {
	user, ok := identityhttp.UserFromContext(ctx)
	if !ok {
		return 0
	}
	return user.ID
}

// DecodeJSON decodes the request body into dst with the shared strict decoder
// and the caller's size limit. Decoding failures are reported as a 422
// invalid_argument domain error, matching the P2 handlers.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	if err := platform.DecodeJSON(r, dst, maxBytes); err != nil {
		if errors.Is(err, platform.ErrBodyTooLarge) {
			return domain.NewInvalid("body", "body_too_large", "request body is too large")
		}
		return domain.NewInvalid("body", "invalid_json", "request body is not a valid JSON object")
	}
	return nil
}

// DecodeOptionalJSON decodes the body when one is present and leaves dst
// untouched for an empty body. Actions such as enter-design accept no body.
func DecodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	if r == nil || r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		return nil
	}
	return DecodeJSON(w, r, dst, maxBytes)
}

// NextCursor returns the opaque cursor for the next page: the last returned id
// when another row exists, and an empty string otherwise.
func NextCursor(items int, lastID int64, hasMore bool) string {
	if !hasMore || items == 0 {
		return ""
	}
	return platform.EncodeCursor(lastID)
}

// contentDisposition builds the RFC 5987 inline Content-Disposition used by the
// attachment download. The filename is always percent-encoded as UTF-8, so the
// header stays ASCII and cannot inject a second header line.
func contentDisposition(filename string) string {
	var builder strings.Builder
	builder.WriteString("inline; filename*=UTF-8''")
	for i := 0; i < len(filename); i++ {
		c := filename[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '-', c == '_', c == '~':
			builder.WriteByte(c)
		default:
			fmt.Fprintf(&builder, "%%%02X", c)
		}
	}
	return builder.String()
}
