package agentapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
)

// ProfileService is the admin profile/prompt application surface.
// *agentapp.ProfileAdminService satisfies it.
type ProfileService interface {
	List(ctx context.Context) ([]agentapp.ProfileView, error)
	Get(ctx context.Context, key string) (agentapp.ProfileDetail, error)
	Patch(ctx context.Context, actor platform.Actor, key string, input agentapp.PatchInput) (agentapp.ProfileView, bool, error)
	SavePrompt(ctx context.Context, actor platform.Actor, key string, input agentapp.SavePromptInput) (agentapp.ProfileView, int, bool, error)
	ResetPrompt(ctx context.Context, actor platform.Actor, key string) (agentapp.ProfileView, int, bool, error)
	RestorePrompt(ctx context.Context, actor platform.Actor, key string, version int) (agentapp.ProfileView, int, bool, error)
	Revisions(ctx context.Context, key string, limit int, beforeVersion *int) ([]agentapp.PromptRevision, bool, error)
	Preview(ctx context.Context, key, tmpl string, sample map[string]string) (string, error)
	Tools() ([]agentapp.AgentToolView, error)
}

// ProfileHandler serves the P6 admin profile and prompt API. Every route is
// mounted behind RequireAdmin; each handler additionally re-checks the role.
type ProfileHandler struct {
	service ProfileService
}

// NewProfileHandler builds the admin profile handler.
func NewProfileHandler(service ProfileService) *ProfileHandler {
	return &ProfileHandler{service: service}
}

var _ ProfileService = (*agentapp.ProfileAdminService)(nil)

// List GET /api/agents/profiles (admin; includes the current prompt).
func (h *ProfileHandler) List(w http.ResponseWriter, r *http.Request) {
	if _, err := requireAdminActor(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("agent profile service is not configured"))
		return
	}
	views, err := h.service.List(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]profileViewDTO, 0, len(views))
	for _, view := range views {
		items = append(items, toProfileViewDTO(view))
	}
	httpx.JSON(w, http.StatusOK, items)
}

// Get GET /api/agents/profiles/{key} (admin).
func (h *ProfileHandler) Get(w http.ResponseWriter, r *http.Request) {
	if _, err := requireAdminActor(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	key, err := parseProfileKey(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("agent profile service is not configured"))
		return
	}
	detail, err := h.service.Get(r.Context(), key)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toProfileDetailDTO(detail))
}

// Patch PATCH /api/agents/profiles/{key} (admin). Only model/budgets/
// allowed_tools are read; enabled/name/system_prompt/key are ignored.
func (h *ProfileHandler) Patch(w http.ResponseWriter, r *http.Request) {
	actor, err := requireAdminActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	key, err := parseProfileKey(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body profilePatchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, r, platform.Invalid("body", "invalid JSON body: "+err.Error()))
		return
	}
	input, err := body.toInput()
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("agent profile service is not configured"))
		return
	}
	view, changed, err := h.service.Patch(r.Context(), actor, key, input)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	writePayload(w, http.StatusOK, profilePatchResponse{Data: toProfileViewDTO(view), Changed: changed})
}

// SavePrompt PUT /api/agents/profiles/{key}/prompt (admin).
func (h *ProfileHandler) SavePrompt(w http.ResponseWriter, r *http.Request) {
	actor, err := requireAdminActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	key, err := parseProfileKey(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body promptSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, r, platform.Invalid("body", "invalid JSON body: "+err.Error()))
		return
	}
	if body.SystemPrompt == nil {
		httpx.Error(w, r, platform.Invalid("system_prompt", "is required"))
		return
	}
	if strings.TrimSpace(*body.SystemPrompt) == "" {
		httpx.Error(w, r, platform.Invalid("system_prompt", "must not be empty"))
		return
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("agent profile service is not configured"))
		return
	}
	view, version, changed, err := h.service.SavePrompt(r.Context(), actor, key, agentapp.SavePromptInput{
		SystemPrompt:    *body.SystemPrompt,
		Note:            body.Note,
		ExpectedVersion: body.ExpectedVersion,
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toPromptMutationDTO(view, version, changed))
}

// ResetPrompt POST /api/agents/profiles/{key}/prompt/reset (admin).
func (h *ProfileHandler) ResetPrompt(w http.ResponseWriter, r *http.Request) {
	actor, err := requireAdminActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	key, err := parseProfileKey(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("agent profile service is not configured"))
		return
	}
	view, version, changed, err := h.service.ResetPrompt(r.Context(), actor, key)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toPromptMutationDTO(view, version, changed))
}

// RestorePrompt POST /api/agents/profiles/{key}/prompt/restore (admin).
func (h *ProfileHandler) RestorePrompt(w http.ResponseWriter, r *http.Request) {
	actor, err := requireAdminActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	key, err := parseProfileKey(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body promptRestoreRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, r, platform.Invalid("body", "invalid JSON body: "+err.Error()))
		return
	}
	if body.Version == nil {
		httpx.Error(w, r, platform.Invalid("version", "is required"))
		return
	}
	if *body.Version <= 0 {
		httpx.Error(w, r, platform.Invalid("version", "must be a positive integer"))
		return
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("agent profile service is not configured"))
		return
	}
	view, version, changed, err := h.service.RestorePrompt(r.Context(), actor, key, *body.Version)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toPromptMutationDTO(view, version, changed))
}

// Revisions GET /api/agents/profiles/{key}/prompt/revisions (admin). The
// cursor is the last revision version of the page.
func (h *ProfileHandler) Revisions(w http.ResponseWriter, r *http.Request) {
	if _, err := requireAdminActor(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	key, err := parseProfileKey(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			httpx.Error(w, r, platform.Invalid("limit", "must be between 1 and 100"))
			return
		}
		limit = parsed
	}
	beforeVersion, err := parseOptionalPositiveInt("before_version", r.URL.Query().Get("before_version"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("agent profile service is not configured"))
		return
	}
	revisions, hasMore, err := h.service.Revisions(r.Context(), key, limit, beforeVersion)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]promptRevisionDTO, 0, len(revisions))
	for _, revision := range revisions {
		items = append(items, toPromptRevisionDTO(revision))
	}
	cursor := ""
	if hasMore && len(items) > 0 {
		cursor = strconv.Itoa(items[len(items)-1].Version)
	}
	httpx.List(w, http.StatusOK, items, cursor, hasMore)
}

// Preview POST /api/agents/profiles/{key}/prompt/preview (admin).
func (h *ProfileHandler) Preview(w http.ResponseWriter, r *http.Request) {
	if _, err := requireAdminActor(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	key, err := parseProfileKey(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body promptPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, r, platform.Invalid("body", "invalid JSON body: "+err.Error()))
		return
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("agent profile service is not configured"))
		return
	}
	rendered, err := h.service.Preview(r.Context(), key, body.SystemPrompt, body.Sample)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, promptPreviewResponse{Rendered: rendered})
}

// Tools GET /api/agents/tools (admin). The catalog is read-only and never
// executes a tool.
func (h *ProfileHandler) Tools(w http.ResponseWriter, r *http.Request) {
	if _, err := requireAdminActor(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("agent profile service is not configured"))
		return
	}
	tools, err := h.service.Tools()
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]agentToolDTO, 0, len(tools))
	for _, tool := range tools {
		parameters := tool.Parameters
		if len(parameters) == 0 {
			parameters = json.RawMessage(`{}`)
		}
		items = append(items, agentToolDTO{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  parameters,
		})
	}
	httpx.JSON(w, http.StatusOK, items)
}

/* -------------------------------- helpers ------------------------------- */

func requireAdminActor(r *http.Request) (platform.Actor, error) {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok || actor.ID <= 0 {
		return platform.Actor{}, platform.Unauthenticated()
	}
	if !actor.IsAdmin() {
		return platform.Actor{}, platform.Forbidden()
	}
	return actor, nil
}

func parseProfileKey(r *http.Request) (string, error) {
	key := r.PathValue("key")
	if !agentProfileKeyPattern.MatchString(key) {
		return "", platform.Invalid("key", "must match ^[a-z][a-z0-9_]{0,63}$")
	}
	return key, nil
}

func parseOptionalPositiveInt(field, raw string) (*int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := strconv.Atoi(trimmed)
	if err != nil || parsed <= 0 {
		return nil, platform.Invalid(field, "must be a positive integer")
	}
	return &parsed, nil
}

// toInput converts the transport PATCH body into the application input. A
// present JSON null (or empty string) clears the model to the default.
func (r profilePatchRequest) toInput() (agentapp.PatchInput, error) {
	input := agentapp.PatchInput{}
	if r.Model != nil {
		input.SetModel = true
		if trimmed := strings.TrimSpace(string(r.Model)); trimmed != "null" {
			var model string
			if err := json.Unmarshal(r.Model, &model); err != nil {
				return agentapp.PatchInput{}, platform.Invalid("model", "must be a string or null")
			}
			input.Model = &model
		}
	}
	if r.Budgets != nil {
		budgets := r.Budgets.toBudgets()
		input.Budgets = &budgets
	}
	if r.AllowedTools != nil {
		tools := append([]string(nil), (*r.AllowedTools)...)
		input.AllowedTools = &tools
	}
	return input, nil
}

// writePayload writes a non-enveloped success body. Only the PATCH response
// needs it because its contract has `changed` as a sibling of `data`.
func writePayload(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

var _ = agent.MaxPromptRunes
