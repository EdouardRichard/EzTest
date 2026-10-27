package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// candidateColumnList is the shared column order of every candidate query and
// the order scanCandidate expects. ImpactCandidate carries no created_at, so
// the column stays internal to the table.
const candidateColumnList = `id, analysis_id, subject_kind, subject_id, reason,
	confidence, source, evidence, selected, selection_updated_by,
	selection_updated_at`

// scanCandidate scans one row in candidateColumnList order and decodes the
// JSONB evidence.
func scanCandidate(row rowScanner) (domain.ImpactCandidate, error) {
	var (
		candidate domain.ImpactCandidate
		evidence  []byte
	)
	if err := row.Scan(
		&candidate.ID,
		&candidate.AnalysisID,
		&candidate.SubjectKind,
		&candidate.SubjectID,
		&candidate.Reason,
		&candidate.Confidence,
		&candidate.Source,
		&evidence,
		&candidate.Selected,
		&candidate.SelectionUpdatedBy,
		&candidate.SelectionUpdatedAt,
	); err != nil {
		return domain.ImpactCandidate{}, err
	}
	decodedEvidence, err := decodeEvidence(evidence)
	if err != nil {
		return domain.ImpactCandidate{}, fmt.Errorf("decode candidate evidence: %w", err)
	}
	candidate.Evidence = decodedEvidence
	return candidate, nil
}

// candidateOrder is the deterministic ordering of design spec §6.6 step 5
// (source priority, confidence, subject kind, subject id) with the id as final
// tie-break so the order is total and reproducible (INV-4).
const candidateOrder = `ORDER BY
	CASE source WHEN 'direct' THEN 0 WHEN 'contract' THEN 1 WHEN 'relation' THEN 2 WHEN 'ai' THEN 3 ELSE 4 END,
	CASE confidence WHEN 'high' THEN 0 WHEN 'medium' THEN 1 WHEN 'low' THEN 2 ELSE 3 END,
	CASE subject_kind WHEN 'requirement' THEN 0 WHEN 'app_model' THEN 1 ELSE 2 END,
	subject_id ASC, id ASC`

// CandidateRepo implements domain.CandidateRepository on a pgx pool.
type CandidateRepo struct {
	pool *pgxpool.Pool
}

// NewCandidateRepository returns an impact-candidate repository backed by pg.
func NewCandidateRepository(pg *pgxpool.Pool) *CandidateRepo {
	return &CandidateRepo{pool: pg}
}

var _ domain.CandidateRepository = (*CandidateRepo)(nil)

// preparedCandidate carries one fully encoded candidate of a replacement
// batch.
type preparedCandidate struct {
	id          int64
	subjectKind string
	subjectID   int64
	reason      string
	confidence  string
	source      string
	evidence    string
	selected    bool
	selectedBy  *int64
	selectedAt  *time.Time
}

// Replace atomically replaces the whole candidate set of one analysis: the
// previous rows are deleted inside the same transaction that inserts the new
// ones (design spec §6.11). Duplicate candidates are left to the database's
// unique constraint and skipped.
func (r *CandidateRepo) Replace(ctx context.Context, analysisID int64, items []domain.ImpactCandidate) error {
	prepared := make([]preparedCandidate, 0, len(items))
	for _, item := range items {
		encodedEvidence, err := marshalEvidence(item.Evidence)
		if err != nil {
			return fmt.Errorf("postgres: encode candidate evidence: %w", err)
		}
		id := item.ID
		if id == 0 {
			id = nextRepoID()
		}
		prepared = append(prepared, preparedCandidate{
			id:          id,
			subjectKind: item.SubjectKind,
			subjectID:   item.SubjectID,
			reason:      item.Reason,
			confidence:  item.Confidence,
			source:      item.Source,
			evidence:    encodedEvidence,
			selected:    item.Selected,
			selectedBy:  item.SelectionUpdatedBy,
			selectedAt:  item.SelectionUpdatedAt,
		})
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin replace candidates: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM code_impact_candidates WHERE analysis_id = $1`, analysisID); err != nil {
		return fmt.Errorf("postgres: delete analysis candidates: %w", err)
	}
	if len(prepared) > 0 {
		const insert = `INSERT INTO code_impact_candidates
			(id, analysis_id, subject_kind, subject_id, reason, confidence, source,
			 evidence, selected, selection_updated_by, selection_updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10, $11)
			ON CONFLICT DO NOTHING`
		batch := &pgx.Batch{}
		for _, candidate := range prepared {
			batch.Queue(insert, candidate.id, analysisID, candidate.subjectKind,
				candidate.subjectID, candidate.reason, candidate.confidence,
				candidate.source, candidate.evidence, candidate.selected,
				candidate.selectedBy, candidate.selectedAt)
		}
		results := tx.SendBatch(ctx, batch)
		for range prepared {
			if _, err := results.Exec(); err != nil {
				_ = results.Close()
				return fmt.Errorf("postgres: insert impact candidate: %w", err)
			}
		}
		if err := results.Close(); err != nil {
			return fmt.Errorf("postgres: insert impact candidates: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit replace candidates: %w", err)
	}
	return nil
}

// List returns every candidate of one analysis in the deterministic order of
// design spec §6.6 step 5.
func (r *CandidateRepo) List(ctx context.Context, analysisID int64) ([]domain.ImpactCandidate, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+candidateColumnList+`
		 FROM code_impact_candidates
		 WHERE analysis_id = $1
		 `+candidateOrder,
		analysisID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list impact candidates: %w", err)
	}
	defer rows.Close()

	candidates := make([]domain.ImpactCandidate, 0)
	for rows.Next() {
		candidate, err := scanCandidate(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan impact candidate: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list impact candidates: %w", err)
	}
	return candidates, nil
}

// Get returns one candidate of one analysis.
func (r *CandidateRepo) Get(ctx context.Context, analysisID, candidateID int64) (domain.ImpactCandidate, error) {
	candidate, err := scanCandidate(r.pool.QueryRow(ctx,
		`SELECT `+candidateColumnList+`
		 FROM code_impact_candidates
		 WHERE analysis_id = $1 AND id = $2`,
		analysisID, candidateID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ImpactCandidate{}, notFoundError("candidate", candidateID)
		}
		return domain.ImpactCandidate{}, fmt.Errorf("postgres: get impact candidate: %w", err)
	}
	return candidate, nil
}
