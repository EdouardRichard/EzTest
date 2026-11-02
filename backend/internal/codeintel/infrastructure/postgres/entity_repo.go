package codeintelpg

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EntityRepository stores the current code_entity projection.
type EntityRepository struct {
	pool *pgxpool.Pool
	ids  platformid.Generator
	now  func() time.Time
}

// NewEntityRepository wires the entity projection repository.
func NewEntityRepository(pool *pgxpool.Pool, ids platformid.Generator) *EntityRepository {
	return &EntityRepository{pool: pool, ids: ids, now: time.Now}
}

var _ codeintelapp.EntityRepository = (*EntityRepository)(nil)

const entityColumns = `id, repo_id, commit_sha, index_run_id, kind, path, name, signature,
	start_line, end_line, blob_hash, source, confidence, evidence, meta, fact_key,
	fact_version, analysis_id, created_at, updated_at`

func scanEntity(row rowScanner) (codeintel.Entity, error) {
	var entity codeintel.Entity
	var kind, confidence, source string
	var evidence, meta []byte
	if err := row.Scan(&entity.ID, &entity.RepoID, &entity.CommitSHA, &entity.IndexRunID,
		&kind, &entity.Path, &entity.Name, &entity.Signature, &entity.StartLine, &entity.EndLine,
		&entity.BlobHash, &source, &confidence, &evidence, &meta, &entity.FactKey,
		&entity.FactVersion, &entity.AnalysisID, &entity.CreatedAt, &entity.UpdatedAt); err != nil {
		return codeintel.Entity{}, err
	}
	entity.Kind = codeintel.EntityKind(kind)
	entity.Source = source
	entity.Confidence = codeintel.Confidence(confidence)
	entity.Evidence = evidence
	entity.Meta = meta
	return entity, nil
}

// UpsertFileEntities replaces the (repo, commit) file projection inside one
// transaction. Re-indexing the same commit is therefore idempotent.
func (r *EntityRepository) UpsertFileEntities(ctx context.Context, runID, repoID int64, commitSHA string, entries []codeintel.TreeEntry) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return wrapDB("begin file entity replace", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		DELETE FROM code_entity
		WHERE repo_id = $1 AND commit_sha = $2 AND kind = 'file' AND source = 'git'
	`, repoID, commitSHA); err != nil {
		return wrapDB("delete file entities", err)
	}
	if len(entries) > 0 {
		batch := &pgx.Batch{}
		now := r.now()
		for _, entry := range entries {
			meta := fmt.Sprintf(`{"size_bytes":%d,"binary":%t,"unsupported":%t,"mode":%q}`,
				entry.Size, entry.Binary, entry.Unsupported, entry.Mode)
			blobHash := ""
			if entry.BlobSHA != "" {
				blobHash = "git-sha1:" + entry.BlobSHA
			}
			batch.Queue(`
				INSERT INTO code_entity (id, repo_id, commit_sha, index_run_id, kind, path, name,
					blob_hash, source, confidence, evidence, meta, fact_key, fact_version,
					created_at, updated_at)
				VALUES ($1, $2, $3, $4, 'file', $5, $6, $7, 'git', 'high', '[]'::jsonb, $8::jsonb,
					'', 1, $9, $9)
			`, r.ids.Next(), repoID, commitSHA, nullableInt64(indexRunIDPtr(runID)), entry.Path,
				path.Base(entry.Path), blobHash, meta, now)
		}
		results := tx.SendBatch(ctx, batch)
		for range entries {
			if _, err := results.Exec(); err != nil {
				_ = results.Close()
				return wrapDB("insert file entity", err)
			}
		}
		if err := results.Close(); err != nil {
			return wrapDB("close file entity batch", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return wrapDB("commit file entity replace", err)
	}
	return nil
}

// ListTree pages the indexed file projection by path cursor.
func (r *EntityRepository) ListTree(ctx context.Context, repoID int64, commitSHA, pathPrefix, cursor string, limit int) ([]codeintel.Entity, string, bool, error) {
	args := []any{repoID, commitSHA}
	where := `repo_id = $1 AND commit_sha = $2 AND kind = 'file' AND source = 'git'`
	if pathPrefix != "" {
		args = append(args, pathPrefix)
		where += fmt.Sprintf(` AND (path = $%d OR path LIKE $%d || '/%%')`, len(args), len(args))
	}
	if cursor != "" {
		args = append(args, cursor)
		where += fmt.Sprintf(` AND path > $%d`, len(args))
	}
	args = append(args, limit+1)
	query := `SELECT ` + entityColumns + ` FROM code_entity WHERE ` + where +
		fmt.Sprintf(` ORDER BY path ASC, id ASC LIMIT $%d`, len(args))
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", false, wrapDB("list code tree", err)
	}
	defer rows.Close()

	entities := make([]codeintel.Entity, 0, limit)
	for rows.Next() {
		entity, scanErr := scanEntity(rows)
		if scanErr != nil {
			return nil, "", false, wrapDB("scan code tree entity", scanErr)
		}
		entities = append(entities, entity)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, wrapDB("iterate code tree", err)
	}
	hasMore := len(entities) > limit
	if hasMore {
		entities = entities[:limit]
	}
	nextCursor := ""
	if hasMore && len(entities) > 0 {
		nextCursor = entities[len(entities)-1].Path
	}
	return entities, nextCursor, hasMore, nil
}

// Search finds entities whose path or name contains query (case-insensitive).
func (r *EntityRepository) Search(ctx context.Context, repoID int64, commitSHA, query, kind string, limit int) ([]codeintel.Entity, error) {
	args := []any{repoID, commitSHA, strings.ToLower(query)}
	where := `repo_id = $1 AND commit_sha = $2 AND
		(STRPOS(LOWER(path), $3) > 0 OR STRPOS(LOWER(name), $3) > 0)`
	if kind != "" {
		args = append(args, kind)
		where += fmt.Sprintf(` AND kind = $%d`, len(args))
	}
	args = append(args, limit)
	rows, err := r.pool.Query(ctx, `SELECT `+entityColumns+` FROM code_entity WHERE `+where+
		fmt.Sprintf(` ORDER BY path ASC, id ASC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, wrapDB("search code entities", err)
	}
	defer rows.Close()

	entities := make([]codeintel.Entity, 0, limit)
	for rows.Next() {
		entity, scanErr := scanEntity(rows)
		if scanErr != nil {
			return nil, wrapDB("scan searched entity", scanErr)
		}
		entities = append(entities, entity)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("iterate searched entities", err)
	}
	return entities, nil
}

// EntitiesByFactKeys loads the current projection of the given fact keys.
func (r *EntityRepository) EntitiesByFactKeys(ctx context.Context, repoID int64, commitSHA string, factKeys []string) ([]codeintel.Entity, error) {
	if len(factKeys) == 0 {
		return []codeintel.Entity{}, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+entityColumns+` FROM code_entity
		WHERE repo_id = $1 AND commit_sha = $2 AND fact_key = ANY($3::text[])
		ORDER BY path ASC, id ASC
	`, repoID, commitSHA, factKeys)
	if err != nil {
		return nil, wrapDB("list entities by fact key", err)
	}
	defer rows.Close()

	entities := make([]codeintel.Entity, 0, len(factKeys))
	for rows.Next() {
		entity, scanErr := scanEntity(rows)
		if scanErr != nil {
			return nil, wrapDB("scan entity by fact key", scanErr)
		}
		entities = append(entities, entity)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("iterate entities by fact key", err)
	}
	return entities, nil
}

// UpsertFactEntity maintains the current projection of one AI fact.
func (r *EntityRepository) UpsertFactEntity(ctx context.Context, entity codeintel.Entity) (codeintel.Entity, error) {
	if strings.TrimSpace(entity.FactKey) == "" {
		return codeintel.Entity{}, codeintel.ErrInvalid
	}
	now := r.now()
	stored, err := scanEntity(r.pool.QueryRow(ctx, `
		INSERT INTO code_entity (id, repo_id, commit_sha, index_run_id, kind, path, name, signature,
			start_line, end_line, blob_hash, source, confidence, evidence, meta, fact_key,
			fact_version, analysis_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14::jsonb, $15::jsonb,
			$16, $17, $18, $19, $19)
		ON CONFLICT (repo_id, commit_sha, fact_key) WHERE fact_key <> '' DO UPDATE
		SET index_run_id = EXCLUDED.index_run_id,
		    kind = EXCLUDED.kind,
		    path = EXCLUDED.path,
		    name = EXCLUDED.name,
		    signature = EXCLUDED.signature,
		    start_line = EXCLUDED.start_line,
		    end_line = EXCLUDED.end_line,
		    blob_hash = EXCLUDED.blob_hash,
		    source = EXCLUDED.source,
		    confidence = EXCLUDED.confidence,
		    evidence = EXCLUDED.evidence,
		    meta = EXCLUDED.meta,
		    fact_version = EXCLUDED.fact_version,
		    analysis_id = EXCLUDED.analysis_id,
		    updated_at = EXCLUDED.updated_at
		RETURNING `+entityColumns,
		r.ids.Next(), entity.RepoID, entity.CommitSHA, nullableInt64(entity.IndexRunID), string(entity.Kind),
		entity.Path, entity.Name, entity.Signature, entity.StartLine, entity.EndLine, entity.BlobHash,
		entity.Source, string(entity.Confidence), jsonText(entity.Evidence, `[]`), jsonText(entity.Meta, `{}`),
		entity.FactKey, entity.FactVersion, nullableInt64(entity.AnalysisID), now))
	if err != nil {
		return codeintel.Entity{}, wrapDB("upsert fact entity", err)
	}
	return stored, nil
}

// UpsertEdge inserts one relation between two current entities; replaying the
// same (src, dst, rel) triple is a no-op.
func (r *EntityRepository) UpsertEdge(ctx context.Context, edge codeintel.Edge) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO code_edge (id, repo_id, commit_sha, analysis_id, src_entity_id, dst_entity_id,
			rel, source, confidence, evidence, meta, fact_key, fact_version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11::jsonb, $12, $13, $14, $14)
		ON CONFLICT (src_entity_id, dst_entity_id, rel) DO NOTHING
	`, r.ids.Next(), edge.RepoID, edge.CommitSHA, nullableInt64(edge.AnalysisID),
		edge.SrcEntityID, edge.DstEntityID, string(edge.Rel), edge.Source, string(edge.Confidence),
		jsonText(edge.Evidence, `[]`), jsonText(edge.Meta, `{}`), edge.FactKey, edge.FactVersion,
		r.now())
	if err != nil {
		return wrapDB("upsert code edge", err)
	}
	return nil
}

func indexRunIDPtr(runID int64) *int64 {
	if runID <= 0 {
		return nil
	}
	value := runID
	return &value
}
