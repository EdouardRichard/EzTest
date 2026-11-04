package knowledgeapp_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	knowledgeapp "github.com/EdouardRichard/EzTest/backend/internal/knowledge/application"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

/* ---------------------------- retrieval fixtures --------------------------- */

// retrievalRepo overrides ListForInjection so the strict binding pass and the
// generic fallback pass can be fed different candidate sets. It embeds the
// shared fakeRepo (same test package) for Get/Insert/Update.
type retrievalRepo struct {
	*fakeRepo
	strict  []knowledge.Item
	generic []knowledge.Item
	calls   []knowledge.InjectionQuery
	listErr error
}

func newRetrievalRepo(strict, generic []knowledge.Item) *retrievalRepo {
	return &retrievalRepo{
		fakeRepo: &fakeRepo{items: map[int64]knowledge.Item{}},
		strict:   strict,
		generic:  generic,
	}
}

func (r *retrievalRepo) ListForInjection(_ context.Context, query knowledge.InjectionQuery) ([]knowledge.Item, error) {
	r.calls = append(r.calls, query)
	if r.listErr != nil {
		return nil, r.listErr
	}
	if !query.Bindings.IsZero() {
		return append([]knowledge.Item(nil), r.strict...), nil
	}
	return append([]knowledge.Item(nil), r.generic...), nil
}

func (r *retrievalRepo) track(items ...knowledge.Item) *retrievalRepo {
	for _, item := range items {
		r.fakeRepo.items[item.ID] = item
	}
	return r
}

func newRetrievalService(repo knowledge.Repository) *knowledgeapp.Service {
	return knowledgeapp.NewService(repo, nil, nil, nil, nil, nil, nil, nil)
}

func retrievalItem(id int64, status knowledge.Status, scope knowledge.Scope, systemKey string, bindings knowledge.Bindings, title, content string, tags ...string) knowledge.Item {
	item := knowledge.Item{
		ID:       id,
		Scope:    scope,
		Title:    title,
		Content:  content,
		Tags:     tags,
		Status:   status,
		Bindings: bindings,
		Revision: 1,
	}
	if systemKey != "" {
		item.SystemKey = ptr(systemKey)
	}
	return item
}

func hitIDs(page knowledgeapp.RetrievalPage) []int64 {
	ids := make([]int64, 0, len(page.Items))
	for _, hit := range page.Items {
		ids = append(ids, hit.ID)
	}
	return ids
}

func containsID(page knowledgeapp.RetrievalPage, id int64) bool {
	for _, hit := range page.Items {
		if hit.ID == id {
			return true
		}
	}
	return false
}

/* --------------------------------- search --------------------------------- */

// TestKnowledgeSearchBindings pins the two-pass binding policy (spec §10.2):
// the strict structure query is unchanged JSONB containment and the generic
// fallback only adds compatible items, never conflicts, and never outranks a
// strict hit.
func TestKnowledgeSearchBindings(t *testing.T) {
	t.Run("page and role strict hits outrank the generic fallback", func(t *testing.T) {
		strictItem := retrievalItem(101, knowledge.StatusApproved, knowledge.ScopeSystem, "shop",
			knowledge.Bindings{PageKey: ptr("/login"), AccountRole: ptr("admin")},
			"登录配方：管理员", "打开 /login，使用管理员账号登录。", "kind:login_recipe")
		conflictingPage := retrievalItem(104, knowledge.StatusApproved, knowledge.ScopeSystem, "shop",
			knowledge.Bindings{PageKey: ptr("/checkout"), AccountRole: ptr("admin")},
			"登录结账页", "结账页登录入口。", "kind:page_locator")
		conflictingRole := retrievalItem(102, knowledge.StatusApproved, knowledge.ScopeSystem, "shop",
			knowledge.Bindings{PageKey: ptr("/login"), AccountRole: ptr("tester")},
			"登录配方：测试员", "测试员登录步骤。", "kind:login_recipe")
		genericItem := retrievalItem(103, knowledge.StatusApproved, knowledge.ScopeGlobal, "",
			knowledge.Bindings{}, "登录通用说明", "所有角色可用的登录说明。", "kind:login_recipe")
		otherSystem := retrievalItem(105, knowledge.StatusApproved, knowledge.ScopeSystem, "crm",
			knowledge.Bindings{}, "登录配方：其他系统", "其他系统的登录步骤。", "kind:login_recipe")
		unapproved := retrievalItem(106, knowledge.StatusSuspect, knowledge.ScopeGlobal, "",
			knowledge.Bindings{}, "登录配方：可疑", "失败的登录配方。", "kind:login_recipe")

		repo := newRetrievalRepo([]knowledge.Item{strictItem, conflictingPage},
			[]knowledge.Item{strictItem, conflictingPage, conflictingRole, genericItem, otherSystem, unapproved})
		service := newRetrievalService(repo)

		page, err := service.Retrieve(context.Background(), knowledgeapp.Query{
			VersionID:   1,
			SystemKeys:  []string{"shop"},
			PageKey:     ptr("/login"),
			AccountRole: ptr("admin"),
			Text:        " 登录 ",
			Limit:       10,
		})
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}

		if len(repo.calls) != 2 {
			t.Fatalf("repository calls = %d, want 2 (strict + generic)", len(repo.calls))
		}
		strictQuery := repo.calls[0]
		if strictQuery.Bindings.PageKey == nil || *strictQuery.Bindings.PageKey != "/login" ||
			strictQuery.Bindings.AccountRole == nil || *strictQuery.Bindings.AccountRole != "admin" {
			t.Fatalf("strict query bindings = %#v, want page_key/account_role containment", strictQuery.Bindings)
		}
		if strictQuery.Query != "登录" {
			t.Fatalf("strict query text = %q, want trimmed 登录", strictQuery.Query)
		}
		if strings.Join(strictQuery.SystemKeys, ",") != "shop" {
			t.Fatalf("strict query system keys = %v", strictQuery.SystemKeys)
		}
		if !repo.calls[1].Bindings.IsZero() {
			t.Fatalf("generic fallback bindings = %#v, want none", repo.calls[1].Bindings)
		}

		if got := strings.Join(int64Strings(hitIDs(page)), ","); got != "101,103" {
			t.Fatalf("hit ids = %s, want 101,103 (strict first, compatible generic fallback)", got)
		}
		first, second := page.Items[0], page.Items[1]
		if first.Title != "登录配方：管理员" || first.Kind != "login_recipe" {
			t.Fatalf("strict hit = %#v", first)
		}
		if first.Score < 1.0 {
			t.Fatalf("strict hit score = %v, want a binding bonus >= 1.0", first.Score)
		}
		if second.Score >= 1.0 || second.Score <= 0 {
			t.Fatalf("generic hit score = %v, want (0,1)", second.Score)
		}
		if first.Score <= second.Score {
			t.Fatalf("strict score %v must outrank generic score %v", first.Score, second.Score)
		}
		if first.Snippet == "" || utf8.RuneCountInString(first.Snippet) > knowledge.SnippetMaxRunes+1 {
			t.Fatalf("snippet = %q, want a bounded excerpt", first.Snippet)
		}
		for _, id := range []int64{102, 104, 105, 106} {
			if containsID(page, id) {
				t.Fatalf("hit %d must be filtered out; hits = %v", id, hitIDs(page))
			}
		}
	})

	t.Run("system environment and API bindings hit", func(t *testing.T) {
		apiItem := retrievalItem(201, knowledge.StatusApproved, knowledge.ScopeSystem, "shop",
			knowledge.Bindings{
				SystemID:      ptr[int64](7),
				EnvironmentID: ptr[int64](8),
				APIKey:        ptr("POST /api/login"),
			},
			"API 鉴权：登录", "调用 POST /api/login，使用令牌模板。", "kind:api_auth")
		otherAPI := retrievalItem(202, knowledge.StatusApproved, knowledge.ScopeSystem, "shop",
			knowledge.Bindings{APIKey: ptr("GET /api/orders")},
			"API 鉴权：订单", "订单接口鉴权。", "kind:api_auth")

		repo := newRetrievalRepo([]knowledge.Item{apiItem}, []knowledge.Item{apiItem, otherAPI})
		service := newRetrievalService(repo)

		page, err := service.Retrieve(context.Background(), knowledgeapp.Query{
			VersionID:     2,
			SystemKeys:    []string{"shop"},
			SystemID:      ptr[int64](7),
			EnvironmentID: ptr[int64](8),
			APIKey:        ptr("POST /api/login"),
			Text:          "鉴权",
			Limit:         5,
		})
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		if got := int64Strings(hitIDs(page)); len(got) != 1 || got[0] != "201" {
			t.Fatalf("hit ids = %v, want [201]", got)
		}
		strictQuery := repo.calls[0]
		if strictQuery.Bindings.SystemID == nil || *strictQuery.Bindings.SystemID != 7 ||
			strictQuery.Bindings.EnvironmentID == nil || *strictQuery.Bindings.EnvironmentID != 8 ||
			strictQuery.Bindings.APIKey == nil || *strictQuery.Bindings.APIKey != "POST /api/login" {
			t.Fatalf("strict query bindings = %#v", strictQuery.Bindings)
		}
	})

	t.Run("no binding filter makes one repository pass", func(t *testing.T) {
		item := retrievalItem(301, knowledge.StatusApproved, knowledge.ScopeGlobal, "",
			knowledge.Bindings{}, "登录说明", "登录步骤。", "kind:login_recipe")
		repo := newRetrievalRepo(nil, []knowledge.Item{item})
		service := newRetrievalService(repo)

		page, err := service.Retrieve(context.Background(), knowledgeapp.Query{
			VersionID: 3,
			Text:      "登录",
		})
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		if len(repo.calls) != 1 || !repo.calls[0].Bindings.IsZero() {
			t.Fatalf("repository calls = %d, want one unfiltered pass", len(repo.calls))
		}
		if !containsID(page, 301) {
			t.Fatalf("hits = %v, want 301", hitIDs(page))
		}
		if page.Limit != knowledgeapp.DefaultRetrievalLimit {
			t.Fatalf("limit = %d, want default %d", page.Limit, knowledgeapp.DefaultRetrievalLimit)
		}
	})

	t.Run("structured browse without text keeps compatible generic rows", func(t *testing.T) {
		strictItem := retrievalItem(401, knowledge.StatusApproved, knowledge.ScopeGlobal, "",
			knowledge.Bindings{PageKey: ptr("/login")}, "页面定位：登录页", "登录页元素。", "kind:page_locator")
		compatible := retrievalItem(402, knowledge.StatusApproved, knowledge.ScopeGlobal, "",
			knowledge.Bindings{AccountRole: ptr("admin")}, "登录通用规范", "管理员登录规范。", "kind:login_recipe")
		conflicting := retrievalItem(403, knowledge.StatusApproved, knowledge.ScopeGlobal, "",
			knowledge.Bindings{PageKey: ptr("/checkout")}, "页面定位：结账页", "结账页元素。", "kind:page_locator")

		repo := newRetrievalRepo([]knowledge.Item{strictItem}, []knowledge.Item{strictItem, compatible, conflicting})
		service := newRetrievalService(repo)

		page, err := service.Retrieve(context.Background(), knowledgeapp.Query{
			VersionID: 4,
			PageKey:   ptr("/login"),
			Limit:     10,
		})
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		if !containsID(page, 401) || !containsID(page, 402) {
			t.Fatalf("hits = %v, want strict 401 plus compatible generic 402", hitIDs(page))
		}
		if containsID(page, 403) {
			t.Fatalf("conflicting page binding leaked into hits: %v", hitIDs(page))
		}
		if page.Items[0].ID != 401 {
			t.Fatalf("strict hit is not first: %v", hitIDs(page))
		}
	})
}

// TestKnowledgeSearchApprovedOnly proves the application re-checks status,
// scope visibility and secret hygiene even when the adapter over-returns, and
// that knowledge.read is bounded to the review lifecycle (stale/suspect remain
// readable per Task 33).
func TestKnowledgeSearchApprovedOnly(t *testing.T) {
	approved := retrievalItem(501, knowledge.StatusApproved, knowledge.ScopeGlobal, "",
		knowledge.Bindings{}, "登录配方：管理员", "打开登录页。", "kind:login_recipe")
	draft := retrievalItem(502, knowledge.StatusDraft, knowledge.ScopeGlobal, "",
		knowledge.Bindings{}, "草稿登录配方", "未审核的登录步骤。", "kind:login_recipe")
	rejected := retrievalItem(503, knowledge.StatusRejected, knowledge.ScopeGlobal, "",
		knowledge.Bindings{}, "已拒绝登录配方", "被拒绝的登录步骤。", "kind:login_recipe")
	revoked := retrievalItem(504, knowledge.StatusRevoked, knowledge.ScopeGlobal, "",
		knowledge.Bindings{}, "已撤销登录配方", "被撤销的登录步骤。", "kind:login_recipe")
	suspect := retrievalItem(505, knowledge.StatusSuspect, knowledge.ScopeGlobal, "",
		knowledge.Bindings{}, "可疑登录配方", "复用失败的登录步骤。", "kind:login_recipe")
	stale := retrievalItem(506, knowledge.StatusStale, knowledge.ScopeGlobal, "",
		knowledge.Bindings{}, "失效登录配方", "页面变更后的登录步骤。", "kind:login_recipe")
	secret := retrievalItem(507, knowledge.StatusApproved, knowledge.ScopeGlobal, "",
		knowledge.Bindings{}, "登录配方：含凭据", `{"password":"hunter2"}`, "kind:login_recipe")
	otherSystem := retrievalItem(508, knowledge.StatusApproved, knowledge.ScopeSystem, "crm",
		knowledge.Bindings{}, "登录配方：其他系统", "其他系统的登录步骤。", "kind:login_recipe")

	all := []knowledge.Item{approved, draft, rejected, revoked, suspect, stale, secret, otherSystem}
	repo := newRetrievalRepo(nil, all)
	repo.track(all...)
	service := newRetrievalService(repo)

	page, err := service.Retrieve(context.Background(), knowledgeapp.Query{
		VersionID:  5,
		SystemKeys: []string{"shop"},
		Limit:      50,
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if got := int64Strings(hitIDs(page)); len(got) != 1 || got[0] != "501" {
		t.Fatalf("hit ids = %v, want only approved visible item 501", got)
	}

	readable := []int64{501, 505, 506}
	for _, id := range readable {
		item, err := service.Read(context.Background(), id)
		if err != nil {
			t.Fatalf("Read(%d): %v", id, err)
		}
		if item.ID != id {
			t.Fatalf("Read(%d) returned id %d", id, item.ID)
		}
	}
	for _, id := range []int64{502, 503, 504, 507} {
		if _, err := service.Read(context.Background(), id); !isNotFound(err) {
			t.Fatalf("Read(%d) error = %v, want not_found", id, err)
		}
	}
	if _, err := service.Read(context.Background(), 9999); !isNotFound(err) {
		t.Fatalf("Read(missing) error = %v, want not_found", err)
	}
	if _, err := service.Read(context.Background(), 0); !isInvalid(err, "id") {
		t.Fatalf("Read(0) error = %v, want invalid id", err)
	}
}

// TestKnowledgeSearchBounded covers page/limit clamping, snippet bounds,
// argument validation and parameter-only text handling (a hostile query is
// data, never SQL).
func TestKnowledgeSearchBounded(t *testing.T) {
	makeItems := func(count int) []knowledge.Item {
		items := make([]knowledge.Item, 0, count)
		for i := 0; i < count; i++ {
			items = append(items, retrievalItem(int64(6000+i), knowledge.StatusApproved, knowledge.ScopeGlobal, "",
				knowledge.Bindings{}, "登录规范 "+fmt.Sprintf("%02d", i), "登录步骤与注意事项。", "kind:login_recipe"))
		}
		return items
	}

	t.Run("limit is clamped and paged", func(t *testing.T) {
		items := makeItems(60)
		repo := newRetrievalRepo(nil, items)
		service := newRetrievalService(repo)

		page, err := service.Retrieve(context.Background(), knowledgeapp.Query{
			VersionID: 6,
			Text:      "登录",
			Page:      1,
			Limit:     999,
		})
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		if page.Limit != knowledgeapp.MaxRetrievalLimit {
			t.Fatalf("limit = %d, want clamped %d", page.Limit, knowledgeapp.MaxRetrievalLimit)
		}
		if len(page.Items) != knowledgeapp.MaxRetrievalLimit {
			t.Fatalf("items = %d, want %d", len(page.Items), knowledgeapp.MaxRetrievalLimit)
		}
		if !page.HasMore {
			t.Fatal("has_more = false, want true with 60 candidates")
		}

		second, err := service.Retrieve(context.Background(), knowledgeapp.Query{
			VersionID: 6,
			Text:      "登录",
			Page:      2,
			Limit:     10,
		})
		if err != nil {
			t.Fatalf("Retrieve page 2: %v", err)
		}
		if len(second.Items) != 10 {
			t.Fatalf("page 2 items = %d, want 10", len(second.Items))
		}
		if second.Items[0].ID >= page.Items[0].ID {
			// Both pages are ordered by score/id: the first page holds the
			// newest ids (same text score), so page 2 starts below page 1.
			t.Fatalf("page 2 first id %d is not below page 1 first id %d", second.Items[0].ID, page.Items[0].ID)
		}

		far, err := service.Retrieve(context.Background(), knowledgeapp.Query{
			VersionID: 6,
			Text:      "登录",
			Page:      1000,
			Limit:     10,
		})
		if err != nil {
			t.Fatalf("Retrieve far page: %v", err)
		}
		if len(far.Items) != 0 || far.HasMore {
			t.Fatalf("far page = %d items has_more=%v, want empty", len(far.Items), far.HasMore)
		}
	})

	t.Run("defaults and invalid arguments", func(t *testing.T) {
		item := retrievalItem(7001, knowledge.StatusApproved, knowledge.ScopeGlobal, "",
			knowledge.Bindings{}, "登录说明", "登录步骤。", "kind:login_recipe")
		repo := newRetrievalRepo(nil, []knowledge.Item{item})
		service := newRetrievalService(repo)

		page, err := service.Retrieve(context.Background(), knowledgeapp.Query{VersionID: 7, Text: "登录"})
		if err != nil {
			t.Fatalf("Retrieve defaults: %v", err)
		}
		if page.Page != 1 || page.Limit != knowledgeapp.DefaultRetrievalLimit {
			t.Fatalf("defaults = page %d limit %d", page.Page, page.Limit)
		}

		cases := []struct {
			name  string
			query knowledgeapp.Query
			field string
		}{
			{name: "missing version", query: knowledgeapp.Query{}, field: "version_id"},
			{name: "negative page", query: knowledgeapp.Query{VersionID: 7, Page: -1}, field: "page"},
			{name: "negative limit", query: knowledgeapp.Query{VersionID: 7, Limit: -1}, field: "limit"},
			{name: "oversized text", query: knowledgeapp.Query{VersionID: 7, Text: strings.Repeat("长", knowledgeapp.MaxRetrievalTextRunes+1)}, field: "text"},
			{name: "oversized page key", query: knowledgeapp.Query{VersionID: 7, PageKey: ptr(strings.Repeat("a", knowledge.MaxBindingKeyRunes+1))}, field: "bindings.page_key"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if _, err := service.Retrieve(context.Background(), tc.query); !isInvalid(err, tc.field) {
					t.Fatalf("error = %v, want invalid field %q", err, tc.field)
				}
			})
		}
	})

	t.Run("snippet is bounded", func(t *testing.T) {
		long := strings.Repeat("长", knowledge.SnippetMaxRunes+400)
		item := retrievalItem(7101, knowledge.StatusApproved, knowledge.ScopeGlobal, "",
			knowledge.Bindings{}, "超长登录说明", long, "kind:login_recipe")
		repo := newRetrievalRepo(nil, []knowledge.Item{item})
		service := newRetrievalService(repo)

		page, err := service.Retrieve(context.Background(), knowledgeapp.Query{VersionID: 8, Text: "长"})
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("hits = %v", hitIDs(page))
		}
		if length := utf8.RuneCountInString(page.Items[0].Snippet); length > knowledge.SnippetMaxRunes+1 {
			t.Fatalf("snippet runes = %d, want bounded at %d", length, knowledge.SnippetMaxRunes+1)
		}
	})

	t.Run("hostile text is data and ranking is deterministic", func(t *testing.T) {
		hostile := `'; DROP TABLE knowledge_item; --`
		item := retrievalItem(7201, knowledge.StatusApproved, knowledge.ScopeGlobal, "",
			knowledge.Bindings{}, "注入文本", hostile, "kind:login_recipe")
		repo := newRetrievalRepo(nil, []knowledge.Item{item})
		service := newRetrievalService(repo)

		first, err := service.Retrieve(context.Background(), knowledgeapp.Query{VersionID: 9, Text: hostile})
		if err != nil {
			t.Fatalf("Retrieve hostile text: %v", err)
		}
		second, err := service.Retrieve(context.Background(), knowledgeapp.Query{VersionID: 9, Text: hostile})
		if err != nil {
			t.Fatalf("Retrieve hostile text again: %v", err)
		}
		if len(repo.calls) != 2 {
			t.Fatalf("repository calls = %d", len(repo.calls))
		}
		for _, call := range repo.calls {
			if call.Query != hostile {
				t.Fatalf("query = %q, want the hostile text forwarded as data", call.Query)
			}
		}
		if len(first.Items) != 1 || first.Items[0].ID != 7201 {
			t.Fatalf("hits = %v", hitIDs(first))
		}
		if len(second.Items) != 1 || second.Items[0].Score != first.Items[0].Score {
			t.Fatalf("scores are not deterministic: %v vs %v", first.Items, second.Items)
		}
	})
}

// TestKnowledgeChineseTrigram pins the character-trigram recall that replaces
// the 'simple' full-text match for Chinese queries (spec §10.2).
func TestKnowledgeChineseTrigram(t *testing.T) {
	login := retrievalItem(801, knowledge.StatusApproved, knowledge.ScopeGlobal, "",
		knowledge.Bindings{}, "登录配方：管理员", "打开 /login 并填写账号。", "kind:login_recipe")
	payment := retrievalItem(802, knowledge.StatusApproved, knowledge.ScopeGlobal, "",
		knowledge.Bindings{}, "支付回调幂等", "回调必须幂等重试。", "kind:api_auth")
	contentOnly := retrievalItem(803, knowledge.StatusApproved, knowledge.ScopeGlobal, "",
		knowledge.Bindings{}, "通用操作说明", "先打开登录页再填写用户名。", "kind:login_recipe")
	all := []knowledge.Item{login, payment, contentOnly}
	service := newRetrievalService(newRetrievalRepo(nil, all))

	loginPage, err := service.Retrieve(context.Background(), knowledgeapp.Query{VersionID: 10, Text: "登录", Limit: 10})
	if err != nil {
		t.Fatalf("Retrieve 登录: %v", err)
	}
	if !containsID(loginPage, 801) || !containsID(loginPage, 803) {
		t.Fatalf("登录 hits = %v, want title 801 and content 803", hitIDs(loginPage))
	}
	if containsID(loginPage, 802) {
		t.Fatalf("登录 hits include the unrelated payment item: %v", hitIDs(loginPage))
	}
	if loginPage.Items[0].ID != 801 {
		t.Fatalf("登录 ranking = %v, want title hit 801 first", hitIDs(loginPage))
	}

	paymentPage, err := service.Retrieve(context.Background(), knowledgeapp.Query{VersionID: 10, Text: "支付回调", Limit: 10})
	if err != nil {
		t.Fatalf("Retrieve 支付回调: %v", err)
	}
	if len(paymentPage.Items) != 1 || paymentPage.Items[0].ID != 802 {
		t.Fatalf("支付回调 hits = %v, want [802]", hitIDs(paymentPage))
	}

	// A near-miss Chinese query still recalls the item (trigram, not exact FTS).
	typoPage, err := service.Retrieve(context.Background(), knowledgeapp.Query{VersionID: 10, Text: "登陆", Limit: 10})
	if err != nil {
		t.Fatalf("Retrieve 登陆: %v", err)
	}
	if !containsID(typoPage, 801) || typoPage.Items[0].Score <= 0 {
		t.Fatalf("登陆 hits = %#v, want fuzzy recall of 801", typoPage.Items)
	}
}

// topKRetrievalRepo adds the optional trigram top-K capability on top of the
// ts_rank injection window fake, so Retrieve's preference and fallback can be
// pinned without a database.
type topKRetrievalRepo struct {
	*retrievalRepo
	topKItems []knowledge.Item
	topKQuery knowledge.InjectionQuery
	topKCalls int
	topKErr   error
}

func (r *topKRetrievalRepo) SearchTopK(_ context.Context, query knowledge.InjectionQuery) ([]knowledge.Item, error) {
	r.topKCalls++
	r.topKQuery = query
	if r.topKErr != nil {
		return nil, r.topKErr
	}
	return append([]knowledge.Item(nil), r.topKItems...), nil
}

// TestKnowledgeSearchTrgmTopK pins the repository-stage contract of spec §10.2:
// when the repository offers the trigram top-K stage, Retrieve must use it for
// a text query instead of the bounded ts_rank window; an empty trigram result
// falls back to that window so short/lexically disjoint queries still recall.
func TestKnowledgeSearchTrgmTopK(t *testing.T) {
	ctx := context.Background()

	t.Run("trigram top-K candidates replace the ts_rank window", func(t *testing.T) {
		target := retrievalItem(1201, knowledge.StatusApproved, knowledge.ScopeGlobal, "",
			knowledge.Bindings{}, "登录页元素", "登录页元素定位。", "kind:page_locator")
		windowItem := retrievalItem(1202, knowledge.StatusApproved, knowledge.ScopeGlobal, "",
			knowledge.Bindings{}, "登录说明", "登录步骤说明。", "kind:login_recipe")
		repo := &topKRetrievalRepo{
			retrievalRepo: newRetrievalRepo(nil, []knowledge.Item{windowItem}),
			topKItems:     []knowledge.Item{target},
		}
		service := newRetrievalService(repo)

		page, err := service.Retrieve(ctx, knowledgeapp.Query{VersionID: 12, Text: "登陆", Limit: 10})
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		if repo.topKCalls != 1 {
			t.Fatalf("top-K calls = %d, want 1", repo.topKCalls)
		}
		if len(repo.calls) != 0 {
			t.Fatalf("injection-window calls = %d, want 0 when the top-K stage has candidates", len(repo.calls))
		}
		if repo.topKQuery.Query != "登陆" || repo.topKQuery.Limit != 200 {
			t.Fatalf("top-K query = %#v, want the text and the bounded candidate limit", repo.topKQuery)
		}
		if !containsID(page, 1201) || containsID(page, 1202) {
			t.Fatalf("hits = %v, want only the top-K candidate 1201", hitIDs(page))
		}
	})

	t.Run("empty top-K falls back to the ts_rank window", func(t *testing.T) {
		windowItem := retrievalItem(1211, knowledge.StatusApproved, knowledge.ScopeGlobal, "",
			knowledge.Bindings{}, "登录说明", "登录步骤说明。", "kind:login_recipe")
		repo := &topKRetrievalRepo{
			retrievalRepo: newRetrievalRepo(nil, []knowledge.Item{windowItem}),
		}
		service := newRetrievalService(repo)

		page, err := service.Retrieve(ctx, knowledgeapp.Query{VersionID: 13, Text: "登录", Limit: 10})
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		if repo.topKCalls != 1 || len(repo.calls) != 1 {
			t.Fatalf("top-K/injection calls = %d/%d, want 1/1 fallback", repo.topKCalls, len(repo.calls))
		}
		if !containsID(page, 1211) {
			t.Fatalf("hits = %v, want the injection-window fallback 1211", hitIDs(page))
		}
	})

	t.Run("structured browse skips the trigram stage", func(t *testing.T) {
		item := retrievalItem(1221, knowledge.StatusApproved, knowledge.ScopeGlobal, "",
			knowledge.Bindings{PageKey: ptr("/login")}, "登录页元素", "登录页元素定位。", "kind:page_locator")
		repo := &topKRetrievalRepo{
			retrievalRepo: newRetrievalRepo(nil, []knowledge.Item{item}),
		}
		service := newRetrievalService(repo)

		page, err := service.Retrieve(ctx, knowledgeapp.Query{
			VersionID: 14,
			PageKey:   ptr("/login"),
			Limit:     10,
		})
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		if repo.topKCalls != 0 {
			t.Fatalf("top-K calls = %d, want 0 for a structured-only query", repo.topKCalls)
		}
		if !containsID(page, 1221) {
			t.Fatalf("hits = %v, want the structured window item 1221", hitIDs(page))
		}
	})

	t.Run("a failing top-K stage is not hidden", func(t *testing.T) {
		repo := &topKRetrievalRepo{
			retrievalRepo: newRetrievalRepo(nil, nil),
			topKErr:       errors.New("trigram unavailable"),
		}
		service := newRetrievalService(repo)

		if _, err := service.Retrieve(ctx, knowledgeapp.Query{VersionID: 15, Text: "登录"}); err == nil {
			t.Fatal("Retrieve error = nil, want the top-K failure propagated")
		}
	})
}

// TestKnowledgeReadFullContent proves knowledge.read returns the full item body
// while knowledge.search only exposes the bounded snippet.
func TestKnowledgeReadFullContent(t *testing.T) {
	longContent := strings.Repeat("登录步骤。", 2000)
	item := retrievalItem(901, knowledge.StatusApproved, knowledge.ScopeSystem, "shop",
		knowledge.Bindings{PageKey: ptr("/login"), AccountRole: ptr("admin")},
		"登录配方：管理员", longContent, "kind:login_recipe")
	repo := newRetrievalRepo(nil, []knowledge.Item{item})
	repo.track(item)
	service := newRetrievalService(repo)

	page, err := service.Retrieve(context.Background(), knowledgeapp.Query{
		VersionID:  11,
		SystemKeys: []string{"shop"},
		Text:       "登录",
		Limit:      5,
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Snippet == longContent {
		t.Fatalf("search must return a bounded snippet, got %#v", page.Items)
	}

	full, err := service.Read(context.Background(), 901)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if full.Content != longContent {
		t.Fatalf("Read content was truncated: %d runes, want %d", utf8.RuneCountInString(full.Content), utf8.RuneCountInString(longContent))
	}
	if full.Revision != 1 || full.Bindings.PageKey == nil || *full.Bindings.PageKey != "/login" {
		t.Fatalf("Read did not return the full item: %#v", full)
	}
}

/* --------------------------------- helpers -------------------------------- */

func int64Strings(ids []int64) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, fmt.Sprintf("%d", id))
	}
	return out
}

func isNotFound(err error) bool {
	var apiErr *platform.Error
	return errors.As(err, &apiErr) && apiErr.Status == 404
}

func isInvalid(err error, field string) bool {
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 400 {
		return false
	}
	if field == "" {
		return true
	}
	return apiErr.Details["field"] == field
}
