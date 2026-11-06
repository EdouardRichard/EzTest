// Package httpapi exposes the P6 quality REST API (frozen contract):
//
//	GET  /api/quality/eval-runs        admin   list terminal eval runs
//	POST /api/quality/eval-runs        admin   record a terminal run
//	GET  /api/quality/eval-runs/{id}   admin   one run with its metrics
//	GET  /api/quality/overview         login   operational metrics + last eval
//
// Handlers only parse/validate transport input, call the quality application
// service and render views; metric derivation stays in the domain. The assembly
// root mounts these routes (Task 22), so this package never imports server.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	qualityapp "github.com/EdouardRichard/EzTest/backend/internal/quality/application"
	quality "github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
)

// Service is the subset of *qualityapp.Service used by the HTTP layer.
type Service interface {
	List(ctx context.Context, limit int) ([]qualityapp.EvalRun, error)
	Record(ctx context.Context, actor platform.Actor, input qualityapp.RecordInput) (qualityapp.EvalRun, error)
	Get(ctx context.Context, id int64) (qualityapp.EvalRun, error)
	Overview(ctx context.Context, versionID *int64) (qualityapp.Overview, error)
	TriggerSnapshot(ctx context.Context, actor platform.Actor, versionID int64) (qualityapp.EvalRun, error)
	Trends(ctx context.Context, versionID int64, metric string, from, to time.Time) ([]qualityapp.TrendPoint, error)
}

// Handler serves the P6 quality endpoints.
type Handler struct {
	service Service
}

// New builds the quality HTTP handler.
func New(service Service) *Handler { return &Handler{service: service} }

// *qualityapp.Service must satisfy the handler's local Service port; the
// assembly root relies on this when wiring Task 22.
var _ Service = (*qualityapp.Service)(nil)

// ListEvalRuns GET /api/quality/eval-runs (admin).
func (h *Handler) ListEvalRuns(w http.ResponseWriter, r *http.Request) {
	if _, err := requireAdmin(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	limit := qualityapp.DefaultListLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > qualityapp.MaxListLimit {
			httpx.Error(w, r, platform.Invalid("limit", "must be between 1 and 50"))
			return
		}
		limit = parsed
	}
	if h.service == nil {
		httpx.Error(w, r, platform.Unavailable("quality service is not configured"))
		return
	}
	runs, err := h.service.List(r.Context(), limit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	views := make([]evalRunView, 0, len(runs))
	for _, run := range runs {
		views = append(views, toEvalRunView(run))
	}
	// eval-runs has no cursor: every page is the newest `limit` runs.
	httpx.List(w, http.StatusOK, views, "", false)
}

// CreateEvalRun POST /api/quality/eval-runs (admin). The server derives the
// metrics and status; a client-sent passed/failed verdict is ignored, not
// rejected, and only `status=error` is honored.
func (h *Handler) CreateEvalRun(w http.ResponseWriter, r *http.Request) {
	actor, err := requireAdmin(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body createEvalRunRequest
	decoder := json.NewDecoder(r.Body)
	// Unknown request fields are ignored on purpose (0.2 contract): a client
	// verdict, `passed` or any future field never becomes a 400.
	if err := decoder.Decode(&body); err != nil {
		httpx.Error(w, r, platform.Invalid("body", "invalid JSON body: "+err.Error()))
		return
	}
	if h.service == nil {
		httpx.Error(w, r, platform.Unavailable("quality service is not configured"))
		return
	}
	// W2-14 (R-W2-6): mode=snapshot is a server-derived trigger, not a client
	// sample. version_id is required and the sample/verdict fields are ignored.
	if strings.TrimSpace(body.Mode) == quality.EvalModeSnapshot {
		versionID, err := parsePositiveID("version_id", body.VersionID)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		run, err := h.service.TriggerSnapshot(r.Context(), actor, versionID)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, toEvalRunView(run))
		return
	}
	run, err := h.service.Record(r.Context(), actor, body.toInput())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toEvalRunView(run))
}

// GetEvalRun GET /api/quality/eval-runs/{id} (admin).
func (h *Handler) GetEvalRun(w http.ResponseWriter, r *http.Request) {
	if _, err := requireAdmin(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	id, err := parsePositiveID("id", r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.service == nil {
		httpx.Error(w, r, platform.Unavailable("quality service is not configured"))
		return
	}
	run, err := h.service.Get(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toEvalRunView(run))
}

// GetOverview GET /api/quality/overview (any logged-in user).
func (h *Handler) GetOverview(w http.ResponseWriter, r *http.Request) {
	if _, err := requireActor(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	versionID, err := optionalPositiveID("version_id", r.URL.Query().Get("version_id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.service == nil {
		httpx.Error(w, r, platform.Unavailable("quality service is not configured"))
		return
	}
	overview, err := h.service.Overview(r.Context(), versionID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toQualityOverviewView(overview))
}

// GetTrends GET /api/quality/trends?version_id=&metric=&from=&to= (admin).
// Returns the version/metric eval series oldest first; from/to are optional
// RFC3339 bounds. A baseline regression is carried as a flag only (warn-only).
func (h *Handler) GetTrends(w http.ResponseWriter, r *http.Request) {
	if _, err := requireAdmin(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	query := r.URL.Query()
	versionID, err := parsePositiveID("version_id", query.Get("version_id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	metric := strings.TrimSpace(query.Get("metric"))
	if metric == "" {
		httpx.Error(w, r, platform.Invalid("metric", "is required"))
		return
	}
	if !quality.IsKnownMetric(metric) {
		httpx.Error(w, r, platform.Invalid("metric", "is not a known eval metric"))
		return
	}
	from, err := parseOptionalTime("from", query.Get("from"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	to, err := parseOptionalTime("to", query.Get("to"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.service == nil {
		httpx.Error(w, r, platform.Unavailable("quality service is not configured"))
		return
	}
	points, err := h.service.Trends(r.Context(), versionID, metric, from, to)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	views := make([]trendPointView, 0, len(points))
	for _, point := range points {
		views = append(views, toTrendPointView(point))
	}
	httpx.JSON(w, http.StatusOK, views)
}

/* --------------------------------- helpers -------------------------------- */

// parseOptionalTime parses an optional RFC3339 query bound. An empty value means
// unbounded and returns the zero time.
func parseOptionalTime(field, raw string) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return time.Time{}, platform.Invalid(field, "must be an RFC3339 timestamp")
	}
	return parsed.UTC(), nil
}

func requireActor(r *http.Request) (platform.Actor, error) {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok {
		return platform.Actor{}, platform.Unauthenticated()
	}
	return actor, nil
}

// requireAdmin is defense in depth: the assembly root also mounts the eval
// routes behind RequireAdmin, but a direct handler call must never leak runs to
// a non-admin.
func requireAdmin(r *http.Request) (platform.Actor, error) {
	actor, err := requireActor(r)
	if err != nil {
		return platform.Actor{}, err
	}
	if !actor.IsAdmin() {
		return platform.Actor{}, platform.Forbidden()
	}
	return actor, nil
}

func parsePositiveID(field, raw string) (int64, error) {
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

func optionalPositiveID(field, raw string) (*int64, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parsed, err := parsePositiveID(field, raw)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
