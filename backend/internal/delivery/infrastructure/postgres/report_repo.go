package deliverypg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// delivery_report, delivery_report_revision and delivery_report_ref columns in
// scan order. The lists are shared by every SELECT/RETURNING of the adapter.
const reportColumns = `id, version_id, wave_id, kind, current_revision_id,
	status, input_fingerprint, created_by_run_id, created_at, updated_at`

const reportRevisionColumns = `id, report_id, revision, status, html_ref, pdf_ref,
	input_fingerprint, as_of, template_version, render_status,
	pdf_status, pdf_error, pdf_attempts, pdf_rendered_at,
	created_by_run_id, created_at, updated_at`

const reportRefColumns = `id, report_id, report_revision_id, kind, ref_id,
	created_at, updated_at`

const (
	insertReportSQL = `
		INSERT INTO delivery_report (
			id, version_id, wave_id, kind, current_revision_id, status,
			input_fingerprint, created_by_run_id, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT DO NOTHING
		RETURNING ` + reportColumns

	insertReportRevisionSQL = `
		INSERT INTO delivery_report_revision (
			id, report_id, revision, status, html_ref, pdf_ref, input_fingerprint,
			as_of, template_version, render_status, created_by_run_id, created_at,
			updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING ` + reportRevisionColumns

	insertReportRefSQL = `
		INSERT INTO delivery_report_ref (
			id, report_id, report_revision_id, kind, ref_id, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (report_revision_id, kind, ref_id) DO NOTHING`
)

// ReportRepository persists delivery_report, delivery_report_revision and
// delivery_report_ref rows. It implements deliveryapp.ReportRepository and
// owns the database-enforced invariants of the report lifecycle: the
// wave/version partial unique keys, the (report_id, revision) unique key, the
// FOR UPDATE serialization of publish finalization and the append-only
// revision/ref rows.
type ReportRepository struct {
	pool *pgxpool.Pool
	ids  platformid.Generator
}

var _ deliveryapp.ReportRepository = (*ReportRepository)(nil)

// NewReportRepository wires the adapter over a pgx pool and the process-wide
// snowflake generator. The caller MUST pass the generator seeded with this
// replica's registry-allocated node (P7): a private node-0 generator would
// collide with other replicas on the work_item IDs written by EnsureRenderJob.
// A caller-supplied positive row ID is preserved for deterministic replays.
func NewReportRepository(pool *pgxpool.Pool, ids platformid.Generator) *ReportRepository {
	if ids == nil {
		panic("deliverypg: report repository requires an id generator")
	}
	return &ReportRepository{pool: pool, ids: ids}
}

// CreateReport inserts one report header. A repeat of the wave
// (version_id, wave_id) or version (version_id) partial unique key returns the
// existing report with no error so the reconciler ensure is idempotent.
func (r *ReportRepository) CreateReport(ctx context.Context, report delivery.Report) (delivery.Report, error) {
	ctx = contextOrBackground(ctx)
	if err := validateReport(report); err != nil {
		return delivery.Report{}, err
	}
	now := nowUTC()
	createdAt := report.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	updatedAt := report.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	status := report.Status
	if status == "" {
		status = delivery.ReportStatusDraft
	}
	if !status.Valid() {
		return delivery.Report{}, delivery.ErrInvalidArgument
	}

	created, err := scanReport(r.pool.QueryRow(ctx, insertReportSQL,
		r.insertID(report.ID), report.VersionID, report.WaveID, string(report.Kind),
		report.CurrentRevisionID, string(status), report.InputFingerprint,
		report.CreatedByRunID, createdAt, updatedAt,
	))
	if isNoRows(err) {
		existing, err := r.findReportByScope(ctx, r.pool, report)
		if err != nil {
			return delivery.Report{}, err
		}
		return existing, nil
	}
	if err != nil {
		return delivery.Report{}, wrapReportDB("create report", err)
	}
	return created, nil
}

// GetReport loads one report inside its version scope.
func (r *ReportRepository) GetReport(ctx context.Context, versionID, reportID int64) (delivery.Report, error) {
	ctx = contextOrBackground(ctx)
	report, err := scanReport(r.pool.QueryRow(ctx, `
		SELECT `+reportColumns+`
		FROM delivery_report
		WHERE version_id = $1 AND id = $2
	`, versionID, reportID))
	if isNoRows(err) {
		return delivery.Report{}, deliveryapp.ErrReportNotFound
	}
	if err != nil {
		return delivery.Report{}, wrapReportDB("get report", err)
	}
	return report, nil
}

// GetReportByID loads one report regardless of version, for the report worker
// and reconciler that derive the version from the report row.
func (r *ReportRepository) GetReportByID(ctx context.Context, reportID int64) (delivery.Report, error) {
	ctx = contextOrBackground(ctx)
	report, err := scanReport(r.pool.QueryRow(ctx, `
		SELECT `+reportColumns+`
		FROM delivery_report
		WHERE id = $1
	`, reportID))
	if isNoRows(err) {
		return delivery.Report{}, deliveryapp.ErrReportNotFound
	}
	if err != nil {
		return delivery.Report{}, wrapReportDB("get report by id", err)
	}
	return report, nil
}

// ListReports returns one descending-ID page of a version's reports.
func (r *ReportRepository) ListReports(ctx context.Context, versionID int64, f deliveryapp.ReportFilter) ([]delivery.Report, error) {
	ctx = contextOrBackground(ctx)
	conditions := []string{"version_id = $1"}
	args := []any{versionID}
	if f.Kind != nil {
		if !f.Kind.Valid() {
			return nil, delivery.ErrInvalidArgument
		}
		args = append(args, string(*f.Kind))
		conditions = append(conditions, fmt.Sprintf("kind = $%d", len(args)))
	}
	if f.Before > 0 {
		args = append(args, f.Before)
		conditions = append(conditions, fmt.Sprintf("id < $%d", len(args)))
	}
	limit := effectiveLimit(f.Limit)
	args = append(args, limit)
	query := `SELECT ` + reportColumns + `
		FROM delivery_report
		WHERE ` + strings.Join(conditions, " AND ") + `
		ORDER BY id DESC
		LIMIT $` + strconv.Itoa(len(args))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, wrapReportDB("list reports", err)
	}
	defer rows.Close()

	reports := make([]delivery.Report, 0, limit)
	for rows.Next() {
		report, err := scanReport(rows)
		if err != nil {
			return nil, wrapReportDB("scan report", err)
		}
		reports = append(reports, report)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapReportDB("list reports", err)
	}
	return reports, nil
}

// NextRevision returns max(revision)+1 under a FOR UPDATE lock of the report
// row, serializing concurrent revision allocation. The row lock is held for
// one statement only; CreateRevision re-checks the unique key so two racing
// allocations cannot both publish the same revision number.
func (r *ReportRepository) NextRevision(ctx context.Context, reportID int64) (int32, error) {
	ctx = contextOrBackground(ctx)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("next report revision: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockReport(ctx, tx, reportID); err != nil {
		return 0, err
	}
	var next int32
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(revision), 0) + 1
		FROM delivery_report_revision
		WHERE report_id = $1
	`, reportID).Scan(&next); err != nil {
		return 0, wrapReportDB("next report revision", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("next report revision: commit: %w", err)
	}
	return next, nil
}

// CreateRevision inserts one draft revision. A revision <= 0 allocates the
// next number under the report row lock; an explicit revision is used as
// given. A duplicate (report_id, revision) is a state conflict.
func (r *ReportRepository) CreateRevision(ctx context.Context, revision delivery.ReportRevision) (delivery.ReportRevision, error) {
	ctx = contextOrBackground(ctx)
	if revision.ReportID <= 0 || revision.Revision < 0 {
		return delivery.ReportRevision{}, delivery.ErrInvalidArgument
	}
	fingerprint := revision.InputFingerprint
	if !validFingerprint(fingerprint) {
		return delivery.ReportRevision{}, delivery.ErrInvalidArgument
	}
	status := revision.Status
	if status == "" {
		status = delivery.ReportStatusDraft
	}
	if status != delivery.ReportStatusDraft {
		return delivery.ReportRevision{}, delivery.ErrInvalidArgument
	}
	renderStatus := revision.RenderStatus
	if renderStatus == "" {
		renderStatus = "pending"
	}
	if !validRenderStatus(renderStatus) {
		return delivery.ReportRevision{}, delivery.ErrInvalidArgument
	}
	templateVersion := revision.TemplateVersion
	if templateVersion == "" {
		templateVersion = "1"
	}
	if !validTemplateVersion(templateVersion) {
		return delivery.ReportRevision{}, delivery.ErrInvalidArgument
	}
	asOf := revision.AsOf
	if asOf.IsZero() {
		asOf = nowUTC()
	}
	now := nowUTC()
	createdAt := revision.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	updatedAt := revision.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return delivery.ReportRevision{}, fmt.Errorf("create report revision: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockReport(ctx, tx, revision.ReportID); err != nil {
		return delivery.ReportRevision{}, err
	}
	number := revision.Revision
	if number <= 0 {
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(MAX(revision), 0) + 1
			FROM delivery_report_revision
			WHERE report_id = $1
		`, revision.ReportID).Scan(&number); err != nil {
			return delivery.ReportRevision{}, wrapReportDB("create report revision: next", err)
		}
	}
	created, err := scanReportRevision(tx.QueryRow(ctx, insertReportRevisionSQL,
		r.insertID(revision.ID), revision.ReportID, number, string(status),
		revision.HTMLRef, revision.PDFRef, fingerprint, asOf, templateVersion,
		renderStatus, revision.CreatedByRunID, createdAt, updatedAt,
	))
	if err != nil {
		if isUniqueViolation(err) {
			return delivery.ReportRevision{}, delivery.ErrStateConflict
		}
		return delivery.ReportRevision{}, wrapReportDB("create report revision", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return delivery.ReportRevision{}, fmt.Errorf("create report revision: commit: %w", err)
	}
	return created, nil
}

// FinalizePublished atomically publishes one draft revision: the revision gets
// html_ref/render_status='done'/status='published' and the report points its
// current_revision_id at it with status='published' and the new fingerprint.
// Published revisions are immutable: finalizing one again is a state conflict.
// Publishing never moves the report backwards: a revision lower than the
// current or the highest published revision is a stale finalize and is
// rejected without touching either row.
func (r *ReportRepository) FinalizePublished(ctx context.Context, reportID int64, revision int32, htmlRef int64, fingerprint string) error {
	ctx = contextOrBackground(ctx)
	if reportID <= 0 || revision <= 0 || htmlRef <= 0 || !validFingerprint(fingerprint) {
		return delivery.ErrInvalidArgument
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("finalize report: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockReport(ctx, tx, reportID); err != nil {
		return err
	}
	var revisionID int64
	var revisionStatus string
	err = tx.QueryRow(ctx, `
		SELECT id, status
		FROM delivery_report_revision
		WHERE report_id = $1 AND revision = $2
		FOR UPDATE
	`, reportID, revision).Scan(&revisionID, &revisionStatus)
	if isNoRows(err) {
		return deliveryapp.ErrReportNotFound
	}
	if err != nil {
		return wrapReportDB("finalize report: lock revision", err)
	}
	if delivery.ReportStatus(revisionStatus) == delivery.ReportStatusPublished {
		return delivery.ErrStateConflict
	}
	// Publishing must never move the report backwards. The report row lock above
	// serializes this check with concurrent finalizers: a revision below the
	// current one (or below the highest published revision) is a stale finalize
	// from a slower writer and is rejected before any write.
	var currentRevision, maxPublishedRevision int32
	if err := tx.QueryRow(ctx, `
		SELECT
			COALESCE((
				SELECT cur.revision
				FROM delivery_report_revision cur
				WHERE cur.id = r.current_revision_id AND cur.report_id = r.id
			), 0),
			COALESCE((
				SELECT MAX(pub.revision)
				FROM delivery_report_revision pub
				WHERE pub.report_id = r.id AND pub.status = 'published'
			), 0)
		FROM delivery_report r
		WHERE r.id = $1
	`, reportID).Scan(&currentRevision, &maxPublishedRevision); err != nil {
		return wrapReportDB("finalize report: load revision monotonicity", err)
	}
	if revision < currentRevision || revision < maxPublishedRevision {
		return delivery.ErrStateConflict
	}
	now := nowUTC()
	if _, err := tx.Exec(ctx, `
		UPDATE delivery_report_revision
		SET status = 'published',
			render_status = 'done',
			html_ref = $3,
			input_fingerprint = $4,
			updated_at = $5
		WHERE id = $1 AND report_id = $2
	`, revisionID, reportID, htmlRef, fingerprint, now); err != nil {
		return wrapReportDB("finalize report: update revision", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE delivery_report
		SET current_revision_id = $2,
			status = 'published',
			input_fingerprint = $3,
			updated_at = $4
		WHERE id = $1
	`, reportID, revisionID, fingerprint, now); err != nil {
		return wrapReportDB("finalize report: update report", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("finalize report: commit: %w", err)
	}
	return nil
}

// MarkRenderMetadata updates the render_status of a draft revision. Published
// revisions are append-only and reject the update with ErrStateConflict; the
// schema has no error column, so errMsg only travels back to the caller in the
// task failure.
func (r *ReportRepository) MarkRenderMetadata(ctx context.Context, revisionID int64, status, errMsg string) error {
	ctx = contextOrBackground(ctx)
	_ = errMsg
	if revisionID <= 0 || !validRenderStatus(status) {
		return delivery.ErrInvalidArgument
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE delivery_report_revision
		SET render_status = $2,
			updated_at = $3
		WHERE id = $1 AND status <> 'published'
	`, revisionID, status, nowUTC())
	if err != nil {
		return wrapReportDB("mark report render metadata", err)
	}
	if tag.RowsAffected() == 0 {
		var revisionStatus string
		err := r.pool.QueryRow(ctx, `SELECT status FROM delivery_report_revision WHERE id = $1`, revisionID).Scan(&revisionStatus)
		if isNoRows(err) {
			return deliveryapp.ErrReportNotFound
		}
		if err != nil {
			return wrapReportDB("mark report render metadata: load revision", err)
		}
		return delivery.ErrStateConflict
	}
	return nil
}

// MarkStale marks a published report stale. It reports whether this call
// changed the row, so the service publishes report.stale exactly once. A draft
// report is not stale-marked and an already-stale report reports false.
func (r *ReportRepository) MarkStale(ctx context.Context, reportID int64) (bool, error) {
	ctx = contextOrBackground(ctx)
	tag, err := r.pool.Exec(ctx, `
		UPDATE delivery_report
		SET status = 'stale',
			updated_at = $2
		WHERE id = $1 AND status = 'published'
	`, reportID, nowUTC())
	if err != nil {
		return false, wrapReportDB("mark report stale", err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	var status string
	err = r.pool.QueryRow(ctx, `SELECT status FROM delivery_report WHERE id = $1`, reportID).Scan(&status)
	if isNoRows(err) {
		return false, deliveryapp.ErrReportNotFound
	}
	if err != nil {
		return false, wrapReportDB("mark report stale: load report", err)
	}
	return false, nil
}

// GetRevision loads one numbered revision of a report.
func (r *ReportRepository) GetRevision(ctx context.Context, reportID int64, revision int32) (delivery.ReportRevision, error) {
	ctx = contextOrBackground(ctx)
	row, err := scanReportRevision(r.pool.QueryRow(ctx, `
		SELECT `+reportRevisionColumns+`
		FROM delivery_report_revision
		WHERE report_id = $1 AND revision = $2
	`, reportID, revision))
	if isNoRows(err) {
		return delivery.ReportRevision{}, deliveryapp.ErrReportNotFound
	}
	if err != nil {
		return delivery.ReportRevision{}, wrapReportDB("get report revision", err)
	}
	return row, nil
}

// ListRevisions returns every revision of a report, newest first.
func (r *ReportRepository) ListRevisions(ctx context.Context, reportID int64) ([]delivery.ReportRevision, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+reportRevisionColumns+`
		FROM delivery_report_revision
		WHERE report_id = $1
		ORDER BY revision DESC
	`, reportID)
	if err != nil {
		return nil, wrapReportDB("list report revisions", err)
	}
	defer rows.Close()

	revisions := make([]delivery.ReportRevision, 0)
	for rows.Next() {
		revision, err := scanReportRevision(rows)
		if err != nil {
			return nil, wrapReportDB("scan report revision", err)
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapReportDB("list report revisions", err)
	}
	return revisions, nil
}

/* ------------------------- P7 report PDF pipeline ------------------------- */

// ListRenderableRevisions returns the published revisions whose PDF still needs
// work: status published, render_status done, an html_ref, pdf_status
// pending/failed, no pdf_ref yet, and (for failed rows) attempts left under
// report.render_max_attempts. Exhausted failed rows are excluded in SQL so they
// cannot consume the bounded LIMIT and starve newer pending revisions.
// Oldest revision first. The existing partial index
// idx_delivery_report_revision_pdf_pending covers the status filter and the
// primary key covers the ordered scan, so no new migration is needed.
func (r *ReportRepository) ListRenderableRevisions(ctx context.Context, limit, maxAttempts int) ([]delivery.RenderableRevision, error) {
	ctx = contextOrBackground(ctx)
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if maxAttempts < deliveryapp.MinRenderAttempts || maxAttempts > deliveryapp.MaxRenderAttempts {
		maxAttempts = deliveryapp.DefaultRenderMaxAttempts
	}
	rows, err := r.pool.Query(ctx, `
		SELECT rev.id, rev.report_id, report.version_id, rev.revision,
		       rev.pdf_status, rev.pdf_attempts
		FROM delivery_report_revision rev
		JOIN delivery_report report ON report.id = rev.report_id
		WHERE rev.status = 'published'
		  AND rev.render_status = 'done'
		  AND rev.html_ref IS NOT NULL
		  AND rev.pdf_status IN ('pending', 'failed')
		  AND rev.pdf_ref IS NULL
		  AND (rev.pdf_status = 'pending' OR rev.pdf_attempts < $2)
		ORDER BY rev.id
		LIMIT $1
	`, limit, maxAttempts)
	if err != nil {
		return nil, wrapReportDB("list renderable report revisions", err)
	}
	defer rows.Close()

	revisions := make([]delivery.RenderableRevision, 0, limit)
	for rows.Next() {
		var (
			revision delivery.RenderableRevision
			status   string
		)
		if err := rows.Scan(&revision.RevisionID, &revision.ReportID, &revision.VersionID,
			&revision.Revision, &status, &revision.PDFAttempts); err != nil {
			return nil, wrapReportDB("scan renderable report revision", err)
		}
		revision.PDFStatus = delivery.PDFStatus(status)
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapReportDB("list renderable report revisions", err)
	}
	return revisions, nil
}

// MarkPDFRunning moves one published revision to running and increments
// pdf_attempts. It accepts pending/failed (first attempt) and running (a retry
// after a transient failure whose lease expired); the work item lease/fencing
// is what prevents two live workers from rendering the same revision.
func (r *ReportRepository) MarkPDFRunning(ctx context.Context, revisionID int64) error {
	ctx = contextOrBackground(ctx)
	if revisionID <= 0 {
		return delivery.ErrInvalidArgument
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE delivery_report_revision
		SET pdf_status = 'running',
			pdf_attempts = pdf_attempts + 1,
			pdf_error = NULL,
			updated_at = $2
		WHERE id = $1
		  AND status = 'published'
		  AND render_status = 'done'
		  AND pdf_status IN ('pending', 'failed', 'running')
		  AND pdf_ref IS NULL
	`, revisionID, nowUTC())
	if err != nil {
		return wrapReportDB("mark report PDF running", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	return r.markPDFConflict(ctx, revisionID)
}

// MarkPDFRendered writes the ledger ref of a successful render. It is fenced on
// pdf_status='running', an empty pdf_ref and an attempt no older than the
// current pdf_attempts; a stale worker whose lease was taken over can therefore
// never settle a newer attempt. as_of/html_ref/input_fingerprint/
// template_version are never part of this UPDATE.
func (r *ReportRepository) MarkPDFRendered(ctx context.Context, revisionID int64, ref deliveryapp.ReportContentRef, attempts int32, at time.Time) error {
	ctx = contextOrBackground(ctx)
	if revisionID <= 0 || ref.RefID <= 0 {
		return delivery.ErrInvalidArgument
	}
	if attempts < 0 {
		attempts = 0
	}
	if at.IsZero() {
		at = nowUTC()
	}
	at = at.UTC()
	tag, err := r.pool.Exec(ctx, `
		UPDATE delivery_report_revision
		SET pdf_status = 'done',
			pdf_ref = $2,
			pdf_error = NULL,
			pdf_rendered_at = $3,
			updated_at = $3
		WHERE id = $1
		  AND status = 'published'
		  AND pdf_status = 'running'
		  AND pdf_ref IS NULL
		  AND pdf_attempts <= $4
	`, revisionID, ref.RefID, at, attempts)
	if err != nil {
		return wrapReportDB("mark report PDF rendered", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	return r.markPDFConflict(ctx, revisionID)
}

// MarkPDFFailed records a terminal PDF failure. The message is clamped to the
// delivery_0004 CHECK length; the revision is never failed after it is done and
// the write is fenced on the worker's attempt, so a stale worker cannot fail a
// revision that a newer attempt is rendering.
func (r *ReportRepository) MarkPDFFailed(ctx context.Context, revisionID int64, message string, attempts int32, at time.Time) error {
	ctx = contextOrBackground(ctx)
	if revisionID <= 0 {
		return delivery.ErrInvalidArgument
	}
	if at.IsZero() {
		at = nowUTC()
	}
	at = at.UTC()
	if attempts < 0 {
		attempts = 0
	}
	message = clampPDFError(message)
	tag, err := r.pool.Exec(ctx, `
		UPDATE delivery_report_revision
		SET pdf_status = 'failed',
			pdf_error = NULLIF($2, ''),
			pdf_attempts = GREATEST(pdf_attempts, $3),
			updated_at = $4
		WHERE id = $1
		  AND status = 'published'
		  AND pdf_status IN ('pending', 'failed', 'running')
		  AND pdf_ref IS NULL
		  AND pdf_attempts <= $3
	`, revisionID, message, attempts, at)
	if err != nil {
		return wrapReportDB("mark report PDF failed", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	return r.markPDFConflict(ctx, revisionID)
}

// MarkPDFDisabled moves a pending revision to disabled when report.render_pdf
// is off. Repeating it on an already-disabled revision is a no-op.
func (r *ReportRepository) MarkPDFDisabled(ctx context.Context, revisionID int64) error {
	ctx = contextOrBackground(ctx)
	if revisionID <= 0 {
		return delivery.ErrInvalidArgument
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE delivery_report_revision
		SET pdf_status = 'disabled',
			updated_at = $2
		WHERE id = $1 AND pdf_status = 'pending'
	`, revisionID, nowUTC())
	if err != nil {
		return wrapReportDB("mark report PDF disabled", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	status, err := r.pdfStatus(ctx, revisionID)
	if err != nil {
		return err
	}
	if status == delivery.PDFStatusDisabled {
		return nil
	}
	return delivery.ErrStateConflict
}

// GetRevisionForRender loads one revision row for the PDF worker by its ID.
func (r *ReportRepository) GetRevisionForRender(ctx context.Context, revisionID int64) (delivery.ReportRevision, error) {
	ctx = contextOrBackground(ctx)
	if revisionID <= 0 {
		return delivery.ReportRevision{}, delivery.ErrInvalidArgument
	}
	revision, err := scanReportRevision(r.pool.QueryRow(ctx, `
		SELECT `+reportRevisionColumns+`
		FROM delivery_report_revision
		WHERE id = $1
	`, revisionID))
	if isNoRows(err) {
		return delivery.ReportRevision{}, deliveryapp.ErrReportNotFound
	}
	if err != nil {
		return delivery.ReportRevision{}, wrapReportDB("get report revision for render", err)
	}
	return revision, nil
}

// EnsureRenderJob inserts the idempotent report.render_pdf work item. A repeat
// of (version_id, type, dedupe_key) leaves the existing row untouched, so a
// dead work item is never silently revived and a pending one is never
// duplicated.
func (r *ReportRepository) EnsureRenderJob(ctx context.Context, revisionID, reportID, versionID int64, dedupeKey string, maxAttempts int) error {
	ctx = contextOrBackground(ctx)
	dedupeKey = strings.TrimSpace(dedupeKey)
	if revisionID <= 0 || reportID <= 0 || versionID <= 0 || dedupeKey == "" {
		return delivery.ErrInvalidArgument
	}
	if maxAttempts < 1 {
		maxAttempts = deliveryapp.DefaultRenderMaxAttempts
	}
	if maxAttempts > 20 {
		maxAttempts = 20
	}
	payload, err := json.Marshal(map[string]any{
		"report_id":          platformid.Format(reportID),
		"report_revision_id": platformid.Format(revisionID),
	})
	if err != nil {
		return fmt.Errorf("encode report.render_pdf payload: %w", err)
	}
	// team=ops: deterministic worker task, never claimed by the agent worker
	// (which claims only the five product teams); RenderWorker claims by type.
	// The work_item IDs are application-generated; the report repository owns a
	// snowflake source for exactly this kind of composition-root write.
	_, err = r.pool.Exec(ctx, `
		INSERT INTO work_item (id, version_id, team, type, status, priority,
			dedupe_key, payload, max_attempts, available_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'open', 0,
			$5, $6::jsonb, $7, now(), now(), now())
		ON CONFLICT (version_id, type, dedupe_key) DO NOTHING
	`, r.ids.Next(), versionID, string(deliveryapp.RenderTaskTeam), deliveryapp.RenderTaskType,
		dedupeKey, string(payload), maxAttempts)
	if err != nil {
		return fmt.Errorf("ensure report.render_pdf work item: %w", err)
	}
	return nil
}

// markPDFConflict classifies a zero-row PDF transition: a missing revision is
// ErrReportNotFound, any other state is the fenced ErrStateConflict.
func (r *ReportRepository) markPDFConflict(ctx context.Context, revisionID int64) error {
	if _, err := r.pdfStatus(ctx, revisionID); err != nil {
		return err
	}
	return delivery.ErrStateConflict
}

// pdfStatus loads the current pdf_status of one revision.
func (r *ReportRepository) pdfStatus(ctx context.Context, revisionID int64) (delivery.PDFStatus, error) {
	var status string
	err := r.pool.QueryRow(ctx, `
		SELECT pdf_status FROM delivery_report_revision WHERE id = $1
	`, revisionID).Scan(&status)
	if isNoRows(err) {
		return "", deliveryapp.ErrReportNotFound
	}
	if err != nil {
		return "", wrapReportDB("load report PDF status", err)
	}
	return delivery.PDFStatus(status), nil
}

// clampPDFError keeps pdf_error within the delivery_0004 CHECK (<= 2000 runes).
func clampPDFError(message string) string {
	message = strings.TrimSpace(message)
	runes := []rune(message)
	if len(runes) > 2000 {
		return string(runes[:2000])
	}
	return message
}

// InsertRefs appends the logical refs of one revision. Re-inserting the same
// (revision, kind, ref) triple is idempotent; refs are append-only and their
// report_id is derived from the revision row.
func (r *ReportRepository) InsertRefs(ctx context.Context, revisionID int64, refs []delivery.ReportRef) error {
	ctx = contextOrBackground(ctx)
	if revisionID <= 0 {
		return delivery.ErrInvalidArgument
	}
	for _, ref := range refs {
		if !validReportRefKind(ref.Kind) || ref.RefID <= 0 {
			return delivery.ErrInvalidArgument
		}
	}
	if len(refs) == 0 {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("insert report refs: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var reportID int64
	err = tx.QueryRow(ctx, `
		SELECT report_id FROM delivery_report_revision WHERE id = $1
	`, revisionID).Scan(&reportID)
	if isNoRows(err) {
		return deliveryapp.ErrReportNotFound
	}
	if err != nil {
		return wrapReportDB("insert report refs: load revision", err)
	}
	now := nowUTC()
	for _, ref := range refs {
		createdAt := ref.CreatedAt
		if createdAt.IsZero() {
			createdAt = now
		}
		updatedAt := ref.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = createdAt
		}
		if _, err := tx.Exec(ctx, insertReportRefSQL,
			r.insertID(ref.ID), reportID, revisionID, ref.Kind, ref.RefID,
			createdAt, updatedAt,
		); err != nil {
			if isUniqueViolation(err) {
				return delivery.ErrStateConflict
			}
			return wrapReportDB("insert report ref", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("insert report refs: commit: %w", err)
	}
	return nil
}

// ListRefs returns the append-only refs of one report in insertion order.
func (r *ReportRepository) ListRefs(ctx context.Context, reportID int64) ([]delivery.ReportRef, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+reportRefColumns+`
		FROM delivery_report_ref
		WHERE report_id = $1
		ORDER BY id
	`, reportID)
	if err != nil {
		return nil, wrapReportDB("list report refs", err)
	}
	defer rows.Close()

	refs := make([]delivery.ReportRef, 0)
	for rows.Next() {
		ref, err := scanReportRef(rows)
		if err != nil {
			return nil, wrapReportDB("scan report ref", err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapReportDB("list report refs", err)
	}
	return refs, nil
}

// SetWaveReportID backfills run_wave.report_id for a wave report. The same
// value is idempotent; a different report on the same wave is a state
// conflict. A missing wave is ErrReportNotFound.
func (r *ReportRepository) SetWaveReportID(ctx context.Context, versionID, waveID, reportID int64) error {
	ctx = contextOrBackground(ctx)
	if versionID <= 0 || waveID <= 0 || reportID <= 0 {
		return delivery.ErrInvalidArgument
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE run_wave
		SET report_id = $3,
			updated_at = $4
		WHERE version_id = $1 AND id = $2
		  AND (report_id IS NULL OR report_id = $3)
	`, versionID, waveID, reportID, nowUTC())
	if err != nil {
		return wrapReportDB("set wave report id", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var current *int64
	err = r.pool.QueryRow(ctx, `
		SELECT report_id FROM run_wave WHERE version_id = $1 AND id = $2
	`, versionID, waveID).Scan(&current)
	if isNoRows(err) {
		return deliveryapp.ErrReportNotFound
	}
	if err != nil {
		return wrapReportDB("set wave report id: load wave", err)
	}
	if current != nil && *current == reportID {
		return nil
	}
	return delivery.ErrStateConflict
}

// insertID preserves an explicit positive ID and otherwise draws a snowflake.
func (r *ReportRepository) insertID(explicit int64) int64 {
	if explicit > 0 {
		return explicit
	}
	return r.ids.Next()
}

// findReportByScope loads the existing report matched by the conflicting
// partial unique key (wave: version+wave, version: version+kind).
func (r *ReportRepository) findReportByScope(ctx context.Context, exec interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, report delivery.Report) (delivery.Report, error) {
	var (
		found delivery.Report
		err   error
	)
	if report.Kind == delivery.ReportKindWave {
		found, err = scanReport(exec.QueryRow(ctx, `
			SELECT `+reportColumns+`
			FROM delivery_report
			WHERE version_id = $1 AND wave_id = $2 AND kind = 'wave'
		`, report.VersionID, *report.WaveID))
	} else {
		found, err = scanReport(exec.QueryRow(ctx, `
			SELECT `+reportColumns+`
			FROM delivery_report
			WHERE version_id = $1 AND kind = 'version'
		`, report.VersionID))
	}
	if isNoRows(err) {
		return delivery.Report{}, deliveryapp.ErrReportNotFound
	}
	if err != nil {
		return delivery.Report{}, wrapReportDB("load existing report", err)
	}
	return found, nil
}

// lockReport locks one delivery_report row FOR UPDATE inside tx.
func lockReport(ctx context.Context, tx pgx.Tx, reportID int64) error {
	var id int64
	err := tx.QueryRow(ctx, `SELECT id FROM delivery_report WHERE id = $1 FOR UPDATE`, reportID).Scan(&id)
	if isNoRows(err) {
		return deliveryapp.ErrReportNotFound
	}
	if err != nil {
		return wrapReportDB("lock report", err)
	}
	return nil
}

// validateReport rejects report headers that violate the schema shape.
func validateReport(report delivery.Report) error {
	if report.VersionID <= 0 || !report.Kind.Valid() {
		return delivery.ErrInvalidArgument
	}
	switch report.Kind {
	case delivery.ReportKindWave:
		if report.WaveID == nil || *report.WaveID <= 0 {
			return delivery.ErrInvalidArgument
		}
	case delivery.ReportKindVersion:
		if report.WaveID != nil {
			return delivery.ErrInvalidArgument
		}
	}
	return nil
}

// validFingerprint reports a lowercase hex sha256 as required by the
// delivery_report_revision input_fingerprint CHECK.
func validFingerprint(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// validRenderStatus reports a persisted render_status value.
func validRenderStatus(value string) bool {
	switch value {
	case "pending", "running", "done", "failed":
		return true
	default:
		return false
	}
}

// validTemplateVersion reports a built-in report template version: "1" (frozen
// P4 layout) and "2" (P7 layout with the optional appendix).
func validTemplateVersion(value string) bool {
	switch value {
	case "1", "2":
		return true
	default:
		return false
	}
}

// validReportRefKind reports a report_ref.kind allowed by the schema.
func validReportRefKind(value string) bool {
	switch value {
	case "requirement", "scenario", "case_version", "wave", "case_run", "issue", "version_sut":
		return true
	default:
		return false
	}
}

// wrapReportDB adds operation context; report-specific not-found cases are
// mapped by each method because a generic pgx.ErrNoRows can mean either the
// report or the revision.
func wrapReportDB(op string, err error) error {
	if err == nil {
		return nil
	}
	if isNoRows(err) {
		return fmt.Errorf("%s: %w", op, deliveryapp.ErrReportNotFound)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// isUniqueViolation reports a PostgreSQL unique-constraint violation.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// scanReport maps one delivery_report row.
func scanReport(row rowScanner) (delivery.Report, error) {
	var (
		report delivery.Report
		kind   string
		status string
	)
	if err := row.Scan(
		&report.ID, &report.VersionID, &report.WaveID, &kind,
		&report.CurrentRevisionID, &status, &report.InputFingerprint,
		&report.CreatedByRunID, &report.CreatedAt, &report.UpdatedAt,
	); err != nil {
		return delivery.Report{}, err
	}
	report.Kind = delivery.ReportKind(kind)
	report.Status = delivery.ReportStatus(status)
	return report, nil
}

// scanReportRevision maps one delivery_report_revision row.
func scanReportRevision(row rowScanner) (delivery.ReportRevision, error) {
	var (
		revision delivery.ReportRevision
		status   string
		pdfState string
		pdfError *string
	)
	if err := row.Scan(
		&revision.ID, &revision.ReportID, &revision.Revision, &status,
		&revision.HTMLRef, &revision.PDFRef, &revision.InputFingerprint,
		&revision.AsOf, &revision.TemplateVersion, &revision.RenderStatus,
		&pdfState, &pdfError, &revision.PDFAttempts, &revision.PDFRenderedAt,
		&revision.CreatedByRunID, &revision.CreatedAt, &revision.UpdatedAt,
	); err != nil {
		return delivery.ReportRevision{}, err
	}
	revision.Status = delivery.ReportStatus(status)
	revision.PDFStatus = delivery.PDFStatus(pdfState)
	if pdfError != nil {
		revision.PDFError = *pdfError
	}
	return revision, nil
}

// scanReportRef maps one delivery_report_ref row.
func scanReportRef(row rowScanner) (delivery.ReportRef, error) {
	var ref delivery.ReportRef
	if err := row.Scan(
		&ref.ID, &ref.ReportID, &ref.ReportRevisionID, &ref.Kind, &ref.RefID,
		&ref.CreatedAt, &ref.UpdatedAt,
	); err != nil {
		return delivery.ReportRef{}, err
	}
	return ref, nil
}
