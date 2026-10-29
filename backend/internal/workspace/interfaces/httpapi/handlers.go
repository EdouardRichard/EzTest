// Package httpapi exposes the P8 todo centre over HTTP (design spec §6.3):
// one read-only endpoint that aggregates the versions waiting for review and,
// for administrators, the execution case runs waiting for takeover/gate
// escalation. Handlers stay thin: they validate the query, resolve the
// administrator flag from the authenticated identity and render the shared
// envelopes.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	workspaceapp "github.com/EdouardRichard/EzTest/backend/internal/workspace/application"
)

// Query bounds of spec §6.3/§11.4 (EZTEST_TODOS_MAX default 50, range 1–200).
const (
	defaultTodosLimit = 50
	minTodosLimit     = 1
	maxTodosLimit     = 200
)

// AdminChecker reports whether the request's authenticated identity is an
// administrator. The composition root wires the identity adapter; tests inject a
// fake. A nil checker treats every caller as a regular user.
type AdminChecker func(ctx context.Context) bool

// Handlers is the todo-centre HTTP handler group. The composition root stores it
// in server.Dependencies.Workspace; a nil group mounts nothing.
type Handlers struct {
	lister  Lister
	isAdmin AdminChecker
}

// NewHandlers builds the handler group. It returns nil when no lister is wired,
// mirroring the other contexts' partially-wired-process behaviour.
func NewHandlers(lister Lister, isAdmin AdminChecker) *Handlers {
	if lister == nil {
		return nil
	}
	return &Handlers{lister: lister, isAdmin: isAdmin}
}

// RegisterRoutes mounts GET /api/todos. Authentication is the server's global
// bearer middleware; the endpoint adds no per-route guard (spec §11.1: every
// logged-in user may read their review todos, administrators additionally see
// takeover/gate items).
func (h *Handlers) RegisterRoutes(mux *http.ServeMux) {
	if h == nil || h.lister == nil || mux == nil {
		return
	}
	mux.Handle("GET /api/todos", http.HandlerFunc(h.handleTodos))
}

// RegisterRoutes mounts handler on mux. It is the package-level form of the
// method, for composition roots that prefer an explicit handler argument.
func RegisterRoutes(mux *http.ServeMux, handler *Handlers) {
	handler.RegisterRoutes(mux)
}

// handleTodos serves GET /api/todos?limit=50.
func (h *Handlers) handleTodos(w http.ResponseWriter, r *http.Request) {
	limit, err := parseLimit(r)
	if err != nil {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument,
			"limit must be between 1 and 200",
			platform.WithDetails(map[string]any{
				"field":  "limit",
				"min":    minTodosLimit,
				"max":    maxTodosLimit,
				"reason": workspaceapp.ReasonInvalidArgument,
			}))
		return
	}

	admin := false
	if h.isAdmin != nil {
		admin = h.isAdmin(r.Context())
	}

	snapshot, err := h.lister.List(r.Context(), workspaceapp.Query{Limit: limit, Admin: admin})
	if err != nil {
		// Never leak the provider message: it may carry database details.
		platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal,
			"internal server error")
		return
	}

	// The todo centre is real-time data: no cross-request or browser caching.
	w.Header().Set("Cache-Control", "no-store")
	writeData(w, http.StatusOK, snapshot)
}

// parseLimit validates the optional limit query parameter.
func parseLimit(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return defaultTodosLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < minTodosLimit || limit > maxTodosLimit {
		return 0, errInvalidLimit
	}
	return limit, nil
}

// errInvalidLimit is the sentinel of a rejected limit parameter; the handler
// renders the public details map itself.
var errInvalidLimit = errors.New("invalid limit")

// dataEnvelope is the success shape of the todo endpoint: {"data": snapshot}.
type dataEnvelope struct {
	Data any `json:"data"`
}

// writeData writes {"data": data} with the shared JSON content type.
func writeData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(dataEnvelope{Data: data})
}
