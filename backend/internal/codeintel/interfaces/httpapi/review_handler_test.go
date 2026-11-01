package codeintelapi_test

import (
	"context"
	"net/http"
	"testing"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	codeintelapi "github.com/EdouardRichard/EzTest/backend/internal/codeintel/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

type stubReviewService struct {
	reviews    []codeintel.FactReview
	listErr    error
	listCall   []int64
	created    codeintel.FactReview
	createErr  error
	createCall struct {
		versionID, factID, reviewerID int64
		verdict                       codeintel.ReviewVerdict
		reason                        string
	}
}

func (s *stubReviewService) List(_ context.Context, versionID, factID int64) ([]codeintel.FactReview, error) {
	s.listCall = []int64{versionID, factID}
	return s.reviews, s.listErr
}

func (s *stubReviewService) Create(_ context.Context, versionID, factID, reviewerID int64, verdict codeintel.ReviewVerdict, reason string) (codeintel.FactReview, error) {
	s.createCall.versionID, s.createCall.factID, s.createCall.reviewerID = versionID, factID, reviewerID
	s.createCall.verdict, s.createCall.reason = verdict, reason
	return s.created, s.createErr
}

func reviewTestMux(t *testing.T, svc codeintelapi.ReviewServicePort) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	authenticated := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := platform.WithActor(r.Context(), platform.Actor{ID: 42, Role: "user"})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	codeintelapi.NewReviewHandler(svc).Register(mux, authenticated)
	return mux
}

func sampleReview() codeintel.FactReview {
	return codeintel.FactReview{
		ID: 71, FactID: 9, FactVersion: 2, RepoID: 10, CommitSHA: testCommitA,
		Verdict: "incorrect", Reason: "moved", ReviewerID: 42,
		CreatedAt: testTime, UpdatedAt: testTime,
	}
}

func TestListAndCreateReviewRoutes(t *testing.T) {
	svc := &stubReviewService{reviews: []codeintel.FactReview{sampleReview()}, created: sampleReview()}
	mux := reviewTestMux(t, svc)

	recorder := doRequest(t, mux, http.MethodGet, "/api/versions/1/code/facts/9/reviews", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	items, _ := dataList(t, recorder)
	if len(items) != 1 {
		t.Fatalf("reviews = %+v", items)
	}
	view := items[0].(map[string]any)
	if view["id"] != "71" || view["fact_id"] != "9" || view["verdict"] != "incorrect" || view["reason"] != "moved" {
		t.Fatalf("review view = %+v", view)
	}
	if len(svc.listCall) != 2 || svc.listCall[0] != 1 || svc.listCall[1] != 9 {
		t.Fatalf("list call = %+v", svc.listCall)
	}

	recorder = doRequest(t, mux, http.MethodPost, "/api/versions/1/code/facts/9/reviews", `{"verdict":"incorrect","reason":"moved"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	if view := dataObject(t, recorder); view["verdict"] != "incorrect" {
		t.Fatalf("created view = %+v", view)
	}
	if svc.createCall.versionID != 1 || svc.createCall.factID != 9 || svc.createCall.reviewerID != 42 ||
		svc.createCall.verdict != codeintel.ReviewIncorrect || svc.createCall.reason != "moved" {
		t.Fatalf("create call = %+v", svc.createCall)
	}

	recorder = doRequest(t, mux, http.MethodPost, "/api/versions/1/code/facts/9/reviews", `{"verdict":"maybe"}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid verdict status = %d", recorder.Code)
	}
	recorder = doRequest(t, mux, http.MethodPost, "/api/versions/1/code/facts/9/reviews", `{"verdict":`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad body status = %d", recorder.Code)
	}
	recorder = doRequest(t, mux, http.MethodGet, "/api/versions/1/code/facts/abc/reviews", "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad fact id status = %d", recorder.Code)
	}
	svc.listErr = codeintel.ErrNotFound
	recorder = doRequest(t, mux, http.MethodGet, "/api/versions/1/code/facts/9/reviews", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing fact status = %d", recorder.Code)
	}
}
