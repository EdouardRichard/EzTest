package codeintelpg

import (
	"context"
	"encoding/json"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CandidateRepository stores code_impact_candidate rows. ReplaceForAnalysis
// atomically replaces the candidate set; selection updates are the only mutable
// columns.
type CandidateRepository struct {
	pool *pgxpool.Pool
	ids  platformid.Generator
}

// NewCandidateRepository wires the impact-candidate repository.
func NewCandidateRepository(pool *pgxpool.Pool, ids platformid.Generator) *CandidateRepository {
	return &CandidateRepository{pool: pool, ids: ids}
}

var _ codeintelapp.CandidateRepository = (*CandidateRepository)(nil)

const candidateColumns = `id, analysis_id, subject_kind, subject_id, reason, confidence,
	source, evidence, selected, selection_updated_by, selection_updated_at, created_at, updated_at`

func scanCandidate(row rowScanner) (codeintel.ImpactCandidate, error) {
	var candidate codeintel.ImpactCandidate
	var subjectKind, confidence, source string
	var evidence []byte
	if err := row.Scan(&candidate.ID, &candidate.AnalysisID, &subjectKind, &candidate.SubjectID,
		&candidate.Reason, &confidence, &source, &evidence, &candidate.Selected,
		&candidate.SelectionUpdatedBy, &candidate.SelectionUpdatedAt,
		&candidate.CreatedAt, &candidate.UpdatedAt); err != nil {
		return codeintel.ImpactCandidate{}, err
	}
	candidate.SubjectKind = codeintel.SubjectKind(subjectKind)
	candidate.Confidence = codeintel.Confidence(confidence)
	candidate.Source = codeintel.CandidateSource(source)
	candidate.Evidence = json.RawMessage(evidence)
	return candidate, nil
}

// ReplaceForAnalysis deletes the previous candidate set and inserts the new one
// in a single transaction.
func (r *CandidateRepository) ReplaceForAnalysis(ctx context.Context, analysisID int64, candidates []codeintel.ImpactCandidate) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return wrapDB("begin impact candidate replace", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM code_impact_candidate WHERE analysis_id = $1`, analysisID); err != nil {
		return wrapDB("delete impact candidates", err)
	}
	for _, candidate := range candidates {
		if _, err := tx.Exec(ctx, `
			INSERT INTO code_impact_candidate (id, analysis_id, subject_kind, subject_id, reason,
				confidence, source, evidence, selected, selection_updated_by, selection_updated_at,
				created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10, $11, $12, $13)
		`, r.ids.Next(), analysisID, string(candidate.SubjectKind), candidate.SubjectID,
			candidate.Reason, string(candidate.Confidence), string(candidate.Source),
			jsonText(candidate.Evidence, `[]`), candidate.Selected, candidate.SelectionUpdatedBy,
			candidate.SelectionUpdatedAt, candidate.CreatedAt, candidate.UpdatedAt); err != nil {
			return wrapDB("create impact candidate", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return wrapDB("commit impact candidate replace", err)
	}
	return nil
}

// ListByAnalysis returns the stored candidates in creation order.
func (r *CandidateRepository) ListByAnalysis(ctx context.Context, analysisID int64) ([]codeintel.ImpactCandidate, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+candidateColumns+` FROM code_impact_candidate
		WHERE analysis_id = $1
		ORDER BY id ASC
	`, analysisID)
	if err != nil {
		return nil, wrapDB("list impact candidates", err)
	}
	defer rows.Close()
	candidates := make([]codeintel.ImpactCandidate, 0)
	for rows.Next() {
		candidate, scanErr := scanCandidate(rows)
		if scanErr != nil {
			return nil, wrapDB("scan impact candidate", scanErr)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list impact candidates", err)
	}
	return candidates, nil
}

// UpdateSelection records one explicit selection change.
func (r *CandidateRepository) UpdateSelection(ctx context.Context, analysisID, candidateID, actorID int64, selected bool) (codeintel.ImpactCandidate, error) {
	candidate, err := scanCandidate(r.pool.QueryRow(ctx, `
		UPDATE code_impact_candidate
		SET selected = $3, selection_updated_by = $4, selection_updated_at = now(), updated_at = now()
		WHERE id = $2 AND analysis_id = $1
		RETURNING `+candidateColumns,
		analysisID, candidateID, selected, actorID))
	if err != nil {
		return codeintel.ImpactCandidate{}, wrapDB("update impact candidate selection", err)
	}
	return candidate, nil
}

// ImpactEdgeRepository reads the one-hop contract edges of one commit (spec
// §5.2). Both endpoint fact keys are joined from the entity projection.
type ImpactEdgeRepository struct {
	pool *pgxpool.Pool
}

// NewImpactEdgeRepository wires the impact edge reader.
func NewImpactEdgeRepository(pool *pgxpool.Pool) *ImpactEdgeRepository {
	return &ImpactEdgeRepository{pool: pool}
}

var _ codeintelapp.ImpactEdgeReader = (*ImpactEdgeRepository)(nil)

// ImpactEdges returns the route_to_api edges whose endpoints both carry a fact
// key, ordered by edge id.
func (r *ImpactEdgeRepository) ImpactEdges(ctx context.Context, repoID int64, commitSHA string) ([]codeintelapp.ImpactEdge, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT src.fact_key, dst.fact_key, e.rel, e.confidence
		FROM code_edge e
		JOIN code_entity src ON src.id = e.src_entity_id
		JOIN code_entity dst ON dst.id = e.dst_entity_id
		WHERE e.repo_id = $1 AND e.commit_sha = $2 AND e.rel = 'route_to_api'
		  AND src.fact_key <> '' AND dst.fact_key <> ''
		ORDER BY e.id ASC
	`, repoID, commitSHA)
	if err != nil {
		return nil, wrapDB("list impact edges", err)
	}
	defer rows.Close()
	edges := make([]codeintelapp.ImpactEdge, 0)
	for rows.Next() {
		var edge codeintelapp.ImpactEdge
		var rel, confidence string
		if err := rows.Scan(&edge.SrcFactKey, &edge.DstFactKey, &rel, &confidence); err != nil {
			return nil, wrapDB("scan impact edge", err)
		}
		edge.Rel = codeintel.EdgeRel(rel)
		edge.Confidence = codeintel.Confidence(confidence)
		edges = append(edges, edge)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list impact edges", err)
	}
	return edges, nil
}
