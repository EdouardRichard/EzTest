package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// analysisColumnList is the shared column order of every analysis query and the
// order scanAnalysis expects.
const analysisColumnList = `id, version_id, repo_id, base_commit, head_commit, change_set_id,
	status, degraded, model, prompt_version, analysis_version,
	fact_versions, stats, errors, unmapped, created_by, created_at, started_at, finished_at`

// analysisErrorJSON, unmappedChangeJSON and factVersionRefJSON are the
// persisted shapes of the analyses' JSONB arrays. The domain types carry no
// JSON tags, so the repository owns the stored document shape; the keys match
// the API field names.
type analysisErrorJSON struct {
	Code    string `json:"code"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message,omitempty"`
}

type unmappedChangeJSON struct {
	Path       string `json:"path"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	Suggestion string `json:"suggestion,omitempty"`
}

type factVersionRefJSON struct {
	FactKey string `json:"factKey"`
	Version int    `json:"version"`
}

// marshalAnalysisErrors encodes the error entries as a JSONB array.
func marshalAnalysisErrors(items []domain.AnalysisError) (string, error) {
	entries := make([]analysisErrorJSON, 0, len(items))
	for _, item := range items {
		entries = append(entries, analysisErrorJSON{Code: item.Code, Path: item.Path, Message: item.Message})
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// decodeAnalysisErrors decodes a stored error-entry array.
func decodeAnalysisErrors(raw []byte) ([]domain.AnalysisError, error) {
	if len(raw) == 0 {
		return []domain.AnalysisError{}, nil
	}
	var entries []analysisErrorJSON
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	items := make([]domain.AnalysisError, 0, len(entries))
	for _, entry := range entries {
		items = append(items, domain.AnalysisError{Code: entry.Code, Path: entry.Path, Message: entry.Message})
	}
	return items, nil
}

// marshalUnmappedChanges encodes the unmapped-change entries as a JSONB array.
func marshalUnmappedChanges(items []domain.UnmappedChange) (string, error) {
	entries := make([]unmappedChangeJSON, 0, len(items))
	for _, item := range items {
		entries = append(entries, unmappedChangeJSON{
			Path:       item.Path,
			Status:     item.Status,
			Reason:     item.Reason,
			Suggestion: item.Suggestion,
		})
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// decodeUnmappedChanges decodes a stored unmapped-change array.
func decodeUnmappedChanges(raw []byte) ([]domain.UnmappedChange, error) {
	if len(raw) == 0 {
		return []domain.UnmappedChange{}, nil
	}
	var entries []unmappedChangeJSON
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	items := make([]domain.UnmappedChange, 0, len(entries))
	for _, entry := range entries {
		items = append(items, domain.UnmappedChange{
			Path:       entry.Path,
			Status:     entry.Status,
			Reason:     entry.Reason,
			Suggestion: entry.Suggestion,
		})
	}
	return items, nil
}

// marshalFactVersionRefs encodes the fact-version snapshot as a JSONB array.
func marshalFactVersionRefs(items []domain.FactVersionRef) (string, error) {
	entries := make([]factVersionRefJSON, 0, len(items))
	for _, item := range items {
		entries = append(entries, factVersionRefJSON{FactKey: item.FactKey, Version: item.Version})
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// decodeFactVersionRefs decodes a stored fact-version snapshot.
func decodeFactVersionRefs(raw []byte) ([]domain.FactVersionRef, error) {
	if len(raw) == 0 {
		return []domain.FactVersionRef{}, nil
	}
	var entries []factVersionRefJSON
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	items := make([]domain.FactVersionRef, 0, len(entries))
	for _, entry := range entries {
		items = append(items, domain.FactVersionRef{FactKey: entry.FactKey, Version: entry.Version})
	}
	return items, nil
}

// scanAnalysis scans one row in analysisColumnList order and decodes the JSONB
// columns.
func scanAnalysis(row rowScanner) (domain.CodeAnalysis, error) {
	var (
		analysis     domain.CodeAnalysis
		factVersions []byte
		stats        []byte
		errorsJSON   []byte
		unmapped     []byte
	)
	if err := row.Scan(
		&analysis.ID,
		&analysis.VersionID,
		&analysis.RepoID,
		&analysis.BaseCommit,
		&analysis.HeadCommit,
		&analysis.ChangeSetID,
		&analysis.Status,
		&analysis.Degraded,
		&analysis.Model,
		&analysis.PromptVersion,
		&analysis.AnalysisVersion,
		&factVersions,
		&stats,
		&errorsJSON,
		&unmapped,
		&analysis.CreatedBy,
		&analysis.CreatedAt,
		&analysis.StartedAt,
		&analysis.FinishedAt,
	); err != nil {
		return domain.CodeAnalysis{}, err
	}
	decodedFactVersions, err := decodeFactVersionRefs(factVersions)
	if err != nil {
		return domain.CodeAnalysis{}, fmt.Errorf("decode analysis fact versions: %w", err)
	}
	decodedErrors, err := decodeAnalysisErrors(errorsJSON)
	if err != nil {
		return domain.CodeAnalysis{}, fmt.Errorf("decode analysis errors: %w", err)
	}
	decodedUnmapped, err := decodeUnmappedChanges(unmapped)
	if err != nil {
		return domain.CodeAnalysis{}, fmt.Errorf("decode analysis unmapped changes: %w", err)
	}
	analysis.FactVersions = decodedFactVersions
	analysis.Stats = stats
	analysis.Errors = decodedErrors
	analysis.Unmapped = decodedUnmapped
	return analysis, nil
}

// AnalysisRepo implements domain.AnalysisRepository on a pgx pool.
type AnalysisRepo struct {
	pool *pgxpool.Pool
}

// NewAnalysisRepository returns an impact-analysis repository backed by pg.
func NewAnalysisRepository(pg *pgxpool.Pool) *AnalysisRepo {
	return &AnalysisRepo{pool: pg}
}

var _ domain.AnalysisRepository = (*AnalysisRepo)(nil)

// Create inserts a queued analysis run with its application-generated id. An
// empty status/analysis version falls back to the DDL defaults.
func (r *AnalysisRepo) Create(ctx context.Context, analysis domain.CodeAnalysis) error {
	status := analysis.Status
	if status == "" {
		status = domain.RunQueued
	}
	analysisVersion := analysis.AnalysisVersion
	if analysisVersion == 0 {
		analysisVersion = 1
	}
	encodedFactVersions, err := marshalFactVersionRefs(analysis.FactVersions)
	if err != nil {
		return fmt.Errorf("postgres: encode analysis fact versions: %w", err)
	}
	encodedErrors, err := marshalAnalysisErrors(analysis.Errors)
	if err != nil {
		return fmt.Errorf("postgres: encode analysis errors: %w", err)
	}
	encodedUnmapped, err := marshalUnmappedChanges(analysis.Unmapped)
	if err != nil {
		return fmt.Errorf("postgres: encode analysis unmapped changes: %w", err)
	}
	const query = `INSERT INTO code_analysis
		(id, version_id, repo_id, base_commit, head_commit, change_set_id,
		 status, degraded, model, prompt_version, analysis_version,
		 fact_versions, stats, errors, unmapped, created_by, started_at, finished_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
		        $12::jsonb, $13::jsonb, $14::jsonb, $15::jsonb, $16, $17, $18)`
	if _, err := r.pool.Exec(ctx, query,
		analysis.ID, analysis.VersionID, analysis.RepoID, analysis.BaseCommit,
		analysis.HeadCommit, analysis.ChangeSetID, status, analysis.Degraded,
		analysis.Model, analysis.PromptVersion, analysisVersion,
		encodedFactVersions, jsonParam(analysis.Stats, jsonFallbackEmptyObject),
		encodedErrors, encodedUnmapped, analysis.CreatedBy,
		analysis.StartedAt, analysis.FinishedAt,
	); err != nil {
		if isUniqueViolation(err) {
			return domain.NewConflict("duplicate_key",
				fmt.Sprintf("codeintel: analysis %d already exists", analysis.ID))
		}
		return fmt.Errorf("postgres: create analysis: %w", err)
	}
	return nil
}

// Update rewrites the run state of one analysis: change set, status, degraded
// flag, fact-version snapshot, stats, errors, unmapped entries and the
// start/finish timestamps. The analysis identity is immutable (§6.4/INV-6).
func (r *AnalysisRepo) Update(ctx context.Context, analysis domain.CodeAnalysis) error {
	encodedFactVersions, err := marshalFactVersionRefs(analysis.FactVersions)
	if err != nil {
		return fmt.Errorf("postgres: encode analysis fact versions: %w", err)
	}
	encodedErrors, err := marshalAnalysisErrors(analysis.Errors)
	if err != nil {
		return fmt.Errorf("postgres: encode analysis errors: %w", err)
	}
	encodedUnmapped, err := marshalUnmappedChanges(analysis.Unmapped)
	if err != nil {
		return fmt.Errorf("postgres: encode analysis unmapped changes: %w", err)
	}
	const query = `UPDATE code_analysis
		SET change_set_id = $3, status = $4, degraded = $5,
		    fact_versions = $6::jsonb, stats = $7::jsonb,
		    errors = $8::jsonb, unmapped = $9::jsonb,
		    started_at = $10, finished_at = $11
		WHERE id = $1 AND version_id = $2`
	tag, err := r.pool.Exec(ctx, query,
		analysis.ID, analysis.VersionID, analysis.ChangeSetID, analysis.Status,
		analysis.Degraded, encodedFactVersions,
		jsonParam(analysis.Stats, jsonFallbackEmptyObject),
		encodedErrors, encodedUnmapped,
		analysis.StartedAt, analysis.FinishedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: update analysis: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return notFoundError("analysis", analysis.ID)
	}
	return nil
}

// GetByID returns one analysis of versionID.
func (r *AnalysisRepo) GetByID(ctx context.Context, versionID, id int64) (domain.CodeAnalysis, error) {
	analysis, err := scanAnalysis(r.pool.QueryRow(ctx,
		`SELECT `+analysisColumnList+`
		 FROM code_analysis WHERE id = $1 AND version_id = $2`, id, versionID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CodeAnalysis{}, notFoundError("analysis", id)
		}
		return domain.CodeAnalysis{}, fmt.Errorf("postgres: get analysis: %w", err)
	}
	return analysis, nil
}

// GetLatest returns the newest analysis of versionID (any status).
func (r *AnalysisRepo) GetLatest(ctx context.Context, versionID int64) (domain.CodeAnalysis, error) {
	analysis, err := scanAnalysis(r.pool.QueryRow(ctx,
		`SELECT `+analysisColumnList+`
		 FROM code_analysis
		 WHERE version_id = $1
		 ORDER BY created_at DESC, id DESC
		 LIMIT 1`, versionID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CodeAnalysis{}, domain.NewNotFound("analysis_not_found",
				fmt.Sprintf("codeintel: version %d has no analysis", versionID))
		}
		return domain.CodeAnalysis{}, fmt.Errorf("postgres: get latest analysis: %w", err)
	}
	return analysis, nil
}

// FindReusable returns the newest done analysis of the exact
// (version, repo, base, head, model, prompt_version) input, or nil when the
// input was never analysed (design spec §6.4). A degraded done analysis is
// reusable: degraded is a terminal state.
func (r *AnalysisRepo) FindReusable(ctx context.Context, versionID, repoID int64, base, head, model, prompt string) (*domain.CodeAnalysis, error) {
	analysis, err := scanAnalysis(r.pool.QueryRow(ctx,
		`SELECT `+analysisColumnList+`
		 FROM code_analysis
		 WHERE version_id = $1 AND repo_id = $2
		   AND base_commit = $3 AND head_commit = $4
		   AND model = $5 AND prompt_version = $6
		   AND status = 'done'
		 ORDER BY analysis_version DESC, created_at DESC, id DESC
		 LIMIT 1`,
		versionID, repoID, base, head, model, prompt))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("postgres: find reusable analysis: %w", err)
	}
	return &analysis, nil
}

// HasActive reports whether versionID has a queued or running analysis
// (409 analysis_in_progress).
func (r *AnalysisRepo) HasActive(ctx context.Context, versionID int64) (bool, error) {
	var active bool
	if err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM code_analysis
			WHERE version_id = $1 AND status IN ('queued', 'running')
		)`, versionID).Scan(&active); err != nil {
		return false, fmt.Errorf("postgres: check active analysis: %w", err)
	}
	return active, nil
}

// NextAnalysisVersion returns COALESCE(MAX(analysis_version), 0) + 1 for the
// (version, base, head) triple (INV-6).
func (r *AnalysisRepo) NextAnalysisVersion(ctx context.Context, versionID int64, base, head string) (int, error) {
	var version int
	if err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(MAX(analysis_version), 0) + 1
		FROM code_analysis
		WHERE version_id = $1 AND base_commit = $2 AND head_commit = $3`,
		versionID, base, head).Scan(&version); err != nil {
		return 0, fmt.Errorf("postgres: next analysis version: %w", err)
	}
	return version, nil
}

// RecoverStale marks analyses a previous process left queued or running before
// before as failed(interrupted), the startup recovery rule of design spec §6.8.
// A queued analysis is aged by created_at, a running one by started_at (with
// created_at as fallback). It returns the number of recovered analyses.
func (r *AnalysisRepo) RecoverStale(ctx context.Context, before time.Time) (int64, error) {
	const query = `UPDATE code_analysis
		SET status = 'failed',
		    finished_at = now(),
		    errors = errors || '[{"code":"interrupted"}]'::jsonb
		WHERE status IN ('queued', 'running')
		  AND ((status = 'queued' AND created_at < $1)
		    OR (status = 'running' AND COALESCE(started_at, created_at) < $1))`
	tag, err := r.pool.Exec(ctx, query, before)
	if err != nil {
		return 0, fmt.Errorf("postgres: recover stale analyses: %w", err)
	}
	return tag.RowsAffected(), nil
}

// UpdateCandidateSelection persists a human candidate selection. It is
// last-write-wins by design (design spec §6.11): the actor and timestamp are
// recorded, no revision is introduced.
func (r *AnalysisRepo) UpdateCandidateSelection(ctx context.Context, analysisID, candidateID, actorID int64, selected bool) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE code_impact_candidate
		SET selected = $4, selection_updated_by = $3, selection_updated_at = now()
		WHERE id = $2 AND analysis_id = $1`,
		analysisID, candidateID, actorID, selected)
	if err != nil {
		return fmt.Errorf("postgres: update candidate selection: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return notFoundError("candidate", candidateID)
	}
	return nil
}
