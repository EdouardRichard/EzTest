package knowledgeadapter

import (
	"context"
	"errors"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	knowledgeapp "github.com/EdouardRichard/EzTest/backend/internal/knowledge/application"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// TestKnowledgeRetrievalAdapterScope pins the knowledge.search/read adapter:
// the version's SUT families scope every read, a foreign-system or cross-version
// ID is denied exactly like a missing item, secrets never surface, and the
// search page is mapped onto the tool result.
func TestKnowledgeRetrievalAdapterScope(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryKnowledgeRepository()
	seedRetrievalItems(t, repo)
	service := knowledgeapp.NewService(
		repo, nil, nil, nil, nil, nil,
		fixedKnowledgeClock{}, &sequenceKnowledgeIDs{next: 8000},
	)
	adapter := New(service, &fakeVersionSystemReader{keys: []string{"auth"}})

	t.Run("search hides foreign systems, drafts and secrets", func(t *testing.T) {
		result, err := adapter.SearchKnowledge(ctx, tools.KnowledgeSearchFilters{VersionID: 10})
		if err != nil {
			t.Fatalf("SearchKnowledge: %v", err)
		}
		assertHitSet(t, result, []int64{7001, 7002, 7006})
		if result.Page != 1 || result.Limit != knowledgeapp.DefaultRetrievalLimit {
			// The knowledge application always fills page/limit; the adapter must
			// return them verbatim.
			t.Fatalf("page/limit = %d/%d", result.Page, result.Limit)
		}
		hit := findKnowledgeHit(t, result, 7001)
		if hit.Title != "统一登录约定" || hit.Kind != string(knowledgeapp.KindLoginRecipe) {
			t.Fatalf("hit = %#v", hit)
		}
		if hit.Snippet == "" {
			t.Fatalf("hit snippet is empty: %#v", hit)
		}
	})

	t.Run("search applies the structured page binding", func(t *testing.T) {
		pageKey := "/login"
		result, err := adapter.SearchKnowledge(ctx, tools.KnowledgeSearchFilters{
			VersionID: 10,
			PageKey:   &pageKey,
		})
		if err != nil {
			t.Fatalf("SearchKnowledge: %v", err)
		}
		if len(result.Hits) == 0 || result.Hits[0].ID != 7006 {
			t.Fatalf("strict page hit must rank first: %#v", result.Hits)
		}
		assertHitSet(t, result, []int64{7001, 7002, 7006})
	})

	t.Run("search free text recalls only matches", func(t *testing.T) {
		result, err := adapter.SearchKnowledge(ctx, tools.KnowledgeSearchFilters{
			VersionID: 10,
			Text:      "登录",
		})
		if err != nil {
			t.Fatalf("SearchKnowledge: %v", err)
		}
		assertHitSet(t, result, []int64{7001, 7002, 7006})
		if hit := findKnowledgeHit(t, result, 7001); hit.Score <= 0 {
			t.Fatalf("a text match must score above zero: %#v", hit)
		}
	})

	t.Run("a version without families sees only global knowledge", func(t *testing.T) {
		globalOnly := New(service, &fakeVersionSystemReader{keys: nil})
		result, err := globalOnly.SearchKnowledge(ctx, tools.KnowledgeSearchFilters{VersionID: 10})
		if err != nil {
			t.Fatalf("SearchKnowledge: %v", err)
		}
		assertHitSet(t, result, []int64{7001})
	})

	t.Run("read own-system and global knowledge", func(t *testing.T) {
		global, err := adapter.ReadKnowledge(ctx, tools.Scope{VersionID: 10}, 7001)
		if err != nil {
			t.Fatalf("ReadKnowledge(global): %v", err)
		}
		if global.Title != "统一登录约定" || global.Content != "复用 SSO 登录，不保存凭据。" {
			t.Fatalf("global detail = %#v", global)
		}
		if global.Kind != string(knowledgeapp.KindLoginRecipe) || global.Scope != string(knowledge.ScopeGlobal) {
			t.Fatalf("global kind/scope = %q/%q", global.Kind, global.Scope)
		}
		if global.Status != string(knowledge.StatusApproved) || global.Revision != 1 {
			t.Fatalf("global status/revision = %q/%d", global.Status, global.Revision)
		}
		if global.Bindings.SystemID != nil || global.Bindings.PageKey != nil {
			t.Fatalf("global bindings = %#v, want unset", global.Bindings)
		}

		ownSystem, err := adapter.ReadKnowledge(ctx, tools.Scope{VersionID: 10}, 7002)
		if err != nil {
			t.Fatalf("ReadKnowledge(own system): %v", err)
		}
		if ownSystem.Scope != string(knowledge.ScopeSystem) || ownSystem.ID != 7002 {
			t.Fatalf("own-system detail = %#v", ownSystem)
		}

		pageItem, err := adapter.ReadKnowledge(ctx, tools.Scope{VersionID: 10}, 7006)
		if err != nil {
			t.Fatalf("ReadKnowledge(page locator): %v", err)
		}
		if pageItem.Bindings.PageKey == nil || *pageItem.Bindings.PageKey != "/login" ||
			pageItem.Bindings.SystemID == nil || *pageItem.Bindings.SystemID != 7001 {
			t.Fatalf("page locator bindings = %#v", pageItem.Bindings)
		}
	})

	t.Run("read denies foreign-system and cross-version IDs", func(t *testing.T) {
		_, err := adapter.ReadKnowledge(ctx, tools.Scope{VersionID: 10}, 7003)
		requirePlatformError(t, err, 404, "not_found")
	})

	t.Run("read hides secrets and unverified lifecycle", func(t *testing.T) {
		_, err := adapter.ReadKnowledge(ctx, tools.Scope{VersionID: 10}, 7004)
		requirePlatformError(t, err, 404, "not_found")
		_, err = adapter.ReadKnowledge(ctx, tools.Scope{VersionID: 10}, 7005)
		requirePlatformError(t, err, 404, "not_found")
	})

	t.Run("a version without families cannot read system knowledge", func(t *testing.T) {
		globalOnly := New(service, &fakeVersionSystemReader{keys: nil})
		if _, err := globalOnly.ReadKnowledge(ctx, tools.Scope{VersionID: 10}, 7001); err != nil {
			t.Fatalf("global read: %v", err)
		}
		_, err := globalOnly.ReadKnowledge(ctx, tools.Scope{VersionID: 10}, 7002)
		requirePlatformError(t, err, 404, "not_found")
	})

	t.Run("version system resolution failures fail closed", func(t *testing.T) {
		broken := New(service, &fakeVersionSystemReader{err: errors.New("version read failed")})
		if _, err := broken.SearchKnowledge(ctx, tools.KnowledgeSearchFilters{VersionID: 10}); err == nil {
			t.Fatal("search swallowed the version reader failure")
		}
		if _, err := broken.ReadKnowledge(ctx, tools.Scope{VersionID: 10}, 7001); err == nil {
			t.Fatal("read swallowed the version reader failure")
		}
	})

	t.Run("invalid scope and missing service fail closed", func(t *testing.T) {
		_, err := adapter.SearchKnowledge(ctx, tools.KnowledgeSearchFilters{})
		requirePlatformError(t, err, 400, "invalid_argument")
		_, err = adapter.ReadKnowledge(ctx, tools.Scope{}, 7001)
		requirePlatformError(t, err, 400, "invalid_argument")
		_, err = adapter.ReadKnowledge(ctx, tools.Scope{VersionID: 10}, 0)
		requirePlatformError(t, err, 400, "invalid_argument")

		unwired := New(nil, &fakeVersionSystemReader{keys: []string{"auth"}})
		if _, err := unwired.SearchKnowledge(ctx, tools.KnowledgeSearchFilters{VersionID: 10}); err == nil {
			t.Fatal("search without the knowledge service must fail")
		}
		if _, err := unwired.ReadKnowledge(ctx, tools.Scope{VersionID: 10}, 7001); err == nil {
			t.Fatal("read without the knowledge service must fail")
		}
	})

	t.Run("search hit count is bounded by the requested page", func(t *testing.T) {
		result, err := adapter.SearchKnowledge(ctx, tools.KnowledgeSearchFilters{VersionID: 10, Limit: 1})
		if err != nil {
			t.Fatalf("SearchKnowledge: %v", err)
		}
		if len(result.Hits) != 1 || !result.HasMore {
			t.Fatalf("hits = %d has_more = %v, want a bounded first page", len(result.Hits), result.HasMore)
		}
	})
}

// TestKnowledgeCuratorProfileTools pins profile visibility: the seeded
// knowledge_curator profile exposes knowledge.search/knowledge.read, is mapped
// to the knowledge_curator role (so the runtime guard keeps the list closed),
// and the role's model-visible definitions include both tools.
func TestKnowledgeCuratorProfileTools(t *testing.T) {
	var allowedTools []string
	found := false
	for _, profile := range agentapp.Profiles() {
		if profile.Key != agentapp.ProfileKeyKnowledgeCurator {
			continue
		}
		found = true
		allowedTools = append([]string(nil), profile.AllowedTools...)
		if !profile.Enabled {
			t.Fatal("knowledge_curator seed is disabled")
		}
	}
	if !found {
		t.Fatalf("Profiles() has no %q seed", agentapp.ProfileKeyKnowledgeCurator)
	}

	role, ok := agentapp.RoleForProfile(agentapp.ProfileKeyKnowledgeCurator)
	if !ok || role.Key != agentapp.RoleKnowledgeCurator {
		t.Fatalf("RoleForProfile(knowledge_curator) = %q/%v, want knowledge_curator", role.Key, ok)
	}
	set, ok := agentapp.RoleToolSet(role.Key)
	if !ok {
		t.Fatalf("RoleToolSet(%q) is missing", role.Key)
	}

	required := []string{tools.ToolKnowledgeSearch, tools.ToolKnowledgeRead}
	for _, name := range required {
		if !hasToolName(allowedTools, name) {
			t.Fatalf("knowledge_curator profile is missing %q: %v", name, allowedTools)
		}
		if !set.Allows(name) {
			t.Fatalf("knowledge_curator role does not allow %q", name)
		}
	}

	// The seed surface must stay inside the role whitelist (the runner
	// intersects the two, and the profile must never widen the role).
	for _, name := range allowedTools {
		if !set.Allows(name) {
			t.Fatalf("profile tool %q is outside the knowledge_curator role whitelist", name)
		}
	}
	for _, forbidden := range []string{
		"browser.goto", "execution.case_finish", "case.confirm",
		tools.ToolCaseGraphPropose, tools.ToolDesignReviewSubmit,
	} {
		if hasToolName(allowedTools, forbidden) {
			t.Fatalf("knowledge_curator profile must not expose %q: %v", forbidden, allowedTools)
		}
	}

	// The tools are model-visible only when the role ToolSet can render their
	// definitions from the live registry (the registry is what the model sees).
	registry := tools.NewP7Registry(tools.Deps{})
	visible := set.Names(registry)
	for _, name := range required {
		if !hasToolName(visible, name) {
			t.Fatalf("role ToolSet does not expose %q from the registry: %v", name, visible)
		}
	}
	definitionNames := make([]string, 0)
	for _, definition := range set.Definitions(registry) {
		definitionNames = append(definitionNames, definition.Name)
	}
	for _, name := range required {
		if !hasToolName(definitionNames, name) {
			t.Fatalf("model tool definitions are missing %q: %v", name, definitionNames)
		}
	}
}

/* -------------------------------- helpers -------------------------------- */

func seedRetrievalItems(t *testing.T, repo *memoryKnowledgeRepository) {
	t.Helper()
	insertKnowledgeItem(t, repo, knowledge.Item{
		ID: 7001, Scope: knowledge.ScopeGlobal,
		Title: "统一登录约定", Content: "复用 SSO 登录，不保存凭据。",
		Tags: []string{"kind:login_recipe"}, Status: knowledge.StatusApproved,
	})
	insertKnowledgeItem(t, repo, knowledge.Item{
		ID: 7002, Scope: knowledge.ScopeSystem, SystemKey: stringPtr("auth"),
		Title: "登录页元素", Content: "用户名、密码输入框与提交按钮。",
		Tags: []string{"kind:page_locator"}, Status: knowledge.StatusApproved,
	})
	insertKnowledgeItem(t, repo, knowledge.Item{
		ID: 7003, Scope: knowledge.ScopeSystem, SystemKey: stringPtr("payment"),
		Title: "支付回调重试", Content: "回调必须幂等。",
		Tags: []string{"kind:api_auth"}, Status: knowledge.StatusApproved,
	})
	insertKnowledgeItem(t, repo, knowledge.Item{
		ID: 7004, Scope: knowledge.ScopeGlobal,
		Title: "凭据示例", Content: `{"recipe":{"password":"hunter2"}}`,
		Tags: []string{"kind:api_auth"}, Status: knowledge.StatusApproved,
	})
	insertKnowledgeItem(t, repo, knowledge.Item{
		ID: 7005, Scope: knowledge.ScopeGlobal,
		Title: "待审知识", Content: "草稿内容。",
		Tags: []string{"kind:login_recipe"}, Status: knowledge.StatusDraft,
	})
	insertKnowledgeItem(t, repo, knowledge.Item{
		ID: 7006, Scope: knowledge.ScopeSystem, SystemKey: stringPtr("auth"),
		Title: "登录页定位", Content: "登录页的账号和提交按钮定位。",
		Tags: []string{"kind:page_locator"}, Status: knowledge.StatusApproved,
		Bindings: knowledge.Bindings{
			SystemID: int64Pointer(7001), EnvironmentID: int64Pointer(7101), PageKey: stringPtr("/login"),
		},
	})
}

func assertHitSet(t *testing.T, result tools.KnowledgeSearchResult, want []int64) {
	t.Helper()
	got := make([]int64, 0, len(result.Hits))
	for _, hit := range result.Hits {
		got = append(got, hit.ID)
	}
	if len(got) != len(want) {
		t.Fatalf("hit IDs = %v, want %v", got, want)
	}
	seen := make(map[int64]struct{}, len(got))
	for _, id := range got {
		seen[id] = struct{}{}
	}
	for _, id := range want {
		if _, ok := seen[id]; !ok {
			t.Fatalf("hit IDs = %v, want %v", got, want)
		}
	}
}

func findKnowledgeHit(t *testing.T, result tools.KnowledgeSearchResult, id int64) tools.KnowledgeHit {
	t.Helper()
	for _, hit := range result.Hits {
		if hit.ID == id {
			return hit
		}
	}
	t.Fatalf("hit %d not in %#v", id, result.Hits)
	return tools.KnowledgeHit{}
}

func requirePlatformError(t *testing.T, err error, status int, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a %d/%s error", status, code)
	}
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *platform.Error", err)
	}
	if apiErr.Status != status || apiErr.Code != code {
		t.Fatalf("error = %#v, want status %d code %s", apiErr, status, code)
	}
}

func hasToolName(names []string, target string) bool {
	for _, name := range names {
		if name == target {
			return true
		}
	}
	return false
}

func int64Pointer(value int64) *int64 { return &value }
