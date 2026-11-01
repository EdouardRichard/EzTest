package codeintelpg

import (
	"context"
	"encoding/json"
	"errors"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AnalysisRepository stores code_analysis rows.
type AnalysisRepository struct {
	pool *pgxpool.Pool
}

// NewAnalysisRepository wires the analysis-run repository.
func NewAnalysisRepository(pool *pgxpool.Pool) *AnalysisRepository {
	return &AnalysisRepository{pool: pool}
}

var _ codeintelapp.AnalysisRepository = (*AnalysisRepository)(nil)

const analysisColumns = `id, version_id, repo_id, base_commit_sha, head_commit_sha, status,
	degraded, model, prompt_version, analysis_version, changed_files, stats, unmapped, errors,
	fact_versions, created_by, started_at, finished_at, created_at, updated_at`

func scanAnalysis(row rowScanner) (codeintel.Analysis, error) {
	var analysis codeintel.Analysis
	var status string
	var changedFiles, stats, unmapped, errorsRaw, factVersions []byte
	if err := row.Scan(&analysis.ID, &analysis.VersionID, &analysis.RepoID, &analysis.BaseCommitSHA,
		&analysis.HeadCommitSHA, &status, &analysis.Degraded, &analysis.Model, &analysis.PromptVersion,
		&analysis.AnalysisVersion, &changedFiles, &stats, &unmapped, &errorsRaw, &factVersions,
		&analysis.CreatedBy, &analysis.StartedAt, &analysis.FinishedAt,
		&analysis.CreatedAt, &analysis.UpdatedAt); err != nil {
		return codeintel.Analysis{}, err
	}
	analysis.Status = codeintel.AnalysisStatus(status)
	analysis.ChangedFiles = json.RawMessage(changedFiles)
	analysis.Stats = json.RawMessage(stats)
	analysis.Unmapped = json.RawMessage(unmapped)
	analysis.Errors = json.RawMessage(errorsRaw)
	analysis.FactVersions = json.RawMessage(factVersions)
	return analysis, nil
}

// Create inserts one queued analysis run.
func (r *AnalysisRepository) Create(ctx context.Context, analysis codeintel.Analysis) (codeintel.Analysis, error) {
	stored, err := scanAnalysis(r.pool.QueryRow(ctx, `
		INSERT INTO code_analysis (id, version_id, repo_id, base_commit_sha, head_commit_sha, status,
			degraded, model, prompt_version, analysis_version, changed_files, stats, unmapped,
			errors, fact_versions, created_by, started_at, finished_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, $12::jsonb, $13::jsonb,
			$14::jsonb, $15::jsonb, $16, $17, $18, $19, $20)
		RETURNING `+analysisColumns,
		analysis.ID, analysis.VersionID, analysis.RepoID, analysis.BaseCommitSHA, analysis.HeadCommitSHA,
		string(analysis.Status), analysis.Degraded, analysis.Model, analysis.PromptVersion,
		analysis.AnalysisVersion, jsonText(analysis.ChangedFiles, `[]`), jsonText(analysis.Stats, `{}`),
		jsonText(analysis.Unmapped, `[]`), jsonText(analysis.Errors, `[]`),
		jsonText(analysis.FactVersions, `{}`), analysis.CreatedBy, analysis.StartedAt,
		analysis.FinishedAt, analysis.CreatedAt, analysis.UpdatedAt))
	if err != nil {
		return codeintel.Analysis{}, wrapDB("create code analysis", err)
	}
	return stored, nil
}

// Get loads one analysis.
func (r *AnalysisRepository) Get(ctx context.Context, id int64) (codeintel.Analysis, error) {
	analysis, err := scanAnalysis(r.pool.QueryRow(ctx, `SELECT `+analysisColumns+` FROM code_analysis WHERE id = $1`, id))
	if err != nil {
		return codeintel.Analysis{}, wrapDB("get code analysis", err)
	}
	return analysis, nil
}

// Latest returns the newest analysis of one version×repo.
func (r *AnalysisRepository) Latest(ctx context.Context, versionID, repoID int64) (codeintel.Analysis, error) {
	analysis, err := scanAnalysis(r.pool.QueryRow(ctx, `
		SELECT `+analysisColumns+` FROM code_analysis
		WHERE version_id = $1 AND repo_id = $2
		ORDER BY id DESC
		LIMIT 1
	`, versionID, repoID))
	if err != nil {
		return codeintel.Analysis{}, wrapDB("latest code analysis", err)
	}
	return analysis, nil
}

// Update writes the mutable analysis status/result columns.
func (r *AnalysisRepository) Update(ctx context.Context, analysis codeintel.Analysis) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE code_analysis
		SET status = $2, degraded = $3, model = $4, prompt_version = $5, analysis_version = $6,
		    changed_files = $7::jsonb, stats = $8::jsonb, unmapped = $9::jsonb,
		    errors = $10::jsonb, fact_versions = $11::jsonb, started_at = $12,
		    finished_at = $13, updated_at = $14
		WHERE id = $1
	`, analysis.ID, string(analysis.Status), analysis.Degraded, analysis.Model, analysis.PromptVersion,
		analysis.AnalysisVersion, jsonText(analysis.ChangedFiles, `[]`), jsonText(analysis.Stats, `{}`),
		jsonText(analysis.Unmapped, `[]`), jsonText(analysis.Errors, `[]`),
		jsonText(analysis.FactVersions, `{}`), analysis.StartedAt, analysis.FinishedAt, analysis.UpdatedAt)
	if err != nil {
		return wrapDB("update code analysis", err)
	}
	if tag.RowsAffected() == 0 {
		return codeintel.ErrNotFound
	}
	return nil
}

// Active returns the queued/running analysis of one version×repo.
func (r *AnalysisRepository) Active(ctx context.Context, versionID, repoID int64) (codeintel.Analysis, bool, error) {
	analysis, err := scanAnalysis(r.pool.QueryRow(ctx, `
		SELECT `+analysisColumns+` FROM code_analysis
		WHERE version_id = $1 AND repo_id = $2 AND status IN ('queued', 'running')
		ORDER BY id DESC
		LIMIT 1
	`, versionID, repoID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return codeintel.Analysis{}, false, nil
		}
		return codeintel.Analysis{}, false, wrapDB("active code analysis", err)
	}
	return analysis, true, nil
}

// DoneByRange returns a completed analysis for the same version×repo×range.
func (r *AnalysisRepository) DoneByRange(ctx context.Context, versionID, repoID int64, base, head string) (codeintel.Analysis, bool, error) {
	analysis, err := scanAnalysis(r.pool.QueryRow(ctx, `
		SELECT `+analysisColumns+` FROM code_analysis
		WHERE version_id = $1 AND repo_id = $2 AND base_commit_sha = $3 AND head_commit_sha = $4
		  AND status = 'done'
		ORDER BY id DESC
		LIMIT 1
	`, versionID, repoID, base, head))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return codeintel.Analysis{}, false, nil
		}
		return codeintel.Analysis{}, false, wrapDB("done code analysis", err)
	}
	return analysis, true, nil
}

// MaxVersion returns the highest analysis_version of one version×repo (0 when
// no analysis exists).
func (r *AnalysisRepository) MaxVersion(ctx context.Context, versionID, repoID int64) (int, error) {
	var version int
	if err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(MAX(analysis_version), 0) FROM code_analysis
		WHERE version_id = $1 AND repo_id = $2
	`, versionID, repoID).Scan(&version); err != nil {
		return 0, wrapDB("max code analysis version", err)
	}
	return version, nil
}
