package deliverypg

import (
	"context"
	"fmt"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ deliveryapp.QueryReader = (*QueryReader)(nil)

// QueryReader is the delivery context's P6 read-only source. It only reads the
// delivery-owned issue/report tables; the version title is resolved by the
// consumer through the version application port, never by a cross-context join.
type QueryReader struct {
	pool *pgxpool.Pool
}

// NewQueryReader wraps a pgx pool. A nil pool is a wiring error.
func NewQueryReader(pool *pgxpool.Pool) (*QueryReader, error) {
	if pool == nil {
		return nil, fmt.Errorf("delivery query reader: nil pool")
	}
	return &QueryReader{pool: pool}, nil
}

// CountIssueValidity returns confirmed issues over confirmed+dismissed issues,
// for one version when versionID is non-nil and globally otherwise. Open and
// merged issues are excluded from both counters.
func (r *QueryReader) CountIssueValidity(ctx context.Context, versionID *int64) (int, int, error) {
	var num, denom int
	if err := r.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE status = 'confirmed'),
			count(*) FILTER (WHERE status IN ('confirmed', 'dismissed'))
		FROM delivery_issue
		WHERE $1::bigint IS NULL OR version_id = $1
	`, versionID).Scan(&num, &denom); err != nil {
		return 0, 0, fmt.Errorf("count issue validity: %w", err)
	}
	return num, denom, nil
}

// SearchIssues returns delivery_issue title matches, newest first.
func (r *QueryReader) SearchIssues(ctx context.Context, q string, limit int) ([]deliveryapp.IssueSearchRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, version_id, title, status, severity, updated_at
		FROM delivery_issue
		WHERE title ILIKE '%' || $1 || '%'
		ORDER BY updated_at DESC, id DESC
		LIMIT $2
	`, q, limit)
	if err != nil {
		return nil, fmt.Errorf("search issues: %w", err)
	}
	defer rows.Close()

	items := make([]deliveryapp.IssueSearchRow, 0)
	for rows.Next() {
		var item deliveryapp.IssueSearchRow
		if err := rows.Scan(&item.ID, &item.VersionID, &item.Title,
			&item.Status, &item.Severity, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan issue search row: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan issue search rows: %w", err)
	}
	return items, nil
}

// SearchReports returns delivery_report matches. A report has no title column:
// the search matches the report kind, its status and the derived display title
// («波次报告»/«版本报告») so a user query in either vocabulary finds the report.
func (r *QueryReader) SearchReports(ctx context.Context, q string, limit int) ([]deliveryapp.ReportSearchRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, version_id, kind, status, updated_at
		FROM delivery_report
		WHERE kind ILIKE '%' || $1 || '%'
		   OR status ILIKE '%' || $1 || '%'
		   OR (kind = 'wave' AND '波次报告' ILIKE '%' || $1 || '%')
		   OR (kind = 'version' AND '版本报告' ILIKE '%' || $1 || '%')
		ORDER BY updated_at DESC, id DESC
		LIMIT $2
	`, q, limit)
	if err != nil {
		return nil, fmt.Errorf("search reports: %w", err)
	}
	defer rows.Close()

	items := make([]deliveryapp.ReportSearchRow, 0)
	for rows.Next() {
		var item deliveryapp.ReportSearchRow
		if err := rows.Scan(&item.ID, &item.VersionID, &item.Kind,
			&item.Status, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan report search row: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan report search rows: %w", err)
	}
	return items, nil
}

// ListOpenIssues returns the open issues, newest first.
func (r *QueryReader) ListOpenIssues(ctx context.Context, limit int) ([]deliveryapp.OpenIssueRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, version_id, title, severity, created_at
		FROM delivery_issue
		WHERE status = 'open'
		ORDER BY created_at DESC, id DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list open issues: %w", err)
	}
	defer rows.Close()

	items := make([]deliveryapp.OpenIssueRow, 0)
	for rows.Next() {
		var item deliveryapp.OpenIssueRow
		if err := rows.Scan(&item.ID, &item.VersionID, &item.Title,
			&item.Severity, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan open issue: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan open issues: %w", err)
	}
	return items, nil
}
