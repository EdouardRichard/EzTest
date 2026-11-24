package agentapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	skillapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/skill"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
)

// SkillService is the admin skill-library application surface.
// *skillapp.Service satisfies it.
type SkillService interface {
	List(ctx context.Context) ([]agent.Skill, error)
	Get(ctx context.Context, name string) (agent.Skill, error)
	Create(ctx context.Context, actor platform.Actor, input agent.Skill, note string) (agent.Skill, error)
	Update(ctx context.Context, actor platform.Actor, name string, update agent.SkillUpdate, note string) (agent.Skill, bool, error)
	Delete(ctx context.Context, actor platform.Actor, name string) error
	Versions(ctx context.Context, name string, limit int, beforeVersion *int) ([]agent.SkillVersion, bool, error)
}

// SkillUsageReader is the production read side of the append-only skill_usage
// ledger (v0.2.4-C T19): one run's found/miss facts in seq order.
// *skillapp.Service satisfies it; a SkillService without this surface leaves
// the usage route failing closed (503), never silently unaccounted.
type SkillUsageReader interface {
	Usage(ctx context.Context, runID int64) ([]agent.SkillUsage, error)
}

// SkillHandler serves the admin skill-library API. Every route is mounted
// behind RequireAdmin; each handler additionally re-checks the role
// (profile_handler precedent). Skill bodies are management-surface content:
// normal users never read them through this API.
type SkillHandler struct {
	service SkillService
	usage   SkillUsageReader
}

// NewSkillHandler builds the admin skill handler. The usage read surface is
// adopted when the service implements it (*skillapp.Service does).
func NewSkillHandler(service SkillService) *SkillHandler {
	handler := &SkillHandler{service: service}
	if reader, ok := service.(SkillUsageReader); ok {
		handler.usage = reader
	}
	return handler
}

var _ SkillService = (*skillapp.Service)(nil)

// RegisterSkillRoutes mounts the admin skill operations. A nil handler keeps
// the route table of a deployment without the skill library unchanged.
func RegisterSkillRoutes(mux *http.ServeMux, admin func(http.Handler) http.Handler, h *SkillHandler) {
	if mux == nil || admin == nil || h == nil {
		return
	}
	mux.Handle("GET /api/skills", admin(http.HandlerFunc(h.List)))
	mux.Handle("POST /api/skills", admin(http.HandlerFunc(h.Create)))
	mux.Handle("GET /api/skills/{name}", admin(http.HandlerFunc(h.Get)))
	mux.Handle("PATCH /api/skills/{name}", admin(http.HandlerFunc(h.Update)))
	mux.Handle("DELETE /api/skills/{name}", admin(http.HandlerFunc(h.Delete)))
	mux.Handle("GET /api/skills/{name}/versions", admin(http.HandlerFunc(h.Versions)))
	// skill_usage ledger read side (T19). A distinct collection path so a
	// package legitimately named "usage" is never shadowed by {name}.
	mux.Handle("GET /api/skill-usage", admin(http.HandlerFunc(h.Usage)))
}

// List GET /api/skills (admin).
func (h *SkillHandler) List(w http.ResponseWriter, r *http.Request) {
	if _, err := requireAdminActor(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("skill service is not configured"))
		return
	}
	skills, err := h.service.List(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]skillViewDTO, 0, len(skills))
	for _, skill := range skills {
		items = append(items, toSkillViewDTO(skill))
	}
	httpx.JSON(w, http.StatusOK, items)
}

// Get GET /api/skills/{name} (admin; includes the body).
func (h *SkillHandler) Get(w http.ResponseWriter, r *http.Request) {
	if _, err := requireAdminActor(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	name, err := parseSkillName(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("skill service is not configured"))
		return
	}
	skill, err := h.service.Get(r.Context(), name)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toSkillViewDTO(skill))
}

// Create POST /api/skills (admin). Unknown body fields are rejected so a
// misspelled field never silently disappears.
func (h *SkillHandler) Create(w http.ResponseWriter, r *http.Request) {
	actor, err := requireAdminActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body skillCreateRequest
	if err := decodeSkillBody(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if body.Name == nil || strings.TrimSpace(*body.Name) == "" {
		httpx.Error(w, r, platform.Invalid("name", "is required"))
		return
	}
	if body.Body == nil {
		httpx.Error(w, r, platform.Invalid("body", "is required"))
		return
	}
	if body.Description == nil {
		httpx.Error(w, r, platform.Invalid("description", "is required"))
		return
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	input := agent.Skill{
		Name:        *body.Name,
		Description: *body.Description,
		Body:        *body.Body,
		Metadata:    body.Metadata,
		Enabled:     enabled,
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("skill service is not configured"))
		return
	}
	stored, err := h.service.Create(r.Context(), actor, input, body.Note)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toSkillViewDTO(stored))
}

// Update PATCH /api/skills/{name} (admin). Only provided fields change; the
// edit appends one append-only version and is visible to the next read
// without a restart.
func (h *SkillHandler) Update(w http.ResponseWriter, r *http.Request) {
	actor, err := requireAdminActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	name, err := parseSkillName(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body skillPatchRequest
	if err := decodeSkillBody(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}
	update := agent.SkillUpdate{
		Description:     body.Description,
		Body:            body.Body,
		Metadata:        body.Metadata,
		Enabled:         body.Enabled,
		ExpectedVersion: body.ExpectedVersion,
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("skill service is not configured"))
		return
	}
	stored, changed, err := h.service.Update(r.Context(), actor, name, update, body.Note)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	writePayload(w, http.StatusOK, skillPatchResponse{Data: toSkillViewDTO(stored), Changed: changed})
}

// Delete DELETE /api/skills/{name} (admin). The version history is kept.
func (h *SkillHandler) Delete(w http.ResponseWriter, r *http.Request) {
	actor, err := requireAdminActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	name, err := parseSkillName(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("skill service is not configured"))
		return
	}
	if err := h.service.Delete(r.Context(), actor, name); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Versions GET /api/skills/{name}/versions (admin). The cursor is the last
// version of the page.
func (h *SkillHandler) Versions(w http.ResponseWriter, r *http.Request) {
	if _, err := requireAdminActor(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	name, err := parseSkillName(r)
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
		httpx.Error(w, r, platform.Unavailable("skill service is not configured"))
		return
	}
	versions, hasMore, err := h.service.Versions(r.Context(), name, limit, beforeVersion)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]skillVersionDTO, 0, len(versions))
	for _, version := range versions {
		items = append(items, toSkillVersionDTO(version))
	}
	cursor := ""
	if hasMore && len(items) > 0 {
		cursor = strconv.Itoa(items[len(items)-1].Version)
	}
	httpx.List(w, http.StatusOK, items, cursor, hasMore)
}

/* -------------------------------- helpers ------------------------------- */

// Usage GET /api/skill-usage?run_id= (admin): one run's append-only skill_usage
// facts in seq order — the production read side of the ledger (T19).
func (h *SkillHandler) Usage(w http.ResponseWriter, r *http.Request) {
	if _, err := requireAdminActor(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	runID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("run_id")), 10, 64)
	if err != nil || runID <= 0 {
		httpx.Error(w, r, platform.Invalid("run_id", "must be a positive decimal agent run id"))
		return
	}
	if h == nil || h.usage == nil {
		httpx.Error(w, r, platform.Unavailable("skill usage ledger is not configured"))
		return
	}
	rows, err := h.usage.Usage(r.Context(), runID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]skillUsageDTO, 0, len(rows))
	for _, row := range rows {
		items = append(items, skillUsageDTO{
			RunID:     strconv.FormatInt(row.RunID, 10),
			Seq:       row.Seq,
			SkillName: row.SkillName,
			Found:     row.Found,
			CreatedAt: formatTime(row.CreatedAt),
		})
	}
	httpx.JSON(w, http.StatusOK, items)
}

func parseSkillName(r *http.Request) (string, error) {
	name := r.PathValue("name")
	if err := agent.ValidateSkillName(name); err != nil {
		return "", err
	}
	return name, nil
}

func decodeSkillBody(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return platform.Invalid("body", "invalid JSON body: "+err.Error())
	}
	return nil
}

/* ---------------------------------- DTOs ---------------------------------- */

type skillViewDTO struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Body        string          `json:"body"`
	Metadata    json.RawMessage `json:"metadata"`
	Version     int             `json:"version"`
	Enabled     bool            `json:"enabled"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

type skillVersionDTO struct {
	Version     int             `json:"version"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Body        string          `json:"body"`
	Metadata    json.RawMessage `json:"metadata"`
	Enabled     bool            `json:"enabled"`
	Source      string          `json:"source"`
	ActorID     *string         `json:"actor_id"`
	Note        string          `json:"note"`
	CreatedAt   string          `json:"created_at"`
}

// skillUsageDTO is one append-only skill_usage fact (T19 ledger read side).
type skillUsageDTO struct {
	RunID     string `json:"run_id"`
	Seq       int    `json:"seq"`
	SkillName string `json:"skill_name"`
	Found     bool   `json:"found"`
	CreatedAt string `json:"created_at"`
}

type skillCreateRequest struct {
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	Body        *string         `json:"body"`
	Metadata    json.RawMessage `json:"metadata"`
	Enabled     *bool           `json:"enabled"`
	Note        string          `json:"note"`
}

type skillPatchRequest struct {
	Description     *string         `json:"description"`
	Body            *string         `json:"body"`
	Metadata        json.RawMessage `json:"metadata"`
	Enabled         *bool           `json:"enabled"`
	Note            string          `json:"note"`
	ExpectedVersion *int            `json:"expected_version"`
}

// skillPatchResponse mirrors the profile PATCH contract: `changed` is a
// sibling of `data`.
type skillPatchResponse struct {
	Data    skillViewDTO `json:"data"`
	Changed bool         `json:"changed"`
}

func toSkillViewDTO(skill agent.Skill) skillViewDTO {
	metadata := skill.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	return skillViewDTO{
		ID:          strconv.FormatInt(skill.ID, 10),
		Name:        skill.Name,
		Description: skill.Description,
		Body:        skill.Body,
		Metadata:    metadata,
		Version:     skill.Version,
		Enabled:     skill.Enabled,
		CreatedAt:   formatTime(skill.CreatedAt),
		UpdatedAt:   formatTime(skill.UpdatedAt),
	}
}

func toSkillVersionDTO(version agent.SkillVersion) skillVersionDTO {
	metadata := version.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	dto := skillVersionDTO{
		Version:     version.Version,
		Name:        version.Name,
		Description: version.Description,
		Body:        version.Body,
		Metadata:    metadata,
		Enabled:     version.Enabled,
		Source:      version.Source,
		Note:        version.Note,
		CreatedAt:   formatTime(version.CreatedAt),
	}
	if version.ActorID != nil {
		actorID := strconv.FormatInt(*version.ActorID, 10)
		dto.ActorID = &actorID
	}
	return dto
}
