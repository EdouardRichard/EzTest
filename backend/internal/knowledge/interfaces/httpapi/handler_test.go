package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	knowledgeapp "github.com/EdouardRichard/EzTest/backend/internal/knowledge/application"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	knowledgeapi "github.com/EdouardRichard/EzTest/backend/internal/knowledge/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

var handlerNow = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

/* --------------------------------- fakes ---------------------------------- */

type fakeService struct {
	listPage   knowledgeapp.Page
	listFilter knowledgeapp.ListFilter
	listErr    error
	listCalls  int

	getItem  knowledge.Item
	getErr   error
	getID    int64
	getCalls int

	createItem  knowledge.Item
	createInput knowledgeapp.CreateInput
	createActor platform.Actor
	createErr   error
	createCalls int

	updateItem  knowledge.Item
	updateID    int64
	updateInput knowledgeapp.UpdateInput
	updateActor platform.Actor
	updateErr   error
	updateCalls int

	reviewItem   knowledge.Item
	reviewID     int64
	reviewReason string
	reviewAction string
	reviewActor  platform.Actor
	reviewErr    error
	reviewCalls  int

	revisions     []knowledge.Revision
	revisionsErr  error
	revisionsID   int64
	revisionsCall int
}

func (f *fakeService) List(_ context.Context, filter knowledgeapp.ListFilter) (knowledgeapp.Page, error) {
	f.listCalls++
	f.listFilter = filter
	return f.listPage, f.listErr
}

func (f *fakeService) Get(_ context.Context, id int64) (knowledge.Item, error) {
	f.getCalls++
	f.getID = id
	return f.getItem, f.getErr
}

func (f *fakeService) Create(_ context.Context, actor platform.Actor, input knowledgeapp.CreateInput) (knowledge.Item, error) {
	f.createCalls++
	f.createActor = actor
	f.createInput = input
	return f.createItem, f.createErr
}

func (f *fakeService) Update(_ context.Context, actor platform.Actor, id int64, input knowledgeapp.UpdateInput) (knowledge.Item, error) {
	f.updateCalls++
	f.updateActor = actor
	f.updateID = id
	f.updateInput = input
	return f.updateItem, f.updateErr
}

func (f *fakeService) Approve(_ context.Context, actor platform.Actor, id int64, reason string) (knowledge.Item, error) {
	f.reviewCalls++
	f.reviewAction = "approve"
	f.reviewActor = actor
	f.reviewID = id
	f.reviewReason = reason
	return f.reviewItem, f.reviewErr
}

func (f *fakeService) Reject(_ context.Context, actor platform.Actor, id int64, reason string) (knowledge.Item, error) {
	f.reviewCalls++
	f.reviewAction = "reject"
	f.reviewActor = actor
	f.reviewID = id
	f.reviewReason = reason
	return f.reviewItem, f.reviewErr
}

func (f *fakeService) Revoke(_ context.Context, actor platform.Actor, id int64, reason string) (knowledge.Item, error) {
	f.reviewCalls++
	f.reviewAction = "revoke"
	f.reviewActor = actor
	f.reviewID = id
	f.reviewReason = reason
	return f.reviewItem, f.reviewErr
}

func (f *fakeService) Revisions(_ context.Context, id int64) ([]knowledge.Revision, error) {
	f.revisionsCall++
	f.revisionsID = id
	return f.revisions, f.revisionsErr
}

/* --------------------------------- helpers -------------------------------- */

func newKnowledgeMux(service knowledgeapi.Service, role string) *http.ServeMux {
	mux := http.NewServeMux()
	authenticated := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if role == "" {
				next.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 77, Role: role})))
		})
	}
	admin := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if role == "" {
				next.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 88, Role: role})))
		})
	}
	knowledgeapi.RegisterRoutes(mux, authenticated, admin, knowledgeapi.New(service))
	return mux
}

func perform(t *testing.T, mux *http.ServeMux, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	decoded := map[string]any{}
	if recorder.Body.Len() > 0 {
		if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("response is not JSON: %v (%s)", err, recorder.Body.String())
		}
	}
	return recorder, decoded
}

func assertErrorCode(t *testing.T, status int, body map[string]any, wantStatus int, wantCode string) {
	t.Helper()
	if status != wantStatus {
		t.Fatalf("status = %d body=%v, want %d", status, body, wantStatus)
	}
	envelope, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("body = %v, want error envelope", body)
	}
	if envelope["code"] != wantCode {
		t.Fatalf("error code = %v, want %s", envelope["code"], wantCode)
	}
}

func fixtureItem() knowledge.Item {
	systemKey := "payments"
	reason := "needs a source version"
	return knowledge.Item{
		ID:           9001,
		Scope:        knowledge.ScopeSystem,
		SystemKey:    &systemKey,
		Title:        "Retry contract",
		Content:      "Callbacks must be idempotent.",
		Tags:         []string{"payments", "callback"},
		Status:       knowledge.StatusApproved,
		Revision:     2,
		ReviewReason: &reason,
		ReviewedBy:   ptr[int64](88),
		ReviewedAt:   ptr(handlerNow),
		RevokedAt:    ptr(handlerNow.Add(-time.Hour)),
		CreatedBy:    ptr[int64](7),
		CreatedAt:    handlerNow.Add(-2 * time.Hour),
		UpdatedAt:    handlerNow.Add(-time.Hour),
		SourceStale:  true,
	}
}

func fixtureRevision() knowledge.Revision {
	return knowledge.Revision{
		ID:          2,
		KnowledgeID: 9001,
		Revision:    2,
		Title:       "Retry contract v2",
		Content:     "Updated body",
		Tags:        []string{"payments"},
		ChangedBy:   ptr[int64](7),
		CreatedAt:   handlerNow,
	}
}

func ptr[T any](value T) *T { return &value }

/* ------------------------------- auth matrix ------------------------------ */

func TestKnowledgeRoutesRequireAuthentication(t *testing.T) {
	fake := &fakeService{listPage: knowledgeapp.Page{Items: []knowledge.Item{}}, getItem: fixtureItem()}
	mux := newKnowledgeMux(fake, "")
	routes := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/knowledge/items", ""},
		{http.MethodPost, "/api/knowledge/items", `{"title":"t","content":"c"}`},
		{http.MethodGet, "/api/knowledge/items/9001", ""},
		{http.MethodPatch, "/api/knowledge/items/9001", `{"title":"new"}`},
		{http.MethodGet, "/api/knowledge/items/9001/revisions", ""},
	}
	for _, route := range routes {
		recorder, body := perform(t, mux, route.method, route.path, route.body)
		assertErrorCode(t, recorder.Code, body, http.StatusUnauthorized, "unauthenticated")
	}
	for _, path := range []string{
		"/api/knowledge/items/9001/approve",
		"/api/knowledge/items/9001/reject",
		"/api/knowledge/items/9001/revoke",
	} {
		recorder, body := perform(t, mux, http.MethodPost, path, `{"reason":"why"}`)
		assertErrorCode(t, recorder.Code, body, http.StatusUnauthorized, "unauthenticated")
	}
}

func TestKnowledgeReviewRoutesRequireAdmin(t *testing.T) {
	fake := &fakeService{reviewItem: fixtureItem()}
	mux := newKnowledgeMux(fake, "user")
	for _, path := range []string{
		"/api/knowledge/items/9001/approve",
		"/api/knowledge/items/9001/reject",
		"/api/knowledge/items/9001/revoke",
	} {
		recorder, body := perform(t, mux, http.MethodPost, path, `{"reason":"why"}`)
		assertErrorCode(t, recorder.Code, body, http.StatusForbidden, "forbidden")
	}
	if fake.reviewCalls != 0 {
		t.Fatalf("review calls = %d", fake.reviewCalls)
	}
}

/* ---------------------------------- list ---------------------------------- */

func TestListItemsParsesQueryAndReturnsCursor(t *testing.T) {
	fake := &fakeService{listPage: knowledgeapp.Page{
		Items:      []knowledge.Item{fixtureItem(), fixtureItem()},
		NextCursor: 9001,
		HasMore:    true,
	}}
	mux := newKnowledgeMux(fake, "user")
	recorder, body := perform(t, mux, http.MethodGet,
		"/api/knowledge/items?scope=system&status=approved&system_key=payments&tag=callback&q=retry&cursor=9500&limit=2", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%v", recorder.Code, body)
	}
	filter := fake.listFilter
	if filter.Scope != knowledge.ScopeSystem || filter.Status != knowledge.StatusApproved ||
		filter.SystemKey != "payments" || filter.Tag != "callback" || filter.Query != "retry" ||
		filter.Cursor != 9500 || filter.Limit != 2 {
		t.Fatalf("filter = %+v", filter)
	}
	if body["next_cursor"] != "9001" || body["has_more"] != true {
		t.Fatalf("pagination = %v / %v", body["next_cursor"], body["has_more"])
	}
	items, ok := body["data"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("data = %#v", body["data"])
	}
	first := items[0].(map[string]any)
	if first["id"] != "9001" || first["scope"] != "system" || first["system_key"] != "payments" ||
		first["source_stale"] != true {
		t.Fatalf("item view = %#v", first)
	}
	if first["created_at"] != handlerNow.Add(-2*time.Hour).Format(time.RFC3339) {
		t.Fatalf("created_at = %v", first["created_at"])
	}
}

func TestListItemsAppliesDefaults(t *testing.T) {
	fake := &fakeService{listPage: knowledgeapp.Page{Items: []knowledge.Item{}}}
	mux := newKnowledgeMux(fake, "user")
	recorder, body := perform(t, mux, http.MethodGet, "/api/knowledge/items", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%v", recorder.Code, body)
	}
	if fake.listFilter.Limit != knowledgeapp.DefaultListLimit || fake.listFilter.Cursor != 0 {
		t.Fatalf("filter = %+v", fake.listFilter)
	}
	if items, ok := body["data"].([]any); !ok || len(items) != 0 {
		t.Fatalf("data = %#v", body["data"])
	}
	if body["next_cursor"] != "" || body["has_more"] != false {
		t.Fatalf("pagination = %v / %v", body["next_cursor"], body["has_more"])
	}
}

func TestListItemsRejectsInvalidParameters(t *testing.T) {
	tests := []struct {
		name  string
		query string
		field string
	}{
		{name: "bad scope", query: "?scope=tenant", field: "scope"},
		{name: "bad status", query: "?status=pending", field: "status"},
		{name: "global with system key", query: "?scope=global&system_key=payments", field: "system_key"},
		{name: "bad cursor", query: "?cursor=abc", field: "cursor"},
		{name: "negative cursor", query: "?cursor=-1", field: "cursor"},
		{name: "zero limit", query: "?limit=0", field: "limit"},
		{name: "large limit", query: "?limit=101", field: "limit"},
		{name: "bad limit", query: "?limit=many", field: "limit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeService{listPage: knowledgeapp.Page{Items: []knowledge.Item{}}}
			mux := newKnowledgeMux(fake, "user")
			recorder, body := perform(t, mux, http.MethodGet, "/api/knowledge/items"+test.query, "")
			assertErrorCode(t, recorder.Code, body, http.StatusBadRequest, "invalid_argument")
			envelope := body["error"].(map[string]any)
			details := envelope["details"].(map[string]any)
			if details["field"] != test.field {
				t.Fatalf("field = %v, want %s", details["field"], test.field)
			}
			if fake.listCalls != 0 {
				t.Fatalf("service calls = %d", fake.listCalls)
			}
		})
	}
}

/* --------------------------------- create --------------------------------- */

func TestCreateItemParsesBodyAndReturns201(t *testing.T) {
	fake := &fakeService{createItem: fixtureItem()}
	mux := newKnowledgeMux(fake, "user")
	body := `{
		"title":"Retry contract",
		"content":"Callbacks must be idempotent.",
		"scope":"system",
		"system_key":"payments",
		"tags":["payments","callback"],
		"source_version_id":"500",
		"promoted_from_version_id":"501",
		"status":"approved"
	}`
	recorder, decoded := perform(t, mux, http.MethodPost, "/api/knowledge/items", body)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%v", recorder.Code, decoded)
	}
	input := fake.createInput
	if input.Title != "Retry contract" || input.Scope != knowledge.ScopeSystem ||
		input.SystemKey == nil || *input.SystemKey != "payments" ||
		input.SourceVersionID == nil || *input.SourceVersionID != 500 ||
		input.PromotedFromVersionID == nil || *input.PromotedFromVersionID != 501 ||
		len(input.Tags) != 2 {
		t.Fatalf("input = %+v", input)
	}
	if fake.createActor.ID != 77 {
		t.Fatalf("actor = %+v", fake.createActor)
	}
	view := decoded["data"].(map[string]any)
	if view["id"] != "9001" || view["status"] != "approved" || view["source_stale"] != true {
		t.Fatalf("view = %#v", view)
	}
}

func TestCreateItemRejectsMalformedRequests(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		field string
	}{
		{name: "invalid json", body: `{"title":`, field: "body"},
		{name: "source id", body: `{"title":"t","content":"c","source_version_id":"nope"}`, field: "source_version_id"},
		{name: "zero source id", body: `{"title":"t","content":"c","source_version_id":"0"}`, field: "source_version_id"},
		{name: "promoted id", body: `{"title":"t","content":"c","promoted_from_version_id":"-4"}`, field: "promoted_from_version_id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeService{}
			mux := newKnowledgeMux(fake, "user")
			recorder, body := perform(t, mux, http.MethodPost, "/api/knowledge/items", test.body)
			assertErrorCode(t, recorder.Code, body, http.StatusBadRequest, "invalid_argument")
			envelope := body["error"].(map[string]any)
			details := envelope["details"].(map[string]any)
			if details["field"] != test.field {
				t.Fatalf("field = %v, want %s", details["field"], test.field)
			}
			if fake.createCalls != 0 {
				t.Fatalf("service calls = %d", fake.createCalls)
			}
		})
	}
}

func TestCreateItemMapsServiceValidationError(t *testing.T) {
	fake := &fakeService{createErr: platform.Invalid("title", "title must be between 1 and 200 characters")}
	mux := newKnowledgeMux(fake, "user")
	recorder, body := perform(t, mux, http.MethodPost, "/api/knowledge/items", `{"title":"","content":"c"}`)
	assertErrorCode(t, recorder.Code, body, http.StatusBadRequest, "invalid_argument")
}

/* ---------------------------------- get ----------------------------------- */

func TestGetItemReturnsDetailWithRevisions(t *testing.T) {
	fake := &fakeService{getItem: fixtureItem(), revisions: []knowledge.Revision{fixtureRevision()}}
	mux := newKnowledgeMux(fake, "user")
	recorder, body := perform(t, mux, http.MethodGet, "/api/knowledge/items/9001", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%v", recorder.Code, body)
	}
	if fake.getID != 9001 || fake.revisionsID != 9001 {
		t.Fatalf("get id = %d revisions id = %d", fake.getID, fake.revisionsID)
	}
	view := body["data"].(map[string]any)
	if view["source_stale"] != true || view["review_reason"] != "needs a source version" ||
		view["reviewed_by"] != "88" {
		t.Fatalf("view = %#v", view)
	}
	revisions, ok := view["revisions"].([]any)
	if !ok || len(revisions) != 1 {
		t.Fatalf("revisions = %#v", view["revisions"])
	}
	revision := revisions[0].(map[string]any)
	if revision["id"] != "2" || revision["knowledge_id"] != "9001" || revision["revision"] != float64(2) {
		t.Fatalf("revision view = %#v", revision)
	}
}

func TestGetItemMapsNotFound(t *testing.T) {
	fake := &fakeService{getErr: platform.NotFound("knowledge_item")}
	mux := newKnowledgeMux(fake, "user")
	recorder, body := perform(t, mux, http.MethodGet, "/api/knowledge/items/404", "")
	assertErrorCode(t, recorder.Code, body, http.StatusNotFound, "not_found")
}

func TestGetItemRejectsInvalidID(t *testing.T) {
	fake := &fakeService{}
	mux := newKnowledgeMux(fake, "user")
	recorder, body := perform(t, mux, http.MethodGet, "/api/knowledge/items/not-an-id", "")
	assertErrorCode(t, recorder.Code, body, http.StatusBadRequest, "invalid_argument")
	if fake.getCalls != 0 {
		t.Fatalf("service calls = %d", fake.getCalls)
	}
}

/* --------------------------------- update --------------------------------- */

func TestUpdateItemParsesPartialBody(t *testing.T) {
	updated := fixtureItem()
	updated.Title = "New title"
	fake := &fakeService{updateItem: updated}
	mux := newKnowledgeMux(fake, "user")
	recorder, body := perform(t, mux, http.MethodPatch, "/api/knowledge/items/9001", `{"title":"New title"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%v", recorder.Code, body)
	}
	input := fake.updateInput
	if input.Title == nil || *input.Title != "New title" || input.Content != nil ||
		input.Tags != nil || input.Scope != nil || input.SystemKey != nil {
		t.Fatalf("input = %+v", input)
	}
	if fake.updateID != 9001 || fake.updateActor.ID != 77 {
		t.Fatalf("id = %d actor = %+v", fake.updateID, fake.updateActor)
	}
	if body["data"].(map[string]any)["title"] != "New title" {
		t.Fatalf("view = %#v", body["data"])
	}
}

func TestUpdateItemMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "not found", err: platform.NotFound("knowledge_item"), wantStatus: 404, wantCode: "not_found"},
		{name: "forbidden", err: platform.Forbidden(), wantStatus: 403, wantCode: "forbidden"},
		{name: "state conflict", err: func() error {
			conflict := platform.StateConflict("knowledge item state conflict")
			conflict.Details = map[string]any{"reason": "invalid_transition"}
			return conflict
		}(), wantStatus: 409, wantCode: "state_conflict"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeService{updateErr: test.err}
			mux := newKnowledgeMux(fake, "user")
			recorder, body := perform(t, mux, http.MethodPatch, "/api/knowledge/items/9001", `{"title":"New"}`)
			assertErrorCode(t, recorder.Code, body, test.wantStatus, test.wantCode)
		})
	}
}

func TestUpdateItemRejectsMalformedJSON(t *testing.T) {
	fake := &fakeService{}
	mux := newKnowledgeMux(fake, "user")
	recorder, body := perform(t, mux, http.MethodPatch, "/api/knowledge/items/9001", `{`)
	assertErrorCode(t, recorder.Code, body, http.StatusBadRequest, "invalid_argument")
	if fake.updateCalls != 0 {
		t.Fatalf("service calls = %d", fake.updateCalls)
	}
}

/* --------------------------------- review --------------------------------- */

func TestApproveItemIsAdminOnlyAndAcceptsEmptyBody(t *testing.T) {
	fake := &fakeService{reviewItem: fixtureItem()}
	mux := newKnowledgeMux(fake, "admin")
	recorder, body := perform(t, mux, http.MethodPost, "/api/knowledge/items/9001/approve", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%v", recorder.Code, body)
	}
	if fake.reviewAction != "approve" || fake.reviewID != 9001 || fake.reviewActor.ID != 88 {
		t.Fatalf("review = %s id=%d actor=%+v", fake.reviewAction, fake.reviewID, fake.reviewActor)
	}
	if body["data"].(map[string]any)["status"] != "approved" {
		t.Fatalf("view = %#v", body["data"])
	}
}

func TestRejectAndRevokeRequireReason(t *testing.T) {
	fake := &fakeService{reviewItem: fixtureItem()}
	mux := newKnowledgeMux(fake, "admin")
	for _, path := range []string{
		"/api/knowledge/items/9001/reject",
		"/api/knowledge/items/9001/revoke",
	} {
		recorder, body := perform(t, mux, http.MethodPost, path, `{}`)
		assertErrorCode(t, recorder.Code, body, http.StatusBadRequest, "invalid_argument")
		recorder, body = perform(t, mux, http.MethodPost, path,
			`{"reason":"`+strings.Repeat("x", 2001)+`"}`)
		assertErrorCode(t, recorder.Code, body, http.StatusBadRequest, "invalid_argument")
	}
	if fake.reviewCalls != 0 {
		t.Fatalf("review calls = %d", fake.reviewCalls)
	}

	recorder, body := perform(t, mux, http.MethodPost, "/api/knowledge/items/9001/reject", `{"reason":" duplicated "}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%v", recorder.Code, body)
	}
	if fake.reviewAction != "reject" || fake.reviewReason != " duplicated " {
		t.Fatalf("review = %s reason=%q", fake.reviewAction, fake.reviewReason)
	}
	recorder, body = perform(t, mux, http.MethodPost, "/api/knowledge/items/9001/revoke", `{"reason":"obsolete"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%v", recorder.Code, body)
	}
	if fake.reviewAction != "revoke" || fake.reviewReason != "obsolete" {
		t.Fatalf("review = %s reason=%q", fake.reviewAction, fake.reviewReason)
	}
}

func TestReviewMapsStateConflict(t *testing.T) {
	conflict := platform.StateConflict("knowledge item state conflict")
	conflict.Details = map[string]any{"reason": "invalid_transition"}
	fake := &fakeService{reviewErr: conflict}
	mux := newKnowledgeMux(fake, "admin")
	recorder, body := perform(t, mux, http.MethodPost, "/api/knowledge/items/9001/approve", "")
	assertErrorCode(t, recorder.Code, body, http.StatusConflict, "state_conflict")
	envelope := body["error"].(map[string]any)
	details := envelope["details"].(map[string]any)
	if details["reason"] != "invalid_transition" {
		t.Fatalf("details = %#v", details)
	}
}

/* -------------------------------- revisions ------------------------------- */

func TestListRevisionsReturnsHistory(t *testing.T) {
	fake := &fakeService{revisions: []knowledge.Revision{fixtureRevision()}}
	mux := newKnowledgeMux(fake, "user")
	recorder, body := perform(t, mux, http.MethodGet, "/api/knowledge/items/9001/revisions", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%v", recorder.Code, body)
	}
	if fake.revisionsID != 9001 {
		t.Fatalf("id = %d", fake.revisionsID)
	}
	items, ok := body["data"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("data = %#v", body["data"])
	}
	if items[0].(map[string]any)["title"] != "Retry contract v2" {
		t.Fatalf("revision = %#v", items[0])
	}
}

func TestListRevisionsMapsNotFound(t *testing.T) {
	fake := &fakeService{revisionsErr: platform.NotFound("knowledge_item")}
	mux := newKnowledgeMux(fake, "user")
	recorder, body := perform(t, mux, http.MethodGet, "/api/knowledge/items/404/revisions", "")
	assertErrorCode(t, recorder.Code, body, http.StatusNotFound, "not_found")
}

/* ------------------------------- wire format ------------------------------ */

func TestItemViewFormatsNullableFields(t *testing.T) {
	item := knowledge.Item{
		ID:        7,
		Scope:     knowledge.ScopeGlobal,
		Title:     "Title",
		Content:   "Body",
		Tags:      nil,
		Status:    knowledge.StatusDraft,
		Revision:  1,
		CreatedAt: handlerNow,
		UpdatedAt: handlerNow,
	}
	fake := &fakeService{createItem: item}
	mux := newKnowledgeMux(fake, "user")
	recorder, body := perform(t, mux, http.MethodPost, "/api/knowledge/items", `{"title":"Title","content":"Body"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%v", recorder.Code, body)
	}
	view := body["data"].(map[string]any)
	for _, field := range []string{"system_key", "confidence", "source_version_id", "promoted_from_version_id",
		"review_reason", "reviewed_by", "reviewed_at", "revoked_at", "created_by"} {
		if view[field] != nil {
			t.Fatalf("field %s = %v, want null", field, view[field])
		}
	}
	tags, ok := view["tags"].([]any)
	if !ok || len(tags) != 0 {
		t.Fatalf("tags = %#v, want []", view["tags"])
	}
	if view["id"] != platformid.Format(7) {
		t.Fatalf("id = %v", view["id"])
	}
}
