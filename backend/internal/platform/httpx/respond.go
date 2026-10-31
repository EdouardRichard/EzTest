// Package httpx writes the shared JSON success and error envelopes.
package httpx

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

// JSON writes a 2xx/4xx body wrapped in {"data": ...}.
func JSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// Error writes the shared error envelope. Unknown errors become a safe 500.
func Error(w http.ResponseWriter, r *http.Request, err error) {
	apiErr := platform.AsError(err)
	requestID := requestid.From(r.Context())
	if requestID == "" {
		requestID = r.Header.Get(requestid.Header)
	}

	details := apiErr.Details
	if details == nil {
		details = map[string]any{}
	}
	if apiErr.Status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if requestID != "" {
		w.Header().Set(requestid.Header, requestID)
	}
	w.WriteHeader(apiErr.Status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":       apiErr.Code,
			"message":    apiErr.Message,
			"request_id": requestID,
			"retryable":  apiErr.Retryable,
			"details":    details,
		},
	})
}

// RequestID returns the context request ID (convenience for handlers).
func RequestID(ctx context.Context) string {
	return requestid.From(ctx)
}
