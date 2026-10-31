package deliveryapi

import (
	"net/http"
	"testing"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// TestIssueRoutesAreRegistered exercises the local mux registration used by the
// server assembly: Register must mount every issue path with the contract's
// parameter names and reach the application service only.
func TestIssueRoutesAreRegistered(t *testing.T) {
	issue := sampleIssue(1001, delivery.IssueStatusConfirmed)
	service := &fakeIssueService{
		listResult:  []delivery.Issue{issue},
		actionIssue: issue,
		details:     map[int64]deliveryapp.IssueDetail{1001: sampleIssueDetail(issue)},
	}
	mux := newDeliveryMux(t, New(service))

	recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/issues", "", nil)
	requireStatus(t, recorder, http.StatusOK)
	if service.listCalls != 1 || service.versionID != 42 {
		t.Fatalf("list route did not reach the service: calls=%d version=%d", service.listCalls, service.versionID)
	}

	recorder = deliveryCall(t, mux, http.MethodGet, "/api/versions/42/issues/1001", "", nil)
	requireStatus(t, recorder, http.StatusOK)
	if len(service.getCalls) == 0 || service.getCalls[0].issueID != 1001 {
		t.Fatalf("detail route did not reach the service: %#v", service.getCalls)
	}

	recorder = deliveryCall(t, mux, http.MethodPost, "/api/versions/42/issues/1001/confirm", `{}`, userActor())
	requireStatus(t, recorder, http.StatusOK)
	if len(service.confirmCalls) != 1 || service.confirmCalls[0].issueID != 1001 {
		t.Fatalf("confirm route did not reach the service: %#v", service.confirmCalls)
	}

	// The mux must reject unsupported methods instead of falling through.
	recorder = deliveryCall(t, mux, http.MethodDelete, "/api/versions/42/issues/1001", "", nil)
	requireStatus(t, recorder, http.StatusMethodNotAllowed)
}

// TestReportRoutesAreRegistered exercises the local report mux registration:
// Register must mount list/detail/revision/HTML/regenerate and reach the report
// application service only. The issue service may be absent in such an
// assembly; report routes must still work.
func TestReportRoutesAreRegistered(t *testing.T) {
	reports, _ := reportFixture()
	html := []byte("<html>report</html>")
	reports.html = html
	reports.htmlRevision = sampleRevision(900, 300, 2, delivery.ReportStatusPublished, "done", int64Ptr(500))
	reports.regenerateRevision = 2
	mux := newDeliveryMux(t, New(nil, WithReports(reports)))

	recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports", "", nil)
	requireStatus(t, recorder, http.StatusOK)
	if reports.listCalls != 1 || reports.listVersion != 42 {
		t.Fatalf("list route did not reach the report service: calls=%d version=%d", reports.listCalls, reports.listVersion)
	}

	recorder = deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports/300", "", nil)
	requireStatus(t, recorder, http.StatusOK)
	if len(reports.getCalls) == 0 || reports.getCalls[0].reportID != 300 {
		t.Fatalf("detail route did not reach the report service: %#v", reports.getCalls)
	}

	recorder = deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports/300/revisions/2", "", nil)
	requireStatus(t, recorder, http.StatusOK)

	recorder = deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports/300/revisions/2/html", "", nil)
	requireStatus(t, recorder, http.StatusOK)
	if recorder.Body.String() != string(html) {
		t.Fatalf("html route body = %q", recorder.Body.String())
	}

	recorder = deliveryCall(t, mux, http.MethodPost, "/api/versions/42/reports/300/regenerate", "", userActor())
	requireStatus(t, recorder, http.StatusAccepted)
	if len(reports.regenerateCalls) != 1 || reports.regenerateCalls[0].reportID != 300 {
		t.Fatalf("regenerate route did not reach the report service: %#v", reports.regenerateCalls)
	}
}
