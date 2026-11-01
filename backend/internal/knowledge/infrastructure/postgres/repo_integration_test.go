//go:build integration

package knowledgepg_test

import (
	"context"
	"errors"
	"testing"
	"time"

	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	knowledgepg "github.com/EdouardRichard/EzTest/backend/internal/knowledge/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

func systemPtr(value string) *string { return &value }

func itemFixture(id int64, scope knowledge.Scope, systemKey string, status knowledge.Status, title, content string, tags []string) knowledge.Item {
	item := knowledge.Item{
		ID:        id,
		Scope:     scope,
		SystemKey: nil,
		Title:     title,
		Content:   content,
		Tags:      tags,
		Status:    status,
		Revision:  1,
		CreatedAt: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC),
	}
	if scope == knowledge.ScopeSystem {
		item.SystemKey = systemPtr(systemKey)
	}
	return item
}

func revisionFixture(item knowledge.Item, id int64) knowledge.Revision {
	return knowledge.Revision{
		ID:          id,
		KnowledgeID: item.ID,
		Revision:    item.Revision,
		Title:       item.Title,
		Content:     item.Content,
		Tags:        item.Tags,
		CreatedAt:   item.CreatedAt,
	}
}

func mustInsert(t *testing.T, repo *knowledgepg.Repository, item knowledge.Item, revisionID int64) {
	t.Helper()
	if err := repo.Insert(context.Background(), item, revisionFixture(item, revisionID)); err != nil {
		t.Fatalf("insert item %d: %v", item.ID, err)
	}
}

func TestRepositoryInsertGetAndList(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := knowledgepg.NewRepository(pool)
	ctx := context.Background()

	global := itemFixture(1001, knowledge.ScopeGlobal, "", knowledge.StatusApproved,
		"Retry contract", "Callback must be idempotent and retry-safe", []string{"payments", "retry"})
	global.SourceVersionID = ptr[int64](500)
	global.CreatedBy = ptr[int64](7)
	global.Confidence = ptr(0.75)
	mustInsert(t, repo, global, 9001)
	payments := itemFixture(1002, knowledge.ScopeSystem, "payments", knowledge.StatusDraft,
		"Design token", "Use the shared design token file", []string{"design"})
	mustInsert(t, repo, payments, 9002)
	design := itemFixture(1003, knowledge.ScopeSystem, "design", knowledge.StatusApproved,
		"Design review", "Review the design page layout", nil)
	mustInsert(t, repo, design, 9003)

	loaded, err := repo.Get(ctx, 1001)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if loaded.Scope != knowledge.ScopeGlobal || loaded.Status != knowledge.StatusApproved ||
		loaded.Revision != 1 || loaded.SourceVersionID == nil || *loaded.SourceVersionID != 500 ||
		loaded.CreatedBy == nil || *loaded.CreatedBy != 7 {
		t.Fatalf("loaded = %+v", loaded)
	}
	if loaded.Confidence == nil || *loaded.Confidence != 0.75 {
		t.Fatalf("confidence = %v", loaded.Confidence)
	}
	if len(loaded.Tags) != 2 || loaded.Tags[0] != "payments" {
		t.Fatalf("tags = %#v", loaded.Tags)
	}
	if loaded.CreatedAt.IsZero() || loaded.UpdatedAt.IsZero() {
		t.Fatalf("timestamps = %v / %v", loaded.CreatedAt, loaded.UpdatedAt)
	}
	if _, err := repo.Get(ctx, 9999); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatalf("missing get error = %v", err)
	}

	all, err := repo.List(ctx, knowledge.Filter{Limit: 2})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 || all[0].ID != 1003 || all[1].ID != 1002 {
		t.Fatalf("list = %+v", all)
	}
	older, err := repo.List(ctx, knowledge.Filter{BeforeID: 1002, Limit: 10})
	if err != nil {
		t.Fatalf("list cursor: %v", err)
	}
	if len(older) != 1 || older[0].ID != 1001 {
		t.Fatalf("older = %+v", older)
	}
	systems, err := repo.List(ctx, knowledge.Filter{Scope: knowledge.ScopeSystem, Status: knowledge.StatusApproved, Limit: 10})
	if err != nil {
		t.Fatalf("list scope: %v", err)
	}
	if len(systems) != 1 || systems[0].ID != 1003 {
		t.Fatalf("systems = %+v", systems)
	}
	byKey, err := repo.List(ctx, knowledge.Filter{SystemKey: "payments", Limit: 10})
	if err != nil {
		t.Fatalf("list system key: %v", err)
	}
	if len(byKey) != 1 || byKey[0].ID != 1002 {
		t.Fatalf("by key = %+v", byKey)
	}
	byTag, err := repo.List(ctx, knowledge.Filter{Tag: "payments", Limit: 10})
	if err != nil {
		t.Fatalf("list tag: %v", err)
	}
	if len(byTag) != 1 || byTag[0].ID != 1001 {
		t.Fatalf("by tag = %+v", byTag)
	}
	if byTag[0].Tags != nil && len(byTag[0].Tags) == 0 {
		t.Fatalf("tags = %#v", byTag[0].Tags)
	}
	if design.Tags == nil {
		// nil tags must round-trip as an empty array, never NULL.
		stored, err := repo.Get(ctx, 1003)
		if err != nil {
			t.Fatalf("get empty tags: %v", err)
		}
		if stored.Tags == nil || len(stored.Tags) != 0 {
			t.Fatalf("empty tags = %#v", stored.Tags)
		}
	}
}

func TestRepositoryFullTextSearch(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := knowledgepg.NewRepository(pool)
	ctx := context.Background()

	heavy := itemFixture(1101, knowledge.ScopeGlobal, "", knowledge.StatusApproved,
		"Payment idempotency", "retry retry retry retry payment callback", []string{"retry"})
	heavy.CreatedAt = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	mustInsert(t, repo, heavy, 9101)
	light := itemFixture(1102, knowledge.ScopeGlobal, "", knowledge.StatusApproved,
		"Retry contract", "Callback must be retry safe", []string{"payments"})
	light.CreatedAt = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	mustInsert(t, repo, light, 9102)
	unrelated := itemFixture(1103, knowledge.ScopeGlobal, "", knowledge.StatusApproved,
		"Design tokens", "Use the shared design token file", []string{"design"})
	unrelated.CreatedAt = time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC)
	mustInsert(t, repo, unrelated, 9103)

	hits, err := repo.List(ctx, knowledge.Filter{Query: "retry", Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %+v", hits)
	}
	if hits[0].ID != 1101 || hits[1].ID != 1102 {
		t.Fatalf("rank order = %d, %d", hits[0].ID, hits[1].ID)
	}
	tagHit, err := repo.List(ctx, knowledge.Filter{Query: "design", Limit: 10})
	if err != nil {
		t.Fatalf("search tag: %v", err)
	}
	if len(tagHit) != 1 || tagHit[0].ID != 1103 {
		t.Fatalf("tag hit = %+v", tagHit)
	}
	none, err := repo.List(ctx, knowledge.Filter{Query: "unfindable", Limit: 10})
	if err != nil {
		t.Fatalf("search miss: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("search miss = %+v", none)
	}
}

func TestRepositoryUpdateAppendsRevisionAndRejectsStaleWrites(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := knowledgepg.NewRepository(pool)
	ctx := context.Background()

	item := itemFixture(1201, knowledge.ScopeGlobal, "", knowledge.StatusDraft,
		"Retry contract", "First body", []string{"payments"})
	mustInsert(t, repo, item, 9201)

	item.Content = "Second body"
	item.Revision = 2
	item.UpdatedAt = item.UpdatedAt.Add(time.Hour)
	if err := repo.Update(ctx, item, revisionFixture(item, 9202)); err != nil {
		t.Fatalf("update: %v", err)
	}
	loaded, err := repo.Get(ctx, 1201)
	if err != nil {
		t.Fatalf("get updated: %v", err)
	}
	if loaded.Content != "Second body" || loaded.Revision != 2 {
		t.Fatalf("loaded = %+v", loaded)
	}
	revisions, err := repo.ListRevisions(ctx, 1201)
	if err != nil {
		t.Fatalf("revisions: %v", err)
	}
	if len(revisions) != 2 || revisions[0].Revision != 2 || revisions[1].Revision != 1 {
		t.Fatalf("revisions = %+v", revisions)
	}
	if revisions[0].Content != "Second body" || revisions[1].Content != "First body" {
		t.Fatalf("revision bodies = %+v", revisions)
	}

	// A concurrent writer that still believes the stored revision is 2 must not
	// append revision 2 again.
	stale := item
	stale.Revision = 2
	if err := repo.Update(ctx, stale, revisionFixture(stale, 9203)); !errors.Is(err, knowledge.ErrStateConflict) {
		t.Fatalf("stale update error = %v", err)
	}
}

func TestRepositoryUpdateStatusGuardsFromStatus(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := knowledgepg.NewRepository(pool)
	ctx := context.Background()

	item := itemFixture(1301, knowledge.ScopeGlobal, "", knowledge.StatusDraft, "Title", "Body", nil)
	mustInsert(t, repo, item, 9301)

	item.Status = knowledge.StatusApproved
	item.ReviewedBy = ptr[int64](88)
	reviewedAt := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	item.ReviewedAt = &reviewedAt
	item.UpdatedAt = reviewedAt
	if err := repo.UpdateStatus(ctx, item, knowledge.StatusDraft); err != nil {
		t.Fatalf("update status: %v", err)
	}
	loaded, err := repo.Get(ctx, 1301)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if loaded.Status != knowledge.StatusApproved || loaded.ReviewedBy == nil || *loaded.ReviewedBy != 88 ||
		loaded.ReviewedAt == nil {
		t.Fatalf("loaded = %+v", loaded)
	}
	if err := repo.UpdateStatus(ctx, item, knowledge.StatusDraft); !errors.Is(err, knowledge.ErrStateConflict) {
		t.Fatalf("stale transition error = %v", err)
	}
}

func TestRepositoryListForInjection(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := knowledgepg.NewRepository(pool)
	ctx := context.Background()

	mustInsert(t, repo, itemFixture(1401, knowledge.ScopeGlobal, "", knowledge.StatusApproved,
		"Payment idempotency", "retry retry retry payment callback", []string{"retry"}), 9401)
	mustInsert(t, repo, itemFixture(1402, knowledge.ScopeSystem, "payments", knowledge.StatusApproved,
		"Payments retry", "retry payment", []string{"payments"}), 9402)
	mustInsert(t, repo, itemFixture(1403, knowledge.ScopeSystem, "design", knowledge.StatusApproved,
		"Design retry", "retry design", []string{"design"}), 9403)
	mustInsert(t, repo, itemFixture(1404, knowledge.ScopeGlobal, "", knowledge.StatusDraft,
		"Retry draft", "retry retry", []string{"retry"}), 9404)
	mustInsert(t, repo, itemFixture(1405, knowledge.ScopeGlobal, "", knowledge.StatusRevoked,
		"Retry revoked", "retry retry", []string{"retry"}), 9405)

	items, err := repo.ListForInjection(ctx, knowledge.InjectionQuery{
		SystemKeys: []string{"payments"},
		Query:      "retry",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("injection query: %v", err)
	}
	if len(items) != 2 || items[0].ID != 1401 || items[1].ID != 1402 {
		t.Fatalf("items = %+v", items)
	}
	for _, item := range items {
		if item.Status != knowledge.StatusApproved {
			t.Fatalf("injected non-approved item: %+v", item)
		}
	}

	global, err := repo.ListForInjection(ctx, knowledge.InjectionQuery{Query: "retry", Limit: 10})
	if err != nil {
		t.Fatalf("global injection query: %v", err)
	}
	if len(global) != 1 || global[0].ID != 1401 {
		t.Fatalf("global = %+v", global)
	}

	byID, err := repo.ListForInjection(ctx, knowledge.InjectionQuery{SystemKeys: []string{"payments"}, Limit: 10})
	if err != nil {
		t.Fatalf("injection id order: %v", err)
	}
	if len(byID) != 2 || byID[0].ID != 1402 || byID[1].ID != 1401 {
		t.Fatalf("id order = %+v", byID)
	}
}

func TestRepositoryUsageInsert(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := knowledgepg.NewRepository(pool)
	ctx := context.Background()

	mustInsert(t, repo, itemFixture(1501, knowledge.ScopeGlobal, "", knowledge.StatusApproved, "Title", "Body", nil), 9501)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	usageRepo := knowledgepg.NewUsageRepository(pool)
	err := usageRepo.Insert(ctx, []knowledge.Usage{
		{ID: 9601, KnowledgeID: 1501, AgentRunID: ptr[int64](7), WorkItemID: ptr[int64](8),
			TokenEstimate: 12, InjectedAt: now, CreatedAt: now, UpdatedAt: now},
		{ID: 9602, KnowledgeID: 1501, TokenEstimate: 3, InjectedAt: now, CreatedAt: now, UpdatedAt: now},
	})
	if err != nil {
		t.Fatalf("insert usage: %v", err)
	}
	var count int
	var maxTokens int
	if err := pool.QueryRow(ctx,
		`SELECT count(*), coalesce(max(token_estimate), 0) FROM knowledge_usage WHERE knowledge_id = 1501`).
		Scan(&count, &maxTokens); err != nil {
		t.Fatalf("count usage: %v", err)
	}
	if count != 2 || maxTokens != 12 {
		t.Fatalf("usage count = %d max = %d", count, maxTokens)
	}
	var agentRunID *int64
	if err := pool.QueryRow(ctx, `SELECT agent_run_id FROM knowledge_usage WHERE id = 9601`).Scan(&agentRunID); err != nil {
		t.Fatalf("scan agent run: %v", err)
	}
	if agentRunID == nil || *agentRunID != 7 {
		t.Fatalf("agent run = %v", agentRunID)
	}
}

func ptr[T any](value T) *T { return &value }
