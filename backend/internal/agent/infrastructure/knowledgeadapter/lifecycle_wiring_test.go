package knowledgeadapter

import (
	"context"
	"errors"
	"strings"
	"testing"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	knowledgeapp "github.com/EdouardRichard/EzTest/backend/internal/knowledge/application"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
)

// TestLifecycleWiringSuccessFailure drives the success/failure pipeline through
// the real knowledge application service and repository contract:
//
//   - a passed case run stamps last_verified_at on the approved knowledge reused
//     by its page/API/account bindings (成功复用 → MarkVerified);
//   - a failed case run moves that same knowledge to suspect with a non-secret
//     reason (失败复用 → MarkSuspect);
//   - unrelated bindings are never touched and a missing configuration fails
//     closed instead of silently succeeding.
func TestLifecycleWiringSuccessFailure(t *testing.T) {
	ctx := context.Background()

	t.Run("a passed case verifies the knowledge reused by its bindings", func(t *testing.T) {
		repo, service := newLifecycleKnowledgeService(t, 7500)
		seeded := insertKnowledgeItem(t, repo, knowledge.Item{
			ID:      8001,
			Title:   "登录页定位",
			Content: `{"kind":"page_locator","route":"/login"}`,
			Tags:    []string{"page_locator"},
			Status:  knowledge.StatusApproved,
			Bindings: knowledge.Bindings{
				SystemID:      int64Ptr(7001),
				EnvironmentID: int64Ptr(7101),
				PageKey:       stringPtr("/login"),
			},
		})

		adapter := lifecycleAdapter(t, service)
		if err := adapter.OnCaseRunPassed(ctx, caseSuccessFactFixture()); err != nil {
			t.Fatalf("OnCaseRunPassed: %v", err)
		}

		stored, err := repo.Get(ctx, seeded.ID)
		if err != nil {
			t.Fatalf("Get seeded item: %v", err)
		}
		if stored.LastVerifiedAt == nil {
			t.Fatal("a passed case must stamp last_verified_at on the reused knowledge")
		}
		for _, item := range repo.allItems() {
			if item.LastVerifiedAt == nil {
				t.Fatalf("item %d was not marked verified after a passed case", item.ID)
			}
		}
	})

	t.Run("a failed case marks the knowledge reused by its bindings suspect", func(t *testing.T) {
		repo, service := newLifecycleKnowledgeService(t, 7600)
		pageItem := insertKnowledgeItem(t, repo, knowledge.Item{
			ID:      8101,
			Title:   "登录页定位",
			Content: `{"kind":"page_locator","route":"/login"}`,
			Tags:    []string{"page_locator"},
			Status:  knowledge.StatusApproved,
			Bindings: knowledge.Bindings{
				SystemID:      int64Ptr(7001),
				EnvironmentID: int64Ptr(7101),
				PageKey:       stringPtr("/login"),
			},
		})
		apiItem := insertKnowledgeItem(t, repo, knowledge.Item{
			ID:      8102,
			Title:   "登录 API 鉴权",
			Content: `{"kind":"api_auth","endpoint":"/api/login"}`,
			Tags:    []string{"api_auth"},
			Status:  knowledge.StatusApproved,
			Bindings: knowledge.Bindings{
				SystemID:      int64Ptr(7001),
				EnvironmentID: int64Ptr(7101),
				APIKey:        stringPtr("POST /api/login"),
			},
		})
		roleItem := insertKnowledgeItem(t, repo, knowledge.Item{
			ID:      8103,
			Title:   "admin 登录配方",
			Content: `{"kind":"login_recipe","role":"admin"}`,
			Tags:    []string{"login_recipe"},
			Status:  knowledge.StatusApproved,
			Bindings: knowledge.Bindings{
				SystemID:      int64Ptr(7001),
				EnvironmentID: int64Ptr(7101),
				AccountRole:   stringPtr("admin"),
			},
		})
		unrelated := insertKnowledgeItem(t, repo, knowledge.Item{
			ID:      8104,
			Title:   "购物车页定位",
			Content: `{"kind":"page_locator","route":"/checkout"}`,
			Tags:    []string{"page_locator"},
			Status:  knowledge.StatusApproved,
			Bindings: knowledge.Bindings{
				SystemID:      int64Ptr(7001),
				EnvironmentID: int64Ptr(7101),
				PageKey:       stringPtr("/checkout"),
			},
		})

		adapter := lifecycleAdapter(t, service)
		fact := executionapp.CaseFailureFact{
			VersionID:     10,
			CaseRunID:     9001,
			CaseVersionID: 801,
			FailureClass:  execution.FailureClassApplication,
			AccountRoles:  []string{"admin"},
			PageIDs:       []int64{501},
			APIIDs:        []int64{601},
		}
		if err := adapter.OnCaseRunFailed(ctx, fact); err != nil {
			t.Fatalf("OnCaseRunFailed: %v", err)
		}

		for _, id := range []int64{pageItem.ID, apiItem.ID, roleItem.ID} {
			stored, err := repo.Get(ctx, id)
			if err != nil {
				t.Fatalf("Get %d: %v", id, err)
			}
			if stored.Status != knowledge.StatusSuspect {
				t.Fatalf("item %d status = %q, want suspect after a failed case", id, stored.Status)
			}
			if stored.ReviewReason == nil || !strings.Contains(*stored.ReviewReason, "case_run 9001") {
				t.Fatalf("item %d review reason = %v, want the case_run provenance", id, stored.ReviewReason)
			}
		}
		stored, err := repo.Get(ctx, unrelated.ID)
		if err != nil {
			t.Fatalf("Get unrelated: %v", err)
		}
		if stored.Status != knowledge.StatusApproved {
			t.Fatalf("unrelated knowledge status = %q, want approved", stored.Status)
		}
		if stored.ReviewReason != nil {
			t.Fatalf("unrelated knowledge review reason = %v, want nil", stored.ReviewReason)
		}

		// No credential and no case title may reach the persisted review reason.
		for _, id := range []int64{pageItem.ID, apiItem.ID, roleItem.ID} {
			item, err := repo.Get(ctx, id)
			if err != nil {
				t.Fatalf("Get %d: %v", id, err)
			}
			reason := ""
			if item.ReviewReason != nil {
				reason = *item.ReviewReason
			}
			lowered := strings.ToLower(reason)
			for _, marker := range []string{"password", "bearer", "authorization", "secret", "登录并下单"} {
				if strings.Contains(lowered, strings.ToLower(marker)) {
					t.Fatalf("item %d review reason leaks %q: %q", id, marker, reason)
				}
			}
		}
	})

	t.Run("unbound facts and missing configuration never write", func(t *testing.T) {
		repo, service := newLifecycleKnowledgeService(t, 7700)
		adapter := lifecycleAdapter(t, service)
		fact := executionapp.CaseFailureFact{
			VersionID: 10,
			CaseRunID: 9002,
			PageIDs:   []int64{999},
			APIIDs:    []int64{998},
		}
		if err := adapter.OnCaseRunFailed(ctx, fact); err != nil {
			t.Fatalf("unbound failure fact must be a no-op, got %v", err)
		}
		if len(repo.allItems()) != 0 {
			t.Fatalf("unbound failure fact wrote %d items", len(repo.allItems()))
		}

		if err := New(nil, nil).OnCaseRunFailed(ctx, fact); err == nil {
			t.Fatal("a failure hook without the knowledge service must fail closed")
		}
		if err := New(service, nil).OnCaseRunFailed(ctx, fact); err == nil {
			t.Fatal("a failure hook without the version binding reader must fail closed")
		}
		invalid := fact
		invalid.VersionID = 0
		if err := adapter.OnCaseRunFailed(ctx, invalid); err == nil {
			t.Fatal("a failure hook without a version must fail closed")
		}
		if len(repo.allItems()) != 0 {
			t.Fatalf("failing hooks wrote %d items", len(repo.allItems()))
		}
	})
}

func newLifecycleKnowledgeService(t *testing.T, firstID int64) (*memoryKnowledgeRepository, *knowledgeapp.Service) {
	t.Helper()
	repo := newMemoryKnowledgeRepository()
	service := knowledgeapp.NewService(
		repo, nil, nil, nil, nil, nil,
		fixedKnowledgeClock{}, &sequenceKnowledgeIDs{next: firstID},
	)
	return repo, service
}

func lifecycleAdapter(t *testing.T, service *knowledgeapp.Service) *Adapter {
	t.Helper()
	adapter := New(service, &fakeVersionSystemReader{keys: []string{"shop"}})
	adapter.SetVersionBindings(&fakeVersionBindingReader{bindings: []VersionBinding{{
		SystemID:      7001,
		EnvironmentID: 7101,
		SystemKey:     "shop",
		Pages:         []PageBinding{{ID: 501, Key: "/login"}},
		APIs:          []APIBinding{{ID: 601, Key: "POST /api/login"}},
		AccountRoles:  []string{"admin"},
	}}})
	return adapter
}

func int64Ptr(value int64) *int64 { return &value }

// The lifecycle methods widen the adapter's knowledge surface; the injection /
// proposal fake never uses them, so they fail closed when called accidentally.
func (f *fakeKnowledgeService) List(context.Context, knowledgeapp.ListFilter) (knowledgeapp.Page, error) {
	return knowledgeapp.Page{}, errors.New("knowledge lifecycle is not configured in this fake")
}

func (f *fakeKnowledgeService) MarkVerified(context.Context, int64) (knowledge.Item, error) {
	return knowledge.Item{}, errors.New("knowledge lifecycle is not configured in this fake")
}

func (f *fakeKnowledgeService) MarkSuspect(context.Context, int64, string) (knowledge.Item, error) {
	return knowledge.Item{}, errors.New("knowledge lifecycle is not configured in this fake")
}
