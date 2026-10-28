package httpapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// serviceGlobalIssueStore is the delivery-owned store fake behind the real
// GlobalReaderService.
type serviceGlobalIssueStore struct {
	rows    []deliveryapp.GlobalIssueRow
	next    string
	hasMore bool
	err     error

	gotFilter *deliveryapp.GlobalIssueFilter
}

func (f *serviceGlobalIssueStore) ListGlobalIssues(_ context.Context, filter deliveryapp.GlobalIssueFilter) ([]deliveryapp.GlobalIssueRow, string, bool, error) {
	f.gotFilter = &filter
	if f.err != nil {
		return nil, "", false, f.err
	}
	return f.rows, f.next, f.hasMore, nil
}

// serviceGlobalReportStore is the report store fake behind the real service.
type serviceGlobalReportStore struct {
	rows    []deliveryapp.GlobalReportRow
	next    string
	hasMore bool
	err     error

	gotFilter *deliveryapp.GlobalReportFilter
}

func (f *serviceGlobalReportStore) ListGlobalReports(_ context.Context, filter deliveryapp.GlobalReportFilter) ([]deliveryapp.GlobalReportRow, string, bool, error) {
	f.gotFilter = &filter
	if f.err != nil {
		return nil, "", false, f.err
	}
	return f.rows, f.next, f.hasMore, nil
}

// serviceGlobalVersionNames is the version-name port fake behind the real
// service.
type serviceGlobalVersionNames struct {
	names  map[int64]string
	err    error
	calls  int
	gotIDs []int64
}

func (f *serviceGlobalVersionNames) VersionNames(_ context.Context, versionIDs []int64) (map[int64]string, error) {
	f.calls++
	f.gotIDs = append([]int64(nil), versionIDs...)
	if f.err != nil {
		return nil, f.err
	}
	return f.names, nil
}

// TestGlobalIssueCentreWithRealReaderService mounts the real application-level
// global reader behind the HTTP handler and proves filters, cursor, versionName,
// refs, recurrenceCount, deepLink and the page envelope survive the whole chain.
func TestGlobalIssueCentreWithRealReaderService(t *testing.T) {
	issue := sampleIssue()
	issues := &serviceGlobalIssueStore{
		rows: []deliveryapp.GlobalIssueRow{{
			Issue: issue, Refs: sampleIssueRefs(issue.ID), VersionName: "",
		}},
		next:    encodeCursor(issue.ID),
		hasMore: true,
	}
	versions := &serviceGlobalVersionNames{names: map[int64]string{testVersionID: "checkout v1"}}
	service := deliveryapp.NewGlobalReaderService(deliveryapp.GlobalReaderDeps{
		Issues: issues, Versions: versions,
	})
	h := newTestServer(t, HandlerDeps{GlobalIssues: service})

	cursor := encodeCursor(9000)
	rec := doRequest(h, http.MethodGet,
		"/api/issues?versionId=101&status=open&severity=major&category=defect&q=checkout&limit=5&cursor="+cursor,
		"ezt_user", "")
	requireStatus(t, rec, http.StatusOK)

	if issues.gotFilter == nil {
		t.Fatal("the real service did not reach the store")
	}
	filter := *issues.gotFilter
	if filter.VersionID == nil || *filter.VersionID != testVersionID ||
		filter.Status != domain.StatusOpen || filter.Severity != domain.SeverityMajor ||
		filter.Category != domain.CategoryDefect || filter.Query != "checkout" ||
		filter.Cursor != cursor || filter.Limit != 5 {
		t.Fatalf("normalized filter=%+v", filter)
	}
	if versions.calls != 1 || len(versions.gotIDs) != 1 || versions.gotIDs[0] != testVersionID {
		t.Fatalf("version lookups=%v calls=%d", versions.gotIDs, versions.calls)
	}

	items, next, hasMore := decodePage(t, rec)
	if len(items) != 1 || next != encodeCursor(issue.ID) || !hasMore {
		t.Fatalf("items=%d next=%q hasMore=%v", len(items), next, hasMore)
	}
	item := items[0]
	if requireStringField(t, item, "id") != "9001" || requireStringField(t, item, "versionId") != "101" {
		t.Fatalf("ids=%v/%v", item["id"], item["versionId"])
	}
	if item["versionName"] != "checkout v1" {
		t.Fatalf("versionName=%v, want the version-name port value", item["versionName"])
	}
	if item["recurrenceCount"] != float64(2) || item["severity"] != domain.SeverityMajor {
		t.Fatalf("summary fields=%v", item)
	}
	refs, ok := item["refs"].([]any)
	if !ok || len(refs) != 2 {
		t.Fatalf("refs=%v", item["refs"])
	}
	codeRefs, ok := item["codeRefs"].([]any)
	if !ok || len(codeRefs) != 0 {
		t.Fatalf("nil optional codeRefs must render [] not 500: %v", item["codeRefs"])
	}
	deepLink, ok := item["deepLink"].(map[string]any)
	if !ok || deepLink["tab"] != "issues" {
		t.Fatalf("deepLink=%v", item["deepLink"])
	}
	if requireStringField(t, deepLink, "versionId") != "101" ||
		requireStringField(t, deepLink, "issueId") != "9001" {
		t.Fatalf("deepLink=%v", deepLink)
	}
}

// TestGlobalReportCentreWithRealReaderService mounts the real report reader and
// proves status/versionId filters, versionName, batchNo, the deep link and the
// summary-only shape (no content) survive the whole chain.
func TestGlobalReportCentreWithRealReaderService(t *testing.T) {
	report := sampleReport()
	reports := &serviceGlobalReportStore{
		rows: []deliveryapp.GlobalReportRow{{
			Report: report, VersionName: "", BatchNo: 2,
		}},
	}
	versions := &serviceGlobalVersionNames{names: map[int64]string{testVersionID: "checkout v1"}}
	service := deliveryapp.NewGlobalReaderService(deliveryapp.GlobalReaderDeps{
		Reports: reports, Versions: versions,
	})
	h := newTestServer(t, HandlerDeps{GlobalReports: service})

	rec := doRequest(h, http.MethodGet,
		"/api/reports?versionId=101&status=published&limit=3", "ezt_user", "")
	requireStatus(t, rec, http.StatusOK)

	if reports.gotFilter == nil || reports.gotFilter.VersionID == nil ||
		*reports.gotFilter.VersionID != testVersionID ||
		reports.gotFilter.Status != domain.ReportStatusPublished || reports.gotFilter.Limit != 3 {
		t.Fatalf("normalized filter=%+v", reports.gotFilter)
	}
	if versions.calls != 1 || len(versions.gotIDs) != 1 || versions.gotIDs[0] != testVersionID {
		t.Fatalf("version lookups=%v calls=%d", versions.gotIDs, versions.calls)
	}

	items, next, hasMore := decodePage(t, rec)
	if len(items) != 1 || next != "" || hasMore {
		t.Fatalf("items=%d next=%q hasMore=%v", len(items), next, hasMore)
	}
	item := items[0]
	if requireStringField(t, item, "id") != "9101" || item["versionName"] != "checkout v1" ||
		item["batchNo"] != float64(2) {
		t.Fatalf("global report row=%v", item)
	}
	if _, exposed := item["content"]; exposed {
		t.Fatalf("global report list must stay a summary (no content): %v", item)
	}
	deepLink, ok := item["deepLink"].(map[string]any)
	if !ok || deepLink["tab"] != "report" {
		t.Fatalf("deepLink=%v", item["deepLink"])
	}
	if requireStringField(t, deepLink, "versionId") != "101" ||
		requireStringField(t, deepLink, "reportId") != "9101" {
		t.Fatalf("deepLink=%v", deepLink)
	}
}

// TestGlobalCentresWithRealReaderServiceEmpty proves empty stores render an
// empty page without touching the version-name port.
func TestGlobalCentresWithRealReaderServiceEmpty(t *testing.T) {
	versions := &serviceGlobalVersionNames{err: errors.New("version names must not be read for an empty page")}
	service := deliveryapp.NewGlobalReaderService(deliveryapp.GlobalReaderDeps{
		Issues:   &serviceGlobalIssueStore{rows: []deliveryapp.GlobalIssueRow{}},
		Reports:  &serviceGlobalReportStore{rows: []deliveryapp.GlobalReportRow{}},
		Versions: versions,
	})
	h := newTestServer(t, HandlerDeps{GlobalIssues: service, GlobalReports: service})

	for _, path := range []string{"/api/issues", "/api/reports"} {
		rec := doRequest(h, http.MethodGet, path, "ezt_user", "")
		requireStatus(t, rec, http.StatusOK)
		items, next, hasMore := decodePage(t, rec)
		if len(items) != 0 || next != "" || hasMore {
			t.Fatalf("%s items=%d next=%q hasMore=%v", path, len(items), next, hasMore)
		}
	}
	if versions.calls != 0 {
		t.Fatalf("version-name port called %d times for empty pages", versions.calls)
	}
}

// TestGlobalCentresWithRealReaderServiceError proves a store failure is a 500
// (never a silently empty page).
func TestGlobalCentresWithRealReaderServiceError(t *testing.T) {
	storeErr := errors.New("store down")
	service := deliveryapp.NewGlobalReaderService(deliveryapp.GlobalReaderDeps{
		Issues:   &serviceGlobalIssueStore{err: storeErr},
		Reports:  &serviceGlobalReportStore{err: storeErr},
		Versions: &serviceGlobalVersionNames{},
	})
	h := newTestServer(t, HandlerDeps{GlobalIssues: service, GlobalReports: service})

	for _, path := range []string{"/api/issues", "/api/reports"} {
		rec := doRequest(h, http.MethodGet, path, "ezt_user", "")
		requireStatus(t, rec, http.StatusInternalServerError)
	}
}

// TestGlobalCentresNilOptionalDeps proves the global centres stay up with only
// their required reader: no code-ref reader is a 200 with codeRefs [], and a
// group whose reader is not wired is simply not mounted.
func TestGlobalCentresNilOptionalDeps(t *testing.T) {
	issue := sampleIssue()
	service := deliveryapp.NewGlobalReaderService(deliveryapp.GlobalReaderDeps{
		Issues: &serviceGlobalIssueStore{rows: []deliveryapp.GlobalIssueRow{{
			Issue: issue, Refs: sampleIssueRefs(issue.ID),
		}}},
		Versions: &serviceGlobalVersionNames{names: map[int64]string{testVersionID: "v1"}},
	})
	h := newTestServer(t, HandlerDeps{GlobalIssues: service})

	rec := doRequest(h, http.MethodGet, "/api/issues", "ezt_user", "")
	requireStatus(t, rec, http.StatusOK)
	items, _, _ := decodePage(t, rec)
	if len(items) != 1 {
		t.Fatalf("items=%d", len(items))
	}
	codeRefs, ok := items[0]["codeRefs"].([]any)
	if !ok || len(codeRefs) != 0 {
		t.Fatalf("codeRefs=%v, want [] with a nil optional reader", items[0]["codeRefs"])
	}
	requireStatus(t, doRequest(h, http.MethodGet, "/api/reports", "ezt_user", ""), http.StatusNotFound)
}
