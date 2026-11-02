package workspaceapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	workspaceapp "github.com/EdouardRichard/EzTest/backend/internal/workspace/application"
	workspaceapi "github.com/EdouardRichard/EzTest/backend/internal/workspace/interfaces/httpapi"
)

type fakeService struct {
	snapshot workspaceapp.Snapshot
	err      error

	calls  int
	viewer platform.Actor
	limit  int
}

func (f *fakeService) Snapshot(_ context.Context, viewer platform.Actor, limit int) (workspaceapp.Snapshot, error) {
	f.calls++
	f.viewer = viewer
	f.limit = limit
	return f.snapshot, f.err
}

func actorRequest(method, target string, actor platform.Actor) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	return request.WithContext(platform.WithActor(request.Context(), actor))
}

type todoItemResponse struct {
	Kind         string  `json:"kind"`
	Title        string  `json:"title"`
	VersionID    string  `json:"version_id"`
	VersionTitle string  `json:"version_title"`
	Severity     string  `json:"severity"`
	CreatedAt    string  `json:"created_at"`
	ExpiresAt    *string `json:"expires_at"`
	URL          string  `json:"url"`
}

type todoCountsResponse struct {
	RequirementConfirmation int `json:"requirement_confirmation"`
	CaseConfirmation        int `json:"case_confirmation"`
	IssueConfirmation       int `json:"issue_confirmation"`
	DesignPending           int `json:"design_pending"`
	HumanRequest            int `json:"human_request"`
	DeadWorkItem            int `json:"dead_work_item"`
}

type todoSnapshotResponse struct {
	Items  []todoItemResponse `json:"items"`
	Counts todoCountsResponse `json:"counts"`
}

func TestListTodosReturnsSnapshotEnvelope(t *testing.T) {
	expiresAt := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	createdAt := time.Date(2026, 11, 1, 15, 0, 0, 0, time.FixedZone("CST", 8*3600))
	service := &fakeService{snapshot: workspaceapp.Snapshot{
		Items: []workspaceapp.TodoItem{
			{
				Kind:         workspaceapp.KindHumanRequest,
				Title:        "接管请求",
				VersionID:    2001,
				VersionTitle: "订单中心",
				Severity:     workspaceapp.SeverityWarning,
				CreatedAt:    createdAt,
				ExpiresAt:    &expiresAt,
				URL:          "/versions/2001?tab=execution",
			},
			{
				Kind:         workspaceapp.KindRequirementConfirmation,
				Title:        "需求 A",
				VersionID:    2001,
				VersionTitle: "订单中心",
				Severity:     workspaceapp.SeverityWarning,
				CreatedAt:    createdAt,
				URL:          "/versions/2001?tab=requirements&requirement=4001",
			},
		},
		Counts: workspaceapp.Counts{
			RequirementConfirmation: 1,
			CaseConfirmation:        2,
			IssueConfirmation:       3,
			DesignPending:           4,
			HumanRequest:            5,
			DeadWorkItem:            6,
		},
	}}
	handler := workspaceapi.New(service)

	request := actorRequest(http.MethodGet, "/api/todos?limit=7", platform.Actor{ID: 1, Role: "admin"})
	recorder := httptest.NewRecorder()
	handler.ListTodos(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	if service.calls != 1 || service.limit != 7 || !service.viewer.IsAdmin() {
		t.Fatalf("service call = %+v, want one admin call with limit 7", service)
	}

	body := recorder.Body.Bytes()
	var envelope struct {
		Data todoSnapshotResponse `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(envelope.Data.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(envelope.Data.Items))
	}
	first := envelope.Data.Items[0]
	if first.Kind != workspaceapp.KindHumanRequest || first.Title != "接管请求" ||
		first.VersionID != "2001" || first.VersionTitle != "订单中心" ||
		first.Severity != workspaceapp.SeverityWarning ||
		first.URL != "/versions/2001?tab=execution" {
		t.Fatalf("first item = %+v", first)
	}
	if first.CreatedAt != "2026-11-01T07:00:00Z" {
		t.Fatalf("created_at = %q, want UTC RFC3339", first.CreatedAt)
	}
	if first.ExpiresAt == nil || *first.ExpiresAt != "2026-11-01T09:00:00Z" {
		t.Fatalf("expires_at = %v, want 2026-11-01T09:00:00Z", first.ExpiresAt)
	}
	if envelope.Data.Items[1].ExpiresAt != nil {
		t.Fatalf("second expires_at = %v, want null", *envelope.Data.Items[1].ExpiresAt)
	}

	wantCounts := todoCountsResponse{
		RequirementConfirmation: 1,
		CaseConfirmation:        2,
		IssueConfirmation:       3,
		DesignPending:           4,
		HumanRequest:            5,
		DeadWorkItem:            6,
	}
	if envelope.Data.Counts != wantCounts {
		t.Fatalf("counts = %+v, want %+v", envelope.Data.Counts, wantCounts)
	}

	// The contract counts object always carries all six keys.
	var raw struct {
		Data struct {
			Counts map[string]int `json:"counts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode raw counts: %v", err)
	}
	if len(raw.Data.Counts) != 6 {
		t.Fatalf("count keys = %d, want 6: %v", len(raw.Data.Counts), raw.Data.Counts)
	}
}

func TestListTodosDefaultsTo50(t *testing.T) {
	service := &fakeService{}
	handler := workspaceapi.New(service)
	request := actorRequest(http.MethodGet, "/api/todos", platform.Actor{ID: 1, Role: "admin"})
	recorder := httptest.NewRecorder()

	handler.ListTodos(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if service.limit != workspaceapp.DefaultLimit {
		t.Fatalf("limit = %d, want %d", service.limit, workspaceapp.DefaultLimit)
	}
	// An empty snapshot is a successful empty feed, not a 404.
	var envelope struct {
		Data todoSnapshotResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if envelope.Data.Items == nil || len(envelope.Data.Items) != 0 {
		t.Fatalf("items = %#v, want an empty array", envelope.Data.Items)
	}
}

func TestListTodosRejectsInvalidLimit(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		valid bool
	}{
		{name: "zero", raw: "0"},
		{name: "negative", raw: "-1"},
		{name: "above max", raw: "201"},
		{name: "not a number", raw: "abc"},
		{name: "decimal", raw: "1.5"},
		{name: "trimmed", raw: "%207%20", valid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeService{}
			handler := workspaceapi.New(service)
			request := actorRequest(http.MethodGet, "/api/todos?limit="+test.raw, platform.Actor{ID: 1, Role: "admin"})
			recorder := httptest.NewRecorder()

			handler.ListTodos(recorder, request)

			if test.valid {
				if recorder.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200 for a trimmed valid limit", recorder.Code)
				}
				if service.limit != 7 {
					t.Fatalf("limit = %d, want 7", service.limit)
				}
				return
			}
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 for limit %q", recorder.Code, test.raw)
			}
			if service.calls != 0 {
				t.Fatalf("service calls = %d, want 0 after a rejected limit", service.calls)
			}
			var envelope struct {
				Error struct {
					Code    string         `json:"code"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if envelope.Error.Code != "invalid_argument" || envelope.Error.Details["field"] != "limit" {
				t.Fatalf("error = %+v, want invalid_argument on limit", envelope.Error)
			}
		})
	}
}

func TestListTodosRequiresActor(t *testing.T) {
	service := &fakeService{}
	handler := workspaceapi.New(service)
	request := httptest.NewRequest(http.MethodGet, "/api/todos", nil)
	recorder := httptest.NewRecorder()

	handler.ListTodos(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if service.calls != 0 {
		t.Fatalf("service calls = %d, want 0", service.calls)
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if envelope.Error.Code != "unauthenticated" {
		t.Fatalf("error code = %q, want unauthenticated", envelope.Error.Code)
	}
}

func TestListTodosPropagatesServiceErrors(t *testing.T) {
	service := &fakeService{err: platform.Unavailable("execution query reader is not configured")}
	handler := workspaceapi.New(service)
	request := actorRequest(http.MethodGet, "/api/todos", platform.Actor{ID: 1, Role: "admin"})
	recorder := httptest.NewRecorder()

	handler.ListTodos(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if envelope.Error.Code != "unavailable" || !envelope.Error.Retryable {
		t.Fatalf("error = %+v, want retryable unavailable", envelope.Error)
	}
}

func TestRegisterRoutesMountsTodosBehindTheGivenMiddleware(t *testing.T) {
	service := &fakeService{}
	handler := workspaceapi.New(service)
	mux := http.NewServeMux()
	workspaceapi.RegisterRoutes(mux, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 9001, Role: "user"})))
		})
	}, handler)

	request := httptest.NewRequest(http.MethodGet, "/api/todos?limit=3", nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if service.calls != 1 || service.limit != 3 || service.viewer.Role != "user" {
		t.Fatalf("service call = %+v, want one regular-user call with limit 3", service)
	}
}
