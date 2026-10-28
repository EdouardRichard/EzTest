// Package postgres implements the delivery context's repository ports with
// PostgreSQL. Every report write supplies the application-generated Snowflake id
// (D29): the DDL has no sequence or default. The report Markdown itself never
// enters the database; the row only carries the root-relative path, sha256 and
// size (D30).
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// reportUniqueViolationCode is PostgreSQL's SQLSTATE for unique_violation.
const reportUniqueViolationCode = "23505"

// reportColumnList is the shared column order of every report query and the
// order scanReport expects. The list projection selects NULL for content so list
// pages never move report bodies.
const reportColumnList = `id, version_id, batch_id, revision, status, title, content,
	input_fingerprint, markdown_path, markdown_sha256, markdown_size, stale_reason,
	generated_by, published_at, created_at, updated_at`

// reportListColumnList is reportColumnList with content replaced by NULL.
const reportListColumnList = `id, version_id, batch_id, revision, status, title, NULL::jsonb,
	input_fingerprint, markdown_path, markdown_sha256, markdown_size, stale_reason,
	generated_by, published_at, created_at, updated_at`

// reportRowScanner is the subset of pgx.Row and pgx.Rows used to scan one row.
type reportRowScanner interface {
	Scan(dest ...any) error
}

// ReportRepository implements application.ReportStore on a pgx pool.
type ReportRepository struct {
	pool *pgxpool.Pool
}

// NewReportRepository returns a report repository backed by pool.
func NewReportRepository(pool *pgxpool.Pool) *ReportRepository {
	return &ReportRepository{pool: pool}
}

var _ application.ReportStore = (*ReportRepository)(nil)

// Create inserts one fully populated revision and its report_ref rows in one
// transaction. The Markdown metadata is known before the file write (the bytes
// are rendered in memory), so the row is inserted first and a (batch_id,
// revision) collision maps to domain.ErrReportRevisionConflict for Ensure's
// re-read.
func (r *ReportRepository) Create(ctx context.Context, report domain.Report) (domain.Report, error) {
	content, err := json.Marshal(report.Content)
	if err != nil {
		return domain.Report{}, fmt.Errorf("postgres: marshal report content: %w", err)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Report{}, fmt.Errorf("postgres: begin report transaction: %w", err)
	}
	defer func() {
		// A commit already closed the transaction; this rollback is then a
		// no-op. WithoutCancel keeps the cleanup working when the caller's
		// context was cancelled.
		_ = tx.Rollback(context.WithoutCancel(ctx))
	}()
	const query = `INSERT INTO report
		(id, version_id, batch_id, revision, status, title, content, input_fingerprint,
		 markdown_path, markdown_sha256, markdown_size, stale_reason, generated_by,
		 published_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		RETURNING ` + reportColumnList
	created, err := scanReport(tx.QueryRow(ctx, query,
		report.ID, report.VersionID, report.BatchID, report.Revision, report.Status,
		report.Title, string(content), report.InputFingerprint, report.MarkdownPath,
		reportNullableString(report.MarkdownSHA256), report.MarkdownSize,
		report.StaleReason, reportNullableInt(report.GeneratedBy),
		reportNullableTime(report.PublishedAt), report.CreatedAt, report.UpdatedAt,
	))
	if err != nil {
		switch reportUniqueConstraint(err) {
		case "uk_report_batch_revision":
			return domain.Report{}, fmt.Errorf("%w: batch %d revision %d",
				domain.ErrReportRevisionConflict, report.BatchID, report.Revision)
		case "pk_report":
			return domain.Report{}, fmt.Errorf("postgres: report %d already exists: %w", report.ID, err)
		}
		return domain.Report{}, fmt.Errorf("postgres: create report: %w", err)
	}
	for _, ref := range reportRefsFromContent(report.Content) {
		if _, err := tx.Exec(ctx,
			`INSERT INTO report_ref (report_id, ref_kind, ref_id) VALUES ($1, $2, $3)
			 ON CONFLICT DO NOTHING`,
			report.ID, ref.Kind, ref.ID); err != nil {
			return domain.Report{}, fmt.Errorf("postgres: create report ref: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Report{}, fmt.Errorf("postgres: commit report transaction: %w", err)
	}
	return created, nil
}

// Get returns one revision of versionID, including its content snapshot.
func (r *ReportRepository) Get(ctx context.Context, versionID, reportID int64) (domain.Report, error) {
	report, err := scanReport(r.pool.QueryRow(ctx,
		`SELECT `+reportColumnList+` FROM report WHERE id = $1 AND version_id = $2`,
		reportID, versionID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Report{}, fmt.Errorf("%w: report %d", domain.ErrReportNotFound, reportID)
		}
		return domain.Report{}, fmt.Errorf("postgres: get report: %w", err)
	}
	return report, nil
}

// LatestByBatch returns the highest revision of the batch.
func (r *ReportRepository) LatestByBatch(ctx context.Context, versionID, batchID int64) (domain.Report, error) {
	report, err := scanReport(r.pool.QueryRow(ctx,
		`SELECT `+reportColumnList+`
		 FROM report WHERE version_id = $1 AND batch_id = $2
		 ORDER BY revision DESC LIMIT 1`,
		versionID, batchID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Report{}, fmt.Errorf("%w: batch %d", domain.ErrReportNotFound, batchID)
		}
		return domain.Report{}, fmt.Errorf("postgres: latest report of batch: %w", err)
	}
	return report, nil
}

// List returns one page of the version's reports newest first with an opaque
// keyset cursor. BatchID and Status are optional filters; list rows omit the
// content snapshot.
func (r *ReportRepository) List(ctx context.Context, versionID int64, filter application.ReportFilter) ([]domain.Report, string, bool, error) {
	if filter.Limit <= 0 {
		return nil, "", false, fmt.Errorf("postgres: list reports: limit must be positive")
	}
	cursorID, err := platform.DecodeCursor(filter.Cursor)
	if err != nil {
		return nil, "", false, fmt.Errorf("postgres: list reports: %w", err)
	}
	var (
		cursorCreated time.Time
		hasCursor     bool
	)
	if cursorID != 0 {
		if err := r.pool.QueryRow(ctx,
			`SELECT created_at FROM report WHERE id = $1 AND version_id = $2`,
			cursorID, versionID).Scan(&cursorCreated); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return []domain.Report{}, "", false, nil
			}
			return nil, "", false, fmt.Errorf("postgres: list reports: read cursor: %w", err)
		}
		hasCursor = true
	}

	const query = `SELECT ` + reportListColumnList + `
		FROM report
		WHERE version_id = $1
		  AND ($2::bigint IS NULL OR batch_id = $2)
		  AND ($3::text = '' OR status = $3)
		  AND ($4::boolean = false OR (created_at, id) < ($5::timestamptz, $6::bigint))
		ORDER BY created_at DESC, id DESC
		LIMIT $7`
	rows, err := r.pool.Query(ctx, query, versionID, reportNullableInt(filter.BatchID),
		filter.Status, hasCursor, cursorCreated, cursorID, filter.Limit+1)
	if err != nil {
		return nil, "", false, fmt.Errorf("postgres: list reports: %w", err)
	}
	defer rows.Close()

	reports := make([]domain.Report, 0, filter.Limit+1)
	for rows.Next() {
		report, err := scanReport(rows)
		if err != nil {
			return nil, "", false, fmt.Errorf("postgres: scan report: %w", err)
		}
		reports = append(reports, report)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, fmt.Errorf("postgres: list reports: %w", err)
	}

	more := len(reports) > filter.Limit
	nextCursor := ""
	if more {
		reports = reports[:filter.Limit]
		nextCursor = platform.EncodeCursor(reports[len(reports)-1].ID)
	}
	return reports, nextCursor, more, nil
}

// NextRevision returns max(revision)+1 of the batch.
func (r *ReportRepository) NextRevision(ctx context.Context, versionID, batchID int64) (int, error) {
	var revision int
	if err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(revision), 0) + 1 FROM report WHERE version_id = $1 AND batch_id = $2`,
		versionID, batchID).Scan(&revision); err != nil {
		return 0, fmt.Errorf("postgres: next report revision: %w", err)
	}
	return revision, nil
}

// MarkStale flips published→stale with a CAS on the stored fingerprint. False
// means the row was already changed (another reader won or it is not published).
func (r *ReportRepository) MarkStale(ctx context.Context, versionID, reportID int64, fingerprint, reason string, at time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE report SET status = 'stale', stale_reason = $3, updated_at = $4
		 WHERE version_id = $1 AND id = $2 AND status = 'published' AND input_fingerprint = $5`,
		versionID, reportID, reason, at, fingerprint)
	if err != nil {
		return false, fmt.Errorf("postgres: mark report stale: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// MarkPublished flips draft→published with a CAS on the status.
func (r *ReportRepository) MarkPublished(ctx context.Context, versionID, reportID int64, at time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE report SET status = 'published', published_at = $3, updated_at = $3
		 WHERE version_id = $1 AND id = $2 AND status = 'draft'`,
		versionID, reportID, at)
	if err != nil {
		return false, fmt.Errorf("postgres: publish report: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// Delete removes one revision whose Markdown file could not be written. It is
// the compensating action of generate (spec §6.5), not a user-facing delete.
func (r *ReportRepository) Delete(ctx context.Context, versionID, reportID int64) error {
	if _, err := r.pool.Exec(ctx,
		`DELETE FROM report WHERE version_id = $1 AND id = $2`, versionID, reportID); err != nil {
		return fmt.Errorf("postgres: delete report: %w", err)
	}
	return nil
}

// scanReport scans one row in reportColumnList/reportListColumnList order.
func scanReport(row reportRowScanner) (domain.Report, error) {
	var (
		report      domain.Report
		content     []byte
		markdownSHA *string
		generatedBy *int64
		publishedAt *time.Time
	)
	if err := row.Scan(
		&report.ID,
		&report.VersionID,
		&report.BatchID,
		&report.Revision,
		&report.Status,
		&report.Title,
		&content,
		&report.InputFingerprint,
		&report.MarkdownPath,
		&markdownSHA,
		&report.MarkdownSize,
		&report.StaleReason,
		&generatedBy,
		&publishedAt,
		&report.CreatedAt,
		&report.UpdatedAt,
	); err != nil {
		return domain.Report{}, err
	}
	if markdownSHA != nil {
		report.MarkdownSHA256 = *markdownSHA
	}
	report.GeneratedBy = generatedBy
	report.PublishedAt = publishedAt
	if len(content) > 0 {
		if err := json.Unmarshal(content, &report.Content); err != nil {
			return domain.Report{}, fmt.Errorf("postgres: unmarshal report content: %w", err)
		}
	}
	return report, nil
}

// reportNullableInt renders an optional bigint binding column; nil becomes NULL.
func reportNullableInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

// reportNullableString renders an optional text binding column; nil becomes
// NULL. An empty string is also stored as NULL because the DDL only allows NULL
// or a full sha256 for markdown_sha256.
func reportNullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// reportNullableTime renders an optional timestamptz binding column.
func reportNullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}

// reportRef is one report_ref row derived from the snapshot.
type reportRef struct {
	Kind string
	ID   string
}

// reportRefsFromContent derives the report_ref rows of spec §4.2.2/§6.5 from
// the snapshot: the batch, every case run, issue and requirement, plus the
// 40-hex base/head commits. Evidence bodies and code anchors are deliberately
// absent because the snapshot only carries counts/locations, never ids.
func reportRefsFromContent(content domain.ReportContent) []reportRef {
	refs := make([]reportRef, 0, 1+len(content.Cases)+len(content.Issues)+len(content.Requirements)+2)
	if content.Batch.ID != "" {
		refs = append(refs, reportRef{Kind: "batch", ID: content.Batch.ID})
	}
	for _, item := range content.Cases {
		if item.CaseRunID != "" {
			refs = append(refs, reportRef{Kind: "case_run", ID: item.CaseRunID})
		}
	}
	for _, item := range content.Issues {
		if item.IssueID != "" {
			refs = append(refs, reportRef{Kind: "issue", ID: item.IssueID})
		}
	}
	for _, item := range content.Requirements {
		if item.RequirementID != "" {
			refs = append(refs, reportRef{Kind: "requirement", ID: item.RequirementID})
		}
	}
	for _, commit := range []string{content.Batch.CodeBaseCommit, content.Batch.HeadCommit} {
		if reportCommitHex(commit) {
			refs = append(refs, reportRef{Kind: "commit", ID: commit})
		}
	}
	return refs
}

// reportCommitHex reports whether value is a lowercase 40-hex commit.
func reportCommitHex(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// reportUniqueConstraint returns the violated unique constraint or index name,
// or "" when err is not a unique violation.
func reportUniqueConstraint(err error) string {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != reportUniqueViolationCode {
		return ""
	}
	return pgErr.ConstraintName
}
