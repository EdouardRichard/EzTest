package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// issueColumnList is the shared column order of every issue query and the order
// scanIssue expects.
const issueColumnList = `id, version_id, batch_id, case_run_id, case_node_id, step_result_id,
	source, category, severity, status, confidence, failure_class, dedupe_key,
	title, description, repro_steps, expected, actual, rerun_suggestion, external_ref,
	recurrence_count, revision, dismissed_reason, created_by, updated_by,
	confirmed_by, confirmed_at, dismissed_by, dismissed_at, created_at, updated_at`

// issueDefaultPageSize mirrors platform.DefaultLimit without coupling the
// repository to the HTTP pagination helpers.
const issueDefaultPageSize = 20

// issueUniqueViolationCode is PostgreSQL's SQLSTATE for unique_violation.
const issueUniqueViolationCode = "23505"

// issueQuerier is the subset of pgxpool.Pool and pgx.Tx the issue repository
// uses. Names are issue-prefixed so the shared delivery postgres package can
// host other repositories without identifier clashes.
type issueQuerier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// issueRowScanner is the subset of pgx.Row and pgx.Rows used to scan one row.
type issueRowScanner interface {
	Scan(dest ...any) error
}

// IssueRepository implements application.IssueStore on a pgx querier. Every id
// is application-generated (D29); created_at/updated_at are persisted from the
// entity the service built so a unit-tested clock stays authoritative.
type IssueRepository struct {
	q issueQuerier
}

// NewIssueRepository returns an issue repository backed by pool.
func NewIssueRepository(pool *pgxpool.Pool) application.IssueStore {
	return &IssueRepository{q: pool}
}

var _ application.IssueStore = (*IssueRepository)(nil)

// InTx runs fn inside one transaction. A repository already bound to a
// transaction joins it instead of opening a nested one.
func (r *IssueRepository) InTx(ctx context.Context, fn func(application.IssueStore) error) error {
	return r.inTx(ctx, func(q issueQuerier) error {
		return fn(&IssueRepository{q: q})
	})
}

// Create inserts one issue and its refs atomically.
func (r *IssueRepository) Create(ctx context.Context, issue domain.Issue, refs []domain.IssueRef) (domain.Issue, error) {
	err := r.inTx(ctx, func(q issueQuerier) error {
		if _, err := q.Exec(ctx, `INSERT INTO issue (
			id, version_id, batch_id, case_run_id, case_node_id, step_result_id,
			source, category, severity, status, confidence, failure_class, dedupe_key,
			title, description, repro_steps, expected, actual, rerun_suggestion, external_ref,
			recurrence_count, revision, dismissed_reason, created_by, updated_by,
			confirmed_by, confirmed_at, dismissed_by, dismissed_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
				$16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31)`,
			issueArgs(issue)...); err != nil {
			if issueUniqueConstraint(err) == "uk_issue_version_dedupe" {
				// Internal signal, not an HTTP 409: the service retries outside
				// this failed transaction and merges into the now-visible issue
				// (spec §6.9).
				return application.ErrIssueDedupeConflict
			}
			return fmt.Errorf("postgres: create issue %d: %w", issue.ID, err)
		}
		return issueInsertRefs(ctx, q, issue.ID, refs)
	})
	if err != nil {
		return domain.Issue{}, err
	}
	return issue, nil
}

// Get returns one issue of versionID. A missing or foreign row is
// (zero, false, nil).
func (r *IssueRepository) Get(ctx context.Context, versionID, issueID int64) (domain.Issue, bool, error) {
	issue, err := scanIssue(r.q.QueryRow(ctx,
		`SELECT `+issueColumnList+` FROM issue WHERE version_id = $1 AND id = $2`,
		versionID, issueID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Issue{}, false, nil
		}
		return domain.Issue{}, false, fmt.Errorf("postgres: get issue %d: %w", issueID, err)
	}
	return issue, true, nil
}

// FindByDedupeKey returns the issue with the same (version_id, dedupe_key).
func (r *IssueRepository) FindByDedupeKey(ctx context.Context, versionID int64, dedupeKey string) (domain.Issue, bool, error) {
	if dedupeKey == "" {
		return domain.Issue{}, false, nil
	}
	issue, err := scanIssue(r.q.QueryRow(ctx,
		`SELECT `+issueColumnList+` FROM issue WHERE version_id = $1 AND dedupe_key = $2`,
		versionID, dedupeKey))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Issue{}, false, nil
		}
		return domain.Issue{}, false, fmt.Errorf("postgres: find issue by dedupe key: %w", err)
	}
	return issue, true, nil
}

// Update applies the revision CAS. A stale revision is issue_state_conflict; a
// missing or foreign row is issue_not_found.
func (r *IssueRepository) Update(ctx context.Context, issue domain.Issue, expectedRevision int) (domain.Issue, error) {
	args := append(issueArgs(issue), expectedRevision)
	updated, err := scanIssue(r.q.QueryRow(ctx, `UPDATE issue SET
		batch_id = $3, case_run_id = $4, case_node_id = $5, step_result_id = $6,
		source = $7, category = $8, severity = $9, status = $10, confidence = $11,
		failure_class = $12, dedupe_key = $13, title = $14, description = $15,
		repro_steps = $16, expected = $17, actual = $18, rerun_suggestion = $19,
		external_ref = $20, recurrence_count = $21, revision = $22, dismissed_reason = $23,
		created_by = $24,
		updated_by = $25, confirmed_by = $26, confirmed_at = $27, dismissed_by = $28,
		dismissed_at = $29, created_at = $30, updated_at = $31
		WHERE id = $1 AND version_id = $2 AND revision = $32
		RETURNING `+issueColumnList, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Issue{}, r.issueWriteConflict(ctx, issue)
		}
		return domain.Issue{}, fmt.Errorf("postgres: update issue %d: %w", issue.ID, err)
	}
	return updated, nil
}

// issueWriteConflict distinguishes a missing row from a stale revision after a
// CAS write returned no row.
func (r *IssueRepository) issueWriteConflict(ctx context.Context, issue domain.Issue) error {
	var existing int64
	if err := r.q.QueryRow(ctx,
		`SELECT id FROM issue WHERE version_id = $1 AND id = $2`,
		issue.VersionID, issue.ID).Scan(&existing); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NewNotFound("issue", issue.ID)
		}
		return fmt.Errorf("postgres: re-read issue %d after CAS: %w", issue.ID, err)
	}
	return domain.NewStateConflict(domain.ReasonIssueStateConflict,
		fmt.Sprintf("issue %d revision does not match", issue.ID))
}

// AddRefs inserts refs idempotently: an existing (issue_id, ref_kind, ref_id)
// row is left untouched.
func (r *IssueRepository) AddRefs(ctx context.Context, issueID int64, refs []domain.IssueRef) error {
	return issueInsertRefs(ctx, r.q, issueID, refs)
}

// ListRefs returns every ref of one issue ordered by kind then id.
func (r *IssueRepository) ListRefs(ctx context.Context, issueID int64) ([]domain.IssueRef, error) {
	rows, err := r.q.Query(ctx,
		`SELECT issue_id, ref_kind, ref_id FROM issue_ref
		 WHERE issue_id = $1
		 ORDER BY ref_kind, ref_id`, issueID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list issue refs: %w", err)
	}
	defer rows.Close()

	refs := make([]domain.IssueRef, 0)
	for rows.Next() {
		var ref domain.IssueRef
		if err := rows.Scan(&ref.IssueID, &ref.RefKind, &ref.RefID); err != nil {
			return nil, fmt.Errorf("postgres: scan issue ref: %w", err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list issue refs: %w", err)
	}
	return refs, nil
}

// List returns one keyset page of the version's issues ordered by severity
// weight then id descending (spec §7.3).
func (r *IssueRepository) List(ctx context.Context, versionID int64, filter application.IssueFilter) ([]domain.Issue, bool, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = issueDefaultPageSize
	}
	rows, err := r.q.Query(ctx, `SELECT `+issueColumnList+` FROM issue
		WHERE version_id = $1
		  AND ($2 = '' OR status = $2)
		  AND ($3 = '' OR severity = $3)
		  AND ($4 = '' OR category = $4)
		  AND ($5::bigint IS NULL OR batch_id = $5)
		  AND ($6::bigint IS NULL OR case_run_id = $6)
		  AND ($7::bigint IS NULL OR case_node_id = $7)
		  AND ($8 = '' OR title ILIKE '%' || $8 || '%')
		  AND (
		    $9::bigint = 0
		    OR (
		      (CASE severity
		         WHEN 'blocker' THEN 0 WHEN 'major' THEN 1
		         WHEN 'minor' THEN 2 ELSE 3 END)
		      > (SELECT CASE severity
		                   WHEN 'blocker' THEN 0 WHEN 'major' THEN 1
		                   WHEN 'minor' THEN 2 ELSE 3 END
		         FROM issue WHERE id = $9 AND version_id = $1)
		      OR (
		        (CASE severity
		           WHEN 'blocker' THEN 0 WHEN 'major' THEN 1
		           WHEN 'minor' THEN 2 ELSE 3 END)
		        = (SELECT CASE severity
		                     WHEN 'blocker' THEN 0 WHEN 'major' THEN 1
		                     WHEN 'minor' THEN 2 ELSE 3 END
		           FROM issue WHERE id = $9 AND version_id = $1)
		        AND id < $9
		      )
		    )
		  )
		ORDER BY CASE severity
		           WHEN 'blocker' THEN 0
		           WHEN 'major' THEN 1
		           WHEN 'minor' THEN 2
		           ELSE 3
		         END,
		         id DESC
		LIMIT $10`,
		versionID, filter.Status, filter.Severity, filter.Category,
		issueNullableInt(filter.BatchID), issueNullableInt(filter.CaseRunID),
		issueNullableInt(filter.CaseNodeID), filter.Query, filter.Cursor, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list issues: %w", err)
	}
	defer rows.Close()

	issues := make([]domain.Issue, 0, limit)
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			return nil, false, fmt.Errorf("postgres: scan issue: %w", err)
		}
		issues = append(issues, issue)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("postgres: list issues: %w", err)
	}
	if len(issues) > limit {
		return issues[:limit], true, nil
	}
	return issues, false, nil
}

// issueInsertRefs inserts every ref of one issue idempotently.
func issueInsertRefs(ctx context.Context, q issueQuerier, issueID int64, refs []domain.IssueRef) error {
	for _, ref := range refs {
		if _, err := q.Exec(ctx,
			`INSERT INTO issue_ref (issue_id, ref_kind, ref_id)
			 VALUES ($1, $2, $3)
			 ON CONFLICT (issue_id, ref_kind, ref_id) DO NOTHING`,
			issueID, ref.RefKind, ref.RefID); err != nil {
			return fmt.Errorf("postgres: insert issue ref %s:%s: %w", ref.RefKind, ref.RefID, err)
		}
	}
	return nil
}

// inTx runs fn on q, opening a transaction only when the repository is bound to
// a pool. A transaction-bound repository joins the caller's transaction.
func (r *IssueRepository) inTx(ctx context.Context, fn func(issueQuerier) error) error {
	if _, ok := r.q.(pgx.Tx); ok {
		return fn(r.q)
	}
	pool, ok := r.q.(*pgxpool.Pool)
	if !ok {
		return fn(r.q)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin issue transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.WithoutCancel(ctx))
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit issue transaction: %w", err)
	}
	return nil
}

// issueArgs renders the 31 persisted issue columns in issueColumnList order.
func issueArgs(issue domain.Issue) []any {
	return []any{
		issue.ID,
		issue.VersionID,
		issueNullableInt(issue.BatchID),
		issueNullableInt(issue.CaseRunID),
		issueNullableInt(issue.CaseNodeID),
		issueNullableInt(issue.StepResultID),
		issue.Source,
		issue.Category,
		issue.Severity,
		issue.Status,
		issue.Confidence,
		issueNullableString(issue.FailureClass),
		issueNullableString(issue.DedupeKey),
		issue.Title,
		issue.Description,
		issue.ReproSteps,
		issue.Expected,
		issue.Actual,
		issue.RerunSuggestion,
		issue.ExternalRef,
		issue.RecurrenceCount,
		issue.Revision,
		issue.DismissedReason,
		issue.CreatedBy,
		issue.UpdatedBy,
		issueNullableInt(issue.ConfirmedBy),
		issueNullableTime(issue.ConfirmedAt),
		issueNullableInt(issue.DismissedBy),
		issueNullableTime(issue.DismissedAt),
		issue.CreatedAt,
		issue.UpdatedAt,
	}
}

// scanIssue scans one row in issueColumnList order.
func scanIssue(row issueRowScanner) (domain.Issue, error) {
	var (
		issue        domain.Issue
		batchID      *int64
		caseRunID    *int64
		caseNodeID   *int64
		stepResultID *int64
		confirmedBy  *int64
		dismissedBy  *int64
		confirmedAt  *time.Time
		dismissedAt  *time.Time
		failureClass *string
		dedupeKey    *string
	)
	if err := row.Scan(
		&issue.ID,
		&issue.VersionID,
		&batchID,
		&caseRunID,
		&caseNodeID,
		&stepResultID,
		&issue.Source,
		&issue.Category,
		&issue.Severity,
		&issue.Status,
		&issue.Confidence,
		&failureClass,
		&dedupeKey,
		&issue.Title,
		&issue.Description,
		&issue.ReproSteps,
		&issue.Expected,
		&issue.Actual,
		&issue.RerunSuggestion,
		&issue.ExternalRef,
		&issue.RecurrenceCount,
		&issue.Revision,
		&issue.DismissedReason,
		&issue.CreatedBy,
		&issue.UpdatedBy,
		&confirmedBy,
		&confirmedAt,
		&dismissedBy,
		&dismissedAt,
		&issue.CreatedAt,
		&issue.UpdatedAt,
	); err != nil {
		return domain.Issue{}, err
	}
	issue.BatchID = batchID
	issue.CaseRunID = caseRunID
	issue.CaseNodeID = caseNodeID
	issue.StepResultID = stepResultID
	issue.ConfirmedBy = confirmedBy
	issue.DismissedBy = dismissedBy
	issue.ConfirmedAt = confirmedAt
	issue.DismissedAt = dismissedAt
	if failureClass != nil {
		issue.FailureClass = *failureClass
	}
	if dedupeKey != nil {
		issue.DedupeKey = *dedupeKey
	}
	return issue, nil
}

// issueNullableInt renders an optional bigint binding column; nil becomes SQL
// NULL.
func issueNullableInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

// issueNullableString renders an optional text binding column; an empty string
// becomes SQL NULL because issue.failure_class/dedupe_key use NULL, not ”.
func issueNullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// issueNullableTime renders an optional timestamptz binding column.
func issueNullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}

// issueUniqueConstraint returns the violated unique constraint name, or "" when
// err is not a unique violation.
func issueUniqueConstraint(err error) string {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != issueUniqueViolationCode {
		return ""
	}
	return pgErr.ConstraintName
}
