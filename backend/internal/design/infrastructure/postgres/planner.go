package designpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/jackc/pgx/v5"
)

// plannerTxKey carries the advisory-locked transaction through repository
// calls made by the planner callback, so round/scope writes participate in the
// same transaction instead of silently using the pool.
type plannerTxKey struct{}

func withPlannerTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, plannerTxKey{}, tx)
}

func plannerQuerier(ctx context.Context, r *Repository) pgxQuerier {
	if tx, ok := ctx.Value(plannerTxKey{}).(pgx.Tx); ok {
		return tx
	}
	return r.pool
}

// WithVersionLock runs fn with the per-version planner advisory lock held.
func (r *Repository) WithVersionLock(ctx context.Context, versionID int64, fn func(context.Context) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin version design transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := lockVersion(ctx, tx, versionID); err != nil {
		return err
	}
	if err := fn(withPlannerTx(ctx, tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit version design transaction: %w", err)
	}
	return nil
}

// ListCandidateVersionIDs returns initial/running versions that hold any design
// input, newest first (spec §9.1 rule 1).
func (r *Repository) ListCandidateVersionIDs(ctx context.Context, limit int) ([]int64, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT v.id
		FROM version v
		WHERE v.status IN ('initial', 'running')
		  AND (
		      EXISTS (SELECT 1 FROM requirement r WHERE r.version_id = v.id AND r.status = 'clarified')
		      OR EXISTS (SELECT 1 FROM conversation c WHERE c.version_id = v.id AND c.kind = 'design')
		      OR EXISTS (SELECT 1 FROM design_scope ds WHERE ds.version_id = v.id)
		      OR EXISTS (SELECT 1 FROM case_scenario s WHERE s.version_id = v.id)
		  )
		ORDER BY v.id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list design candidate versions: %w", err)
	}
	defer rows.Close()
	versionIDs := make([]int64, 0)
	for rows.Next() {
		var versionID int64
		if err := rows.Scan(&versionID); err != nil {
			return nil, fmt.Errorf("scan design candidate version: %w", err)
		}
		versionIDs = append(versionIDs, versionID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan design candidate versions: %w", err)
	}
	return versionIDs, nil
}

// ListClarifiedRequirements returns the clarified requirements of one version.
func (r *Repository) ListClarifiedRequirements(ctx context.Context, versionID int64) ([]design.RequirementRef, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT r.id, r.version_id, r.current_version_id, rv.revision, rv.title, r.status,
		       COALESCE(rv.target_version_suts, '[]'::jsonb)
		FROM requirement r
		JOIN requirement_version rv ON rv.id = r.current_version_id
		WHERE r.version_id = $1 AND r.status = 'clarified'
		ORDER BY r.id
	`, versionID)
	if err != nil {
		return nil, fmt.Errorf("list clarified requirements: %w", err)
	}
	defer rows.Close()
	refs := make([]design.RequirementRef, 0)
	for rows.Next() {
		var ref design.RequirementRef
		var targetSUTs []byte
		if err := rows.Scan(&ref.ID, &ref.VersionID, &ref.CurrentVersionID, &ref.Revision,
			&ref.Title, &ref.Status, &targetSUTs); err != nil {
			return nil, fmt.Errorf("scan clarified requirement: %w", err)
		}
		if ref.TargetVersionSUTIDs, err = decodeIDArray(targetSUTs); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan clarified requirements: %w", err)
	}
	return refs, nil
}

// ListActiveScopes returns the active design scopes of one version.
func (r *Repository) ListActiveScopes(ctx context.Context, versionID int64) ([]design.Scope, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, version_id, requirement_id, activated_by_round_id, activated_at,
		       status, created_at, updated_at
		FROM design_scope
		WHERE version_id = $1 AND status = 'active'
		ORDER BY id
	`, versionID)
	if err != nil {
		return nil, fmt.Errorf("list design scopes: %w", err)
	}
	defer rows.Close()
	scopes := make([]design.Scope, 0)
	for rows.Next() {
		var scope design.Scope
		if err := rows.Scan(&scope.ID, &scope.VersionID, &scope.RequirementID,
			&scope.ActivatedByRoundID, &scope.ActivatedAt, &scope.Status,
			&scope.CreatedAt, &scope.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan design scope: %w", err)
		}
		scopes = append(scopes, scope)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan design scopes: %w", err)
	}
	return scopes, nil
}

// ListRequirementCoverage returns the activated requirements of one version
// with their current-version scenario/case coverage.
func (r *Repository) ListRequirementCoverage(ctx context.Context, versionID int64) ([]design.RequirementCoverage, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT r.id, r.version_id, r.current_version_id, rv.revision, rv.title, r.status,
		       COALESCE(rv.target_version_suts, '[]'::jsonb),
		       (SELECT count(*) FROM case_scenario s
		        WHERE s.version_id = r.version_id AND s.requirement_id = r.id
		          AND s.requirement_version_id = r.current_version_id),
		       (SELECT count(*) FROM case_entity ce
		        JOIN case_version cv ON cv.id = ce.current_version_id
		        JOIN case_scenario s ON s.id = ce.scenario_id
		        WHERE s.version_id = r.version_id AND s.requirement_id = r.id
		          AND s.requirement_version_id = r.current_version_id
		          AND cv.status = 'draft'),
		       (SELECT count(*) FROM case_entity ce
		        JOIN case_version cv ON cv.id = ce.current_version_id
		        JOIN case_scenario s ON s.id = ce.scenario_id
		        WHERE s.version_id = r.version_id AND s.requirement_id = r.id
		          AND s.requirement_version_id = r.current_version_id
		          AND cv.status = 'confirmed'),
		       (SELECT count(*) FROM case_entity ce
		        JOIN case_version cv ON cv.id = ce.current_version_id
		        JOIN case_scenario s ON s.id = ce.scenario_id
		        WHERE s.version_id = r.version_id AND s.requirement_id = r.id
		          AND s.requirement_version_id = r.current_version_id
		          AND cv.status = 'stale')
		FROM requirement r
		JOIN requirement_version rv ON rv.id = r.current_version_id
		WHERE r.version_id = $1
		  AND EXISTS (SELECT 1 FROM design_scope ds
		              WHERE ds.version_id = r.version_id AND ds.requirement_id = r.id
		                AND ds.status = 'active')
		ORDER BY r.id
	`, versionID)
	if err != nil {
		return nil, fmt.Errorf("list requirement coverage: %w", err)
	}
	defer rows.Close()
	coverages := make([]design.RequirementCoverage, 0)
	for rows.Next() {
		var coverage design.RequirementCoverage
		var targetSUTs []byte
		if err := rows.Scan(&coverage.Requirement.ID, &coverage.Requirement.VersionID,
			&coverage.Requirement.CurrentVersionID, &coverage.Requirement.Revision,
			&coverage.Requirement.Title, &coverage.Requirement.Status, &targetSUTs,
			&coverage.ScenarioCount, &coverage.DraftCount, &coverage.ConfirmedCount,
			&coverage.StaleCount); err != nil {
			return nil, fmt.Errorf("scan requirement coverage: %w", err)
		}
		if coverage.Requirement.TargetVersionSUTIDs, err = decodeIDArray(targetSUTs); err != nil {
			return nil, err
		}
		coverages = append(coverages, coverage)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan requirement coverage: %w", err)
	}
	return coverages, nil
}

// LatestDesignConversation returns the newest design conversation of a version.
func (r *Repository) LatestDesignConversation(ctx context.Context, versionID int64) (int64, bool, error) {
	var conversationID int64
	err := r.pool.QueryRow(ctx, `
		SELECT id FROM conversation
		WHERE version_id = $1 AND kind = 'design'
		ORDER BY id DESC
		LIMIT 1
	`, versionID).Scan(&conversationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read latest design conversation: %w", err)
	}
	return conversationID, true, nil
}

func scanRound(row pgx.Row) (design.Round, error) {
	var round design.Round
	var requirementIDs, requirementVersionIDs, scope []byte
	if err := row.Scan(&round.ID, &round.VersionID, &round.ConversationID, &round.Activation,
		&requirementIDs, &requirementVersionIDs, &scope, &round.Status,
		&round.CreatedBy, &round.CreatedAt, &round.UpdatedAt); err != nil {
		return design.Round{}, err
	}
	var err error
	if round.RequirementIDs, err = decodeIDArray(requirementIDs); err != nil {
		return design.Round{}, err
	}
	if round.RequirementVersionIDs, err = decodeIDArray(requirementVersionIDs); err != nil {
		return design.Round{}, err
	}
	round.Scope = scope
	return round, nil
}

// GetRoundByConversation returns the round owned by one design conversation.
func (r *Repository) GetRoundByConversation(ctx context.Context, conversationID int64) (*design.Round, error) {
	q := plannerQuerier(ctx, r)
	round, err := scanRound(q.QueryRow(ctx, `
		SELECT id, version_id, conversation_id, activation, requirement_ids,
		       requirement_version_ids, scope, status, created_by, created_at, updated_at
		FROM design_round
		WHERE conversation_id = $1
	`, conversationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read design round by conversation: %w", err)
	}
	return &round, nil
}

// GetRound returns one round of a version.
func (r *Repository) GetRound(ctx context.Context, versionID, roundID int64) (*design.Round, error) {
	round, err := scanRound(r.pool.QueryRow(ctx, `
		SELECT id, version_id, conversation_id, activation, requirement_ids,
		       requirement_version_ids, scope, status, created_by, created_at, updated_at
		FROM design_round
		WHERE id = $1 AND version_id = $2
	`, roundID, versionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read design round: %w", err)
	}
	return &round, nil
}

// GetLatestRound returns the newest round of a version.
func (r *Repository) GetLatestRound(ctx context.Context, versionID int64) (*design.Round, error) {
	q := plannerQuerier(ctx, r)
	round, err := scanRound(q.QueryRow(ctx, `
		SELECT id, version_id, conversation_id, activation, requirement_ids,
		       requirement_version_ids, scope, status, created_by, created_at, updated_at
		FROM design_round
		WHERE version_id = $1
		ORDER BY id DESC
		LIMIT 1
	`, versionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read latest design round: %w", err)
	}
	return &round, nil
}

// ListRounds returns every round of a version in ascending ID order.
func (r *Repository) ListRounds(ctx context.Context, versionID int64) ([]design.Round, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, version_id, conversation_id, activation, requirement_ids,
		       requirement_version_ids, scope, status, created_by, created_at, updated_at
		FROM design_round
		WHERE version_id = $1
		ORDER BY id
	`, versionID)
	if err != nil {
		return nil, fmt.Errorf("list design rounds: %w", err)
	}
	defer rows.Close()
	rounds := make([]design.Round, 0)
	for rows.Next() {
		round, err := scanRound(rows)
		if err != nil {
			return nil, fmt.Errorf("scan design round: %w", err)
		}
		rounds = append(rounds, round)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan design rounds: %w", err)
	}
	return rounds, nil
}

// CreateRound inserts one design round. The caller supplies the snowflake ID.
func (r *Repository) CreateRound(ctx context.Context, round design.Round) (design.Round, error) {
	q := plannerQuerier(ctx, r)
	requirementIDs, err := encodeIDArray(round.RequirementIDs)
	if err != nil {
		return design.Round{}, err
	}
	requirementVersionIDs, err := encodeIDArray(round.RequirementVersionIDs)
	if err != nil {
		return design.Round{}, err
	}
	scope := round.Scope
	if len(scope) == 0 {
		scope = json.RawMessage(`{}`)
	}
	if round.Status == "" {
		round.Status = design.RoundStatusActive
	}
	err = q.QueryRow(ctx, `
		INSERT INTO design_round (
			id, version_id, conversation_id, activation, requirement_ids,
			requirement_version_ids, scope, status, created_by, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5::jsonb,$6::jsonb,$7::jsonb,$8,$9,$10,$11)
		RETURNING id
	`, round.ID, round.VersionID, round.ConversationID, round.Activation,
		string(requirementIDs), string(requirementVersionIDs), string(scope),
		round.Status, round.CreatedBy, round.CreatedAt, round.UpdatedAt).Scan(&round.ID)
	if err != nil {
		return design.Round{}, fmt.Errorf("insert design round: %w", err)
	}
	return round, nil
}

// UpdateRoundRequirements rewrites the round's requirement snapshot.
func (r *Repository) UpdateRoundRequirements(ctx context.Context, roundID int64, requirementIDs, requirementVersionIDs []int64) error {
	q := plannerQuerier(ctx, r)
	ids, err := encodeIDArray(requirementIDs)
	if err != nil {
		return err
	}
	versionIDs, err := encodeIDArray(requirementVersionIDs)
	if err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `
		UPDATE design_round
		SET requirement_ids = $2::jsonb, requirement_version_ids = $3::jsonb, updated_at = now()
		WHERE id = $1
	`, roundID, string(ids), string(versionIDs)); err != nil {
		return fmt.Errorf("update design round requirements: %w", err)
	}
	return nil
}

// UpsertScope activates one requirement. It returns whether the row was created
// and is idempotent on (version_id, requirement_id). The caller must hold the
// version lock.
func (r *Repository) UpsertScope(ctx context.Context, scope design.Scope) (design.Scope, bool, error) {
	q := plannerQuerier(ctx, r)
	if scope.Status == "" {
		scope.Status = design.ScopeStatusActive
	}
	var existingID int64
	err := q.QueryRow(ctx, `
		SELECT id FROM design_scope WHERE version_id = $1 AND requirement_id = $2
	`, scope.VersionID, scope.RequirementID).Scan(&existingID)
	created := false
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		created = true
		err = q.QueryRow(ctx, `
			INSERT INTO design_scope (
				id, version_id, requirement_id, activated_by_round_id, activated_at,
				status, created_at, updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			RETURNING id
		`, scope.ID, scope.VersionID, scope.RequirementID, scope.ActivatedByRoundID,
			scope.ActivatedAt, scope.Status, scope.CreatedAt, scope.UpdatedAt).Scan(&scope.ID)
		if err != nil {
			return design.Scope{}, false, fmt.Errorf("insert design scope: %w", err)
		}
	case err != nil:
		return design.Scope{}, false, fmt.Errorf("read design scope: %w", err)
	default:
		scope.ID = existingID
		if _, err := q.Exec(ctx, `
			UPDATE design_scope
			SET status = 'active', activated_by_round_id = $2, activated_at = $3, updated_at = $4
			WHERE id = $1
		`, scope.ID, scope.ActivatedByRoundID, scope.ActivatedAt, scope.UpdatedAt); err != nil {
			return design.Scope{}, false, fmt.Errorf("update design scope: %w", err)
		}
	}
	return scope, created, nil
}

// CurrentCaseVersion loads one case version if it is the current version of its
// case inside the version scope.
func (r *Repository) CurrentCaseVersion(ctx context.Context, versionID, caseVersionID int64) (design.CaseVersion, error) {
	var currentVersionID int64
	err := r.pool.QueryRow(ctx, `
		SELECT ce.current_version_id
		FROM case_entity ce
		JOIN case_scenario s ON s.id = ce.scenario_id
		WHERE ce.current_version_id = $1 AND s.version_id = $2
	`, caseVersionID, versionID).Scan(&currentVersionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return design.CaseVersion{}, design.ErrNotFound
	}
	if err != nil {
		return design.CaseVersion{}, fmt.Errorf("read current case version: %w", err)
	}
	return loadCaseVersion(ctx, r.pool, currentVersionID)
}

// InsertDataPlanRevision inserts a prepared data-planner draft revision and
// moves the current pointer. It re-validates inside a row-locking transaction.
func (r *Repository) InsertDataPlanRevision(ctx context.Context, versionID, baseCaseVersionID int64, revision design.CaseVersion, at time.Time) (design.CaseVersion, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return design.CaseVersion{}, fmt.Errorf("begin data plan transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	// Serialize with case.graph_propose on the same requirement. The proposal
	// path holds the advisory lock while it reads/updates the current pointer;
	// taking it here prevents a data-plan revision from racing that snapshot and
	// causing a duplicate (case_id, revision) or a lost pointer update.
	var requirementID int64
	if err := tx.QueryRow(ctx, `
		SELECT s.requirement_id
		FROM case_version cv
		JOIN case_entity ce ON ce.id = cv.case_id
		JOIN case_scenario s ON s.id = ce.scenario_id
		WHERE cv.id = $1 AND s.version_id = $2
	`, baseCaseVersionID, versionID).Scan(&requirementID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return design.CaseVersion{}, design.ErrNotFound
		}
		return design.CaseVersion{}, fmt.Errorf("resolve data plan requirement: %w", err)
	}
	if err := lockRequirement(ctx, tx, versionID, requirementID); err != nil {
		return design.CaseVersion{}, err
	}

	var currentVersionID int64
	var status string
	var currentRevision int
	err = tx.QueryRow(ctx, `
		SELECT ce.current_version_id, cv.status, cv.revision
		FROM case_entity ce
		JOIN case_version cv ON cv.id = ce.current_version_id
		JOIN case_scenario s ON s.id = ce.scenario_id
		WHERE cv.id = $1 AND s.version_id = $2
		FOR UPDATE OF ce, cv
	`, baseCaseVersionID, versionID).Scan(&currentVersionID, &status, &currentRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		// The base version exists but is no longer the current version of its
		// case: a concurrent revision won, so report a state conflict.
		var exists bool
		if existsErr := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM case_version cv
				JOIN case_entity ce ON ce.id = cv.case_id
				JOIN case_scenario s ON s.id = ce.scenario_id
				WHERE cv.id = $1 AND s.version_id = $2
			)
		`, baseCaseVersionID, versionID).Scan(&exists); existsErr != nil {
			return design.CaseVersion{}, fmt.Errorf("check data plan base version: %w", existsErr)
		}
		if exists {
			return design.CaseVersion{}, design.ErrStateConflict
		}
		return design.CaseVersion{}, design.ErrNotFound
	}
	if err != nil {
		return design.CaseVersion{}, fmt.Errorf("lock data plan base version: %w", err)
	}
	if status != design.CaseStatusDraft {
		return design.CaseVersion{}, design.ErrStateConflict
	}
	if currentRevision != revision.Revision-1 {
		return design.CaseVersion{}, design.ErrStateConflict
	}

	txWriter := &proposeTx{tx: tx}
	stored, err := txWriter.InsertCaseVersion(ctx, revision)
	if err != nil {
		return design.CaseVersion{}, err
	}
	if err := txWriter.SetCurrentVersion(ctx, revision.CaseID, stored.ID, design.CaseStatusDraft, at); err != nil {
		return design.CaseVersion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return design.CaseVersion{}, fmt.Errorf("commit data plan transaction: %w", err)
	}
	return stored, nil
}
