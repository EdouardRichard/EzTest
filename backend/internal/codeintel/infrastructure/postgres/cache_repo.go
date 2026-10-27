package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// cacheColumnList is the shared column order of every cache query and the order
// scanCachedFile expects. Reads return the caller-supplied Snowflake id even
// though the cache is also addressable by its six-part key.
const cacheColumnList = `id, repo_id, commit_sha, path, file_hash, model, prompt_version,
	facts, stats, created_at, last_used_at`

// scanCachedFile scans one row in cacheColumnList order. The JSONB facts/stats
// documents are returned as raw JSON bytes.
func scanCachedFile(row rowScanner) (domain.CachedFile, error) {
	var (
		cached domain.CachedFile
		facts  []byte
		stats  []byte
	)
	if err := row.Scan(
		&cached.ID,
		&cached.RepoID,
		&cached.Commit,
		&cached.Path,
		&cached.FileHash,
		&cached.Model,
		&cached.PromptVersion,
		&facts,
		&stats,
		&cached.CreatedAt,
		&cached.LastUsedAt,
	); err != nil {
		return domain.CachedFile{}, err
	}
	cached.Facts = facts
	cached.Stats = stats
	return cached, nil
}

// CacheRepo implements domain.CacheRepository on a pgx pool.
type CacheRepo struct {
	pool *pgxpool.Pool
}

// NewCacheRepository returns a file-analysis cache repository backed by pg.
func NewCacheRepository(pg *pgxpool.Pool) *CacheRepo {
	return &CacheRepo{pool: pg}
}

var _ domain.CacheRepository = (*CacheRepo)(nil)

// Get returns the cached analysis of one exact six-part key. A hit refreshes
// last_used_at (design spec §6.4); a miss returns (nil, false, nil).
func (r *CacheRepo) Get(ctx context.Context, repoID int64, commit, path, fileHash, model, prompt string) (*domain.CachedFile, bool, error) {
	const query = `UPDATE code_analysis_cache
		SET last_used_at = now()
		WHERE repo_id = $1 AND commit_sha = $2 AND path = $3
		  AND file_hash = $4 AND model = $5 AND prompt_version = $6
		RETURNING ` + cacheColumnList
	cached, err := scanCachedFile(r.pool.QueryRow(ctx, query,
		repoID, commit, path, fileHash, model, prompt))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("postgres: get cached file analysis: %w", err)
	}
	return &cached, true, nil
}

// Upsert stores one successful file analysis under the caller-supplied Snowflake
// id and, on conflict, refreshes its facts/stats/last_used_at while keeping the
// original id and created_at (design spec §6.11). A zero id is rejected with
// ErrInvalid/missing_id because this package never mints ids (D29). The cache
// key is the six-part (repo, commit, path, file_hash, model, prompt_version)
// tuple.
func (r *CacheRepo) Upsert(ctx context.Context, cached domain.CachedFile) error {
	if cached.ID == 0 {
		return missingIDError("cached file analysis")
	}
	const query = `INSERT INTO code_analysis_cache
		(id, repo_id, commit_sha, path, file_hash, model, prompt_version, facts, stats)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::jsonb)
		ON CONFLICT (repo_id, commit_sha, path, file_hash, model, prompt_version)
		DO UPDATE SET
			facts = EXCLUDED.facts,
			stats = EXCLUDED.stats,
			last_used_at = now()`
	if _, err := r.pool.Exec(ctx, query,
		cached.ID, cached.RepoID, cached.Commit, cached.Path, cached.FileHash,
		cached.Model, cached.PromptVersion,
		jsonParam(cached.Facts, jsonFallbackEmptyArray),
		jsonParam(cached.Stats, jsonFallbackEmptyObject),
	); err != nil {
		return fmt.Errorf("postgres: upsert cached file analysis: %w", err)
	}
	return nil
}
