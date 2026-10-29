package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// fact_review_service.go implements the P8 code-fact quality use cases (design
// spec §7.2/§9.2): a logged-in user records a correct/incorrect verdict plus an
// optional note on one fact at one commit, and the read-only repository quality
// aggregate serves evidence pass rate, invalid facts, the confidence
// distribution, human review counters and the newest evaluation run's impact
// precision/recall. The human note is persisted with the review only; it never
// reaches the audit log, an event or a prompt.

// auditActionCodeReview is the codeintel write audit action of a human fact
// review. The audit adapter allowlists it and records identifiers/verdict only.
const auditActionCodeReview = "code.review"

// FactReviewServiceDeps are the ports of the fact-review/quality use case.
// Repos is the P2 repository catalog (ownership + disabled check), Reviews is
// the PostgreSQL fact-review/quality store, Audit stays injectable (nil disables
// auditing) and IDs/Clock keep ids and timestamps deterministic in tests.
type FactReviewServiceDeps struct {
	Repos   RepoCatalog
	Reviews domain.FactReviewRepository
	Audit   AuditRecorder
	IDs     IDGenerator
	Clock   Clock
	Logger  *slog.Logger
}

// FactReviewService implements the human fact review write and the repository
// quality read.
type FactReviewService struct {
	deps FactReviewServiceDeps
}

// NewFactReviewService wires the fact-review/quality use case.
func NewFactReviewService(deps FactReviewServiceDeps) *FactReviewService {
	return &FactReviewService{deps: deps}
}

// QualityEvidence is the evidence-verification projection of the quality block.
type QualityEvidence struct {
	Checks   int
	Failures int
	// PassRate is (checks-failures)/checks, or nil when no evidence was checked.
	PassRate *float64
}

// QualityConfidence is the confidence distribution of the AI facts at the
// quality head commit.
type QualityConfidence struct {
	High   int
	Medium int
	Low    int
	Total  int
}

// QualityReviews is the human review counter projection.
type QualityReviews struct {
	Total     int
	Correct   int
	Incorrect int
}

// QualityImpact is the newest evaluation run's impact metrics. When the
// evaluation is unavailable (or has never run) Precision/Recall stay nil,
// Available is false and Reason names the degraded source.
type QualityImpact struct {
	Precision   *float64
	Recall      *float64
	Available   bool
	Reason      string
	EvalRunID   *int64
	EvaluatedAt *time.Time
}

// QualityAnalysis refs the newest done analysis that supplied the counters.
type QualityAnalysis struct {
	ID         int64
	HeadCommit string
	AnalyzedAt time.Time
}

// RepoQuality is the repository quality aggregate of GET
// /api/code/repos/{repoId}/quality. Nullable rates/metrics are pointers so the
// wire contract can distinguish "no data" from a real zero.
type RepoQuality struct {
	RepoID        int64
	GeneratedAt   time.Time
	Evidence      QualityEvidence
	InvalidFacts  int
	Confidence    QualityConfidence
	Reviews       QualityReviews
	Impact        QualityImpact
	CacheHitRate  *float64
	Analysis      *QualityAnalysis
	IndexedCommit string
}

// Create validates and stores one human verdict on the fact at its own commit.
// The fact must belong to repoID (404 fact_not_found otherwise) and the
// repository must be active. A second verdict for the same (repo, fact, commit)
// overwrites the stored row. Every successful write records one summary-only
// code.review audit entry (identifiers and verdict, never the note).
func (s *FactReviewService) Create(ctx context.Context, repoID, actorID, factID int64, rawVerdict, rawReason string) (domain.FactReview, error) {
	verdict, err := normalizeFactReviewVerdict(rawVerdict)
	if err != nil {
		return domain.FactReview{}, err
	}
	reason, err := normalizeFactReviewReason(rawReason)
	if err != nil {
		return domain.FactReview{}, err
	}
	if s == nil || s.deps.Reviews == nil {
		return domain.FactReview{}, errors.New("codeintel: fact review repository is not configured")
	}
	if err := s.validateRepo(ctx, repoID); err != nil {
		return domain.FactReview{}, err
	}
	fact, err := s.deps.Reviews.GetFact(ctx, repoID, factID)
	if err != nil {
		return domain.FactReview{}, err
	}

	id := int64(0)
	if s.deps.IDs != nil {
		id = s.deps.IDs.Next()
	}
	if id == 0 {
		return domain.FactReview{}, errors.New("codeintel: id generator returned a zero fact review id")
	}
	review := domain.FactReview{
		ID:        id,
		RepoID:    repoID,
		FactID:    fact.ID,
		CommitSHA: fact.CommitSHA,
		Verdict:   verdict,
		Reason:    reason,
		CreatedBy: actorID,
		CreatedAt: s.now(),
	}
	stored, err := s.deps.Reviews.Upsert(ctx, review)
	if err != nil {
		return domain.FactReview{}, err
	}
	s.recordReviewAudit(ctx, actorID, stored)
	return stored, nil
}

// ReviewsForFacts loads the persisted verdicts of one facts page, grouped by
// fact id. The entities of one page normally share one commit; a mixed page is
// split per commit so the (repo, fact, commit) scope never leaks. An empty page
// performs no store read. The caller treats enrichment failures as additive: the
// facts page itself still renders without the review markers.
func (s *FactReviewService) ReviewsForFacts(ctx context.Context, repoID int64, entities []domain.CodeEntity) (map[int64]domain.FactReview, error) {
	reviews := make(map[int64]domain.FactReview)
	if s == nil || s.deps.Reviews == nil || len(entities) == 0 {
		return reviews, nil
	}

	idsByCommit := make(map[string][]int64)
	seen := make(map[int64]struct{}, len(entities))
	for _, entity := range entities {
		if entity.ID <= 0 || entity.Commit == "" {
			continue
		}
		if _, ok := seen[entity.ID]; ok {
			continue
		}
		seen[entity.ID] = struct{}{}
		idsByCommit[entity.Commit] = append(idsByCommit[entity.Commit], entity.ID)
	}
	commits := make([]string, 0, len(idsByCommit))
	for commit := range idsByCommit {
		commits = append(commits, commit)
	}
	sort.Strings(commits)

	for _, commit := range commits {
		stored, err := s.deps.Reviews.ListByFacts(ctx, repoID, commit, idsByCommit[commit])
		if err != nil {
			return nil, err
		}
		for _, review := range stored {
			reviews[review.FactID] = review
		}
	}
	return reviews, nil
}

// Quality aggregates the repository quality block. Evidence/invalid-fact
// counters come from the newest done analysis, the confidence distribution from
// the AI facts at that analysis head (falling back to the newest done index
// commit), review counters from code_fact_review and precision/recall from the
// newest evaluation run. Missing sources stay null/zero and are marked, never
// fabricated.
func (s *FactReviewService) Quality(ctx context.Context, repoID int64) (RepoQuality, error) {
	if s == nil || s.deps.Reviews == nil {
		return RepoQuality{}, errors.New("codeintel: fact review repository is not configured")
	}
	if err := s.validateRepo(ctx, repoID); err != nil {
		return RepoQuality{}, err
	}

	stats, err := s.deps.Reviews.QualityStats(ctx, repoID)
	if err != nil {
		return RepoQuality{}, err
	}
	counts, err := s.deps.Reviews.Counts(ctx, repoID)
	if err != nil {
		return RepoQuality{}, err
	}
	impact, err := s.deps.Reviews.LatestImpactMetrics(ctx)
	if err != nil {
		return RepoQuality{}, err
	}

	quality := RepoQuality{
		RepoID:       repoID,
		GeneratedAt:  s.now(),
		InvalidFacts: stats.InvalidFacts,
		Evidence: QualityEvidence{
			Checks:   stats.EvidenceChecks,
			Failures: stats.EvidenceFailures,
			PassRate: ratio(stats.EvidenceChecks-stats.EvidenceFailures, stats.EvidenceChecks),
		},
		Confidence: QualityConfidence{
			High:   stats.ConfidenceHigh,
			Medium: stats.ConfidenceMedium,
			Low:    stats.ConfidenceLow,
			Total:  stats.ConfidenceHigh + stats.ConfidenceMedium + stats.ConfidenceLow,
		},
		Reviews: QualityReviews{
			Total:     counts.Total,
			Correct:   counts.Correct,
			Incorrect: counts.Incorrect,
		},
		Impact: QualityImpact{
			Precision:   impact.Precision,
			Recall:      impact.Recall,
			Available:   impact.Reason == "" && impact.Precision != nil && impact.Recall != nil,
			Reason:      impact.Reason,
			EvalRunID:   impact.EvalRunID,
			EvaluatedAt: impact.EvaluatedAt,
		},
		CacheHitRate:  ratio(stats.CacheHits, stats.AnalyzedFiles),
		IndexedCommit: stats.IndexedCommit,
	}
	if stats.AnalysisID != nil && stats.AnalyzedAt != nil {
		quality.Analysis = &QualityAnalysis{
			ID:         *stats.AnalysisID,
			HeadCommit: stats.AnalysisHeadCommit,
			AnalyzedAt: *stats.AnalyzedAt,
		}
	}
	return quality, nil
}

// validateRepo proves the repository exists and is active. A missing repository
// is the catalog adapter's 422 code_repository_not_found; a disabled one is the
// read-path's code_repository_disabled verdict.
func (s *FactReviewService) validateRepo(ctx context.Context, repoID int64) error {
	if s.deps.Repos == nil {
		return errors.New("codeintel: repository catalog is not configured")
	}
	repo, err := s.deps.Repos.Get(ctx, repoID)
	if err != nil {
		return err
	}
	if repo.Status != codeRepositoryActive {
		return domain.NewInvalid("repoId", "code_repository_disabled",
			fmt.Sprintf("codeintel: code repository %d is disabled", repoID))
	}
	return nil
}

// recordReviewAudit appends the summary-only code.review entry. The note body is
// deliberately absent (the audit allowlist would drop it anyway); an audit
// failure never fails the review write and is logged instead.
func (s *FactReviewService) recordReviewAudit(ctx context.Context, actorID int64, review domain.FactReview) {
	if s.deps.Audit == nil {
		return
	}
	detail := map[string]any{
		"repoId":  review.RepoID,
		"factId":  review.FactID,
		"commit":  review.CommitSHA,
		"verdict": review.Verdict,
	}
	if err := s.deps.Audit.Record(ctx, actorID, auditActionCodeReview,
		fmt.Sprintf("code_repository:%d", review.RepoID), detail); err != nil {
		s.logger().Warn("codeintel: fact review audit record failed",
			"repoId", review.RepoID, "factId", review.FactID, "error", err)
	}
}

// now returns the injected clock reading (wall time when Clock is nil).
func (s *FactReviewService) now() time.Time {
	if s.deps.Clock == nil {
		return time.Now().UTC()
	}
	return s.deps.Clock.Now()
}

// logger returns the injected logger or the process default.
func (s *FactReviewService) logger() *slog.Logger {
	if s.deps.Logger != nil {
		return s.deps.Logger
	}
	return slog.Default()
}

// normalizeFactReviewVerdict trims and lowercases the verdict and enforces the
// closed correct/incorrect enum (422 invalid_verdict).
func normalizeFactReviewVerdict(raw string) (string, error) {
	verdict := strings.ToLower(strings.TrimSpace(raw))
	switch verdict {
	case domain.FactReviewCorrect, domain.FactReviewIncorrect:
		return verdict, nil
	default:
		return "", domain.NewInvalid("verdict", "invalid_verdict",
			"codeintel: verdict must be correct or incorrect")
	}
}

// normalizeFactReviewReason trims the optional human note and bounds it to the
// documented DDL length (422 invalid_reason).
func normalizeFactReviewReason(raw string) (string, error) {
	reason := strings.TrimSpace(raw)
	if utf8.RuneCountInString(reason) > domain.MaxFactReviewReasonChars {
		return "", domain.NewInvalid("reason", "invalid_reason",
			fmt.Sprintf("codeintel: review reason exceeds %d characters", domain.MaxFactReviewReasonChars))
	}
	return reason, nil
}

// ratio renders numerator/denominator as an optional rate: nil when the
// denominator is zero (no data), clamped to [0,1] so inconsistent counters can
// never produce a negative or >100% rate.
func ratio(numerator, denominator int) *float64 {
	if denominator <= 0 {
		return nil
	}
	if numerator < 0 {
		numerator = 0
	}
	if numerator > denominator {
		numerator = denominator
	}
	value := float64(numerator) / float64(denominator)
	return &value
}
