// Package auditapi exposes the admin audit-log query endpoint.
package auditapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// Service is the application surface this handler needs.
type Service interface {
	List(ctx context.Context, filter auditapp.Filter, limit int, beforeID *int64) ([]audit.Entry, bool, error)
}

// Handler serves GET /api/admin/audit-logs. The server mounts it behind the
// bearer-auth and admin guards.
type Handler struct {
	svc Service
}

// New builds the handler.
func New(svc Service) *Handler {
	return &Handler{svc: svc}
}

// List GET /api/admin/audit-logs?action=&actor=&limit=&cursor=.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	var actorID *int64
	if raw := strings.TrimSpace(query.Get("actor")); raw != "" {
		parsed, err := validation.NonEmptyID("actor", raw)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		actorID = &parsed
	}

	limit := pagination.Limit(query.Get("limit"), auditapp.DefaultListLimit, auditapp.MaxListLimit)
	cursor, err := pagination.Cursor(query.Get("cursor"))
	if err != nil {
		httpx.Error(w, r, platform.Invalid("cursor", "cursor must be a decimal ID"))
		return
	}
	var beforeID *int64
	if cursor > 0 {
		beforeID = &cursor
	}

	filter := auditapp.Filter{Action: strings.TrimSpace(query.Get("action")), ActorID: actorID}
	entries, hasMore, err := h.svc.List(r.Context(), filter, limit, beforeID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	items := make([]entryDTO, 0, len(entries))
	for _, entry := range entries {
		items = append(items, toEntryDTO(entry))
	}
	var nextCursor string
	if hasMore && len(entries) > 0 {
		nextCursor = pagination.NextCursor(true, entries[len(entries)-1].ID)
	}
	httpx.List(w, http.StatusOK, items, nextCursor, hasMore)
}
