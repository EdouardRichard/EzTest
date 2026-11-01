package deliveryapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// reportContentSecurityPolicy is the frozen CSP of the HTML download endpoint
// (spec §6.2/§6.4).
const reportContentSecurityPolicy = "default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; base-uri 'none'; form-action 'none'"

// ReportService is the report application surface the handlers consume. It is
// implemented by *deliveryapp.ReportService.
type ReportService interface {
	List(ctx context.Context, versionID int64, kind *delivery.ReportKind) ([]delivery.Report, error)
	Get(ctx context.Context, versionID, reportID int64) (deliveryapp.ReportDetail, error)
	OpenHTML(ctx context.Context, versionID, reportID int64, revision int32) ([]byte, delivery.ReportRevision, error)
	OpenPDF(ctx context.Context, versionID, reportID int64, revision int32) ([]byte, delivery.ReportRevision, error)
	Regenerate(ctx context.Context, versionID, reportID int64) (int32, error)
}

var _ ReportService = (*deliveryapp.ReportService)(nil)

/* ---------------------------------- list ---------------------------------- */

// ListReports GET /api/versions/{versionId}/reports. The contract order is the
// version summary first, then wave reports by wave ID DESC; the page then
// applies the before/limit window.
func (h *Handler) ListReports(w http.ResponseWriter, r *http.Request) {
	if h.reports == nil {
		writeNotConfigured(w, r, "report service")
		return
	}
	versionID, err := pathID(r, "versionId", "versionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	query := r.URL.Query()
	kindValue, err := optionalEnum("kind", query.Get("kind"), "wave", "version")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var kind *delivery.ReportKind
	if kindValue != "" {
		parsed := delivery.ReportKind(kindValue)
		kind = &parsed
	}
	before, err := parseBefore(query.Get("before"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	limit := pageLimit(r)

	reports, err := h.reports.List(r.Context(), versionID, kind)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	// Sort a copy: the service owns the returned slice and may reuse it.
	reports = append([]delivery.Report(nil), reports...)
	sortReports(reports)
	window := make([]delivery.Report, 0, len(reports))
	for _, report := range reports {
		if before > 0 && report.ID >= before {
			continue
		}
		window = append(window, report)
	}
	hasMore := len(window) > limit
	if hasMore {
		window = window[:limit]
	}

	items := make([]ReportView, 0, len(window))
	for _, report := range window {
		view, viewErr := h.reportView(r.Context(), versionID, report)
		if viewErr != nil {
			httpx.Error(w, r, viewErr)
			return
		}
		items = append(items, view)
	}
	nextCursor := ""
	if hasMore && len(window) > 0 {
		nextCursor = platformid.Format(window[len(window)-1].ID)
	}
	httpx.List(w, http.StatusOK, items, nextCursor, hasMore)
}

// sortReports applies the frozen list order: version reports first, then wave
// reports by wave ID DESC, with the report ID as the stable tie-breaker.
func sortReports(reports []delivery.Report) {
	sort.SliceStable(reports, func(i, j int) bool {
		left, right := reports[i], reports[j]
		leftVersion := left.Kind == delivery.ReportKindVersion
		rightVersion := right.Kind == delivery.ReportKindVersion
		if leftVersion != rightVersion {
			return leftVersion
		}
		if left.Kind == delivery.ReportKindWave && right.Kind == delivery.ReportKindWave {
			leftWave, rightWave := int64(0), int64(0)
			if left.WaveID != nil {
				leftWave = *left.WaveID
			}
			if right.WaveID != nil {
				rightWave = *right.WaveID
			}
			if leftWave != rightWave {
				return leftWave > rightWave
			}
		}
		return left.ID > right.ID
	})
}

// reportView projects one report and, when it has a current revision, resolves
// that revision summary through the detail read.
func (h *Handler) reportView(ctx context.Context, versionID int64, report delivery.Report) (ReportView, error) {
	if report.CurrentRevisionID == nil || *report.CurrentRevisionID <= 0 {
		return toReportView(report, nil), nil
	}
	detail, err := h.reports.Get(ctx, versionID, report.ID)
	if err != nil {
		return ReportView{}, err
	}
	return toReportView(report, currentReportRevision(detail.Report, detail.Revisions)), nil
}

/* --------------------------------- detail --------------------------------- */

// GetReport GET /api/versions/{versionId}/reports/{reportId}.
func (h *Handler) GetReport(w http.ResponseWriter, r *http.Request) {
	if h.reports == nil {
		writeNotConfigured(w, r, "report service")
		return
	}
	versionID, err := pathID(r, "versionId", "versionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	reportID, err := pathID(r, "reportId", "reportId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	detail, err := h.reports.Get(r.Context(), versionID, reportID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	revisions := make([]ReportRevisionView, 0, len(detail.Revisions))
	for _, revision := range detail.Revisions {
		revisions = append(revisions, toReportRevisionView(revision))
	}
	refs := make([]ReportRefView, 0, len(detail.Refs))
	for _, ref := range detail.Refs {
		refs = append(refs, toReportRefView(ref))
	}
	httpx.JSON(w, http.StatusOK, ReportDetailView{
		ReportView: toReportView(detail.Report, currentReportRevision(detail.Report, detail.Revisions)),
		Revisions:  revisions,
		Refs:       refs,
	})
}

// GetReportRevision GET /api/versions/{versionId}/reports/{reportId}/revisions/{revision}.
func (h *Handler) GetReportRevision(w http.ResponseWriter, r *http.Request) {
	if h.reports == nil {
		writeNotConfigured(w, r, "report service")
		return
	}
	versionID, err := pathID(r, "versionId", "versionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	reportID, err := pathID(r, "reportId", "reportId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	revisionNumber, err := parseRevision(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	detail, err := h.reports.Get(r.Context(), versionID, reportID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	revision, found := findReportRevision(detail.Revisions, revisionNumber)
	if !found {
		httpx.Error(w, r, platform.NotFound("report revision"))
		return
	}
	httpx.JSON(w, http.StatusOK, toReportRevisionView(revision))
}

/* ---------------------------------- html ---------------------------------- */

// DownloadReportHTML GET .../reports/{reportId}/revisions/{revision}/html.
// The application service verifies version ownership, published/render state,
// content-ledger ownership and the sha256/size; this handler only sets the
// frozen download headers and writes the verified bytes.
func (h *Handler) DownloadReportHTML(w http.ResponseWriter, r *http.Request) {
	if h.reports == nil {
		writeNotConfigured(w, r, "report service")
		return
	}
	versionID, err := pathID(r, "versionId", "versionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	reportID, err := pathID(r, "reportId", "reportId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	revisionNumber, err := parseRevision(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	html, _, err := h.reports.OpenHTML(r.Context(), versionID, reportID, revisionNumber)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	sum := sha256.Sum256(html)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", reportContentSecurityPolicy)
	w.Header().Set("Content-Length", strconv.Itoa(len(html)))
	w.Header().Set("Content-Disposition", reportContentDisposition(reportID, revisionNumber))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(html)
}

// reportContentDisposition builds a safe inline filename from the decimal IDs;
// internal content paths are never exposed.
func reportContentDisposition(reportID int64, revision int32) string {
	name := "report-" + platformid.Format(reportID) + "-r" + strconv.FormatInt(int64(revision), 10) + ".html"
	if disposition := mime.FormatMediaType("inline", map[string]string{"filename": name}); disposition != "" {
		return disposition
	}
	return "inline"
}

// DownloadReportPDF GET .../reports/{reportId}/revisions/{revision}/pdf. The
// application service verifies version ownership, the PDF state, content-ledger
// ownership and sha256/size; this handler only sets the frozen download headers
// and writes the verified bytes (spec §6.4).
func (h *Handler) DownloadReportPDF(w http.ResponseWriter, r *http.Request) {
	if h.reports == nil {
		writeNotConfigured(w, r, "report service")
		return
	}
	versionID, err := pathID(r, "versionId", "versionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	reportID, err := pathID(r, "reportId", "reportId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	revisionNumber, err := parseRevision(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	pdf, _, err := h.reports.OpenPDF(r.Context(), versionID, reportID, revisionNumber)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	sum := sha256.Sum256(pdf)
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(pdf)))
	w.Header().Set("Content-Disposition", reportPDFContentDisposition(reportID, revisionNumber))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdf)
}

// reportPDFContentDisposition builds the safe inline PDF filename.
func reportPDFContentDisposition(reportID int64, revision int32) string {
	name := "report-" + platformid.Format(reportID) + "-r" + strconv.FormatInt(int64(revision), 10) + ".pdf"
	if disposition := mime.FormatMediaType("inline", map[string]string{"filename": name}); disposition != "" {
		return disposition
	}
	return "inline"
}

/* ------------------------------- regenerate -------------------------------- */

// RegenerateReport POST /api/versions/{versionId}/reports/{reportId}/regenerate.
// The application service makes the call idempotent for an in-flight revision;
// the endpoint always answers 202 with that revision number.
func (h *Handler) RegenerateReport(w http.ResponseWriter, r *http.Request) {
	if h.reports == nil {
		writeNotConfigured(w, r, "report service")
		return
	}
	if _, err := requireActor(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	versionID, err := pathID(r, "versionId", "versionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	reportID, err := pathID(r, "reportId", "reportId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request regenerateReportRequest
	if err := decodeBody(w, r, &request, false); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := request.parse(); err != nil {
		httpx.Error(w, r, err)
		return
	}
	revision, err := h.reports.Regenerate(r.Context(), versionID, reportID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, RegenerateReportResult{
		ReportID: platformid.Format(reportID),
		Revision: revision,
	})
}

/* --------------------------------- helpers -------------------------------- */

// parseRevision parses the positive integer revision path parameter.
func parseRevision(r *http.Request) (int32, error) {
	raw := strings.TrimSpace(r.PathValue("revision"))
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 || value > math.MaxInt32 {
		return 0, platform.Invalid("revision", "must be a positive integer")
	}
	return int32(value), nil
}
