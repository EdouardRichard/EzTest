//go:build integration

package knowledgeapp_test

import (
	"context"
	"fmt"
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
