package deliveryapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// samplePDFRevision is one published revision whose PDF is done.
func samplePDFRevision(reportID int64, pdfRef int64) delivery.ReportRevision {
	revision := sampleRevision(900, reportID, 2, delivery.ReportStatusPublished, "done", int64Ptr(500))
	revision.PDFStatus = delivery.PDFStatusDone
	revision.PDFRef = int64Ptr(pdfRef)
	renderedAt := deliveryTestTime
	revision.PDFRenderedAt = &renderedAt
	return revision
}

// TestReportHandlerRevisionPDFFields covers the non-breaking P7 DTO widening:
// pdf_available is derived from pdf_status+pdf_ref and pdf_status/pdf_error/
// pdf_rendered_at are always present.
func TestReportHandlerRevisionPDFFields(t *testing.T) {
	report := sampleReport(300, delivery.ReportKindVersion, nil,
		delivery.ReportStatusPublished, int64Ptr(900))
	detail := sampleReportDetail(report)
	detail.Revisions[0] = samplePDFRevision(report.ID, 500)
	detail.Revisions[1].PDFStatus = delivery.PDFStatusFailed
	detail.Revisions[1].PDFError = "renderer unavailable"
	detail.Revisions[1].PDFAttempts = 3
	service := &fakeReportService{details: map[int64]deliveryapp.ReportDetail{300: detail}}
	mux := newDeliveryMux(t, New(nil, WithReports(service)))

	recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports/300", "", nil)
	requireStatus(t, recorder, http.StatusOK)
	data := decodeDeliveryObject(t, recorder)
	revisions, ok := data["revisions"].([]any)
	if !ok || len(revisions) != 2 {
		t.Fatalf("revisions = %#v", data["revisions"])
	}

	done := revisions[0].(map[string]any)
	if done["pdf_available"] != true || done["pdf_status"] != "done" {
		t.Errorf("done PDF fields = available:%v status:%v", done["pdf_available"], done["pdf_status"])
	}
	if done["pdf_rendered_at"] != "2026-11-01T06:00:00Z" {
		t.Errorf("pdf_rendered_at = %v", done["pdf_rendered_at"])
	}
	if value, present := done["pdf_error"]; !present || value != nil {
		t.Errorf("pdf_error = %v (present=%v), want null", value, present)
	}

	failed := revisions[1].(map[string]any)
	if failed["pdf_available"] != false || failed["pdf_status"] != "failed" {
		t.Errorf("failed PDF fields = available:%v status:%v", failed["pdf_available"], failed["pdf_status"])
	}
	if failed["pdf_error"] != "renderer unavailable" {
		t.Errorf("pdf_error = %v", failed["pdf_error"])
	}

	// The single-revision endpoint exposes the same fields.
	recorder = deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports/300/revisions/2", "", nil)
	requireStatus(t, recorder, http.StatusOK)
	revisionView := decodeDeliveryObject(t, recorder)
	if revisionView["pdf_available"] != true || revisionView["pdf_status"] != "done" {
		t.Errorf("revision PDF fields = %v", revisionView)
	}
}

// TestReportHandlerPDFDownload covers the P7 PDF download headers and bytes.
func TestReportHandlerPDFDownload(t *testing.T) {
	pdf := []byte("%PDF-1.7\n报告 PDF")
	revision := samplePDFRevision(300, 600)
	service := &fakeReportService{pdf: pdf, pdfRevision: revision}
	mux := newDeliveryMux(t, New(nil, WithReports(service)))

	recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports/300/revisions/2/pdf", "", nil)
	requireStatus(t, recorder, http.StatusOK)
	if len(service.pdfCalls) != 1 || service.pdfCalls[0].versionID != 42 ||
		service.pdfCalls[0].reportID != 300 || service.pdfCalls[0].revision != 2 {
		t.Fatalf("OpenPDF calls = %#v", service.pdfCalls)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/pdf" {
		t.Errorf("Content-Type = %q", got)
	}
	sum := sha256.Sum256(pdf)
	wantETag := `"` + hex.EncodeToString(sum[:]) + `"`
	if got := recorder.Header().Get("ETag"); got != wantETag {
		t.Errorf("ETag = %q, want %q", got, wantETag)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if got := recorder.Header().Get("Content-Disposition"); got == "" {
		t.Errorf("Content-Disposition is empty")
	}
	if recorder.Body.String() != string(pdf) {
		t.Errorf("body = %q, want the verified PDF bytes", recorder.Body.String())
	}
}

// TestReportHandlerPDFDownloadErrors maps the frozen PDF error semantics:
// pending/running 409 pdf_not_ready, failed 409 pdf_failed, missing 404.
func TestReportHandlerPDFDownloadErrors(t *testing.T) {
	pending := platform.NewError(http.StatusConflict, "pdf_not_ready", "report PDF is not ready")
	failed := platform.NewError(http.StatusConflict, "pdf_failed", "report PDF rendering failed")
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "pending", err: pending, status: http.StatusConflict, code: "pdf_not_ready"},
		{name: "failed", err: failed, status: http.StatusConflict, code: "pdf_failed"},
		{name: "missing", err: platform.NotFound("report PDF"), status: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeReportService{pdfErr: test.err}
			mux := newDeliveryMux(t, New(nil, WithReports(service)))
			recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports/300/revisions/2/pdf", "", nil)
			requireStatus(t, recorder, test.status)
			if test.code != "" {
				if code := decodeDeliveryError(t, recorder)["code"]; code != test.code {
					t.Errorf("code = %v, want %s", code, test.code)
				}
			}
		})
	}
}

func TestReportHandlerPDFRejectsInvalidPath(t *testing.T) {
	service := &fakeReportService{}
	mux := newDeliveryMux(t, New(nil, WithReports(service)))
	for _, path := range []string{
		"/api/versions/42/reports/300/revisions/0/pdf",
		"/api/versions/42/reports/300/revisions/abc/pdf",
	} {
		recorder := deliveryCall(t, mux, http.MethodGet, path, "", nil)
		requireStatus(t, recorder, http.StatusBadRequest)
	}
	if len(service.pdfCalls) != 0 {
		t.Errorf("OpenPDF calls = %d, want none", len(service.pdfCalls))
	}
}

func TestReportHandlerPDFWithoutReportsService(t *testing.T) {
	mux := newDeliveryMux(t, New(nil))
	recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports/300/revisions/2/pdf", "", nil)
	requireStatus(t, recorder, http.StatusInternalServerError)
}
