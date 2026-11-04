//go:build integration

package knowledgeapp_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	knowledgeapp "github.com/EdouardRichard/EzTest/backend/internal/knowledge/application"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	knowledgepg "github.com/EdouardRichard/EzTest/backend/internal/knowledge/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// TestRetrievalTrgmTopK pins the pg_trgm retrieval stage of spec §10.2 against
// a real PostgreSQL instance: Chinese fuzzy recall must not depend on the
// bounded ts_rank candidate window, so an item older than the newest 200 rows
// has to be found through the trigram index/top-K query. It also pins the
// approved/scope filters, the empty corpus and deterministic ranking of the
// repository stage.
func TestRetrievalTrgmTopK(t *testing.T) {
	const query = "支付回调幂等形" // a one-rune typo of 性; no full-text token match

	insert := func(t *testing.T, repo *knowledgepg.Repository, item knowledge.Item) {
		t.Helper()
		revision := knowledge.Revision{
			ID:          item.ID + 500000,
			KnowledgeID: item.ID,
			Revision:    1,
			Title:       item.Title,
			Content:     item.Content,
			CreatedAt:   item.CreatedAt,
		}
		if err := repo.Insert(context.Background(), item, revision); err != nil {
			t.Fatalf("insert knowledge item %d: %v", item.ID, err)
		}
	}

	t.Run("chinese similarity recalls an item older than the newest 200 rows", func(t *testing.T) {
		pool := testutil.NewDB(t)
		repo := knowledgepg.NewRepository(pool)
		service := knowledgeapp.NewService(repo, nil, nil, nil, nil, nil, nil, nil)
		ctx := context.Background()
		base := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

		// The target is the oldest approved row. A ts_rank-ordered window (the
		// previous candidate stage) can only reach it if its text score is high,
		// which PostgreSQL's 'simple' FTS cannot give a Chinese query: the query
		// token differs from every stored token.
		target := knowledge.Item{
			ID: 900001, Scope: knowledge.ScopeGlobal, Title: "支付回调幂等性检查",
			Content: "支付回调必须保持幂等，重复通知只处理一次。", Status: knowledge.StatusApproved,
			Revision: 1, CreatedAt: base, UpdatedAt: base,
		}
		insert(t, repo, target)

		// More than 200 newer approved rows with no trigram overlap with the
		// query: the target sits outside the newest-200 id window.
		for index := int64(1); index <= 210; index++ {
			createdAt := base.Add(time.Duration(index) * time.Minute)
			insert(t, repo, knowledge.Item{
				ID: 900001 + index, Scope: knowledge.ScopeGlobal,
				Title:   fmt.Sprintf("测试条目 %03d", index),
				Content: fmt.Sprintf("第 %d 条与检索无关的中文操作说明。", index),
				Status:  knowledge.StatusApproved, Revision: 1,
				CreatedAt: createdAt, UpdatedAt: createdAt,
			})
		}

		// Non-approved and foreign-scope twins of the target must never be
		// retrieved, even though their text matches exactly.
		for offset, status := range []knowledge.Status{
			knowledge.StatusDraft, knowledge.StatusRejected, knowledge.StatusRevoked,
			knowledge.StatusSuspect, knowledge.StatusStale,
		} {
			createdAt := base.Add(time.Duration(300+offset) * time.Minute)
			insert(t, repo, knowledge.Item{
				ID: 910001 + int64(offset), Scope: knowledge.ScopeGlobal,
				Title: target.Title, Content: target.Content,
				Status: status, Revision: 1, CreatedAt: createdAt, UpdatedAt: createdAt,
			})
		}
		foreignKey := "crm"
		createdAt := base.Add(400 * time.Minute)
		insert(t, repo, knowledge.Item{
			ID: 920001, Scope: knowledge.ScopeSystem, SystemKey: &foreignKey,
			Title: target.Title, Content: target.Content,
			Status: knowledge.StatusApproved, Revision: 1, CreatedAt: createdAt, UpdatedAt: createdAt,
		})
		// An approved twin whose body carries a credential is a SQL candidate
		// (its title matches) but must be removed by the application secret scan.
		insert(t, repo, knowledge.Item{
			ID: 920002, Scope: knowledge.ScopeGlobal, Title: target.Title,
			Content: `{"password":"hunter2"}`,
			Status:  knowledge.StatusApproved, Revision: 1, CreatedAt: createdAt, UpdatedAt: createdAt,
		})

		page, err := service.Retrieve(ctx, knowledgeapp.Query{
			VersionID:  1,
			SystemKeys: []string{"shop"},
			Text:       query,
			Limit:      10,
		})
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		if !containsID(page, target.ID) {
			t.Fatalf("hits = %v, want the old target %d recalled by trigram similarity", hitIDs(page), target.ID)
		}
		allowed := map[int64]bool{target.ID: true}
		for _, hit := range page.Items {
			if !allowed[hit.ID] {
				t.Fatalf("hits = %v, want only the approved visible target %d", hitIDs(page), target.ID)
			}
			if hit.Score <= 0 {
				t.Fatalf("hit %d score = %v, want a positive trigram score", hit.ID, hit.Score)
			}
		}

		again, err := service.Retrieve(ctx, knowledgeapp.Query{
			VersionID:  1,
			SystemKeys: []string{"shop"},
			Text:       query,
			Limit:      10,
		})
		if err != nil {
			t.Fatalf("Retrieve again: %v", err)
		}
		if fmt.Sprint(hitIDs(page)) != fmt.Sprint(hitIDs(again)) {
			t.Fatalf("retrieval is not deterministic: %v vs %v", hitIDs(page), hitIDs(again))
		}
		for index := range page.Items {
			if page.Items[index].Score != again.Items[index].Score {
				t.Fatalf("score %d is not deterministic: %v vs %v",
					index, page.Items[index].Score, again.Items[index].Score)
			}
		}
	})

	// A short query that is an exact substring of a longer CJK word has no
	// shared pg_trgm trigrams with it (the query's trigrams are all padded), so
	// whole-string and word similarity are both zero. If another row passes the
	// SQL floor the top-K stage is not empty and the ts_rank fallback never
	// runs, so the candidate predicate itself must admit the substring match —
	// this is the real-PostgreSQL twin of TestKnowledgeChineseTrigram's item 803.
	t.Run("content-only item is recalled for a two-rune query", func(t *testing.T) {
		pool := testutil.NewDB(t)
		repo := knowledgepg.NewRepository(pool)
		service := knowledgeapp.NewService(repo, nil, nil, nil, nil, nil, nil, nil)
		ctx := context.Background()
		base := time.Date(2026, 11, 2, 11, 0, 0, 0, time.UTC)

		loginRecipe := knowledge.Item{
			ID: 940001, Scope: knowledge.ScopeGlobal, Title: "登录配方：管理员",
			Content: "打开 /login 并填写账号。", Tags: []string{"kind:login_recipe"},
			Status: knowledge.StatusApproved, Revision: 1, CreatedAt: base, UpdatedAt: base,
		}
		payment := knowledge.Item{
			ID: 940002, Scope: knowledge.ScopeGlobal, Title: "支付回调幂等",
			Content: "回调必须幂等重试。", Tags: []string{"kind:api_auth"},
			Status: knowledge.StatusApproved, Revision: 1, CreatedAt: base, UpdatedAt: base,
		}
		// The target: "登录" appears only inside its content, never at a word
		// boundary, so its title is unrelated and its content has trigram
		// similarity zero to the query.
		contentOnly := knowledge.Item{
			ID: 940003, Scope: knowledge.ScopeGlobal, Title: "通用操作说明",
			Content: "先打开登录页再填写用户名。", Tags: []string{"kind:login_recipe"},
			Status: knowledge.StatusApproved, Revision: 1, CreatedAt: base, UpdatedAt: base,
		}
		insert(t, repo, loginRecipe)
		insert(t, repo, payment)
		insert(t, repo, contentOnly)

		page, err := service.Retrieve(ctx, knowledgeapp.Query{
			VersionID:  1,
			SystemKeys: []string{"shop"},
			Text:       "登录",
			Limit:      10,
		})
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		if !containsID(page, contentOnly.ID) {
			t.Fatalf("hits = %v, want the content-only item %d recalled next to the title hit %d",
				hitIDs(page), contentOnly.ID, loginRecipe.ID)
		}
		if !containsID(page, loginRecipe.ID) {
			t.Fatalf("hits = %v, want the title hit %d", hitIDs(page), loginRecipe.ID)
		}
		if containsID(page, payment.ID) {
			t.Fatalf("hits = %v, want the unrelated payment item dropped", hitIDs(page))
		}
		if page.Items[0].ID != loginRecipe.ID {
			t.Fatalf("hits = %v, want the stronger title hit %d first", hitIDs(page), loginRecipe.ID)
		}
		for _, hit := range page.Items {
			if hit.Score <= 0 {
				t.Fatalf("hit %d score = %v, want a positive character-ngram score", hit.ID, hit.Score)
			}
		}
	})

	// The 0.1 whole-string floor excluded the one-rune typo 登陆 (similarity
	// 0.0909 to 登录配方：管理员). Here a second row passes the old floor, so the
	// top-K stage is non-empty and the ts_rank fallback cannot rescue the typo:
	// the near-miss must be admitted by the candidate predicate itself.
	t.Run("two-rune near miss is not hidden by another top-K hit", func(t *testing.T) {
		pool := testutil.NewDB(t)
		repo := knowledgepg.NewRepository(pool)
		service := knowledgeapp.NewService(repo, nil, nil, nil, nil, nil, nil, nil)
		ctx := context.Background()
		base := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

		typoTarget := knowledge.Item{
			ID: 941001, Scope: knowledge.ScopeGlobal, Title: "登录配方：管理员",
			Content: "打开 /login 并填写账号。", Tags: []string{"kind:login_recipe"},
			Status: knowledge.StatusApproved, Revision: 1, CreatedAt: base, UpdatedAt: base,
		}
		// Old whole-string similarity 0.125 > the previous 0.1 floor: this row
		// used to make the top-K stage non-empty on its own.
		topKHit := knowledge.Item{
			ID: 941002, Scope: knowledge.ScopeGlobal, Title: "登录页元素",
			Content: "登录页元素定位。", Tags: []string{"kind:page_locator"},
			Status: knowledge.StatusApproved, Revision: 1, CreatedAt: base, UpdatedAt: base,
		}
		insert(t, repo, typoTarget)
		insert(t, repo, topKHit)

		page, err := service.Retrieve(ctx, knowledgeapp.Query{
			VersionID:  1,
			SystemKeys: []string{"shop"},
			Text:       "登陆",
			Limit:      10,
		})
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		if !containsID(page, typoTarget.ID) {
			t.Fatalf("hits = %v, want the two-rune near miss %d recalled alongside the stronger hit %d",
				hitIDs(page), typoTarget.ID, topKHit.ID)
		}
		if !containsID(page, topKHit.ID) {
			t.Fatalf("hits = %v, want the top-K row %d preserved", hitIDs(page), topKHit.ID)
		}
		if page.Items[0].ID != topKHit.ID {
			t.Fatalf("hits = %v, want %d first: its character-ngram score is higher", hitIDs(page), topKHit.ID)
		}
		for _, hit := range page.Items {
			if hit.Score <= 0 {
				t.Fatalf("hit %d score = %v, want a positive character-ngram score", hit.ID, hit.Score)
			}
		}
	})

	// The Go scorer accepts a tag match. A tag-only row must not be silently
	// dropped just because some other row has a title/content trigram hit; the
	// candidate SQL covers tags through the same trigram predicate (the tag
	// expression is part of the retrieval index, see knowledge_0003_trgm.sql).
	t.Run("tag-only match is recalled when a text hit exists", func(t *testing.T) {
		pool := testutil.NewDB(t)
		repo := knowledgepg.NewRepository(pool)
		service := knowledgeapp.NewService(repo, nil, nil, nil, nil, nil, nil, nil)
		ctx := context.Background()
		base := time.Date(2026, 11, 2, 13, 0, 0, 0, time.UTC)

		tagOnly := knowledge.Item{
			ID: 942001, Scope: knowledge.ScopeGlobal, Title: "通用操作说明",
			Content: "先打开登录页再填写用户名。", Tags: []string{"kind:login_recipe"},
			Status: knowledge.StatusApproved, Revision: 1, CreatedAt: base, UpdatedAt: base,
		}
		// A title/content hit for the same query keeps the old top-K stage
		// non-empty, which used to hide the tag-only item.
		textHit := knowledge.Item{
			ID: 942002, Scope: knowledge.ScopeGlobal, Title: "登录配方：管理员",
			Content: "打开 login 页面并填写账号。", Tags: []string{"kind:login_recipe"},
			Status: knowledge.StatusApproved, Revision: 1, CreatedAt: base, UpdatedAt: base,
		}
		insert(t, repo, tagOnly)
		insert(t, repo, textHit)

		page, err := service.Retrieve(ctx, knowledgeapp.Query{
			VersionID:  1,
			SystemKeys: []string{"shop"},
			Text:       "login",
			Limit:      10,
		})
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		if !containsID(page, tagOnly.ID) {
			t.Fatalf("hits = %v, want the tag-only item %d recalled next to the text hit %d",
				hitIDs(page), tagOnly.ID, textHit.ID)
		}
		if !containsID(page, textHit.ID) {
			t.Fatalf("hits = %v, want the text hit %d", hitIDs(page), textHit.ID)
		}
		for _, hit := range page.Items {
			if hit.Score <= 0 {
				t.Fatalf("hit %d score = %v, want a positive character-ngram score", hit.ID, hit.Score)
			}
		}
	})

	// The strict binding pass keeps its own parameterized top-K query: a bound
	// item outranks the compatible generic fallback, and an item that conflicts
	// on the queried dimension never leaks through either pass.
	t.Run("binding containment and fallback are preserved", func(t *testing.T) {
		pool := testutil.NewDB(t)
		repo := knowledgepg.NewRepository(pool)
		service := knowledgeapp.NewService(repo, nil, nil, nil, nil, nil, nil, nil)
		ctx := context.Background()
		base := time.Date(2026, 11, 2, 14, 0, 0, 0, time.UTC)

		strictBound := knowledge.Item{
			ID: 943001, Scope: knowledge.ScopeGlobal, Title: "支付回调幂等性检查",
			Content:  "支付回调必须保持幂等，重复通知只处理一次。",
			Bindings: knowledge.Bindings{PageKey: ptr("/checkout")},
			Status:   knowledge.StatusApproved, Revision: 1, CreatedAt: base, UpdatedAt: base,
		}
		generic := knowledge.Item{
			ID: 943002, Scope: knowledge.ScopeGlobal, Title: "支付回调幂等性检查",
			Content: "支付回调必须保持幂等，重复通知只处理一次。",
			Status:  knowledge.StatusApproved, Revision: 1, CreatedAt: base, UpdatedAt: base,
		}
		conflicting := knowledge.Item{
			ID: 943003, Scope: knowledge.ScopeGlobal, Title: "支付回调幂等性检查",
			Content:  "支付回调必须保持幂等，重复通知只处理一次。",
			Bindings: knowledge.Bindings{PageKey: ptr("/login")},
			Status:   knowledge.StatusApproved, Revision: 1, CreatedAt: base, UpdatedAt: base,
		}
		insert(t, repo, strictBound)
		insert(t, repo, generic)
		insert(t, repo, conflicting)

		page, err := service.Retrieve(ctx, knowledgeapp.Query{
			VersionID:  1,
			SystemKeys: []string{"shop"},
			PageKey:    ptr("/checkout"),
			Text:       "支付回调幂等形",
			Limit:      10,
		})
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		if !containsID(page, strictBound.ID) || !containsID(page, generic.ID) {
			t.Fatalf("hits = %v, want the strict bound item %d and the compatible generic item %d",
				hitIDs(page), strictBound.ID, generic.ID)
		}
		if containsID(page, conflicting.ID) {
			t.Fatalf("hits = %v, want the conflicting binding item %d excluded", hitIDs(page), conflicting.ID)
		}
		if page.Items[0].ID != strictBound.ID {
			t.Fatalf("hits = %v, want the strict containment hit %d first", hitIDs(page), strictBound.ID)
		}
	})

	// The retrieval SQL depends on the pg_trgm extension and the GIN index; a
	// manual rollback must also delete the schema_migrations row, otherwise the
	// forward-only runner treats the migration as applied and never recreates
	// the objects (see the rollback note in knowledge_0003_trgm.sql).
	t.Run("migration installs pg_trgm and the trigram index", func(t *testing.T) {
		pool := testutil.NewDB(t)
		ctx := context.Background()

		var extensionVersion string
		if err := pool.QueryRow(ctx,
			`SELECT extversion FROM pg_extension WHERE extname = 'pg_trgm'`).Scan(&extensionVersion); err != nil {
			t.Fatalf("pg_trgm extension is not installed: %v", err)
		}
		var indexDef string
		if err := pool.QueryRow(ctx, `
			SELECT indexdef FROM pg_indexes
			WHERE schemaname = current_schema() AND indexname = 'idx_knowledge_item_trgm'
		`).Scan(&indexDef); err != nil {
			t.Fatalf("idx_knowledge_item_trgm is not installed: %v", err)
		}
		lower := strings.ToLower(indexDef)
		for _, want := range []string{
			"using gin", "gin_trgm_ops", "title", "content", "eztest_text_array_to_string(tags)",
		} {
			if !strings.Contains(lower, want) {
				t.Fatalf("index definition %q does not contain %q", indexDef, want)
			}
		}
		var applied int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM schema_migrations WHERE version = 'knowledge_0003_trgm.sql'`).Scan(&applied); err != nil {
			t.Fatalf("read schema_migrations: %v", err)
		}
		if applied != 1 {
			t.Fatalf("knowledge_0003_trgm.sql applied %d times, want 1", applied)
		}
	})

	t.Run("empty corpus returns an empty page", func(t *testing.T) {
		pool := testutil.NewDB(t)
		repo := knowledgepg.NewRepository(pool)
		service := knowledgeapp.NewService(repo, nil, nil, nil, nil, nil, nil, nil)

		page, err := service.Retrieve(context.Background(), knowledgeapp.Query{
			VersionID: 1,
			Text:      query,
			Limit:     10,
		})
		if err != nil {
			t.Fatalf("Retrieve on an empty corpus: %v", err)
		}
		if len(page.Items) != 0 || page.HasMore {
			t.Fatalf("empty corpus page = %#v, want no hits", page)
		}
	})

	t.Run("equal trigram scores break ties by id desc", func(t *testing.T) {
		pool := testutil.NewDB(t)
		repo := knowledgepg.NewRepository(pool)
		service := knowledgeapp.NewService(repo, nil, nil, nil, nil, nil, nil, nil)
		base := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

		// Three approved items with identical text therefore identical scores;
		// the deterministic tie-break is id desc.
		for _, id := range []int64{930001, 930002, 930003} {
			insert(t, repo, knowledge.Item{
				ID: id, Scope: knowledge.ScopeGlobal, Title: "支付回调幂等性检查",
				Content: "支付回调必须保持幂等。", Status: knowledge.StatusApproved,
				Revision: 1, CreatedAt: base, UpdatedAt: base,
			})
		}

		page, err := service.Retrieve(context.Background(), knowledgeapp.Query{
			VersionID: 2,
			Text:      query,
			Limit:     10,
		})
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		want := []int64{930003, 930002, 930001}
		got := hitIDs(page)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("hits = %v, want deterministic id-desc tie-break %v", got, want)
		}
	})
}
