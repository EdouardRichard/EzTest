package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// GlobalReadRepository implements the delivery-owned persistence ports of the
// cross-version centres (spec §7.10). It reads only the delivery context's own
// tables (issue/issue_ref and report); the version display names are resolved by
// the application service through the version-context port, never by joining the
// version table here (Agent.md §6.2).
type GlobalReadRepository struct {
	q issueQuerier
}

// NewGlobalReadRepository returns the cross-version reader backed by pool.
func NewGlobalReadRepository(pool *pgxpool.Pool) *GlobalReadRepository {
	return &GlobalReadRepository{q: pool}
}

var (
	_ application.GlobalIssueStore  = (*GlobalReadRepository)(nil)
	_ application.GlobalReportStore = (*GlobalReadRepository)(nil)
)

// globalIssueListSQL is the cross-version issue page: the §7.3 filters (plus the
// optional versionId) over issue only, newest first, with the created_at/id
// keyset predicate. The limit+1 probe row tells whether another page exists.
const globalIssueListSQL = `SELECT ` + issueColumnList + ` FROM issue
	WHERE ($1::bigint IS NULL OR version_id = $1)
	  AND ($2::text = '' OR status = $2)
	  AND ($3::text = '' OR severity = $3)
	  AND ($4::text = '' OR category = $4)
	  AND ($5::bigint IS NULL OR batch_id = $5)
	  AND ($6::bigint IS NULL OR case_run_id = $6)
	  AND ($7::bigint IS NULL OR case_node_id = $7)
	  AND ($8::text = '' OR title ILIKE '%' || $8 || '%')
	  AND ($9::boolean = false OR (created_at, id) < ($10::timestamptz, $11::bigint))
	ORDER BY created_at DESC, id DESC
	LIMIT $12`

// globalIssueRefsSQL loads the refs of one page in a single round trip.
const globalIssueRefsSQL = `SELECT issue_id, ref_kind, ref_id FROM issue_ref
	WHERE issue_id = ANY($1)
	ORDER BY issue_id, ref_kind, ref_id`

// globalReportListSQL is the cross-version report page. The list projection keeps
// the content column out of the result set; batchNo is read directly from the
// stored snapshot (report content.batch.batchNo) so no execution-context read is
// needed. The scan appends this extra column after reportListColumnList.
const globalReportListSQL = `SELECT ` + reportListColumnList + `,
		COALESCE(NULLIF(content->'batch'->>'batchNo', '')::int, 0)
	FROM report
	WHERE ($1::bigint IS NULL OR version_id = $1)
	  AND ($2::text = '' OR status = $2)
	  AND ($3::boolean = false OR (created_at, id) < ($4::timestamptz, $5::bigint))
	ORDER BY created_at DESC, id DESC
	LIMIT $6`

// issueCursorSQL resolves the opaque cursor id of the global issue page.
const issueCursorSQL = `SELECT created_at FROM issue WHERE id = $1`

// reportCursorSQL resolves the opaque cursor id of the global report page.
const reportCursorSQL = `SELECT created_at FROM report WHERE id = $1`

// ListGlobalIssues returns one created_at-DESC page of the issues of every
// version, with the §7.3 filters and an opaque keyset cursor.
func (r *GlobalReadRepository) ListGlobalIssues(ctx context.Context, filter application.GlobalIssueFilter) ([]application.GlobalIssueRow, string, bool, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = issueDefaultPageSize
	}
	cursorID, err := platform.DecodeCursor(filter.Cursor)
	if err != nil {
		return nil, "", false, fmt.Errorf("postgres: list global issues: %w", err)
	}
	var (
		cursorCreated time.Time
		hasCursor     bool
	)
	if cursorID != 0 {
		if err := r.q.QueryRow(ctx, issueCursorSQL, cursorID).Scan(&cursorCreated); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return []application.GlobalIssueRow{}, "", false, nil
			}
			return nil, "", false, fmt.Errorf("postgres: list global issues: read cursor: %w", err)
		}
		hasCursor = true
	}

	rows, err := r.q.Query(ctx, globalIssueListSQL,
		issueNullableInt(filter.VersionID), filter.Status, filter.Severity, filter.Category,
		issueNullableInt(filter.BatchID), issueNullableInt(filter.CaseRunID),
		issueNullableInt(filter.CaseNodeID), filter.Query,
		hasCursor, cursorCreated, cursorID, limit+1)
	if err != nil {
		return nil, "", false, fmt.Errorf("postgres: list global issues: %w", err)
	}
	defer rows.Close()

	page := make([]application.GlobalIssueRow, 0, limit+1)
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			return nil, "", false, fmt.Errorf("postgres: scan global issue: %w", err)
		}
		page = append(page, application.GlobalIssueRow{Issue: issue})
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, fmt.Errorf("postgres: list global issues: %w", err)
	}

	more := len(page) > limit
	next := ""
	if more {
		page = page[:limit]
		next = platform.EncodeCursor(page[len(page)-1].Issue.ID)
	}
	if len(page) == 0 {
		return []application.GlobalIssueRow{}, "", false, nil
	}
	if err := r.attachIssueRefs(ctx, page); err != nil {
		return nil, "", false, err
	}
	return page, next, more, nil
}

// ListGlobalReports returns one created_at-DESC page of the reports of every
// version, with the optional version/status filters and an opaque keyset cursor.
func (r *GlobalReadRepository) ListGlobalReports(ctx context.Context, filter application.GlobalReportFilter) ([]application.GlobalReportRow, string, bool, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = issueDefaultPageSize
	}
	cursorID, err := platform.DecodeCursor(filter.Cursor)
	if err != nil {
		return nil, "", false, fmt.Errorf("postgres: list global reports: %w", err)
	}
	var (
		cursorCreated time.Time
		hasCursor     bool
	)
	if cursorID != 0 {
		if err := r.q.QueryRow(ctx, reportCursorSQL, cursorID).Scan(&cursorCreated); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return []application.GlobalReportRow{}, "", false, nil
			}
			return nil, "", false, fmt.Errorf("postgres: list global reports: read cursor: %w", err)
		}
		hasCursor = true
	}

	rows, err := r.q.Query(ctx, globalReportListSQL,
		issueNullableInt(filter.VersionID), filter.Status,
		hasCursor, cursorCreated, cursorID, limit+1)
	if err != nil {
		return nil, "", false, fmt.Errorf("postgres: list global reports: %w", err)
	}
	defer rows.Close()

	page := make([]application.GlobalReportRow, 0, limit+1)
	for rows.Next() {
		report, batchNo, err := scanGlobalReport(rows)
		if err != nil {
			return nil, "", false, fmt.Errorf("postgres: scan global report: %w", err)
		}
		page = append(page, application.GlobalReportRow{Report: report, BatchNo: batchNo})
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, fmt.Errorf("postgres: list global reports: %w", err)
	}

	more := len(page) > limit
	next := ""
	if more {
		page = page[:limit]
		next = platform.EncodeCursor(page[len(page)-1].Report.ID)
	}
	return page, next, more, nil
}

// attachIssueRefs loads the refs of every issue of one page in a single query
// and attaches them in (issue, kind, id) order.
func (r *GlobalReadRepository) attachIssueRefs(ctx context.Context, page []application.GlobalIssueRow) error {
	ids := make([]int64, 0, len(page))
	indexByID := make(map[int64]int, len(page))
	for index, row := range page {
		ids = append(ids, row.Issue.ID)
		indexByID[row.Issue.ID] = index
	}
	rows, err := r.q.Query(ctx, globalIssueRefsSQL, ids)
	if err != nil {
		return fmt.Errorf("postgres: list global issue refs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var ref domain.IssueRef
		if err := rows.Scan(&ref.IssueID, &ref.RefKind, &ref.RefID); err != nil {
			return fmt.Errorf("postgres: scan global issue ref: %w", err)
		}
		if index, ok := indexByID[ref.IssueID]; ok {
			page[index].Refs = append(page[index].Refs, ref)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: list global issue refs: %w", err)
	}
	return nil
}

// globalReportScanner appends the batchNo column to scanReport's destinations so
// the shared report column mapping is reused without a second projection.
type globalReportScanner struct {
	rows    reportRowScanner
	batchNo *int
}

// Scan implements reportRowScanner.
func (s globalReportScanner) Scan(dest ...any) error {
	targets := make([]any, 0, len(dest)+1)
	targets = append(targets, dest...)
	targets = append(targets, s.batchNo)
	return s.rows.Scan(targets...)
}

// scanGlobalReport scans one report list row plus its derived batch number.
func scanGlobalReport(row reportRowScanner) (domain.Report, int, error) {
	batchNo := 0
	report, err := scanReport(globalReportScanner{rows: row, batchNo: &batchNo})
	if err != nil {
		return domain.Report{}, 0, err
	}
	return report, batchNo, nil
}
