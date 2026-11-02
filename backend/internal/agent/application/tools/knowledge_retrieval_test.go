package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

/* ---------------------------- retrieval fakes ----------------------------- */

// fakeKnowledgeRetrieval implements KnowledgeRetrievalPort.
type fakeKnowledgeRetrieval struct {
	searchCalls int
	filters     KnowledgeSearchFilters
	search      KnowledgeSearchResult
	searchErr   error

	readCalls  int
	readScope  Scope
	readID     int64
	readDetail KnowledgeDetail
	readErr    error
}

func (f *fakeKnowledgeRetrieval) SearchKnowledge(_ context.Context, filters KnowledgeSearchFilters) (KnowledgeSearchResult, error) {
	f.searchCalls++
	f.filters = filters
	if f.searchErr != nil {
		return KnowledgeSearchResult{}, f.searchErr
	}
	return f.search, nil
}

func (f *fakeKnowledgeRetrieval) ReadKnowledge(_ context.Context, scope Scope, id int64) (KnowledgeDetail, error) {
	f.readCalls++
	f.readScope = scope
	f.readID = id
	if f.readErr != nil {
		return KnowledgeDetail{}, f.readErr
	}
	return f.readDetail, nil
}

func executeKnowledgeTool(t *testing.T, port KnowledgeRetrievalPort, scope Scope, name, payload string) (any, error) {
	t.Helper()
	deps := Deps{P7: P7Deps{Retrieval: port}}
	registry := NewP7Registry(deps)
	return registry.Execute(context.Background(), scope, name, json.RawMessage(payload))
}

func knowledgeSearchItems(t *testing.T, result any) []map[string]any {
	t.Helper()
	decoded, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want object", result)
	}
	items, ok := decoded["items"].([]map[string]any)
	if !ok {
		t.Fatalf("items = %#v, want []map[string]any", decoded["items"])
	}
	return items
}

/* ------------------------------ knowledge.search -------------------------- */

func TestKnowledgeSearchTool(t *testing.T) {
	t.Run("filters are validated and forwarded with the run scope", func(t *testing.T) {
		port := &fakeKnowledgeRetrieval{search: KnowledgeSearchResult{
			Hits: []KnowledgeHit{{ID: 9001, Title: "登录配方", Kind: "login_recipe", Score: 1.25, Snippet: "打开登录页"}},
			Page: 2, Limit: 20, HasMore: true,
		}}
		result, err := executeKnowledgeTool(t, port, Scope{VersionID: 42, AgentRunID: 77}, ToolKnowledgeSearch,
			`{"system_id":"7","environment_id":"8","page_key":" /login ","api_key":"POST /api/login","account_role":"admin","text":" 登录 ","page":2,"limit":20}`)
		if err != nil {
			t.Fatalf("knowledge.search: %v", err)
		}
		filters := port.filters
		if filters.VersionID != 42 {
			t.Fatalf("version id = %d, want injected 42", filters.VersionID)
		}
		if filters.SystemID == nil || *filters.SystemID != 7 ||
			filters.EnvironmentID == nil || *filters.EnvironmentID != 8 {
			t.Fatalf("numeric bindings = %#v", filters)
		}
		if filters.PageKey == nil || *filters.PageKey != "/login" ||
			filters.APIKey == nil || *filters.APIKey != "POST /api/login" ||
			filters.AccountRole == nil || *filters.AccountRole != "admin" {
			t.Fatalf("string bindings = %#v", filters)
		}
		if filters.Text != "登录" || filters.Page != 2 || filters.Limit != 20 {
			t.Fatalf("filters = %#v, want trimmed text and page/limit", filters)
		}

		decoded, ok := result.(map[string]any)
		if !ok {
			t.Fatalf("result = %#v", result)
		}
		if decoded["page"] != 2 || decoded["limit"] != 20 || decoded["has_more"] != true {
			t.Fatalf("page/limit/has_more = %#v", decoded)
		}
		items := knowledgeSearchItems(t, result)
		if len(items) != 1 {
			t.Fatalf("items = %#v", items)
		}
		hit := items[0]
		if hit["id"] != "9001" || hit["title"] != "登录配方" || hit["kind"] != "login_recipe" ||
			hit["score"] != 1.25 || hit["snippet"] != "打开登录页" {
			t.Fatalf("hit = %#v", hit)
		}
		if _, ok := hit["content"]; ok {
			t.Fatal("knowledge.search must not return full content")
		}
	})

	t.Run("limit is clamped and defaults apply", func(t *testing.T) {
		port := &fakeKnowledgeRetrieval{}
		if _, err := executeKnowledgeTool(t, port, Scope{VersionID: 42}, ToolKnowledgeSearch, `{"limit":999}`); err != nil {
			t.Fatalf("knowledge.search: %v", err)
		}
		if port.filters.Limit != maxKnowledgeSearchLimit {
			t.Fatalf("limit = %d, want clamped %d", port.filters.Limit, maxKnowledgeSearchLimit)
		}
		if port.filters.Page != 1 {
			t.Fatalf("page = %d, want default 1", port.filters.Page)
		}

		port = &fakeKnowledgeRetrieval{}
		if _, err := executeKnowledgeTool(t, port, Scope{VersionID: 42}, ToolKnowledgeSearch, `{}`); err != nil {
			t.Fatalf("knowledge.search: %v", err)
		}
		if port.filters.Limit != defaultKnowledgeSearchLimit {
			t.Fatalf("limit = %d, want default %d", port.filters.Limit, defaultKnowledgeSearchLimit)
		}
	})

	t.Run("hostile text stays a structured filter value", func(t *testing.T) {
		port := &fakeKnowledgeRetrieval{}
		hostile := `'; DROP TABLE knowledge_item; --`
		if _, err := executeKnowledgeTool(t, port, Scope{VersionID: 42}, ToolKnowledgeSearch,
			fmt.Sprintf(`{"text":%q}`, hostile)); err != nil {
			t.Fatalf("knowledge.search: %v", err)
		}
		if port.filters.Text != hostile {
			t.Fatalf("text = %q, want the hostile value forwarded verbatim", port.filters.Text)
		}
	})

	t.Run("invalid arguments never reach the service", func(t *testing.T) {
		cases := []struct {
			name    string
			payload string
			field   string
		}{
			{name: "negative page", payload: `{"page":-1}`, field: "page"},
			{name: "negative limit", payload: `{"limit":-1}`, field: "limit"},
			{name: "zero system id", payload: `{"system_id":"0"}`, field: "system_id"},
			{name: "non numeric system id", payload: `{"system_id":"abc"}`, field: "system_id"},
			{name: "non numeric environment id", payload: `{"environment_id":"1; DROP"}`, field: "environment_id"},
			{name: "oversized text", payload: `{"text":"` + strings.Repeat("长", maxKnowledgeSearchTextRunes+1) + `"}`, field: "text"},
			{name: "unknown scope field", payload: `{"version_id":"9"}`, field: "parameters"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				port := &fakeKnowledgeRetrieval{}
				_, err := executeKnowledgeTool(t, port, Scope{VersionID: 42}, ToolKnowledgeSearch, tc.payload)
				var toolErr *Error
				if !errors.As(err, &toolErr) || toolErr.Code != CodeInvalidArgument {
					t.Fatalf("error = %v, want invalid_argument", err)
				}
				if toolErr.Details["field"] != tc.field {
					t.Fatalf("field = %v, want %q", toolErr.Details["field"], tc.field)
				}
				if port.searchCalls != 0 {
					t.Fatalf("service called %d times for invalid arguments", port.searchCalls)
				}
			})
		}
	})

	t.Run("missing version scope or service fails closed", func(t *testing.T) {
		port := &fakeKnowledgeRetrieval{}
		_, err := executeKnowledgeTool(t, port, Scope{}, ToolKnowledgeSearch, `{}`)
		var toolErr *Error
		if !errors.As(err, &toolErr) || toolErr.Code != CodeInvalidArgument || toolErr.Details["field"] != "scope" {
			t.Fatalf("missing version error = %v, want invalid_argument scope", err)
		}

		registry := NewP7Registry(Deps{})
		if _, err := registry.Execute(context.Background(), Scope{VersionID: 42}, ToolKnowledgeSearch, json.RawMessage(`{}`)); err == nil {
			t.Fatal("unwired retrieval service must fail as an internal configuration error")
		} else if errors.As(err, &toolErr) {
			t.Fatalf("unwired service must not be a model-visible tool error: %v", err)
		}
	})

	t.Run("service errors are mapped to typed tool errors", func(t *testing.T) {
		port := &fakeKnowledgeRetrieval{searchErr: platform.NotFound("knowledge_item")}
		_, err := executeKnowledgeTool(t, port, Scope{VersionID: 42}, ToolKnowledgeSearch, `{}`)
		var toolErr *Error
		if !errors.As(err, &toolErr) || toolErr.Code != CodeNotFound {
			t.Fatalf("error = %v, want not_found", err)
		}
	})
}

/* ------------------------------- knowledge.read --------------------------- */

func TestKnowledgeReadTool(t *testing.T) {
	verified := time.Date(2026, 11, 3, 6, 30, 0, 0, time.UTC)
	content := "打开登录页\n填写账号与密码\n提交并等待首页"
	port := &fakeKnowledgeRetrieval{readDetail: KnowledgeDetail{
		ID:             9001,
		Title:          "登录配方：管理员",
		Kind:           "login_recipe",
		Content:        content,
		Scope:          "system",
		Status:         "approved",
		Tags:           []string{"kind:login_recipe"},
		Revision:       3,
		LastVerifiedAt: &verified,
		Bindings: KnowledgeBindings{
			SystemID:      ptrInt64(7),
			EnvironmentID: ptrInt64(8),
			PageKey:       ptrString("/login"),
			AccountRole:   ptrString("admin"),
		},
	}}

	result, err := executeKnowledgeTool(t, port, Scope{VersionID: 42, AgentRunID: 77}, ToolKnowledgeRead, `{"id":"9001"}`)
	if err != nil {
		t.Fatalf("knowledge.read: %v", err)
	}
	if port.readID != 9001 {
		t.Fatalf("read id = %d, want 9001", port.readID)
	}
	if port.readScope.VersionID != 42 || port.readScope.AgentRunID != 77 {
		t.Fatalf("read scope = %#v, want the injected run scope", port.readScope)
	}
	decoded, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v", result)
	}
	if decoded["id"] != "9001" || decoded["title"] != "登录配方：管理员" || decoded["kind"] != "login_recipe" ||
		decoded["content"] != content || decoded["scope"] != "system" || decoded["status"] != "approved" {
		t.Fatalf("read result = %#v", decoded)
	}
	if decoded["revision"] != 3 {
		t.Fatalf("revision = %v, want 3", decoded["revision"])
	}
	if decoded["last_verified_at"] != "2026-11-03T06:30:00Z" {
		t.Fatalf("last_verified_at = %v", decoded["last_verified_at"])
	}
	tags, ok := decoded["tags"].([]string)
	if !ok || len(tags) != 1 || tags[0] != "kind:login_recipe" {
		t.Fatalf("tags = %#v", decoded["tags"])
	}
	bindings, ok := decoded["bindings"].(map[string]any)
	if !ok {
		t.Fatalf("bindings = %#v", decoded["bindings"])
	}
	if bindings["system_id"] != "7" || bindings["environment_id"] != "8" ||
		bindings["page_key"] != "/login" || bindings["account_role"] != "admin" {
		t.Fatalf("bindings = %#v", bindings)
	}

	t.Run("invalid ids never reach the service", func(t *testing.T) {
		for _, payload := range []string{`{}`, `{"id":""}`, `{"id":"0"}`, `{"id":"1; DROP TABLE knowledge_item"}`, `{"id":"-5"}`, `{"id":9001}`} {
			port := &fakeKnowledgeRetrieval{}
			_, err := executeKnowledgeTool(t, port, Scope{VersionID: 42}, ToolKnowledgeRead, payload)
			var toolErr *Error
			if !errors.As(err, &toolErr) || toolErr.Code != CodeInvalidArgument {
				t.Fatalf("payload %s: error = %v, want invalid_argument", payload, err)
			}
			if port.readCalls != 0 {
				t.Fatalf("payload %s: service called %d times", payload, port.readCalls)
			}
		}
	})

	t.Run("missing version scope or service fails closed", func(t *testing.T) {
		port := &fakeKnowledgeRetrieval{}
		_, err := executeKnowledgeTool(t, port, Scope{}, ToolKnowledgeRead, `{"id":"9001"}`)
		var toolErr *Error
		if !errors.As(err, &toolErr) || toolErr.Code != CodeInvalidArgument || toolErr.Details["field"] != "scope" {
			t.Fatalf("missing version error = %v, want invalid_argument scope", err)
		}

		registry := NewP7Registry(Deps{})
		if _, err := registry.Execute(context.Background(), Scope{VersionID: 42}, ToolKnowledgeRead, json.RawMessage(`{"id":"9001"}`)); err == nil {
			t.Fatal("unwired retrieval service must fail as an internal configuration error")
		} else if errors.As(err, &toolErr) {
			t.Fatalf("unwired service must not be a model-visible tool error: %v", err)
		}
	})

	t.Run("not found and internal failures stay typed", func(t *testing.T) {
		notFound := &fakeKnowledgeRetrieval{readErr: platform.NotFound("knowledge_item")}
		_, err := executeKnowledgeTool(t, notFound, Scope{VersionID: 42}, ToolKnowledgeRead, `{"id":"9001"}`)
		var toolErr *Error
		if !errors.As(err, &toolErr) || toolErr.Code != CodeNotFound {
			t.Fatalf("not found error = %v, want not_found", err)
		}

		internal := &fakeKnowledgeRetrieval{readErr: errors.New("repository exploded")}
		_, err = executeKnowledgeTool(t, internal, Scope{VersionID: 42}, ToolKnowledgeRead, `{"id":"9001"}`)
		if err == nil {
			t.Fatal("internal failure was swallowed")
		}
		if errors.As(err, &toolErr) {
			t.Fatalf("internal failure must not be model-visible: %v", err)
		}
	})
}

func ptrInt64(value int64) *int64    { return &value }
func ptrString(value string) *string { return &value }
