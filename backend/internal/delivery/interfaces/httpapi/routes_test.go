package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
)

// successDeps wires every delivery handler group with fakes that answer 200/201/
// 202 so the route table can be exercised end to end.
func successDeps() HandlerDeps {
	issue := sampleIssue()
	report := sampleReport()
	run := sampleAnalysisRun()
	return HandlerDeps{
		Issues: &fakeIssueService{
			createHuman: func(context.Context, int64, int64, deliveryapp.CreateIssueCommand) (domain.Issue, error) {
				return issue, nil
			},
			applyDrafts: func(context.Context, int64, int64, int64, []domain.IssueDraft) ([]domain.Issue, error) {
				return nil, nil
			},
			confirm: func(context.Context, int64, int64, int64, int, string) (domain.Issue, error) {
				return issue, nil
			},
			dismiss: func(context.Context, int64, int64, int64, int, string) (domain.Issue, error) {
				return issue, nil
			},
			get: func(context.Context, int64, int64) (domain.Issue, error) { return issue, nil },
			list: func(context.Context, int64, deliveryapp.IssueFilter) ([]domain.Issue, bool, error) {
				return []domain.Issue{issue}, false, nil
			},
		},
		IssueRefs: &fakeIssueRefs{refs: map[int64][]domain.IssueRef{issue.ID: sampleIssueRefs(issue.ID)}},
		Reports: &fakeReportService{
			ensure: func(context.Context, int64, int64, int64) (domain.Report, bool, error) {
				return report, true, nil
			},
			regenerate: func(context.Context, int64, int64, int64) (domain.Report, error) {
				next := report
				next.ID = 9102
				next.Revision = 2
				return next, nil
			},
			get: func(context.Context, int64, int64) (domain.Report, error) { return report, nil },
			listPage: func(context.Context, int64, deliveryapp.ReportFilter) ([]domain.Report, string, bool, error) {
				return []domain.Report{report}, "", false, nil
			},
			diff: func(context.Context, int64, int64, *int64) (domain.ReportDiff, error) {
				return domain.ReportDiff{BaseReportID: 9000, TargetReportID: testReportID}, nil
			},
			export: func(context.Context, int64, int64) (domain.ReportExport, error) {
				return domain.ReportExport{
					ReportID: testReportID, VersionID: testVersionID, BatchID: testBatchID,
					Revision: 1, Content: []byte("# r"), SHA256: sha256Hex([]byte("# r")),
					Size: 3, Filename: "report-9101-r1.md",
				}, nil
			},
			publish: func(context.Context, int64, int64, int64) (domain.Report, error) { return report, nil },
		},
		Analyses: &fakeAnalysisService{
			startFailure: func(context.Context, int64, int64, int64) (domain.AnalysisRun, error) {
				return run, nil
			},
			get: func(context.Context, int64, int64, int64) (domain.AnalysisRun, error) { return run, nil },
			list: func(context.Context, int64, int64) ([]domain.AnalysisRun, error) {
				return []domain.AnalysisRun{run}, nil
			},
		},
		Batches:      &fakeBatchReader{batch: terminalBatch(testVersionID, testBatchID)},
		GlobalIssues: &fakeGlobalIssues{rows: []GlobalIssueRow{{Issue: issue, VersionName: "v1"}}},
		GlobalReports: &fakeGlobalReports{
			rows: []GlobalReportRow{{Report: report, VersionName: "v1", BatchNo: 2}},
		},
	}
}

// TestDeliveryRouteTable proves every P7 delivery route is mounted with the
// documented method and success status.
func TestDeliveryRouteTable(t *testing.T) {
	h := newTestServer(t, successDeps())
	routes := []struct {
		method, path, body string
		want               int
	}{
		{http.MethodGet, "/api/versions/101/issues", "", http.StatusOK},
		{http.MethodPost, "/api/versions/101/issues", `{"title":"t","severity":"major"}`, http.StatusCreated},
		{http.MethodGet, "/api/versions/101/issues/9001", "", http.StatusOK},
		{http.MethodPatch, "/api/versions/101/issues/9001", `{"status":"confirmed","expectedRevision":3}`, http.StatusOK},
		{http.MethodGet, "/api/versions/101/reports", "", http.StatusOK},
		{http.MethodPost, "/api/versions/101/reports", `{"batchId":"7001"}`, http.StatusCreated},
		{http.MethodGet, "/api/versions/101/reports/9101", "", http.StatusOK},
		{http.MethodPost, "/api/versions/101/reports/9101/publish", "", http.StatusOK},
		{http.MethodPost, "/api/versions/101/reports/9101/regenerate", "", http.StatusCreated},
		{http.MethodGet, "/api/versions/101/reports/9101/export?format=markdown", "", http.StatusOK},
		{http.MethodGet, "/api/versions/101/reports/9101/diff", "", http.StatusOK},
		{http.MethodGet, "/api/versions/101/batches/7001/analysis", "", http.StatusOK},
		{http.MethodGet, "/api/versions/101/batches/7001/analysis/9201", "", http.StatusOK},
		{http.MethodPost, "/api/versions/101/batches/7001/analysis", `{"kind":"failure"}`, http.StatusAccepted},
		{http.MethodGet, "/api/issues", "", http.StatusOK},
		{http.MethodGet, "/api/reports", "", http.StatusOK},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			rec := doRequest(h, route.method, route.path, "ezt_user", route.body)
			requireStatus(t, rec, route.want)
		})
	}
}

// TestDeliveryHandlersNilSafe proves a partially assembled process mounts
// nothing instead of panicking.
func TestDeliveryHandlersNilSafe(t *testing.T) {
	if handlers := NewHandlers(HandlerDeps{}); handlers == nil {
		t.Fatal("NewHandlers must never return nil")
	}
	mux := http.NewServeMux()
	var nilHandlers *Handlers
	nilHandlers.RegisterRoutes(mux)
	NewHandlers(HandlerDeps{}).RegisterRoutes(mux)

	h := identityhttp.AuthMiddleware(testTokens)(mux)
	for _, path := range []string{
		"/api/versions/101/issues",
		"/api/versions/101/reports",
		"/api/versions/101/batches/7001/analysis",
		"/api/issues",
		"/api/reports",
	} {
		rec := doRequest(h, http.MethodGet, path, "ezt_user", "")
		requireStatus(t, rec, http.StatusNotFound)
	}
}

// TestP6DerivedReportRouteUnaffected proves the P6 batch report endpoint keeps
// its own route and is not shadowed by the P7 report routes.
func TestP6DerivedReportRouteUnaffected(t *testing.T) {
	mux := http.NewServeMux()
	p6Hit := false
	mux.HandleFunc("GET /api/versions/{id}/batches/{batchId}/report", func(w http.ResponseWriter, r *http.Request) {
		p6Hit = true
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"batch":{"id":"7001"},"summary":{"case_count":1}}}`)
	})
	NewHandlers(successDeps()).RegisterRoutes(mux)
	h := identityhttp.AuthMiddleware(testTokens)(mux)

	rec := doRequest(h, http.MethodGet, "/api/versions/101/batches/7001/report", "ezt_user", "")
	requireStatus(t, rec, http.StatusOK)
	if !p6Hit {
		t.Fatal("P6 derived report handler did not receive its request")
	}
	if body := rec.Body.String(); body == "" || body[0] != '{' || !strings.Contains(body, `"case_count"`) {
		t.Fatalf("P6 report body changed: %s", body)
	}

	// The P7 report routes are distinct and still work next to the P6 route.
	rec = doRequest(h, http.MethodGet, "/api/versions/101/reports", "ezt_user", "")
	requireStatus(t, rec, http.StatusOK)
	rec = doRequest(h, http.MethodGet, "/api/versions/101/reports/9101/export?format=markdown", "ezt_user", "")
	requireStatus(t, rec, http.StatusOK)
}

// TestDTOsDoNotExposeSensitiveFields proves no P7 DTO leaks credentials, prompt
// text, evidence bodies or the stored Markdown path (spec §11.3).
func TestDTOsDoNotExposeSensitiveFields(t *testing.T) {
	h := newTestServer(t, successDeps())
	for _, path := range []string{
		"/api/versions/101/issues/9001",
		"/api/versions/101/reports/9101",
		"/api/versions/101/reports/9101/diff",
		"/api/versions/101/batches/7001/analysis/9201",
		"/api/issues",
		"/api/reports",
	} {
		rec := doRequest(h, http.MethodGet, path, "ezt_user", "")
		requireStatus(t, rec, http.StatusOK)
		body := rec.Body.String()
		for _, forbidden := range []string{
			"password", "storageState", "Authorization", "Cookie",
			"markdownPath", "versions/101/reports/9101-r1.md",
		} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s leaks %q: %s", path, forbidden, body)
			}
		}
	}
}
