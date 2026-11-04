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
// false). A different hash under the same (version, kind, key) is appended as
// a second current row and reported with conflict=true; the existing row is
// left untouched for Arbitrate to resolve.
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
		// Same (version, kind, key, hash): append-only dedupe, nothing written.
		return existing, false, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return version.Fact{}, false, fmt.Errorf("record fact: lookup identical fact: %w", err)
	}

	var conflict bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM version_fact
			WHERE version_id = $1 AND kind = $2 AND key = $3
			  AND superseded_by IS NULL AND content_hash <> $4
		)
	`, fact.VersionID, fact.Kind, fact.Key, fact.ContentHash).Scan(&conflict); err != nil {
		return version.Fact{}, false, fmt.Errorf("record fact: detect conflict: %w", err)
	}

	stored, err := scanFact(tx.QueryRow(ctx, `
		INSERT INTO version_fact (
			id, version_id, kind, key, payload, confidence,
			source, source_ref, evidence_refs, content_hash
		)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10)
		RETURNING `+factColumns+`
	`, fact.ID, fact.VersionID, fact.Kind, fact.Key, payload, fact.Confidence,
		fact.Source, fact.SourceRef, evidence, fact.ContentHash))
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
	if err := tx.Commit(ctx); err != nil {
		return version.Fact{}, false, fmt.Errorf("record fact: commit: %w", err)
	}
	return stored, conflict, nil
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
