package visualpg

import (
	"context"
	"errors"
	"time"

	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
)

// AssertionRepository implements visualapp.AssertionRepository.
type AssertionRepository struct{ base }

const assertionColumns = `id, version_id, case_version_id, case_assertion_id, step_seq,
	assertion_seq, version_sut_id, selector, region, threshold::float8, viewport_width,
	viewport_height, device_scale_factor::float8, wait_policy, config_revision, status,
	created_by, created_at, updated_at`

func (r *AssertionRepository) Create(ctx context.Context, assertion visual.Assertion) (visual.Assertion, error) {
	ctx = contextOrBackground(ctx)
	_, err := r.pool.Exec(ctx, `
		INSERT INTO visual_assertion (id, version_id, case_version_id, case_assertion_id,
			step_seq, assertion_seq, version_sut_id, selector, region, threshold,
			viewport_width, viewport_height, device_scale_factor, wait_policy,
			config_revision, status, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11, $12, $13, $14::jsonb,
			$15, $16, $17, $18, $19)
	`, assertion.ID, assertion.VersionID, assertion.CaseVersionID, assertion.CaseAssertionID,
		assertion.StepSeq, assertion.AssertionSeq, assertion.VersionSUTID, assertion.Selector,
		jsonOrEmptyObject(assertion.Region), assertion.Threshold, assertion.ViewportWidth,
		assertion.ViewportHeight, assertion.DeviceScaleFactor, jsonOrEmptyObject(assertion.WaitPolicy),
		assertion.ConfigRevision, string(assertion.Status), assertion.CreatedBy,
		assertion.CreatedAt, assertion.UpdatedAt)
	if err != nil {
		return visual.Assertion{}, wrapDB("create assertion", err)
	}
	return r.Get(ctx, assertion.ID)
}

// UpdateWithRevision updates the mutable config columns and supersedes the
// current approved baseline in the same transaction (spec §7.1).
func (r *AssertionRepository) UpdateWithRevision(ctx context.Context, assertion visual.Assertion, supersededAt time.Time) (visual.Assertion, error) {
	ctx = contextOrBackground(ctx)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return visual.Assertion{}, wrapDB("update assertion: begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE visual_assertion SET
			version_sut_id = $2, selector = $3, region = $4::jsonb, threshold = $5,
			viewport_width = $6, viewport_height = $7, device_scale_factor = $8,
			wait_policy = $9::jsonb, config_revision = $10, status = $11, updated_at = $12
		WHERE id = $1
	`, assertion.ID, assertion.VersionSUTID, assertion.Selector, jsonOrEmptyObject(assertion.Region),
		assertion.Threshold, assertion.ViewportWidth, assertion.ViewportHeight,
		assertion.DeviceScaleFactor, jsonOrEmptyObject(assertion.WaitPolicy),
		assertion.ConfigRevision, string(assertion.Status), assertion.UpdatedAt)
	if err != nil {
		return visual.Assertion{}, wrapDB("update assertion", err)
	}
	if tag.RowsAffected() == 0 {
		return visual.Assertion{}, visual.ErrNotFound
	}
	_, err = tx.Exec(ctx, `
		UPDATE visual_baseline SET status = 'superseded', superseded_at = $2, updated_at = $2
		WHERE visual_assertion_id = $1 AND status = 'approved'
	`, assertion.ID, supersededAt)
	if err != nil {
		return visual.Assertion{}, wrapDB("supersede baseline", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return visual.Assertion{}, wrapDB("update assertion: commit", err)
	}
	return r.Get(ctx, assertion.ID)
}

// Get returns one visual assertion.
func (r *AssertionRepository) Get(ctx context.Context, id int64) (visual.Assertion, error) {
	ctx = contextOrBackground(ctx)
	row := r.pool.QueryRow(ctx, `SELECT `+assertionColumns+` FROM visual_assertion WHERE id = $1`, id)
	assertion, err := scanAssertion(row)
	if err != nil {
		return visual.Assertion{}, wrapDB("get assertion", err)
	}
	return assertion, nil
}

// GetByCaseAssertion returns the configuration of one design assertion.
func (r *AssertionRepository) GetByCaseAssertion(ctx context.Context, versionID, caseAssertionID int64) (visual.Assertion, bool, error) {
	ctx = contextOrBackground(ctx)
	row := r.pool.QueryRow(ctx, `
		SELECT `+assertionColumns+` FROM visual_assertion
		WHERE version_id = $1 AND case_assertion_id = $2
	`, versionID, caseAssertionID)
	assertion, err := scanAssertion(row)
	if err != nil {
		mapped := wrapDB("get assertion by case assertion", err)
		if errors.Is(mapped, visual.ErrNotFound) {
			return visual.Assertion{}, false, nil
		}
		return visual.Assertion{}, false, mapped
	}
	return assertion, true, nil
}

// List returns the visual assertions of a version.
func (r *AssertionRepository) List(ctx context.Context, versionID int64, caseVersionID, versionSUTID *int64) ([]visual.Assertion, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+assertionColumns+` FROM visual_assertion
		WHERE version_id = $1
		  AND ($2::bigint IS NULL OR case_version_id = $2)
		  AND ($3::bigint IS NULL OR version_sut_id = $3)
		ORDER BY case_version_id, step_seq, assertion_seq, id
	`, versionID, caseVersionID, versionSUTID)
	if err != nil {
		return nil, wrapDB("list assertions", err)
	}
	defer rows.Close()
	assertions := make([]visual.Assertion, 0)
	for rows.Next() {
		assertion, scanErr := scanAssertion(rows)
		if scanErr != nil {
			return nil, wrapDB("scan assertion", scanErr)
		}
		assertions = append(assertions, assertion)
	}
	return assertions, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAssertion(row rowScanner) (visual.Assertion, error) {
	var assertion visual.Assertion
	var status string
	var region, waitPolicy []byte
	err := row.Scan(&assertion.ID, &assertion.VersionID, &assertion.CaseVersionID,
		&assertion.CaseAssertionID, &assertion.StepSeq, &assertion.AssertionSeq,
		&assertion.VersionSUTID, &assertion.Selector, &region, &assertion.Threshold,
		&assertion.ViewportWidth, &assertion.ViewportHeight, &assertion.DeviceScaleFactor,
		&waitPolicy, &assertion.ConfigRevision, &status, &assertion.CreatedBy,
		&assertion.CreatedAt, &assertion.UpdatedAt)
	if err != nil {
		return visual.Assertion{}, err
	}
	assertion.Status = visual.AssertionStatus(status)
	assertion.Region = jsonOrEmptyObject(region)
	assertion.WaitPolicy = jsonOrEmptyObject(waitPolicy)
	return assertion, nil
}

func jsonOrEmptyObject(raw []byte) []byte {
	if len(raw) == 0 {
		return []byte(`{}`)
	}
	return raw
}
