package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// This file holds the cross-handler plumbing of the mindmap HTTP API: the shared
// envelopes, request decoding/parsing helpers and the actor/role accessors. It
// contains no endpoint of its own.

const (
	// maxRequestBytes bounds one mindmap request body. The largest body is a
	// node patch with a free-form testData document; the P4/P3 endpoints use the
	// same 1 MiB platform default.
	maxRequestBytes int64 = 1 << 20
)

// dataEnvelope is the success shape of one resource (spec §7.1).
type dataEnvelope struct {
	Data any `json:"data"`
}

// pageEnvelope is the cursor page shape of the list endpoints (spec §7.1).
type pageEnvelope struct {
	Data       any    `json:"data"`
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}

// writeData renders {"data": data} with status.
func writeData(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, dataEnvelope{Data: data})
}

// writePage renders one cursor page.
func writePage(w http.ResponseWriter, items any, next string, hasMore bool) {
	writeJSON(w, http.StatusOK, pageEnvelope{Data: items, NextCursor: next, HasMore: hasMore})
}

// writeJSON writes payload with the shared JSON content type. Encoding failures
// cannot be repaired once the header is written and every payload is a plain
// DTO, so they are ignored like the P2/P3 handlers do.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// writeNoContent answers 204 with no body (the documented delete/reorder result).
func writeNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// pathID parses one decimal-string {name} path value. Mindmap ids are 64-bit
// snowflakes serialized as strings (D29): they are parsed from the raw path
// value and never through a JSON number.
func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, newBadRequest(name, "invalid_id", name+" must be a positive decimal id")
	}
	return id, nil
}

// queryRevision parses a required positive ?revision= / ?nodeRevision= token.
func queryRevision(r *http.Request, name string) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, newBadRequest(name, "invalid_revision", name+" must be a positive integer")
	}
	return value, nil
}

// queryOptionalID parses one optional positive decimal-id query filter.
func queryOptionalID(r *http.Request, name string) (*int64, error) {
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

// queryPage parses the shared limit/cursor pair of the list endpoints. A
// malformed value is a 400 instead of a repository failure.
func queryPage(r *http.Request) (int, string, error) {
	limit, err := platform.ParseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		return 0, "", newBadRequest("limit", "invalid_limit", "limit must be an integer between 1 and 100")
	}
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if _, err := platform.DecodeCursor(cursor); err != nil {
		return 0, "", newBadRequest("cursor", "invalid_cursor", "cursor is not a valid pagination cursor")
	}
	return limit, cursor, nil
}

// decodeBody decodes one required JSON object body. A missing body, a body that
// is not a JSON object, unknown fields and trailing data are 400 invalid_argument
// (the malformed-request class of spec §7.1).
func decodeBody(r *http.Request, dst any) error {
	if err := platform.DecodeJSON(r, dst, maxRequestBytes); err != nil {
		if errors.Is(err, platform.ErrBodyTooLarge) {
			return newBadRequest("body", "body_too_large", "request body is too large")
		}
		return newBadRequest("body", "invalid_json", "request body must be a single valid JSON object")
	}
	return nil
}

// decodeOptionalBody decodes the body when one is present and leaves dst
// untouched otherwise. Actions such as submit-review accept an empty body.
func decodeOptionalBody(r *http.Request, dst any) error {
	if r == nil || r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		return nil
	}
	return decodeBody(r, dst)
}

// actorID returns the authenticated user id. The mindmap routes always run behind
// the identity middleware; an absent user yields 0 so a wiring mistake cannot
// panic (the services accept the zero actor).
func actorID(ctx context.Context) int64 {
	user, ok := identityhttp.UserFromContext(ctx)
	if !ok {
		return 0
	}
	return user.ID
}

// isAdmin reports whether the authenticated user is an administrator.
func isAdmin(ctx context.Context) bool {
	user, ok := identityhttp.UserFromContext(ctx)
	return ok && user.Role == identity.RoleAdmin
}

// requireAdmin returns the 403 forbidden failure of an admin-only service call.
func requireAdmin(ctx context.Context, reason, message string) error {
	if isAdmin(ctx) {
		return nil
	}
	return newForbidden(reason, message)
}

// notFound reports whether err is a missing-resource domain error.
func notFound(err error) bool {
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr == nil {
		return false
	}
	return domainErr.Kind == domain.KindNotFound || notFoundReason(domainErr.Reason)
}

// nextCursor renders the opaque cursor of the next page: the last returned id when
// another row exists, and an empty string otherwise.
func nextCursor(items int, lastID int64, hasMore bool) string {
	if !hasMore || items == 0 {
		return ""
	}
	return platform.EncodeCursor(lastID)
}
