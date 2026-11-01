package deliveryapi

import (
	"context"
	"net/http"
	"strings"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// GlobalIssueService is the cross-version issue read surface the P6 global
// handler consumes. It is implemented by *deliveryapp.IssueService.
type GlobalIssueService interface {
	ListGlobal(ctx context.Context, filter deliveryapp.GlobalIssueFilter) ([]deliveryapp.GlobalIssueView, bool, error)
}

// GlobalReportService is the cross-version report read surface the P6 global
// handler consumes. It is implemented by *deliveryapp.ReportService.
type GlobalReportService interface {
	ListGlobal(ctx context.Context, filter deliveryapp.GlobalReportFilter) ([]deliveryapp.GlobalReportView, bool, error)
}

var _ GlobalIssueService = (*deliveryapp.IssueService)(nil)
var _ GlobalReportService = (*deliveryapp.ReportService)(nil)

// GlobalHandler serves the P6 cross-version read-only issue/report endpoints
// (spec §4.3.5). Both paths are login-only and carry no write operation.
type GlobalHandler struct {
	issues  GlobalIssueService
	reports GlobalReportService
}

// NewGlobal builds the P6 global handler. Either surface may be nil; its route
// then answers a safe 500 instead of panicking.
func NewGlobal(issues GlobalIssueService, reports GlobalReportService) *GlobalHandler {
	return &GlobalHandler{issues: issues, reports: reports}
}

/* ---------------------------------- views --------------------------------- */

// GlobalIssueView is IssueView plus the version title and the issues-workspace
// deep link (contract GlobalIssueView).
type GlobalIssueView struct {
	IssueView
	VersionTitle string `json:"version_title"`
	URL          string `json:"url"`
}

// GlobalReportView is ReportView plus the version title and the
// reports-workspace deep link (contract GlobalReportView). It never carries
// report content or internal storage refs.
type GlobalReportView struct {
	ReportView
	VersionTitle string `json:"version_title"`
	URL          string `json:"url"`
}

/* ---------------------------------- list ---------------------------------- */

// ListGlobalIssues GET /api/issues. The endpoint is login-only and read-only:
// status/severity/category/q/version_id filters plus the shared before/limit
// page, ordered by ID DESC across every version.
func (h *GlobalHandler) ListGlobalIssues(w http.ResponseWriter, r *http.Request) {
	if h.issues == nil {
		writeNotConfigured(w, r, "global issue service")
		return
	}
	query := r.URL.Query()
	versionID, err := optionalQueryID(r, "version_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	status, err := optionalEnum("status", query.Get("status"),
		"open", "confirmed", "dismissed", "merged")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	severity, err := optionalEnum("severity", query.Get("severity"),
		"major", "minor", "trivial")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	category, err := optionalEnum("category", query.Get("category"), "ui", "api", "db")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	q, err := globalTextQuery(query.Get("q"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	limit, before, err := issuePageParams(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	views, hasMore, err := h.issues.ListGlobal(r.Context(), deliveryapp.GlobalIssueFilter{
		VersionID: versionID,
		Status:    status,
		Severity:  severity,
		Category:  category,
		Q:         q,
		Before:    before,
		Limit:     limit,
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	items := make([]GlobalIssueView, 0, len(views))
	for _, view := range views {
		items = append(items, GlobalIssueView{
			IssueView:    toIssueView(view.Issue, nil, nil),
			VersionTitle: view.VersionTitle,
			URL:          view.URL,
		})
	}
	nextCursor := ""
	if hasMore && len(views) > 0 {
		nextCursor = platformid.Format(views[len(views)-1].Issue.ID)
	}
	httpx.List(w, http.StatusOK, items, nextCursor, hasMore)
}

// ListGlobalReports GET /api/reports. The endpoint is login-only and read-only:
// status/kind/version_id filters plus the shared before/limit page, ordered by
// ID DESC across every version.
func (h *GlobalHandler) ListGlobalReports(w http.ResponseWriter, r *http.Request) {
	if h.reports == nil {
		writeNotConfigured(w, r, "global report service")
		return
	}
	query := r.URL.Query()
	versionID, err := optionalQueryID(r, "version_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	status, err := optionalEnum("status", query.Get("status"),
		"draft", "published", "stale")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	kind, err := optionalEnum("kind", query.Get("kind"), "wave", "version")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	limit, before, err := issuePageParams(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	views, hasMore, err := h.reports.ListGlobal(r.Context(), deliveryapp.GlobalReportFilter{
		VersionID: versionID,
		Status:    status,
		Kind:      kind,
		Before:    before,
		Limit:     limit,
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	items := make([]GlobalReportView, 0, len(views))
	for _, view := range views {
		items = append(items, GlobalReportView{
			ReportView:   toReportView(view.Report, view.CurrentRevision),
			VersionTitle: view.VersionTitle,
			URL:          view.URL,
		})
	}
	nextCursor := ""
	if hasMore && len(views) > 0 {
		nextCursor = platformid.Format(views[len(views)-1].Report.ID)
	}
	httpx.List(w, http.StatusOK, items, nextCursor, hasMore)
}

// globalTextQuery trims and bounds the optional title query (contract 1–100
// runes; an absent value stays absent).
func globalTextQuery(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if err := validation.String("q", trimmed, 1, 100); err != nil {
		return "", err
	}
	return trimmed, nil
}
