package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// visualColumnList is the shared column order of the case_visual_assertion
// queries and the order scanVisual expects. The numeric columns are cast to
// float8 so pgx scans them without a pgtype wrapper.
const visualColumnList = `assertion_id, selector, region, threshold::float8,
	baseline_evidence_id, viewport_width, viewport_height,
	device_scale_factor::float8, approved_by, approved_at, revision,
	created_at, updated_at`

// VisualAssertionRepository implements application.VisualAssertionStore on a pgx
// querier.
type VisualAssertionRepository struct {
	q querier
}

// NewVisualAssertionRepository returns the visual assertion repository backed by
// pool.
func NewVisualAssertionRepository(pool *pgxpool.Pool) application.VisualAssertionStore {
	return &VisualAssertionRepository{q: pool}
}

var _ application.VisualAssertionStore = (*VisualAssertionRepository)(nil)

// scanVisual scans one case_visual_assertion row in visualColumnList order.
func scanVisual(row rowScanner) (domain.VisualAssertion, error) {
	var (
		visual    domain.VisualAssertion
		rawRegion []byte
	)
	if err := row.Scan(
		&visual.AssertionID,
		&visual.Selector,
		&rawRegion,
		&visual.Threshold,
		&visual.BaselineEvidenceID,
		&visual.ViewportWidth,
		&visual.ViewportHeight,
		&visual.DeviceScaleFactor,
		&visual.ApprovedBy,
		&visual.ApprovedAt,
		&visual.Revision,
		&visual.CreatedAt,
		&visual.UpdatedAt,
	); err != nil {
		return domain.VisualAssertion{}, err
	}
	visual.Region = decodeVisualRegion(rawRegion)
	return visual, nil
}

// decodeVisualRegion decodes the normalized JSONB crop. An empty object is no
// crop, so it maps back to nil.
func decodeVisualRegion(raw []byte) *domain.VisualRegion {
	if len(raw) == 0 {
		return nil
	}
	var region domain.VisualRegion
	if err := json.Unmarshal(raw, &region); err != nil {
		return nil
	}
	if region.X == 0 && region.Y == 0 && region.Width == 0 && region.Height == 0 {
		return nil
	}
	return &region
}

// encodeVisualRegion renders the optional crop as JSONB. No crop is '{}'.
func encodeVisualRegion(region *domain.VisualRegion) ([]byte, error) {
	if region == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(region)
}

// Get returns the assertion kind, owning node/revision and the current visual
// parameters (nil before the first PUT). A missing assertion of the version is a
// not-found error.
func (r *VisualAssertionRepository) Get(ctx context.Context, versionID, assertionID int64) (application.VisualAssertionTarget, error) {
	var (
		target      application.VisualAssertionTarget
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
	err := r.q.QueryRow(ctx, `
		SELECT a.node_id, n.revision, a.kind,
		       v.selector, v.region, v.threshold::float8, v.baseline_evidence_id,
		       v.viewport_width, v.viewport_height, v.device_scale_factor::float8,
		       v.approved_by, v.approved_at, v.revision, v.created_at, v.updated_at
		FROM case_assertion AS a
		JOIN case_node AS n ON n.id = a.node_id
		LEFT JOIN case_visual_assertion AS v ON v.assertion_id = a.id
		WHERE a.id = $1 AND n.version_id = $2`, assertionID, versionID).Scan(
		&target.NodeID, &target.NodeRevision, &target.Kind,
		&selector, &rawRegion, &threshold, &baselineID,
		&viewportW, &viewportH, &scaleFactor,
		&approvedBy, &approvedAt, &revision, &createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return application.VisualAssertionTarget{}, notFound("assertion", assertionID)
		}
		return application.VisualAssertionTarget{}, fmt.Errorf("postgres: get case visual assertion: %w", err)
	}
	if selector != nil {
		visual := domain.VisualAssertion{
			AssertionID:        assertionID,
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
		target.Visual = &visual
	}
	return target, nil
}

// Save upserts the comparison parameters and bumps the owning node revision
// under the expectedNodeRevision CAS inside one transaction. The node row is
// locked so two concurrent writes cannot both pass the revision check.
func (r *VisualAssertionRepository) Save(ctx context.Context, versionID, assertionID int64, expectedNodeRevision int, params domain.VisualAssertionParams, actor int64, now time.Time) (domain.VisualAssertion, error) {
	var stored domain.VisualAssertion
	err := withTx(ctx, r.q, func(q querier) error {
		nodeID, kind, revision, err := lockVisualAssertion(ctx, q, versionID, assertionID)
		if err != nil {
			return err
		}
		if expectedNodeRevision <= 0 || revision != expectedNodeRevision {
			return revisionConflict(nodeID)
		}
		if !isVisualKind(kind) {
			return domain.NewInvalid("assertionId", "invalid_assertion_kind",
				"only kind=visual assertions accept visual comparison parameters")
		}
		region, err := encodeVisualRegion(params.Region)
		if err != nil {
			return fmt.Errorf("postgres: encode visual region: %w", err)
		}
		stored, err = scanVisual(q.QueryRow(ctx, `
			INSERT INTO case_visual_assertion
				(assertion_id, selector, region, threshold, viewport_width,
				 viewport_height, device_scale_factor, revision, updated_at)
			VALUES ($1, $2, $3::jsonb, $4, $5, $6, $7, 1, $8)
			ON CONFLICT (assertion_id) DO UPDATE SET
				selector = EXCLUDED.selector,
				region = EXCLUDED.region,
				threshold = EXCLUDED.threshold,
				viewport_width = EXCLUDED.viewport_width,
				viewport_height = EXCLUDED.viewport_height,
				device_scale_factor = EXCLUDED.device_scale_factor,
				revision = case_visual_assertion.revision + 1,
				updated_at = EXCLUDED.updated_at
			RETURNING `+visualColumnList,
			assertionID, params.Selector, string(region), params.Threshold,
			params.ViewportWidth, params.ViewportHeight, params.DeviceScaleFactor, now))
		if err != nil {
			return fmt.Errorf("postgres: upsert case visual assertion: %w", err)
		}
		tag, err := q.Exec(ctx, `
			UPDATE case_node
			SET revision = revision + 1, updated_by = $2, updated_at = $3
			WHERE id = $1 AND revision = $4`, nodeID, actor, now, expectedNodeRevision)
		if err != nil {
			return fmt.Errorf("postgres: bump visual assertion node revision: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return revisionConflict(nodeID)
		}
		return nil
	})
	if err != nil {
		return domain.VisualAssertion{}, err
	}
	return stored, nil
}

// ApproveBaseline records the approved baseline evidence, approver and instant.
// The node revision is not bumped: approval is an annotation, not a graph
// content change.
func (r *VisualAssertionRepository) ApproveBaseline(ctx context.Context, versionID, assertionID, evidenceID, actor int64, now time.Time) (domain.VisualAssertion, error) {
	var stored domain.VisualAssertion
	err := withTx(ctx, r.q, func(q querier) error {
		_, kind, _, err := lockVisualAssertion(ctx, q, versionID, assertionID)
		if err != nil {
			return err
		}
		if !isVisualKind(kind) {
			return domain.NewInvalid("assertionId", "invalid_assertion_kind",
				"only kind=visual assertions can approve a visual baseline")
		}
		stored, err = scanVisual(q.QueryRow(ctx, `
			INSERT INTO case_visual_assertion
				(assertion_id, baseline_evidence_id, approved_by, approved_at, updated_at)
			VALUES ($1, $2, $3, $4, $4)
			ON CONFLICT (assertion_id) DO UPDATE SET
				baseline_evidence_id = EXCLUDED.baseline_evidence_id,
				approved_by = EXCLUDED.approved_by,
				approved_at = EXCLUDED.approved_at,
				updated_at = EXCLUDED.updated_at
			RETURNING `+visualColumnList,
			assertionID, evidenceID, actor, now))
		if err != nil {
			return fmt.Errorf("postgres: approve visual baseline: %w", err)
		}
		return nil
	})
	if err != nil {
		return domain.VisualAssertion{}, err
	}
	return stored, nil
}

// lockVisualAssertion reads the owning node, kind and current revision of an
// assertion and locks the node row for the rest of the transaction.
func lockVisualAssertion(ctx context.Context, q querier, versionID, assertionID int64) (nodeID int64, kind string, revision int, err error) {
	err = q.QueryRow(ctx, `
		SELECT a.node_id, a.kind, n.revision
		FROM case_assertion AS a
		JOIN case_node AS n ON n.id = a.node_id
		WHERE a.id = $1 AND n.version_id = $2
		FOR UPDATE OF n`, assertionID, versionID).Scan(&nodeID, &kind, &revision)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, "", 0, notFound("assertion", assertionID)
		}
		return 0, "", 0, fmt.Errorf("postgres: lock case visual assertion: %w", err)
	}
	return nodeID, kind, revision, nil
}

// isVisualKind reports whether kind is the visual assertion kind.
func isVisualKind(kind string) bool {
	return kind == domain.AssertionKindVisual
}

// valueOr dereferences value or returns fallback when it is nil.
func valueOr[T any](value *T, fallback T) T {
	if value == nil {
		return fallback
	}
	return *value
}
