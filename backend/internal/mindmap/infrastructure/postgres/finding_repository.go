package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// findingColumnList is the shared column order of every case_review_finding
// query and the order scanFinding expects.
const findingColumnList = `id, review_id, node_id, category, severity, detail,
	status, source, created_by, created_at, updated_at`

// FindingRepository implements application.FindingRepository on a pgx querier.
type FindingRepository struct {
	q querier
}

// NewFindingRepository returns a review-finding repository backed by pool.
func NewFindingRepository(pool *pgxpool.Pool) application.FindingRepository {
	return newFindingRepository(pool)
}

// newFindingRepository binds the repository to q.
func newFindingRepository(q querier) *FindingRepository {
	return &FindingRepository{q: q}
}

var _ application.FindingRepository = (*FindingRepository)(nil)

// scanFinding scans one row in findingColumnList order.
func scanFinding(row rowScanner) (domain.CaseReviewFinding, error) {
	var finding domain.CaseReviewFinding
	if err := row.Scan(
		&finding.ID,
		&finding.ReviewID,
		&finding.NodeID,
		&finding.Category,
		&finding.Severity,
		&finding.Detail,
		&finding.Status,
		&finding.Source,
		&finding.CreatedBy,
		&finding.CreatedAt,
		&finding.UpdatedAt,
	); err != nil {
		return domain.CaseReviewFinding{}, err
	}
	return finding, nil
}

// CreateBatch inserts the findings of one review round in a single round trip.
// The whole batch runs inside the caller's transaction when the repository is
// bound by GraphTx, so a failure inserts none of them.
func (r *FindingRepository) CreateBatch(ctx context.Context, findings []domain.CaseReviewFinding) error {
	if len(findings) == 0 {
		return nil
	}
	const query = `INSERT INTO case_review_finding
		(id, review_id, node_id, category, severity, detail, status, source,
		 created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
	batch := &pgx.Batch{}
	for _, finding := range findings {
		batch.Queue(query,
			finding.ID, finding.ReviewID, nullableInt(finding.NodeID),
			finding.Category, finding.Severity, finding.Detail, finding.Status,
			finding.Source, finding.CreatedBy, finding.CreatedAt, finding.UpdatedAt)
	}
	results := r.q.SendBatch(ctx, batch)
	defer results.Close()
	for _, finding := range findings {
		if _, err := results.Exec(); err != nil {
			if uniqueConstraint(err) == "pk_case_review_finding" {
				return domain.NewConflict("duplicate_key",
					fmt.Sprintf("finding %d already exists", finding.ID))
			}
			return fmt.Errorf("postgres: create case review finding %d: %w", finding.ID, err)
		}
	}
	if err := results.Close(); err != nil {
		return fmt.Errorf("postgres: create case review findings: %w", err)
	}
	return nil
}

// List returns the findings of the version, newest id last. Empty status and
// severity match everything; a nil nodeID matches every finding.
func (r *FindingRepository) List(ctx context.Context, versionID int64, status, severity string, nodeID *int64) ([]domain.CaseReviewFinding, error) {
	rows, err := r.q.Query(ctx, `
		SELECT f.id, f.review_id, f.node_id, f.category, f.severity, f.detail,
		       f.status, f.source, f.created_by, f.created_at, f.updated_at
		FROM case_review_finding AS f
		JOIN case_review AS r ON r.id = f.review_id
		WHERE r.version_id = $1
		  AND ($2 = '' OR f.status = $2)
		  AND ($3 = '' OR f.severity = $3)
		  AND ($4::bigint IS NULL OR f.node_id = $4)
		ORDER BY f.id ASC`, versionID, status, severity, nullableInt(nodeID))
	if err != nil {
		return nil, fmt.Errorf("postgres: list case review findings: %w", err)
	}
	defer rows.Close()

	findings := make([]domain.CaseReviewFinding, 0)
	for rows.Next() {
		finding, err := scanFinding(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan case review finding: %w", err)
		}
		findings = append(findings, finding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list case review findings: %w", err)
	}
	return findings, nil
}

// UpdateStatus moves one finding to fixed or dismissed. The schema has no
// updated_by column, so actor is accepted for the port contract but not
// persisted; the audit sink records the actor.
func (r *FindingRepository) UpdateStatus(ctx context.Context, findingID int64, status string, actor int64) (domain.CaseReviewFinding, error) {
	const query = `UPDATE case_review_finding
		SET status = $2, updated_at = now()
		WHERE id = $1
		RETURNING ` + findingColumnList
	updated, err := scanFinding(r.q.QueryRow(ctx, query, findingID, status))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CaseReviewFinding{}, notFound("finding", findingID)
		}
		return domain.CaseReviewFinding{}, fmt.Errorf("postgres: update case review finding: %w", err)
	}
	return updated, nil
}

// DismissOpenAI dismisses the still-open AI findings of the version, which a
// newer review run supersedes (spec §6.8), and returns the number dismissed.
func (r *FindingRepository) DismissOpenAI(ctx context.Context, versionID int64) (int, error) {
	tag, err := r.q.Exec(ctx, `
		UPDATE case_review_finding AS f
		SET status = 'dismissed', updated_at = now()
		FROM case_review AS r
		WHERE f.review_id = r.id
		  AND r.version_id = $1
		  AND f.source = 'ai'
		  AND f.status = 'open'`, versionID)
	if err != nil {
		return 0, fmt.Errorf("postgres: dismiss open AI findings: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// CountOpenBlockers counts the still-open blocker findings of the version; the
// approve gate needs the ids (spec §6.3), the repository exposes the count.
func (r *FindingRepository) CountOpenBlockers(ctx context.Context, versionID int64) (int, error) {
	var count int
	if err := r.q.QueryRow(ctx, `
		SELECT count(*)::int
		FROM case_review_finding AS f
		JOIN case_review AS r ON r.id = f.review_id
		WHERE r.version_id = $1 AND f.status = 'open' AND f.severity = 'blocker'`,
		versionID).Scan(&count); err != nil {
		return 0, fmt.Errorf("postgres: count open blocker findings: %w", err)
	}
	return count, nil
}
