// Package httpapi exposes the resource context's HTTP interface. Handlers stay
// thin: they decode DTOs, call the application services and render the shared
// response envelopes through the helpers in this file.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
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
	writeResourceJSON(w, status, dataEnvelope{Data: data})
}

// WritePage writes {"data": items, "next_cursor": next, "has_more": hasMore}.
func WritePage(w http.ResponseWriter, items any, next string, hasMore bool) {
	writeResourceJSON(w, http.StatusOK, pageEnvelope{Data: items, NextCursor: next, HasMore: hasMore})
}

// WriteServiceError maps a domain error onto the shared error envelope:
//
//	invalid                 -> 422 invalid_argument
//	not_found               -> 404 not_found
//	conflict/dependency/
//	state/lease conflict    -> 409 conflict
//
// Invalid errors carry details.field and details.reason; every domain error
// carries details.reason. Anything that is not a *domain.Error - or an unknown
// kind - is an internal failure whose message is never leaked to the client.
func WriteServiceError(w http.ResponseWriter, r *http.Request, err error) {
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr == nil {
		platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "internal server error")
		return
	}

	details := map[string]any{}
	if domainErr.Field != "" {
		details["field"] = domainErr.Field
	}
	if domainErr.Reason != "" {
		details["reason"] = domainErr.Reason
	}

	var status int
	var code string
	switch domainErr.Kind {
	case domain.KindInvalid:
		status, code = http.StatusUnprocessableEntity, platform.CodeInvalidArgument
	case domain.KindNotFound:
		status, code = http.StatusNotFound, platform.CodeNotFound
	case domain.KindConflict, domain.KindDependency, domain.KindStateConflict, domain.KindLeaseConflict:
		status, code = http.StatusConflict, platform.CodeConflict
	default:
		platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "internal server error")
		return
	}

	message := domainErr.Message
	if message == "" {
		message = http.StatusText(status)
	}
	platform.WriteError(w, r, status, code, message, platform.WithDetails(details))
}

// ParsePage parses the shared limit/cursor query parameters into a domain page.
// The default limit and the 1-100 bound come from platform.
func ParsePage(r *http.Request) (domain.Page, error) {
	limit, err := platform.ParseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		return domain.Page{}, domain.NewInvalid("limit", "invalid_limit", "limit must be an integer between 1 and 100")
	}
	afterID, err := platform.DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		return domain.Page{}, domain.NewInvalid("cursor", "invalid_cursor", "cursor is not a valid pagination cursor")
	}
	return domain.Page{Limit: limit, AfterID: afterID}, nil
}

// PathID parses the positive integer {id} path value. On failure it returns a
// domain invalid error so the caller can render it with WriteServiceError.
func PathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, domain.NewInvalid("id", "invalid_id", "id must be a positive integer")
	}
	return id, nil
}

// DecodeJSON decodes the request body into dst with the shared strict decoder
// and the caller's size limit. Decoding failures are reported as a 422
// invalid_argument domain error.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	if err := platform.DecodeJSON(r, dst, maxBytes); err != nil {
		if errors.Is(err, platform.ErrBodyTooLarge) {
			return domain.NewInvalid("body", "body_too_large", "request body is too large")
		}
		return domain.NewInvalid("body", "invalid_json", "request body is not a valid JSON object")
	}
	return nil
}

// ActorID returns the authenticated platform user's id, or nil when the request
// has no authenticated user. The resource routes always run behind the
// identity middleware, so a nil actor means an internal wiring error; services
// accept nil created_by/requested_by instead of failing the request.
func ActorID(ctx context.Context) *int64 {
	user, ok := identityhttp.UserFromContext(ctx)
	if !ok {
		return nil
	}
	id := user.ID
	return &id
}

// writeResourceJSON writes payload as JSON with the shared content type.
// Encoding errors cannot be repaired after the header is written and the
// payloads here are plain structs, so they are ignored like the identity
// handlers do.
func writeResourceJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
