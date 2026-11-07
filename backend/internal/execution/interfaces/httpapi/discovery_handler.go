package httpapi

// W1-14A (spec R-EX-5, API-36/37/39): read-only execution-time discovery APIs.
//
//	GET /api/versions/{id}/discovery/observations?kind=&decision=&wave_id=&limit=
//	GET /api/versions/{id}/discovery/deltas?wave_id=
//	GET /api/versions/{id}/coverage
//
// Every operation is version-scoped and authenticated. IDs are rendered as
// decimal strings (snowflake ids exceed 2^53, so a JSON number would truncate);
// nullable provenance columns render null instead of a bogus id. The coverage
// endpoint is the W1 discovery-side matrix: per kind it aggregates the observed
// rows and the five-way decision, and lists the proposed templates that are
// still uncovered. Case coverage (T14) is deliberately W2.

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

const (
	// defaultDiscoveryLimit is the observation page size when ?limit= is absent.
	defaultDiscoveryLimit = 50
	// maxDiscoveryLimit caps ?limit=; out-of-range numeric values are clamped.
	maxDiscoveryLimit = 200
	// maxUncoveredTemplates bounds the coverage uncovered list.
	maxUncoveredTemplates = 200
)

// Discovery decision values (DB-29 CHECK).
const (
	discoveryDecisionNew        = "new"
	discoveryDecisionProposed   = "proposed"
	discoveryDecisionApproved   = "approved"
	discoveryDecisionRejected   = "rejected"
	discoveryDecisionOutOfScope = "out_of_scope"
)

func discoveryDecisions() []string {
	return []string{
		discoveryDecisionNew, discoveryDecisionProposed, discoveryDecisionApproved,
		discoveryDecisionRejected, discoveryDecisionOutOfScope,
	}
}

// DiscoveryObservationView is one DB-29 row of
// GET /api/versions/{id}/discovery/observations.
type DiscoveryObservationView struct {
	ObservationID    string  `json:"observation_id"`
	Kind             string  `json:"kind"`
	Method           string  `json:"method"`
	Host             string  `json:"host"`
	ResourceTemplate string  `json:"resource_template"`
	StatusCode       *int    `json:"status_code"`
	MIME             string  `json:"mime"`
	SourceKind       string  `json:"source_kind"`
	Occurrences      int     `json:"occurrences"`
	Decision         string  `json:"decision"`
	WaveID           *string `json:"wave_id"`
	CaseRunID        *string `json:"case_run_id"`
	VersionSUTID     string  `json:"version_sut_id"`
	EnvironmentID    *string `json:"environment_id"`
	ResourceID       *string `json:"resource_id"`
	FactID           *string `json:"fact_id"`
	FirstSeenAt      string  `json:"first_seen_at"`
	LastSeenAt       string  `json:"last_seen_at"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

// DiscoveryObservationListView is the observations envelope payload.
type DiscoveryObservationListView struct {
	Items []DiscoveryObservationView `json:"items"`
	Count int                        `json:"count"`
}

// DiscoveryDecisionCounts is the five-way delta classification count (API-37).
type DiscoveryDecisionCounts struct {
	New        int `json:"new"`
	Proposed   int `json:"proposed"`
	Approved   int `json:"approved"`
	Rejected   int `json:"rejected"`
	OutOfScope int `json:"out_of_scope"`
}

func (c *DiscoveryDecisionCounts) add(decision string) {
	switch decision {
	case discoveryDecisionNew:
		c.New++
	case discoveryDecisionProposed:
		c.Proposed++
	case discoveryDecisionApproved:
		c.Approved++
	case discoveryDecisionRejected:
		c.Rejected++
	case discoveryDecisionOutOfScope:
		c.OutOfScope++
	}
}

// DiscoveryDeltaItemView is the reduced per-resource projection of API-37.
type DiscoveryDeltaItemView struct {
	ObservationID    string  `json:"observation_id"`
	Kind             string  `json:"kind"`
	Method           string  `json:"method"`
	Host             string  `json:"host"`
	ResourceTemplate string  `json:"resource_template"`
	Decision         string  `json:"decision"`
	Occurrences      int     `json:"occurrences"`
	WaveID           *string `json:"wave_id"`
	CaseRunID        *string `json:"case_run_id"`
	ResourceID       *string `json:"resource_id"`
	FactID           *string `json:"fact_id"`
}

// DiscoveryDeltasView is the deltas envelope payload.
type DiscoveryDeltasView struct {
	Counts DiscoveryDecisionCounts  `json:"counts"`
	Items  []DiscoveryDeltaItemView `json:"items"`
}

// DiscoveryCoverageBucket is the per-kind discovery coverage matrix.
type DiscoveryCoverageBucket struct {
	Observed   int      `json:"observed"`
	Proposed   int      `json:"proposed"`
	Approved   int      `json:"approved"`
	Rejected   int      `json:"rejected"`
	OutOfScope int      `json:"out_of_scope"`
	Uncovered  []string `json:"uncovered"`
}

// DiscoveryCoverageView is the coverage envelope payload.
type DiscoveryCoverageView struct {
	Pages DiscoveryCoverageBucket `json:"pages"`
	APIs  DiscoveryCoverageBucket `json:"apis"`
}

// DiscoveryObservations GET /api/versions/{id}/discovery/observations.
func (h *Handler) DiscoveryObservations(w http.ResponseWriter, r *http.Request) {
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	filters, err := parseDiscoveryFilters(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.deps.Discovery == nil {
		httpx.Error(w, r, platform.Unavailable("discovery read model is not configured"))
		return
	}
	ctx := r.Context()
	if err := h.requireVersion(ctx, versionID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.requireWaveOfVersion(ctx, versionID, filters.waveID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	rows, err := h.deps.Discovery.ListByVersion(ctx, versionID, filters.waveID, filters.kind, filters.decision, filters.limit)
	if err != nil {
		httpx.Error(w, r, mapRepoError(err, "discovery observation"))
		return
	}
	items := make([]DiscoveryObservationView, 0, len(rows))
	for _, row := range rows {
		items = append(items, toDiscoveryObservationView(row))
	}
	httpx.JSON(w, http.StatusOK, DiscoveryObservationListView{Items: items, Count: len(items)})
}

// DiscoveryDeltas GET /api/versions/{id}/discovery/deltas?wave_id=.
func (h *Handler) DiscoveryDeltas(w http.ResponseWriter, r *http.Request) {
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	waveID, err := optionalCanonicalQueryID(r, "wave_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.deps.Discovery == nil {
		httpx.Error(w, r, platform.Unavailable("discovery read model is not configured"))
		return
	}
	ctx := r.Context()
	if err := h.requireVersion(ctx, versionID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.requireWaveOfVersion(ctx, versionID, waveID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	// The delta view is a whole-wave classification, never a paginated page.
	rows, err := h.deps.Discovery.ListByVersion(ctx, versionID, waveID, "", "", 0)
	if err != nil {
		httpx.Error(w, r, mapRepoError(err, "discovery delta"))
		return
	}
	counts := DiscoveryDecisionCounts{}
	items := make([]DiscoveryDeltaItemView, 0, len(rows))
	for _, row := range rows {
		counts.add(row.Decision)
		items = append(items, toDiscoveryDeltaItemView(row))
	}
	httpx.JSON(w, http.StatusOK, DiscoveryDeltasView{Counts: counts, Items: items})
}

// DiscoveryCoverage GET /api/versions/{id}/coverage.
func (h *Handler) DiscoveryCoverage(w http.ResponseWriter, r *http.Request) {
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.deps.Discovery == nil {
		httpx.Error(w, r, platform.Unavailable("discovery read model is not configured"))
		return
	}
	ctx := r.Context()
	if err := h.requireVersion(ctx, versionID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	rows, err := h.deps.Discovery.ListByVersion(ctx, versionID, nil, "", "", 0)
	if err != nil {
		httpx.Error(w, r, mapRepoError(err, "discovery coverage"))
		return
	}
	view := DiscoveryCoverageView{
		Pages: DiscoveryCoverageBucket{Uncovered: []string{}},
		APIs:  DiscoveryCoverageBucket{Uncovered: []string{}},
	}
	// observed counts distinct resource templates per kind (a repeated
	// observation row is the same discovery resource), never raw row counts.
	observed := map[string]map[string]struct{}{"page": {}, "api": {}}
	uncovered := map[string]map[string]struct{}{"page": {}, "api": {}}
	for _, row := range rows {
		// DB-29 constrains kind to page|api, but a read model must never panic
		// on unexpected data: skip an unknown kind instead of indexing a missing
		// bucket.
		var bucket *DiscoveryCoverageBucket
		switch row.Kind {
		case "page":
			bucket = &view.Pages
		case "api":
			bucket = &view.APIs
		default:
			continue
		}
		observed[row.Kind][strings.TrimSpace(row.ResourceTemplate)] = struct{}{}
		switch row.Decision {
		case discoveryDecisionProposed:
			bucket.Proposed++
			if identity := discoveryResourceIdentity(row); identity != "" {
				uncovered[row.Kind][identity] = struct{}{}
			}
		case discoveryDecisionApproved:
			bucket.Approved++
		case discoveryDecisionRejected:
			bucket.Rejected++
		case discoveryDecisionOutOfScope:
			bucket.OutOfScope++
		}
	}
	view.Pages.Observed = len(observed["page"])
	view.APIs.Observed = len(observed["api"])
	view.Pages.Uncovered = sortedUncovered(uncovered["page"])
	view.APIs.Uncovered = sortedUncovered(uncovered["api"])
	httpx.JSON(w, http.StatusOK, view)
}

// discoveryResourceIdentity renders the R-EX-2 resource identity of an
// uncovered observation: an API is "METHOD template" (GET and POST on the same
// path are different resources), while a page is its template alone.
func discoveryResourceIdentity(row executionapp.DiscoveryObservationRow) string {
	template := strings.TrimSpace(row.ResourceTemplate)
	if template == "" {
		return ""
	}
	if row.Kind != "api" {
		return template
	}
	method := strings.ToUpper(strings.TrimSpace(row.Method))
	if method == "" {
		method = "GET"
	}
	return method + " " + template
}

// discoveryFilters is the normalized query of the observations read.
type discoveryFilters struct {
	kind     string
	decision string
	waveID   *int64
	limit    int
}

// parseDiscoveryFilters validates the frozen query parameters: kind and decision
// are closed enums, wave_id is an optional canonical decimal ID, and limit is an
// optional decimal integer clamped to 1..200 (non-numeric is a 400).
func parseDiscoveryFilters(r *http.Request) (discoveryFilters, error) {
	query := r.URL.Query()
	kind, err := requireEnum("kind", query.Get("kind"), "page", "api")
	if err != nil {
		return discoveryFilters{}, err
	}
	decision, err := requireEnum("decision", query.Get("decision"), discoveryDecisions()...)
	if err != nil {
		return discoveryFilters{}, err
	}
	waveID, err := optionalCanonicalQueryID(r, "wave_id")
	if err != nil {
		return discoveryFilters{}, err
	}
	limit, err := parseDiscoveryLimit(query.Get("limit"))
	if err != nil {
		return discoveryFilters{}, err
	}
	return discoveryFilters{kind: kind, decision: decision, waveID: waveID, limit: limit}, nil
}

// parseDiscoveryLimit clamps an explicit numeric limit to 1..200; an absent
// value uses the default and a non-numeric value is a typed 400.
func parseDiscoveryLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultDiscoveryLimit, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, platform.Invalid("limit", "must be a decimal integer")
	}
	if value < 1 {
		value = 1
	}
	if value > maxDiscoveryLimit {
		value = maxDiscoveryLimit
	}
	return value, nil
}

// optionalCanonicalQueryID parses an optional query ID with the canonical
// decimal form (digits only, no sign, no leading zeros), matching
// requiredQueryID/API-19.
func optionalCanonicalQueryID(r *http.Request, field string) (*int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(field))
	if raw == "" {
		return nil, nil
	}
	if !canonicalDecimalID(raw) {
		return nil, platform.Invalid(field, "must be a positive decimal ID")
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return nil, platform.Invalid(field, "must be a positive decimal ID")
	}
	return &value, nil
}

// requireWaveOfVersion turns a provided wave filter into a 404 when the wave is
// unknown or belongs to another version, so the read never leaks cross-version
// existence.
func (h *Handler) requireWaveOfVersion(ctx context.Context, versionID int64, waveID *int64) error {
	if waveID == nil {
		return nil
	}
	if h.deps.Waves == nil {
		return platform.Unavailable("wave read model is not configured")
	}
	_, err := h.waveOfVersion(ctx, versionID, *waveID)
	return err
}

func toDiscoveryObservationView(row executionapp.DiscoveryObservationRow) DiscoveryObservationView {
	return DiscoveryObservationView{
		ObservationID:    platformid.Format(row.ID),
		Kind:             row.Kind,
		Method:           row.Method,
		Host:             row.Host,
		ResourceTemplate: row.ResourceTemplate,
		StatusCode:       row.StatusCode,
		MIME:             row.MIME,
		SourceKind:       row.SourceKind,
		Occurrences:      row.Occurrences,
		Decision:         row.Decision,
		WaveID:           formatOptionalRowID(row.WaveID),
		CaseRunID:        formatOptionalRowID(row.CaseRunID),
		VersionSUTID:     platformid.Format(row.VersionSUTID),
		EnvironmentID:    formatOptionalRowID(row.EnvironmentID),
		ResourceID:       formatOptionalRowID(row.ResourceID),
		FactID:           formatOptionalRowID(row.FactID),
		FirstSeenAt:      formatTime(row.FirstSeenAt),
		LastSeenAt:       formatTime(row.LastSeenAt),
		CreatedAt:        formatTime(row.CreatedAt),
		UpdatedAt:        formatTime(row.UpdatedAt),
	}
}

func toDiscoveryDeltaItemView(row executionapp.DiscoveryObservationRow) DiscoveryDeltaItemView {
	return DiscoveryDeltaItemView{
		ObservationID:    platformid.Format(row.ID),
		Kind:             row.Kind,
		Method:           row.Method,
		Host:             row.Host,
		ResourceTemplate: row.ResourceTemplate,
		Decision:         row.Decision,
		Occurrences:      row.Occurrences,
		WaveID:           formatOptionalRowID(row.WaveID),
		CaseRunID:        formatOptionalRowID(row.CaseRunID),
		ResourceID:       formatOptionalRowID(row.ResourceID),
		FactID:           formatOptionalRowID(row.FactID),
	}
}

// formatOptionalRowID renders a nullable DB-29 id as a decimal string; null and
// the zero value both mean "absent" and render null.
func formatOptionalRowID(value *int64) *string {
	if value == nil || *value <= 0 {
		return nil
	}
	formatted := platformid.Format(*value)
	return &formatted
}

// sortedUncovered renders the uncovered template set: deduplicated by the map,
// ascending and capped at maxUncoveredTemplates.
func sortedUncovered(values map[string]struct{}) []string {
	templates := make([]string, 0, len(values))
	for template := range values {
		templates = append(templates, template)
	}
	sort.Strings(templates)
	if len(templates) > maxUncoveredTemplates {
		templates = templates[:maxUncoveredTemplates]
	}
	return templates
}
