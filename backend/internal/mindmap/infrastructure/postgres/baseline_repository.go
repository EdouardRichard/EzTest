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

// baselineColumnList is the shared column order of every case_baseline query
// and the order scanBaseline expects.
const baselineColumnList = `id, version_id, review_id, node_count, case_count,
	graph_hash, code_baseline_commit, frozen_by, frozen_at`

// BaselineRepository implements application.BaselineRepository on a pgx
// querier.
type BaselineRepository struct {
	q querier
}

// NewBaselineRepository returns a case-baseline repository backed by pool.
func NewBaselineRepository(pool *pgxpool.Pool) application.BaselineRepository {
	return newBaselineRepository(pool)
}

// newBaselineRepository binds the repository to q.
func newBaselineRepository(q querier) *BaselineRepository {
	return &BaselineRepository{q: q}
}

var _ application.BaselineRepository = (*BaselineRepository)(nil)

// baselineNotFound is the version-scoped not-found of the baseline lookup.
func baselineNotFound(versionID int64) error {
	return &domain.Error{
		Kind:    domain.KindNotFound,
		Reason:  "baseline_not_found",
		Message: fmt.Sprintf("version %d has no case baseline", versionID),
	}
}

// scanBaseline scans one row in baselineColumnList order.
func scanBaseline(row rowScanner) (domain.CaseBaseline, error) {
	var baseline domain.CaseBaseline
	if err := row.Scan(
		&baseline.ID,
		&baseline.VersionID,
		&baseline.ReviewID,
		&baseline.NodeCount,
		&baseline.CaseCount,
		&baseline.GraphHash,
		&baseline.CodeBaselineCommit,
		&baseline.FrozenBy,
		&baseline.FrozenAt,
	); err != nil {
		return domain.CaseBaseline{}, err
	}
	return baseline, nil
}

// Create inserts b with its explicit id. case_baseline.version_id and
// case_baseline.review_id are both unique: a version is frozen only once (D5)
// and a second freeze is a baseline_frozen conflict.
func (r *BaselineRepository) Create(ctx context.Context, b domain.CaseBaseline) (domain.CaseBaseline, error) {
	const query = `INSERT INTO case_baseline
		(id, version_id, review_id, node_count, case_count, graph_hash,
		 code_baseline_commit, frozen_by, frozen_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING ` + baselineColumnList
	created, err := scanBaseline(r.q.QueryRow(ctx, query,
		b.ID, b.VersionID, b.ReviewID, b.NodeCount, b.CaseCount, b.GraphHash,
		nullableString(b.CodeBaselineCommit), b.FrozenBy, b.FrozenAt,
	))
	if err != nil {
		switch uniqueConstraint(err) {
		case "pk_case_baseline":
			return domain.CaseBaseline{}, domain.NewConflict("duplicate_key",
				fmt.Sprintf("baseline %d already exists", b.ID))
		case "uk_case_baseline_version":
			return domain.CaseBaseline{}, domain.NewConflict("baseline_frozen",
				fmt.Sprintf("version %d is already frozen", b.VersionID))
		case "uk_case_baseline_review":
			return domain.CaseBaseline{}, domain.NewConflict("baseline_frozen",
				fmt.Sprintf("review %d is already frozen", b.ReviewID))
		}
		return domain.CaseBaseline{}, fmt.Errorf("postgres: create case baseline: %w", err)
	}
	return created, nil
}

// GetByVersion returns the frozen baseline of one version.
func (r *BaselineRepository) GetByVersion(ctx context.Context, versionID int64) (domain.CaseBaseline, error) {
	baseline, err := scanBaseline(r.q.QueryRow(ctx,
		`SELECT `+baselineColumnList+` FROM case_baseline WHERE version_id = $1`, versionID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CaseBaseline{}, baselineNotFound(versionID)
		}
		return domain.CaseBaseline{}, fmt.Errorf("postgres: get case baseline: %w", err)
	}
	return baseline, nil
}

// DeleteByVersion removes the baseline of one version; the approve compensation
// path (spec §6.4) uses it when VersionLifecycle.ApproveBaseline fails after the
// baseline was written.
func (r *BaselineRepository) DeleteByVersion(ctx context.Context, versionID int64) error {
	tag, err := r.q.Exec(ctx, `DELETE FROM case_baseline WHERE version_id = $1`, versionID)
	if err != nil {
		return fmt.Errorf("postgres: delete case baseline: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return baselineNotFound(versionID)
	}
	return nil
}
