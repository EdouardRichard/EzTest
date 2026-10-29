package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// ---------------------------------------------------------------------------
// FactReviewService fixtures.
// ---------------------------------------------------------------------------

const (
	reviewRepoID   int64 = 7
	reviewFactID   int64 = 5001
	reviewActorID  int64 = 2
	reviewCommitID       = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	reviewCommit2        = "cccccccccccccccccccccccccccccccccccccccc"
)

var reviewNow = time.Date(2026, 10, 29, 9, 30, 0, 0, time.UTC)

// reviewUpsertCall records one repository upsert.
type reviewUpsertCall struct {
	review domain.FactReview
}

// reviewListCall records one ListByFacts lookup.
type reviewListCall struct {
	repoID int64
	commit string
	factID []int64
}

// stubFactReviews is the in-memory FactReviewRepository double of the service
// tests. The default upsert overwrites a row keyed by (repo, fact, commit),
// mirroring the PostgreSQL unique key.
type stubFactReviews struct {
	fact    domain.FactRef
	factErr error

	upsertFn  func(domain.FactReview) (domain.FactReview, error)
	listFn    func(int64, string, []int64) ([]domain.FactReview, error)
	counts    domain.FactReviewCounts
	countsErr error
	stats     domain.RepoQualityStats
	statsErr  error
	impact    domain.ImpactMetrics
	impactErr error

	rows        map[string]domain.FactReview
	upserts     []reviewUpsertCall
	listCalls   []reviewListCall
	qualityRepo []int64
}

func newStubFactReviews() *stubFactReviews {
	return &stubFactReviews{rows: make(map[string]domain.FactReview)}
}

func (s *stubFactReviews) GetFact(_ context.Context, repoID, factID int64) (domain.FactRef, error) {
	if s.factErr != nil {
		return domain.FactRef{}, s.factErr
	}
	if s.fact.ID == 0 {
		return domain.FactRef{}, domain.NewNotFound("fact_not_found", "fact not found")
	}
	if s.fact.RepoID != repoID || s.fact.ID != factID {
		return domain.FactRef{}, domain.NewNotFound("fact_not_found", "fact not found")
	}
	return s.fact, nil
}

func (s *stubFactReviews) Upsert(_ context.Context, review domain.FactReview) (domain.FactReview, error) {
	s.upserts = append(s.upserts, reviewUpsertCall{review: review})
	if s.upsertFn != nil {
		return s.upsertFn(review)
	}
	key := fmt.Sprintf("%d/%d/%s", review.RepoID, review.FactID, review.CommitSHA)
	if existing, ok := s.rows[key]; ok {
		review.ID = existing.ID
	}
	s.rows[key] = review
	return review, nil
}

func (s *stubFactReviews) ListByFacts(_ context.Context, repoID int64, commit string, factIDs []int64) ([]domain.FactReview, error) {
	s.listCalls = append(s.listCalls, reviewListCall{repoID: repoID, commit: commit, factID: append([]int64(nil), factIDs...)})
	if s.listFn != nil {
		return s.listFn(repoID, commit, factIDs)
	}
	reviews := make([]domain.FactReview, 0, len(factIDs))
	for _, factID := range factIDs {
		key := fmt.Sprintf("%d/%d/%s", repoID, factID, commit)
		if review, ok := s.rows[key]; ok {
			reviews = append(reviews, review)
		}
	}
	return reviews, nil
}

func (s *stubFactReviews) Counts(_ context.Context, repoID int64) (domain.FactReviewCounts, error) {
	s.qualityRepo = append(s.qualityRepo, repoID)
	return s.counts, s.countsErr
}

func (s *stubFactReviews) QualityStats(_ context.Context, repoID int64) (domain.RepoQualityStats, error) {
	s.qualityRepo = append(s.qualityRepo, repoID)
	return s.stats, s.statsErr
}

func (s *stubFactReviews) LatestImpactMetrics(context.Context) (domain.ImpactMetrics, error) {
	return s.impact, s.impactErr
}

// factReviewFixture wires the service with the shared repository/audit/id/clock
// doubles of the application tests.
type factReviewFixture struct {
	repos   *stubRepos
	reviews *stubFactReviews
	audit   *stubAudit
	ids     *stubIDs
	clock   *stubClock
	svc     *FactReviewService
}

func newFactReviewFixture() *factReviewFixture {
	f := &factReviewFixture{
		repos:   &stubRepos{infos: map[int64]RepoInfo{reviewRepoID: {ID: reviewRepoID, Status: codeRepositoryActive}}},
		reviews: newStubFactReviews(),
		audit:   &stubAudit{},
		ids:     &stubIDs{},
		clock:   &stubClock{now: reviewNow},
	}
	f.svc = NewFactReviewService(FactReviewServiceDeps{
		Repos:   f.repos,
		Reviews: f.reviews,
		Audit:   f.audit,
		IDs:     f.ids,
		Clock:   f.clock,
	})
	return f
}

// activeReviewFact is the fact projection the happy-path tests serve.
func activeReviewFact() domain.FactRef {
	return domain.FactRef{
		ID:        reviewFactID,
		RepoID:    reviewRepoID,
		CommitSHA: reviewCommitID,
		FactKey:   "api:POST /api/refund",
		Kind:      domain.KindAPI,
	}
}

// TestCreateFactReviewPersistsAndAuditsWithoutNote is the core review write
// contract: a regular user's verdict is stored against the fact's commit, the
// note is trimmed and the audit entry carries identifiers/verdict only (never
// the human note body).
func TestCreateFactReviewPersistsAndAuditsWithoutNote(t *testing.T) {
	f := newFactReviewFixture()
	f.reviews.fact = activeReviewFact()
	reason := "证据行号偏移，实际在第 80 行"

	stored, err := f.svc.Create(context.Background(), reviewRepoID, reviewActorID, reviewFactID,
		" Correct ", "  "+reason+"  ")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if stored.ID != 1 {
		t.Fatalf("review id = %d, want the generated id 1", stored.ID)
	}
	if stored.RepoID != reviewRepoID || stored.FactID != reviewFactID {
		t.Fatalf("review scope = %d/%d, want %d/%d", stored.RepoID, stored.FactID, reviewRepoID, reviewFactID)
	}
	if stored.CommitSHA != reviewCommitID {
		t.Fatalf("review commit = %q, want the fact commit %q", stored.CommitSHA, reviewCommitID)
	}
	if stored.Verdict != domain.FactReviewCorrect {
		t.Fatalf("verdict = %q, want normalized %q", stored.Verdict, domain.FactReviewCorrect)
	}
	if stored.Reason != reason {
		t.Fatalf("reason = %q, want trimmed %q", stored.Reason, reason)
	}
	if stored.CreatedBy != reviewActorID {
		t.Fatalf("createdBy = %d, want the authenticated actor %d", stored.CreatedBy, reviewActorID)
	}
	if !stored.CreatedAt.Equal(reviewNow) {
		t.Fatalf("createdAt = %v, want the injected clock %v", stored.CreatedAt, reviewNow)
	}

	if len(f.reviews.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(f.reviews.upserts))
	}
	written := f.reviews.upserts[0].review
	if written.ID != 1 || written.RepoID != reviewRepoID || written.FactID != reviewFactID ||
		written.CommitSHA != reviewCommitID || written.Verdict != domain.FactReviewCorrect {
		t.Fatalf("persisted review = %+v", written)
	}

	if len(f.audit.calls) != 1 {
		t.Fatalf("audit calls = %d, want 1", len(f.audit.calls))
	}
	entry := f.audit.calls[0]
	if entry.actorID != reviewActorID {
		t.Fatalf("audit actor = %d, want %d", entry.actorID, reviewActorID)
	}
	if entry.action != "code.review" {
		t.Fatalf("audit action = %q, want code.review", entry.action)
	}
	if entry.subject != fmt.Sprintf("code_repository:%d", reviewRepoID) {
		t.Fatalf("audit subject = %q", entry.subject)
	}
	if entry.detail["repoId"] != reviewRepoID || entry.detail["factId"] != reviewFactID ||
		entry.detail["commit"] != reviewCommitID || entry.detail["verdict"] != domain.FactReviewCorrect {
		t.Fatalf("audit detail = %v", entry.detail)
	}
	if _, ok := entry.detail["reason"]; ok {
		t.Fatalf("audit detail leaks the review note: %v", entry.detail)
	}
	for key, value := range entry.detail {
		if text, ok := value.(string); ok && strings.Contains(text, reason) {
			t.Fatalf("audit detail %q leaks the review note body: %v", key, entry.detail)
		}
	}
}

// TestCreateFactReviewOverwritesSameFactCommit proves the (repo, fact, commit)
// identity: a second verdict updates the same row instead of appending a
// second one.
func TestCreateFactReviewOverwritesSameFactCommit(t *testing.T) {
	f := newFactReviewFixture()
	f.reviews.fact = activeReviewFact()
	rows := make(map[string]domain.FactReview)
	f.reviews.upsertFn = func(review domain.FactReview) (domain.FactReview, error) {
		key := fmt.Sprintf("%d/%d/%s", review.RepoID, review.FactID, review.CommitSHA)
		if existing, ok := rows[key]; ok {
			review.ID = existing.ID
			rows[key] = review
			return review, nil
		}
		rows[key] = review
		return review, nil
	}

	first, err := f.svc.Create(context.Background(), reviewRepoID, reviewActorID, reviewFactID,
		domain.FactReviewCorrect, "证据成立")
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	second, err := f.svc.Create(context.Background(), reviewRepoID, reviewActorID, reviewFactID,
		domain.FactReviewIncorrect, "复核后判定错误")
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}

	if second.ID != first.ID {
		t.Fatalf("overwrite changed the review id: %d -> %d", first.ID, second.ID)
	}
	if second.Verdict != domain.FactReviewIncorrect || second.Reason != "复核后判定错误" {
		t.Fatalf("overwritten review = %+v", second)
	}
	if len(rows) != 1 {
		t.Fatalf("stored rows = %d, want one row per (repo, fact, commit)", len(rows))
	}
}

// TestCreateFactReviewRejectsInvalidInput pins the 422 validation surface: the
// verdict is a closed enum and the note is bounded.
func TestCreateFactReviewRejectsInvalidInput(t *testing.T) {
	testCases := []struct {
		name    string
		verdict string
		reason  string
		field   string
		reason2 string
	}{
		{name: "missing verdict", verdict: "", reason: "", field: "verdict", reason2: "invalid_verdict"},
		{name: "unknown verdict", verdict: "maybe", reason: "", field: "verdict", reason2: "invalid_verdict"},
		{name: "over-long reason", verdict: domain.FactReviewCorrect,
			reason: strings.Repeat("x", domain.MaxFactReviewReasonChars+1), field: "reason", reason2: "invalid_reason"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			f := newFactReviewFixture()
			f.reviews.fact = activeReviewFact()
			_, err := f.svc.Create(context.Background(), reviewRepoID, reviewActorID, reviewFactID,
				testCase.verdict, testCase.reason)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("error = %v, want a domain invalid error", err)
			}
			if got := domain.Reason(err); got != testCase.reason2 {
				t.Fatalf("reason = %q, want %q", got, testCase.reason2)
			}
			var domainErr *domain.Error
			if !errors.As(err, &domainErr) || domainErr.Field != testCase.field {
				t.Fatalf("field = %+v, want %q", domainErr, testCase.field)
			}
			if len(f.reviews.upserts) != 0 || len(f.audit.calls) != 0 {
				t.Fatalf("invalid input wrote review/audit: upserts=%d audit=%d",
					len(f.reviews.upserts), len(f.audit.calls))
			}
		})
	}
}

// TestCreateFactReviewRejectsFactOutsideRepository is the 404 contract: a fact
// id of another repository (or a missing one) is a not-found, never a write.
func TestCreateFactReviewRejectsFactOutsideRepository(t *testing.T) {
	f := newFactReviewFixture()
	f.reviews.fact = domain.FactRef{ID: reviewFactID, RepoID: reviewRepoID + 1, CommitSHA: reviewCommitID}

	_, err := f.svc.Create(context.Background(), reviewRepoID, reviewActorID, reviewFactID,
		domain.FactReviewCorrect, "无关事实")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want a domain not-found error", err)
	}
	if got := domain.Reason(err); got != "fact_not_found" {
		t.Fatalf("reason = %q, want fact_not_found", got)
	}
	if len(f.reviews.upserts) != 0 || len(f.audit.calls) != 0 {
		t.Fatalf("foreign fact wrote review/audit: upserts=%d audit=%d",
			len(f.reviews.upserts), len(f.audit.calls))
	}
}

// TestCreateFactReviewRejectsDisabledRepository keeps reviews off a disabled
// repository, matching the read-path verdict.
func TestCreateFactReviewRejectsDisabledRepository(t *testing.T) {
	f := newFactReviewFixture()
	f.repos.infos[reviewRepoID] = RepoInfo{ID: reviewRepoID, Status: "disabled"}
	f.reviews.fact = activeReviewFact()

	_, err := f.svc.Create(context.Background(), reviewRepoID, reviewActorID, reviewFactID,
		domain.FactReviewCorrect, "")
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("error = %v, want a domain invalid error", err)
	}
	if got := domain.Reason(err); got != "code_repository_disabled" {
		t.Fatalf("reason = %q, want code_repository_disabled", got)
	}
	if len(f.reviews.upserts) != 0 {
		t.Fatalf("disabled repository wrote a review")
	}
}

// TestReviewsForFactsGroupsByCommit loads the persisted verdicts of one facts
// page without failing the page when the store is unavailable.
func TestReviewsForFactsGroupsByCommit(t *testing.T) {
	f := newFactReviewFixture()
	f.reviews.rows[fmt.Sprintf("%d/%d/%s", reviewRepoID, 5001, reviewCommitID)] = domain.FactReview{
		ID: 1, RepoID: reviewRepoID, FactID: 5001, CommitSHA: reviewCommitID,
		Verdict: domain.FactReviewIncorrect, Reason: "代码已删除该接口",
	}
	entities := []domain.CodeEntity{
		{ID: 5001, Commit: reviewCommitID},
		{ID: 5002, Commit: reviewCommitID},
		{ID: 5003, Commit: reviewCommit2},
	}

	reviews, err := f.svc.ReviewsForFacts(context.Background(), reviewRepoID, entities)
	if err != nil {
		t.Fatalf("ReviewsForFacts: %v", err)
	}
	if len(reviews) != 1 || reviews[5001].Verdict != domain.FactReviewIncorrect {
		t.Fatalf("reviews = %+v, want the persisted verdict of fact 5001", reviews)
	}
	if len(f.reviews.listCalls) != 2 {
		t.Fatalf("ListByFacts calls = %d, want one per commit", len(f.reviews.listCalls))
	}
	if got := f.reviews.listCalls[0].factID; len(got) != 2 || got[0] != 5001 || got[1] != 5002 {
		t.Fatalf("first commit fact ids = %v, want [5001 5002]", got)
	}
	if got := f.reviews.listCalls[1].factID; len(got) != 1 || got[0] != 5003 {
		t.Fatalf("second commit fact ids = %v, want [5003]", got)
	}

	empty, err := f.svc.ReviewsForFacts(context.Background(), reviewRepoID, nil)
	if err != nil {
		t.Fatalf("ReviewsForFacts(empty): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty reviews = %+v", empty)
	}
}

// TestQualityAggregatesRepoMetrics pins the aggregation contract of
// GET /api/code/repos/{repoId}/quality.
func TestQualityAggregatesRepoMetrics(t *testing.T) {
	f := newFactReviewFixture()
	analysisID := int64(9101)
	analyzedAt := reviewNow.Add(-time.Hour)
	evalRunID := int64(9201)
	evaluatedAt := reviewNow.Add(-2 * time.Hour)
	precision := 0.75
	recall := 0.8
	f.reviews.stats = domain.RepoQualityStats{
		AnalysisID:         &analysisID,
		AnalysisHeadCommit: reviewCommitID,
		AnalyzedAt:         &analyzedAt,
		IndexedCommit:      reviewCommitID,
		EvidenceChecks:     20,
		EvidenceFailures:   5,
		InvalidFacts:       2,
		CacheHits:          6,
		AnalyzedFiles:      12,
		ConfidenceHigh:     4,
		ConfidenceMedium:   2,
		ConfidenceLow:      1,
	}
	f.reviews.counts = domain.FactReviewCounts{Total: 3, Correct: 2, Incorrect: 1}
	f.reviews.impact = domain.ImpactMetrics{
		Precision: &precision, Recall: &recall, EvalRunID: &evalRunID, EvaluatedAt: &evaluatedAt,
	}

	quality, err := f.svc.Quality(context.Background(), reviewRepoID)
	if err != nil {
		t.Fatalf("Quality: %v", err)
	}

	if quality.RepoID != reviewRepoID || !quality.GeneratedAt.Equal(reviewNow) {
		t.Fatalf("quality header = %+v", quality)
	}
	if quality.Evidence.Checks != 20 || quality.Evidence.Failures != 5 {
		t.Fatalf("evidence = %+v", quality.Evidence)
	}
	if quality.Evidence.PassRate == nil || *quality.Evidence.PassRate != 0.75 {
		t.Fatalf("evidence pass rate = %v, want 0.75", quality.Evidence.PassRate)
	}
	if quality.InvalidFacts != 2 {
		t.Fatalf("invalid facts = %d, want 2", quality.InvalidFacts)
	}
	if quality.Confidence.High != 4 || quality.Confidence.Medium != 2 ||
		quality.Confidence.Low != 1 || quality.Confidence.Total != 7 {
		t.Fatalf("confidence = %+v", quality.Confidence)
	}
	if quality.Reviews.Total != 3 || quality.Reviews.Correct != 2 || quality.Reviews.Incorrect != 1 {
		t.Fatalf("reviews = %+v", quality.Reviews)
	}
	if quality.CacheHitRate == nil || *quality.CacheHitRate != 0.5 {
		t.Fatalf("cache hit rate = %v, want 0.5", quality.CacheHitRate)
	}
	if !quality.Impact.Available || quality.Impact.Precision == nil ||
		*quality.Impact.Precision != precision || quality.Impact.Recall == nil ||
		*quality.Impact.Recall != recall {
		t.Fatalf("impact = %+v", quality.Impact)
	}
	if quality.Impact.EvalRunID == nil || *quality.Impact.EvalRunID != evalRunID ||
		quality.Impact.EvaluatedAt == nil || !quality.Impact.EvaluatedAt.Equal(evaluatedAt) {
		t.Fatalf("impact evaluation ref = %+v", quality.Impact)
	}
	if quality.Analysis == nil || quality.Analysis.ID != analysisID ||
		quality.Analysis.HeadCommit != reviewCommitID || !quality.Analysis.AnalyzedAt.Equal(analyzedAt) {
		t.Fatalf("analysis ref = %+v", quality.Analysis)
	}
	if quality.IndexedCommit != reviewCommitID {
		t.Fatalf("indexed commit = %q", quality.IndexedCommit)
	}
}

// TestQualityMarksUnavailableSources proves the empty/degraded contract: no
// analysis yields null rates, and a missing evaluation run is explicitly
// flagged instead of fabricating 0.0 metrics.
func TestQualityMarksUnavailableSources(t *testing.T) {
	f := newFactReviewFixture()
	f.reviews.impact = domain.ImpactMetrics{Reason: "no_eval_run"}

	quality, err := f.svc.Quality(context.Background(), reviewRepoID)
	if err != nil {
		t.Fatalf("Quality: %v", err)
	}
	if quality.Evidence.PassRate != nil {
		t.Fatalf("pass rate = %v, want null without evidence checks", *quality.Evidence.PassRate)
	}
	if quality.CacheHitRate != nil {
		t.Fatalf("cache hit rate = %v, want null without analyzed files", *quality.CacheHitRate)
	}
	if quality.Analysis != nil {
		t.Fatalf("analysis = %+v, want null without a done analysis", quality.Analysis)
	}
	if quality.Impact.Available || quality.Impact.Precision != nil || quality.Impact.Recall != nil {
		t.Fatalf("impact = %+v, want unavailable null metrics", quality.Impact)
	}
	if quality.Impact.Reason != "no_eval_run" {
		t.Fatalf("impact reason = %q, want no_eval_run", quality.Impact.Reason)
	}
	if quality.Confidence.Total != 0 || quality.Reviews.Total != 0 || quality.InvalidFacts != 0 {
		t.Fatalf("empty quality = %+v", quality)
	}
}

// TestQualityRejectsUnknownRepository keeps the aggregate behind the same
// repository ownership check as the reads.
func TestQualityRejectsUnknownRepository(t *testing.T) {
	f := newFactReviewFixture()
	unknownRepoID := int64(999)

	_, err := f.svc.Quality(context.Background(), unknownRepoID)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("error = %v, want a domain invalid error", err)
	}
	if got := domain.Reason(err); got != "code_repository_not_found" {
		t.Fatalf("reason = %q, want code_repository_not_found", got)
	}
	if len(f.reviews.qualityRepo) != 0 {
		t.Fatalf("unknown repository reached the quality store: %v", f.reviews.qualityRepo)
	}
}
