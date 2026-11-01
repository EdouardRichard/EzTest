package resourceapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
)

func proposalRequest(method, target string, actor *platform.Actor) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	if actor != nil {
		request = request.WithContext(platform.WithActor(request.Context(), *actor))
	}
	return request
}

func TestConfirmAndRejectPageRoutes(t *testing.T) {
	handler, svc := newHandler()
	page := resourceapp.PageView{
		ID: "1003", SUTID: "1001", EnvironmentID: "1002", Name: "Orders",
		URL: "https://app.example.com/orders", RouteTemplate: "/orders",
		Status: "approved", Source: "ai", CreatedAt: testNow, UpdatedAt: testNow,
	}
	svc.page = page

	request := proposalRequest(http.MethodPost, "/api/resources/pages/1003/confirm", admin)
	request.SetPathValue("pageId", "1003")
	recorder := httptest.NewRecorder()
	handler.ConfirmPage(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("confirm status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	dto := pageDTO{}
	decodeData(t, recorder, &dto)
	if dto.ID != "1003" || dto.Status != "approved" {
		t.Fatalf("dto = %+v", dto)
	}
	if svc.lastID != 1003 || svc.lastActor != 42 {
		t.Fatalf("service call = id %d actor %d", svc.lastID, svc.lastActor)
	}

	request = proposalRequest(http.MethodPost, "/api/resources/pages/1003/reject", admin)
	request.SetPathValue("pageId", "1003")
	recorder = httptest.NewRecorder()
	handler.RejectPage(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("reject status = %d", recorder.Code)
	}
	if svc.lastID != 1003 {
		t.Fatalf("reject id = %d", svc.lastID)
	}

	request = proposalRequest(http.MethodPost, "/api/resources/pages/1003/confirm", plainUser)
	request.SetPathValue("pageId", "1003")
	recorder = httptest.NewRecorder()
	handler.ConfirmPage(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin status = %d", recorder.Code)
	}

	request = proposalRequest(http.MethodPost, "/api/resources/pages/1003/confirm", nil)
	request.SetPathValue("pageId", "1003")
	recorder = httptest.NewRecorder()
	handler.ConfirmPage(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", recorder.Code)
	}

	svc.err = platform.Conflict("page is not proposed")
	request = proposalRequest(http.MethodPost, "/api/resources/pages/1003/confirm", admin)
	request.SetPathValue("pageId", "1003")
	recorder = httptest.NewRecorder()
	handler.ConfirmPage(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d", recorder.Code)
	}

	svc.err = platform.NotFound("page")
	request = proposalRequest(http.MethodPost, "/api/resources/pages/1003/confirm", admin)
	request.SetPathValue("pageId", "1003")
	recorder = httptest.NewRecorder()
	handler.ConfirmPage(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", recorder.Code)
	}

	request = proposalRequest(http.MethodPost, "/api/resources/pages/abc/confirm", admin)
	request.SetPathValue("pageId", "abc")
	recorder = httptest.NewRecorder()
	handler.ConfirmPage(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad id status = %d", recorder.Code)
	}
}

func TestConfirmAndRejectAPIRoutes(t *testing.T) {
	handler, svc := newHandler()
	api := resourceapp.APIView{
		ID: "1004", SUTID: "1001", EnvironmentID: "1002", Method: "POST",
		PathTemplate: "/api/refund", Name: "Refund", Status: "approved", Source: "code",
		CreatedAt: testNow, UpdatedAt: testNow,
	}
	svc.api = api

	request := proposalRequest(http.MethodPost, "/api/resources/apis/1004/confirm", admin)
	request.SetPathValue("apiId", "1004")
	recorder := httptest.NewRecorder()
	handler.ConfirmAPI(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("confirm status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	dto := apiDTO{}
	decodeData(t, recorder, &dto)
	if dto.ID != "1004" || dto.Status != "approved" || dto.Source != "code" {
		t.Fatalf("dto = %+v", dto)
	}

	request = proposalRequest(http.MethodPost, "/api/resources/apis/1004/reject", admin)
	request.SetPathValue("apiId", "1004")
	recorder = httptest.NewRecorder()
	handler.RejectAPI(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("reject status = %d", recorder.Code)
	}

	request = proposalRequest(http.MethodPost, "/api/resources/apis/1004/confirm", plainUser)
	request.SetPathValue("apiId", "1004")
	recorder = httptest.NewRecorder()
	handler.ConfirmAPI(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin status = %d", recorder.Code)
	}

	if svc.lastActor != 42 {
		t.Fatalf("last actor = %d", svc.lastActor)
	}
}
