package deliveryapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

/* --------------------------------- fakes ---------------------------------- */

type fakeGlobalIssueService struct {
	views   []deliveryapp.GlobalIssueView
	hasMore bool
	err     error
	calls   []deliveryapp.GlobalIssueFilter
}

func (f *fakeGlobalIssueService) ListGlobal(_ context.Context, filter deliveryapp.GlobalIssueFilter) ([]deliveryapp.GlobalIssueView, bool, error) {
	f.calls = append(f.calls, filter)
	if f.err != nil {
		return nil, false, f.err
	}
	return f.views, f.hasMore, nil
}

var _ GlobalIssueService = (*fakeGlobalIssueService)(nil)

type fakeGlobalReportService struct {
	views   []deliveryapp.GlobalReportView
	hasMore bool
	err     error
	calls   []deliveryapp.GlobalReportFilter
}

func (f *fakeGlobalReportService) ListGlobal(_ context.Context, filter deliveryapp.GlobalReportFilter) ([]deliveryapp.GlobalReportView, bool, error) {
	f.calls = append(f.calls, filter)
	if f.err != nil {
		return nil, false, f.err
	}
	return f.views, f.hasMore, nil
}

var _ GlobalReportService = (*fakeGlobalReportService)(nil)

func newGlobalMux(t *testing.T, issues GlobalIssueService, reports GlobalReportService) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	passthrough := func(next http.Handler) http.Handler { return next }
	RegisterRoutes(mux, passthrough, NewGlobal(issues, reports))
	return mux
}

func sampleGlobalIssueViews() []deliveryapp.GlobalIssueView {
	merged := sampleIssue(103, delivery.IssueStatusMerged)
	merged.VersionID = 42
	reopened := sampleIssue(102, delivery.IssueStatusConfirmed)
	reopened.VersionID = 43
	return []deliveryapp.GlobalIssueView{
		{
			Issue:        merged,
			VersionTitle: "登录改版",
			URL:          "/versions/42?tab=issues&issue=103",
		},
		{
			Issue:        reopened,
			VersionTitle: "结算改版",
			URL:          "/versions/43?tab=issues&issue=102",
		},
	}
}

func sampleGlobalReportViews() []deliveryapp.GlobalReportView {
	published := sampleReport(701, delivery.ReportKindVersion, nil,
		delivery.ReportStatusPublished, int64Ptr(1701))
	revision := sampleRevision(1701, 701, 3, delivery.ReportStatusPublished, "done", int64Ptr(500))
	stale := sampleReport(702, delivery.ReportKindWave, int64Ptr(9),
		delivery.ReportStatusStale, nil)
	return []deliveryapp.GlobalReportView{
		{
			Report:          published,
			CurrentRevision: &revision,
			VersionTitle:    "登录改版",
			URL:             "/versions/42?tab=reports&report=701",
		},
		{
			Report:       stale,
			VersionTitle: "结算改版",
			URL:          "/versions/42?tab=reports&report=702",
		},
	}
}

/* ---------------------------------- tests --------------------------------- */

func TestGlobalIssueRouteForwardsFiltersAndProjectsViews(t *testing.T) {
	service := &fakeGlobalIssueService{views: sampleGlobalIssueViews(), hasMore: true}
	mux := newGlobalMux(t, service, nil)

	recorder := deliveryCall(t, mux, http.MethodGet,
		"/api/issues?version_id=42&status=merged&severity=minor&category=ui&q=%E7%BB%93%E7%AE%97&before=900&limit=10",
		"", nil)
	requireStatus(t, recorder, http.StatusOK)

	if len(service.calls) != 1 {
		t.Fatalf("ListGlobal calls = %d, want 1", len(service.calls))
	}
	filter := service.calls[0]
	if filter.VersionID == nil || *filter.VersionID != 42 || filter.Status != "merged" ||
		filter.Severity != "minor" || filter.Category != "ui" || filter.Q != "结算" ||
		filter.Before != 900 || filter.Limit != 10 {
		t.Fatalf("filter = %#v", filter)
	}

	items, cursor, hasMore := decodeDeliveryList(t, recorder)
	if !hasMore || cursor != "102" {
		t.Fatalf("cursor = %q has_more = %v, want 102/true", cursor, hasMore)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	first := items[0].(map[string]any)
	if first["version_id"] != "42" || first["version_title"] != "登录改版" ||
		first["url"] != "/versions/42?tab=issues&issue=103" ||
		first["merged_into_id"] != "2002" || first["reopen_count"] != float64(1) {
		t.Fatalf("first issue = %#v", first)
	}
	second := items[1].(map[string]any)
	if second["version_title"] != "结算改版" ||
		second["url"] != "/versions/43?tab=issues&issue=102" {
		t.Fatalf("second issue = %#v", second)
	}
	if _, present := first["refs"]; !present {
		t.Fatalf("issue row has no refs array: %#v", first)
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{"html_ref", "content", "storage", "dedupe_key", "fingerprint"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("issue response leaks %q: %s", forbidden, body)
		}
	}
}

func TestGlobalIssueRouteDefaultsPage(t *testing.T) {
	service := &fakeGlobalIssueService{}
	mux := newGlobalMux(t, service, nil)

	recorder := deliveryCall(t, mux, http.MethodGet, "/api/issues", "", nil)
	requireStatus(t, recorder, http.StatusOK)

	if len(service.calls) != 1 {
		t.Fatalf("ListGlobal calls = %d, want 1", len(service.calls))
	}
	filter := service.calls[0]
	if filter.VersionID != nil || filter.Status != "" || filter.Q != "" ||
		filter.Limit != 20 || filter.Before != 0 {
		t.Fatalf("default filter = %#v", filter)
	}
	items, cursor, hasMore := decodeDeliveryList(t, recorder)
	if len(items) != 0 || cursor != "" || hasMore {
		t.Fatalf("empty response = %#v/%q/%v", items, cursor, hasMore)
	}
}

func TestGlobalIssueRouteRejectsInvalidFilters(t *testing.T) {
	cases := []string{
		"/api/issues?status=bogus",
		"/api/issues?severity=critical",
		"/api/issues?category=security",
		"/api/issues?version_id=zero",
		"/api/issues?before=nope",
		"/api/issues?q=" + url.QueryEscape(strings.Repeat("长", 101)),
	}
	for _, target := range cases {
		t.Run(target, func(t *testing.T) {
			service := &fakeGlobalIssueService{}
			mux := newGlobalMux(t, service, nil)
			recorder := deliveryCall(t, mux, http.MethodGet, target, "", nil)
			requireStatus(t, recorder, http.StatusBadRequest)
			apiError := decodeDeliveryError(t, recorder)
			if apiError["code"] != "invalid_argument" {
				t.Fatalf("error code = %v, want invalid_argument", apiError["code"])
			}
			if len(service.calls) != 0 {
				t.Fatalf("service calls = %d, want 0", len(service.calls))
			}
		})
	}
}

func TestGlobalIssueRouteMapsErrors(t *testing.T) {
	t.Run("unavailable", func(t *testing.T) {
		service := &fakeGlobalIssueService{err: platform.Unavailable("no wiring")}
		mux := newGlobalMux(t, service, nil)
		recorder := deliveryCall(t, mux, http.MethodGet, "/api/issues", "", nil)
		requireStatus(t, recorder, http.StatusServiceUnavailable)
	})
	t.Run("internal", func(t *testing.T) {
		service := &fakeGlobalIssueService{err: errors.New("boom")}
		mux := newGlobalMux(t, service, nil)
		recorder := deliveryCall(t, mux, http.MethodGet, "/api/issues", "", nil)
		requireStatus(t, recorder, http.StatusInternalServerError)
	})
	t.Run("not configured", func(t *testing.T) {
		mux := newGlobalMux(t, nil, nil)
		recorder := deliveryCall(t, mux, http.MethodGet, "/api/issues", "", nil)
		requireStatus(t, recorder, http.StatusInternalServerError)
	})
}

func TestGlobalReportRouteForwardsFiltersAndProjectsCurrentRevision(t *testing.T) {
	service := &fakeGlobalReportService{views: sampleGlobalReportViews(), hasMore: true}
	mux := newGlobalMux(t, nil, service)

	recorder := deliveryCall(t, mux, http.MethodGet,
		"/api/reports?version_id=42&status=published&kind=wave&before=800&limit=5", "", nil)
	requireStatus(t, recorder, http.StatusOK)

	if len(service.calls) != 1 {
		t.Fatalf("ListGlobal calls = %d, want 1", len(service.calls))
	}
	filter := service.calls[0]
	if filter.VersionID == nil || *filter.VersionID != 42 || filter.Status != "published" ||
		filter.Kind != "wave" || filter.Before != 800 || filter.Limit != 5 {
		t.Fatalf("filter = %#v", filter)
	}

	items, cursor, hasMore := decodeDeliveryList(t, recorder)
	if !hasMore || cursor != "702" {
		t.Fatalf("cursor = %q has_more = %v, want 702/true", cursor, hasMore)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	first := items[0].(map[string]any)
	if first["version_title"] != "登录改版" ||
		first["url"] != "/versions/42?tab=reports&report=701" {
		t.Fatalf("first report = %#v", first)
	}
	current, ok := first["current_revision"].(map[string]any)
	if !ok {
		t.Fatalf("current_revision = %#v", first["current_revision"])
	}
	if current["revision"] != float64(3) || current["html_available"] != true {
		t.Fatalf("current revision = %#v", current)
	}
	second := items[1].(map[string]any)
	if value, present := second["current_revision"]; !present || value != nil {
		t.Fatalf("second current_revision = %v (present=%v), want null", value, present)
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{"html_ref", "pdf_ref", "content", "storage_path"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("report response leaks %q: %s", forbidden, body)
		}
	}
}

func TestGlobalReportRouteRejectsInvalidFilters(t *testing.T) {
	cases := []string{
		"/api/reports?status=deleted",
		"/api/reports?kind=snapshot",
		"/api/reports?version_id=zero",
		"/api/reports?before=nope",
	}
	for _, target := range cases {
		t.Run(target, func(t *testing.T) {
			service := &fakeGlobalReportService{}
			mux := newGlobalMux(t, nil, service)
			recorder := deliveryCall(t, mux, http.MethodGet, target, "", nil)
			requireStatus(t, recorder, http.StatusBadRequest)
			apiError := decodeDeliveryError(t, recorder)
			if apiError["code"] != "invalid_argument" {
				t.Fatalf("error code = %v, want invalid_argument", apiError["code"])
			}
			if len(service.calls) != 0 {
				t.Fatalf("service calls = %d, want 0", len(service.calls))
			}
		})
	}
}

func TestGlobalReportRouteMapsErrors(t *testing.T) {
	service := &fakeGlobalReportService{err: platform.Unavailable("no wiring")}
	mux := newGlobalMux(t, nil, service)
	recorder := deliveryCall(t, mux, http.MethodGet, "/api/reports", "", nil)
	requireStatus(t, recorder, http.StatusServiceUnavailable)
}

func TestGlobalRoutesAreReadOnly(t *testing.T) {
	mux := newGlobalMux(t, &fakeGlobalIssueService{}, &fakeGlobalReportService{})
	for _, target := range []string{"/api/issues", "/api/reports"} {
		recorder := deliveryCall(t, mux, http.MethodPost, target, "{}", nil)
		requireStatus(t, recorder, http.StatusMethodNotAllowed)
	}
}

func TestGlobalRoutesUseTheAuthenticatedMiddleware(t *testing.T) {
	mux := http.NewServeMux()
	deny := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
	}
	RegisterGlobal(mux, deny, NewGlobal(&fakeGlobalIssueService{}, &fakeGlobalReportService{}))
	for _, target := range []string{"/api/issues", "/api/reports"} {
		recorder := deliveryCall(t, mux, http.MethodGet, target, "", nil)
		requireStatus(t, recorder, http.StatusUnauthorized)
	}

	// A missing middleware must not mount an unauthenticated route.
	bare := http.NewServeMux()
	RegisterGlobal(bare, nil, NewGlobal(&fakeGlobalIssueService{}, &fakeGlobalReportService{}))
	recorder := deliveryCall(t, bare, http.MethodGet, "/api/issues", "", nil)
	requireStatus(t, recorder, http.StatusNotFound)
}
