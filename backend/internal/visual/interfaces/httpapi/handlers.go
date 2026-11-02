// Package httpapi exposes the P5 visual REST API (spec §4.2 #30–#38) and the
// minimal quality metrics endpoint. Handlers only parse DTOs, call the visual
// application services and render views; no visual logic lives here. The routes
// are mounted by the assembly root (server) so this package stays independent
// from cmd/server.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	visualapp "github.com/EdouardRichard/EzTest/backend/internal/visual/application"
	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
)

// ConfigService is the subset of the visual configuration service used by the
// HTTP layer (visualapp.ConfigService satisfies it).
type ConfigService interface {
	Upsert(ctx context.Context, versionID, caseAssertionID, actorID int64, in visual.ConfigInput) (visual.Assertion, error)
	List(ctx context.Context, versionID int64, caseVersionID, versionSUTID *int64) ([]visual.Assertion, error)
	Get(ctx context.Context, id int64) (visual.Assertion, error)
}

// BaselineService is the subset of the visual baseline service used by the HTTP
// layer (visualapp.BaselineService satisfies it).
type BaselineService interface {
	Approve(ctx context.Context, versionID, assertionID, evidenceID, actorID int64) (visual.Baseline, error)
	List(ctx context.Context, versionID, assertionID int64) ([]visual.Baseline, error)
	Active(ctx context.Context, assertionID int64) (visual.Baseline, bool, error)
}

// DiffService is the subset of the visual comparison service used by the HTTP
// layer (visualapp.DiffService satisfies it).
type DiffService interface {
	List(ctx context.Context, versionID int64, filter visual.DiffFilter) ([]visual.Diff, bool, error)
	Get(ctx context.Context, versionID, id int64) (visual.Diff, error)
}

// OverviewService serves the version overview and quality metrics
// (visualapp.OverviewService satisfies it).
type OverviewService interface {
	Version(ctx context.Context, versionID int64) (visualapp.Overview, error)
	Global(ctx context.Context, versionID *int64) (visualapp.Metrics, error)
}

// Deps wires the visual HTTP handler.
type Deps struct {
	Configs   ConfigService
	Baselines BaselineService
	Diffs     DiffService
	Overview  OverviewService
}

// Handler serves the P5 visual endpoints.
type Handler struct {
	deps Deps
}

// New builds the visual HTTP handler.
func New(deps Deps) *Handler { return &Handler{deps: deps} }

/* ------------------------------- assertions ------------------------------- */

// ListAssertions GET /api/versions/{versionId}/visual/assertions.
func (h *Handler) ListAssertions(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "versionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	caseVersionID, err := queryIDOptional(r, "caseVersionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	versionSUTID, err := queryIDOptional(r, "versionSutId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.deps.Configs == nil {
		httpx.Error(w, r, platform.Unavailable("visual configuration service is not configured"))
		return
	}
	assertions, err := h.deps.Configs.List(r.Context(), versionID, caseVersionID, versionSUTID)
	if err != nil {
		httpx.Error(w, r, mapError(err))
		return
	}
	views := make([]VisualAssertionView, 0, len(assertions))
	for _, assertion := range assertions {
		view, viewErr := h.assertionView(r.Context(), assertion)
		if viewErr != nil {
			httpx.Error(w, r, viewErr)
			return
		}
		views = append(views, view)
	}
	httpx.List(w, http.StatusOK, views, "", false)
}

// UpsertAssertion PUT /api/versions/{versionId}/visual/assertions/{caseAssertionId}.
func (h *Handler) UpsertAssertion(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "versionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	caseAssertionID, err := pathID(r, "caseAssertionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := requireActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body upsertAssertionRequest
	if err := decodeBody(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}
	input, err := body.toInput()
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.deps.Configs == nil {
		httpx.Error(w, r, platform.Unavailable("visual configuration service is not configured"))
		return
	}
	assertion, err := h.deps.Configs.Upsert(r.Context(), versionID, caseAssertionID, actor.ID, input)
	if err != nil {
		httpx.Error(w, r, mapError(err))
		return
	}
	view, err := h.assertionView(r.Context(), assertion)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}

// GetAssertion GET /api/versions/{versionId}/visual/assertions/{id}.
func (h *Handler) GetAssertion(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "versionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.deps.Configs == nil {
		httpx.Error(w, r, platform.Unavailable("visual configuration service is not configured"))
		return
	}
	assertion, err := h.deps.Configs.Get(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, mapError(err))
		return
	}
	if assertion.VersionID != versionID {
		httpx.Error(w, r, platform.NotFound("visual_assertion"))
		return
	}
	view, err := h.assertionView(r.Context(), assertion)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}

/* -------------------------------- baselines -------------------------------- */

// ListBaselines GET /api/versions/{versionId}/visual/assertions/{id}/baselines.
func (h *Handler) ListBaselines(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "versionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	assertionID, err := pathID(r, "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.deps.Baselines == nil {
		httpx.Error(w, r, platform.Unavailable("visual baseline service is not configured"))
		return
	}
	baselines, err := h.deps.Baselines.List(r.Context(), versionID, assertionID)
	if err != nil {
		httpx.Error(w, r, mapError(err))
		return
	}
	views := make([]VisualBaselineView, 0, len(baselines))
	for _, baseline := range baselines {
		views = append(views, toBaselineView(baseline))
	}
	httpx.List(w, http.StatusOK, views, "", false)
}

// ApproveBaseline POST /api/versions/{versionId}/visual/assertions/{id}/baselines.
// It is mounted behind the admin middleware and additionally checks the actor
// role so a direct call can never approve a baseline.
func (h *Handler) ApproveBaseline(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "versionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	assertionID, err := pathID(r, "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := requireActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if !actor.IsAdmin() {
		httpx.Error(w, r, platform.Forbidden())
		return
	}
	var body approveBaselineRequest
	if err := decodeBody(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}
	evidenceID, err := parseIDString("evidenceId", body.EvidenceID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.deps.Baselines == nil {
		httpx.Error(w, r, platform.Unavailable("visual baseline service is not configured"))
		return
	}
	baseline, err := h.deps.Baselines.Approve(r.Context(), versionID, assertionID, evidenceID, actor.ID)
	if err != nil {
		httpx.Error(w, r, mapError(err))
		return
	}
	httpx.JSON(w, http.StatusCreated, toBaselineView(baseline))
}

/* ---------------------------------- diffs ---------------------------------- */

// ListDiffs GET /api/versions/{versionId}/visual/diffs.
func (h *Handler) ListDiffs(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "versionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	caseRunID, err := queryIDOptional(r, "caseRunId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	caseVersionID, err := queryIDOptional(r, "caseVersionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	classification, err := parseClassification(r.URL.Query().Get("classification"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	limit, beforeID, err := parsePage(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.deps.Diffs == nil {
		httpx.Error(w, r, platform.Unavailable("visual diff service is not configured"))
		return
	}
	diffs, hasMore, err := h.deps.Diffs.List(r.Context(), versionID, visual.DiffFilter{
		CaseRunID: caseRunID, CaseVersionID: caseVersionID, Classification: classification,
		BeforeID: beforeID, Limit: limit,
	})
	if err != nil {
		httpx.Error(w, r, mapError(err))
		return
	}
	views := make([]VisualDiffView, 0, len(diffs))
	for _, diff := range diffs {
		views = append(views, toDiffView(diff))
	}
	nextCursor := ""
	if hasMore && len(diffs) > 0 {
		nextCursor = formatID(diffs[len(diffs)-1].ID)
	}
	httpx.List(w, http.StatusOK, views, nextCursor, hasMore)
}

// GetDiff GET /api/versions/{versionId}/visual/diffs/{diffId}.
func (h *Handler) GetDiff(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "versionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	diffID, err := pathID(r, "diffId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.deps.Diffs == nil {
		httpx.Error(w, r, platform.Unavailable("visual diff service is not configured"))
		return
	}
	diff, err := h.deps.Diffs.Get(r.Context(), versionID, diffID)
	if err != nil {
		httpx.Error(w, r, mapError(err))
		return
	}
	httpx.JSON(w, http.StatusOK, toDiffDetailView(diff))
}

/* -------------------------------- overview -------------------------------- */

// GetOverview GET /api/versions/{versionId}/visual/overview.
func (h *Handler) GetOverview(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "versionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.deps.Overview == nil {
		httpx.Error(w, r, platform.Unavailable("visual overview service is not configured"))
		return
	}
	overview, err := h.deps.Overview.Version(r.Context(), versionID)
	if err != nil {
		httpx.Error(w, r, mapError(err))
		return
	}
	httpx.JSON(w, http.StatusOK, toOverviewView(overview))
}

// GetMetrics GET /api/quality/visual-metrics.
func (h *Handler) GetMetrics(w http.ResponseWriter, r *http.Request) {
	versionID, err := queryIDOptional(r, "versionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.deps.Overview == nil {
		httpx.Error(w, r, platform.Unavailable("visual overview service is not configured"))
		return
	}
	metrics, err := h.deps.Overview.Global(r.Context(), versionID)
	if err != nil {
		httpx.Error(w, r, mapError(err))
		return
	}
	httpx.JSON(w, http.StatusOK, toMetricsView(metrics))
}

/* --------------------------------- helpers -------------------------------- */

func (h *Handler) assertionView(ctx context.Context, assertion visual.Assertion) (VisualAssertionView, error) {
	view := toAssertionView(assertion)
	if h.deps.Baselines == nil {
		return view, nil
	}
	baseline, ok, err := h.deps.Baselines.Active(ctx, assertion.ID)
	if err != nil {
		return VisualAssertionView{}, mapError(err)
	}
	if ok {
		summary := toBaselineSummary(baseline)
		view.Baseline = &summary
	}
	return view, nil
}

type upsertAssertionRequest struct {
	VersionSUTID      *string         `json:"versionSutId"`
	Selector          *string         `json:"selector"`
	Region            json.RawMessage `json:"region"`
	Threshold         *float64        `json:"threshold"`
	Viewport          *viewportDTO    `json:"viewport"`
	DeviceScaleFactor *float64        `json:"deviceScaleFactor"`
	WaitPolicy        json.RawMessage `json:"waitPolicy"`
}

type viewportDTO struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

func (b upsertAssertionRequest) toInput() (visual.ConfigInput, error) {
	var input visual.ConfigInput
	if b.VersionSUTID != nil {
		parsed, err := parseIDString("versionSutId", *b.VersionSUTID)
		if err != nil {
			return visual.ConfigInput{}, err
		}
		input.VersionSUTID = &parsed
	}
	input.Selector = b.Selector
	if len(b.Region) > 0 && string(b.Region) != "null" {
		input.Region = b.Region
	}
	input.Threshold = b.Threshold
	if b.Viewport != nil {
		input.Viewport = &visual.Viewport{Width: b.Viewport.Width, Height: b.Viewport.Height}
	}
	input.DeviceScaleFactor = b.DeviceScaleFactor
	if len(b.WaitPolicy) > 0 && string(b.WaitPolicy) != "null" {
		input.WaitPolicy = b.WaitPolicy
	}
	return input, nil
}

type approveBaselineRequest struct {
	EvidenceID string `json:"evidenceId"`
}

func decodeBody(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return platform.Invalid("body", "invalid JSON body: "+err.Error())
	}
	return nil
}

func pathID(r *http.Request, name string) (int64, error) {
	return parseIDString(name, r.PathValue(name))
}

func parseIDString(field, raw string) (int64, error) {
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

func queryIDOptional(r *http.Request, name string) (*int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil, nil
	}
	parsed, err := parseIDString(name, raw)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func parseClassification(raw string) (visual.Classification, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	classification := visual.Classification(trimmed)
	if !classification.Valid() {
		return "", platform.Invalid("classification", "must be one of passed, failed, inconclusive, blocked")
	}
	return classification, nil
}

func parsePage(r *http.Request) (int, *int64, error) {
	limit := visual.DefaultListLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > visual.MaxListLimit {
			return 0, nil, platform.Invalid("limit", "must be between 1 and 100")
		}
		limit = parsed
	}
	beforeID, err := queryIDOptional(r, "cursor")
	if err != nil {
		return 0, nil, err
	}
	return limit, beforeID, nil
}
