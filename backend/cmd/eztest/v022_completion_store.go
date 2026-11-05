package main

// v0.22 W0 fix round 1 (Task 36 review Important 4): the production
// agentapp.CompletionStore. task.complete writes the target plan item
// status/decision and the progress fact in ONE PostgreSQL transaction, under
// the same version advisory lock the fact store already uses, so a crash can
// never leave the plan done without its fact (or vice versa). The plan revision
// optimistic lock is checked under FOR UPDATE; the fact append is idempotent on
// (version_id, kind, key, content_hash), so a retried completion converges.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// completionPlanItemColumns is the plan-item projection the transaction reads
// and returns. It mirrors the version context's plan repository.
const completionPlanItemColumns = `id, version_id, phase, title, status, depends_on,
	work_item_id, decision, sort_order, revision`

// completionAdvisoryLockSQL is the version-scoped serialization lock shared
// with the fact repository (versionAdvisoryLockSQL in the version Postgres
// adapter). Holding it for the whole completion transaction makes the plan and
// fact writes atomic against concurrent fact arbitration.
const completionAdvisoryLockSQL = `SELECT pg_advisory_xact_lock(hashtext('eztest:version:' || ($1::bigint)::text))`

// pgCompletionStore is the real agentapp.CompletionStore adapter (pgx.Tx).
type pgCompletionStore struct {
	pool *pgxpool.Pool
	ids  platformid.Generator
}

var _ agentapp.CompletionStore = pgCompletionStore{}

// CommitCompletion persists one verified completion atomically.
func (s pgCompletionStore) CommitCompletion(ctx context.Context, write agentapp.CompletionWrite) (agentapp.CompletionWriteResult, error) {
	if s.pool == nil || s.ids == nil {
		return agentapp.CompletionWriteResult{}, errors.New("completion store: pool or id generator is not configured")
	}
	if write.VersionID <= 0 || write.PlanItemID <= 0 {
		return agentapp.CompletionWriteResult{}, fmt.Errorf("completion store: version and plan item must be positive")
	}
	status := strings.TrimSpace(write.PlanStatus)
	if status == "" {
		return agentapp.CompletionWriteResult{}, errors.New("completion store: plan status is required")
	}
	if strings.TrimSpace(write.FactKind) == "" || strings.TrimSpace(write.FactKey) == "" {
		return agentapp.CompletionWriteResult{}, errors.New("completion store: fact kind and key are required")
	}
	payload, err := json.Marshal(write.FactPayload)
	if err != nil {
		return agentapp.CompletionWriteResult{}, fmt.Errorf("completion store: encode fact payload: %w", err)
	}
	contentHash, err := versiondomain.FactContentHash(write.FactPayload)
	if err != nil {
		return agentapp.CompletionWriteResult{}, fmt.Errorf("completion store: hash fact payload: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return agentapp.CompletionWriteResult{}, fmt.Errorf("completion store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, completionAdvisoryLockSQL, write.VersionID); err != nil {
		return agentapp.CompletionWriteResult{}, fmt.Errorf("completion store: lock version: %w", err)
	}

	item, err := scanCompletionPlanItem(tx.QueryRow(ctx, `
		SELECT `+completionPlanItemColumns+` FROM version_plan_item
		WHERE id = $1 AND version_id = $2
		FOR UPDATE
	`, write.PlanItemID, write.VersionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return agentapp.CompletionWriteResult{}, fmt.Errorf(
			"completion store: plan item %d not found in version %d", write.PlanItemID, write.VersionID)
	}
	if err != nil {
		return agentapp.CompletionWriteResult{}, fmt.Errorf("completion store: lock plan item: %w", err)
	}

	revision := item.Revision
	decision := write.Decision
	if item.Status != status || item.Decision != decision {
		if err := tx.QueryRow(ctx, `
			UPDATE version_plan_item
			SET status = $3, decision = $4, revision = revision + 1, updated_at = now()
			WHERE id = $1 AND version_id = $2
			RETURNING revision
		`, write.PlanItemID, write.VersionID, status, decision).Scan(&revision); err != nil {
			return agentapp.CompletionWriteResult{}, fmt.Errorf("completion store: write plan item: %w", err)
		}
	}

	factID := s.ids.Next()
	var storedFactID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO version_fact (
			id, version_id, kind, key, payload, confidence,
			source, source_ref, evidence_refs, content_hash, superseded_by
		)
		VALUES ($1, $2, $3, $4, $5::jsonb, NULL, $6, $7, '{}'::bigint[], $8, NULL)
		ON CONFLICT (version_id, kind, key, content_hash) DO NOTHING
		RETURNING id
	`, factID, write.VersionID, write.FactKind, write.FactKey, payload,
		write.Source, write.SourceRef, contentHash).Scan(&storedFactID)
	if errors.Is(err, pgx.ErrNoRows) {
		// The same fact content was already recorded (C14 intentional
		// idempotency): return the stored row instead of a duplicate.
		if err := tx.QueryRow(ctx, `
			SELECT id FROM version_fact
			WHERE version_id = $1 AND kind = $2 AND key = $3 AND content_hash = $4
		`, write.VersionID, write.FactKind, write.FactKey, contentHash).Scan(&storedFactID); err != nil {
			return agentapp.CompletionWriteResult{}, fmt.Errorf("completion store: read idempotent fact: %w", err)
		}
	} else if err != nil {
		return agentapp.CompletionWriteResult{}, fmt.Errorf("completion store: insert fact: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return agentapp.CompletionWriteResult{}, fmt.Errorf("completion store: commit: %w", err)
	}
	return agentapp.CompletionWriteResult{FactID: storedFactID, PlanItemRevision: revision}, nil
}

func scanCompletionPlanItem(row pgx.Row) (versiondomain.PlanItem, error) {
	var item versiondomain.PlanItem
	if err := row.Scan(&item.ID, &item.VersionID, &item.Phase, &item.Title,
		&item.Status, &item.DependsOn, &item.WorkItemID, &item.Decision,
		&item.Order, &item.Revision); err != nil {
		return versiondomain.PlanItem{}, err
	}
	if item.DependsOn == nil {
		item.DependsOn = []int64{}
	}
	return item, nil
}
