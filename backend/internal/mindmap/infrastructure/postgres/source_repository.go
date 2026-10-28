package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// sourceColumnList is the shared column order of every case_source query and
// the order scanSource expects.
const sourceColumnList = `id, version_id, node_id, ref_kind, ref_id, ref_key,
	source, confidence, created_at, updated_at`

// SourceRepository implements application.SourceRepository on a pgx querier.
type SourceRepository struct {
	q querier
}

// NewSourceRepository returns a case-source repository backed by pool.
func NewSourceRepository(pool *pgxpool.Pool) application.SourceRepository {
	return newSourceRepository(pool)
}

// newSourceRepository binds the repository to q.
func newSourceRepository(q querier) *SourceRepository {
	return &SourceRepository{q: q}
}

var _ application.SourceRepository = (*SourceRepository)(nil)

// scanSource scans one row in sourceColumnList order.
func scanSource(row rowScanner) (domain.CaseSource, error) {
	var source domain.CaseSource
	if err := row.Scan(
		&source.ID,
		&source.VersionID,
		&source.NodeID,
		&source.RefKind,
		&source.RefID,
		&source.RefKey,
		&source.Source,
		&source.Confidence,
		&source.CreatedAt,
		&source.UpdatedAt,
	); err != nil {
		return domain.CaseSource{}, err
	}
	return source, nil
}

// ListByVersion returns every source row of the version ordered by owning case
// and id.
func (r *SourceRepository) ListByVersion(ctx context.Context, versionID int64) ([]domain.CaseSource, error) {
	rows, err := r.q.Query(ctx,
		`SELECT `+sourceColumnList+`
		 FROM case_source
		 WHERE version_id = $1
		 ORDER BY node_id ASC, id ASC`, versionID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list case sources: %w", err)
	}
	defer rows.Close()

	sources := make([]domain.CaseSource, 0)
	for rows.Next() {
		source, err := scanSource(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan case source: %w", err)
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list case sources: %w", err)
	}
	return sources, nil
}

// Create inserts s with its explicit id. A duplicate of the
// uk_case_source_ref key (node, ref kind, ref id, ref key) is a
// duplicate_case_source conflict (spec §6.2/§7.4).
func (r *SourceRepository) Create(ctx context.Context, s domain.CaseSource) (domain.CaseSource, error) {
	const query = `INSERT INTO case_source
		(id, version_id, node_id, ref_kind, ref_id, ref_key, source, confidence,
		 created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING ` + sourceColumnList
	created, err := scanSource(r.q.QueryRow(ctx, query,
		s.ID, s.VersionID, s.NodeID, s.RefKind, nullableInt(s.RefID), s.RefKey,
		s.Source, s.Confidence, s.CreatedAt, s.UpdatedAt,
	))
	if err != nil {
		switch uniqueConstraint(err) {
		case "pk_case_source":
			return domain.CaseSource{}, domain.NewConflict("duplicate_key",
				fmt.Sprintf("source %d already exists", s.ID))
		case "uk_case_source_ref":
			return domain.CaseSource{}, domain.NewConflict("duplicate_case_source",
				fmt.Sprintf("node %d already has this %s source", s.NodeID, s.RefKind))
		}
		return domain.CaseSource{}, fmt.Errorf("postgres: create case source: %w", err)
	}
	return created, nil
}

// Delete removes one source row of nodeID.
func (r *SourceRepository) Delete(ctx context.Context, nodeID, sourceID int64) error {
	tag, err := r.q.Exec(ctx,
		`DELETE FROM case_source WHERE id = $1 AND node_id = $2`, sourceID, nodeID)
	if err != nil {
		return fmt.Errorf("postgres: delete case source: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return notFound("source", sourceID)
	}
	return nil
}

// CountRequirementSources counts the ref_kind='requirement' sources of one
// node. The application service rejects deleting the last one with
// invalid_case_tree (spec §6.2).
func (r *SourceRepository) CountRequirementSources(ctx context.Context, nodeID int64) (int, error) {
	var count int
	if err := r.q.QueryRow(ctx, `
		SELECT count(*)::int
		FROM case_source
		WHERE node_id = $1 AND ref_kind = 'requirement'`, nodeID).Scan(&count); err != nil {
		return 0, fmt.Errorf("postgres: count case requirement sources: %w", err)
	}
	return count, nil
}
