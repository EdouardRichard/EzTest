// Package workspaceapi serves the read-only global todo snapshot of the P6
// workspace tag (GET /api/todos).
package workspaceapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	workspaceapp "github.com/EdouardRichard/EzTest/backend/internal/workspace/application"
)

// ServicePort is the workspace application surface the handler needs.
type ServicePort interface {
	Snapshot(ctx context.Context, viewer platform.Actor, limit int) (workspaceapp.Snapshot, error)
}

// Handler serves GET /api/todos. The server mounts it behind RequireAuth; the
// handler still rejects a missing actor so direct calls stay safe.
type Handler struct {
	service ServicePort
}

// New builds the workspace HTTP handler.
func New(service ServicePort) *Handler {
	return &Handler{service: service}
}

// ListTodos returns the global todo snapshot (spec §4.3.2): six kinds, counts
// before the limit, server-enforced admin-only groups, read-only.
func (h *Handler) ListTodos(w http.ResponseWriter, r *http.Request) {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok || actor.ID <= 0 {
		httpx.Error(w, r, platform.Unauthenticated())
		return
	}
	limit, err := todoLimit(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("workspace service is not configured"))
		return
	}
	snapshot, err := h.service.Snapshot(r.Context(), actor, limit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toTodoSnapshotDTO(snapshot))
}

// todoLimit parses the contract limit (1–200, default 50). Unlike the shared
// pagination helper it rejects an out-of-range value instead of clamping it, so
// the API returns the contract 400 invalid_argument.
func todoLimit(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return workspaceapp.DefaultLimit, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > workspaceapp.MaxLimit {
		return 0, platform.Invalid("limit", "must be an integer between 1 and 200")
	}
	return value, nil
}
