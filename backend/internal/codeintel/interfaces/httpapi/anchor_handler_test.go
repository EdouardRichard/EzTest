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

type stubAnchorService struct {
	create       codeintel.Anchor
	createErr    error
	createCalls  []codeintelapp.CreateAnchorInput
	list         []codeintel.Anchor
	listErr      error
	listVersion  int64
	listKind     string
	listSubject  int64
	validated    codeintel.Anchor
	validateErr  error
	validateCall []int64
	deleteErr    error
	deleteCalls  []int64
}

func (s *stubAnchorService) Create(_ context.Context, in codeintelapp.CreateAnchorInput) (codeintel.Anchor, error) {
	s.createCalls = append(s.createCalls, in)
	return s.create, s.createErr
}

func (s *stubAnchorService) List(_ context.Context, versionID int64, subjectKind string, subjectID int64) ([]codeintel.Anchor, error) {
	s.listVersion, s.listKind, s.listSubject = versionID, subjectKind, subjectID
	return s.list, s.listErr
}

func (s *stubAnchorService) Validate(_ context.Context, versionID, anchorID int64) (codeintel.Anchor, error) {
	s.validateCall = []int64{versionID, anchorID}
	return s.validated, s.validateErr
}

func (s *stubAnchorService) Delete(_ context.Context, versionID, anchorID int64) error {
	s.deleteCalls = append(s.deleteCalls, versionID, anchorID)
	return s.deleteErr
}

func anchorTestMux(t *testing.T, svc codeintelapi.AnchorServicePort) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	authenticated := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := platform.WithActor(r.Context(), platform.Actor{ID: 42, Role: "admin"})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	codeintelapi.NewAnchorHandler(svc).Register(mux, authenticated)
	return mux
}

func sampleAnchor() codeintel.Anchor {
	start, end := 2, 3
	now := testTime
	return codeintel.Anchor{
		ID: 61, VersionID: 1, RepoID: 10, SubjectKind: codeintel.AnchorSubjectRequirement,
		SubjectID: 100, FilePath: "src/orders.ts", StartLine: &start, EndLine: &end,
		CommitSHA: testCommitA, ExcerptSHA: "sha256:abc", Status: "valid", Confidence: "high",
		CreatedByKind: "user", LastValidatedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
}

func TestCreateAnchorRoute(t *testing.T) {
	anchor := sampleAnchor()
	svc := &stubAnchorService{create: anchor}
	mux := anchorTestMux(t, svc)
	body := `{"subjectKind":"requirement","subjectId":"100","repoId":"10","filePath":"src/orders.ts","startLine":2,"endLine":3,"excerptSha":"sha256:abc","commit":"` + testCommitA + `"}`
	recorder := doRequest(t, mux, http.MethodPost, "/api/versions/1/code/anchors", body)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	view := dataObject(t, recorder)
	if view["id"] != "61" || view["subject_kind"] != "requirement" || view["file_path"] != "src/orders.ts" ||
		view["status"] != "valid" || view["excerpt_sha"] != "sha256:abc" {
		t.Fatalf("view = %+v", view)
	}
	if len(svc.createCalls) != 1 {
		t.Fatalf("create calls = %+v", svc.createCalls)
	}
	in := svc.createCalls[0]
	if in.VersionID != 1 || in.SubjectID != 100 || in.RepoID != 10 || in.FilePath != "src/orders.ts" ||
		in.StartLine == nil || *in.StartLine != 2 || in.EndLine == nil || *in.EndLine != 3 ||
		in.ExcerptSHA != "sha256:abc" || in.CommitSHA != testCommitA {
		t.Fatalf("input = %+v", in)
	}

	duplicate := platform.NewError(http.StatusConflict, "anchor_duplicate", "duplicate")
	svc.createErr = duplicate
	recorder = doRequest(t, mux, http.MethodPost, "/api/versions/1/code/anchors", body)
	if recorder.Code != http.StatusConflict || errorObject(t, recorder)["code"] != "anchor_duplicate" {
		t.Fatalf("duplicate status = %d (%s)", recorder.Code, recorder.Body.String())
	}

	recorder = doRequest(t, mux, http.MethodPost, "/api/versions/1/code/anchors", `{"subjectKind":`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad body status = %d", recorder.Code)
	}
	recorder = doRequest(t, mux, http.MethodPost, "/api/versions/1/code/anchors", `{"subjectKind":"requirement","subjectId":"nope","repoId":"10","filePath":"a"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad id status = %d", recorder.Code)
	}
}

func TestListAnchorRoute(t *testing.T) {
	svc := &stubAnchorService{list: []codeintel.Anchor{sampleAnchor()}}
	mux := anchorTestMux(t, svc)
	recorder := doRequest(t, mux, http.MethodGet, "/api/versions/1/code/anchors?subjectKind=requirement&subjectId=100", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	items, envelope := dataList(t, recorder)
	if len(items) != 1 || envelope["has_more"] != false {
		t.Fatalf("list = %s", recorder.Body.String())
	}
	if svc.listVersion != 1 || svc.listKind != "requirement" || svc.listSubject != 100 {
		t.Fatalf("list filter = %d/%s/%d", svc.listVersion, svc.listKind, svc.listSubject)
	}
	recorder = doRequest(t, mux, http.MethodGet, "/api/versions/1/code/anchors", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("unfiltered status = %d", recorder.Code)
	}
	svc.list = nil
	recorder = doRequest(t, mux, http.MethodGet, "/api/versions/1/code/anchors", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("empty status = %d", recorder.Code)
	}
	if items, _ := dataList(t, recorder); len(items) != 0 {
		t.Fatalf("empty list = %+v", items)
	}
}

func TestValidateAndDeleteAnchorRoutes(t *testing.T) {
	svc := &stubAnchorService{validated: sampleAnchor()}
	mux := anchorTestMux(t, svc)
	recorder := doRequest(t, mux, http.MethodPost, "/api/versions/1/code/anchors/61/validate", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("validate status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	if view := dataObject(t, recorder); view["id"] != "61" || view["status"] != "valid" {
		t.Fatalf("validate view = %+v", view)
	}
	if len(svc.validateCall) != 2 || svc.validateCall[0] != 1 || svc.validateCall[1] != 61 {
		t.Fatalf("validate call = %+v", svc.validateCall)
	}

	recorder = doRequest(t, mux, http.MethodDelete, "/api/versions/1/code/anchors/61", "")
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	if len(svc.deleteCalls) != 2 || svc.deleteCalls[0] != 1 || svc.deleteCalls[1] != 61 {
		t.Fatalf("delete calls = %+v", svc.deleteCalls)
	}

	svc.validateErr = codeintel.ErrNotFound
	recorder = doRequest(t, mux, http.MethodPost, "/api/versions/1/code/anchors/62/validate", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing validate status = %d", recorder.Code)
	}
	svc.deleteErr = codeintel.ErrNotFound
	recorder = doRequest(t, mux, http.MethodDelete, "/api/versions/1/code/anchors/62", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing delete status = %d", recorder.Code)
	}

	recorder = doRequest(t, mux, http.MethodPost, "/api/versions/1/code/anchors/abc/validate", "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad anchor id status = %d", recorder.Code)
	}
	recorder = doRequest(t, mux, http.MethodGet, "/api/versions/abc/code/anchors", "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad version id status = %d", recorder.Code)
	}
}

func TestAnchorRoutesRequireAuth(t *testing.T) {
	mux := http.NewServeMux()
	authenticated := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := platform.ActorFrom(r.Context()); !ok {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	codeintelapi.NewAnchorHandler(&stubAnchorService{}).Register(mux, authenticated)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/versions/1/code/anchors", nil)
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d", recorder.Code)
	}
}
