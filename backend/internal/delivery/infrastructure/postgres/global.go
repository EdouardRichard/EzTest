package deliverypg

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GlobalRepository is the P6 cross-version read-only adapter of the delivery
// context (spec §9). It reads the delivery tables only; version titles are
// resolved by the application service through the version application port.
type GlobalRepository struct {
	pool *pgxpool.Pool
}

var _ deliveryapp.GlobalIssueReader = (*GlobalRepository)(nil)
var _ deliveryapp.GlobalReportReader = (*GlobalRepository)(nil)

// NewGlobalRepository wraps a pgx pool.
func NewGlobalRepository(pool *pgxpool.Pool) *GlobalRepository {
	return &GlobalRepository{pool: pool}
}

const (
	globalIssuesSQL = `SELECT ` + issueColumns + `
		FROM delivery_issue
		WHERE `

	// globalReportColumns qualify the report columns because the current
	// revision join adds rows with the same column names.
	globalReportColumns = `r.id, r.version_id, r.wave_id, r.kind,
		r.current_revision_id, r.status, r.input_fingerprint, r.created_by_run_id,
		r.created_at, r.updated_at`
	globalRevisionColumns = `rev.id, rev.report_id, rev.revision, rev.status,
		rev.html_ref, rev.pdf_ref, rev.input_fingerprint, rev.as_of,
		rev.template_version, rev.render_status, rev.created_by_run_id,
		rev.created_at, rev.updated_at`

	globalReportsSQL = `SELECT ` + globalReportColumns + `, ` + globalRevisionColumns + `
		FROM delivery_report r
		LEFT JOIN delivery_report_revision rev ON rev.id = r.current_revision_id
		WHERE `

	globalIssueOrderSQL = `
		ORDER BY id DESC
		LIMIT $`

	globalReportOrderSQL = `
		ORDER BY r.id DESC
		LIMIT $`
)

// ListGlobalIssues returns one cross-version descending-ID issue page. The
// adapter applies the version/status/severity/category/title/before filters and
// the page limit; it never loads issue refs or other contexts' tables.
func (r *GlobalRepository) ListGlobalIssues(ctx context.Context, f deliveryapp.GlobalIssueFilter) ([]delivery.Issue, error) {
	ctx = contextOrBackground(ctx)
	where, args := globalIssueWhere(f)
	limit := globalQueryLimit(f.Limit)
	args = append(args, limit)
	query := globalIssuesSQL + where + globalIssueOrderSQL + strconv.Itoa(len(args))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, wrapDB("list global issues", err)
	}
	defer rows.Close()

	issues := make([]delivery.Issue, 0, limit)
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			return nil, wrapDB("scan global issue", err)
		}
		issues = append(issues, issue)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list global issues", err)
	}
	return issues, nil
}

// ListGlobalReports returns one cross-version descending-ID report page. The
// current revision is joined in the same read; report content bytes and
// internal storage refs are never selected.
func (r *GlobalRepository) ListGlobalReports(ctx context.Context, f deliveryapp.GlobalReportFilter) ([]deliveryapp.GlobalReportRow, error) {
	ctx = contextOrBackground(ctx)
	where, args := globalReportWhere(f)
	limit := globalQueryLimit(f.Limit)
	args = append(args, limit)
	query := globalReportsSQL + where + globalReportOrderSQL + strconv.Itoa(len(args))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, wrapDB("list global reports", err)
	}
	defer rows.Close()

	reports := make([]deliveryapp.GlobalReportRow, 0, limit)
	for rows.Next() {
		report, err := scanGlobalReport(rows)
		if err != nil {
			return nil, wrapDB("scan global report", err)
		}
		reports = append(reports, report)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list global reports", err)
	}
	return reports, nil
}

// globalIssueWhere builds the parameterized issue filter. The caller appends
// the limit as the final parameter.
func globalIssueWhere(f deliveryapp.GlobalIssueFilter) (string, []any) {
	conditions := make([]string, 0, 6)
	args := make([]any, 0, 7)
	add := func(condition string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf(condition, len(args)))
	}
	if f.VersionID != nil {
		add("version_id = $%d", *f.VersionID)
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.Category != "" {
		add("category = $%d", f.Category)
	}
	if f.Severity != "" {
		add("severity = $%d", f.Severity)
	}
	if f.Q != "" {
		add("title ILIKE $%d", "%"+f.Q+"%")
	}
	if f.Before > 0 {
		add("id < $%d", f.Before)
	}
	if len(conditions) == 0 {
		return "TRUE", args
	}
	return strings.Join(conditions, " AND "), args
}

// globalReportWhere builds the parameterized report filter. The caller appends
// the limit as the final parameter.
func globalReportWhere(f deliveryapp.GlobalReportFilter) (string, []any) {
	conditions := make([]string, 0, 4)
	args := make([]any, 0, 5)
	add := func(condition string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf(condition, len(args)))
	}
	if f.VersionID != nil {
		add("r.version_id = $%d", *f.VersionID)
	}
	if f.Status != "" {
		add("r.status = $%d", f.Status)
	}
	if f.Kind != "" {
		add("r.kind = $%d", f.Kind)
	}
	if f.Before > 0 {
		add("r.id < $%d", f.Before)
	}
	if len(conditions) == 0 {
		return "TRUE", args
	}
	return strings.Join(conditions, " AND "), args
}

// globalQueryLimit bounds one global page read. The service asks for one extra
// row as the has_more probe, so the internal maximum is the API maximum plus
// one.
func globalQueryLimit(limit int) int {
	switch {
	case limit <= 0:
		return 21
	case limit > 101:
		return 101
	default:
		return limit
	}
}

// scanGlobalReport maps the report header and the nullable current-revision
// join columns of one row.
func scanGlobalReport(row rowScanner) (deliveryapp.GlobalReportRow, error) {
	var (
		report  delivery.Report
		kind    string
		status  string
		revID   *int64
		revRow  delivery.ReportRevision
		revText struct {
			status           *string
			inputFingerprint *string
			templateVersion  *string
			renderStatus     *string
		}
		revNumbers struct {
			reportID     *int64
			revision     *int32
			htmlRef      *int64
			pdfRef       *int64
			createdByRun *int64
		}
		revTimes struct {
			asOf      *time.Time
			createdAt *time.Time
			updatedAt *time.Time
		}
	)

	if err := row.Scan(
		&report.ID, &report.VersionID, &report.WaveID, &kind,
		&report.CurrentRevisionID, &status, &report.InputFingerprint,
		&report.CreatedByRunID, &report.CreatedAt, &report.UpdatedAt,
		&revID, &revNumbers.reportID, &revNumbers.revision,
		&revText.status, &revNumbers.htmlRef, &revNumbers.pdfRef,
		&revText.inputFingerprint, &revTimes.asOf, &revText.templateVersion,
		&revText.renderStatus, &revNumbers.createdByRun, &revTimes.createdAt,
		&revTimes.updatedAt,
	); err != nil {
		return deliveryapp.GlobalReportRow{}, err
	}

	report.Kind = delivery.ReportKind(kind)
	report.Status = delivery.ReportStatus(status)
	projected := deliveryapp.GlobalReportRow{Report: report}
	if revID == nil {
		return projected, nil
	}

	revRow.ID = *revID
	if revNumbers.reportID != nil {
		revRow.ReportID = *revNumbers.reportID
	}
	if revNumbers.revision != nil {
		revRow.Revision = *revNumbers.revision
	}
	if revText.status != nil {
		revRow.Status = delivery.ReportStatus(*revText.status)
	}
	revRow.HTMLRef = revNumbers.htmlRef
	revRow.PDFRef = revNumbers.pdfRef
	if revText.inputFingerprint != nil {
		revRow.InputFingerprint = *revText.inputFingerprint
	}
	if revTimes.asOf != nil {
		revRow.AsOf = *revTimes.asOf
	}
	if revText.templateVersion != nil {
		revRow.TemplateVersion = *revText.templateVersion
	}
	if revText.renderStatus != nil {
		revRow.RenderStatus = *revText.renderStatus
	}
	revRow.CreatedByRunID = revNumbers.createdByRun
	if revTimes.createdAt != nil {
		revRow.CreatedAt = *revTimes.createdAt
	}
	if revTimes.updatedAt != nil {
		revRow.UpdatedAt = *revTimes.updatedAt
	}
	projected.CurrentRevision = &revRow
	return projected, nil
}
