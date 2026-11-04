// Package versionpg is the PostgreSQL adapter for the 0.2.1 version-context
// stores (version_fact and version_plan_item). The legacy version aggregate
// adapter remains in the parent infrastructure package; both packages share the
// same database schema.
package versionpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const factColumns = `id, version_id, kind, key, payload, confidence, source,
	source_ref, evidence_refs, content_hash, superseded_by`

// versionAdvisoryLockSQL is the version-scoped serialization lock shared with
// the legacy version adapter and the execution intent path:
// pg_advisory_xact_lock(hashtext('eztest:version:' || version_id)). Fact
// appends and arbitration take it before touching version_fact, so a conflict
// is decided against a committed current row instead of a racing snapshot.
const versionAdvisoryLockSQL = `SELECT pg_advisory_xact_lock(hashtext('eztest:version:' || ($1::bigint)::text))`

func lockVersionTx(ctx context.Context, tx pgx.Tx, versionID int64, op string) error {
	if _, err := tx.Exec(ctx, versionAdvisoryLockSQL, versionID); err != nil {
		return fmt.Errorf("%s: lock version advisory: %w", op, err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

// FactRepository stores the append-only version_fact rows (spec §6.1, D28).
// It only INSERTs facts and, during arbitration, writes superseded_by; it never
// deletes a row and never rewrites a fact body.
type FactRepository struct {
	pool *pgxpool.Pool
	ids  platformid.Generator
}

// NewFactRepository wraps a pgx pool. ids supplies primary keys when the
// application service did not assign one.
func NewFactRepository(pool *pgxpool.Pool, ids platformid.Generator) *FactRepository {
	return &FactRepository{pool: pool, ids: ids}
}

// Record appends one fact. The unique (version_id, kind, key, content_hash)
// index deduplicates an identical fact (the stored row is returned, conflict
// false). That dedupe is intentional idempotency (Lead ruling C14): the stored
// row may be a superseded historical fact, and it is returned unchanged --
// never resurrected, rewritten or treated as the new current value -- so a
// higher confidence on the replayed payload cannot supersede the current row.
// Callers detect the dedupe by comparing the assigned ID with the returned ID;
// the application service exposes it as FactRecordOutcome.AlreadyRecorded.
//
// A different hash under the same (version, kind, key) is resolved by
// versionapp.ResolveFactAppend inside the same transaction as the append: a
// not-lower confidence sets the old row's superseded_by to the new fact, a
// lower/unknown confidence stores the new row already superseded by the old
// one, and two high-confidence contradictions stay current and report
// conflict=true for the host to arbitrate. Once an unresolved conflict group
// exists, a further append joins it as current regardless of confidence (the
// pinned conflict-group policy in ResolveFactAppend).
func (r *FactRepository) Record(ctx context.Context, fact version.Fact) (version.Fact, bool, error) {
	if fact.VersionID <= 0 {
		return version.Fact{}, false, fmt.Errorf("record fact: version id must be positive")
	}
	payload, err := encodeFactPayload(fact.Payload)
	if err != nil {
		return version.Fact{}, false, err
	}
	evidence := fact.EvidenceIDs
	if evidence == nil {
		evidence = []int64{}
	}
	if fact.ID == 0 {
		fact.ID = r.ids.Next()
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return version.Fact{}, false, fmt.Errorf("record fact: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockVersionTx(ctx, tx, fact.VersionID, "record fact"); err != nil {
		return version.Fact{}, false, err
	}

	existing, err := scanFact(tx.QueryRow(ctx, `
		SELECT `+factColumns+` FROM version_fact
		WHERE version_id = $1 AND kind = $2 AND key = $3 AND content_hash = $4
	`, fact.VersionID, fact.Kind, fact.Key, fact.ContentHash))
	switch {
	case err == nil:
		// Same (version, kind, key, hash): C14 intentional idempotency. Return
		// the historical row unchanged (it may already be superseded) and write
		// nothing: no supersede of the current row, no conflict, no event.
		return existing, false, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return version.Fact{}, false, fmt.Errorf("record fact: lookup identical fact: %w", err)
	}

	current, err := currentFactsForAppend(ctx, tx, fact.VersionID, fact.Kind, fact.Key, fact.ContentHash)
	if err != nil {
		return version.Fact{}, false, err
	}
	decision := versionapp.ResolveFactAppend(current, fact)
	fact.SupersededBy = decision.SupersedeNewBy

	stored, err := scanFact(tx.QueryRow(ctx, `
		INSERT INTO version_fact (
			id, version_id, kind, key, payload, confidence,
			source, source_ref, evidence_refs, content_hash, superseded_by
		)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10, $11)
		RETURNING `+factColumns+`
	`, fact.ID, fact.VersionID, fact.Kind, fact.Key, payload, fact.Confidence,
		fact.Source, fact.SourceRef, evidence, fact.ContentHash, fact.SupersededBy))
	if err != nil {
		// A concurrent append of the same hash can still win outside this
		// database's advisory lock (another primary); the unique index is the
		// final guard, and the stored row is the correct answer.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case "23505": // unique_violation
				existing, lookupErr := scanFact(tx.QueryRow(ctx, `
					SELECT `+factColumns+` FROM version_fact
					WHERE version_id = $1 AND kind = $2 AND key = $3 AND content_hash = $4
				`, fact.VersionID, fact.Kind, fact.Key, fact.ContentHash))
				if lookupErr == nil {
					return existing, false, nil
				}
			case "23503": // foreign_key_violation: version does not exist
				return version.Fact{}, false, version.ErrNotFound
			}
		}
		return version.Fact{}, false, fmt.Errorf("record fact: insert: %w", err)
	}
	// R-B6-1: the supersede write is in the same transaction as the append, so
	// no observer can ever see both rows current for a non-conflict append.
	for _, oldID := range decision.SupersedeOldIDs {
		tag, updateErr := tx.Exec(ctx, `
			UPDATE version_fact SET superseded_by = $1
			WHERE id = $2 AND superseded_by IS NULL
		`, stored.ID, oldID)
		if updateErr != nil {
			return version.Fact{}, false, fmt.Errorf("record fact: supersede %d: %w", oldID, updateErr)
		}
		if tag.RowsAffected() != 1 {
			return version.Fact{}, false, fmt.Errorf("record fact: supersede %d: current row disappeared", oldID)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return version.Fact{}, false, fmt.Errorf("record fact: commit: %w", err)
	}
	return stored, decision.Conflict, nil
}

// currentFactsForAppend locks and returns the current rows of the same
// (version, kind, key) whose content hash differs from the new fact. The
// version advisory lock plus FOR UPDATE make the append decision stable until
// commit.
func currentFactsForAppend(ctx context.Context, tx pgx.Tx, versionID int64, kind, key, contentHash string) ([]version.Fact, error) {
	rows, err := tx.Query(ctx, `
		SELECT `+factColumns+` FROM version_fact
		WHERE version_id = $1 AND kind = $2 AND key = $3
		  AND superseded_by IS NULL AND content_hash <> $4
		ORDER BY id
		FOR UPDATE
	`, versionID, kind, key, contentHash)
	if err != nil {
		return nil, fmt.Errorf("record fact: current facts: %w", err)
	}
	defer rows.Close()
	items := make([]version.Fact, 0, 2)
	for rows.Next() {
		fact, err := scanFact(rows)
		if err != nil {
			return nil, fmt.Errorf("record fact: scan current fact: %w", err)
		}
		items = append(items, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("record fact: current facts: %w", err)
	}
	return items, nil
}

// Current returns the effective facts of one version (superseded_by IS NULL),
// optionally narrowed to one kind (empty = all kinds), in append order.
func (r *FactRepository) Current(ctx context.Context, versionID int64, kind string) ([]version.Fact, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+factColumns+` FROM version_fact
		WHERE version_id = $1 AND ($2 = '' OR kind = $2) AND superseded_by IS NULL
		ORDER BY id
	`, versionID, kind)
	if err != nil {
		return nil, fmt.Errorf("current facts: %w", err)
	}
	defer rows.Close()

	facts := make([]version.Fact, 0)
	for rows.Next() {
		fact, err := scanFact(rows)
		if err != nil {
			return nil, fmt.Errorf("current facts: scan: %w", err)
		}
		facts = append(facts, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("current facts: %w", err)
	}
	return facts, nil
}

// Arbitrate marks dropID superseded by keepID. Both rows must exist, be
// current and share the same (version_id, kind, key). Only superseded_by is
// written; the losing fact body and every other row are preserved (D28).
//
// Deprecated: this is the legacy pairwise write required by the frozen
// version.FactRepository port. It has no conflict-group selection and no
// version/actor context, so it cannot publish fact.arbitrated or write the
// audit record and bypasses the v0.22 arbitration audit trail. Production code
// must call ArbitrateFact; there are no production callers of this method.
func (r *FactRepository) Arbitrate(ctx context.Context, keepID, dropID int64) error {
	if keepID <= 0 || dropID <= 0 || keepID == dropID {
		return fmt.Errorf("arbitrate facts: keep and drop must be positive and distinct")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("arbitrate facts: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var versionID int64
	if err := tx.QueryRow(ctx, `SELECT version_id FROM version_fact WHERE id = $1`, keepID).Scan(&versionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return version.ErrFactNotFound
		}
		return fmt.Errorf("arbitrate facts: locate keep fact: %w", err)
	}
	if err := lockVersionTx(ctx, tx, versionID, "arbitrate facts"); err != nil {
		return err
	}

	// Lock both rows in a deterministic order so two opposite arbitrations
	// cannot deadlock.
	rows, err := tx.Query(ctx, `
		SELECT `+factColumns+` FROM version_fact
		WHERE id = ANY($1::BIGINT[])
		ORDER BY id
		FOR UPDATE
	`, []int64{keepID, dropID})
	if err != nil {
		return fmt.Errorf("arbitrate facts: lock facts: %w", err)
	}
	facts := make(map[int64]version.Fact, 2)
	for rows.Next() {
		fact, err := scanFact(rows)
		if err != nil {
			rows.Close()
			return fmt.Errorf("arbitrate facts: scan locked fact: %w", err)
		}
		facts[fact.ID] = fact
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("arbitrate facts: lock facts: %w", err)
	}
	keep, ok := facts[keepID]
	if !ok {
		return version.ErrFactNotFound
	}
	drop, ok := facts[dropID]
	if !ok {
		return version.ErrFactNotFound
	}
	if keep.VersionID != drop.VersionID || keep.Kind != drop.Kind || keep.Key != drop.Key {
		return version.ErrFactMismatch
	}
	if keep.SupersededBy != nil || drop.SupersededBy != nil {
		return version.ErrFactSuperseded
	}

	tag, err := tx.Exec(ctx, `
		UPDATE version_fact SET superseded_by = $1
		WHERE id = $2 AND superseded_by IS NULL
	`, keepID, dropID)
	if err != nil {
		return fmt.Errorf("arbitrate facts: supersede drop fact: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return version.ErrFactSuperseded
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("arbitrate facts: commit: %w", err)
	}
	return nil
}

// ArbitrateFact resolves one conflict group under the version advisory lock.
// The pure selection lives in versionapp.ResolveFactArbitration; this adapter
// loads every row of the key FOR UPDATE, lets it decide, and writes only
// superseded_by. History is never deleted and a repeated no-op decision writes
// nothing.
func (r *FactRepository) ArbitrateFact(
	ctx context.Context, versionID, factID int64, decision versionapp.FactArbitrationDecision,
) (versionapp.FactArbitration, error) {
	if versionID <= 0 || factID <= 0 {
		return versionapp.FactArbitration{}, fmt.Errorf("arbitrate fact: ids must be positive")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return versionapp.FactArbitration{}, fmt.Errorf("arbitrate fact: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var targetVersion int64
	var kind, key string
	if err := tx.QueryRow(ctx, `
		SELECT version_id, kind, key FROM version_fact WHERE id = $1
	`, factID).Scan(&targetVersion, &kind, &key); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return versionapp.FactArbitration{}, version.ErrFactNotFound
		}
		return versionapp.FactArbitration{}, fmt.Errorf("arbitrate fact: locate fact: %w", err)
	}
	if targetVersion != versionID {
		return versionapp.FactArbitration{}, version.ErrFactNotFound
	}
	if err := lockVersionTx(ctx, tx, versionID, "arbitrate fact"); err != nil {
		return versionapp.FactArbitration{}, err
	}

	rows, err := tx.Query(ctx, `
		SELECT `+factColumns+` FROM version_fact
		WHERE version_id = $1 AND kind = $2 AND key = $3
		ORDER BY id
		FOR UPDATE
	`, versionID, kind, key)
	if err != nil {
		return versionapp.FactArbitration{}, fmt.Errorf("arbitrate fact: lock facts: %w", err)
	}
	facts := make([]version.Fact, 0, 2)
	for rows.Next() {
		fact, scanErr := scanFact(rows)
		if scanErr != nil {
			rows.Close()
			return versionapp.FactArbitration{}, fmt.Errorf("arbitrate fact: scan fact: %w", scanErr)
		}
		facts = append(facts, fact)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return versionapp.FactArbitration{}, fmt.Errorf("arbitrate fact: lock facts: %w", err)
	}

	outcome, err := versionapp.ResolveFactArbitration(facts, factID, decision)
	if err != nil {
		return versionapp.FactArbitration{}, err
	}
	if !outcome.Changed {
		return outcome, nil
	}
	for _, dropID := range outcome.DroppedIDs {
		tag, updateErr := tx.Exec(ctx, `
			UPDATE version_fact SET superseded_by = $1
			WHERE id = $2 AND superseded_by IS NULL
		`, outcome.KeepID, dropID)
		if updateErr != nil {
			return versionapp.FactArbitration{}, fmt.Errorf("arbitrate fact: supersede %d: %w", dropID, updateErr)
		}
		if tag.RowsAffected() == 0 {
			return versionapp.FactArbitration{}, version.ErrFactSuperseded
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return versionapp.FactArbitration{}, fmt.Errorf("arbitrate fact: commit: %w", err)
	}
	return outcome, nil
}

var _ interface {
	ArbitrateFact(context.Context, int64, int64, versionapp.FactArbitrationDecision) (versionapp.FactArbitration, error)
} = (*FactRepository)(nil)

func encodeFactPayload(payload map[string]any) (string, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode fact payload: %w", err)
	}
	return string(encoded), nil
}

func scanFact(row rowScanner) (version.Fact, error) {
	var fact version.Fact
	var payload []byte
	if err := row.Scan(&fact.ID, &fact.VersionID, &fact.Kind, &fact.Key, &payload,
		&fact.Confidence, &fact.Source, &fact.SourceRef, &fact.EvidenceIDs,
		&fact.ContentHash, &fact.SupersededBy); err != nil {
		return version.Fact{}, err
	}
	if err := json.Unmarshal(payload, &fact.Payload); err != nil {
		return version.Fact{}, fmt.Errorf("decode fact payload: %w", err)
	}
	if fact.Payload == nil {
		fact.Payload = map[string]any{}
	}
	if fact.EvidenceIDs == nil {
		fact.EvidenceIDs = []int64{}
	}
	return fact, nil
}

var _ version.FactRepository = (*FactRepository)(nil)
