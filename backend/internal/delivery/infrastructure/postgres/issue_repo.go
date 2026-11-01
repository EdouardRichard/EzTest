// Package deliverypg is the PostgreSQL adapter for the P4 delivery bounded
// context. It implements the application repository ports with explicit SQL and
// database-enforced invariants: the candidate dedupe unique key, the
// expected_revision CAS of issue transitions, and append-only refs/history.
//
// The delivery context assumes a single API instance (like P3 execution); the
// repositories still enforce the row-level guards (unique keys, FOR UPDATE
// serialization, ON CONFLICT DO NOTHING) so retries and duplicate events cannot
// double-write the lifecycle rows.
package deliverypg

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/jackc/pgx/v5/pgxpool"
)

// delivery_issue columns in scan order. The list is shared by every SELECT,
// INSERT ... RETURNING and UPDATE ... RETURNING of the adapter.
const issueColumns = `id, version_id, title, summary, status, category, severity,
	dedupe_key, fingerprint, source, expected_revision, merged_into_id, merged_at,
	merged_by, reopen_count, recurrence_count, last_recurred_at,
	created_by_run_id, created_at, updated_at`

const issueRefColumns = `id, issue_id, kind, ref_id, version_sut_id, role, created_at, updated_at`

const issueHistoryColumns = `id, issue_id, from_status, to_status, reason, actor_id,
	actor_kind, created_at, updated_at`

// issueReasonRecurrence is the status-history reason of a recurrence redirect
// (spec §4.6): the target status is unchanged (from=to), the row records the
// redirect.
const issueReasonRecurrence = "recurrence"

const (
	insertIssueSQL = `
		INSERT INTO delivery_issue (
			id, version_id, title, summary, status, category, severity, dedupe_key,
			fingerprint, source, expected_revision, created_by_run_id, created_at,
			updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (version_id, dedupe_key) DO NOTHING
		RETURNING ` + issueColumns

	selectIssueByDedupeSQL = `
		SELECT ` + issueColumns + `
		FROM delivery_issue
		WHERE version_id = $1 AND dedupe_key = $2`

	insertIssueRefSQL = `
		INSERT INTO delivery_issue_ref (
			id, issue_id, kind, ref_id, version_sut_id, role, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (issue_id, kind, ref_id, role) DO NOTHING`

	insertIssueHistorySQL = `
		INSERT INTO delivery_issue_status_history (
			id, issue_id, from_status, to_status, reason, actor_id, actor_kind,
			created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
)

// IssueRepository persists delivery_issue, delivery_issue_ref and
// delivery_issue_status_history rows. It implements
// deliveryapp.IssueRepository.
type IssueRepository struct {
	pool *pgxpool.Pool
	ids  platformid.Generator
}

var _ deliveryapp.IssueRepository = (*IssueRepository)(nil)

// NewIssueRepository wraps a pgx pool. The adapter owns the snowflake source of
// the rows it inserts (the candidate domain constructor leaves IDs unset); a
// caller-supplied positive ID is preserved for deterministic replays.
func NewIssueRepository(pool *pgxpool.Pool) *IssueRepository {
	ids, err := platformid.NewSnowflake(0)
	if err != nil {
		// Node 0 is always valid; this only guards a future change of the
		// platform generator contract.
		panic(fmt.Sprintf("deliverypg: init snowflake generator: %v", err))
	}
	return &IssueRepository{pool: pool, ids: ids}
}

// CreateCandidate inserts the issue and supplements its refs in one
// transaction. A repeat of (version_id, dedupe_key) returns the existing issue
// with created=false; refs are still appended (ON CONFLICT DO NOTHING) so a
// deduplicated failure from a new case_run/wave becomes resolvable by
// FindByCaseRun. The creation history row is written exactly once.
//
// A dedupe hit whose stored issue is merged defers its refs to the application
// recurrence redirect (spec §4.6): the source issue keeps its historical refs
// and the target receives them with role='recurrence' (or the source receives
// them through AppendRefs when the chain is broken). The caller therefore sees
// created=false with a merged row and owns the next write.
func (r *IssueRepository) CreateCandidate(ctx context.Context, issue delivery.Issue, refs []delivery.IssueRef) (delivery.Issue, bool, error) {
	ctx = contextOrBackground(ctx)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return delivery.Issue{}, false, fmt.Errorf("create issue candidate: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	createdAt := issue.CreatedAt
	if createdAt.IsZero() {
		createdAt = nowUTC()
	}
	updatedAt := issue.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	status := issue.Status
	if status == "" {
		status = delivery.IssueStatusOpen
	}
	source := issue.Source
	if source == "" {
		source = delivery.IssueCandidateSource
	}
	expectedRevision := issue.ExpectedRevision
	if expectedRevision <= 0 {
		expectedRevision = 1
	}

	created, err := scanIssue(tx.QueryRow(ctx, insertIssueSQL,
		r.insertID(issue.ID), issue.VersionID, issue.Title, issue.Summary,
		string(status), issue.Category, issue.Severity, issue.DedupeKey,
		issue.Fingerprint, source, expectedRevision, issue.CreatedByRunID,
		createdAt, updatedAt,
	))
	if isNoRows(err) {
		existing, err := scanIssue(tx.QueryRow(ctx, selectIssueByDedupeSQL, issue.VersionID, issue.DedupeKey))
		if err != nil {
			return delivery.Issue{}, false, wrapDB("load deduplicated issue", err)
		}
		if existing.Status != delivery.IssueStatusMerged {
			if err := r.insertRefs(ctx, tx, existing.ID, refs); err != nil {
				return delivery.Issue{}, false, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return delivery.Issue{}, false, fmt.Errorf("create issue candidate: commit: %w", err)
		}
		return existing, false, nil
	}
	if err != nil {
		return delivery.Issue{}, false, wrapDB("create issue candidate", err)
	}

	if err := r.insertRefs(ctx, tx, created.ID, refs); err != nil {
		return delivery.Issue{}, false, err
	}
	if err := r.insertHistory(ctx, tx, delivery.IssueStatusChange{
		ID:        r.ids.Next(),
		IssueID:   created.ID,
		To:        created.Status,
		ActorKind: "system",
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}); err != nil {
		return delivery.Issue{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return delivery.Issue{}, false, fmt.Errorf("create issue candidate: commit: %w", err)
	}
	return created, true, nil
}

// Get loads one issue inside its version scope.
func (r *IssueRepository) Get(ctx context.Context, versionID, issueID int64) (delivery.Issue, error) {
	ctx = contextOrBackground(ctx)
	issue, err := scanIssue(r.pool.QueryRow(ctx, `
		SELECT `+issueColumns+`
		FROM delivery_issue
		WHERE version_id = $1 AND id = $2
	`, versionID, issueID))
	if err != nil {
		return delivery.Issue{}, wrapDB("get issue", err)
	}
	return issue, nil
}

// GetByID loads one issue regardless of version, for tools and workers that
// resolve an issue ID without the request's version scope.
func (r *IssueRepository) GetByID(ctx context.Context, issueID int64) (delivery.Issue, error) {
	ctx = contextOrBackground(ctx)
	issue, err := scanIssue(r.pool.QueryRow(ctx, `
		SELECT `+issueColumns+`
		FROM delivery_issue
		WHERE id = $1
	`, issueID))
	if err != nil {
		return delivery.Issue{}, wrapDB("get issue by id", err)
	}
	return issue, nil
}

// List returns one descending-ID page of a version's issues. Status, category,
// severity and the refs' version SUT filter when set; Before is the exclusive
// ID cursor of the previous page.
func (r *IssueRepository) List(ctx context.Context, versionID int64, f deliveryapp.IssueFilter) ([]delivery.Issue, error) {
	ctx = contextOrBackground(ctx)
	conditions := []string{"version_id = $1"}
	args := []any{versionID}
	if f.Status != "" {
		args = append(args, f.Status)
		conditions = append(conditions, fmt.Sprintf("status = $%d", len(args)))
	}
	if f.Category != "" {
		args = append(args, f.Category)
		conditions = append(conditions, fmt.Sprintf("category = $%d", len(args)))
	}
	if f.Severity != "" {
		args = append(args, f.Severity)
		conditions = append(conditions, fmt.Sprintf("severity = $%d", len(args)))
	}
	if f.VersionSUTID != nil {
		args = append(args, *f.VersionSUTID)
		conditions = append(conditions, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM delivery_issue_ref r
			WHERE r.issue_id = delivery_issue.id AND r.version_sut_id = $%d
		)`, len(args)))
	}
	if f.Before > 0 {
		args = append(args, f.Before)
		conditions = append(conditions, fmt.Sprintf("id < $%d", len(args)))
	}
	limit := effectiveLimit(f.Limit)
	args = append(args, limit)
	query := `SELECT ` + issueColumns + `
		FROM delivery_issue
		WHERE ` + strings.Join(conditions, " AND ") + `
		ORDER BY id DESC
		LIMIT $` + strconv.Itoa(len(args))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, wrapDB("list issues", err)
	}
	defer rows.Close()

	issues := make([]delivery.Issue, 0, limit)
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			return nil, wrapDB("scan issue", err)
		}
		issues = append(issues, issue)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list issues", err)
	}
	return issues, nil
}

// UpdateStatus applies one status transition with an optional expected_revision
// CAS. The issue row is locked first and the domain state machine is re-applied
// under that lock, so a concurrent writer cannot bypass the lifecycle rules; the
// history row and the revision bump commit atomically with the status.
// confirmed/dismissed -> open increments reopen_count; merged is terminal.
func (r *IssueRepository) UpdateStatus(ctx context.Context, u deliveryapp.IssueStatusUpdate) (delivery.Issue, error) {
	ctx = contextOrBackground(ctx)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return delivery.Issue{}, fmt.Errorf("update issue status: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var currentStatus string
	var currentRevision int32
	err = tx.QueryRow(ctx, `
		SELECT status, expected_revision
		FROM delivery_issue
		WHERE id = $1 AND version_id = $2
		FOR UPDATE
	`, u.IssueID, u.VersionID).Scan(&currentStatus, &currentRevision)
	if isNoRows(err) {
		return delivery.Issue{}, ErrNotFound
	}
	if err != nil {
		return delivery.Issue{}, wrapDB("update issue status: lock issue", err)
	}
	from := delivery.IssueStatus(currentStatus)
	if !delivery.CanTransitionIssue(from, u.To) {
		// Re-applied after the row lock: the caller's pre-check can be stale
		// when two transitions race, and merged must stay terminal.
		return delivery.Issue{}, delivery.ErrStateConflict
	}
	if u.ExpectedRevision != nil && currentRevision != *u.ExpectedRevision {
		return delivery.Issue{}, delivery.ErrStateConflict
	}

	now := nowUTC()
	updated, err := scanIssue(tx.QueryRow(ctx, `
		UPDATE delivery_issue
		SET status = $3,
			expected_revision = expected_revision + 1,
			reopen_count = reopen_count + CASE
				WHEN $3 = 'open' AND status IN ('confirmed', 'dismissed') THEN 1
				ELSE 0
			END,
			updated_at = $4
		WHERE id = $1 AND version_id = $2
		RETURNING `+issueColumns,
		u.IssueID, u.VersionID, string(u.To), now,
	))
	if err != nil {
		return delivery.Issue{}, wrapDB("update issue status", err)
	}
	if err := r.insertHistory(ctx, tx, delivery.IssueStatusChange{
		ID:        r.ids.Next(),
		IssueID:   u.IssueID,
		From:      &from,
		To:        u.To,
		Reason:    u.Reason,
		ActorID:   actorIDPtr(u.ActorID),
		ActorKind: actorKind(u.ActorKind, "user"),
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		return delivery.Issue{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return delivery.Issue{}, fmt.Errorf("update issue status: commit: %w", err)
	}
	return updated, nil
}

// Merge marks the source issue merged into the target issue of the same version
// and appends the merge history in one transaction. The target must exist, be
// in the source version and not be merged; a merged source is terminal. An
// optional expected_revision CAS guards the source row.
func (r *IssueRepository) Merge(ctx context.Context, m deliveryapp.IssueMerge) (delivery.Issue, error) {
	ctx = contextOrBackground(ctx)
	if m.SourceID == m.TargetID {
		return delivery.Issue{}, delivery.ErrStateConflict
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return delivery.Issue{}, fmt.Errorf("merge issue: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var sourceStatus string
	var sourceRevision int32
	err = tx.QueryRow(ctx, `
		SELECT status, expected_revision
		FROM delivery_issue
		WHERE id = $1 AND version_id = $2
		FOR UPDATE
	`, m.SourceID, m.VersionID).Scan(&sourceStatus, &sourceRevision)
	if isNoRows(err) {
		return delivery.Issue{}, ErrNotFound
	}
	if err != nil {
		return delivery.Issue{}, wrapDB("merge issue: lock source", err)
	}
	if delivery.IssueStatus(sourceStatus) == delivery.IssueStatusMerged {
		return delivery.Issue{}, delivery.ErrStateConflict
	}
	if m.ExpectedRevision != nil && sourceRevision != *m.ExpectedRevision {
		return delivery.Issue{}, delivery.ErrStateConflict
	}

	// Lock the target before validating it so a concurrent merge of the target
	// cannot turn it into a merged (invalid) target mid-transaction.
	var targetVersionID int64
	var targetStatus string
	err = tx.QueryRow(ctx, `
		SELECT version_id, status
		FROM delivery_issue
		WHERE id = $1
		FOR SHARE
	`, m.TargetID).Scan(&targetVersionID, &targetStatus)
	if isNoRows(err) {
		return delivery.Issue{}, delivery.ErrStateConflict
	}
	if err != nil {
		return delivery.Issue{}, wrapDB("merge issue: lock target", err)
	}
	if targetVersionID != m.VersionID || delivery.IssueStatus(targetStatus) == delivery.IssueStatusMerged {
		return delivery.Issue{}, delivery.ErrStateConflict
	}

	now := nowUTC()
	merged, err := scanIssue(tx.QueryRow(ctx, `
		UPDATE delivery_issue
		SET status = 'merged',
			merged_into_id = $3,
			merged_at = $4,
			merged_by = $5,
			expected_revision = expected_revision + 1,
			updated_at = $4
		WHERE id = $1 AND version_id = $2 AND status <> 'merged'
		RETURNING `+issueColumns,
		m.SourceID, m.VersionID, m.TargetID, now, actorIDPtr(m.ActorID),
	))
	if err != nil {
		return delivery.Issue{}, wrapDB("merge issue", err)
	}
	from := delivery.IssueStatus(sourceStatus)
	if err := r.insertHistory(ctx, tx, delivery.IssueStatusChange{
		ID:        r.ids.Next(),
		IssueID:   m.SourceID,
		From:      &from,
		To:        delivery.IssueStatusMerged,
		Reason:    m.Reason,
		ActorID:   actorIDPtr(m.ActorID),
		ActorKind: "user",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		return delivery.Issue{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return delivery.Issue{}, fmt.Errorf("merge issue: commit: %w", err)
	}
	return merged, nil
}

// ApplyRecurrence writes one recurrence redirection in one transaction
// (spec §4.6): the target row is locked and must still be non-merged, its
// recurrence_count is incremented and last_recurred_at stamped, the candidate
// refs are appended with role='recurrence' (ON CONFLICT DO NOTHING), and one
// history row records the unchanged target status with reason='recurrence' and
// actor_kind='system'. The source issue is never written.
func (r *IssueRepository) ApplyRecurrence(ctx context.Context, rec deliveryapp.IssueRecurrence) (delivery.Issue, error) {
	ctx = contextOrBackground(ctx)
	if rec.VersionID <= 0 || rec.TargetID <= 0 {
		return delivery.Issue{}, delivery.ErrInvalidArgument
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return delivery.Issue{}, fmt.Errorf("apply issue recurrence: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	err = tx.QueryRow(ctx, `
		SELECT status
		FROM delivery_issue
		WHERE id = $1 AND version_id = $2
		FOR UPDATE
	`, rec.TargetID, rec.VersionID).Scan(&status)
	if isNoRows(err) {
		return delivery.Issue{}, ErrNotFound
	}
	if err != nil {
		return delivery.Issue{}, wrapDB("apply issue recurrence: lock target", err)
	}
	targetStatus := delivery.IssueStatus(status)
	if !targetStatus.Valid() || targetStatus == delivery.IssueStatusMerged {
		// A concurrent merge turned the resolved target into a merged issue;
		// the service falls back to the source behavior without losing refs.
		return delivery.Issue{}, delivery.ErrStateConflict
	}

	now := nowUTC()
	updated, err := scanIssue(tx.QueryRow(ctx, `
		UPDATE delivery_issue
		SET recurrence_count = recurrence_count + 1,
			last_recurred_at = GREATEST(COALESCE(last_recurred_at, $2), $2),
			updated_at = $2
		WHERE id = $1 AND version_id = $3 AND status <> 'merged'
		RETURNING `+issueColumns,
		rec.TargetID, now, rec.VersionID,
	))
	if isNoRows(err) {
		return delivery.Issue{}, delivery.ErrStateConflict
	}
	if err != nil {
		return delivery.Issue{}, wrapDB("apply issue recurrence: update target", err)
	}
	if err := r.insertRefsWithRole(ctx, tx, rec.TargetID, rec.Refs, delivery.IssueRefRoleRecurrence); err != nil {
		return delivery.Issue{}, err
	}
	from := targetStatus
	if err := r.insertHistory(ctx, tx, delivery.IssueStatusChange{
		ID:        r.ids.Next(),
		IssueID:   rec.TargetID,
		From:      &from,
		To:        targetStatus,
		Reason:    issueReasonRecurrence,
		ActorKind: "system",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		return delivery.Issue{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return delivery.Issue{}, fmt.Errorf("apply issue recurrence: commit: %w", err)
	}
	return updated, nil
}

// AppendRefs appends the logical refs of one issue idempotently. It is the
// pre-P7 dedupe fallback used when a merged issue's recurrence chain cannot be
// redirected: the candidate refs land on the source so no data is lost.
func (r *IssueRepository) AppendRefs(ctx context.Context, issueID int64, refs []delivery.IssueRef) error {
	ctx = contextOrBackground(ctx)
	if issueID <= 0 {
		return delivery.ErrInvalidArgument
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("append issue refs: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM delivery_issue WHERE id = $1`, issueID).Scan(&exists); isNoRows(err) {
		return ErrNotFound
	} else if err != nil {
		return wrapDB("append issue refs: check issue", err)
	}
	if err := r.insertRefs(ctx, tx, issueID, refs); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("append issue refs: commit: %w", err)
	}
	return nil
}

// AppendHistory appends one explicit status history row. History and refs are
// append-only: updated_at is initialized once and never updated.
func (r *IssueRepository) AppendHistory(ctx context.Context, h delivery.IssueStatusChange) error {
	ctx = contextOrBackground(ctx)
	createdAt := h.CreatedAt
	if createdAt.IsZero() {
		createdAt = nowUTC()
	}
	updatedAt := h.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	_, err := r.pool.Exec(ctx, insertIssueHistorySQL,
		r.insertID(h.ID), h.IssueID, statusValue(h.From), string(h.To), h.Reason,
		h.ActorID, actorKind(h.ActorKind, "system"), createdAt, updatedAt,
	)
	return wrapDB("append issue history", err)
}

// ListRefs returns the append-only logical refs of one issue in insertion
// order.
func (r *IssueRepository) ListRefs(ctx context.Context, issueID int64) ([]delivery.IssueRef, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+issueRefColumns+`
		FROM delivery_issue_ref
		WHERE issue_id = $1
		ORDER BY id
	`, issueID)
	if err != nil {
		return nil, wrapDB("list issue refs", err)
	}
	defer rows.Close()

	refs := make([]delivery.IssueRef, 0)
	for rows.Next() {
		ref, err := scanIssueRef(rows)
		if err != nil {
			return nil, wrapDB("scan issue ref", err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list issue refs", err)
	}
	return refs, nil
}

// ListHistory returns the append-only status history of one issue in insertion
// order (the first row is the creation entry with from_status NULL).
func (r *IssueRepository) ListHistory(ctx context.Context, issueID int64) ([]delivery.IssueStatusChange, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+issueHistoryColumns+`
		FROM delivery_issue_status_history
		WHERE issue_id = $1
		ORDER BY id
	`, issueID)
	if err != nil {
		return nil, wrapDB("list issue history", err)
	}
	defer rows.Close()

	history := make([]delivery.IssueStatusChange, 0)
	for rows.Next() {
		change, err := scanIssueHistory(rows)
		if err != nil {
			return nil, wrapDB("scan issue history", err)
		}
		history = append(history, change)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list issue history", err)
	}
	return history, nil
}

// FindByCaseRun resolves the issue that already references one case_run. The
// reconciler uses it to skip failures that were ingested in an earlier scan,
// including deduplicated failures from a later wave.
func (r *IssueRepository) FindByCaseRun(ctx context.Context, versionID, caseRunID int64) (delivery.Issue, bool, error) {
	ctx = contextOrBackground(ctx)
	issue, err := scanIssue(r.pool.QueryRow(ctx, `
		SELECT `+issueColumns+`
		FROM delivery_issue
		WHERE version_id = $1 AND EXISTS (
			SELECT 1 FROM delivery_issue_ref r
			WHERE r.issue_id = delivery_issue.id AND r.kind = $3 AND r.ref_id = $2
		)
		ORDER BY id
		LIMIT 1
	`, versionID, caseRunID, delivery.IssueRefKindCaseRun))
	if isNoRows(err) {
		return delivery.Issue{}, false, nil
	}
	if err != nil {
		return delivery.Issue{}, false, wrapDB("find issue by case run", err)
	}
	return issue, true, nil
}

// insertID preserves an explicit positive ID and otherwise draws a snowflake.
func (r *IssueRepository) insertID(explicit int64) int64 {
	if explicit > 0 {
		return explicit
	}
	return r.ids.Next()
}

// insertRefs supplements the logical refs of one issue. The unique
// (issue_id, kind, ref_id, role) constraint makes the write idempotent.
func (r *IssueRepository) insertRefs(ctx context.Context, exec execer, issueID int64, refs []delivery.IssueRef) error {
	return r.insertRefsWithRole(ctx, exec, issueID, refs, "")
}

// insertRefsWithRole appends logical refs, optionally forcing one role (the
// recurrence redirect always writes role='recurrence'). Without an override an
// empty ref role defaults to role='source'.
func (r *IssueRepository) insertRefsWithRole(ctx context.Context, exec execer, issueID int64, refs []delivery.IssueRef, overrideRole string) error {
	now := nowUTC()
	for _, ref := range refs {
		role := ref.Role
		if overrideRole != "" {
			role = overrideRole
		}
		if role == "" {
			role = delivery.IssueRefRoleSource
		}
		createdAt := ref.CreatedAt
		if createdAt.IsZero() {
			createdAt = now
		}
		updatedAt := ref.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = createdAt
		}
		if _, err := exec.Exec(ctx, insertIssueRefSQL,
			r.insertID(ref.ID), issueID, ref.Kind, ref.RefID, ref.VersionSUTID,
			role, createdAt, updatedAt,
		); err != nil {
			return wrapDB("insert issue ref", err)
		}
	}
	return nil
}

// insertHistory appends one status history row inside a transaction.
func (r *IssueRepository) insertHistory(ctx context.Context, exec execer, change delivery.IssueStatusChange) error {
	if _, err := exec.Exec(ctx, insertIssueHistorySQL,
		r.insertID(change.ID), change.IssueID, statusValue(change.From),
		string(change.To), change.Reason, change.ActorID,
		actorKind(change.ActorKind, "system"), change.CreatedAt, change.UpdatedAt,
	); err != nil {
		return wrapDB("insert issue history", err)
	}
	return nil
}

// scanIssue maps one delivery_issue row.
func scanIssue(row rowScanner) (delivery.Issue, error) {
	var (
		issue  delivery.Issue
		status string
	)
	if err := row.Scan(
		&issue.ID, &issue.VersionID, &issue.Title, &issue.Summary, &status,
		&issue.Category, &issue.Severity, &issue.DedupeKey, &issue.Fingerprint,
		&issue.Source, &issue.ExpectedRevision, &issue.MergedIntoID,
		&issue.MergedAt, &issue.MergedBy, &issue.ReopenCount,
		&issue.RecurrenceCount, &issue.LastRecurredAt,
		&issue.CreatedByRunID, &issue.CreatedAt, &issue.UpdatedAt,
	); err != nil {
		return delivery.Issue{}, err
	}
	issue.Status = delivery.IssueStatus(status)
	return issue, nil
}

// scanIssueRef maps one delivery_issue_ref row.
func scanIssueRef(row rowScanner) (delivery.IssueRef, error) {
	var ref delivery.IssueRef
	if err := row.Scan(&ref.ID, &ref.IssueID, &ref.Kind, &ref.RefID,
		&ref.VersionSUTID, &ref.Role, &ref.CreatedAt, &ref.UpdatedAt); err != nil {
		return delivery.IssueRef{}, err
	}
	return ref, nil
}

// scanIssueHistory maps one delivery_issue_status_history row.
func scanIssueHistory(row rowScanner) (delivery.IssueStatusChange, error) {
	var (
		change     delivery.IssueStatusChange
		fromStatus *string
		toStatus   string
	)
	if err := row.Scan(&change.ID, &change.IssueID, &fromStatus, &toStatus,
		&change.Reason, &change.ActorID, &change.ActorKind,
		&change.CreatedAt, &change.UpdatedAt); err != nil {
		return delivery.IssueStatusChange{}, err
	}
	if fromStatus != nil {
		from := delivery.IssueStatus(*fromStatus)
		change.From = &from
	}
	change.To = delivery.IssueStatus(toStatus)
	return change, nil
}
