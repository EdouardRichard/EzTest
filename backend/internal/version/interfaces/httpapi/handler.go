// Package versionapi exposes the /api/versions HTTP API defined by spec §7.2.
// The server mounts these handlers behind the bearer-auth guard; handlers only
// parse DTOs and map errors.
package versionapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

const maxRequestBody = 1 << 20

// Service is the application surface the version handlers need.
type Service interface {
	Create(ctx context.Context, cmd versionapp.CreateCommand, actorID int64) (versionapp.VersionView, error)
	Get(ctx context.Context, id int64) (versionapp.VersionView, error)
	List(ctx context.Context, limit int, beforeID *int64) ([]versionapp.VersionSummary, bool, error)
	Update(ctx context.Context, id int64, patch versionapp.Patch, actorID int64) (versionapp.VersionView, error)
}

// Handler serves /api/versions.
type Handler struct {
	svc Service
}

// New builds the version handler.
func New(svc Service) *Handler {
	return &Handler{svc: svc}
}

var _ Service = (*versionapp.Service)(nil)

// Create POST /api/versions.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	var request createVersionRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	command, err := request.command()
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.Create(r.Context(), command, actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toVersionDTO(view))
}

// Get GET /api/versions/{id}.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.Get(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toVersionDTO(view))
}

// List GET /api/versions?limit=&cursor=.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit := pagination.Limit(query.Get("limit"), pagination.DefaultLimit, pagination.MaxLimit)
	cursor, err := pagination.Cursor(strings.TrimSpace(query.Get("cursor")))
	if err != nil {
		httpx.Error(w, r, platform.Invalid("cursor", "must be a decimal ID"))
		return
	}
	var beforeID *int64
	if cursor > 0 {
		beforeID = &cursor
	}
	summaries, hasMore, err := h.svc.List(r.Context(), limit, beforeID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]versionSummaryDTO, 0, len(summaries))
	for _, summary := range summaries {
		items = append(items, toVersionSummaryDTO(summary))
	}
	nextCursor := ""
	if hasMore && len(items) > 0 {
		nextCursor = items[len(items)-1].ID
	}
	httpx.List(w, http.StatusOK, items, nextCursor, hasMore)
}

// Update PATCH /api/versions/{id}.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request updateVersionRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.Update(r.Context(), id, versionapp.Patch{
		Title:                     request.Title,
		AutoDesignNewRequirements: request.AutoDesignNewRequirements,
	}, actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toVersionDTO(view))
}

func requireActor(w http.ResponseWriter, r *http.Request) (int64, bool) {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok || actor.ID <= 0 {
		httpx.Error(w, r, platform.Unauthenticated())
		return 0, false
	}
	return actor.ID, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err := decoder.Decode(dst); err != nil {
		return platform.Invalid("body", "request body must be a valid JSON object")
	}
	return nil
}

func pathID(r *http.Request) (int64, error) {
	return validation.NonEmptyID("id", r.PathValue("id"))
}
