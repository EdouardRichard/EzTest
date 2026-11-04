package versionapi

// Task 62 (0.2.1): read-only, version-scoped API for the version fact store,
// the task plan and the agent work-item (job) queue.
//
//	GET /api/versions/{id}/facts
//	GET /api/versions/{id}/plan-items
//	GET /api/versions/{id}/work-items
//	GET /api/versions/{id}/work-items/{workItemId}
//
// The handlers only parse path/query input and render the frozen OpenAPI DTOs:
// version ownership, cursor normalization and the frozen limit bounds live in
// the application services, and the version existence probe turns a missing or
// cross-context version into a 404 before any list read. Nothing here writes.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// VersionReadService is the version-context read surface of the facts and
// plan-item endpoints. *versionapp.VersionReadService implements it.
type VersionReadService interface {
	VersionExists(ctx context.Context, versionID int64) (bool, error)
	Facts(ctx context.Context, versionID int64, filter versionapp.FactReadFilter) ([]versionapp.FactReadView, bool, error)
	PlanItems(ctx context.Context, versionID int64, filter versionapp.PlanItemReadFilter) ([]versionapp.PlanItemReadView, bool, error)
}

// WorkItemReadService is the agent-context work-item read surface consumed by
// the version-scoped routes. *agentapp.QueryService implements it; the version
// context never reads work_item storage directly.
type WorkItemReadService interface {
	WorkItems(ctx context.Context, filter agentapp.WorkItemFilter) ([]agentapp.WorkItemReadView, bool, error)
	WorkItem(ctx context.Context, versionID, workItemID int64) (agentapp.WorkItemReadView, error)
}

var (
	_ VersionReadService  = (*versionapp.VersionReadService)(nil)
	_ WorkItemReadService = (*agentapp.QueryService)(nil)
)

// VersionReadHandler serves the 0.2.1 version-scoped read routes. Every route
// is mounted behind the server's bearer authentication middleware.
type VersionReadHandler struct {
	versions  VersionReadService
	workItems WorkItemReadService
}

// NewVersionReadHandler builds the handler. Both services are required by the
// read paths; a nil service answers 503 instead of panicking.
func NewVersionReadHandler(versions VersionReadService, workItems WorkItemReadService) *VersionReadHandler {
	return &VersionReadHandler{versions: versions, workItems: workItems}
}

// RegisterVersionReadRoutes mounts the four read-only routes on mux. A nil
// handler, mux or authentication middleware leaves the route table unchanged.
// The assembly root owns the authentication middleware.
func RegisterVersionReadRoutes(mux *http.ServeMux, auth func(http.Handler) http.Handler, h *VersionReadHandler) {
	if mux == nil || auth == nil || h == nil {
		return
	}
	mux.Handle("GET /api/versions/{id}/facts", auth(http.HandlerFunc(h.Facts)))
	mux.Handle("GET /api/versions/{id}/plan-items", auth(http.HandlerFunc(h.PlanItems)))
	mux.Handle("GET /api/versions/{id}/work-items", auth(http.HandlerFunc(h.ListWorkItems)))
	mux.Handle("GET /api/versions/{id}/work-items/{workItemId}", auth(http.HandlerFunc(h.GetWorkItem)))
}

// Facts GET /api/versions/{id}/facts?kind=&include_superseded=&cursor=&limit=.
// Default (include_superseded omitted/false) returns only the current facts;
// true returns the whole append-only history for conflict display.
func (h *VersionReadHandler) Facts(w http.ResponseWriter, r *http.Request) {
	versionID, err := versionReadPathID(r, "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	query := r.URL.Query()
	includeSuperseded, err := parseIncludeSuperseded(query.Get("include_superseded"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	before, limit, err := parseReadPage(query)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if !h.requireVersion(w, r, versionID) {
		return
	}
	views, hasMore, err := h.versions.Facts(r.Context(), versionID, versionapp.FactReadFilter{
		Kind:              strings.TrimSpace(query.Get("kind")),
		IncludeSuperseded: includeSuperseded,
		BeforeID:          before,
		Limit:             limit,
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]factReadDTO, 0, len(views))
	for _, view := range views {
		items = append(items, toFactReadDTO(view))
	}
	httpx.List(w, http.StatusOK, items, nextReadCursor(hasMore, lastReadID(views, factReadViewID)), hasMore)
}

// PlanItems GET /api/versions/{id}/plan-items?status=&cursor=&limit=. Each item
// carries its depends_on list, status and optional work_item_id.
func (h *VersionReadHandler) PlanItems(w http.ResponseWriter, r *http.Request) {
	versionID, err := versionReadPathID(r, "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	query := r.URL.Query()
	before, limit, err := parseReadPage(query)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if !h.requireVersion(w, r, versionID) {
		return
	}
	views, hasMore, err := h.versions.PlanItems(r.Context(), versionID, versionapp.PlanItemReadFilter{
		Status:   strings.TrimSpace(query.Get("status")),
		BeforeID: before,
		Limit:    limit,
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]planItemReadDTO, 0, len(views))
	for _, view := range views {
		items = append(items, toPlanItemReadDTO(view))
	}
	httpx.List(w, http.StatusOK, items, nextReadCursor(hasMore, lastReadID(views, planItemReadViewID)), hasMore)
}

// ListWorkItems GET /api/versions/{id}/work-items?kind=&state=&created_by=&cursor=&limit=.
// Only the version's 0.2.1 work items are returned; legacy rows carry no kind
// and are fenced out by the read source.
func (h *VersionReadHandler) ListWorkItems(w http.ResponseWriter, r *http.Request) {
	versionID, err := versionReadPathID(r, "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h == nil || h.workItems == nil {
		httpx.Error(w, r, platform.Unavailable("version work-item read model is not configured"))
		return
	}
	query := r.URL.Query()
	before, limit, err := parseReadPage(query)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	filter := agentapp.WorkItemFilter{
		VersionID: versionID,
		Kind:      strings.TrimSpace(query.Get("kind")),
		State:     strings.TrimSpace(query.Get("state")),
		CreatedBy: strings.TrimSpace(query.Get("created_by")),
		BeforeID:  before,
		Limit:     limit,
	}
	if err := agentapp.ValidateWorkItemFilter(filter); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if !h.requireVersion(w, r, versionID) {
		return
	}
	views, hasMore, err := h.workItems.WorkItems(r.Context(), filter)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]workItemReadDTO, 0, len(views))
	for _, view := range views {
		items = append(items, toWorkItemReadDTO(view))
	}
	httpx.List(w, http.StatusOK, items, nextReadCursor(hasMore, lastReadID(views, workItemReadViewID)), hasMore)
}

// GetWorkItem GET /api/versions/{id}/work-items/{workItemId}. A work item of
// another version is a 404, never a cross-version read.
func (h *VersionReadHandler) GetWorkItem(w http.ResponseWriter, r *http.Request) {
	versionID, err := versionReadPathID(r, "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	workItemID, err := versionReadPathID(r, "workItemId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h == nil || h.workItems == nil {
		httpx.Error(w, r, platform.Unavailable("version work-item read model is not configured"))
		return
	}
	if !h.requireVersion(w, r, versionID) {
		return
	}
	view, err := h.workItems.WorkItem(r.Context(), versionID, workItemID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toWorkItemReadDTO(view))
}

// requireVersion resolves the addressed version through the version read model.
// A missing version answers 404 so no list can observe another context's rows;
// an unconfigured model fails closed with 503.
func (h *VersionReadHandler) requireVersion(w http.ResponseWriter, r *http.Request, versionID int64) bool {
	if h == nil || h.versions == nil {
		httpx.Error(w, r, platform.Unavailable("version read model is not configured"))
		return false
	}
	exists, err := h.versions.VersionExists(r.Context(), versionID)
	if err != nil {
		httpx.Error(w, r, err)
		return false
	}
	if !exists {
		httpx.Error(w, r, platform.NotFound("version"))
		return false
	}
	return true
}

// ---------- request parsing ----------

func versionReadPathID(r *http.Request, field string) (int64, error) {
	return validation.NonEmptyID(field, r.PathValue(field))
}

// parseIncludeSuperseded accepts the frozen boolean query parameter. Omitted
// means false (current facts only).
func parseIncludeSuperseded(raw string) (bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, platform.Invalid("include_superseded", "must be a boolean")
	}
	return value, nil
}

// parseReadCursor parses the frozen Cursor parameter: the decimal ID of the
// last entity of the previous page. Omitted means the first page.
func parseReadCursor(raw string) (*int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	value, err := pagination.Cursor(raw)
	if err != nil || value <= 0 {
		return nil, platform.Invalid("cursor", "must be a positive decimal ID")
	}
	return &value, nil
}

// parseReadPage parses the frozen Limit/Cursor pair shared by the three list
// routes, so the bounds and their typed 400 live in one place.
func parseReadPage(query url.Values) (*int64, int, error) {
	before, err := parseReadCursor(query.Get("cursor"))
	if err != nil {
		return nil, 0, err
	}
	return before, pagination.Limit(query.Get("limit"), pagination.DefaultLimit, pagination.MaxLimit), nil
}

// nextReadCursor renders the frozen next_cursor: the id of the previous page's
// last entity when has_more is set, empty otherwise.
func nextReadCursor(hasMore bool, lastID int64) string {
	if !hasMore || lastID == 0 {
		return ""
	}
	return formatDecimal(lastID)
}

// lastReadID returns the id of a page's last entity, 0 for an empty page.
func lastReadID[T any](views []T, id func(T) int64) int64 {
	if len(views) == 0 {
		return 0
	}
	return id(views[len(views)-1])
}

func factReadViewID(view versionapp.FactReadView) int64 { return view.ID }

func planItemReadViewID(view versionapp.PlanItemReadView) int64 { return view.ID }

func workItemReadViewID(view agentapp.WorkItemReadView) int64 { return view.ID }

// ---------- DTOs ----------

type factReadDTO struct {
	ID           string         `json:"id"`
	VersionID    string         `json:"version_id"`
	Kind         string         `json:"kind"`
	Key          string         `json:"key"`
	Payload      map[string]any `json:"payload"`
	Confidence   *float64       `json:"confidence"`
	Source       string         `json:"source"`
	SourceRef    string         `json:"source_ref"`
	EvidenceRefs []string       `json:"evidence_refs"`
	ContentHash  string         `json:"content_hash"`
	SupersededBy *string        `json:"superseded_by"`
	CreatedAt    string         `json:"created_at"`
}

type planItemReadDTO struct {
	ID         string   `json:"id"`
	VersionID  string   `json:"version_id"`
	Phase      string   `json:"phase"`
	Title      string   `json:"title"`
	Status     string   `json:"status"`
	Order      int      `json:"order"`
	Revision   int64    `json:"revision"`
	DependsOn  []string `json:"depends_on"`
	WorkItemID *string  `json:"work_item_id"`
	Decision   string   `json:"decision"`
	CreatedAt  string   `json:"created_at"`
	UpdatedAt  string   `json:"updated_at"`
}

type workItemLeaseDTO struct {
	Owner     string  `json:"owner"`
	ExpiresAt *string `json:"expires_at"`
}

type workItemReadDTO struct {
	ID             string            `json:"id"`
	VersionID      string            `json:"version_id"`
	Kind           string            `json:"kind"`
	Goal           string            `json:"goal"`
	State          string            `json:"state"`
	Target         json.RawMessage   `json:"target"`
	Inputs         json.RawMessage   `json:"inputs"`
	Expected       json.RawMessage   `json:"expected"`
	Budget         json.RawMessage   `json:"budget"`
	DependsOn      []string          `json:"depends_on"`
	Lease          *workItemLeaseDTO `json:"lease"`
	Attempt        int               `json:"attempt"`
	Result         json.RawMessage   `json:"result"`
	CreatedBy      string            `json:"created_by"`
	IdempotencyKey *string           `json:"idempotency_key"`
	CreatedAt      string            `json:"created_at"`
	UpdatedAt      string            `json:"updated_at"`
}

func toFactReadDTO(view versionapp.FactReadView) factReadDTO {
	payload := view.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	return factReadDTO{
		ID:           formatDecimal(view.ID),
		VersionID:    formatDecimal(view.VersionID),
		Kind:         view.Kind,
		Key:          view.Key,
		Payload:      payload,
		Confidence:   view.Confidence,
		Source:       view.Source,
		SourceRef:    view.SourceRef,
		EvidenceRefs: decimalIDList(view.EvidenceIDs),
		ContentHash:  view.ContentHash,
		SupersededBy: formatOptionalDecimal(view.SupersededBy),
		CreatedAt:    formatTime(view.CreatedAt),
	}
}

func toPlanItemReadDTO(view versionapp.PlanItemReadView) planItemReadDTO {
	return planItemReadDTO{
		ID:         formatDecimal(view.ID),
		VersionID:  formatDecimal(view.VersionID),
		Phase:      view.Phase,
		Title:      view.Title,
		Status:     view.Status,
		Order:      view.Order,
		Revision:   view.Revision,
		DependsOn:  decimalIDList(view.DependsOn),
		WorkItemID: formatOptionalDecimal(view.WorkItemID),
		Decision:   view.Decision,
		CreatedAt:  formatTime(view.CreatedAt),
		UpdatedAt:  formatTime(view.UpdatedAt),
	}
}

func toWorkItemReadDTO(view agentapp.WorkItemReadView) workItemReadDTO {
	dto := workItemReadDTO{
		ID:             formatDecimal(view.ID),
		VersionID:      formatDecimal(view.VersionID),
		Kind:           view.Kind,
		Goal:           view.Goal,
		State:          view.State,
		Target:         rawJSONObject(view.Target),
		Inputs:         rawJSONObject(view.Inputs),
		Expected:       rawJSONObject(view.Expected),
		Budget:         rawJSONObject(view.Budget),
		DependsOn:      optionalDecimalIDList(view.DependsOn),
		Attempt:        view.Attempt,
		Result:         rawJSONObject(view.Result),
		CreatedBy:      view.CreatedBy,
		IdempotencyKey: view.IdempotencyKey,
		CreatedAt:      formatTime(view.CreatedAt),
		UpdatedAt:      formatTime(view.UpdatedAt),
	}
	if view.Lease != nil {
		dto.Lease = &workItemLeaseDTO{
			Owner:     view.Lease.Owner,
			ExpiresAt: formatOptionalTime(view.Lease.ExpiresAt),
		}
	}
	return dto
}

// ---------- formatting helpers ----------

// decimalIDList renders required ID arrays as decimal strings. A nil slice
// becomes an empty array, matching the frozen required-array fields.
func decimalIDList(ids []int64) []string {
	values := make([]string, 0, len(ids))
	for _, id := range ids {
		values = append(values, formatDecimal(id))
	}
	return values
}

// optionalDecimalIDList preserves the frozen nullable depends_on array: nil is
// JSON null and an empty (but non-nil) list stays [].
func optionalDecimalIDList(ids []int64) []string {
	if ids == nil {
		return nil
	}
	return decimalIDList(ids)
}

func formatOptionalDecimal(value *int64) *string {
	if value == nil {
		return nil
	}
	formatted := formatDecimal(*value)
	return &formatted
}

// rawJSONObject keeps a stored JSON object intact and defaults an absent or
// non-object value to {} so the frozen object fields never render null.
func rawJSONObject(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return json.RawMessage(`{}`)
	}
	return raw
}
