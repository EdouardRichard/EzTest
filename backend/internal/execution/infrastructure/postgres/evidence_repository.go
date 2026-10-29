package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// evidenceColumnList is the shared column order of every evidence query and the
// order scanEvidence expects. updated_at is intentionally absent: evidence rows
// are immutable, so the entity has no such field.
const evidenceColumnList = `id, batch_id, case_run_id, step_result_id, kind,
	path, sha256, size_bytes, mime, metadata, created_at`

// EvidenceRepository implements application.EvidenceRepository on a pgx querier.
type EvidenceRepository struct {
	q querier
}

// NewEvidenceRepository returns an evidence repository backed by pool.
func NewEvidenceRepository(pool *pgxpool.Pool) application.EvidenceRepository {
	return &EvidenceRepository{q: pool}
}

var _ application.EvidenceRepository = (*EvidenceRepository)(nil)

// scanEvidence scans one row in evidenceColumnList order and decodes the JSONB
// metadata document.
func scanEvidence(row rowScanner) (domain.Evidence, error) {
	var (
		evidence     domain.Evidence
		caseRunID    *int64
		stepResultID *int64
		metadata     []byte
	)
	if err := row.Scan(
		&evidence.ID,
		&evidence.BatchID,
		&caseRunID,
		&stepResultID,
		&evidence.Kind,
		&evidence.Path,
		&evidence.SHA256,
		&evidence.SizeBytes,
		&evidence.Mime,
		&metadata,
		&evidence.CreatedAt,
	); err != nil {
		return domain.Evidence{}, err
	}
	if err := json.Unmarshal(metadata, &evidence.Metadata); err != nil {
		return domain.Evidence{}, fmt.Errorf("decode evidence metadata: %w", err)
	}
	if evidence.Metadata == nil {
		evidence.Metadata = map[string]any{}
	}
	evidence.CaseRunID = caseRunID
	evidence.StepResultID = stepResultID
	return evidence, nil
}

// Create inserts one immutable evidence row.
func (r *EvidenceRepository) Create(ctx context.Context, evidence domain.Evidence) (domain.Evidence, error) {
	metadata, err := jsonObjectParam(evidence.Metadata)
	if err != nil {
		return domain.Evidence{}, err
	}
	stored, err := scanEvidence(r.q.QueryRow(ctx, `
		INSERT INTO evidence
			(id, batch_id, case_run_id, step_result_id, kind, path, sha256,
			 size_bytes, mime, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb,
		        COALESCE($11, now()), COALESCE($11, now()))
		RETURNING `+evidenceColumnList,
		evidence.ID, evidence.BatchID, nullableInt(evidence.CaseRunID),
		nullableInt(evidence.StepResultID), evidence.Kind, evidence.Path,
		evidence.SHA256, evidence.SizeBytes, evidence.Mime, metadata,
		timeOrNow(evidence.CreatedAt)))
	if err != nil {
		if uniqueConstraint(err) == "pk_evidence" {
			return domain.Evidence{}, fmt.Errorf("%w: evidence %d already exists",
				application.ErrConflict, evidence.ID)
		}
		return domain.Evidence{}, fmt.Errorf("postgres: create evidence: %w", err)
	}
	return stored, nil
}

// Get returns one evidence row.
func (r *EvidenceRepository) Get(ctx context.Context, evidenceID int64) (domain.Evidence, error) {
	evidence, err := scanEvidence(r.q.QueryRow(ctx,
		`SELECT `+evidenceColumnList+` FROM evidence WHERE id = $1`, evidenceID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Evidence{}, evidenceNotFound(evidenceID)
		}
		return domain.Evidence{}, fmt.Errorf("postgres: get evidence: %w", err)
	}
	return evidence, nil
}

// ListByCaseRun returns the artifacts of one run ordered by capture time.
func (r *EvidenceRepository) ListByCaseRun(ctx context.Context, caseRunID int64) ([]domain.Evidence, error) {
	rows, err := r.q.Query(ctx,
		`SELECT `+evidenceColumnList+` FROM evidence
		 WHERE case_run_id = $1 ORDER BY created_at ASC, id ASC`, caseRunID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list evidence: %w", err)
	}
	defer rows.Close()

	items := make([]domain.Evidence, 0)
	for rows.Next() {
		evidence, err := scanEvidence(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan evidence: %w", err)
		}
		items = append(items, evidence)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list evidence: %w", err)
	}
	return items, nil
}

// evidenceNotFound is the id-scoped not-found of the evidence reads.
func evidenceNotFound(evidenceID int64) error {
	return fmt.Errorf("%w: evidence %d", application.ErrEvidenceNotFound, evidenceID)
}
