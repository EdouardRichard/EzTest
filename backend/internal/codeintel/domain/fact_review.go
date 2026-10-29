package domain

import (
	"context"
	"time"
)

// Human fact-review verdicts from design spec §4.2/§7.2. The verdict says
// whether the AI-produced fact is correct; reason is an optional human note.
const (
	// FactReviewCorrect marks a fact the reviewer confirmed.
	FactReviewCorrect = "correct"
	// FactReviewIncorrect marks a fact the reviewer rejected.
	FactReviewIncorrect = "incorrect"
	// MaxFactReviewReasonChars bounds the human note stored with a review. The
	// note is shown to readers of the fact, never written to the audit log.
	MaxFactReviewReasonChars = 2000
)

// FactRef is the read-only identity of one persisted fact (a code_entity row)
// a review targets. It is the ownership check of the review write: the fact must
// belong to the path repository, and its commit is the reviewed fact version.
type FactRef struct {
	ID        int64
	RepoID    int64
	CommitSHA string
	FactKey   string
	Kind      string
}

// FactReview is one human verdict on one fact at one commit (the
// code_fact_review row). Reviews never modify the fact version history: they are
// annotation rows that also feed the repository quality counters.
type FactReview struct {
	ID        int64
	RepoID    int64
	FactID    int64
	CommitSHA string
	Verdict   string
	Reason    string
	CreatedBy int64
	CreatedAt time.Time
}

// FactReviewCounts is the per-repository review aggregate of the quality block.
type FactReviewCounts struct {
	Total     int
	Correct   int
	Incorrect int
}

// RepoQualityStats is the persisted source of the repository quality aggregate:
// the newest done analysis (evidence/invalid/cache counters), the newest done
// index run commit and the AI-fact confidence distribution at the quality head
// commit. A repository without an analysis or index run yields zero values and
// nil refs, which the application renders as null rates / an empty quality
// block instead of fabricating data.
type RepoQualityStats struct {
	AnalysisID         *int64
	AnalysisHeadCommit string
	AnalyzedAt         *time.Time
	IndexedCommit      string
	EvidenceChecks     int
	EvidenceFailures   int
	InvalidFacts       int
	CacheHits          int
	AnalyzedFiles      int
	ConfidenceHigh     int
	ConfidenceMedium   int
	ConfidenceLow      int
}

// ImpactMetrics is the newest evaluation run's impact precision/recall. A
// zero-value result with Reason set (no_eval_run / no_impact_metric /
// eval_store_unavailable) means the quality block must report the evaluation as
// unavailable instead of showing 0.0.
type ImpactMetrics struct {
	Precision   *float64
	Recall      *float64
	EvalRunID   *int64
	EvaluatedAt *time.Time
	Reason      string
}

// FactReviewRepository persists human fact verdicts and serves the read-only
// sources of the repository quality aggregate (design spec §7.2/§9.2). The
// PostgreSQL adapter implements every method; the aggregate reads only the
// codeintel context's own tables plus the platform evaluation tables.
type FactReviewRepository interface {
	// GetFact returns the identity of one fact owned by repoID or a
	// fact_not_found error (HTTP 404).
	GetFact(ctx context.Context, repoID, factID int64) (FactRef, error)
	// Upsert stores the verdict once per (repo, fact, commit); a second call
	// overwrites the verdict/note/author/time of the existing row.
	Upsert(ctx context.Context, review FactReview) (FactReview, error)
	// ListByFacts returns the persisted verdicts of the requested facts at one
	// commit.
	ListByFacts(ctx context.Context, repoID int64, commit string, factIDs []int64) ([]FactReview, error)
	// Counts returns the review counters of one repository.
	Counts(ctx context.Context, repoID int64) (FactReviewCounts, error)
	// QualityStats returns the newest done analysis/index projections and the
	// confidence distribution at the quality head commit.
	QualityStats(ctx context.Context, repoID int64) (RepoQualityStats, error)
	// LatestImpactMetrics returns the newest evaluation run's impact metrics,
	// or a Reason explaining why they are unavailable.
	LatestImpactMetrics(ctx context.Context) (ImpactMetrics, error)
}
