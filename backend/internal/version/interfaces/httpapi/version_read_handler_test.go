package versionapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

// fakeVersionReadModel is the version-context read service double of the
// version-scoped fact/plan-item endpoints. It records every scoped call so the
// tests can assert version isolation, filter forwarding and pagination.
type fakeVersionReadModel struct {
	exists    bool
	existsErr error
	existsIDs []int64

	facts      []versionapp.FactReadView
	factsMore  bool
	factsErr   error
	factCalls  int
	factScopes []int64
	factQuery  []versionapp.FactReadFilter

	planItems   []versionapp.PlanItemReadView
	planMore    bool
	planErr     error
	planCalls   int
	planScopes  []int64
	planQueries []versionapp.PlanItemReadFilter
}

func (f *fakeVersionReadModel) VersionExists(_ context.Context, versionID int64) (bool, error) {
	f.existsIDs = append(f.existsIDs, versionID)
	return f.exists, f.existsErr
}

func (f *fakeVersionReadModel) Facts(_ context.Context, versionID int64, filter versionapp.FactReadFilter) ([]versionapp.FactReadView, bool, error) {
	f.factCalls++
	f.factScopes = append(f.factScopes, versionID)
	f.factQuery = append(f.factQuery, filter)
	return f.facts, f.factsMore, f.factsErr
}

func (f *fakeVersionReadModel) PlanItems(_ context.Context, versionID int64, filter versionapp.PlanItemReadFilter) ([]versionapp.PlanItemReadView, bool, error) {
	f.planCalls++
	f.planScopes = append(f.planScopes, versionID)
	f.planQueries = append(f.planQueries, filter)
	return f.planItems, f.planMore, f.planErr
}

var _ versionapi.VersionReadService = (*fakeVersionReadModel)(nil)

// fakeWorkItemReadModel is the agent-context work-item read service double.
type fakeWorkItemReadModel struct {
	items     []agentapp.WorkItemReadView
	more      bool
	listErr   error
	listCalls int
	filters   []agentapp.WorkItemFilter

	getView   agentapp.WorkItemReadView
	getErr    error
	getCalls  int
	getScopes [][2]int64
}

func (f *fakeWorkItemReadModel) WorkItems(_ context.Context, filter agentapp.WorkItemFilter) ([]agentapp.WorkItemReadView, bool, error) {
	f.listCalls++
	f.filters = append(f.filters, filter)
	return f.items, f.more, f.listErr
}

func (f *fakeWorkItemReadModel) WorkItem(_ context.Context, versionID, workItemID int64) (agentapp.WorkItemReadView, error) {
	f.getCalls++
	f.getScopes = append(f.getScopes, [2]int64{versionID, workItemID})
	if f.getErr != nil {
		return agentapp.WorkItemReadView{}, f.getErr
	}
	return f.getView, nil
}

var _ versionapi.WorkItemReadService = (*fakeWorkItemReadModel)(nil)

func sampleFactReadViews() []versionapp.FactReadView {
	confidence := 0.75
	superseded := int64(102)
	return []versionapp.FactReadView{
		{
			ID:          101,
			VersionID:   7,
			Kind:        "page",
			Key:         "page:env:1:/login",
			Payload:     map[string]any{"page_key": "login"},
			Confidence:  &confidence,
			Source:      "explore",
			SourceRef:   "agent_run:5",
			EvidenceIDs: []int64{11, 12},
			ContentHash: strings.Repeat("a", 64),
			CreatedAt:   testTime,
		},
		{
			ID:           102,
			VersionID:    7,
			Kind:         "gap",
			Key:          "gap:1",
			Payload:      map[string]any{"dimension": "权限"},
			Source:       "explore",
			EvidenceIDs:  []int64{},
			ContentHash:  strings.Repeat("b", 64),
			SupersededBy: &superseded,
			CreatedAt:    testTime,
		},
	}
}

func samplePlanItemReadViews() []versionapp.PlanItemReadView {
	workItemID := int64(301)
	return []versionapp.PlanItemReadView{
		{
			ID:         201,
			VersionID:  7,
			Phase:      "clarify",
			Title:      "需求澄清",
			Status:     "pending",
			Order:      1,
			DependsOn:  []int64{199},
			WorkItemID: &workItemID,
			Decision:   "keep",
			CreatedAt:  testTime,
			UpdatedAt:  testTime,
		},
		{
			ID:        202,
			VersionID: 7,
			Phase:     "design",
			Title:     "用例设计",
			Status:    "done",
			Order:     2,
			DependsOn: []int64{},
			Decision:  "",
			CreatedAt: testTime,
			UpdatedAt: testTime,
		},
	}
}

func sampleWorkItemReadViews() []agentapp.WorkItemReadView {
	expires := testTime.Add(5 * time.Minute)
	return []agentapp.WorkItemReadView{
		{
			ID:             401,
			VersionID:      7,
			Kind:           "execute.batch",
			Goal:           "执行批次",
			State:          "running",
			Target:         json.RawMessage(`{"case_ids":["11"]}`),
			Inputs:         json.RawMessage(`{}`),
			Expected:       json.RawMessage(`{}`),
			Budget:         json.RawMessage(`{"max_tokens":1000}`),
			DependsOn:      []int64{399},
			Lease:          &agentapp.WorkItemLeaseView{Owner: "worker-1", ExpiresAt: &expires},
			Attempt:        2,
			Result:         json.RawMessage(`{"ref":"agent_run:9"}`),
			CreatedBy:      "state_machine",
			IdempotencyKey: nil,
			CreatedAt:      testTime,
			UpdatedAt:      testTime,
		},
		{
			ID:        402,
			VersionID: 7,
			Kind:      "design.cases",
			Goal:      "设计用例",
			State:     "cancelled",
			CreatedBy: "planner",
			CreatedAt: testTime,
			UpdatedAt: testTime,
		},
	}
}

func newVersionReadServer(t *testing.T) (http.Handler, *fakeVersionReadModel, *fakeWorkItemReadModel) {
	t.Helper()
	versions := &fakeVersionReadModel{exists: true}
	workItems := &fakeWorkItemReadModel{}
	handler := versionapi.NewVersionReadHandler(versions, workItems)
	mux := http.NewServeMux()
	versionapi.RegisterVersionReadRoutes(mux, allowVersionReadAuth, handler)
	return mux, versions, workItems
}

func allowVersionReadAuth(next http.Handler) http.Handler { return next }

func denyVersionReadAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, platform.Unauthenticated())
	})
}

func TestVersionFactsRead(t *testing.T) {
	t.Run("current facts are scoped, paginated and projected", func(t *testing.T) {
		mux, versions, _ := newVersionReadServer(t)
		versions.facts = sampleFactReadViews()
		versions.factsMore = true

		recorder := call(t, mux, http.MethodGet,
			"/api/versions/7/facts?kind=%20page%20&include_superseded=true&limit=5&cursor=200", "", nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
		}
		if len(versions.factScopes) != 1 || versions.factScopes[0] != 7 {
			t.Fatalf("fact scopes = %v, want [7]", versions.factScopes)
		}
		filter := versions.factQuery[0]
		if filter.Kind != "page" || !filter.IncludeSuperseded {
			t.Fatalf("filter = %#v, want kind=page include_superseded=true", filter)
		}
		if filter.Limit != 5 {
			t.Fatalf("limit = %d, want 5", filter.Limit)
		}
		if filter.BeforeID == nil || *filter.BeforeID != 200 {
			t.Fatalf("before = %v, want 200", filter.BeforeID)
		}

		payload := decodeBody(t, recorder)
		if payload["has_more"] != true {
			t.Fatalf("has_more = %v, want true", payload["has_more"])
		}
		if payload["next_cursor"] != "102" {
			t.Fatalf("next_cursor = %v, want 102", payload["next_cursor"])
		}
		items := dataList(t, recorder)
		if len(items) != 2 {
			t.Fatalf("items = %d, want 2", len(items))
		}
		first := items[0].(map[string]any)
		if first["id"] != "101" || first["version_id"] != "7" || first["kind"] != "page" {
			t.Fatalf("first fact = %#v", first)
		}
		if first["key"] != "page:env:1:/login" || first["source"] != "explore" || first["source_ref"] != "agent_run:5" {
			t.Fatalf("first fact identity = %#v", first)
		}
		if first["confidence"] != 0.75 {
			t.Fatalf("confidence = %v, want 0.75", first["confidence"])
		}
		if first["content_hash"] != strings.Repeat("a", 64) {
			t.Fatalf("content_hash = %v", first["content_hash"])
		}
		if first["created_at"] != "2026-10-31T12:00:00Z" {
			t.Fatalf("created_at = %v", first["created_at"])
		}
		if first["superseded_by"] != nil {
			t.Fatalf("superseded_by = %v, want null", first["superseded_by"])
		}
		evidence := first["evidence_refs"].([]any)
		if len(evidence) != 2 || evidence[0] != "11" || evidence[1] != "12" {
			t.Fatalf("evidence_refs = %#v", evidence)
		}
		payloadView := first["payload"].(map[string]any)
		if payloadView["page_key"] != "login" {
			t.Fatalf("payload = %#v", payloadView)
		}
		second := items[1].(map[string]any)
		if second["superseded_by"] != "102" {
			t.Fatalf("second superseded_by = %v, want 102", second["superseded_by"])
		}
	})

	t.Run("defaults are current facts, default limit, first page", func(t *testing.T) {
		mux, versions, _ := newVersionReadServer(t)
		versions.facts = []versionapp.FactReadView{}

		recorder := call(t, mux, http.MethodGet, "/api/versions/7/facts", "", nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
		}
		filter := versions.factQuery[0]
		if filter.IncludeSuperseded || filter.Limit != 20 || filter.BeforeID != nil || filter.Kind != "" {
			t.Fatalf("default filter = %#v", filter)
		}
		items := dataList(t, recorder)
		if len(items) != 0 {
			t.Fatalf("empty facts = %#v", items)
		}
		if payload := decodeBody(t, recorder); payload["next_cursor"] != "" || payload["has_more"] != false {
			t.Fatalf("empty envelope = %#v", payload)
		}
	})

	t.Run("invalid include_superseded is a typed 400", func(t *testing.T) {
		mux, versions, _ := newVersionReadServer(t)
		recorder := call(t, mux, http.MethodGet, "/api/versions/7/facts?include_superseded=maybe", "", nil)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
		}
		if errorBody(t, recorder)["code"] != "invalid_argument" {
			t.Fatalf("error = %#v", errorBody(t, recorder))
		}
		if len(versions.factQuery) != 0 {
			t.Fatalf("fact read called for an invalid request")
		}
	})

	t.Run("invalid cursor is a typed 400", func(t *testing.T) {
		mux, _, _ := newVersionReadServer(t)
		recorder := call(t, mux, http.MethodGet, "/api/versions/7/facts?cursor=abc", "", nil)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
		}
		if errorBody(t, recorder)["code"] != "invalid_argument" {
			t.Fatalf("error = %#v", errorBody(t, recorder))
		}
	})

	t.Run("missing version is a 404 and never reads facts", func(t *testing.T) {
		mux, versions, _ := newVersionReadServer(t)
		versions.exists = false
		recorder := call(t, mux, http.MethodGet, "/api/versions/7/facts", "", nil)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: %s", recorder.Code, recorder.Body.String())
		}
		if errorBody(t, recorder)["code"] != "not_found" {
			t.Fatalf("error = %#v", errorBody(t, recorder))
		}
		if len(versions.factQuery) != 0 {
			t.Fatalf("facts read for a missing version")
		}
	})
}

func TestVersionPlanItemsRead(t *testing.T) {
	t.Run("plan items are scoped, filtered and projected", func(t *testing.T) {
		mux, versions, _ := newVersionReadServer(t)
		versions.planItems = samplePlanItemReadViews()
		versions.planMore = true

		recorder := call(t, mux, http.MethodGet,
			"/api/versions/7/plan-items?status=%20pending%20&limit=2&cursor=300", "", nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
		}
		if len(versions.planScopes) != 1 || versions.planScopes[0] != 7 {
			t.Fatalf("plan scopes = %v, want [7]", versions.planScopes)
		}
		filter := versions.planQueries[0]
		if filter.Status != "pending" {
			t.Fatalf("status filter = %q, want pending", filter.Status)
		}
		if filter.Limit != 2 {
			t.Fatalf("limit = %d, want 2", filter.Limit)
		}
		if filter.BeforeID == nil || *filter.BeforeID != 300 {
			t.Fatalf("before = %v, want 300", filter.BeforeID)
		}

		payload := decodeBody(t, recorder)
		if payload["has_more"] != true || payload["next_cursor"] != "202" {
			t.Fatalf("envelope = %#v", payload)
		}
		items := dataList(t, recorder)
		if len(items) != 2 {
			t.Fatalf("items = %d, want 2", len(items))
		}
		first := items[0].(map[string]any)
		if first["id"] != "201" || first["version_id"] != "7" || first["phase"] != "clarify" ||
			first["title"] != "需求澄清" || first["status"] != "pending" ||
			first["order"] != float64(1) || first["decision"] != "keep" ||
			first["work_item_id"] != "301" {
			t.Fatalf("first plan item = %#v", first)
		}
		if first["created_at"] != "2026-10-31T12:00:00Z" || first["updated_at"] != "2026-10-31T12:00:00Z" {
			t.Fatalf("timestamps = %#v", first)
		}
		dependencies := first["depends_on"].([]any)
		if len(dependencies) != 1 || dependencies[0] != "199" {
			t.Fatalf("depends_on = %#v", dependencies)
		}
		second := items[1].(map[string]any)
		if second["work_item_id"] != nil {
			t.Fatalf("second work_item_id = %v, want null", second["work_item_id"])
		}
		if empty, ok := second["depends_on"].([]any); !ok || len(empty) != 0 {
			t.Fatalf("second depends_on = %#v, want []", second["depends_on"])
		}
	})

	t.Run("empty plan is an empty array envelope", func(t *testing.T) {
		mux, versions, _ := newVersionReadServer(t)
		versions.planItems = []versionapp.PlanItemReadView{}

		recorder := call(t, mux, http.MethodGet, "/api/versions/7/plan-items", "", nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
		}
		payload := decodeBody(t, recorder)
		if payload["has_more"] != false || payload["next_cursor"] != "" {
			t.Fatalf("envelope = %#v", payload)
		}
		data, ok := payload["data"].([]any)
		if !ok || len(data) != 0 {
			t.Fatalf("data = %#v, want []", payload["data"])
		}
		if filter := versions.planQueries[0]; filter.Limit != 20 || filter.BeforeID != nil || filter.Status != "" {
			t.Fatalf("default filter = %#v", filter)
		}
	})

	t.Run("missing version is a 404", func(t *testing.T) {
		mux, versions, _ := newVersionReadServer(t)
		versions.exists = false
		recorder := call(t, mux, http.MethodGet, "/api/versions/7/plan-items", "", nil)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: %s", recorder.Code, recorder.Body.String())
		}
		if len(versions.planQueries) != 0 {
			t.Fatalf("plan read for a missing version")
		}
	})

	t.Run("invalid cursor is a typed 400", func(t *testing.T) {
		mux, _, _ := newVersionReadServer(t)
		recorder := call(t, mux, http.MethodGet, "/api/versions/7/plan-items?cursor=-3", "", nil)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
		}
	})
}

func TestVersionWorkItemsRead(t *testing.T) {
	t.Run("work items are scoped, filtered and projected", func(t *testing.T) {
		mux, versions, workItems := newVersionReadServer(t)
		workItems.items = sampleWorkItemReadViews()
		workItems.more = true

		recorder := call(t, mux, http.MethodGet,
			"/api/versions/7/work-items?kind=%20execute.batch%20&state=cancelled&created_by=state_machine&limit=10&cursor=500",
			"", nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
		}
		if len(versions.existsIDs) != 1 || versions.existsIDs[0] != 7 {
			t.Fatalf("version existence calls = %v, want [7]", versions.existsIDs)
		}
		if len(workItems.filters) != 1 {
			t.Fatalf("work item calls = %d, want 1", len(workItems.filters))
		}
		filter := workItems.filters[0]
		if filter.VersionID != 7 || filter.Kind != "execute.batch" || filter.State != "cancelled" ||
			filter.CreatedBy != "state_machine" || filter.Limit != 10 ||
			filter.BeforeID == nil || *filter.BeforeID != 500 {
			t.Fatalf("filter = %#v", filter)
		}

		payload := decodeBody(t, recorder)
		if payload["has_more"] != true || payload["next_cursor"] != "402" {
			t.Fatalf("envelope = %#v", payload)
		}
		items := dataList(t, recorder)
		if len(items) != 2 {
			t.Fatalf("items = %d, want 2", len(items))
		}
		first := items[0].(map[string]any)
		if first["id"] != "401" || first["version_id"] != "7" || first["kind"] != "execute.batch" ||
			first["goal"] != "执行批次" || first["state"] != "running" ||
			first["attempt"] != float64(2) || first["created_by"] != "state_machine" {
			t.Fatalf("first work item = %#v", first)
		}
		if first["created_at"] != "2026-10-31T12:00:00Z" || first["updated_at"] != "2026-10-31T12:00:00Z" {
			t.Fatalf("timestamps = %#v", first)
		}
		target := first["target"].(map[string]any)
		if cases := target["case_ids"].([]any); len(cases) != 1 || cases[0] != "11" {
			t.Fatalf("target = %#v", target)
		}
		budget := first["budget"].(map[string]any)
		if budget["max_tokens"] != float64(1000) {
			t.Fatalf("budget = %#v", budget)
		}
		dependencies := first["depends_on"].([]any)
		if len(dependencies) != 1 || dependencies[0] != "399" {
			t.Fatalf("depends_on = %#v", dependencies)
		}
		lease := first["lease"].(map[string]any)
		if lease["owner"] != "worker-1" || lease["expires_at"] != "2026-10-31T12:05:00Z" {
			t.Fatalf("lease = %#v", lease)
		}
		if first["idempotency_key"] != nil {
			t.Fatalf("idempotency_key = %v, want null", first["idempotency_key"])
		}
		result := first["result"].(map[string]any)
		if result["ref"] != "agent_run:9" {
			t.Fatalf("result = %#v", result)
		}
		second := items[1].(map[string]any)
		if second["state"] != "cancelled" {
			t.Fatalf("second state = %v, want cancelled", second["state"])
		}
		if second["depends_on"] != nil {
			t.Fatalf("second depends_on = %#v, want null", second["depends_on"])
		}
		if second["lease"] != nil {
			t.Fatalf("second lease = %#v, want null", second["lease"])
		}
		for _, objectField := range []string{"target", "inputs", "expected", "budget", "result"} {
			if _, ok := second[objectField].(map[string]any); !ok {
				t.Fatalf("second %s = %#v, want a JSON object", objectField, second[objectField])
			}
		}
	})

	t.Run("empty work item list is an empty array envelope", func(t *testing.T) {
		mux, _, workItems := newVersionReadServer(t)
		workItems.items = []agentapp.WorkItemReadView{}

		recorder := call(t, mux, http.MethodGet, "/api/versions/7/work-items", "", nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
		}
		payload := decodeBody(t, recorder)
		if payload["has_more"] != false || payload["next_cursor"] != "" {
			t.Fatalf("envelope = %#v", payload)
		}
		if data, ok := payload["data"].([]any); !ok || len(data) != 0 {
			t.Fatalf("data = %#v, want []", payload["data"])
		}
		if filter := workItems.filters[0]; filter.Limit != 20 || filter.BeforeID != nil {
			t.Fatalf("default filter = %#v", filter)
		}
	})

	t.Run("invalid work item filters are typed 400s", func(t *testing.T) {
		cases := []string{
			"/api/versions/7/work-items?kind=unknown.kind",
			"/api/versions/7/work-items?state=dead",
			"/api/versions/7/work-items?created_by=robot",
			"/api/versions/7/work-items?cursor=abc",
		}
		for _, path := range cases {
			mux, _, workItems := newVersionReadServer(t)
			recorder := call(t, mux, http.MethodGet, path, "", nil)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("GET %s status = %d, want 400: %s", path, recorder.Code, recorder.Body.String())
			}
			if errorBody(t, recorder)["code"] != "invalid_argument" {
				t.Fatalf("GET %s error = %#v", path, errorBody(t, recorder))
			}
			if len(workItems.filters) != 0 {
				t.Fatalf("GET %s reached the read service", path)
			}
		}
	})

	t.Run("missing version is a 404", func(t *testing.T) {
		mux, versions, workItems := newVersionReadServer(t)
		versions.exists = false
		recorder := call(t, mux, http.MethodGet, "/api/versions/7/work-items", "", nil)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: %s", recorder.Code, recorder.Body.String())
		}
		if len(workItems.filters) != 0 {
			t.Fatalf("work-item read for a missing version")
		}
	})

	t.Run("single work item is version scoped", func(t *testing.T) {
		mux, versions, workItems := newVersionReadServer(t)
		workItems.getView = sampleWorkItemReadViews()[0]

		recorder := call(t, mux, http.MethodGet, "/api/versions/7/work-items/401", "", nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
		}
		if len(versions.existsIDs) != 1 || versions.existsIDs[0] != 7 {
			t.Fatalf("version existence calls = %v, want [7]", versions.existsIDs)
		}
		if len(workItems.getScopes) != 1 || workItems.getScopes[0] != [2]int64{7, 401} {
			t.Fatalf("get scopes = %v, want [7 401]", workItems.getScopes)
		}
		data := dataBody(t, recorder)
		if data["id"] != "401" || data["version_id"] != "7" || data["state"] != "running" {
			t.Fatalf("single work item = %#v", data)
		}
	})

	t.Run("foreign or missing work item is a 404", func(t *testing.T) {
		mux, _, workItems := newVersionReadServer(t)
		workItems.getErr = platform.NotFound("work item")

		recorder := call(t, mux, http.MethodGet, "/api/versions/7/work-items/999", "", nil)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: %s", recorder.Code, recorder.Body.String())
		}
		if errorBody(t, recorder)["code"] != "not_found" {
			t.Fatalf("error = %#v", errorBody(t, recorder))
		}
		if len(workItems.getScopes) != 1 || workItems.getScopes[0] != [2]int64{7, 999} {
			t.Fatalf("get scopes = %v, want [7 999]", workItems.getScopes)
		}
	})

	t.Run("invalid work item id is a typed 400", func(t *testing.T) {
		mux, _, workItems := newVersionReadServer(t)
		recorder := call(t, mux, http.MethodGet, "/api/versions/7/work-items/not-a-number", "", nil)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
		}
		if len(workItems.getScopes) != 0 {
			t.Fatalf("work-item read for an invalid id")
		}
	})
}

func TestVersionReadServiceErrors(t *testing.T) {
	boom := errors.New("boom")
	paths := []string{
		"/api/versions/7/facts",
		"/api/versions/7/plan-items",
		"/api/versions/7/work-items",
		"/api/versions/7/work-items/401",
	}
	assertInternal := func(t *testing.T, path string, recorder *httptest.ResponseRecorder) {
		t.Helper()
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("GET %s status = %d, want 500: %s", path, recorder.Code, recorder.Body.String())
		}
		if errorBody(t, recorder)["code"] != "internal" {
			t.Fatalf("GET %s error = %#v, want internal", path, errorBody(t, recorder))
		}
		if payload := decodeBody(t, recorder); payload["data"] != nil {
			t.Fatalf("GET %s leaked data on error: %#v", path, payload["data"])
		}
	}

	t.Run("version existence probe error fails every route closed", func(t *testing.T) {
		for _, path := range paths {
			mux, versions, _ := newVersionReadServer(t)
			versions.existsErr = boom
			assertInternal(t, path, call(t, mux, http.MethodGet, path, "", nil))
		}
	})

	t.Run("fact read error is a 500", func(t *testing.T) {
		mux, versions, _ := newVersionReadServer(t)
		versions.factsErr = boom
		assertInternal(t, "/api/versions/7/facts", call(t, mux, http.MethodGet, "/api/versions/7/facts", "", nil))
	})

	t.Run("plan-item read error is a 500", func(t *testing.T) {
		mux, versions, _ := newVersionReadServer(t)
		versions.planErr = boom
		assertInternal(t, "/api/versions/7/plan-items", call(t, mux, http.MethodGet, "/api/versions/7/plan-items", "", nil))
	})

	t.Run("work-item list error is a 500", func(t *testing.T) {
		mux, _, workItems := newVersionReadServer(t)
		workItems.listErr = boom
		assertInternal(t, "/api/versions/7/work-items", call(t, mux, http.MethodGet, "/api/versions/7/work-items", "", nil))
	})

	t.Run("work-item get error is a 500", func(t *testing.T) {
		mux, _, workItems := newVersionReadServer(t)
		workItems.getErr = boom
		assertInternal(t, "/api/versions/7/work-items/401", call(t, mux, http.MethodGet, "/api/versions/7/work-items/401", "", nil))
	})
}

func TestVersionReadRoutesMounted(t *testing.T) {
	paths := []string{
		"/api/versions/7/facts",
		"/api/versions/7/plan-items",
		"/api/versions/7/work-items",
		"/api/versions/7/work-items/401",
	}

	t.Run("routes sit behind the authentication middleware", func(t *testing.T) {
		versions := &fakeVersionReadModel{exists: true}
		workItems := &fakeWorkItemReadModel{getView: sampleWorkItemReadViews()[0]}
		mux := http.NewServeMux()
		versionapi.RegisterVersionReadRoutes(mux, denyVersionReadAuth,
			versionapi.NewVersionReadHandler(versions, workItems))

		for _, path := range paths {
			recorder := call(t, mux, http.MethodGet, path, "", nil)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("GET %s status = %d, want 401 (authenticated route): %s",
					path, recorder.Code, recorder.Body.String())
			}
		}
		if len(versions.existsIDs) != 0 || len(workItems.filters) != 0 || len(workItems.getScopes) != 0 {
			t.Fatalf("an unauthenticated request reached the read services")
		}
	})

	t.Run("mounted routes reach the read services", func(t *testing.T) {
		mux, versions, workItems := newVersionReadServer(t)
		versions.facts = sampleFactReadViews()
		versions.planItems = samplePlanItemReadViews()
		workItems.items = sampleWorkItemReadViews()
		workItems.getView = sampleWorkItemReadViews()[0]

		for _, path := range paths {
			recorder := call(t, mux, http.MethodGet, path, "", nil)
			if recorder.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200: %s", path, recorder.Code, recorder.Body.String())
			}
		}
	})

	t.Run("a nil handler leaves the routes unmounted", func(t *testing.T) {
		mux := http.NewServeMux()
		versionapi.RegisterVersionReadRoutes(mux, allowVersionReadAuth, nil)
		for _, path := range paths {
			recorder := call(t, mux, http.MethodGet, path, "", nil)
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("GET %s status = %d, want 404 (unmounted)", path, recorder.Code)
			}
		}
	})

	t.Run("a handler without services fails closed", func(t *testing.T) {
		mux := http.NewServeMux()
		versionapi.RegisterVersionReadRoutes(mux, allowVersionReadAuth,
			versionapi.NewVersionReadHandler(nil, nil))
		for _, path := range paths {
			recorder := call(t, mux, http.MethodGet, path, "", nil)
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("GET %s status = %d, want 503: %s", path, recorder.Code, recorder.Body.String())
			}
		}
	})
}
