// Package httpapi is the audit context's HTTP interface: the admin-only audit
// query endpoint. It only calls the application layer and keeps request and
// response DTOs separate from the domain entry.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	audit "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Lister is the audit query port consumed by the admin endpoint. The audit
// application Service satisfies it.
type Lister interface {
	List(ctx context.Context, limit int, afterID int64) ([]audit.Entry, bool, error)
}

// Handler serves GET /api/admin/audit. The server mounts it behind the global
// bearer auth and the per-route admin guard.
type Handler struct {
	svc Lister
}

// NewHandler returns the audit query handler for svc.
func NewHandler(svc Lister) *Handler {
	return &Handler{svc: svc}
}

// ServeHTTP returns one page of audit entries, newest first, in the generic
// list shape: data plus next_cursor/has_more.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	limit, err := platform.ParseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid limit")
		return
	}
	afterID, err := platform.DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid cursor")
		return
	}

	entries, more, err := h.svc.List(r.Context(), limit, afterID)
	if err != nil {
		platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "internal server error")
		return
	}
	items := make([]entryPayload, len(entries))
	for i, entry := range entries {
		items[i] = newEntryPayload(entry)
	}
	response := listEnvelope{Data: items, HasMore: more}
	if more && len(entries) > 0 {
		response.NextCursor = platform.EncodeCursor(entries[len(entries)-1].ID)
	}
	writeJSON(w, http.StatusOK, response)
}

// entryPayload is the public representation of one audit entry. It mirrors the
// persisted entry and never carries credentials (the domain entry contract
// already excludes them).
type entryPayload struct {
	ID          int64          `json:"id"`
	ActorUserID *int64         `json:"actor_user_id"`
	Action      string         `json:"action"`
	TargetType  string         `json:"target_type"`
	TargetID    string         `json:"target_id"`
	RequestID   string         `json:"request_id"`
	IP          string         `json:"ip"`
	UserAgent   string         `json:"user_agent"`
	Metadata    map[string]any `json:"metadata"`
	CreatedAt   time.Time      `json:"created_at"`
}

func newEntryPayload(entry audit.Entry) entryPayload {
	metadata := entry.Metadata
	if metadata == nil {
		// The contract returns an object, never null.
		metadata = map[string]any{}
	}
	return entryPayload{
		ID:          entry.ID,
		ActorUserID: entry.ActorUserID,
		Action:      entry.Action,
		TargetType:  entry.TargetType,
		TargetID:    entry.TargetID,
		RequestID:   entry.RequestID,
		IP:          entry.IP,
		UserAgent:   entry.UserAgent,
		Metadata:    metadata,
		CreatedAt:   entry.CreatedAt,
	}
}

// listEnvelope is the generic list shape shared by the API: data plus the
// opaque cursor and the has_more flag.
type listEnvelope struct {
	Data       []entryPayload `json:"data"`
	NextCursor string         `json:"next_cursor"`
	HasMore    bool           `json:"has_more"`
}

// writeJSON writes a success response in the shared `{"data": ...}` style.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
