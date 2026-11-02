// Package settingsapi exposes the admin settings-center HTTP API.
package settingsapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
	"github.com/EdouardRichard/EzTest/backend/internal/settings/domain"
)

// Service is the application surface this handler needs.
type Service interface {
	List(ctx context.Context) ([]settings.Setting, error)
	Get(ctx context.Context, key string) (settings.Setting, error)
	Update(ctx context.Context, key string, value any, actorID int64) (settings.Setting, error)
	Revisions(ctx context.Context, key string, limit int, beforeID *int64) ([]settings.Revision, error)
}

// Handler serves /api/admin/settings.
type Handler struct {
	svc Service
}

// New builds the handler.
func New(svc Service) *Handler {
	return &Handler{svc: svc}
}

// List GET /api/admin/settings.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.List(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]settingDTO, 0, len(list))
	for _, item := range list {
		items = append(items, toSettingDTO(item))
	}
	httpx.JSON(w, http.StatusOK, items)
}

// Get GET /api/admin/settings/{key}.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	item, err := h.svc.Get(r.Context(), r.PathValue("key"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toSettingDTO(item))
}

// Update PATCH /api/admin/settings/{key}.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, platform.Unauthenticated())
		return
	}
	var body updateRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		httpx.Error(w, r, platform.Invalid("body", "request body must be {\"value\": ...}"))
		return
	}
	if body.Value == nil {
		httpx.Error(w, r, platform.Invalid("value", "value is required"))
		return
	}
	item, err := h.svc.Update(r.Context(), r.PathValue("key"), body.Value, actor.ID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toSettingDTO(item))
}

// Revisions GET /api/admin/settings/revisions?key=&limit=&cursor=.
func (h *Handler) Revisions(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		httpx.Error(w, r, platform.Invalid("key", "key is required"))
		return
	}
	item, err := h.svc.Get(r.Context(), key)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	limit := pagination.Limit(r.URL.Query().Get("limit"), pagination.DefaultLimit, pagination.MaxLimit)
	cursor, err := pagination.Cursor(r.URL.Query().Get("cursor"))
	if err != nil {
		httpx.Error(w, r, platform.Invalid("cursor", "cursor must be a decimal ID"))
		return
	}
	var beforeID *int64
	if cursor > 0 {
		beforeID = &cursor
	}
	revisions, err := h.svc.Revisions(r.Context(), key, limit+1, beforeID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	hasMore := len(revisions) > limit
	if hasMore {
		revisions = revisions[:limit]
	}
	items := make([]revisionDTO, 0, len(revisions))
	for _, revision := range revisions {
		items = append(items, toRevisionDTO(revision, item.Secret))
	}
	var nextCursor string
	if hasMore && len(revisions) > 0 {
		nextCursor = pagination.NextCursor(true, revisions[len(revisions)-1].ID)
	}
	httpx.List(w, http.StatusOK, items, nextCursor, hasMore)
}

type updateRequest struct {
	Value any `json:"value"`
}

type settingDTO struct {
	Key             string `json:"key"`
	Category        string `json:"category"`
	Type            string `json:"type"`
	Value           any    `json:"value"`
	IsSecret        bool   `json:"is_secret"`
	RestartRequired bool   `json:"restart_required"`
	Description     string `json:"description"`
	Version         int    `json:"version"`
	UpdatedAt       string `json:"updated_at"`
}

func toSettingDTO(item settings.Setting) settingDTO {
	dto := settingDTO{
		Key:             item.Key,
		Category:        item.Category,
		Type:            string(item.Type),
		IsSecret:        item.Secret,
		RestartRequired: item.RestartRequired,
		Description:     item.Description,
		Version:         item.Version,
		UpdatedAt:       item.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if item.Secret {
		dto.Value = nil
		return dto
	}
	var value any
	if err := json.Unmarshal(item.Value, &value); err == nil {
		dto.Value = value
	}
	return dto
}

type revisionDTO struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	OldValue  any    `json:"old_value"`
	NewValue  any    `json:"new_value"`
	ActorID   string `json:"actor_id,omitempty"`
	CreatedAt string `json:"created_at"`
}

func toRevisionDTO(item settings.Revision, secret bool) revisionDTO {
	dto := revisionDTO{
		ID:        strconv.FormatInt(item.ID, 10),
		Key:       item.Key,
		CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339),
	}
	if item.ActorID != nil {
		dto.ActorID = strconv.FormatInt(*item.ActorID, 10)
	}
	if secret {
		return dto
	}
	var oldValue, newValue any
	if len(item.OldValue) > 0 {
		_ = json.Unmarshal(item.OldValue, &oldValue)
	}
	if len(item.NewValue) > 0 {
		_ = json.Unmarshal(item.NewValue, &newValue)
	}
	dto.OldValue = oldValue
	dto.NewValue = newValue
	return dto
}
