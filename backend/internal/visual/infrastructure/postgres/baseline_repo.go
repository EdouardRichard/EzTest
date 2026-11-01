package visualpg

import (
	"context"
	"time"

	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
)

// BaselineRepository implements visualapp.BaselineRepository.
type BaselineRepository struct{ base }

const baselineColumns = `id, visual_assertion_id, case_version_id, version_sut_id,
	evidence_id, sha256, size_bytes, width, height, viewport_width, viewport_height,
	device_scale_factor::float8, config_revision, environment_fingerprint, status,
	approved_by, approved_at, superseded_at, created_at, updated_at`

// InsertApproved supersedes the previous approved baseline and inserts the new
// one in one transaction. The partial unique index is the final guard.
func (r *BaselineRepository) InsertApproved(ctx context.Context, baseline visual.Baseline, supersededAt time.Time) (visual.Baseline, error) {
	ctx = contextOrBackground(ctx)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return visual.Baseline{}, wrapDB("insert baseline: begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `
		UPDATE visual_baseline SET status = 'superseded', superseded_at = $2, updated_at = $2
		WHERE visual_assertion_id = $1 AND status = 'approved'
	`, baseline.VisualAssertionID, supersededAt)
	if err != nil {
		return visual.Baseline{}, wrapDB("supersede approved baseline", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO visual_baseline (id, visual_assertion_id, case_version_id, version_sut_id,
			evidence_id, sha256, size_bytes, width, height, viewport_width, viewport_height,
			device_scale_factor, config_revision, environment_fingerprint, status, approved_by,
			approved_at, superseded_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
			$17, $18, $19, $20)
	`, baseline.ID, baseline.VisualAssertionID, baseline.CaseVersionID, baseline.VersionSUTID,
		baseline.EvidenceID, baseline.SHA256, baseline.SizeBytes, baseline.Width, baseline.Height,
		baseline.ViewportWidth, baseline.ViewportHeight, baseline.DeviceScaleFactor,
		baseline.ConfigRevision, baseline.EnvironmentFingerprint, string(baseline.Status),
		baseline.ApprovedBy, baseline.ApprovedAt, baseline.SupersededAt, baseline.CreatedAt,
		baseline.UpdatedAt)
	if err != nil {
		return visual.Baseline{}, wrapDB("insert approved baseline", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return visual.Baseline{}, wrapDB("insert baseline: commit", err)
	}
	return baseline, nil
}

// List returns the baseline history of one assertion, newest first.
func (r *BaselineRepository) List(ctx context.Context, assertionID int64) ([]visual.Baseline, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+baselineColumns+` FROM visual_baseline
		WHERE visual_assertion_id = $1 ORDER BY id DESC
	`, assertionID)
	if err != nil {
		return nil, wrapDB("list baselines", err)
	}
	defer rows.Close()
	list := make([]visual.Baseline, 0)
	for rows.Next() {
		baseline, scanErr := scanBaseline(rows)
		if scanErr != nil {
			return nil, wrapDB("scan baseline", scanErr)
		}
		list = append(list, baseline)
	}
	return list, rows.Err()
}

// Active returns the currently approved baseline, if any.
func (r *BaselineRepository) Active(ctx context.Context, assertionID int64) (visual.Baseline, bool, error) {
	ctx = contextOrBackground(ctx)
	row := r.pool.QueryRow(ctx, `
		SELECT `+baselineColumns+` FROM visual_baseline
		WHERE visual_assertion_id = $1 AND status = 'approved'
	`, assertionID)
	baseline, err := scanBaseline(row)
	if err != nil {
		if err == visual.ErrNotFound {
			return visual.Baseline{}, false, nil
		}
		return visual.Baseline{}, false, wrapDB("get active baseline", err)
	}
	return baseline, true, nil
}

func scanBaseline(row rowScanner) (visual.Baseline, error) {
	var baseline visual.Baseline
	var status string
	err := row.Scan(&baseline.ID, &baseline.VisualAssertionID, &baseline.CaseVersionID,
		&baseline.VersionSUTID, &baseline.EvidenceID, &baseline.SHA256, &baseline.SizeBytes,
		&baseline.Width, &baseline.Height, &baseline.ViewportWidth, &baseline.ViewportHeight,
		&baseline.DeviceScaleFactor, &baseline.ConfigRevision, &baseline.EnvironmentFingerprint,
		&status, &baseline.ApprovedBy, &baseline.ApprovedAt, &baseline.SupersededAt,
		&baseline.CreatedAt, &baseline.UpdatedAt)
	if err != nil {
		return visual.Baseline{}, err
	}
	baseline.Status = visual.BaselineStatus(status)
	return baseline, nil
}
