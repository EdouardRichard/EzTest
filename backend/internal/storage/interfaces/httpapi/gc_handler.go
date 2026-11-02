package storageapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

const gcRequestBodyLimit = 1 << 20

// GCService is the application surface the admin GC handler needs
// (*storageapp.GCService satisfies it).
type GCService interface {
	Run(ctx context.Context, mode storage.GCMode, actorID int64) (storage.GCRun, error)
	ListRuns(ctx context.Context, limit int) ([]storage.GCRun, error)
	GetRun(ctx context.Context, runID int64) (storageapp.GCRunDetail, error)
}

// GCHandler serves the admin GC endpoints (spec §6.2, tag Admin):
//
//	POST /api/admin/gc/runs          admin  start a dry_run/apply pass
//	GET  /api/admin/gc/runs          admin  run history
//	GET  /api/admin/gc/runs/{id}     admin  run detail + report + tombstones
type GCHandler struct {
	service GCService
}

// NewGC builds the admin GC handler.
func NewGC(service GCService) *GCHandler { return &GCHandler{service: service} }

// RegisterGCRoutes mounts the admin GC endpoints. The assembly root (Task 19)
// calls it with RequireAdmin as the gate.
func RegisterGCRoutes(mux *http.ServeMux, admin func(http.Handler) http.Handler, handler *GCHandler) {
	if mux == nil || handler == nil {
		return
	}
	mux.Handle("POST /api/admin/gc/runs", admin(http.HandlerFunc(handler.CreateRun)))
	mux.Handle("GET /api/admin/gc/runs", admin(http.HandlerFunc(handler.ListRuns)))
	mux.Handle("GET /api/admin/gc/runs/{gcRunId}", admin(http.HandlerFunc(handler.GetRun)))
}

type gcRunRequest struct {
	Mode    string `json:"mode"`
	Confirm bool   `json:"confirm"`
}

// CreateRun POST /api/admin/gc/runs. Apply mode requires confirm=true; the
// application single-running index maps to 409 gc_running.
func (h *GCHandler) CreateRun(w http.ResponseWriter, r *http.Request) {
	actor, err := requireGCAdmin(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request gcRunRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, gcRequestBodyLimit))
	if err := decoder.Decode(&request); err != nil {
		httpx.Error(w, r, platform.Invalid("body", "request body must be a JSON object"))
		return
	}
	mode, err := storage.ParseGCMode(request.Mode)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if mode.RequiresConfirmation() && !request.Confirm {
		httpx.Error(w, r, platform.NewError(http.StatusBadRequest, "confirmation_required", "apply mode requires confirm=true"))
		return
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("gc service is not configured"))
		return
	}
	run, err := h.service.Run(r.Context(), mode, actor.ID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toGCRunDTO(run))
}

// ListRuns GET /api/admin/gc/runs?limit=.
func (h *GCHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	if _, err := requireGCAdmin(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > storageapp.MaxGCRunLimit {
			httpx.Error(w, r, platform.Invalid("limit", "limit must be between 1 and 100"))
			return
		}
		limit = parsed
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("gc service is not configured"))
		return
	}
	runs, err := h.service.ListRuns(r.Context(), limit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	views := make([]gcRunDTO, 0, len(runs))
	for _, run := range runs {
		views = append(views, toGCRunDTO(run))
	}
	httpx.List(w, http.StatusOK, views, "", false)
}

// GetRun GET /api/admin/gc/runs/{gcRunId}.
func (h *GCHandler) GetRun(w http.ResponseWriter, r *http.Request) {
	if _, err := requireGCAdmin(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	runID, err := parseGCPositiveID("gc_run_id", r.PathValue("gcRunId"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("gc service is not configured"))
		return
	}
	detail, err := h.service.GetRun(r.Context(), runID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view := gcRunDetailDTO{gcRunDTO: toGCRunDTO(detail.Run), Tombstones: make([]gcTombstoneDTO, 0, len(detail.Tombstones))}
	for _, tombstone := range detail.Tombstones {
		view.Tombstones = append(view.Tombstones, toGCTombstoneDTO(tombstone))
	}
	httpx.JSON(w, http.StatusOK, view)
}

// requireGCAdmin is defense in depth: the assembly root also mounts these
// routes behind RequireAdmin, but a direct handler call must never expose or
// start GC for a non-admin.
func requireGCAdmin(r *http.Request) (platform.Actor, error) {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok {
		return platform.Actor{}, platform.Unauthenticated()
	}
	if !actor.IsAdmin() {
		return platform.Actor{}, platform.Forbidden()
	}
	return actor, nil
}

func parseGCPositiveID(field, raw string) (int64, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, platform.Invalid(field, "is required")
	}
	parsed, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, platform.Invalid(field, "must be a positive decimal ID")
	}
	return parsed, nil
}
