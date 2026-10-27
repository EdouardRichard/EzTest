// Package httpapi is the Agent context's HTTP interface: the admin-only
// profile, tool, session and message endpoints from design spec §10. It depends
// only on consumer-defined service interfaces plus the domain read models; the
// server composition root mounts the routes behind the global bearer auth and
// the per-route admin guard it passes to RegisterRoutes.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

const (
	// maxAgentBodyBytes caps every agent request body. The largest legitimate
	// field is a 32768-character system prompt (up to ~96 KiB in UTF-8), so
	// 256 KiB leaves headroom without allowing an unbounded body.
	maxAgentBodyBytes int64 = 256 << 10
	// sessionTitleMaxRunes mirrors the design spec's 0–120 character title.
	sessionTitleMaxRunes = 120
)

// ProfileService is the profile use case consumed by the handler. The
// application package's *ProfileService satisfies it.
type ProfileService interface {
	List(ctx context.Context) ([]domain.AgentProfile, error)
	Get(ctx context.Context, id int64) (domain.AgentProfile, error)
	Update(ctx context.Context, id int64, params domain.UpdateParams, actor *int64) (domain.AgentProfile, error)
}

// SessionService is the session use case consumed by the handler. The
// application package's *SessionService satisfies it.
type SessionService interface {
	List(ctx context.Context, cursor int64, limit int, profileID *int64) ([]domain.SessionSummary, bool, error)
	Create(ctx context.Context, profileID int64, title string, actor *int64) (domain.Session, error)
	Get(ctx context.Context, id int64) (domain.SessionDetail, error)
	UpdateTitle(ctx context.Context, id int64, title string) (domain.Session, error)
	Delete(ctx context.Context, id int64, actor *int64) error
	Message(ctx context.Context, sessionID, messageID int64) (domain.Message, error)
}

// RunControl is the run use case consumed by the handler. The application
// package's *RunService satisfies it.
type RunControl interface {
	Start(ctx context.Context, sessionID int64, message string, actor *int64) (domain.StartResult, error)
	Abort(ctx context.Context, sessionID int64, actor *int64) (domain.AbortResult, error)
}

// ToolCatalog is the read-only tool listing consumed by the handler. The
// application package's *ToolCatalog satisfies it.
type ToolCatalog interface {
	Infos() []domain.ToolInfo
}

// Handler serves the admin Agent endpoints. The routes are mounted behind the
// global bearer authentication and the per-route admin guard supplied by the
// server.
type Handler struct {
	profiles ProfileService
	sessions SessionService
	runs     RunControl
	tools    ToolCatalog
}

// NewHandler returns the Agent HTTP handler.
func NewHandler(profiles ProfileService, sessions SessionService, runs RunControl, tools ToolCatalog) *Handler {
	return &Handler{profiles: profiles, sessions: sessions, runs: runs, tools: tools}
}

// RegisterRoutes mounts every Agent endpoint on mux. guard wraps every route with
// the caller's admin middleware; the server passes RequireAdmin and never
// installs a second authentication middleware here. A nil guard is tolerated for
// tests and for deployments that authenticate globally.
func RegisterRoutes(mux *http.ServeMux, h *Handler, guard func(http.Handler) http.Handler) {
	if guard == nil {
		guard = func(next http.Handler) http.Handler { return next }
	}
	mux.Handle("GET /api/agents/profiles", guard(http.HandlerFunc(h.ListProfiles)))
	mux.Handle("GET /api/agents/profiles/{id}", guard(http.HandlerFunc(h.GetProfile)))
	mux.Handle("PATCH /api/agents/profiles/{id}", guard(http.HandlerFunc(h.UpdateProfile)))
	mux.Handle("GET /api/agents/tools", guard(http.HandlerFunc(h.ListTools)))
	mux.Handle("GET /api/agents/sessions", guard(http.HandlerFunc(h.ListSessions)))
	mux.Handle("POST /api/agents/sessions", guard(http.HandlerFunc(h.CreateSession)))
	mux.Handle("GET /api/agents/sessions/{id}", guard(http.HandlerFunc(h.GetSession)))
	mux.Handle("PATCH /api/agents/sessions/{id}", guard(http.HandlerFunc(h.UpdateSessionTitle)))
	mux.Handle("DELETE /api/agents/sessions/{id}", guard(http.HandlerFunc(h.DeleteSession)))
	mux.Handle("POST /api/agents/sessions/{id}/messages", guard(http.HandlerFunc(h.SendMessage)))
	mux.Handle("POST /api/agents/sessions/{id}/stop", guard(http.HandlerFunc(h.StopSession)))
	mux.Handle("GET /api/agents/sessions/{id}/messages/{message_id}", guard(http.HandlerFunc(h.GetMessage)))
}

// ListProfiles returns every profile ordered by ID.
func (h *Handler) ListProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := h.profiles.List(r.Context())
	if err != nil {
		writeInternalError(w, r)
		return
	}
	items := make([]profilePayload, len(profiles))
	for i, profile := range profiles {
		items[i] = newProfilePayload(profile)
	}
	writeJSON(w, http.StatusOK, struct {
		Data []profilePayload `json:"data"`
	}{Data: items})
}

// GetProfile returns one profile or 404.
func (h *Handler) GetProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromPath(w, r, "id", "profile")
	if !ok {
		return
	}
	profile, err := h.profiles.Get(r.Context(), id)
	if err != nil {
		writeProfileError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Data profilePayload `json:"data"`
	}{Data: newProfilePayload(profile)})
}

// UpdateProfile applies a partial profile update. Unknown fields (including key
// and builtin) never reach the service: DecodeJSON rejects them. allowed_tools
// is re-validated against the live tool registry before the service call.
func (h *Handler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromPath(w, r, "id", "profile")
	if !ok {
		return
	}
	var request updateProfileRequest
	if err := platform.DecodeJSON(r, &request, maxAgentBodyBytes); err != nil {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid JSON body")
		return
	}
	params := request.params()
	if field, registered := validateAllowedTools(h.tools, params.AllowedTools); !registered {
		platform.WriteError(w, r, http.StatusUnprocessableEntity, platform.CodeInvalidArgument,
			"allowed_tools contains an unregistered tool", platform.WithDetails(map[string]any{"field": field}))
		return
	}
	profile, err := h.profiles.Update(r.Context(), id, params, actorFromContext(r.Context()))
	if err != nil {
		writeProfileError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Data profilePayload `json:"data"`
	}{Data: newProfilePayload(profile)})
}

// ListTools returns the registered tool catalog, sorted by name.
func (h *Handler) ListTools(w http.ResponseWriter, r *http.Request) {
	var infos []domain.ToolInfo
	if h.tools != nil {
		infos = h.tools.Infos()
	}
	items := make([]toolPayload, len(infos))
	for i, info := range infos {
		items[i] = newToolPayload(info)
	}
	writeJSON(w, http.StatusOK, struct {
		Data []toolPayload `json:"data"`
	}{Data: items})
}

// ListSessions returns one cursor-paged session list. The page body is
// {"data":{"items":[...],"has_more":bool,"next_cursor":"<opaque id>"}}.
func (h *Handler) ListSessions(w http.ResponseWriter, r *http.Request) {
	limit, err := platform.ParseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid limit")
		return
	}
	cursor, err := platform.DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid cursor")
		return
	}
	profileID, ok := profileIDFromQuery(w, r)
	if !ok {
		return
	}
	summaries, hasMore, err := h.sessions.List(r.Context(), cursor, limit, profileID)
	if err != nil {
		writeInternalError(w, r)
		return
	}
	items := make([]sessionPayload, len(summaries))
	for i, summary := range summaries {
		items[i] = newSessionPayload(summary.Session, summary.ActiveRun != nil, summary.ActiveRun, summary.LastRun)
	}
	page := sessionPage{Items: items, HasMore: hasMore}
	if hasMore && len(items) > 0 {
		next := platform.EncodeCursor(items[len(items)-1].ID)
		page.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, struct {
		Data sessionPage `json:"data"`
	}{Data: page})
}

// CreateSession creates a session on the requested profile, or on the builtin
// profile when profile_id is omitted or zero. The body is optional: an empty
// request creates the default session the console's "new session" action needs.
func (h *Handler) CreateSession(w http.ResponseWriter, r *http.Request) {
	var request createSessionRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := platform.DecodeJSON(r, &request, maxAgentBodyBytes); err != nil {
			platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid JSON body")
			return
		}
	}
	if !validSessionTitle(w, r, request.Title) {
		return
	}
	session, err := h.sessions.Create(r.Context(), request.ProfileID, request.Title, actorFromContext(r.Context()))
	if err != nil {
		writeSessionCreateError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		Data sessionPayload `json:"data"`
	}{Data: newSessionPayload(session, false, nil, nil)})
}

// GetSession returns one session with its running and newest terminal run.
func (h *Handler) GetSession(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromPath(w, r, "id", "session")
	if !ok {
		return
	}
	detail, err := h.sessions.Get(r.Context(), id)
	if err != nil {
		writeSessionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Data sessionPayload `json:"data"`
	}{Data: newSessionPayload(detail.Session, detail.Running, detail.ActiveRun, detail.LastRun)})
}

// UpdateSessionTitle changes the title; title is the only mutable field.
func (h *Handler) UpdateSessionTitle(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromPath(w, r, "id", "session")
	if !ok {
		return
	}
	var request updateSessionRequest
	if err := platform.DecodeJSON(r, &request, maxAgentBodyBytes); err != nil {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid JSON body")
		return
	}
	if request.Title == nil {
		platform.WriteError(w, r, http.StatusUnprocessableEntity, platform.CodeInvalidArgument,
			"title is required", platform.WithDetails(map[string]any{"field": "title"}))
		return
	}
	if !validSessionTitle(w, r, *request.Title) {
		return
	}
	session, err := h.sessions.UpdateTitle(r.Context(), id, *request.Title)
	if err != nil {
		writeSessionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Data sessionPayload `json:"data"`
	}{Data: newSessionPayload(session, false, nil, nil)})
}

// DeleteSession aborts an active run, waits up to five seconds and then deletes
// the session. A run that does not stop is 409 run_active and the session stays.
func (h *Handler) DeleteSession(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromPath(w, r, "id", "session")
	if !ok {
		return
	}
	if err := h.sessions.Delete(r.Context(), id, actorFromContext(r.Context())); err != nil {
		switch {
		case errors.Is(err, domain.ErrNotFound):
			platform.WriteError(w, r, http.StatusNotFound, platform.CodeNotFound, "session not found")
		case errors.Is(err, domain.ErrRunActive):
			platform.WriteError(w, r, http.StatusConflict, platform.CodeConflict,
				"the session has a running run", platform.WithDetails(map[string]any{"reason": "run_active"}))
		default:
			writeInternalError(w, r)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SendMessage starts a run and returns its id with 202 Accepted.
func (h *Handler) SendMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromPath(w, r, "id", "session")
	if !ok {
		return
	}
	var request sendMessageRequest
	if err := platform.DecodeJSON(r, &request, maxAgentBodyBytes); err != nil {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid JSON body")
		return
	}
	result, err := h.runs.Start(r.Context(), id, request.Message, actorFromContext(r.Context()))
	if err != nil {
		writeSendError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		Data sendMessageData `json:"data"`
	}{Data: sendMessageData{RunID: result.RunID, Status: "accepted"}})
}

// StopSession asks the run service to abort the active run. It is idempotent:
// "stopping" when a run was cancelled and "idle" when none was active.
func (h *Handler) StopSession(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromPath(w, r, "id", "session")
	if !ok {
		return
	}
	result, err := h.runs.Abort(r.Context(), id, actorFromContext(r.Context()))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			platform.WriteError(w, r, http.StatusNotFound, platform.CodeNotFound, "session not found")
			return
		}
		writeInternalError(w, r)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Data stopData `json:"data"`
	}{Data: stopData{Status: result.Status}})
}

// GetMessage returns one full message, provided it belongs to the session in the
// path.
func (h *Handler) GetMessage(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := idFromPath(w, r, "id", "session")
	if !ok {
		return
	}
	messageID, ok := idFromPath(w, r, "message_id", "message")
	if !ok {
		return
	}
	message, err := h.sessions.Message(r.Context(), sessionID, messageID)
	if err != nil {
		writeSessionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Data messagePayload `json:"data"`
	}{Data: newMessagePayload(message)})
}

// validateAllowedTools reports whether every requested tool is registered. It
// re-validates at the HTTP boundary because the application service cannot see
// the registry; an unregistered name is a request error, not a stored profile.
func validateAllowedTools(tools ToolCatalog, allowed *[]string) (string, bool) {
	if allowed == nil || tools == nil {
		return "", true
	}
	infos := tools.Infos()
	registered := make(map[string]struct{}, len(infos))
	for _, info := range infos {
		registered[info.Name] = struct{}{}
	}
	for _, name := range *allowed {
		if _, ok := registered[name]; !ok {
			return "allowed_tools", false
		}
	}
	return "", true
}

// validSessionTitle writes a 422 and reports false when title exceeds the API
// bound. It is the HTTP-side check; the service validates again for direct
// callers.
func validSessionTitle(w http.ResponseWriter, r *http.Request, title string) bool {
	if utf8.RuneCountInString(title) > sessionTitleMaxRunes {
		platform.WriteError(w, r, http.StatusUnprocessableEntity, platform.CodeInvalidArgument,
			"title must be at most 120 characters", platform.WithDetails(map[string]any{"field": "title"}))
		return false
	}
	return true
}

// idFromPath parses a positive integer path value, writing a 400 when it is not
// one. name is the ServeMux wildcard name; resource names the path segment in
// the error message.
func idFromPath(w http.ResponseWriter, r *http.Request, name, resource string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument,
			"invalid "+resource+" id")
		return 0, false
	}
	return id, true
}

// profileIDFromQuery parses the optional profile_id filter. A missing filter is
// nil; a malformed or non-positive one is a 400.
func profileIDFromQuery(w http.ResponseWriter, r *http.Request) (*int64, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("profile_id"))
	if raw == "" {
		return nil, true
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid profile_id")
		return nil, false
	}
	return &id, true
}

// actorFromContext returns the authenticated user id the server's audit context
// middleware attached, or nil when the request is unauthenticated (unit tests).
func actorFromContext(ctx context.Context) *int64 {
	if id, ok := auditapp.ActorIDFromContext(ctx); ok {
		return &id
	}
	return nil
}

// writeProfileError maps profile use-case failures to the shared envelope: 404
// for an unknown profile, 422 with details.field for a domain validation error,
// and 500 for everything else.
func writeProfileError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, domain.ErrNotFound) {
		platform.WriteError(w, r, http.StatusNotFound, platform.CodeNotFound, "profile not found")
		return
	}
	if field := validationField(err); field != "" {
		platform.WriteError(w, r, http.StatusUnprocessableEntity, platform.CodeInvalidArgument,
			"invalid profile field", platform.WithDetails(map[string]any{"field": field}))
		return
	}
	writeInternalError(w, r)
}

// writeSessionCreateError maps session-creation failures: unknown profile is
// 404, a disabled profile is 409 profile_disabled, an invalid title is 422, and
// anything else is a 500.
func writeSessionCreateError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		platform.WriteError(w, r, http.StatusNotFound, platform.CodeNotFound, "profile not found")
	case errors.Is(err, domain.ErrProfileDisabled):
		platform.WriteError(w, r, http.StatusConflict, platform.CodeConflict,
			"the profile is disabled", platform.WithDetails(map[string]any{"reason": "profile_disabled"}))
	default:
		if field := validationField(err); field != "" {
			platform.WriteError(w, r, http.StatusUnprocessableEntity, platform.CodeInvalidArgument,
				"invalid session field", platform.WithDetails(map[string]any{"field": field}))
			return
		}
		writeInternalError(w, r)
	}
}

// writeSessionError maps a generic session look-up/update failure: 404 for a
// missing session or message, otherwise 500.
func writeSessionError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, domain.ErrNotFound) {
		platform.WriteError(w, r, http.StatusNotFound, platform.CodeNotFound, "session or message not found")
		return
	}
	if field := validationField(err); field != "" {
		platform.WriteError(w, r, http.StatusUnprocessableEntity, platform.CodeInvalidArgument,
			"invalid session field", platform.WithDetails(map[string]any{"field": field}))
		return
	}
	writeInternalError(w, r)
}

// writeSendError maps the run-start failures from design spec §10.3.
func writeSendError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrRunActive):
		platform.WriteError(w, r, http.StatusConflict, platform.CodeConflict,
			"the session already has a running run", platform.WithDetails(map[string]any{"reason": "run_active"}))
	case errors.Is(err, domain.ErrCapacity):
		platform.WriteError(w, r, http.StatusServiceUnavailable, platform.CodeUnavailable,
			"agent run capacity is exhausted",
			platform.WithRetryable(true), platform.WithDetails(map[string]any{"reason": "capacity"}))
	case errors.Is(err, domain.ErrLLMNotConfigured):
		platform.WriteError(w, r, http.StatusServiceUnavailable, platform.CodeUnavailable,
			"the default llm configuration is not available",
			platform.WithDetails(map[string]any{"reason": "llm_not_configured"}))
	case errors.Is(err, domain.ErrToolsUnsupported):
		platform.WriteError(w, r, http.StatusServiceUnavailable, platform.CodeUnavailable,
			"tools require the chat_completions protocol",
			platform.WithDetails(map[string]any{"reason": "tools_require_chat_completions"}))
	case errors.Is(err, domain.ErrProfileDisabled):
		platform.WriteError(w, r, http.StatusConflict, platform.CodeConflict,
			"the profile is disabled", platform.WithDetails(map[string]any{"reason": "profile_disabled"}))
	case errors.Is(err, domain.ErrEmptyMessage):
		platform.WriteError(w, r, http.StatusUnprocessableEntity, platform.CodeInvalidArgument,
			"message must not be empty", platform.WithDetails(map[string]any{"field": "message"}))
	case errors.Is(err, domain.ErrNotFound):
		platform.WriteError(w, r, http.StatusNotFound, platform.CodeNotFound, "session not found")
	default:
		writeInternalError(w, r)
	}
}

// validationField returns the request field named by a domain validation error,
// or "" when err is not a validation error. Domain validation errors are plain
// fmt.Errorf values whose message starts with "agent: " and names the field; the
// HTTP layer maps that field into details.field because the domain deliberately
// has no typed validation error.
func validationField(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if !strings.HasPrefix(message, "agent: ") {
		return ""
	}
	for _, field := range []string{
		"system_prompt",
		"max_output_tokens",
		"max_turns",
		"run_seconds",
		"hard_timeout_seconds",
		"reasoning_effort",
		"thinking",
		"allowed_tools",
		"title",
		"name",
		"key",
	} {
		if strings.Contains(message, field) {
			return field
		}
	}
	return ""
}

// writeInternalError writes the shared opaque 500 envelope.
func writeInternalError(w http.ResponseWriter, r *http.Request) {
	platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "internal server error")
}

// writeJSON writes a success response in the shared `{"data": ...}` style.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
