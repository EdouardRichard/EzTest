package codeintelapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	codeintelapi "github.com/EdouardRichard/EzTest/backend/internal/codeintel/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

type stubImpactService struct {
	views        []codeintelapp.ImpactCandidateView
	listErr      error
	listCalls    []int64
	analysis     codeintel.Analysis
	union        []codeintelapp.ImpactCandidateView
	unionErr     error
	unionCalls   []int64
	selected     codeintel.ImpactCandidate
	selectedErr  error
	selectCall   []int64
	selectedFlag bool
	analysisVer  int64
	analysisErr  error
}

func (s *stubImpactService) List(_ context.Context, analysisID int64) ([]codeintelapp.ImpactCandidateView, error) {
	s.listCalls = append(s.listCalls, analysisID)
	return s.views, s.listErr
}

func (s *stubImpactService) Union(_ context.Context, versionID int64) ([]codeintelapp.ImpactCandidateView, error) {
	s.unionCalls = append(s.unionCalls, versionID)
	return s.union, s.unionErr
}

func (s *stubImpactService) SetSelected(_ context.Context, analysisID, candidateID, actorID int64, selected bool) (codeintel.ImpactCandidate, error) {
	s.selectCall = []int64{analysisID, candidateID, actorID}
	s.selectedFlag = selected
	return s.selected, s.selectedErr
}

func (s *stubImpactService) AnalysisVersion(_ context.Context, _ int64) (int64, error) {
	return s.analysisVer, s.analysisErr
}

func (s *stubImpactService) Detail(_ context.Context, analysisID int64) (codeintel.Analysis, []codeintelapp.ImpactCandidateView, error) {
	s.listCalls = append(s.listCalls, analysisID)
	return s.analysis, s.views, s.listErr
}

func impactTestMux(t *testing.T, svc codeintelapi.ImpactServicePort) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	authenticated := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := platform.WithActor(r.Context(), platform.Actor{ID: 42, Role: "admin"})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	codeintelapi.NewImpactHandler(svc).Register(mux, authenticated)
	return mux
}

func sampleCandidateView(repoID int64) codeintelapp.ImpactCandidateView {
	return codeintelapp.ImpactCandidateView{
		ImpactCandidate: codeintel.ImpactCandidate{
			ID: 81, AnalysisID: 5, SubjectKind: codeintel.SubjectRequirement, SubjectID: 100,
			Reason: "anchor hit", Confidence: codeintel.ConfidenceHigh, Source: codeintel.CandidateDirect,
			Evidence: []byte(`[{"file":"src/a.ts"}]`), Selected: true,
			CreatedAt: testTime, UpdatedAt: testTime,
		},
		RepoID:        repoID,
		VersionSUTIDs: []int64{11},
	}
}

func TestImpactUnionRoute(t *testing.T) {
	svc := &stubImpactService{union: []codeintelapp.ImpactCandidateView{sampleCandidateView(10)}}
	mux := impactTestMux(t, svc)
	recorder := doRequest(t, mux, http.MethodGet, "/api/versions/1/code/impact", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	items, _ := dataList(t, recorder)
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	view := items[0].(map[string]any)
	if view["id"] != "81" || view["subject_kind"] != "requirement" || view["repo_id"] != "10" ||
		view["version_sut_id"] != "11" || view["selected"] != true {
		t.Fatalf("view = %+v", view)
	}
	if len(svc.unionCalls) != 1 || svc.unionCalls[0] != 1 {
		t.Fatalf("union calls = %v", svc.unionCalls)
	}

	recorder = doRequest(t, mux, http.MethodGet, "/api/versions/1/code/impact?repoId=11", "")
	if items, _ := dataList(t, recorder); len(items) != 0 {
		t.Fatalf("repo filter = %+v", items)
	}
	recorder = doRequest(t, mux, http.MethodGet, "/api/versions/1/code/impact?repoId=abc", "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad repo status = %d", recorder.Code)
	}
	svc.unionErr = codeintel.ErrNotFound
	recorder = doRequest(t, mux, http.MethodGet, "/api/versions/1/code/impact", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", recorder.Code)
	}
}

func TestImpactPatchCandidateRoute(t *testing.T) {
	candidate := sampleCandidateView(10)
	svc := &stubImpactService{selected: candidate.ImpactCandidate, analysisVer: 1}
	mux := impactTestMux(t, svc)

	recorder := doRequest(t, mux, http.MethodPatch, "/api/versions/1/code/analyses/5/candidates/81", `{"selected":false}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	view := dataObject(t, recorder)
	if view["id"] != "81" || view["selected"] != true {
		t.Fatalf("view = %+v", view)
	}
	if len(svc.selectCall) != 3 || svc.selectCall[0] != 5 || svc.selectCall[1] != 81 || svc.selectCall[2] != 42 {
		t.Fatalf("select call = %+v", svc.selectCall)
	}
	if svc.selectedFlag {
		t.Fatal("selected flag was not passed through")
	}

	recorder = doRequest(t, mux, http.MethodPatch, "/api/versions/1/code/analyses/5/candidates/81", `{}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing selected status = %d", recorder.Code)
	}
	recorder = doRequest(t, mux, http.MethodPatch, "/api/versions/1/code/analyses/5/candidates/abc", `{"selected":true}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad candidate id status = %d", recorder.Code)
	}

	svc.analysisVer = 2
	recorder = doRequest(t, mux, http.MethodPatch, "/api/versions/1/code/analyses/5/candidates/81", `{"selected":true}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("foreign analysis status = %d", recorder.Code)
	}

	svc.analysisVer = 1
	svc.selectedErr = codeintel.ErrNotFound
	recorder = doRequest(t, mux, http.MethodPatch, "/api/versions/1/code/analyses/5/candidates/81", `{"selected":true}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing candidate status = %d", recorder.Code)
	}
}

func TestImpactAnalysisDetailWithCandidates(t *testing.T) {
	svc := &stubImpactService{analysis: sampleAnalysis(), views: []codeintelapp.ImpactCandidateView{sampleCandidateView(10)}}
	handler := codeintelapi.NewImpactHandler(svc)
	request := httptest.NewRequest(http.MethodGet, "/api/versions/1/code/analyses/7", nil)
	request.SetPathValue("versionId", "1")
	request.SetPathValue("analysisId", "7")
	recorder := httptest.NewRecorder()
	handler.Detail(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("detail status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	detail := dataObject(t, recorder)
	candidates := detail["candidates"].([]any)
	if len(candidates) != 1 || candidates[0].(map[string]any)["subject_kind"] != "requirement" {
		t.Fatalf("candidates = %+v", candidates)
	}
	unmapped := detail["unmapped"].([]any)
	if len(unmapped) != 1 {
		t.Fatalf("unmapped = %+v", unmapped)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/versions/2/code/analyses/7", nil)
	request.SetPathValue("versionId", "2")
	request.SetPathValue("analysisId", "7")
	recorder = httptest.NewRecorder()
	handler.Detail(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("foreign version status = %d", recorder.Code)
	}
}
