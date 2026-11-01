package codeintelapi_test

import (
	"context"
	"net/http"
	"testing"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintelapi "github.com/EdouardRichard/EzTest/backend/internal/codeintel/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

type stubProposalService struct {
	result       codeintelapp.ProposalResult
	generateErr  error
	generateCall struct {
		versionID, repoID int64
	}
	views   []codeintelapp.ProposalView
	listErr error
	listID  int64
}

func (s *stubProposalService) Generate(_ context.Context, versionID, repoID int64) (codeintelapp.ProposalResult, error) {
	s.generateCall.versionID, s.generateCall.repoID = versionID, repoID
	return s.result, s.generateErr
}

func (s *stubProposalService) List(_ context.Context, versionID int64) ([]codeintelapp.ProposalView, error) {
	s.listID = versionID
	return s.views, s.listErr
}

func proposalTestMux(t *testing.T, svc codeintelapi.ProposalServicePort) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	authenticated := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := platform.WithActor(r.Context(), platform.Actor{ID: 42, Role: "user"})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	codeintelapi.NewProposalHandler(svc).Register(mux, authenticated)
	return mux
}

func TestGenerateProposalsRoute(t *testing.T) {
	svc := &stubProposalService{result: codeintelapp.ProposalResult{PagesCreated: 2, PagesSkipped: 1, APIsCreated: 1}}
	mux := proposalTestMux(t, svc)

	recorder := doRequest(t, mux, http.MethodPost, "/api/versions/1/code/page-api-proposals", `{"repoId":"10"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	view := dataObject(t, recorder)
	pages := view["pages"].(map[string]any)
	apis := view["apis"].(map[string]any)
	if pages["created"] != float64(2) || pages["skipped"] != float64(1) || apis["created"] != float64(1) || apis["skipped"] != float64(0) {
		t.Fatalf("view = %+v", view)
	}
	if svc.generateCall.versionID != 1 || svc.generateCall.repoID != 10 {
		t.Fatalf("generate call = %+v", svc.generateCall)
	}

	recorder = doRequest(t, mux, http.MethodPost, "/api/versions/1/code/page-api-proposals", `{}`)
	if recorder.Code != http.StatusOK || svc.generateCall.repoID != 0 {
		t.Fatalf("optional repo status = %d (repo %d)", recorder.Code, svc.generateCall.repoID)
	}
	recorder = doRequest(t, mux, http.MethodPost, "/api/versions/1/code/page-api-proposals", `{"repoId":"abc"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad repo status = %d", recorder.Code)
	}
	svc.generateErr = codeintelappError(422, "analysis_not_found")
	recorder = doRequest(t, mux, http.MethodPost, "/api/versions/1/code/page-api-proposals", `{}`)
	if recorder.Code != http.StatusUnprocessableEntity || errorObject(t, recorder)["code"] != "analysis_not_found" {
		t.Fatalf("missing analysis = %d (%s)", recorder.Code, recorder.Body.String())
	}
}

func TestListProposalsRoute(t *testing.T) {
	svc := &stubProposalService{views: []codeintelapp.ProposalView{{
		Kind: "page", ID: 501, Name: "Orders", URL: "https://staging.example.test/orders",
		RouteTemplate: "/orders", Source: "ai", Status: "proposed",
	}, {
		Kind: "api", ID: 601, Name: "Refund", Method: "POST", PathTemplate: "/api/refund",
		Source: "code", Status: "proposed",
	}}}
	mux := proposalTestMux(t, svc)
	recorder := doRequest(t, mux, http.MethodGet, "/api/versions/1/code/page-api-proposals", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	items, _ := dataList(t, recorder)
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	page := items[0].(map[string]any)
	if page["kind"] != "page" || page["id"] != "501" || page["route_template"] != "/orders" || page["source"] != "ai" {
		t.Fatalf("page view = %+v", page)
	}
	api := items[1].(map[string]any)
	if api["kind"] != "api" || api["id"] != "601" || api["method"] != "POST" || api["status"] != "proposed" {
		t.Fatalf("api view = %+v", api)
	}
	if svc.listID != 1 {
		t.Fatalf("list id = %d", svc.listID)
	}
}

func codeintelappError(status int, code string) error {
	return platform.NewError(status, code, "test error")
}
