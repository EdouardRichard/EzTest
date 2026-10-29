package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// ---------------------------------------------------------------------------
// Fact review + repository quality handler fixtures.
// ---------------------------------------------------------------------------

const reviewFactWireID = "1844674407370955201"

var factReviewCreatedAt = time.Date(2026, 10, 29, 11, 0, 0, 0, time.UTC)

// factReviewCreateCall records one FactReviewService.Create invocation.
type factReviewCreateCall struct {
	RepoID  int64
	ActorID int64
	FactID  int64
	Verdict string
	Reason  string
}

// fakeFactReviewService is the FactReviewService consumer double.
type fakeFactReviewService struct {
	createFn  func(context.Context, int64, int64, int64, string, string) (domain.FactReview, error)
	reviewsFn func(context.Context, int64, []domain.CodeEntity) (map[int64]domain.FactReview, error)

	createCalls  []factReviewCreateCall
	reviewsCalls int
}

func (f *fakeFactReviewService) Create(ctx context.Context, repoID, actorID, factID int64, verdict, reason string) (domain.FactReview, error) {
	f.createCalls = append(f.createCalls, factReviewCreateCall{
		RepoID: repoID, ActorID: actorID, FactID: factID, Verdict: verdict, Reason: reason,
	})
	if f.createFn == nil {
		return domain.FactReview{}, nil
	}
	return f.createFn(ctx, repoID, actorID, factID, verdict, reason)
}

func (f *fakeFactReviewService) ReviewsForFacts(ctx context.Context, repoID int64, entities []domain.CodeEntity) (map[int64]domain.FactReview, error) {
	f.reviewsCalls++
	if f.reviewsFn == nil {
		return nil, nil
	}
	return f.reviewsFn(ctx, repoID, entities)
}

// fakeRepoQualityService is the RepoQualityService consumer double.
type fakeRepoQualityService struct {
	qualityFn func(context.Context, int64) (application.RepoQuality, error)
	calls     []int64
}

func (f *fakeRepoQualityService) Quality(ctx context.Context, repoID int64) (application.RepoQuality, error) {
	f.calls = append(f.calls, repoID)
	if f.qualityFn == nil {
		return application.RepoQuality{}, nil
	}
	return f.qualityFn(ctx, repoID)
}

// factReviewResponseWire is the data payload of the review create response.
type factReviewResponseWire struct {
	ID        string `json:"id"`
	FactID    string `json:"factId"`
	Commit    string `json:"commit"`
	Verdict   string `json:"verdict"`
	Reason    string `json:"reason"`
	CreatedBy int64  `json:"createdBy"`
	CreatedAt string `json:"createdAt"`
}

// TestCreateFactReviewAllowsRegularUser is the permission contract: any
// authenticated user (not only an admin) may record a verdict, and the
// handler forwards the authenticated actor to the service.
func TestCreateFactReviewAllowsRegularUser(t *testing.T) {
	service := &fakeFactReviewService{
		createFn: func(_ context.Context, repoID, actorID, factID int64, verdict, reason string) (domain.FactReview, error) {
			return domain.FactReview{
				ID: 9301, RepoID: repoID, FactID: factID, CommitSHA: commitB,
				Verdict: verdict, Reason: reason, CreatedBy: actorID, CreatedAt: factReviewCreatedAt,
			}, nil
		},
	}
	handler := newTestHandler(t, HandlerDeps{FactReviews: service})

	recorder := serveJSON(t, handler, http.MethodPost,
		"/api/code/repos/7/facts/1844674407370955201/reviews", userToken,
		map[string]string{"verdict": "incorrect", "reason": "代码已删除该接口"})

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Data factReviewResponseWire `json:"data"`
	}
	decodeBody(t, recorder, &body)
	review := body.Data
	if review.ID != "9301" || review.FactID != reviewFactWireID || review.Commit != commitB {
		t.Fatalf("review ids = %+v", review)
	}
	if review.Verdict != domain.FactReviewIncorrect || review.Reason != "代码已删除该接口" {
		t.Fatalf("review verdict/reason = %+v", review)
	}
	if review.CreatedBy != testUserID {
		t.Fatalf("createdBy = %d, want the regular user %d", review.CreatedBy, testUserID)
	}
	if !strings.HasPrefix(review.CreatedAt, "2026-10-29T11:00:00") {
		t.Fatalf("createdAt = %q, want the service timestamp", review.CreatedAt)
	}

	if len(service.createCalls) != 1 {
		t.Fatalf("create calls = %d, want 1", len(service.createCalls))
	}
	call := service.createCalls[0]
	if call.RepoID != 7 || call.ActorID != testUserID || call.FactID != 1844674407370955201 {
		t.Fatalf("create call = %+v", call)
	}
	if call.Verdict != domain.FactReviewIncorrect || call.Reason != "代码已删除该接口" {
		t.Fatalf("create payload = %+v", call)
	}
}

// TestCreateFactReviewMapsErrors pins the error envelope mapping: a foreign or
// missing fact is a 404 fact_not_found, an invalid verdict is the service's 422,
// and a malformed body is a 400 invalid_json.
func TestCreateFactReviewMapsErrors(t *testing.T) {
	t.Run("fact outside repository is 404", func(t *testing.T) {
		service := &fakeFactReviewService{
			createFn: func(context.Context, int64, int64, int64, string, string) (domain.FactReview, error) {
				return domain.FactReview{}, domain.NewNotFound("fact_not_found",
					"codeintel: fact 5001 not found in repository 7")
			},
		}
		handler := newTestHandler(t, HandlerDeps{FactReviews: service})
		recorder := serveJSON(t, handler, http.MethodPost,
			"/api/code/repos/7/facts/5001/reviews", userToken, map[string]string{"verdict": "correct"})
		assertErrorResponse(t, recorder, http.StatusNotFound, "not_found", "fact_not_found")
	})

	t.Run("invalid verdict is 422", func(t *testing.T) {
		service := &fakeFactReviewService{
			createFn: func(context.Context, int64, int64, int64, string, string) (domain.FactReview, error) {
				return domain.FactReview{}, domain.NewInvalid("verdict", "invalid_verdict",
					"codeintel: verdict must be correct or incorrect")
			},
		}
		handler := newTestHandler(t, HandlerDeps{FactReviews: service})
		recorder := serveJSON(t, handler, http.MethodPost,
			"/api/code/repos/7/facts/5001/reviews", userToken, map[string]string{"verdict": "maybe"})
		assertErrorResponse(t, recorder, http.StatusUnprocessableEntity, "invalid_argument", "invalid_verdict")
	})

	t.Run("malformed body is 400", func(t *testing.T) {
		service := &fakeFactReviewService{}
		handler := newTestHandler(t, HandlerDeps{FactReviews: service})
		recorder := serveRawBody(t, handler, http.MethodPost,
			"/api/code/repos/7/facts/5001/reviews", userToken, `{"verdict":`)
		assertErrorResponse(t, recorder, http.StatusBadRequest, "invalid_argument", "invalid_json")
		if len(service.createCalls) != 0 {
			t.Fatalf("malformed body reached the service: %+v", service.createCalls)
		}
	})

	t.Run("unknown body field is rejected", func(t *testing.T) {
		service := &fakeFactReviewService{}
		handler := newTestHandler(t, HandlerDeps{FactReviews: service})
		recorder := serveRawBody(t, handler, http.MethodPost,
			"/api/code/repos/7/facts/5001/reviews", userToken, `{"verdict":"correct","note":"x"}`)
		assertErrorResponse(t, recorder, http.StatusBadRequest, "invalid_argument", "invalid_json")
	})

	t.Run("unauthenticated is 401", func(t *testing.T) {
		handler := newTestHandler(t, HandlerDeps{FactReviews: &fakeFactReviewService{}})
		recorder := serveJSON(t, handler, http.MethodPost,
			"/api/code/repos/7/facts/5001/reviews", "", map[string]string{"verdict": "correct"})
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", recorder.Code)
		}
	})
}

// repoQualityWire is the wire shape of GET /api/code/repos/{repoId}/quality.
type repoQualityWire struct {
	RepoID      int64  `json:"repoId"`
	GeneratedAt string `json:"generatedAt"`
	Evidence    struct {
		Checks   int      `json:"checks"`
		Failures int      `json:"failures"`
		PassRate *float64 `json:"passRate"`
	} `json:"evidence"`
	InvalidFacts int `json:"invalidFacts"`
	Confidence   struct {
		High   int `json:"high"`
		Medium int `json:"medium"`
		Low    int `json:"low"`
		Total  int `json:"total"`
	} `json:"confidence"`
	Reviews struct {
		Total     int `json:"total"`
		Correct   int `json:"correct"`
		Incorrect int `json:"incorrect"`
	} `json:"reviews"`
	Impact struct {
		Precision   *float64 `json:"precision"`
		Recall      *float64 `json:"recall"`
		Available   bool     `json:"available"`
		Reason      string   `json:"reason"`
		EvalRunID   *string  `json:"evalRunId"`
		EvaluatedAt *string  `json:"evaluatedAt"`
	} `json:"impact"`
	CacheHitRate  *float64 `json:"cacheHitRate"`
	Analysis      *struct {
		ID         string `json:"id"`
		HeadCommit string `json:"headCommit"`
		AnalyzedAt string `json:"analyzedAt"`
	} `json:"analysis"`
	IndexedCommit string `json:"indexedCommit"`
}

// TestGetRepoQualityReturnsAggregate pins the read contract of the quality
// aggregate, including the additive analysis reference.
func TestGetRepoQualityReturnsAggregate(t *testing.T) {
	analysisID := int64(9101)
	evalRunID := int64(9201)
	precision := 0.75
	recall := 0.8
	passRate := 0.75
	cacheRate := 0.5
	generatedAt := factReviewCreatedAt
	analyzedAt := generatedAt.Add(-time.Hour)
	evaluatedAt := generatedAt.Add(-2 * time.Hour)
	service := &fakeRepoQualityService{
		qualityFn: func(context.Context, int64) (application.RepoQuality, error) {
			return application.RepoQuality{
				RepoID:      7,
				GeneratedAt: generatedAt,
				Evidence: application.QualityEvidence{
					Checks: 20, Failures: 5, PassRate: &passRate,
				},
				InvalidFacts: 2,
				Confidence: application.QualityConfidence{
					High: 4, Medium: 2, Low: 1, Total: 7,
				},
				Reviews: application.QualityReviews{Total: 3, Correct: 2, Incorrect: 1},
				Impact: application.QualityImpact{
					Precision: &precision, Recall: &recall, Available: true,
					EvalRunID: &evalRunID, EvaluatedAt: &evaluatedAt,
				},
				CacheHitRate: &cacheRate,
				Analysis: &application.QualityAnalysis{
					ID: analysisID, HeadCommit: commitB, AnalyzedAt: analyzedAt,
				},
				IndexedCommit: commitB,
			}, nil
		},
	}
	handler := newTestHandler(t, HandlerDeps{Quality: service})

	recorder := serveJSON(t, handler, http.MethodGet, "/api/code/repos/7/quality", userToken, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Data repoQualityWire `json:"data"`
	}
	decodeBody(t, recorder, &body)
	quality := body.Data
	if quality.RepoID != 7 || quality.InvalidFacts != 2 {
		t.Fatalf("quality header = %+v", quality)
	}
	if quality.Evidence.Checks != 20 || quality.Evidence.Failures != 5 ||
		quality.Evidence.PassRate == nil || *quality.Evidence.PassRate != passRate {
		t.Fatalf("evidence = %+v", quality.Evidence)
	}
	if quality.Confidence.High != 4 || quality.Confidence.Medium != 2 ||
		quality.Confidence.Low != 1 || quality.Confidence.Total != 7 {
		t.Fatalf("confidence = %+v", quality.Confidence)
	}
	if quality.Reviews.Total != 3 || quality.Reviews.Correct != 2 || quality.Reviews.Incorrect != 1 {
		t.Fatalf("reviews = %+v", quality.Reviews)
	}
	if !quality.Impact.Available || quality.Impact.Precision == nil ||
		*quality.Impact.Precision != precision || quality.Impact.Recall == nil ||
		*quality.Impact.Recall != recall {
		t.Fatalf("impact = %+v", quality.Impact)
	}
	if quality.Impact.EvalRunID == nil || *quality.Impact.EvalRunID != "9201" {
		t.Fatalf("impact evalRunId = %v, want the decimal string 9201", quality.Impact.EvalRunID)
	}
	if quality.CacheHitRate == nil || *quality.CacheHitRate != cacheRate {
		t.Fatalf("cache hit rate = %v", quality.CacheHitRate)
	}
	if quality.Analysis == nil || quality.Analysis.ID != "9101" ||
		quality.Analysis.HeadCommit != commitB {
		t.Fatalf("analysis = %+v", quality.Analysis)
	}
	if quality.IndexedCommit != commitB || quality.GeneratedAt == "" {
		t.Fatalf("quality metadata = %+v", quality)
	}
}

// TestGetRepoQualityMarksUnavailableEvaluation proves the degraded wire shape:
// a missing evaluation run is null precision/recall plus available=false and a
// stable reason, never a fabricated 0.
func TestGetRepoQualityMarksUnavailableEvaluation(t *testing.T) {
	service := &fakeRepoQualityService{
		qualityFn: func(context.Context, int64) (application.RepoQuality, error) {
			return application.RepoQuality{
				RepoID:      7,
				GeneratedAt: factReviewCreatedAt,
				Impact:      application.QualityImpact{Reason: "no_eval_run"},
			}, nil
		},
	}
	handler := newTestHandler(t, HandlerDeps{Quality: service})

	recorder := serveJSON(t, handler, http.MethodGet, "/api/code/repos/7/quality", userToken, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var body struct {
		Data repoQualityWire `json:"data"`
	}
	decodeBody(t, recorder, &body)
	quality := body.Data
	if quality.Evidence.PassRate != nil || quality.CacheHitRate != nil || quality.Analysis != nil {
		t.Fatalf("empty quality fabricated data: %+v", quality)
	}
	if quality.Impact.Available || quality.Impact.Precision != nil || quality.Impact.Recall != nil {
		t.Fatalf("impact = %+v, want null metrics", quality.Impact)
	}
	if quality.Impact.Reason != "no_eval_run" {
		t.Fatalf("impact reason = %q, want no_eval_run", quality.Impact.Reason)
	}
}

// TestGetRepoQualityMapsRepositoryError keeps the aggregate behind the same
// repository ownership check as the reads.
func TestGetRepoQualityMapsRepositoryError(t *testing.T) {
	service := &fakeRepoQualityService{
		qualityFn: func(context.Context, int64) (application.RepoQuality, error) {
			return application.RepoQuality{}, domain.NewInvalid("repoId", "code_repository_not_found",
				"code repository not found")
		},
	}
	handler := newTestHandler(t, HandlerDeps{Quality: service})
	recorder := serveJSON(t, handler, http.MethodGet, "/api/code/repos/999/quality", userToken, nil)
	assertErrorResponse(t, recorder, http.StatusUnprocessableEntity, "invalid_argument", "code_repository_not_found")
}

// factEntityWire is the facts page entity projection with the additive review.
type factEntityWire struct {
	ID     string             `json:"id"`
	Kind   string             `json:"kind"`
	Review *factReviewWireOut `json:"review"`
}

// factReviewWireOut is the persisted review projection nested in a fact.
type factReviewWireOut struct {
	ID        string `json:"id"`
	FactID    string `json:"factId"`
	Commit    string `json:"commit"`
	Verdict   string `json:"verdict"`
	Reason    string `json:"reason"`
	CreatedBy int64  `json:"createdBy"`
	CreatedAt string `json:"createdAt"`
}

// TestFactsIncludePersistedReview proves the facts page carries the persisted
// verdict of each fact so the panel can render「已人工修正/错误」without a
// second per-fact request.
func TestFactsIncludePersistedReview(t *testing.T) {
	query := &fakeQueryService{
		factsFn: func(_ context.Context, _ int64, filter domain.EntityFilter) ([]domain.CodeEntity, string, bool, error) {
			return []domain.CodeEntity{
				{
					ID: 5001, RepoID: 7, Commit: commitB, Kind: domain.KindAPI,
					Name: "POST /api/refund", Confidence: domain.ConfidenceHigh, Source: domain.SourceAI,
					FactKey: strPtr("api:POST /api/refund"),
				},
				{
					ID: 5002, RepoID: 7, Commit: commitB, Kind: domain.KindRoute,
					Name: "GET /orders", Confidence: domain.ConfidenceLow, Source: domain.SourceAI,
				},
			}, "", false, nil
		},
	}
	reviews := &fakeFactReviewService{
		reviewsFn: func(_ context.Context, repoID int64, entities []domain.CodeEntity) (map[int64]domain.FactReview, error) {
			if repoID != 7 || len(entities) != 2 {
				return nil, fmt.Errorf("reviews called with repo=%d entities=%d", repoID, len(entities))
			}
			return map[int64]domain.FactReview{
				5001: {
					ID: 9301, RepoID: 7, FactID: 5001, CommitSHA: commitB,
					Verdict: domain.FactReviewIncorrect, Reason: "代码已删除该接口",
					CreatedBy: testUserID, CreatedAt: factReviewCreatedAt,
				},
			}, nil
		},
	}
	handler := newTestHandler(t, HandlerDeps{Query: query, FactReviews: reviews})

	recorder := serveJSON(t, handler, http.MethodGet,
		"/api/code/repos/7/facts?commit="+commitB, userToken, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Data []factEntityWire `json:"data"`
	}
	decodeBody(t, recorder, &body)
	if len(body.Data) != 2 {
		t.Fatalf("entities = %d, want 2", len(body.Data))
	}
	reviewed := body.Data[0]
	if reviewed.ID != "5001" || reviewed.Review == nil {
		t.Fatalf("reviewed entity = %+v", reviewed)
	}
	if reviewed.Review.Verdict != domain.FactReviewIncorrect ||
		reviewed.Review.Reason != "代码已删除该接口" ||
		reviewed.Review.FactID != "5001" || reviewed.Review.Commit != commitB ||
		reviewed.Review.CreatedBy != testUserID {
		t.Fatalf("nested review = %+v", reviewed.Review)
	}
	if body.Data[1].Review != nil {
		t.Fatalf("unreviewed fact carries a review: %+v", body.Data[1].Review)
	}
}

// TestFactsTolerateReviewEnrichmentFailure keeps the additive review marker
// from ever failing the primary facts read.
func TestFactsTolerateReviewEnrichmentFailure(t *testing.T) {
	query := &fakeQueryService{
		factsFn: func(context.Context, int64, domain.EntityFilter) ([]domain.CodeEntity, string, bool, error) {
			return []domain.CodeEntity{{ID: 5001, RepoID: 7, Commit: commitB, Kind: domain.KindAPI}}, "", false, nil
		},
	}
	reviews := &fakeFactReviewService{
		reviewsFn: func(context.Context, int64, []domain.CodeEntity) (map[int64]domain.FactReview, error) {
			return nil, errors.New("review store unavailable")
		},
	}
	handler := newTestHandler(t, HandlerDeps{Query: query, FactReviews: reviews})

	recorder := serveJSON(t, handler, http.MethodGet, "/api/code/repos/7/facts", userToken, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with the facts still served (body %s)",
			recorder.Code, recorder.Body.String())
	}
	var body struct {
		Data []factEntityWire `json:"data"`
	}
	decodeBody(t, recorder, &body)
	if len(body.Data) != 1 || body.Data[0].Review != nil {
		t.Fatalf("facts without enrichment = %+v", body.Data)
	}
}

// TestFactReviewRoutesAreConditional pins the additive route mounting: a
// partially wired process only exposes the group whose service exists.
func TestFactReviewRoutesAreConditional(t *testing.T) {
	reviewsOnly := newTestHandler(t, HandlerDeps{FactReviews: &fakeFactReviewService{}})
	if recorder := serveJSON(t, reviewsOnly, http.MethodGet, "/api/code/repos/7/quality", userToken, nil); recorder.Code != http.StatusNotFound {
		t.Fatalf("quality without the quality service = %d, want 404", recorder.Code)
	}

	qualityOnly := newTestHandler(t, HandlerDeps{Quality: &fakeRepoQualityService{}})
	if recorder := serveJSON(t, qualityOnly, http.MethodPost, "/api/code/repos/7/facts/5001/reviews", userToken, map[string]string{"verdict": "correct"}); recorder.Code != http.StatusNotFound {
		t.Fatalf("review without the review service = %d, want 404", recorder.Code)
	}
}

// strPtr is a small helper for the fixture's optional fact key.
func strPtr(value string) *string { return &value }
