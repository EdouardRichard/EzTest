// Package llmapi exposes the admin LLM configuration and model-catalog HTTP
// API defined by spec §7.2. The server mounts these handlers behind the
// bearer-auth and admin guards; handlers only parse DTOs and map errors.
package llmapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

const maxRequestBody = 1 << 20

// Service is the application surface this handler needs.
type Service interface {
	ListConfigs(ctx context.Context) ([]llm.Config, error)
	GetConfig(ctx context.Context, id int64) (llm.Config, error)
	CreateConfig(ctx context.Context, cmd llmapp.ConfigInput, actorID int64) (llm.Config, error)
	UpdateConfig(ctx context.Context, id int64, cmd llmapp.ConfigInput, actorID int64) (llm.Config, error)
	DeleteConfig(ctx context.Context, id int64, actorID int64) error
	TestConfig(ctx context.Context, id int64) (llmapp.TestResult, error)
	RefreshModels(ctx context.Context, id int64, actorID int64) ([]llm.Model, error)
	AddModel(ctx context.Context, configID int64, model string, contextWindow, maxOutput int, tools, vision bool, actorID int64) (llm.Model, error)
	DeleteModel(ctx context.Context, modelID int64, actorID int64) error
	ListModels(ctx context.Context) ([]llm.Model, error)
}

// Handler serves /api/admin/llm/*.
type Handler struct {
	svc Service
}

// New builds the handler.
func New(svc Service) *Handler {
	return &Handler{svc: svc}
}

// ListConfigs GET /api/admin/llm/configs.
func (h *Handler) ListConfigs(w http.ResponseWriter, r *http.Request) {
	configs, err := h.svc.ListConfigs(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]configDTO, 0, len(configs))
	for _, cfg := range configs {
		items = append(items, toConfigDTO(cfg))
	}
	httpx.List(w, http.StatusOK, items, "", false)
}

// CreateConfig POST /api/admin/llm/configs.
func (h *Handler) CreateConfig(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	request, err := decodeConfigRequest(w, r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	cfg, err := h.svc.CreateConfig(r.Context(), request.input(), actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toConfigDTO(cfg))
}

// GetConfig GET /api/admin/llm/configs/{id}.
func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id", "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	cfg, err := h.svc.GetConfig(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toConfigDTO(cfg))
}

// UpdateConfig PATCH /api/admin/llm/configs/{id}.
func (h *Handler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathID(r, "id", "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	request, err := decodeConfigRequest(w, r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	cfg, err := h.svc.UpdateConfig(r.Context(), id, request.input(), actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toConfigDTO(cfg))
}

// DeleteConfig DELETE /api/admin/llm/configs/{id}.
func (h *Handler) DeleteConfig(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathID(r, "id", "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.DeleteConfig(r.Context(), id, actorID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, deletedDTO{ID: strconv.FormatInt(id, 10), Deleted: true})
}

// TestConfig POST /api/admin/llm/configs/{id}/test.
func (h *Handler) TestConfig(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id", "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	result, err := h.svc.TestConfig(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toTestResultDTO(result))
}

// RefreshModels POST /api/admin/llm/configs/{id}/refresh-models.
func (h *Handler) RefreshModels(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathID(r, "id", "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	models, err := h.svc.RefreshModels(r.Context(), id, actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]modelDTO, 0, len(models))
	for _, model := range models {
		items = append(items, toModelDTO(model))
	}
	httpx.JSON(w, http.StatusOK, items)
}

// AddModel POST /api/admin/llm/configs/{id}/models.
func (h *Handler) AddModel(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	configID, err := pathID(r, "id", "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request addModelRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		httpx.Error(w, r, platform.Invalid("body", "request body must be a JSON object"))
		return
	}
	model, err := h.svc.AddModel(r.Context(), configID, request.Model, request.ContextWindow,
		request.MaxOutput, request.SupportsTools, request.SupportsVision, actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toModelDTO(model))
}

// DeleteModel DELETE /api/admin/llm/configs/{id}/models/{modelId}.
func (h *Handler) DeleteModel(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	// The config ID scopes the route; ownership is enforced by the model row
	// itself, so it is validated but not needed by the application call.
	if _, err := pathID(r, "id", "id"); err != nil {
		httpx.Error(w, r, err)
		return
	}
	modelID, err := pathID(r, "modelId", "model_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.DeleteModel(r.Context(), modelID, actorID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, deletedDTO{ID: strconv.FormatInt(modelID, 10), Deleted: true})
}

// ListModels GET /api/admin/llm/models.
func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	models, err := h.svc.ListModels(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]modelDTO, 0, len(models))
	for _, model := range models {
		items = append(items, toModelDTO(model))
	}
	httpx.List(w, http.StatusOK, items, "", false)
}

func requireActor(w http.ResponseWriter, r *http.Request) (int64, bool) {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok || actor.ID <= 0 {
		httpx.Error(w, r, platform.Unauthenticated())
		return 0, false
	}
	return actor.ID, true
}

func decodeConfigRequest(w http.ResponseWriter, r *http.Request) (configRequest, error) {
	var request configRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return configRequest{}, platform.Invalid("body", "request body must be a JSON object")
	}
	return request, nil
}

func pathID(r *http.Request, name, field string) (int64, error) {
	raw := strings.TrimSpace(r.PathValue(name))
	if raw == "" {
		return 0, platform.Invalid(field, "is required")
	}
	return validation.NonEmptyID(field, raw)
}
