package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// reviewColumnList is the shared column order of every case_review query and
// the order scanReview expects.
const reviewColumnList = `id, version_id, run_id, status, comment, reviewer_id,
	decided_by, decided_at, created_at, updated_at`

// ReviewRepository implements application.ReviewRepository on a pgx querier.
type ReviewRepository struct {
	q querier
}

// NewReviewRepository returns a case-review repository backed by pool.
func NewReviewRepository(pool *pgxpool.Pool) application.ReviewRepository {
	return newReviewRepository(pool)
}

// newReviewRepository binds the repository to q.
func newReviewRepository(q querier) *ReviewRepository {
	return &ReviewRepository{q: q}
}

var _ application.ReviewRepository = (*ReviewRepository)(nil)

// reviewNotFound is the version-scoped not-found of the latest/approved
// lookups: the review of a version is addressed by the version, not by a
// review id.
func reviewNotFound(versionID int64) error {
	return &domain.Error{
		Kind:    domain.KindNotFound,
		Reason:  "review_not_found",
		Message: fmt.Sprintf("version %d has no review", versionID),
	}
}

// scanReview scans one row in reviewColumnList order.
func scanReview(row rowScanner) (domain.CaseReview, error) {
	var review domain.CaseReview
	if err := row.Scan(
		&review.ID,
		&review.VersionID,
		&review.RunID,
		&review.Status,
		&review.Comment,
		&review.ReviewerID,
		&review.DecidedBy,
		&review.DecidedAt,
		&review.CreatedAt,
		&review.UpdatedAt,
	); err != nil {
		return domain.CaseReview{}, err
	}
	return review, nil
}

// Create inserts r with its explicit id. A second approved review of the same
// version violates uk_case_review_approved and is a baseline_frozen conflict
// (spec §6.3/§6.4).
func (r *ReviewRepository) Create(ctx context.Context, review domain.CaseReview) (domain.CaseReview, error) {
	const query = `INSERT INTO case_review
		(id, version_id, run_id, status, comment, reviewer_id, decided_by,
		 decided_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING ` + reviewColumnList
	created, err := scanReview(r.q.QueryRow(ctx, query,
		review.ID, review.VersionID, nullableInt(review.RunID), review.Status,
		review.Comment, review.ReviewerID, nullableInt(review.DecidedBy),
		nullableTime(review.DecidedAt), review.CreatedAt, review.UpdatedAt,
	))
	if err != nil {
		switch uniqueConstraint(err) {
		case "pk_case_review":
			return domain.CaseReview{}, domain.NewConflict("duplicate_key",
				fmt.Sprintf("review %d already exists", review.ID))
		case "uk_case_review_approved":
			return domain.CaseReview{}, domain.NewConflict("baseline_frozen",
				fmt.Sprintf("version %d already has an approved review", review.VersionID))
		}
		return domain.CaseReview{}, fmt.Errorf("postgres: create case review: %w", err)
	}
	return created, nil
}

// GetLatest returns the newest review round of the version.
func (r *ReviewRepository) GetLatest(ctx context.Context, versionID int64) (domain.CaseReview, error) {
	review, err := scanReview(r.q.QueryRow(ctx,
		`SELECT `+reviewColumnList+`
		 FROM case_review
		 WHERE version_id = $1
		 ORDER BY created_at DESC, id DESC
		 LIMIT 1`, versionID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CaseReview{}, reviewNotFound(versionID)
		}
		return domain.CaseReview{}, fmt.Errorf("postgres: get latest case review: %w", err)
	}
	return review, nil
}

// GetApproved returns the approved review of the version, if any.
func (r *ReviewRepository) GetApproved(ctx context.Context, versionID int64) (domain.CaseReview, error) {
	review, err := scanReview(r.q.QueryRow(ctx,
		`SELECT `+reviewColumnList+`
		 FROM case_review
		 WHERE version_id = $1 AND status = 'approved'`, versionID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CaseReview{}, reviewNotFound(versionID)
		}
		return domain.CaseReview{}, fmt.Errorf("postgres: get approved case review: %w", err)
	}
	return review, nil
}

// Decide records the human decision on one draft review. A missing review is
// review_not_found; a review that is already approved is baseline_frozen (its
// approval froze the version); any other already-decided review is a conflict.
func (r *ReviewRepository) Decide(ctx context.Context, reviewID int64, status string, comment string, actor int64) (domain.CaseReview, error) {
	const query = `UPDATE case_review
		SET status = $2, comment = $3, decided_by = $4, decided_at = now(),
		    updated_at = now()
		WHERE id = $1 AND status = 'draft'
		RETURNING ` + reviewColumnList
	decided, err := scanReview(r.q.QueryRow(ctx, query, reviewID, status, comment, actor))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CaseReview{}, r.decideConflict(ctx, reviewID)
		}
		if uniqueConstraint(err) == "uk_case_review_approved" {
			return domain.CaseReview{}, domain.NewConflict("baseline_frozen",
				fmt.Sprintf("review %d cannot be approved: the version already has an approved review", reviewID))
		}
		return domain.CaseReview{}, fmt.Errorf("postgres: decide case review: %w", err)
	}
	return decided, nil
}

// decideConflict classifies a zero-row decision CAS: a missing row is
// review_not_found, an already-approved row is baseline_frozen (approving it
// froze the version), and any other already-decided row stays a generic
// conflict.
func (r *ReviewRepository) decideConflict(ctx context.Context, reviewID int64) error {
	var status string
	if err := r.q.QueryRow(ctx, `SELECT status FROM case_review WHERE id = $1`, reviewID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("review", reviewID)
		}
		return fmt.Errorf("postgres: read case review status after decision CAS: %w", err)
	}
	if status == domain.ReviewApproved {
		return domain.NewConflict("baseline_frozen",
			fmt.Sprintf("review %d is already approved", reviewID))
	}
	return domain.NewConflict("conflict",
		fmt.Sprintf("review %d is already %s", reviewID, status))
}

// List returns one page ordered by created_at DESC, id DESC with an opaque
// keyset cursor. A cursor whose row has been deleted (or belongs to another
// version) yields an empty page, matching the version lists.
func (r *ReviewRepository) List(ctx context.Context, versionID int64, limit int, cursor string) ([]domain.CaseReview, string, bool, error) {
	if limit <= 0 {
		return nil, "", false, fmt.Errorf("postgres: list case reviews: limit must be positive")
	}
	cursorID, err := platform.DecodeCursor(cursor)
	if err != nil {
		return nil, "", false, fmt.Errorf("postgres: list case reviews: %w", err)
	}
	var (
		cursorCreated time.Time
		hasCursor     bool
	)
	if cursorID != 0 {
		if err := r.q.QueryRow(ctx,
			`SELECT created_at FROM case_review WHERE id = $1 AND version_id = $2`,
			cursorID, versionID).Scan(&cursorCreated); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return []domain.CaseReview{}, "", false, nil
			}
			return nil, "", false, fmt.Errorf("postgres: list case reviews: read cursor: %w", err)
		}
		hasCursor = true
	}

	const query = `SELECT ` + reviewColumnList + `
		FROM case_review
		WHERE version_id = $1
		  AND ($2::boolean = false OR (created_at, id) < ($3::timestamptz, $4::bigint))
		ORDER BY created_at DESC, id DESC
		LIMIT $5`
	rows, err := r.q.Query(ctx, query, versionID, hasCursor, cursorCreated, cursorID, limit+1)
	if err != nil {
		return nil, "", false, fmt.Errorf("postgres: list case reviews: %w", err)
	}
	defer rows.Close()

	reviews := make([]domain.CaseReview, 0, limit+1)
	for rows.Next() {
		review, err := scanReview(rows)
		if err != nil {
			return nil, "", false, fmt.Errorf("postgres: scan case review: %w", err)
		}
		reviews = append(reviews, review)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, fmt.Errorf("postgres: list case reviews: %w", err)
	}

	more := len(reviews) > limit
	nextCursor := ""
	if more {
		reviews = reviews[:limit]
		nextCursor = platform.EncodeCursor(reviews[len(reviews)-1].ID)
	}
	return reviews, nextCursor, more, nil
}
