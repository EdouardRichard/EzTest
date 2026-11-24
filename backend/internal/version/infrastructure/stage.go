package versionpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
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

// stageTransitionFactSource is the version_fact.source of the stage_transition
// fact: the deterministic stage machine is its only writer.
const stageTransitionFactSource = "stage_machine"

// ApplyStage writes one validated transition and, in the SAME transaction,
// appends the durable stage_transition fact (V-0020). The WHERE clause pins the
// source stage to the value the stage machine validated against, so a concurrent
// transition settles as version.ErrStageConflict instead of silently overwriting
// the winner. suspended_from is written from next.SuspendedFrom (non-nil only
// while entering blocked/paused) and cleared to NULL on resume.
//
// Locking: the version advisory lock is taken FIRST (before the version row
// lock), the same order Close and the fact appends use, so the stage write and
// its fact serialize against a concurrent fact append/arbitration on the version
// without a lock-order inversion. The fact INSERT is inlined here rather than
// delegated to FactRepository.Record because Record opens its OWN pool
// transaction and re-takes the same advisory lock: calling it inside this tx
// would self-deadlock on a second connection. One tx, one advisory acquisition.
func (r *Repository) ApplyStage(ctx context.Context, expected version.Stage, next version.StageState) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("apply version stage: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockVersionTx(ctx, tx, next.VersionID, "apply version stage"); err != nil {
		return err
	}

	tag, err := tx.Exec(ctx, `
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
	if tag.RowsAffected() == 0 {
		// No row matched: distinguish a stale source stage from a missing version.
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM version WHERE id = $1)
		`, next.VersionID).Scan(&exists); err != nil {
			return fmt.Errorf("apply version stage: existence check: %w", err)
		}
		if !exists {
			return version.ErrNotFound
		}
		return version.ErrStageConflict
	}

	if err := insertStageTransitionFact(ctx, tx, r.ids, expected, next); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("apply version stage: commit: %w", err)
	}
	return nil
}

// insertStageTransitionFact appends the stage_transition fact inside the stage
// write transaction. The key is the visit's stage_entered_at (RFC3339, UTC) —
// the same value the R49 reader matches on — and the payload is
// {from,to,reason,actor}, hashed with version.FactContentHash. ON CONFLICT DO
// NOTHING keeps a replayed identical transition idempotent (the stage CAS already
// prevents a duplicate, so this is only a belt-and-braces guard).
func insertStageTransitionFact(ctx context.Context, tx pgx.Tx, ids platformid.Generator, from version.Stage, next version.StageState) error {
	payload := map[string]any{
		"from":   string(from),
		"to":     string(next.Stage),
		"reason": next.Reason,
		"actor":  next.Actor,
	}
	hash, err := version.FactContentHash(payload)
	if err != nil {
		return fmt.Errorf("apply version stage: hash stage fact: %w", err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("apply version stage: encode stage fact: %w", err)
	}
	key := next.StageEnteredAt.UTC().Format(time.RFC3339)
	if _, err := tx.Exec(ctx, `
		INSERT INTO version_fact (
			id, version_id, kind, key, payload, confidence,
			source, source_ref, evidence_refs, content_hash, superseded_by
		)
		VALUES ($1, $2, $3, $4, $5::jsonb, NULL, $6, '', '{}'::bigint[], $7, NULL)
		ON CONFLICT (version_id, kind, key, content_hash) DO NOTHING
	`, ids.Next(), next.VersionID, version.FactKindStageTransition, key, string(encoded),
		stageTransitionFactSource, hash); err != nil {
		return wrapDB("apply version stage: insert stage fact", err)
	}
	return nil
}

func nullableSuspendedFrom(stage *version.Stage) any {
	if stage == nil {
		return nil
	}
	return string(*stage)
}

var _ version.StageRepository = (*Repository)(nil)
