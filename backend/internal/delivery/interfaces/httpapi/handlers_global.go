package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"unicode/utf8"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// GlobalIssueFilter is the cross-version issue filter of GET /api/issues
// (spec §7.10): the version-scoped filter of §7.3 plus versionId. The cursor is
// the opaque keyset cursor owned by the reader. The shape is owned by
// delivery/application (T17b) so the infrastructure readers can implement the
// port without importing this package; the HTTP contract is unchanged.
type GlobalIssueFilter = deliveryapp.GlobalIssueFilter

// GlobalIssueRow is one row of the global issue centre: the issue aggregate, its
// refs and the owning version's display name.
type GlobalIssueRow = deliveryapp.GlobalIssueRow

// GlobalIssueReader is the cross-version issue page query of GET /api/issues.
// *deliveryapp.GlobalReaderService implements it (T17b), over the delivery-owned
// issue store and the version-name adapter; the composition root wires it.
type GlobalIssueReader interface {
	ListGlobalIssues(ctx context.Context, filter GlobalIssueFilter) ([]GlobalIssueRow, string, bool, error)
}

// GlobalReportFilter is the cross-version report filter of GET /api/reports
// (spec §7.10). The cursor is the opaque keyset cursor owned by the reader.
type GlobalReportFilter = deliveryapp.GlobalReportFilter

// GlobalReportRow is one row of the global report centre: the report summary, its
// owning version's display name and the batch number.
type GlobalReportRow = deliveryapp.GlobalReportRow

// GlobalReportReader is the cross-version report page query of GET /api/reports.
// *deliveryapp.GlobalReaderService implements it (T17b).
type GlobalReportReader interface {
	ListGlobalReports(ctx context.Context, filter GlobalReportFilter) ([]GlobalReportRow, string, bool, error)
}

// GlobalIssueHandler serves the read-only global issue centre (spec §7.10).
type GlobalIssueHandler struct {
	issues   GlobalIssueReader
	codeRefs IssueCodeRefReader
}

// RegisterRoutes mounts GET /api/issues behind the global bearer auth.
func (h *GlobalIssueHandler) RegisterRoutes(mux *http.ServeMux) {
	if h == nil || mux == nil {
		return
	}
	mux.HandleFunc("GET /api/issues", h.List)
}

// List returns one page of the cross-version issues with their version summary
// and deep-link fields.
func (h *GlobalIssueHandler) List(w http.ResponseWriter, r *http.Request) {
	filter := GlobalIssueFilter{
		Status:   optionalString(r.URL.Query().Get("status")),
		Severity: optionalString(r.URL.Query().Get("severity")),
		Category: optionalString(r.URL.Query().Get("category")),
		Query:    optionalString(r.URL.Query().Get("q")),
	}
	// The global reader has no application service behind it to normalize the
	// filters, so the enums and the q bound are validated here. An invalid value
	// must be a 422, never a silently empty page (spec §7.3/§7.10).
	if filter.Status != "" && !stringInSlice(filter.Status,
		domain.StatusOpen, domain.StatusConfirmed, domain.StatusDismissed) {
		writeDeliveryError(w, r, domain.NewInvalid("status", domain.ReasonInvalidArgument,
			"status must be open, confirmed or dismissed"))
		return
	}
	if filter.Severity != "" {
		if err := domain.ValidateSeverity(filter.Severity); err != nil {
			writeDeliveryError(w, r, err)
			return
		}
	}
	if filter.Category != "" {
		if err := domain.ValidateCategory(filter.Category); err != nil {
			writeDeliveryError(w, r, err)
			return
		}
	}
	if utf8.RuneCountInString(filter.Query) > domain.MaxIssueQueryRunes {
		writeDeliveryError(w, r, domain.NewInvalid("q", domain.ReasonInvalidArgument,
			fmt.Sprintf("q must be at most %d characters", domain.MaxIssueQueryRunes)))
		return
	}
	var err error
	if filter.VersionID, err = parseOptionalQueryID(r, "versionId"); err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	if filter.BatchID, err = parseOptionalQueryID(r, "batchId"); err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	if filter.CaseRunID, err = parseOptionalQueryID(r, "caseRunId"); err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	if filter.CaseNodeID, err = parseOptionalQueryID(r, "caseNodeId"); err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	if filter.Cursor, err = parseOpaqueCursor(r); err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	if filter.Limit, err = parseLimit(r); err != nil {
		writeDeliveryError(w, r, err)
		return
	}

	rows, next, hasMore, err := h.issues.ListGlobalIssues(r.Context(), filter)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	items := make([]globalIssueDTO, 0, len(rows))
	for _, row := range rows {
		items = append(items, newGlobalIssueDTO(row,
			readIssueCodeRefs(r.Context(), h.codeRefs, row.Issue.VersionID, row.Issue.ID)))
	}
	writePage(w, items, next, hasMore)
}

// GlobalReportHandler serves the read-only global report centre (spec §7.10).
type GlobalReportHandler struct {
	reports GlobalReportReader
}

// RegisterRoutes mounts GET /api/reports behind the global bearer auth.
func (h *GlobalReportHandler) RegisterRoutes(mux *http.ServeMux) {
	if h == nil || mux == nil {
		return
	}
	mux.HandleFunc("GET /api/reports", h.List)
}

// List returns one page of the cross-version reports with their version summary,
// batch number and deep-link fields.
func (h *GlobalReportHandler) List(w http.ResponseWriter, r *http.Request) {
	filter := GlobalReportFilter{Status: optionalString(r.URL.Query().Get("status"))}
	if filter.Status != "" && !domain.IsValidReportStatus(filter.Status) {
		writeDeliveryError(w, r, domain.NewInvalid("status", domain.ReasonInvalidArgument,
			"status must be draft, published or stale"))
		return
	}
	var err error
	if filter.VersionID, err = parseOptionalQueryID(r, "versionId"); err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	if filter.Cursor, err = parseOpaqueCursor(r); err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	if filter.Limit, err = parseLimit(r); err != nil {
		writeDeliveryError(w, r, err)
		return
	}

	rows, next, hasMore, err := h.reports.ListGlobalReports(r.Context(), filter)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	items := make([]globalReportDTO, 0, len(rows))
	for _, row := range rows {
		items = append(items, newGlobalReportDTO(row))
	}
	writePage(w, items, next, hasMore)
}
