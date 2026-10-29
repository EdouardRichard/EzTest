package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// TestGlobalRoutesRequireAuthentication proves the global centres are behind the
// global bearer auth.
func TestGlobalRoutesRequireAuthentication(t *testing.T) {
	h := newTestServer(t, HandlerDeps{
		GlobalIssues:  &fakeGlobalIssues{},
		GlobalReports: &fakeGlobalReports{},
	})
	for _, path := range []string{"/api/issues", "/api/reports"} {
		rec := doRequest(h, http.MethodGet, path, "", "")
		requireStatus(t, rec, http.StatusUnauthorized)
	}
}

// TestGlobalIssueList proves /api/issues returns the version summary and the
// deep-link fields with decimal-string ids (spec §7.10).
func TestGlobalIssueList(t *testing.T) {
	issue := sampleIssue()
	global := &fakeGlobalIssues{
		rows:    []GlobalIssueRow{{Issue: issue, VersionName: "checkout v1"}},
		next:    "global-next",
		hasMore: true,
	}
	codeRefs := &fakeIssueCodeRefs{refs: map[int64][]IssueCodeRef{
		issue.ID: {{AnchorID: 8001, Path: "src/x.ts", Line: 42, Kind: "file_line", Label: "checkout"}},
	}}
	h := newTestServer(t, HandlerDeps{GlobalIssues: global, IssueCodeRefs: codeRefs})

	cursor := encodeCursor(9009)
	rec := doRequest(h, http.MethodGet,
		"/api/issues?versionId=101&status=open&severity=major&category=defect"+
			"&batchId=7001&caseRunId=7101&caseNodeId=7102&q=login&cursor="+cursor+"&limit=5",
		"ezt_user", "")
	requireStatus(t, rec, http.StatusOK)

	if global.gotFilter == nil {
		t.Fatal("global issue filter was not forwarded")
	}
	filter := *global.gotFilter
	if filter.VersionID == nil || *filter.VersionID != testVersionID ||
		filter.Status != domain.StatusOpen || filter.Severity != domain.SeverityMajor ||
		filter.Category != domain.CategoryDefect || filter.Query != "login" ||
		filter.Limit != 5 || filter.Cursor != cursor {
		t.Fatalf("filter=%+v", filter)
	}
	if filter.BatchID == nil || *filter.BatchID != 7001 ||
		filter.CaseRunID == nil || *filter.CaseRunID != 7101 ||
		filter.CaseNodeID == nil || *filter.CaseNodeID != 7102 {
		t.Fatalf("filter ids=%+v", filter)
	}

	items, next, hasMore := decodePage(t, rec)
	if len(items) != 1 || next != "global-next" || !hasMore {
		t.Fatalf("items=%d next=%q hasMore=%v", len(items), next, hasMore)
	}
	item := items[0]
	if got := requireStringField(t, item, "id"); got != "9001" {
		t.Fatalf("id=%q", got)
	}
	requireStringField(t, item, "versionId")
	if item["versionName"] != "checkout v1" {
		t.Fatalf("versionName=%v", item["versionName"])
	}
	deepLink, ok := item["deepLink"].(map[string]any)
	if !ok {
		t.Fatalf("deepLink=%v", item["deepLink"])
	}
	if deepLink["tab"] != "issues" {
		t.Fatalf("deepLink.tab=%v", deepLink["tab"])
	}
	if requireStringField(t, deepLink, "versionId") != "101" {
		t.Fatalf("deepLink.versionId=%v", deepLink["versionId"])
	}
	if requireStringField(t, deepLink, "issueId") != "9001" {
		t.Fatalf("deepLink.issueId=%v", deepLink["issueId"])
	}
	if codeRefs.calls != 1 || codeRefs.gotVersion != testVersionID || codeRefs.gotIssue != issue.ID {
		t.Fatalf("code refs reader calls=%d version=%d issue=%d",
			codeRefs.calls, codeRefs.gotVersion, codeRefs.gotIssue)
	}
	codeRefItems, ok := item["codeRefs"].([]any)
	if !ok || len(codeRefItems) != 1 {
		t.Fatalf("codeRefs=%v want one entry", item["codeRefs"])
	}
	codeRef, _ := codeRefItems[0].(map[string]any)
	if requireStringField(t, codeRef, "anchorId") != "8001" || codeRef["path"] != "src/x.ts" {
		t.Fatalf("codeRef=%v", codeRef)
	}
}

// TestGlobalIssueListRejectsBadQuery proves limit/cursor/ids plus the enum and
// query filters are validated at the HTTP boundary with a stable details.field
// instead of silently returning an empty page.
func TestGlobalIssueListRejectsBadQuery(t *testing.T) {
	global := &fakeGlobalIssues{}
	h := newTestServer(t, HandlerDeps{GlobalIssues: global})
	for name, test := range map[string]struct{ path, field string }{
		"limit zero":       {"/api/issues?limit=0", "limit"},
		"limit not a num":  {"/api/issues?limit=x", "limit"},
		"cursor malformed": {"/api/issues?cursor=not-a-cursor", "cursor"},
		"version id bad":   {"/api/issues?versionId=x", "versionId"},
		"batch id bad":     {"/api/issues?batchId=-1", "batchId"},
		"case run id bad":  {"/api/issues?caseRunId=abc", "caseRunId"},
		"case node id bad": {"/api/issues?caseNodeId=abc", "caseNodeId"},
		"status invalid":   {"/api/issues?status=closed", "status"},
		"severity invalid": {"/api/issues?severity=critical", "severity"},
		"category invalid": {"/api/issues?category=bug", "category"},
		"query too long":   {"/api/issues?q=" + strings.Repeat("x", domain.MaxIssueQueryRunes+1), "q"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := doRequest(h, http.MethodGet, test.path, "ezt_user", "")
			requireError(t, rec, http.StatusUnprocessableEntity, "invalid_argument", "invalid_argument", false)
			requireErrorField(t, rec, test.field)
		})
	}
	if global.gotFilter != nil {
		t.Fatalf("reader must not be called for an invalid filter: %+v", global.gotFilter)
	}
}

// TestGlobalReportList proves /api/reports returns the version name, batch
// number and deep-link fields (spec §7.10).
func TestGlobalReportList(t *testing.T) {
	report := sampleReport()
	global := &fakeGlobalReports{
		rows:    []GlobalReportRow{{Report: report, VersionName: "checkout v1", BatchNo: 2}},
		next:    "",
		hasMore: false,
	}
	h := newTestServer(t, HandlerDeps{GlobalReports: global})

	cursor := encodeCursor(9109)
	rec := doRequest(h, http.MethodGet,
		"/api/reports?versionId=101&status=published&cursor="+cursor+"&limit=3", "ezt_user", "")
	requireStatus(t, rec, http.StatusOK)

	if global.gotFilter == nil {
		t.Fatal("global report filter was not forwarded")
	}
	filter := *global.gotFilter
	if filter.VersionID == nil || *filter.VersionID != testVersionID ||
		filter.Status != domain.ReportStatusPublished || filter.Cursor != cursor || filter.Limit != 3 {
		t.Fatalf("filter=%+v", filter)
	}

	items, next, hasMore := decodePage(t, rec)
	if len(items) != 1 || next != "" || hasMore {
		t.Fatalf("items=%d next=%q hasMore=%v", len(items), next, hasMore)
	}
	item := items[0]
	if got := requireStringField(t, item, "id"); got != "9101" {
		t.Fatalf("id=%q", got)
	}
	if item["versionName"] != "checkout v1" || item["batchNo"] != float64(2) {
		t.Fatalf("versionName=%v batchNo=%v", item["versionName"], item["batchNo"])
	}
	deepLink, ok := item["deepLink"].(map[string]any)
	if !ok {
		t.Fatalf("deepLink=%v", item["deepLink"])
	}
	if deepLink["tab"] != "report" {
		t.Fatalf("deepLink.tab=%v", deepLink["tab"])
	}
	if requireStringField(t, deepLink, "versionId") != "101" {
		t.Fatalf("deepLink.versionId=%v", deepLink["versionId"])
	}
	if requireStringField(t, deepLink, "reportId") != "9101" {
		t.Fatalf("deepLink.reportId=%v", deepLink["reportId"])
	}
}

// TestGlobalReportListRejectsBadQuery proves filter validation at the HTTP
// boundary with a stable details.field.
func TestGlobalReportListRejectsBadQuery(t *testing.T) {
	h := newTestServer(t, HandlerDeps{GlobalReports: &fakeGlobalReports{}})
	for name, test := range map[string]struct{ path, field string }{
		"limit zero":       {"/api/reports?limit=0", "limit"},
		"limit not a num":  {"/api/reports?limit=x", "limit"},
		"cursor malformed": {"/api/reports?cursor=not-a-cursor", "cursor"},
		"version id bad":   {"/api/reports?versionId=x", "versionId"},
		"status invalid":   {"/api/reports?status=archived", "status"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := doRequest(h, http.MethodGet, test.path, "ezt_user", "")
			requireError(t, rec, http.StatusUnprocessableEntity, "invalid_argument", "invalid_argument", false)
			requireErrorField(t, rec, test.field)
		})
	}
}

// TestGlobalRoutesAllowRegularUsers proves the global lists are read-only and
// available to any authenticated user (spec §11.1).
func TestGlobalRoutesAllowRegularUsers(t *testing.T) {
	h := newTestServer(t, HandlerDeps{
		GlobalIssues: &fakeGlobalIssues{rows: []GlobalIssueRow{}},
		GlobalReports: &fakeGlobalReports{
			rows: []GlobalReportRow{},
		},
	})
	for _, path := range []string{"/api/issues", "/api/reports"} {
		rec := doRequest(h, http.MethodGet, path, "ezt_user", "")
		if rec.Code == http.StatusForbidden {
			t.Fatalf("%s returned 403 for a regular user: %s", path, rec.Body)
		}
		requireStatus(t, rec, http.StatusOK)
	}
}

// TestGlobalListsNilSafe proves the global centres stay unmounted when their
// application readers are not wired.
func TestGlobalListsNilSafe(t *testing.T) {
	h := newTestServer(t, HandlerDeps{})
	for _, path := range []string{"/api/issues", "/api/reports"} {
		rec := doRequest(h, http.MethodGet, path, "ezt_user", "")
		requireStatus(t, rec, http.StatusNotFound)
	}
}

// TestGlobalIssueListMapsServiceErrors proves an unexpected reader failure is a
// 500 without leaking the cause.
func TestGlobalIssueListMapsServiceErrors(t *testing.T) {
	h := newTestServer(t, HandlerDeps{GlobalIssues: &fakeGlobalIssues{err: context.Canceled}})
	rec := doRequest(h, http.MethodGet, "/api/issues", "ezt_user", "")
	requireStatus(t, rec, http.StatusInternalServerError)
}
