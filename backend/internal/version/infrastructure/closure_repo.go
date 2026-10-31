package versionpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const requirementClosureColumns = `id, requirement_id, requirement_version_id, close_round,
	check_result, evidence_refs, closed_by_run_id, closed_at, reopen_count, created_at, updated_at`

// ClosureRepository stores append-only requirement_closure rounds and the
// requirement close/reopen projections (spec §3.5, §7.1).
type ClosureRepository struct {
	pool *pgxpool.Pool
}

// NewClosureRepository wraps a pgx pool. Closure IDs and check snapshots are
// assigned by the application service.
func NewClosureRepository(pool *pgxpool.Pool) *ClosureRepository {
	return &ClosureRepository{pool: pool}
}

// GetRequirement loads one logical requirement by ID without its aggregate.
// It is defined on the requirement repository because the domain interface is
// version-scoped: the closure service only knows the requirement ID.
func (r *RequirementRepository) GetRequirement(ctx context.Context, requirementID int64) (version.Requirement, error) {
	requirement, err := scanRequirement(r.pool.QueryRow(ctx, `
		SELECT `+requirementColumns+` FROM requirement WHERE id = $1
	`, requirementID))
	if err != nil {
		return version.Requirement{}, wrapRequirementLookup("get requirement", err)
	}
	return requirement, nil
}

// Latest returns the newest closure round of one requirement.
func (r *ClosureRepository) Latest(ctx context.Context, requirementID int64) (version.RequirementClosure, bool, error) {
	closure, err := scanRequirementClosure(r.pool.QueryRow(ctx, `
		SELECT `+requirementClosureColumns+`
		FROM requirement_closure
		WHERE requirement_id = $1
		ORDER BY close_round DESC, id DESC
		LIMIT 1
	`, requirementID))
	if errors.Is(err, pgx.ErrNoRows) {
		return version.RequirementClosure{}, false, nil
	}
	if err != nil {
		return version.RequirementClosure{}, false, fmt.Errorf("get latest requirement closure: %w", err)
	}
	return closure, true, nil
}

// Close inserts the next close round and flips the requirement to closed in one
// transaction. The requirement row is locked first, so concurrent closes
// serialize: the loser sees the committed closed status and gets
// ErrStateConflict. After the lock, recheck re-runs the service predicate
// against the locked requirement; its error rolls the whole transaction back,
// so a closure can never commit with an evaluation that went stale before the
// lock. The unique (requirement_id, close_round) constraint remains the final
// guard.
func (r *ClosureRepository) Close(ctx context.Context, closure version.RequirementClosure, requirement version.Requirement, recheck func(context.Context, version.Requirement) error) (version.RequirementClosure, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return version.RequirementClosure{}, fmt.Errorf("close requirement: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	locked, err := scanRequirement(tx.QueryRow(ctx, `
		SELECT `+requirementColumns+` FROM requirement WHERE id = $1 AND version_id = $2 FOR UPDATE
	`, requirement.ID, requirement.VersionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return version.RequirementClosure{}, version.ErrRequirementNotFound
	}
	if err != nil {
		return version.RequirementClosure{}, fmt.Errorf("close requirement: lock requirement: %w", err)
	}
	if locked.Status != version.RequirementClarified {
		return version.RequirementClosure{}, version.ErrStateConflict
	}
	if recheck != nil {
		if err := recheck(ctx, locked); err != nil {
			return version.RequirementClosure{}, err
		}
	}

	var nextRound int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(max(close_round), 0) + 1 FROM requirement_closure WHERE requirement_id = $1
	`, requirement.ID).Scan(&nextRound); err != nil {
		return version.RequirementClosure{}, fmt.Errorf("close requirement: next close round: %w", err)
	}
	closure.CloseRound = nextRound
	checkResult, err := encodeClosureCheckResult(closure.CheckResult)
	if err != nil {
		return version.RequirementClosure{}, err
	}
	evidenceRefs, err := encodeClosureEvidenceRefs(closure.EvidenceRefs)
	if err != nil {
		return version.RequirementClosure{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO requirement_closure (`+requirementClosureColumns+`)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7, $8, $9, $10, $11)
	`, closure.ID, closure.RequirementID, closure.RequirementVersionID, closure.CloseRound,
		checkResult, evidenceRefs, closure.ClosedByRunID, closure.ClosedAt, closure.ReopenCount,
		closure.CreatedAt, closure.UpdatedAt); err != nil {
		if isUniqueViolation(err) {
			return version.RequirementClosure{}, version.ErrStateConflict
		}
		return version.RequirementClosure{}, wrapDB("insert requirement closure", err)
	}

	tag, err := tx.Exec(ctx, `
		UPDATE requirement
		SET status = 'closed', closed_at = $3, closed_by_run_id = $4, updated_at = $5
		WHERE id = $1 AND version_id = $2 AND status = 'clarified'
	`, requirement.ID, requirement.VersionID, requirement.ClosedAt, requirement.ClosedByRunID, requirement.UpdatedAt)
	if err != nil {
		return version.RequirementClosure{}, wrapDB("close requirement", err)
	}
	if tag.RowsAffected() == 0 {
		return version.RequirementClosure{}, version.ErrStateConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return version.RequirementClosure{}, fmt.Errorf("close requirement: commit: %w", err)
	}
	// The persisted row is rounded to the database timestamp; return the value
	// the service needs for the event payload.
	closure.CloseRound = nextRound
	return closure, nil
}

// Reopen increments the newest closure's reopen_count and flips the requirement
// closed -> draft in one transaction, clearing the close projection fields. It
// returns the updated closure row.
func (r *ClosureRepository) Reopen(ctx context.Context, requirement version.Requirement, reopenedAt time.Time) (version.RequirementClosure, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return version.RequirementClosure{}, fmt.Errorf("reopen requirement: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	err = tx.QueryRow(ctx, `
		SELECT status FROM requirement WHERE id = $1 AND version_id = $2 FOR UPDATE
	`, requirement.ID, requirement.VersionID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return version.RequirementClosure{}, version.ErrRequirementNotFound
	}
	if err != nil {
		return version.RequirementClosure{}, fmt.Errorf("reopen requirement: lock requirement: %w", err)
	}
	if version.RequirementStatus(status) != version.RequirementClosed {
		return version.RequirementClosure{}, version.ErrStateConflict
	}

	closure, err := scanRequirementClosure(tx.QueryRow(ctx, `
		UPDATE requirement_closure
		SET reopen_count = reopen_count + 1, updated_at = $2
		WHERE id = (
			SELECT id FROM requirement_closure
			WHERE requirement_id = $1
			ORDER BY close_round DESC, id DESC
			LIMIT 1
		)
		RETURNING `+requirementClosureColumns+`
	`, requirement.ID, reopenedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return version.RequirementClosure{}, version.ErrStateConflict
	}
	if err != nil {
		return version.RequirementClosure{}, wrapDB("reopen requirement closure", err)
	}

	tag, err := tx.Exec(ctx, `
		UPDATE requirement
		SET status = 'draft', closed_at = NULL, closed_by_run_id = NULL, updated_at = $3
		WHERE id = $1 AND version_id = $2 AND status = 'closed'
	`, requirement.ID, requirement.VersionID, reopenedAt)
	if err != nil {
		return version.RequirementClosure{}, wrapDB("reopen requirement", err)
	}
	if tag.RowsAffected() == 0 {
		return version.RequirementClosure{}, version.ErrStateConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return version.RequirementClosure{}, fmt.Errorf("reopen requirement: commit: %w", err)
	}
	return closure, nil
}

func scanRequirementClosure(row rowScanner) (version.RequirementClosure, error) {
	var (
		closure      version.RequirementClosure
		checkResult  []byte
		evidenceRefs []byte
	)
	if err := row.Scan(&closure.ID, &closure.RequirementID, &closure.RequirementVersionID,
		&closure.CloseRound, &checkResult, &evidenceRefs, &closure.ClosedByRunID,
		&closure.ClosedAt, &closure.ReopenCount, &closure.CreatedAt, &closure.UpdatedAt); err != nil {
		return version.RequirementClosure{}, err
	}
	if len(checkResult) > 0 {
		if err := json.Unmarshal(checkResult, &closure.CheckResult); err != nil {
			return version.RequirementClosure{}, fmt.Errorf("decode requirement closure check_result: %w", err)
		}
	}
	if len(evidenceRefs) > 0 {
		if err := json.Unmarshal(evidenceRefs, &closure.EvidenceRefs); err != nil {
			return version.RequirementClosure{}, fmt.Errorf("decode requirement closure evidence_refs: %w", err)
		}
	}
	if closure.EvidenceRefs == nil {
		closure.EvidenceRefs = []version.ClosureEvidenceRef{}
	}
	return closure, nil
}

func encodeClosureCheckResult(result version.ClosureCheckResult) (string, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("encode requirement closure check_result: %w", err)
	}
	return string(raw), nil
}

func encodeClosureEvidenceRefs(refs []version.ClosureEvidenceRef) (string, error) {
	if refs == nil {
		refs = []version.ClosureEvidenceRef{}
	}
	raw, err := json.Marshal(refs)
	if err != nil {
		return "", fmt.Errorf("encode requirement closure evidence_refs: %w", err)
	}
	return string(raw), nil
}

var _ version.RequirementClosureRepository = (*ClosureRepository)(nil)
