package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// GlobalIssueFilter is the cross-version issue filter of GET /api/issues
// (spec §7.10): the version-scoped filter of §7.3 plus versionId. The cursor is
// the opaque keyset cursor owned by the reader.
type GlobalIssueFilter struct {
	VersionID  *int64
	Status     string
	Severity   string
	Category   string
	BatchID    *int64
	CaseRunID  *int64
	CaseNodeID *int64
	Query      string
	Cursor     string
	Limit      int
}

// GlobalIssueRow is one row of the global issue centre: the issue aggregate, its
// refs and the owning version's display name.
type GlobalIssueRow struct {
	Issue       domain.Issue
	Refs        []domain.IssueRef
	VersionName string
}

// GlobalIssueReader is the cross-version issue page query of GET /api/issues.
//
// The delivery application package currently has only version-scoped queries
// (IssueService.List), so the T12 handlers consume this consumer-defined port.
// T17 must add the additive cross-version application/repository query and adapt
// it here (issue joined with version.name, filters + created_at DESC,id DESC
// keyset pagination); T12 is not allowed to edit the T2-owned service files.
type GlobalIssueReader interface {
	ListGlobalIssues(ctx context.Context, filter GlobalIssueFilter) ([]GlobalIssueRow, string, bool, error)
}

// GlobalReportFilter is the cross-version report filter of GET /api/reports
// (spec §7.10). The cursor is the opaque keyset cursor owned by the reader.
type GlobalReportFilter struct {
	VersionID *int64
	Status    string
	Cursor    string
	Limit     int
}

// GlobalReportRow is one row of the global report centre: the report summary, its
// owning version's display name and the batch number.
type GlobalReportRow struct {
	Report      domain.Report
	VersionName string
	BatchNo     int
}

// GlobalReportReader is the cross-version report page query of GET /api/reports.
// It has the same T17 gap as GlobalIssueReader: the delivery application package
// only exposes version-scoped report queries.
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
