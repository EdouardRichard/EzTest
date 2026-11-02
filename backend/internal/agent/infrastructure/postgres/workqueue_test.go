//go:build integration

package agentpg_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

func assertJSONObjectEqual(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode stored JSON %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decode expected JSON %q: %v", want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
}

func createContractWorkItem(t *testing.T, repo *agentpg.WorkItemRepository, in agent.CreateWorkItemParams) agent.WorkItem {
	t.Helper()
	item, err := repo.CreateWorkItem(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateWorkItem(%s): %v", in.Kind, err)
	}
	return item
}

func TestWorkItemRoundTrip(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewWorkItemRepository(pool, newIDs(t))
	ctx := context.Background()
	now := time.Now().UTC().Add(time.Second)

	dependency := createContractWorkItem(t, repo, agent.CreateWorkItemParams{
		VersionID:      42,
		Kind:           agent.WorkItemKindExploreLogin,
		Goal:           "复用已有登录会话",
		CreatedBy:      agent.WorkItemCreatedByPlanner,
		IdempotencyKey: "42:explore.login:dep",
	})
	claimedDep, err := repo.ClaimWorkItem(ctx, "explorer-1", now)
	if err != nil || claimedDep == nil || claimedDep.ID != dependency.ID {
		t.Fatalf("claim dependency = %#v, %v", claimedDep, err)
	}
	if err := repo.CompleteWorkItem(ctx, dependency.ID, claimedDep.LeaseEpoch, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("complete dependency: %v", err)
	}

	params := agent.CreateWorkItemParams{
		VersionID:      42,
		Kind:           agent.WorkItemKindDesignCases,
		Goal:           "覆盖全部已确认需求生成用例草稿",
		Target:         json.RawMessage(`{"sut_id":"1001","page_id":"2002","role":"admin"}`),
		Inputs:         json.RawMessage(`{"requirement_ids":["3001","3002"],"fact_ids":[]}`),
		Expected:       json.RawMessage(`{"artifact":"case_draft","acceptance":"覆盖全部已确认需求"}`),
		Budget:         json.RawMessage(`{"max_tokens":20000,"run_seconds":120,"max_turns_fuse":24}`),
		DependsOn:      []int64{dependency.ID},
		CreatedBy:      agent.WorkItemCreatedByStateMachine,
		IdempotencyKey: "42:design.cases:hash",
	}
	item := createContractWorkItem(t, repo, params)
	if item.ID == 0 || item.Status != agent.WorkItemOpen {
		t.Fatalf("created item = %#v", item)
	}
	if item.Kind != params.Kind || item.Goal != params.Goal {
		t.Fatalf("created kind/goal = %q/%q", item.Kind, item.Goal)
	}
	if item.CreatedBy != params.CreatedBy || item.LeaseEpoch != 0 || item.Attempts != 0 {
		t.Fatalf("created provenance/lease = %#v", item)
	}
	if item.IdempotencyKey == nil || *item.IdempotencyKey != params.IdempotencyKey {
		t.Fatalf("created idempotency_key = %#v", item.IdempotencyKey)
	}
	if !slices.Equal(item.DependsOn, []int64{dependency.ID}) {
		t.Fatalf("created depends_on = %#v", item.DependsOn)
	}
	assertJSONObjectEqual(t, item.Target, string(params.Target))
	assertJSONObjectEqual(t, item.Inputs, string(params.Inputs))
	assertJSONObjectEqual(t, item.Expected, string(params.Expected))
	assertJSONObjectEqual(t, item.Budget, string(params.Budget))
	assertJSONObjectEqual(t, item.Result, `{}`)

	claimed, err := repo.ClaimWorkItem(ctx, "case-author-1", now)
	if err != nil || claimed == nil || claimed.ID != item.ID {
		t.Fatalf("claim = %#v, %v", claimed, err)
	}
	if claimed.LeaseEpoch != 1 || claimed.Attempts != 1 {
		t.Fatalf("claim epoch/attempts = %d/%d, want 1/1", claimed.LeaseEpoch, claimed.Attempts)
	}
	if claimed.ClaimedBy == nil || *claimed.ClaimedBy != "case-author-1" {
		t.Fatalf("claimed_by = %#v", claimed.ClaimedBy)
	}
	result := json.RawMessage(`{"case_ids":["4001","4002"],"reason":"covered"}`)
	if err := repo.CompleteWorkItem(ctx, item.ID, claimed.LeaseEpoch, result); err != nil {
		t.Fatalf("CompleteWorkItem: %v", err)
	}

	stored, err := repo.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Status != agent.WorkItemDone || stored.ClaimedBy != nil || stored.LeaseUntil != nil {
		t.Fatalf("completed item = %#v", stored)
	}
	if stored.Kind != params.Kind || stored.Goal != params.Goal {
		t.Fatalf("stored kind/goal = %q/%q", stored.Kind, stored.Goal)
	}
	if stored.CreatedBy != params.CreatedBy ||
		stored.IdempotencyKey == nil || *stored.IdempotencyKey != params.IdempotencyKey {
		t.Fatalf("stored provenance = %#v", stored)
	}
	if stored.LeaseEpoch != claimed.LeaseEpoch || stored.Attempts != claimed.Attempts {
		t.Fatalf("stored epoch/attempts = %d/%d", stored.LeaseEpoch, stored.Attempts)
	}
	if !slices.Equal(stored.DependsOn, []int64{dependency.ID}) {
		t.Fatalf("stored depends_on = %#v", stored.DependsOn)
	}
	assertJSONObjectEqual(t, stored.Target, string(params.Target))
	assertJSONObjectEqual(t, stored.Inputs, string(params.Inputs))
	assertJSONObjectEqual(t, stored.Expected, string(params.Expected))
	assertJSONObjectEqual(t, stored.Budget, string(params.Budget))
	assertJSONObjectEqual(t, stored.Result, string(result))
	if stored.CreatedAt.IsZero() || stored.UpdatedAt.IsZero() {
		t.Fatalf("timestamps = %v/%v", stored.CreatedAt, stored.UpdatedAt)
	}

	// Re-creating a completed item with the same key returns the existing row.
	again, err := repo.CreateWorkItem(ctx, params)
	if err != nil {
		t.Fatalf("CreateWorkItem again: %v", err)
	}
	if again.ID != item.ID || again.Status != agent.WorkItemDone {
		t.Fatalf("idempotent create = %#v, want id %d done", again, item.ID)
	}
}

func TestWorkItemIdempotency(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewWorkItemRepository(pool, newIDs(t))

	params := agent.CreateWorkItemParams{
		VersionID:      7,
		Kind:           agent.WorkItemKindExplorePage,
		Goal:           "发现页面树",
		CreatedBy:      agent.WorkItemCreatedByPlanner,
		IdempotencyKey: "7:explore.page:target-hash",
	}
	first := createContractWorkItem(t, repo, params)
	second := createContractWorkItem(t, repo, params)
	if second.ID != first.ID {
		t.Fatalf("same idempotency key created %d and %d", first.ID, second.ID)
	}
	if second.IdempotencyKey == nil || *second.IdempotencyKey != params.IdempotencyKey {
		t.Fatalf("idempotency key = %#v", second.IdempotencyKey)
	}

	otherVersion := params
	otherVersion.VersionID = 8
	if item := createContractWorkItem(t, repo, otherVersion); item.ID == first.ID {
		t.Fatal("idempotency key was not scoped by version")
	}
	otherKind := params
	otherKind.Kind = agent.WorkItemKindExploreAPI
	if item := createContractWorkItem(t, repo, otherKind); item.ID == first.ID {
		t.Fatal("idempotency key was not scoped by kind")
	}

	// A NULL key is exempt: each create inserts a new row.
	withoutKey := agent.CreateWorkItemParams{
		VersionID: 7,
		Kind:      agent.WorkItemKindExploreCoverage,
		Goal:      "覆盖矩阵",
		CreatedBy: agent.WorkItemCreatedByPlanner,
	}
	noKeyOne := createContractWorkItem(t, repo, withoutKey)
	noKeyTwo := createContractWorkItem(t, repo, withoutKey)
	if noKeyOne.ID == noKeyTwo.ID {
		t.Fatal("NULL idempotency keys deduplicated")
	}
	if noKeyOne.IdempotencyKey != nil || noKeyTwo.IdempotencyKey != nil {
		t.Fatalf("NULL idempotency key stored as %#v/%#v", noKeyOne.IdempotencyKey, noKeyTwo.IdempotencyKey)
	}
}

func TestDependsOnGating(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewWorkItemRepository(pool, newIDs(t))
	ctx := context.Background()
	now := time.Now().UTC().Add(time.Second)

	dependency := createContractWorkItem(t, repo, agent.CreateWorkItemParams{
		VersionID:      9,
		Kind:           agent.WorkItemKindExploreLogin,
		Goal:           "登录",
		CreatedBy:      agent.WorkItemCreatedByPlanner,
		IdempotencyKey: "9:explore.login:1",
	})
	blocked := createContractWorkItem(t, repo, agent.CreateWorkItemParams{
		VersionID:      9,
		Kind:           agent.WorkItemKindDesignCases,
		Goal:           "依赖登录配方",
		DependsOn:      []int64{dependency.ID},
		CreatedBy:      agent.WorkItemCreatedByStateMachine,
		IdempotencyKey: "9:design.cases:1",
	})
	readyNil := createContractWorkItem(t, repo, agent.CreateWorkItemParams{
		VersionID:      9,
		Kind:           agent.WorkItemKindExplorePage,
		Goal:           "NULL 依赖",
		CreatedBy:      agent.WorkItemCreatedByPlanner,
		IdempotencyKey: "9:explore.page:null",
	})
	readyEmpty := createContractWorkItem(t, repo, agent.CreateWorkItemParams{
		VersionID:      9,
		Kind:           agent.WorkItemKindExploreAPI,
		Goal:           "空数组依赖",
		DependsOn:      []int64{},
		CreatedBy:      agent.WorkItemCreatedByPlanner,
		IdempotencyKey: "9:explore.api:empty",
	})

	// Only the dependency and the two no-dependency items are claimable. The
	// id order keeps the assertions deterministic.
	first, err := repo.ClaimWorkItem(ctx, "worker-a", now)
	if err != nil || first == nil || first.ID != dependency.ID {
		t.Fatalf("first claim = %#v, %v, want dependency %d", first, err, dependency.ID)
	}
	second, err := repo.ClaimWorkItem(ctx, "worker-a", now)
	if err != nil || second == nil || second.ID != readyNil.ID {
		t.Fatalf("second claim = %#v, %v, want ready nil item %d", second, err, readyNil.ID)
	}
	third, err := repo.ClaimWorkItem(ctx, "worker-a", now)
	if err != nil || third == nil || third.ID != readyEmpty.ID {
		t.Fatalf("third claim = %#v, %v, want ready empty item %d", third, err, readyEmpty.ID)
	}
	blockedClaim, err := repo.ClaimWorkItem(ctx, "worker-b", now)
	if err != nil || blockedClaim != nil {
		t.Fatalf("claimed blocked item = %#v, %v", blockedClaim, err)
	}

	if err := repo.CompleteWorkItem(ctx, dependency.ID, first.LeaseEpoch, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("complete dependency: %v", err)
	}
	unblocked, err := repo.ClaimWorkItem(ctx, "worker-b", now)
	if err != nil || unblocked == nil || unblocked.ID != blocked.ID {
		t.Fatalf("claim after dependency done = %#v, %v, want %d", unblocked, err, blocked.ID)
	}
	if !slices.Equal(unblocked.DependsOn, []int64{dependency.ID}) {
		t.Fatalf("unblocked depends_on = %#v", unblocked.DependsOn)
	}

	// A dangling dependency can never be satisfied and must stay blocked.
	orphan := createContractWorkItem(t, repo, agent.CreateWorkItemParams{
		VersionID:      9,
		Kind:           agent.WorkItemKindReviewCases,
		Goal:           "依赖不存在",
		DependsOn:      []int64{blocked.ID + 1_000_000},
		CreatedBy:      agent.WorkItemCreatedByStateMachine,
		IdempotencyKey: "9:review.cases:orphan",
	})
	if item, err := repo.ClaimWorkItem(ctx, "worker-c", now); err != nil || item != nil {
		t.Fatalf("claimed dangling dependency = %#v, %v", item, err)
	}
	if orphan.ID == 0 {
		t.Fatal("orphan item was not created")
	}
}

func TestWorkItemFailRetryUntilMaxAttempts(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewWorkItemRepository(pool, newIDs(t))
	ctx := context.Background()
	now := time.Now().UTC().Add(time.Second)

	item := createContractWorkItem(t, repo, agent.CreateWorkItemParams{
		VersionID:      11,
		Kind:           agent.WorkItemKindAnalyzeFailure,
		Goal:           "归因断言失败",
		CreatedBy:      agent.WorkItemCreatedByTrigger,
		IdempotencyKey: "11:analyze.failure:1",
		MaxAttempts:    2,
	})
	first, err := repo.ClaimWorkItem(ctx, "analyst-1", now)
	if err != nil || first == nil || first.ID != item.ID {
		t.Fatalf("first claim = %#v, %v", first, err)
	}
	if err := repo.FailWorkItem(ctx, item.ID, first.LeaseEpoch, "model temporarily unavailable"); err != nil {
		t.Fatalf("FailWorkItem: %v", err)
	}
	stored, err := repo.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("Get after fail: %v", err)
	}
	if stored.Status != agent.WorkItemFailed || stored.ClaimedBy != nil || stored.LeaseUntil != nil {
		t.Fatalf("failed item = %#v", stored)
	}
	if stored.LastError == nil || *stored.LastError != "model temporarily unavailable" {
		t.Fatalf("failed last_error = %#v", stored.LastError)
	}

	// A failed item with attempts left is retryable.
	second, err := repo.ClaimWorkItem(ctx, "analyst-2", now)
	if err != nil || second == nil || second.ID != item.ID {
		t.Fatalf("retry claim = %#v, %v", second, err)
	}
	if second.Attempts != 2 || second.LeaseEpoch != first.LeaseEpoch+1 {
		t.Fatalf("retry epoch/attempts = %d/%d", second.LeaseEpoch, second.Attempts)
	}
	if err := repo.FailWorkItem(ctx, item.ID, second.LeaseEpoch, "still failing"); err != nil {
		t.Fatalf("second FailWorkItem: %v", err)
	}

	// The attempt budget is exhausted, so the failed item is no longer claimed.
	if exhausted, err := repo.ClaimWorkItem(ctx, "analyst-3", now); err != nil || exhausted != nil {
		t.Fatalf("claim after exhausted budget = %#v, %v", exhausted, err)
	}
	stored, err = repo.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("Get after exhaustion: %v", err)
	}
	if stored.Status != agent.WorkItemFailed || stored.Attempts != 2 || stored.LeaseEpoch != second.LeaseEpoch {
		t.Fatalf("exhausted item = %#v", stored)
	}
}

func TestLeaseEpochFencing(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewWorkItemRepository(pool, newIDs(t))
	ctx := context.Background()
	now := time.Now().UTC().Add(time.Second)

	item := createContractWorkItem(t, repo, agent.CreateWorkItemParams{
		VersionID:      10,
		Kind:           agent.WorkItemKindExecuteBatch,
		Goal:           "执行已确认用例",
		CreatedBy:      agent.WorkItemCreatedByStateMachine,
		IdempotencyKey: "10:execute.batch:1",
	})
	first, err := repo.ClaimWorkItem(ctx, "owner-a", now)
	if err != nil || first == nil || first.ID != item.ID {
		t.Fatalf("first claim = %#v, %v", first, err)
	}
	if first.LeaseEpoch != 1 || first.Attempts != 1 || first.LeaseUntil == nil {
		t.Fatalf("first claim state = %#v", first)
	}

	renewedUntil := now.Add(2 * time.Minute)
	if err := repo.RenewLease(ctx, first.ID, first.LeaseEpoch, renewedUntil); err != nil {
		t.Fatalf("RenewLease: %v", err)
	}

	// The passed now is past the renewed lease, so the item is claimable again.
	reexpiredAt := renewedUntil.Add(time.Second)
	second, err := repo.ClaimWorkItem(ctx, "owner-b", reexpiredAt)
	if err != nil || second == nil || second.ID != item.ID {
		t.Fatalf("re-claim = %#v, %v", second, err)
	}
	if second.LeaseEpoch != first.LeaseEpoch+1 || second.Attempts != 2 {
		t.Fatalf("re-claimed epoch/attempts = %d/%d, want %d/2",
			second.LeaseEpoch, second.Attempts, first.LeaseEpoch+1)
	}
	if second.ClaimedBy == nil || *second.ClaimedBy != "owner-b" {
		t.Fatalf("re-claimed_by = %#v", second.ClaimedBy)
	}

	// Every write carrying the stale epoch is fenced.
	if err := repo.RenewLease(ctx, first.ID, first.LeaseEpoch, reexpiredAt.Add(time.Minute)); !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("stale RenewLease = %v, want ErrLeaseLost", err)
	}
	if err := repo.CompleteWorkItem(ctx, first.ID, first.LeaseEpoch, json.RawMessage(`{}`)); !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("stale CompleteWorkItem = %v, want ErrLeaseLost", err)
	}
	if err := repo.FailWorkItem(ctx, first.ID, first.LeaseEpoch, "stale owner"); !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("stale FailWorkItem = %v, want ErrLeaseLost", err)
	}
	stored, err := repo.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("Get after stale writes: %v", err)
	}
	if stored.Status != agent.WorkItemClaimed || stored.LeaseEpoch != second.LeaseEpoch {
		t.Fatalf("stale writes mutated item = %#v", stored)
	}

	if err := repo.CompleteWorkItem(ctx, item.ID, second.LeaseEpoch, json.RawMessage(`{"passed":3}`)); err != nil {
		t.Fatalf("current CompleteWorkItem: %v", err)
	}
	stored, err = repo.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("Get after complete: %v", err)
	}
	if stored.Status != agent.WorkItemDone {
		t.Fatalf("status = %q, want done", stored.Status)
	}
	assertJSONObjectEqual(t, stored.Result, `{"passed":3}`)
	if err := repo.CompleteWorkItem(ctx, item.ID, first.LeaseEpoch, json.RawMessage(`{}`)); !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("stale CompleteWorkItem after done = %v, want ErrLeaseLost", err)
	}
}
