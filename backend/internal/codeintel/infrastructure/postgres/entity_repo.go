package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// entityColumnList is the shared column order of every entity query and the
// order scanEntity expects.
const entityColumnList = `id, repo_id, commit_sha, kind, path, name, start_line, end_line,
	signature, blob_hash, source, confidence, evidence, meta,
	fact_key, fact_version, analysis_id, created_at, updated_at`

// marshalEvidence encodes the fact/edge evidence as the JSONB document stored
// in the codeintel tables. An empty list is stored as [].
func marshalEvidence(items []domain.Evidence) (string, error) {
	if items == nil {
		return jsonFallbackEmptyArray, nil
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// decodeEvidence decodes a stored evidence document.
func decodeEvidence(raw []byte) ([]domain.Evidence, error) {
	if len(raw) == 0 {
		return []domain.Evidence{}, nil
	}
	var items []domain.Evidence
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	if items == nil {
		items = []domain.Evidence{}
	}
	return items, nil
}

// scanEntity scans one row in entityColumnList order and decodes the JSONB
// columns.
func scanEntity(row rowScanner) (domain.CodeEntity, error) {
	var (
		entity   domain.CodeEntity
		evidence []byte
		meta     []byte
	)
	if err := row.Scan(
		&entity.ID,
		&entity.RepoID,
		&entity.Commit,
		&entity.Kind,
		&entity.Path,
		&entity.Name,
		&entity.StartLine,
		&entity.EndLine,
		&entity.Signature,
		&entity.BlobHash,
		&entity.Source,
		&entity.Confidence,
		&evidence,
		&meta,
		&entity.FactKey,
		&entity.FactVersion,
		&entity.AnalysisID,
		&entity.CreatedAt,
		&entity.UpdatedAt,
	); err != nil {
		return domain.CodeEntity{}, err
	}
	decodedEvidence, err := decodeEvidence(evidence)
	if err != nil {
		return domain.CodeEntity{}, fmt.Errorf("decode entity evidence: %w", err)
	}
	entity.Evidence = decodedEvidence
	entity.Meta = meta
	return entity, nil
}

// EntityRepo implements domain.EntityRepository on a pgx pool.
type EntityRepo struct {
	pool *pgxpool.Pool
}

// NewEntityRepository returns a code-entity repository backed by pg.
func NewEntityRepository(pg *pgxpool.Pool) *EntityRepo {
	return &EntityRepo{pool: pg}
}

var _ domain.EntityRepository = (*EntityRepo)(nil)

// upsertEntityColumns is the shared INSERT column list of the single-row entity
// upserts.
const upsertEntityColumns = `INSERT INTO code_entities
	(id, repo_id, commit_sha, kind, path, name, start_line, end_line, signature,
	 blob_hash, source, confidence, evidence, meta, fact_key, fact_version, analysis_id)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::jsonb, $14::jsonb, $15, $16, $17)`

// Upsert stores e as the current snapshot of its fact. When e carries a
// fact_key, (repo_id, commit_sha, fact_key) is the identity: the current row is
// updated in place (its id and created_at are preserved). Without a fact_key the
// row is treated as a T0 file entity and (repo_id, commit_sha, path) is the
// identity. The full fact body history lives in code_fact_versions.
func (r *EntityRepo) Upsert(ctx context.Context, e domain.CodeEntity) error {
	encodedEvidence, err := marshalEvidence(e.Evidence)
	if err != nil {
		return fmt.Errorf("postgres: encode entity evidence: %w", err)
	}
	meta := jsonParam(e.Meta, jsonFallbackEmptyObject)

	var query string
	if e.FactKey != nil {
		query = upsertEntityColumns + `
		ON CONFLICT (repo_id, commit_sha, fact_key) WHERE fact_key IS NOT NULL
		DO UPDATE SET
			kind = EXCLUDED.kind,
			path = EXCLUDED.path,
			name = EXCLUDED.name,
			start_line = EXCLUDED.start_line,
			end_line = EXCLUDED.end_line,
			signature = EXCLUDED.signature,
			blob_hash = EXCLUDED.blob_hash,
			source = EXCLUDED.source,
			confidence = EXCLUDED.confidence,
			evidence = EXCLUDED.evidence,
			meta = EXCLUDED.meta,
			fact_version = EXCLUDED.fact_version,
			analysis_id = EXCLUDED.analysis_id,
			updated_at = now()`
	} else {
		query = upsertEntityColumns + `
		ON CONFLICT (repo_id, commit_sha, path) WHERE kind = 'file' AND source = 'git'
		DO UPDATE SET
			name = EXCLUDED.name,
			signature = EXCLUDED.signature,
			blob_hash = EXCLUDED.blob_hash,
			confidence = EXCLUDED.confidence,
			evidence = EXCLUDED.evidence,
			meta = EXCLUDED.meta,
			updated_at = now()`
	}
	if _, err := r.pool.Exec(ctx, query,
		e.ID, e.RepoID, e.Commit, e.Kind, e.Path, e.Name,
		e.StartLine, e.EndLine, e.Signature, e.BlobHash, e.Source, e.Confidence,
		encodedEvidence, meta, e.FactKey, e.FactVersion, e.AnalysisID,
	); err != nil {
		return fmt.Errorf("postgres: upsert code entity: %w", err)
	}
	return nil
}

// UpsertFile stores the T0 file entities of one index run in a single
// statement. The identity is (repo_id, commit_sha, path): an existing row keeps
// its id and created_at while its blob hash is refreshed. Every item must carry
// the caller-supplied Snowflake id; a zero id is rejected with
// ErrInvalid/missing_id before any row is written (D29).
func (r *EntityRepo) UpsertFile(ctx context.Context, repoID int64, commit string, files []domain.CodeEntity) error {
	if len(files) == 0 {
		return nil
	}
	ids := make([]int64, len(files))
	paths := make([]string, len(files))
	blobHashes := make([]string, len(files))
	for i, file := range files {
		if file.ID == 0 {
			return missingIDError("file entity")
		}
		ids[i] = file.ID
		paths[i] = file.Path
		blobHashes[i] = file.BlobHash
	}
	const query = `INSERT INTO code_entities
			(id, repo_id, commit_sha, kind, path, blob_hash, source, confidence)
		SELECT f.id, $2::bigint, $3::text, 'file', f.path, f.blob_hash, 'git', 'high'
		FROM unnest($1::bigint[], $4::text[], $5::text[]) AS f(id, path, blob_hash)
		ON CONFLICT (repo_id, commit_sha, path) WHERE kind = 'file' AND source = 'git'
		DO UPDATE SET blob_hash = EXCLUDED.blob_hash, updated_at = now()`
	if _, err := r.pool.Exec(ctx, query, ids, repoID, commit, paths, blobHashes); err != nil {
		return fmt.Errorf("postgres: upsert file entities: %w", err)
	}
	return nil
}

// GetByFactKey returns the current entity snapshot of one fact at a commit.
func (r *EntityRepo) GetByFactKey(ctx context.Context, repoID int64, commit, factKey string) (domain.CodeEntity, error) {
	entity, err := scanEntity(r.pool.QueryRow(ctx,
		`SELECT `+entityColumnList+`
		 FROM code_entities
		 WHERE repo_id = $1 AND commit_sha = $2 AND fact_key = $3`,
		repoID, commit, factKey))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CodeEntity{}, domain.NewNotFound("entity_not_found",
				fmt.Sprintf("codeintel: fact %q not found at commit %s", factKey, commit))
		}
		return domain.CodeEntity{}, fmt.Errorf("postgres: get entity by fact key: %w", err)
	}
	return entity, nil
}

// List returns one page of entities at a commit, ordered by path and id, with an
// opaque keyset cursor. An empty Kind/Path/Query matches everything; Query is a
// case-insensitive path-or-name substring search.
func (r *EntityRepo) List(ctx context.Context, repoID int64, filter domain.EntityFilter) ([]domain.CodeEntity, string, bool, error) {
	if filter.Limit <= 0 {
		return nil, "", false, fmt.Errorf("postgres: list code entities: limit must be positive")
	}
	cursorID, err := platform.DecodeCursor(filter.Cursor)
	if err != nil {
		return nil, "", false, fmt.Errorf("postgres: list code entities: %w", err)
	}
	var (
		cursorPath string
		hasCursor  bool
	)
	if cursorID != 0 {
		if err := r.pool.QueryRow(ctx,
			`SELECT path FROM code_entities WHERE id = $1 AND repo_id = $2 AND commit_sha = $3`,
			cursorID, repoID, filter.Commit).Scan(&cursorPath); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return []domain.CodeEntity{}, "", false, nil
			}
			return nil, "", false, fmt.Errorf("postgres: list code entities: read cursor: %w", err)
		}
		hasCursor = true
	}

	const query = `SELECT ` + entityColumnList + `
		FROM code_entities
		WHERE repo_id = $1 AND commit_sha = $2
		  AND ($3 = '' OR kind = $3)
		  AND ($4 = '' OR path = $4)
		  AND ($5 = '' OR strpos(lower(path), lower($5)) > 0
		                OR strpos(lower(name), lower($5)) > 0)
		  AND ($6::boolean = false OR (path, id) > ($7::text, $8::bigint))
		ORDER BY path ASC, id ASC
		LIMIT $9`
	rows, err := r.pool.Query(ctx, query,
		repoID, filter.Commit, filter.Kind, filter.Path, filter.Query,
		hasCursor, cursorPath, cursorID, filter.Limit+1)
	if err != nil {
		return nil, "", false, fmt.Errorf("postgres: list code entities: %w", err)
	}
	defer rows.Close()

	entities := make([]domain.CodeEntity, 0, filter.Limit+1)
	for rows.Next() {
		entity, err := scanEntity(rows)
		if err != nil {
			return nil, "", false, fmt.Errorf("postgres: scan code entity: %w", err)
		}
		entities = append(entities, entity)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, fmt.Errorf("postgres: list code entities: %w", err)
	}

	more := len(entities) > filter.Limit
	nextCursor := ""
	if more {
		entities = entities[:filter.Limit]
		nextCursor = platform.EncodeCursor(entities[len(entities)-1].ID)
	}
	return entities, nextCursor, more, nil
}

// ListByPaths returns every entity (T0 file or AI fact) at paths, ordered by
// path and id. It backs the analysis of the changed files.
func (r *EntityRepo) ListByPaths(ctx context.Context, repoID int64, commit string, paths []string) ([]domain.CodeEntity, error) {
	if len(paths) == 0 {
		return []domain.CodeEntity{}, nil
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+entityColumnList+`
		 FROM code_entities
		 WHERE repo_id = $1 AND commit_sha = $2 AND path = ANY($3::text[])
		 ORDER BY path ASC, id ASC`,
		repoID, commit, paths)
	if err != nil {
		return nil, fmt.Errorf("postgres: list code entities by path: %w", err)
	}
	defer rows.Close()

	entities := make([]domain.CodeEntity, 0)
	for rows.Next() {
		entity, err := scanEntity(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan code entity: %w", err)
		}
		entities = append(entities, entity)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list code entities by path: %w", err)
	}
	return entities, nil
}

// DeleteFact removes the current entity snapshot of one fact. Deleting a fact
// that is already gone is a no-op, so a re-run can clean up idempotently; the
// append-only body history in code_fact_versions is kept.
func (r *EntityRepo) DeleteFact(ctx context.Context, repoID int64, commit, factKey string) error {
	if _, err := r.pool.Exec(ctx,
		`DELETE FROM code_entities WHERE repo_id = $1 AND commit_sha = $2 AND fact_key = $3`,
		repoID, commit, factKey); err != nil {
		return fmt.Errorf("postgres: delete code entity fact: %w", err)
	}
	return nil
}
