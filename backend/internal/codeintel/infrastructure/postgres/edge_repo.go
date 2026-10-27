package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// edgeColumnList is the shared column order of every edge query and the order
// scanEdge expects.
const edgeColumnList = `id, repo_id, commit_sha, src_entity_id, dst_entity_id, rel,
	source, confidence, evidence, meta, fact_key, fact_version, analysis_id, created_at`

// scanEdge scans one row in edgeColumnList order and decodes the JSONB columns.
func scanEdge(row rowScanner) (domain.CodeEdge, error) {
	var (
		edge     domain.CodeEdge
		evidence []byte
		meta     []byte
	)
	if err := row.Scan(
		&edge.ID,
		&edge.RepoID,
		&edge.Commit,
		&edge.SrcEntityID,
		&edge.DstEntityID,
		&edge.Rel,
		&edge.Source,
		&edge.Confidence,
		&evidence,
		&meta,
		&edge.FactKey,
		&edge.FactVersion,
		&edge.AnalysisID,
		&edge.CreatedAt,
	); err != nil {
		return domain.CodeEdge{}, err
	}
	decodedEvidence, err := decodeEvidence(evidence)
	if err != nil {
		return domain.CodeEdge{}, fmt.Errorf("decode edge evidence: %w", err)
	}
	edge.Evidence = decodedEvidence
	edge.Meta = meta
	return edge, nil
}

// EdgeRepo implements domain.EdgeRepository on a pgx pool.
type EdgeRepo struct {
	pool *pgxpool.Pool
}

// NewEdgeRepository returns a code-edge repository backed by pg.
func NewEdgeRepository(pg *pgxpool.Pool) *EdgeRepo {
	return &EdgeRepo{pool: pg}
}

var _ domain.EdgeRepository = (*EdgeRepo)(nil)

// preparedEdge carries one fully encoded edge of a replacement batch.
type preparedEdge struct {
	id         int64
	src        int64
	dst        int64
	rel        string
	source     string
	confidence string
	evidence   string
	meta       string
	analysisID *int64
}

// ReplaceFactEdges atomically replaces all edges of one fact version: the
// previous set is deleted inside the same transaction that inserts the new one
// (design spec §6.11). The fact_key/fact_version of every inserted edge come
// from the method arguments, so a caller cannot fan an edge into another fact
// version.
func (r *EdgeRepo) ReplaceFactEdges(ctx context.Context, repoID int64, commit, factKey string, version int, edges []domain.CodeEdge) error {
	prepared := make([]preparedEdge, 0, len(edges))
	for _, edge := range edges {
		encodedEvidence, err := marshalEvidence(edge.Evidence)
		if err != nil {
			return fmt.Errorf("postgres: encode edge evidence: %w", err)
		}
		id := edge.ID
		if id == 0 {
			id = nextRepoID()
		}
		source := edge.Source
		if source == "" {
			source = domain.SourceAI
		}
		confidence := edge.Confidence
		if confidence == "" {
			confidence = domain.ConfidenceMedium
		}
		prepared = append(prepared, preparedEdge{
			id:         id,
			src:        edge.SrcEntityID,
			dst:        edge.DstEntityID,
			rel:        edge.Rel,
			source:     source,
			confidence: confidence,
			evidence:   encodedEvidence,
			meta:       jsonParam(edge.Meta, jsonFallbackEmptyObject),
			analysisID: edge.AnalysisID,
		})
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin replace fact edges: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`DELETE FROM code_edges
		 WHERE repo_id = $1 AND commit_sha = $2 AND fact_key = $3 AND fact_version = $4`,
		repoID, commit, factKey, version); err != nil {
		return fmt.Errorf("postgres: delete fact edges: %w", err)
	}
	if len(prepared) > 0 {
		const insert = `INSERT INTO code_edges
			(id, repo_id, commit_sha, src_entity_id, dst_entity_id, rel, source,
			 confidence, evidence, meta, fact_key, fact_version, analysis_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10::jsonb, $11, $12, $13)
			ON CONFLICT DO NOTHING`
		batch := &pgx.Batch{}
		for _, edge := range prepared {
			batch.Queue(insert, edge.id, repoID, commit, edge.src, edge.dst,
				edge.rel, edge.source, edge.confidence, edge.evidence, edge.meta,
				factKey, version, edge.analysisID)
		}
		results := tx.SendBatch(ctx, batch)
		for range prepared {
			if _, err := results.Exec(); err != nil {
				_ = results.Close()
				return fmt.Errorf("postgres: insert fact edge: %w", err)
			}
		}
		if err := results.Close(); err != nil {
			return fmt.Errorf("postgres: insert fact edges: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit replace fact edges: %w", err)
	}
	return nil
}

// ListByEntity returns the edges that touch entityID either as source or as
// destination, ordered by id.
func (r *EdgeRepo) ListByEntity(ctx context.Context, repoID int64, commit string, entityID int64) ([]domain.CodeEdge, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+edgeColumnList+`
		 FROM code_edges
		 WHERE repo_id = $1 AND commit_sha = $2
		   AND (src_entity_id = $3 OR dst_entity_id = $3)
		 ORDER BY id ASC`,
		repoID, commit, entityID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list edges by entity: %w", err)
	}
	return scanEdges(rows)
}

// ListByRel returns every edge of one relation at a commit, ordered by id.
func (r *EdgeRepo) ListByRel(ctx context.Context, repoID int64, commit, rel string) ([]domain.CodeEdge, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+edgeColumnList+`
		 FROM code_edges
		 WHERE repo_id = $1 AND commit_sha = $2 AND rel = $3
		 ORDER BY id ASC`,
		repoID, commit, rel)
	if err != nil {
		return nil, fmt.Errorf("postgres: list edges by relation: %w", err)
	}
	return scanEdges(rows)
}

// scanEdges drains an edge query.
func scanEdges(rows pgx.Rows) ([]domain.CodeEdge, error) {
	defer rows.Close()
	edges := make([]domain.CodeEdge, 0)
	for rows.Next() {
		edge, err := scanEdge(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan code edge: %w", err)
		}
		edges = append(edges, edge)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list code edges: %w", err)
	}
	return edges, nil
}
