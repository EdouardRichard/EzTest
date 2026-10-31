package deliverypg

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

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
	input_fingerprint, as_of, template_version, render_status, created_by_run_id,
	created_at, updated_at`

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

// NewReportRepository wraps a pgx pool. The adapter owns the snowflake source
// of inserted rows; a caller-supplied positive ID is preserved for
// deterministic replays.
func NewReportRepository(pool *pgxpool.Pool) *ReportRepository {
	ids, err := platformid.NewSnowflake(0)
	if err != nil {
		// Node 0 is always valid; this only guards a future change of the
		// platform generator contract.
		panic(fmt.Sprintf("deliverypg: init snowflake generator: %v", err))
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
	if templateVersion != "1" {
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
	)
	if err := row.Scan(
		&revision.ID, &revision.ReportID, &revision.Revision, &status,
		&revision.HTMLRef, &revision.PDFRef, &revision.InputFingerprint,
		&revision.AsOf, &revision.TemplateVersion, &revision.RenderStatus,
		&revision.CreatedByRunID, &revision.CreatedAt, &revision.UpdatedAt,
	); err != nil {
		return delivery.ReportRevision{}, err
	}
	revision.Status = delivery.ReportStatus(status)
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
