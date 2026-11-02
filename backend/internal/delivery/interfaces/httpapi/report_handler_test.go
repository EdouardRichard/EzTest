package deliveryapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

/* --------------------------------- fakes ---------------------------------- */

type reportGetCall struct {
	versionID int64
	reportID  int64
}

type reportHTMLCall struct {
	versionID int64
	reportID  int64
	revision  int32
}

type reportPDFCall struct {
	versionID int64
	reportID  int64
	revision  int32
}

type reportRegenerateCall struct {
	versionID int64
	reportID  int64
}

// fakeReportService records every call and returns configured fixtures/errors.
type fakeReportService struct {
	listResult  []delivery.Report
	listErr     error
	listCalls   int
	listVersion int64
	listKind    *delivery.ReportKind

	details  map[int64]deliveryapp.ReportDetail
	getErr   error
	getCalls []reportGetCall

	html         []byte
	htmlRevision delivery.ReportRevision
	htmlErr      error
	htmlCalls    []reportHTMLCall

	pdf         []byte
	pdfRevision delivery.ReportRevision
	pdfErr      error
	pdfCalls    []reportPDFCall

	regenerateRevision int32
	regenerateErr      error
	regenerateCalls    []reportRegenerateCall
}

func (f *fakeReportService) List(_ context.Context, versionID int64, kind *delivery.ReportKind) ([]delivery.Report, error) {
	f.listCalls++
	f.listVersion = versionID
	f.listKind = kind
	if f.listErr != nil {
		return nil, f.listErr
	}
	if kind == nil {
		return f.listResult, nil
	}
	filtered := make([]delivery.Report, 0, len(f.listResult))
	for _, report := range f.listResult {
		if report.Kind == *kind {
			filtered = append(filtered, report)
		}
	}
	return filtered, nil
}

func (f *fakeReportService) Get(_ context.Context, versionID, reportID int64) (deliveryapp.ReportDetail, error) {
	f.getCalls = append(f.getCalls, reportGetCall{versionID: versionID, reportID: reportID})
	if f.getErr != nil {
		return deliveryapp.ReportDetail{}, f.getErr
	}
	detail, ok := f.details[reportID]
	if !ok || (detail.Report.VersionID != 0 && detail.Report.VersionID != versionID) {
		return deliveryapp.ReportDetail{}, platform.NotFound("report")
	}
	return detail, nil
}

func (f *fakeReportService) OpenHTML(_ context.Context, versionID, reportID int64, revision int32) ([]byte, delivery.ReportRevision, error) {
	f.htmlCalls = append(f.htmlCalls, reportHTMLCall{versionID: versionID, reportID: reportID, revision: revision})
	if f.htmlErr != nil {
		return nil, delivery.ReportRevision{}, f.htmlErr
	}
	return f.html, f.htmlRevision, nil
}

func (f *fakeReportService) OpenPDF(_ context.Context, versionID, reportID int64, revision int32) ([]byte, delivery.ReportRevision, error) {
	f.pdfCalls = append(f.pdfCalls, reportPDFCall{versionID: versionID, reportID: reportID, revision: revision})
	if f.pdfErr != nil {
		return nil, delivery.ReportRevision{}, f.pdfErr
	}
	return f.pdf, f.pdfRevision, nil
}

func (f *fakeReportService) Regenerate(_ context.Context, versionID, reportID int64) (int32, error) {
	f.regenerateCalls = append(f.regenerateCalls, reportRegenerateCall{versionID: versionID, reportID: reportID})
	if f.regenerateErr != nil {
		return 0, f.regenerateErr
	}
	return f.regenerateRevision, nil
}

var _ ReportService = (*fakeReportService)(nil)

/* -------------------------------- fixtures -------------------------------- */

func sampleReport(id int64, kind delivery.ReportKind, waveID *int64, status delivery.ReportStatus, currentRevisionID *int64) delivery.Report {
	return delivery.Report{
		ID:                id,
		VersionID:         42,
		WaveID:            waveID,
		Kind:              kind,
		CurrentRevisionID: currentRevisionID,
		Status:            status,
		InputFingerprint:  strings64("c"),
		CreatedAt:         deliveryTestTime,
		UpdatedAt:         deliveryTestTime.Add(time.Minute),
	}
}

func sampleRevision(id, reportID int64, revision int32, status delivery.ReportStatus, renderStatus string, htmlRef *int64) delivery.ReportRevision {
	return delivery.ReportRevision{
		ID:               id,
		ReportID:         reportID,
		Revision:         revision,
		Status:           status,
		HTMLRef:          htmlRef,
		InputFingerprint: strings64("d"),
		AsOf:             deliveryTestTime,
		TemplateVersion:  "1",
		RenderStatus:     renderStatus,
		PDFStatus:        delivery.PDFStatusPending,
		CreatedAt:        deliveryTestTime,
		UpdatedAt:        deliveryTestTime,
	}
}

func sampleReportDetail(report delivery.Report) deliveryapp.ReportDetail {
	return deliveryapp.ReportDetail{
		Report: report,
		Revisions: []delivery.ReportRevision{
			sampleRevision(900, report.ID, 2, delivery.ReportStatusPublished, "done", int64Ptr(500)),
			sampleRevision(800, report.ID, 1, delivery.ReportStatusDraft, "failed", nil),
		},
		Refs: []delivery.ReportRef{
			{
				ID: 1, ReportID: report.ID, ReportRevisionID: 900, Kind: "requirement", RefID: 7,
				CreatedAt: deliveryTestTime, UpdatedAt: deliveryTestTime,
			},
			{
				ID: 2, ReportID: report.ID, ReportRevisionID: 900, Kind: "wave", RefID: 3,
				CreatedAt: deliveryTestTime, UpdatedAt: deliveryTestTime,
			},
		},
	}
}

// reportFixture returns a mixed version/wave report list whose handler order
// (version first, then wave ID DESC) deliberately differs from raw ID DESC: the
// draft wave report 400 has the highest ID.
func reportFixture() (*fakeReportService, []delivery.Report) {
	versionReport := sampleReport(300, delivery.ReportKindVersion, nil,
		delivery.ReportStatusPublished, int64Ptr(900))
	wave15 := sampleReport(400, delivery.ReportKindWave, int64Ptr(15),
		delivery.ReportStatusDraft, nil)
	wave12 := sampleReport(250, delivery.ReportKindWave, int64Ptr(12),
		delivery.ReportStatusPublished, int64Ptr(950))
	wave9 := sampleReport(200, delivery.ReportKindWave, int64Ptr(9),
		delivery.ReportStatusStale, int64Ptr(920))

	reports := []delivery.Report{wave12, wave15, versionReport, wave9}
	service := &fakeReportService{
		listResult: reports,
		details: map[int64]deliveryapp.ReportDetail{
			300: sampleReportDetail(versionReport),
			250: sampleReportDetail(wave12),
			200: sampleReportDetail(wave9),
		},
	}
	return service, reports
}

func strings64(seed string) string {
	value := ""
	for len(value) < 64 {
		value += seed
	}
	return value[:64]
}

/* ---------------------------------- tests --------------------------------- */

func TestReportHandlerListOrdersAndProjectsViews(t *testing.T) {
	service, _ := reportFixture()
	mux := newDeliveryMux(t, New(nil, WithReports(service)))

	recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports?limit=2", "", nil)
	requireStatus(t, recorder, http.StatusOK)

	if service.listCalls != 1 || service.listVersion != 42 {
		t.Fatalf("List calls = %d version = %d", service.listCalls, service.listVersion)
	}
	if service.listKind != nil {
		t.Errorf("List kind = %v, want nil when the filter is omitted", service.listKind)
	}

	items, nextCursor, hasMore := decodeDeliveryList(t, recorder)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2; body = %s", len(items), recorder.Body.String())
	}
	if !hasMore || nextCursor != "400" {
		t.Errorf("pagination = (%q, %v), want (400, true)", nextCursor, hasMore)
	}

	versionView, _ := items[0].(map[string]any)
	if versionView["id"] != "300" || versionView["kind"] != "version" || versionView["status"] != "published" {
		t.Errorf("first report = %v, want the version summary first", versionView)
	}
	if versionView["version_id"] != "42" || versionView["wave_id"] != nil {
		t.Errorf("version report ids = %v/%v", versionView["version_id"], versionView["wave_id"])
	}
	if versionView["input_fingerprint"] != strings64("c") {
		t.Errorf("input_fingerprint = %v", versionView["input_fingerprint"])
	}
	if versionView["created_at"] != "2026-11-01T06:00:00Z" || versionView["updated_at"] != "2026-11-01T06:01:00Z" {
		t.Errorf("timestamps = %v/%v", versionView["created_at"], versionView["updated_at"])
	}
	current, ok := versionView["current_revision"].(map[string]any)
	if !ok {
		t.Fatalf("current_revision = %#v, want an object", versionView["current_revision"])
	}
	if current["id"] != "900" || current["revision"] != float64(2) || current["html_available"] != true {
		t.Errorf("current_revision = %v", current)
	}

	draftView, _ := items[1].(map[string]any)
	if draftView["id"] != "400" || draftView["wave_id"] != "15" || draftView["status"] != "draft" {
		t.Errorf("second report = %v, want the newest wave report", draftView)
	}
	if value, present := draftView["current_revision"]; !present || value != nil {
		t.Errorf("draft current_revision = %v (present=%v), want null", value, present)
	}
}

func TestReportHandlerListFiltersByKind(t *testing.T) {
	service, _ := reportFixture()
	mux := newDeliveryMux(t, New(nil, WithReports(service)))

	recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports?kind=wave", "", nil)
	requireStatus(t, recorder, http.StatusOK)
	if service.listKind == nil || *service.listKind != delivery.ReportKindWave {
		t.Fatalf("List kind = %v, want wave", service.listKind)
	}
	items, _, _ := decodeDeliveryList(t, recorder)
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	for _, raw := range items {
		if kind := raw.(map[string]any)["kind"]; kind != "wave" {
			t.Errorf("kind = %v, want wave", kind)
		}
	}
}

func TestReportHandlerListBeforeCursor(t *testing.T) {
	service, _ := reportFixture()
	mux := newDeliveryMux(t, New(nil, WithReports(service)))

	recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports?before=300", "", nil)
	requireStatus(t, recorder, http.StatusOK)
	items, nextCursor, hasMore := decodeDeliveryList(t, recorder)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	first := items[0].(map[string]any)
	if first["id"] != "250" || first["wave_id"] != "12" {
		t.Errorf("first item = %v, want wave 12 after the cursor", first)
	}
	if hasMore || nextCursor != "" {
		t.Errorf("pagination = (%q, %v), want exhausted", nextCursor, hasMore)
	}
}

func TestReportHandlerListRejectsInvalidQueries(t *testing.T) {
	tests := []struct {
		name  string
		query string
		field string
	}{
		{name: "kind", query: "?kind=summary", field: "kind"},
		{name: "before", query: "?before=0", field: "before"},
		{name: "before text", query: "?before=soon", field: "before"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, _ := reportFixture()
			mux := newDeliveryMux(t, New(nil, WithReports(service)))
			recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports"+test.query, "", nil)
			requireStatus(t, recorder, http.StatusBadRequest)
			if service.listCalls != 0 {
				t.Errorf("List calls = %d, want none", service.listCalls)
			}
			details := errorDetails(t, decodeDeliveryError(t, recorder))
			if details["field"] != test.field {
				t.Errorf("field = %v, want %s", details["field"], test.field)
			}
		})
	}
}

func TestReportHandlerDetailProjectsRevisionsAndRefs(t *testing.T) {
	report := sampleReport(300, delivery.ReportKindVersion, nil,
		delivery.ReportStatusStale, int64Ptr(900))
	service := &fakeReportService{details: map[int64]deliveryapp.ReportDetail{300: sampleReportDetail(report)}}
	mux := newDeliveryMux(t, New(nil, WithReports(service)))

	recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports/300", "", nil)
	requireStatus(t, recorder, http.StatusOK)

	data := decodeDeliveryObject(t, recorder)
	if data["id"] != "300" || data["status"] != "stale" || data["kind"] != "version" {
		t.Errorf("report = %v", data)
	}
	current, ok := data["current_revision"].(map[string]any)
	if !ok || current["id"] != "900" || current["revision"] != float64(2) {
		t.Errorf("current_revision = %#v", data["current_revision"])
	}
	if current["as_of"] != "2026-11-01T06:00:00Z" || current["template_version"] != "1" {
		t.Errorf("current_revision metadata = %v", current)
	}

	revisions, ok := data["revisions"].([]any)
	if !ok || len(revisions) != 2 {
		t.Fatalf("revisions = %#v, want 2", data["revisions"])
	}
	published, _ := revisions[0].(map[string]any)
	if published["id"] != "900" || published["report_id"] != "300" || published["revision"] != float64(2) {
		t.Errorf("published revision = %v", published)
	}
	if published["status"] != "published" || published["render_status"] != "done" {
		t.Errorf("published status/render = %v/%v", published["status"], published["render_status"])
	}
	if published["html_available"] != true || published["pdf_available"] != false {
		t.Errorf("published availability = %v/%v", published["html_available"], published["pdf_available"])
	}
	draft, _ := revisions[1].(map[string]any)
	if draft["html_available"] != false {
		t.Errorf("draft html_available = %v, want false", draft["html_available"])
	}

	refs, ok := data["refs"].([]any)
	if !ok || len(refs) != 2 {
		t.Fatalf("refs = %#v, want 2", data["refs"])
	}
	ref, _ := refs[0].(map[string]any)
	if ref["kind"] != "requirement" || ref["ref_id"] != "7" {
		t.Errorf("ref = %v", ref)
	}
}

func TestReportHandlerDetailMapsErrors(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		service := &fakeReportService{details: map[int64]deliveryapp.ReportDetail{}}
		mux := newDeliveryMux(t, New(nil, WithReports(service)))
		recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports/999", "", nil)
		requireStatus(t, recorder, http.StatusNotFound)
		if code := decodeDeliveryError(t, recorder)["code"]; code != "not_found" {
			t.Errorf("code = %v", code)
		}
	})
	t.Run("foreign version", func(t *testing.T) {
		report := sampleReport(300, delivery.ReportKindVersion, nil, delivery.ReportStatusPublished, int64Ptr(900))
		service := &fakeReportService{details: map[int64]deliveryapp.ReportDetail{300: sampleReportDetail(report)}}
		mux := newDeliveryMux(t, New(nil, WithReports(service)))
		recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/99/reports/300", "", nil)
		requireStatus(t, recorder, http.StatusNotFound)
	})
	t.Run("unknown", func(t *testing.T) {
		service := &fakeReportService{getErr: errors.New("boom")}
		mux := newDeliveryMux(t, New(nil, WithReports(service)))
		recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports/300", "", nil)
		requireStatus(t, recorder, http.StatusInternalServerError)
	})
	t.Run("invalid id", func(t *testing.T) {
		service := &fakeReportService{}
		mux := newDeliveryMux(t, New(nil, WithReports(service)))
		recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports/abc", "", nil)
		requireStatus(t, recorder, http.StatusBadRequest)
	})
}

func TestReportHandlerRevisionMetadata(t *testing.T) {
	report := sampleReport(300, delivery.ReportKindVersion, nil, delivery.ReportStatusPublished, int64Ptr(900))
	service := &fakeReportService{details: map[int64]deliveryapp.ReportDetail{300: sampleReportDetail(report)}}
	mux := newDeliveryMux(t, New(nil, WithReports(service)))

	recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports/300/revisions/2", "", nil)
	requireStatus(t, recorder, http.StatusOK)
	data := decodeDeliveryObject(t, recorder)
	if data["id"] != "900" || data["report_id"] != "300" || data["revision"] != float64(2) {
		t.Errorf("revision = %v", data)
	}
	if data["status"] != "published" || data["render_status"] != "done" ||
		data["html_available"] != true || data["pdf_available"] != false {
		t.Errorf("revision state = %v", data)
	}
	if data["after"] != nil {
		t.Errorf("unknown field leaked: %v", data["after"])
	}

	recorder = deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports/300/revisions/7", "", nil)
	requireStatus(t, recorder, http.StatusNotFound)

	for _, path := range []string{"/api/versions/42/reports/300/revisions/abc", "/api/versions/42/reports/300/revisions/0"} {
		recorder = deliveryCall(t, mux, http.MethodGet, path, "", nil)
		requireStatus(t, recorder, http.StatusBadRequest)
		details := errorDetails(t, decodeDeliveryError(t, recorder))
		if details["field"] != "revision" {
			t.Errorf("field = %v, want revision", details["field"])
		}
	}
}

func TestReportHandlerHTMLDownload(t *testing.T) {
	html := []byte("<!doctype html><html><body>report</body></html>")
	revision := sampleRevision(900, 300, 2, delivery.ReportStatusPublished, "done", int64Ptr(500))
	service := &fakeReportService{html: html, htmlRevision: revision}
	mux := newDeliveryMux(t, New(nil, WithReports(service)))

	recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports/300/revisions/2/html", "", nil)
	requireStatus(t, recorder, http.StatusOK)

	if len(service.htmlCalls) != 1 || service.htmlCalls[0].reportID != 300 || service.htmlCalls[0].revision != 2 {
		t.Fatalf("OpenHTML calls = %#v", service.htmlCalls)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	sum := sha256.Sum256(html)
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
	const wantCSP = "default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; base-uri 'none'; form-action 'none'"
	if got := recorder.Header().Get("Content-Security-Policy"); got != wantCSP {
		t.Errorf("Content-Security-Policy = %q", got)
	}
	if got := recorder.Header().Get("Content-Disposition"); got == "" {
		t.Errorf("Content-Disposition is empty")
	}
	if recorder.Body.String() != string(html) {
		t.Errorf("body = %q, want the verified HTML bytes", recorder.Body.String())
	}
}

func TestReportHandlerHTMLDownloadErrors(t *testing.T) {
	corrupt := platform.StateConflict("report content failed its integrity check")
	corrupt.Details = map[string]any{"reason": "report_content_corrupt"}
	tests := []struct {
		name   string
		err    error
		status int
		reason string
	}{
		{name: "not published", err: platform.NotFound("report revision"), status: http.StatusNotFound},
		{name: "missing content", err: platform.NotFound("report content"), status: http.StatusNotFound},
		{name: "corrupt", err: corrupt, status: http.StatusConflict, reason: "report_content_corrupt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeReportService{htmlErr: test.err}
			mux := newDeliveryMux(t, New(nil, WithReports(service)))
			recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports/300/revisions/2/html", "", nil)
			requireStatus(t, recorder, test.status)
			apiError := decodeDeliveryError(t, recorder)
			if test.status == http.StatusConflict {
				if code := apiError["code"]; code != "state_conflict" {
					t.Errorf("code = %v", code)
				}
				if reason := errorDetails(t, apiError)["reason"]; reason != test.reason {
					t.Errorf("details.reason = %v, want %s", reason, test.reason)
				}
			}
		})
	}
}

func TestReportHandlerHTMLRejectsInvalidPath(t *testing.T) {
	service := &fakeReportService{}
	mux := newDeliveryMux(t, New(nil, WithReports(service)))

	recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports/300/revisions/0/html", "", nil)
	requireStatus(t, recorder, http.StatusBadRequest)
	if len(service.htmlCalls) != 0 {
		t.Errorf("OpenHTML calls = %d, want none", len(service.htmlCalls))
	}
}

func TestReportHandlerRegenerateIsIdempotent202(t *testing.T) {
	service := &fakeReportService{regenerateRevision: 2}
	mux := newDeliveryMux(t, New(nil, WithReports(service)))

	for attempt := 0; attempt < 2; attempt++ {
		recorder := deliveryCall(t, mux, http.MethodPost, "/api/versions/42/reports/300/regenerate", "", userActor())
		requireStatus(t, recorder, http.StatusAccepted)
		data := decodeDeliveryObject(t, recorder)
		if data["report_id"] != "300" || data["revision"] != float64(2) {
			t.Errorf("attempt %d data = %v", attempt, data)
		}
		if value, present := data["work_item_id"]; !present || value != nil {
			t.Errorf("work_item_id = %v (present=%v), want null", value, present)
		}
	}
	if len(service.regenerateCalls) != 2 || service.regenerateCalls[0].reportID != 300 {
		t.Fatalf("Regenerate calls = %#v", service.regenerateCalls)
	}

	recorder := deliveryCall(t, mux, http.MethodPost, "/api/versions/42/reports/300/regenerate",
		`{"reason":"manual"}`, userActor())
	requireStatus(t, recorder, http.StatusAccepted)
	if len(service.regenerateCalls) != 3 {
		t.Errorf("Regenerate calls = %d, want 3", len(service.regenerateCalls))
	}
}

func TestReportHandlerRegenerateErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{name: "missing report", err: platform.NotFound("report"), status: http.StatusNotFound},
		{name: "state conflict", err: platform.StateConflict("report is not in a valid state"), status: http.StatusConflict},
		{name: "unknown", err: errors.New("boom"), status: http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeReportService{regenerateErr: test.err}
			mux := newDeliveryMux(t, New(nil, WithReports(service)))
			recorder := deliveryCall(t, mux, http.MethodPost, "/api/versions/42/reports/300/regenerate", "", userActor())
			requireStatus(t, recorder, test.status)
		})
	}
}

func TestReportHandlerRegenerateValidatesBody(t *testing.T) {
	service := &fakeReportService{regenerateRevision: 2}
	mux := newDeliveryMux(t, New(nil, WithReports(service)))

	recorder := deliveryCall(t, mux, http.MethodPost, "/api/versions/42/reports/300/regenerate", `{"reason":`, userActor())
	requireStatus(t, recorder, http.StatusBadRequest)
	longReason := strings.Repeat("x", 2001)
	recorder = deliveryCall(t, mux, http.MethodPost, "/api/versions/42/reports/300/regenerate",
		`{"reason":"`+longReason+`"}`, userActor())
	requireStatus(t, recorder, http.StatusBadRequest)
	if len(service.regenerateCalls) != 0 {
		t.Errorf("Regenerate calls = %d, want none", len(service.regenerateCalls))
	}
}

func TestReportHandlerRequiresActorForRegenerate(t *testing.T) {
	service := &fakeReportService{regenerateRevision: 2}
	mux := newDeliveryMux(t, New(nil, WithReports(service)))

	recorder := deliveryCall(t, mux, http.MethodPost, "/api/versions/42/reports/300/regenerate", "", nil)
	requireStatus(t, recorder, http.StatusUnauthorized)
	if len(service.regenerateCalls) != 0 {
		t.Errorf("Regenerate calls = %d, want none", len(service.regenerateCalls))
	}
}

func TestReportHandlerWithoutReportsService(t *testing.T) {
	mux := newDeliveryMux(t, New(nil))
	recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/reports", "", nil)
	requireStatus(t, recorder, http.StatusInternalServerError)
}
