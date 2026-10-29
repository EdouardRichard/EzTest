package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// assertionColumnList is the shared column order of every case_assertion query
// and the order scanAssertion expects.
const assertionColumnList = `id, node_id, step_id, kind, target, expression, expected,
	source, created_at, updated_at`

// AssertionRepository implements application.AssertionRepository on a pgx
// querier.
type AssertionRepository struct {
	q querier
}

// NewAssertionRepository returns a case-assertion repository backed by pool.
func NewAssertionRepository(pool *pgxpool.Pool) application.AssertionRepository {
	return newAssertionRepository(pool)
}

// newAssertionRepository binds the repository to q.
func newAssertionRepository(q querier) *AssertionRepository {
	return &AssertionRepository{q: q}
}

var _ application.AssertionRepository = (*AssertionRepository)(nil)

// scanAssertion scans one row in assertionColumnList order.
func scanAssertion(row rowScanner) (domain.CaseAssertion, error) {
	var assertion domain.CaseAssertion
	if err := row.Scan(
		&assertion.ID,
		&assertion.NodeID,
		&assertion.StepID,
		&assertion.Kind,
		&assertion.Target,
		&assertion.Expression,
		&assertion.Expected,
		&assertion.Source,
		&assertion.CreatedAt,
		&assertion.UpdatedAt,
	); err != nil {
		return domain.CaseAssertion{}, err
	}
	return assertion, nil
}

// visualAssertionProjection is the optional case_visual_assertion projection
// appended to the graph read. The numeric columns are cast to float8 so pgx can
// scan them directly.
const visualAssertionProjection = `v.selector, v.region, v.threshold::float8,
	v.baseline_evidence_id, v.viewport_width, v.viewport_height,
	v.device_scale_factor::float8, v.approved_by, v.approved_at, v.revision,
	v.created_at, v.updated_at`

// scanAssertionWithVisual scans one assertion row followed by the nullable
// visual projection (all NULL for a non-visual assertion or a visual assertion
// whose parameters were never saved). Both projections are scanned in ONE
// call: a database row can only be scanned once, so the base assertion and the
// LEFT JOINed visual columns must be passed to the same Scan.
func scanAssertionWithVisual(row rowScanner) (domain.CaseAssertion, error) {
	var (
		assertion   domain.CaseAssertion
		selector    *string
		rawRegion   []byte
		threshold   *float64
		baselineID  *int64
		viewportW   *int
		viewportH   *int
		scaleFactor *float64
		approvedBy  *int64
		approvedAt  *time.Time
		revision    *int
		createdAt   *time.Time
		updatedAt   *time.Time
	)
	if err := row.Scan(
		&assertion.ID,
		&assertion.NodeID,
		&assertion.StepID,
		&assertion.Kind,
		&assertion.Target,
		&assertion.Expression,
		&assertion.Expected,
		&assertion.Source,
		&assertion.CreatedAt,
		&assertion.UpdatedAt,
		&selector, &rawRegion, &threshold, &baselineID,
		&viewportW, &viewportH, &scaleFactor,
		&approvedBy, &approvedAt, &revision, &createdAt, &updatedAt,
	); err != nil {
		return domain.CaseAssertion{}, err
	}
	if selector == nil {
		return assertion, nil
	}
	visual := domain.VisualAssertion{
		AssertionID:        assertion.ID,
		Selector:           *selector,
		Region:             decodeVisualRegion(rawRegion),
		Threshold:          valueOr(threshold, domain.VisualThresholdDefault),
		BaselineEvidenceID: baselineID,
		ViewportWidth:      valueOr(viewportW, domain.VisualViewportDefaultWidth),
		ViewportHeight:     valueOr(viewportH, domain.VisualViewportDefaultHeight),
		DeviceScaleFactor:  valueOr(scaleFactor, domain.VisualDeviceScaleFactorDefault),
		ApprovedBy:         approvedBy,
		ApprovedAt:         approvedAt,
		Revision:           valueOr(revision, 1),
	}
	if createdAt != nil {
		visual.CreatedAt = *createdAt
	}
	if updatedAt != nil {
		visual.UpdatedAt = *updatedAt
	}
	assertion.Visual = &visual
	return assertion, nil
}

// ListByVersion returns every assertion of the version ordered by owning case
// and id, with the optional visual comparison parameters attached.
func (r *AssertionRepository) ListByVersion(ctx context.Context, versionID int64) ([]domain.CaseAssertion, error) {
	rows, err := r.q.Query(ctx, `
		SELECT a.id, a.node_id, a.step_id, a.kind, a.target, a.expression,
		       a.expected, a.source, a.created_at, a.updated_at,
		       `+visualAssertionProjection+`
		FROM case_assertion AS a
		JOIN case_node AS n ON n.id = a.node_id
		LEFT JOIN case_visual_assertion AS v ON v.assertion_id = a.id
		WHERE n.version_id = $1
		ORDER BY a.node_id ASC, a.id ASC`, versionID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list case assertions: %w", err)
	}
	defer rows.Close()

	assertions := make([]domain.CaseAssertion, 0)
	for rows.Next() {
		assertion, err := scanAssertionWithVisual(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan case assertion: %w", err)
		}
		assertions = append(assertions, assertion)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list case assertions: %w", err)
	}
	return assertions, nil
}

// Create inserts a with its explicit id and writes the stored row back.
func (r *AssertionRepository) Create(ctx context.Context, a domain.CaseAssertion) (domain.CaseAssertion, error) {
	const query = `INSERT INTO case_assertion
		(id, node_id, step_id, kind, target, expression, expected, source,
		 created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING ` + assertionColumnList
	created, err := scanAssertion(r.q.QueryRow(ctx, query,
		a.ID, a.NodeID, nullableInt(a.StepID), a.Kind, a.Target, a.Expression,
		a.Expected, a.Source, a.CreatedAt, a.UpdatedAt,
	))
	if err != nil {
		if uniqueConstraint(err) == "pk_case_assertion" {
			return domain.CaseAssertion{}, domain.NewConflict("duplicate_key",
				fmt.Sprintf("assertion %d already exists", a.ID))
		}
		return domain.CaseAssertion{}, fmt.Errorf("postgres: create case assertion: %w", err)
	}
	return created, nil
}

// Update rewrites the content columns of a. The node revision CAS around the
// assertion subset is owned by the application service (spec §6.5).
func (r *AssertionRepository) Update(ctx context.Context, a domain.CaseAssertion) error {
	tag, err := r.q.Exec(ctx, `
		UPDATE case_assertion
		SET step_id = $3, kind = $4, target = $5, expression = $6, expected = $7,
		    source = $8, updated_at = $9
		WHERE id = $1 AND node_id = $2`,
		a.ID, a.NodeID, nullableInt(a.StepID), a.Kind, a.Target, a.Expression,
		a.Expected, a.Source, a.UpdatedAt)
	if err != nil {
		return fmt.Errorf("postgres: update case assertion: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return notFound("assertion", a.ID)
	}
	return nil
}

// Delete removes one assertion of nodeID.
func (r *AssertionRepository) Delete(ctx context.Context, nodeID, assertionID int64) error {
	tag, err := r.q.Exec(ctx,
		`DELETE FROM case_assertion WHERE id = $1 AND node_id = $2`, assertionID, nodeID)
	if err != nil {
		return fmt.Errorf("postgres: delete case assertion: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return notFound("assertion", assertionID)
	}
	return nil
}

// CountByNode returns the number of assertions of one case.
func (r *AssertionRepository) CountByNode(ctx context.Context, nodeID int64) (int, error) {
	var count int
	if err := r.q.QueryRow(ctx,
		`SELECT count(*)::int FROM case_assertion WHERE node_id = $1`, nodeID).Scan(&count); err != nil {
		return 0, fmt.Errorf("postgres: count case assertions: %w", err)
	}
	return count, nil
}
