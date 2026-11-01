// Package versionapi exposes the /api/versions HTTP API defined by spec §7.2.
// The server mounts these handlers behind the bearer-auth guard; handlers only
// parse DTOs and map errors.
package versionapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

const maxRequestBody = 1 << 20

// Service is the application surface the version handlers need.
type Service interface {
	Create(ctx context.Context, cmd versionapp.CreateCommand, actorID int64) (versionapp.VersionView, error)
	Get(ctx context.Context, id int64) (versionapp.VersionView, error)
	List(ctx context.Context, limit int, beforeID *int64) ([]versionapp.VersionSummary, bool, error)
	Update(ctx context.Context, id int64, patch versionapp.Patch, actorID int64) (versionapp.VersionView, error)
}

// VersionReopenService is the application surface for the manual reopen path.
// The version close service owns the completed -> running transition, so the
// HTTP layer never writes the version row directly.
type VersionReopenService interface {
	ReopenIfCompleted(ctx context.Context, versionID int64, trigger, reason string) error
}

// Option configures the optional P4 version dependencies.
type Option func(*Handler)

// WithVersionReopener wires the application service behind
// POST /api/versions/{versionId}/reopen.
func WithVersionReopener(reopen VersionReopenService) Option {
	return func(h *Handler) { h.reopen = reopen }
}

// Handler serves /api/versions.
type Handler struct {
	svc    Service
	reopen VersionReopenService
}

// New builds the version handler. The reopen service is optional so the P0/P1
// read/write routes keep working in assemblies that do not wire P4 yet.
func New(svc Service, opts ...Option) *Handler {
	handler := &Handler{svc: svc}
	for _, opt := range opts {
		opt(handler)
	}
	return handler
}

var (
	_ Service              = (*versionapp.Service)(nil)
	_ VersionReopenService = (*versionapp.VersionCloseService)(nil)
)

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

// RegisterP4 mounts the P4 version/requirement reopen routes on mux. The
// routes must be mounted behind the server's bearer authentication middleware
// (Task 23 wiring); the handlers never parse credentials themselves.
func RegisterP4(mux *http.ServeMux, versions *Handler, requirements *RequirementHandler) {
	mux.HandleFunc("POST /api/versions/{versionId}/reopen", versions.Reopen)
	mux.HandleFunc(
		"POST /api/versions/{versionId}/requirements/{requirementId}/reopen",
		requirements.Reopen,
	)
}

// Reopen POST /api/versions/{versionId}/reopen. Only a completed version can be
// reopened (completed -> running); every other state is a 409. The response is
// the refreshed version detail, whose close projection is now cleared.
func (h *Handler) Reopen(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	versionID, err := validation.NonEmptyID("versionId", r.PathValue("versionId"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request reopenVersionRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	reason := strings.TrimSpace(request.Reason)
	if reason == "" {
		httpx.Error(w, r, platform.Invalid("reason", "must not be empty"))
		return
	}
	if h.reopen == nil {
		httpx.Error(w, r, errors.New("version reopen service is not configured"))
		return
	}
	current, err := h.svc.Get(r.Context(), versionID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if current.Status != string(version.StatusCompleted) {
		httpx.Error(w, r, platform.StateConflict("only a completed version can be reopened"))
		return
	}
	if err := h.reopen.ReopenIfCompleted(r.Context(), versionID, versionapp.VersionReopenTriggerManual, reason); err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.Get(r.Context(), versionID)
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
