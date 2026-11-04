package versionpg

import (
	"context"
	"errors"
	"fmt"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5"
)

// GetStage loads the deterministic stage-machine projection of one version
// (spec §5.3). A missing version yields version.ErrNotFound.
func (r *Repository) GetStage(ctx context.Context, versionID int64) (version.StageState, error) {
	var state version.StageState
	var stage string
	var suspendedFrom *string
	err := r.pool.QueryRow(ctx, `
		SELECT id, stage, stage_entered_at, COALESCE(blocker, ''), suspended_from
		FROM version
		WHERE id = $1
	`, versionID).Scan(&state.VersionID, &stage, &state.StageEnteredAt, &state.Blocker, &suspendedFrom)
	if errors.Is(err, pgx.ErrNoRows) {
		return version.StageState{}, version.ErrNotFound
	}
	if err != nil {
		return version.StageState{}, fmt.Errorf("get version stage: %w", err)
	}
	state.Stage = version.Stage(stage)
	if suspendedFrom != nil {
		previous := version.Stage(*suspendedFrom)
		state.SuspendedFrom = &previous
	}
	return state, nil
}

// ApplyStage writes one validated transition. The WHERE clause pins the source
// stage to the value the stage machine validated against, so a concurrent
// transition settles as version.ErrStageConflict instead of silently
// overwriting the winner. suspended_from is written from next.SuspendedFrom
// (non-nil only while entering blocked/paused) and cleared to NULL on resume.
func (r *Repository) ApplyStage(ctx context.Context, expected version.Stage, next version.StageState) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE version
		SET stage = $3,
		    stage_entered_at = $4,
		    blocker = NULLIF($5, ''),
		    suspended_from = $6,
		    updated_at = $4
		WHERE id = $1 AND stage = $2
	`, next.VersionID, string(expected), string(next.Stage), next.StageEnteredAt, next.Blocker,
		nullableSuspendedFrom(next.SuspendedFrom))
	if err != nil {
		return wrapDB("apply version stage", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	// No row matched: distinguish a stale source stage from a missing version.
	var exists bool
	if err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM version WHERE id = $1)
	`, next.VersionID).Scan(&exists); err != nil {
		return fmt.Errorf("apply version stage: existence check: %w", err)
	}
	if !exists {
		return version.ErrNotFound
	}
	return version.ErrStageConflict
}

func nullableSuspendedFrom(stage *version.Stage) any {
	if stage == nil {
		return nil
	}
	return string(*stage)
}

var _ version.StageRepository = (*Repository)(nil)
