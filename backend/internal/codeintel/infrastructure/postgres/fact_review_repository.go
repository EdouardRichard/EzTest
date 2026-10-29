package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// fact_review_repository.go persists the P8 human fact verdicts and serves the
// read-only sources of the repository quality aggregate (design spec
// §4.2/§7.2/§9.2). The aggregate reads the codeintel context's own tables plus
// the platform evaluation tables (eval_run/eval_metric) as a read-only
// projection, following the same cross-context read pattern as the version
// change feed; it never writes another context's state. The human review note
// lives only in code_fact_review.reason and is never copied into the aggregate.

// Impact metric names of the evaluation context (design spec §9.1). They are
// read by name from eval_metric; an absent run/metric is reported as
// unavailable instead of a fabricated zero.
const (
	impactMetricPrecision = "impact_precision"
	impactMetricRecall    = "impact_recall"

	reasonNoEvalRun            = "no_eval_run"
	reasonNoImpactMetric       = "no_impact_metric"
	reasonEvalStoreUnavailable = "eval_store_unavailable"
)

// factReviewColumnList is the shared column order of every fact-review query and
// the order scanFactReview expects. fact_id is TEXT: the decimal code_entity id
// is stored without ever going through a float.
const factReviewColumnList = `id, repo_id, fact_id, commit_sha, verdict, reason, created_by, created_at`

// getFactReviewFactSQL is the ownership lookup of the reviewed fact. It reads
// the fact's own commit so the review is always attached to the exact fact
// version the user saw.
const getFactReviewFactSQL = `SELECT id, repo_id, commit_sha, COALESCE(fact_key, ''), kind
	FROM code_entity
	WHERE repo_id = $1 AND id = $2`

// insertFactReviewSQL stores the first verdict of (repo, fact, commit). A
// conflict returns no row so the caller can overwrite the existing verdict.
const insertFactReviewSQL = `INSERT INTO code_fact_review
		(id, repo_id, fact_id, commit_sha, verdict, reason, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (repo_id, fact_id, commit_sha) DO NOTHING
		RETURNING ` + factReviewColumnList

// updateFactReviewSQL overwrites the verdict/note/author/time of the row that
// already exists for (repo, fact, commit); the review id is preserved.
const updateFactReviewSQL = `UPDATE code_fact_review
		SET verdict = $4, reason = $5, created_by = $6, created_at = $7
		WHERE repo_id = $1 AND fact_id = $2 AND commit_sha = $3
		RETURNING ` + factReviewColumnList

// listFactReviewsSQL loads the persisted verdicts of one facts page at one
// commit.
const listFactReviewsSQL = `SELECT ` + factReviewColumnList + `
	FROM code_fact_review
	WHERE repo_id = $1 AND commit_sha = $2 AND fact_id = ANY($3)
	ORDER BY fact_id`

// factReviewCountsSQL is the review counter aggregate of one repository.
const factReviewCountsSQL = `SELECT count(*),
		count(*) FILTER (WHERE verdict = 'correct'),
		count(*) FILTER (WHERE verdict = 'incorrect')
	FROM code_fact_review
	WHERE repo_id = $1`

// latestQualityAnalysisSQL selects the newest done analysis of the repository:
// its stats document supplies the evidence/invalid-fact/cache counters and its
// head commit is the confidence-distribution source.
const latestQualityAnalysisSQL = `SELECT id, head_commit, stats, created_at
	FROM code_analysis
	WHERE repo_id = $1 AND status = 'done'
	ORDER BY created_at DESC, id DESC
	LIMIT 1`

// latestIndexedCommitSQL is the fallback confidence source for a repository
// that has been indexed but never analysed.
const latestIndexedCommitSQL = `SELECT commit_sha
	FROM code_index_run
	WHERE repo_id = $1 AND status = 'done'
	ORDER BY created_at DESC, id DESC
	LIMIT 1`

// factConfidenceSQL distributes the AI facts of one commit over the three
// documented confidence levels. T0 git file entities are excluded.
const factConfidenceSQL = `SELECT confidence, count(*)
	FROM code_entity
	WHERE repo_id = $1 AND commit_sha = $2 AND source = 'ai'
	GROUP BY confidence`

// evalStoreAvailableSQL detects whether the quality context's migration has been
// applied, so a partially migrated database reports the evaluation as
// unavailable instead of failing the whole quality read.
const evalStoreAvailableSQL = `SELECT to_regclass('public.eval_run') IS NOT NULL
		AND to_regclass('public.eval_metric') IS NOT NULL`

// latestEvalRunSQL selects the newest recorded evaluation run.
const latestEvalRunSQL = `SELECT id, started_at
	FROM eval_run
	ORDER BY started_at DESC, id DESC
	LIMIT 1`

// latestEvalImpactMetricsSQL reads the two impact metrics of one evaluation run.
const latestEvalImpactMetricsSQL = `SELECT name, value
	FROM eval_metric
	WHERE run_id = $1 AND name = ANY($2)`

// analysisQualityStats is the quality projection of code_analysis.stats.
type analysisQualityStats struct {
	EvidenceChecks   int `json:"evidenceChecks"`
	EvidenceFailures int `json:"evidenceFailures"`
	InvalidFacts     int `json:"invalidFacts"`
	CacheHits        int `json:"cacheHits"`
	AnalyzedFiles    int `json:"analyzedFiles"`
}

// decodeAnalysisQualityStats projects the stored analysis stats document. A
// malformed document degrades to zeros: the aggregate is additive and must not
// fail because one historical run wrote an unexpected document.
func decodeAnalysisQualityStats(raw []byte) analysisQualityStats {
	var stats analysisQualityStats
	if len(raw) == 0 {
		return stats
	}
	_ = json.Unmarshal(raw, &stats)
	return stats
}

// applyConfidenceCount adds one grouped confidence row to the distribution.
// Unknown levels are ignored (the DDL already restricts the column values).
func applyConfidenceCount(stats *domain.RepoQualityStats, level string, count int) {
	switch level {
	case domain.ConfidenceHigh:
		stats.ConfidenceHigh += count
	case domain.ConfidenceMedium:
		stats.ConfidenceMedium += count
	case domain.ConfidenceLow:
		stats.ConfidenceLow += count
	}
}

// buildImpactMetrics maps one evaluation run's metric values onto the quality
// block. Both impact metrics are required for an available result; a missing
// run or metric is carried as an explicit reason instead of a zero value.
func buildImpactMetrics(runID *int64, evaluatedAt *time.Time, values map[string]float64) domain.ImpactMetrics {
	metrics := domain.ImpactMetrics{EvalRunID: runID, EvaluatedAt: evaluatedAt}
	if runID == nil {
		metrics.Reason = reasonNoEvalRun
		return metrics
	}
	precision, hasPrecision := values[impactMetricPrecision]
	recall, hasRecall := values[impactMetricRecall]
	if hasPrecision {
		metrics.Precision = &precision
	}
	if hasRecall {
		metrics.Recall = &recall
	}
	if !hasPrecision || !hasRecall {
		metrics.Reason = reasonNoImpactMetric
	}
	return metrics
}

// scanFactReview scans one row in factReviewColumnList order and parses the
// stored decimal fact id.
func scanFactReview(row rowScanner) (domain.FactReview, error) {
	var (
		review domain.FactReview
		factID string
	)
	if err := row.Scan(
		&review.ID,
		&review.RepoID,
		&factID,
		&review.CommitSHA,
		&review.Verdict,
		&review.Reason,
		&review.CreatedBy,
		&review.CreatedAt,
	); err != nil {
		return domain.FactReview{}, err
	}
	parsed, err := strconv.ParseInt(factID, 10, 64)
	if err != nil || parsed <= 0 {
		return domain.FactReview{}, fmt.Errorf("postgres: fact review carries an invalid fact id %q", factID)
	}
	review.FactID = parsed
	return review, nil
}

// FactReviewRepo implements domain.FactReviewRepository on a pgx pool.
type FactReviewRepo struct {
	pool *pgxpool.Pool
}

// NewFactReviewRepository returns the fact-review/quality repository backed by
// pg.
func NewFactReviewRepository(pg *pgxpool.Pool) *FactReviewRepo {
	return &FactReviewRepo{pool: pg}
}

var _ domain.FactReviewRepository = (*FactReviewRepo)(nil)

// GetFact returns the identity of one fact owned by repoID. A fact of another
// repository (or a missing id) is the documented 404 fact_not_found.
func (r *FactReviewRepo) GetFact(ctx context.Context, repoID, factID int64) (domain.FactRef, error) {
	if r == nil || r.pool == nil {
		return domain.FactRef{}, errors.New("codeintel: fact review repository is not configured")
	}
	var fact domain.FactRef
	err := r.pool.QueryRow(ctx, getFactReviewFactSQL, repoID, factID).Scan(
		&fact.ID, &fact.RepoID, &fact.CommitSHA, &fact.FactKey, &fact.Kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.FactRef{}, domain.NewNotFound("fact_not_found",
			fmt.Sprintf("codeintel: fact %d not found in repository %d", factID, repoID))
	}
	if err != nil {
		return domain.FactRef{}, fmt.Errorf("postgres: get fact review target: %w", err)
	}
	return fact, nil
}

// Upsert stores one verdict per (repo, fact, commit): the insert wins the first
// time, every later verdict overwrites the existing row and keeps its id.
func (r *FactReviewRepo) Upsert(ctx context.Context, review domain.FactReview) (domain.FactReview, error) {
	if r == nil || r.pool == nil {
		return domain.FactReview{}, errors.New("codeintel: fact review repository is not configured")
	}
	if review.ID == 0 {
		return domain.FactReview{}, missingIDError("fact review")
	}
	if review.FactID <= 0 {
		return domain.FactReview{}, domain.NewInvalid("factId", "missing_id",
			"codeintel: fact id is required for a review")
	}
	factID := strconv.FormatInt(review.FactID, 10)

	stored, err := scanFactReview(r.pool.QueryRow(ctx, insertFactReviewSQL,
		review.ID, review.RepoID, factID, review.CommitSHA, review.Verdict,
		review.Reason, review.CreatedBy, review.CreatedAt))
	if err == nil {
		return stored, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.FactReview{}, fmt.Errorf("postgres: insert fact review: %w", err)
	}

	stored, err = scanFactReview(r.pool.QueryRow(ctx, updateFactReviewSQL,
		review.RepoID, factID, review.CommitSHA, review.Verdict, review.Reason,
		review.CreatedBy, review.CreatedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.FactReview{}, domain.NewNotFound("fact_not_found",
			fmt.Sprintf("codeintel: fact %d not found in repository %d", review.FactID, review.RepoID))
	}
	if err != nil {
		return domain.FactReview{}, fmt.Errorf("postgres: update fact review: %w", err)
	}
	return stored, nil
}

// ListByFacts returns the persisted verdicts of the requested facts at one
// commit. An empty request performs no query.
func (r *FactReviewRepo) ListByFacts(ctx context.Context, repoID int64, commit string, factIDs []int64) ([]domain.FactReview, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("codeintel: fact review repository is not configured")
	}
	ids := make([]string, 0, len(factIDs))
	seen := make(map[int64]struct{}, len(factIDs))
	for _, factID := range factIDs {
		if factID <= 0 {
			continue
		}
		if _, ok := seen[factID]; ok {
			continue
		}
		seen[factID] = struct{}{}
		ids = append(ids, strconv.FormatInt(factID, 10))
	}
	if len(ids) == 0 {
		return []domain.FactReview{}, nil
	}

	rows, err := r.pool.Query(ctx, listFactReviewsSQL, repoID, commit, ids)
	if err != nil {
		return nil, fmt.Errorf("postgres: list fact reviews: %w", err)
	}
	defer rows.Close()

	reviews := make([]domain.FactReview, 0, len(ids))
	for rows.Next() {
		review, err := scanFactReview(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan fact review: %w", err)
		}
		reviews = append(reviews, review)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list fact reviews: %w", err)
	}
	return reviews, nil
}

// Counts returns the review counters of one repository.
func (r *FactReviewRepo) Counts(ctx context.Context, repoID int64) (domain.FactReviewCounts, error) {
	if r == nil || r.pool == nil {
		return domain.FactReviewCounts{}, errors.New("codeintel: fact review repository is not configured")
	}
	var (
		counts               domain.FactReviewCounts
		total, correct, wrong int64
	)
	if err := r.pool.QueryRow(ctx, factReviewCountsSQL, repoID).Scan(&total, &correct, &wrong); err != nil {
		return domain.FactReviewCounts{}, fmt.Errorf("postgres: count fact reviews: %w", err)
	}
	counts.Total = int(total)
	counts.Correct = int(correct)
	counts.Incorrect = int(wrong)
	return counts, nil
}

// QualityStats aggregates the persisted quality sources of one repository: the
// newest done analysis counters, the newest done index commit and the AI-fact
// confidence distribution at the quality head commit. Missing sources are
// zero/nil; nothing is fabricated.
func (r *FactReviewRepo) QualityStats(ctx context.Context, repoID int64) (domain.RepoQualityStats, error) {
	if r == nil || r.pool == nil {
		return domain.RepoQualityStats{}, errors.New("codeintel: fact review repository is not configured")
	}
	stats := domain.RepoQualityStats{}

	var (
		analysisID int64
		headCommit string
		statsRaw   []byte
		analyzedAt time.Time
	)
	err := r.pool.QueryRow(ctx, latestQualityAnalysisSQL, repoID).Scan(
		&analysisID, &headCommit, &statsRaw, &analyzedAt)
	switch {
	case err == nil:
		stats.AnalysisID = &analysisID
		stats.AnalysisHeadCommit = headCommit
		stats.AnalyzedAt = &analyzedAt
		decoded := decodeAnalysisQualityStats(statsRaw)
		stats.EvidenceChecks = decoded.EvidenceChecks
		stats.EvidenceFailures = decoded.EvidenceFailures
		stats.InvalidFacts = decoded.InvalidFacts
		stats.CacheHits = decoded.CacheHits
		stats.AnalyzedFiles = decoded.AnalyzedFiles
	case errors.Is(err, pgx.ErrNoRows):
		// The repository has never had a done analysis: counters stay zero and
		// the application renders the null rates / analysis=null.
	default:
		return domain.RepoQualityStats{}, fmt.Errorf("postgres: latest quality analysis: %w", err)
	}

	var indexedCommit string
	err = r.pool.QueryRow(ctx, latestIndexedCommitSQL, repoID).Scan(&indexedCommit)
	switch {
	case err == nil:
		stats.IndexedCommit = indexedCommit
	case errors.Is(err, pgx.ErrNoRows):
		// Never indexed: the confidence distribution has no head commit.
	default:
		return domain.RepoQualityStats{}, fmt.Errorf("postgres: latest indexed commit: %w", err)
	}

	// The confidence distribution is taken at the analysis head when one exists
	// (the facts the counters describe); an indexed-but-never-analysed
	// repository falls back to its newest done index commit.
	confidenceCommit := stats.AnalysisHeadCommit
	if confidenceCommit == "" {
		confidenceCommit = stats.IndexedCommit
	}
	if confidenceCommit == "" {
		return stats, nil
	}
	rows, err := r.pool.Query(ctx, factConfidenceSQL, repoID, confidenceCommit)
	if err != nil {
		return domain.RepoQualityStats{}, fmt.Errorf("postgres: fact confidence distribution: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			level string
			count int64
		)
		if err := rows.Scan(&level, &count); err != nil {
			return domain.RepoQualityStats{}, fmt.Errorf("postgres: scan fact confidence: %w", err)
		}
		applyConfidenceCount(&stats, level, int(count))
	}
	if err := rows.Err(); err != nil {
		return domain.RepoQualityStats{}, fmt.Errorf("postgres: fact confidence distribution: %w", err)
	}
	return stats, nil
}

// LatestImpactMetrics returns the newest evaluation run's impact precision and
// recall. The quality context's tables are read as a read-only projection; a
// database whose quality migration has not run yet reports the evaluation as
// unavailable instead of failing the code quality read.
func (r *FactReviewRepo) LatestImpactMetrics(ctx context.Context) (domain.ImpactMetrics, error) {
	if r == nil || r.pool == nil {
		return domain.ImpactMetrics{}, errors.New("codeintel: fact review repository is not configured")
	}
	var available bool
	if err := r.pool.QueryRow(ctx, evalStoreAvailableSQL).Scan(&available); err != nil {
		return domain.ImpactMetrics{}, fmt.Errorf("postgres: check evaluation store: %w", err)
	}
	if !available {
		return domain.ImpactMetrics{Reason: reasonEvalStoreUnavailable}, nil
	}

	var (
		runID       int64
		evaluatedAt time.Time
	)
	err := r.pool.QueryRow(ctx, latestEvalRunSQL).Scan(&runID, &evaluatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ImpactMetrics{Reason: reasonNoEvalRun}, nil
	}
	if err != nil {
		return domain.ImpactMetrics{}, fmt.Errorf("postgres: latest evaluation run: %w", err)
	}

	rows, err := r.pool.Query(ctx, latestEvalImpactMetricsSQL, runID,
		[]string{impactMetricPrecision, impactMetricRecall})
	if err != nil {
		return domain.ImpactMetrics{}, fmt.Errorf("postgres: latest evaluation impact metrics: %w", err)
	}
	defer rows.Close()
	values := make(map[string]float64, 2)
	for rows.Next() {
		var (
			name  string
			value float64
		)
		if err := rows.Scan(&name, &value); err != nil {
			return domain.ImpactMetrics{}, fmt.Errorf("postgres: scan evaluation impact metric: %w", err)
		}
		values[name] = value
	}
	if err := rows.Err(); err != nil {
		return domain.ImpactMetrics{}, fmt.Errorf("postgres: latest evaluation impact metrics: %w", err)
	}
	return buildImpactMetrics(&runID, &evaluatedAt, values), nil
}
