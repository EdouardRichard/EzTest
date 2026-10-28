package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// maxAuthFlowBodyBytes caps login recipe request bodies (spec §3.4).
const maxAuthFlowBodyBytes int64 = 64 << 10

// AuthFlowHandler serves the login recipe endpoints (spec §6.2). Every route is
// admin-only: the caller wraps them with the identity RequireAdmin guard.
type AuthFlowHandler struct {
	svc *application.AuthFlowService
}

// NewAuthFlowHandler returns the login recipe handlers for svc.
func NewAuthFlowHandler(svc *application.AuthFlowService) *AuthFlowHandler {
	return &AuthFlowHandler{svc: svc}
}

// RegisterRoutes mounts the auth-flow endpoints on mux. guard wraps every
// route, including reads: login recipes are administrator-only (spec §5.11).
func (h *AuthFlowHandler) RegisterRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	if guard == nil {
		guard = func(next http.Handler) http.Handler { return next }
	}

	mux.Handle("GET /api/resources/auth-flows", guard(http.HandlerFunc(h.List)))
	mux.Handle("POST /api/resources/auth-flows", guard(http.HandlerFunc(h.Create)))
	mux.Handle("GET /api/resources/auth-flows/{id}", guard(http.HandlerFunc(h.Get)))
	mux.Handle("PATCH /api/resources/auth-flows/{id}", guard(http.HandlerFunc(h.Update)))
	mux.Handle("DELETE /api/resources/auth-flows/{id}", guard(http.HandlerFunc(h.Delete)))
}

// authFlowDTO is the public login recipe representation. It uses the snake_case
// keys of spec §3.4 and decimal-string ids (spec §6.1).
type authFlowDTO struct {
	ID         string                `json:"id"`
	SystemID   string                `json:"system_id"`
	Name       string                `json:"name"`
	Kind       string                `json:"kind"`
	Definition domain.FormDefinition `json:"definition"`
	Enabled    bool                  `json:"enabled"`
	CreatedAt  time.Time             `json:"created_at"`
	UpdatedAt  time.Time             `json:"updated_at"`
}

// newAuthFlowDTO maps a login recipe to its API shape.
func newAuthFlowDTO(flow domain.AuthFlow) authFlowDTO {
	return authFlowDTO{
		ID:         strconv.FormatInt(flow.ID, 10),
		SystemID:   strconv.FormatInt(flow.SystemID, 10),
		Name:       flow.Name,
		Kind:       flow.Kind,
		Definition: flow.Definition,
		Enabled:    flow.Enabled,
		CreatedAt:  flow.CreatedAt,
		UpdatedAt:  flow.UpdatedAt,
	}
}

// createAuthFlowRequest is the POST /auth-flows body. system_id is a decimal
// string like every id in the P6 JSON API (spec §6.1).
type createAuthFlowRequest struct {
	SystemID   string                `json:"system_id"`
	Name       string                `json:"name"`
	Kind       string                `json:"kind"`
	Definition domain.FormDefinition `json:"definition"`
	Enabled    *bool                 `json:"enabled"`
}

// updateAuthFlowRequest is the PATCH /auth-flows/{id} body. Nil fields are
// unchanged; id and system are immutable.
type updateAuthFlowRequest struct {
	Name       *string                `json:"name"`
	Kind       *string                `json:"kind"`
	Definition *domain.FormDefinition `json:"definition"`
	Enabled    *bool                  `json:"enabled"`
}

// List returns one cursor page of login recipes. The optional system_id query
// parameter narrows the page to one system.
func (h *AuthFlowHandler) List(w http.ResponseWriter, r *http.Request) {
	page, err := ParsePage(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	systemID, err := parseOptionalAuthFlowID(r.URL.Query().Get("system_id"), "system_id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	flows, hasMore, err := h.svc.List(r.Context(), domain.AuthFlowFilter{
		SystemID: systemID,
		Page:     page,
	})
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	items := make([]authFlowDTO, len(flows))
	for i, flow := range flows {
		items[i] = newAuthFlowDTO(flow)
	}
	next := ""
	if hasMore && len(flows) > 0 {
		next = platform.EncodeCursor(flows[len(flows)-1].ID)
	}
	WritePage(w, items, next, hasMore)
}

// Get returns one login recipe.
func (h *AuthFlowHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	flow, err := h.svc.Get(r.Context(), id)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newAuthFlowDTO(*flow))
}

// Create stores a new login recipe of an existing system.
func (h *AuthFlowHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createAuthFlowRequest
	if err := DecodeJSON(w, r, &req, maxAuthFlowBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	systemID, err := parseAuthFlowID(req.SystemID, "system_id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	flow, err := h.svc.Create(r.Context(), application.CreateAuthFlowInput{
		SystemID:   systemID,
		Name:       req.Name,
		Kind:       req.Kind,
		Definition: req.Definition,
		Enabled:    enabled,
	})
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusCreated, newAuthFlowDTO(*flow))
}

// Update applies a partial update to a login recipe, including the enabled
// toggle.
func (h *AuthFlowHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var req updateAuthFlowRequest
	if err := DecodeJSON(w, r, &req, maxAuthFlowBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	flow, err := h.svc.Update(r.Context(), id, application.UpdateAuthFlowInput{
		Name:       req.Name,
		Kind:       req.Kind,
		Definition: req.Definition,
		Enabled:    req.Enabled,
	})
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newAuthFlowDTO(*flow))
}

// Delete removes one login recipe.
func (h *AuthFlowHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseOptionalAuthFlowID parses an optional positive decimal string id; an
// empty value means "no filter".
func parseOptionalAuthFlowID(raw, field string) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	return parseAuthFlowID(raw, field)
}

// parseAuthFlowID parses a positive decimal string id. Invalid input is the
// shared 422 auth_flow_invalid error.
func parseAuthFlowID(raw, field string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, domain.NewInvalid(field, "auth_flow_invalid", field+" must be a positive decimal string")
	}
	return id, nil
}
