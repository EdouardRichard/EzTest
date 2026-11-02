package codeintelpg

import (
	"context"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReviewRepository stores the append-only code_fact_review rows (spec §3.4).
// It offers no update/delete method: a verdict is superseded only by a newer
// review row.
type ReviewRepository struct {
	pool *pgxpool.Pool
}

// NewReviewRepository wires the fact-review repository.
func NewReviewRepository(pool *pgxpool.Pool) *ReviewRepository {
	return &ReviewRepository{pool: pool}
}

var _ codeintelapp.ReviewRepository = (*ReviewRepository)(nil)

const reviewColumns = `id, fact_id, fact_version, repo_id, commit_sha, verdict, reason,
	reviewer_id, created_at, updated_at`

func scanReview(row rowScanner) (codeintel.FactReview, error) {
	var review codeintel.FactReview
	var verdict string
	if err := row.Scan(&review.ID, &review.FactID, &review.FactVersion, &review.RepoID,
		&review.CommitSHA, &verdict, &review.Reason, &review.ReviewerID,
		&review.CreatedAt, &review.UpdatedAt); err != nil {
		return codeintel.FactReview{}, err
	}
	review.Verdict = verdict
	return review, nil
}

// List returns the reviews of one fact in append order.
func (r *ReviewRepository) List(ctx context.Context, factID int64) ([]codeintel.FactReview, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+reviewColumns+` FROM code_fact_review
		WHERE fact_id = $1
		ORDER BY id ASC
	`, factID)
	if err != nil {
		return nil, wrapDB("list code fact reviews", err)
	}
	defer rows.Close()
	reviews := make([]codeintel.FactReview, 0)
	for rows.Next() {
		review, scanErr := scanReview(rows)
		if scanErr != nil {
			return nil, wrapDB("scan code fact review", scanErr)
		}
		reviews = append(reviews, review)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list code fact reviews", err)
	}
	return reviews, nil
}

// Create appends one review. updated_at is initialized on insert and never
// changed afterwards (append-only table).
func (r *ReviewRepository) Create(ctx context.Context, review codeintel.FactReview) (codeintel.FactReview, error) {
	created, err := scanReview(r.pool.QueryRow(ctx, `
		INSERT INTO code_fact_review (id, fact_id, fact_version, repo_id, commit_sha,
			verdict, reason, reviewer_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING `+reviewColumns,
		review.ID, review.FactID, review.FactVersion, review.RepoID, review.CommitSHA,
		review.Verdict, review.Reason, review.ReviewerID, review.CreatedAt, review.UpdatedAt))
	if err != nil {
		return codeintel.FactReview{}, wrapDB("create code fact review", err)
	}
	return created, nil
}

// LatestVerdicts returns the newest verdict per fact key for the repository
// commit. Only reviewed fact keys are returned; an absent key has no verdict.
func (r *ReviewRepository) LatestVerdicts(ctx context.Context, repoID int64, commitSHA string, factKeys []string) (map[string]string, error) {
	result := map[string]string{}
	if len(factKeys) == 0 {
		return result, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT ON (f.fact_key) f.fact_key, r.verdict
		FROM code_fact_review r
		JOIN code_fact f ON f.id = r.fact_id
		WHERE r.repo_id = $1 AND r.commit_sha = $2 AND f.fact_key = ANY($3::text[])
		ORDER BY f.fact_key, r.id DESC
	`, repoID, commitSHA, factKeys)
	if err != nil {
		return nil, wrapDB("latest code fact verdicts", err)
	}
	defer rows.Close()
	for rows.Next() {
		var factKey, verdict string
		if err := rows.Scan(&factKey, &verdict); err != nil {
			return nil, wrapDB("scan latest code fact verdict", err)
		}
		result[factKey] = verdict
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("latest code fact verdicts", err)
	}
	return result, nil
}
