package agentapp_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// TestBudgetDecide pins the P2-T4 budget policy: completion wins over resource
// budgets, a budget hit triggers settlement, and a settlement that is itself
// over budget is exhausted (hard stop).
func TestBudgetDecide(t *testing.T) {
	defaults := agentapp.Budget{}
	cases := []struct {
		name   string
		budget agentapp.Budget
		usage  agentapp.Usage
		want   agentapp.BudgetDecision
	}{
		{"default under budget", defaults, agentapp.Usage{Tokens: 19999, Seconds: 119, Turns: 23}, agentapp.BudgetContinue},
		{"default tokens at cap", defaults, agentapp.Usage{Tokens: 20000}, agentapp.BudgetSettle},
		{"default tokens over cap", defaults, agentapp.Usage{Tokens: 20001}, agentapp.BudgetSettle},
		{"default run seconds at cap", defaults, agentapp.Usage{Seconds: 120}, agentapp.BudgetSettle},
		{"default turns fuse", defaults, agentapp.Usage{Turns: 24}, agentapp.BudgetSettle},
		{"explicit budget under", agentapp.Budget{MaxTokens: 100, RunSeconds: 10, MaxTurnsFuse: 3},
			agentapp.Usage{Tokens: 99, Seconds: 9, Turns: 2}, agentapp.BudgetContinue},
		{"explicit tokens hit", agentapp.Budget{MaxTokens: 100, RunSeconds: 10, MaxTurnsFuse: 3},
			agentapp.Usage{Tokens: 100}, agentapp.BudgetSettle},
		{"settlement over token budget", agentapp.Budget{MaxTokens: 100, RunSeconds: 10, MaxTurnsFuse: 3},
			agentapp.Usage{Tokens: 101, Settling: true}, agentapp.BudgetExhaust},
		{"settlement within budget continues", agentapp.Budget{MaxTokens: 100, RunSeconds: 10, MaxTurnsFuse: 3},
			agentapp.Usage{Tokens: 101, Settling: true, Completed: true}, agentapp.BudgetSettle},
		{"completion wins over resource exhaustion", defaults,
			agentapp.Usage{Tokens: 1_000_000, Seconds: 100_000, Turns: 1000, Completed: true}, agentapp.BudgetSettle},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.budget.Decide(testCase.usage); got != testCase.want {
				t.Fatalf("Decide(%+v) = %q, want %q", testCase.usage, got, testCase.want)
			}
		})
	}
}

// TestFailureClassification pins the contract §1 failure kinds
// (model_error/tool_failure/budget/aborted/interrupted/context_overflow_unrecoverable)
// and the §5.6 retry/user-visible/action triple.
func TestFailureClassification(t *testing.T) {
	executorUnavailable := platform.NewError(503, "executor_unavailable", "executor is offline")
	executorUnavailable.Retryable = true

	cases := []struct {
		name string
		err  error
		want agentapp.Failure
	}{
		{
			name: "nil is not a failure",
			want: agentapp.Failure{},
		},
		{
			name: "transient model error is retried silently",
			err:  &agentapp.RunError{Reason: "ai_unavailable", Retryable: true},
			want: agentapp.Failure{Kind: agentapp.FailureKindModelError, Retryable: true,
				UserVisible: false, Action: agentapp.FailureActionRetry},
		},
		{
			name: "model capability error needs a config change",
			err:  &agentapp.RunError{Reason: "invalid_payload", Retryable: false},
			want: agentapp.Failure{Kind: agentapp.FailureKindModelError, Retryable: false,
				UserVisible: true, Action: agentapp.FailureActionSwitchModel},
		},
		{
			name: "budget settles partial output",
			err:  &agentapp.RunError{Reason: "budget", Retryable: false},
			want: agentapp.Failure{Kind: agentapp.FailureKindBudget, Retryable: false,
				UserVisible: true, Action: agentapp.FailureActionSettlement},
		},
		{
			name: "executor loss is a tool failure with a recovery action",
			err:  &agentapp.RunError{Reason: "tool_failure", Retryable: true, Cause: executorUnavailable},
			want: agentapp.Failure{Kind: agentapp.FailureKindToolFailure, Retryable: true,
				UserVisible: true, Action: agentapp.FailureActionExecutorRecovery},
		},
		{
			name: "tool failure retries or switches strategy",
			err:  &agentapp.RunError{Reason: "tool_failure", Retryable: true, Cause: errors.New("selector missing")},
			want: agentapp.Failure{Kind: agentapp.FailureKindToolFailure, Retryable: true,
				UserVisible: true, Action: agentapp.FailureActionToolRetry},
		},
		{
			name: "aborted runs are not retried",
			err:  &agentapp.RunError{Reason: "aborted", Retryable: false},
			want: agentapp.Failure{Kind: agentapp.FailureKindAborted, Retryable: false,
				UserVisible: false, Action: agentapp.FailureActionAbort},
		},
		{
			name: "interrupted runs are re-queued by the runtime",
			err:  &agentapp.RunError{Reason: "interrupted", Retryable: false},
			want: agentapp.Failure{Kind: agentapp.FailureKindInterrupted, Retryable: false,
				UserVisible: false, Action: agentapp.FailureActionRequeue},
		},
		{
			name: "lease loss is an interruption, not settled work",
			err:  &agentapp.RunError{Reason: "lease_lost", Retryable: false},
			want: agentapp.Failure{Kind: agentapp.FailureKindInterrupted, Retryable: false,
				UserVisible: false, Action: agentapp.FailureActionRequeue},
		},
		{
			name: "context overflow needs compaction",
			err:  &agentapp.RunError{Reason: "context_overflow_unrecoverable", Retryable: false},
			want: agentapp.Failure{Kind: agentapp.FailureKindContextOverflowUnrecoverable, Retryable: false,
				UserVisible: true, Action: agentapp.FailureActionCompactContext},
		},
		{
			name: "wrapped run errors keep their classification",
			err:  fmt.Errorf("run: %w", &agentapp.RunError{Reason: "budget", Retryable: false}),
			want: agentapp.Failure{Kind: agentapp.FailureKindBudget, Retryable: false,
				UserVisible: true, Action: agentapp.FailureActionSettlement},
		},
		{
			name: "context deadline is the time budget",
			err:  context.DeadlineExceeded,
			want: agentapp.Failure{Kind: agentapp.FailureKindBudget, Retryable: false,
				UserVisible: true, Action: agentapp.FailureActionSettlement},
		},
		{
			name: "context cancel is an abort",
			err:  context.Canceled,
			want: agentapp.Failure{Kind: agentapp.FailureKindAborted, Retryable: false,
				UserVisible: false, Action: agentapp.FailureActionAbort},
		},
		{
			name: "platform lease loss maps to interrupted",
			err:  platform.LeaseLost(),
			want: agentapp.Failure{Kind: agentapp.FailureKindInterrupted, Retryable: false,
				UserVisible: false, Action: agentapp.FailureActionRequeue},
		},
		{
			name: "platform ai_unavailable is a transient model error",
			err:  platform.AIUnavailable("gateway unreachable"),
			want: agentapp.Failure{Kind: agentapp.FailureKindModelError, Retryable: true,
				UserVisible: false, Action: agentapp.FailureActionRetry},
		},
		{
			name: "platform validation error is a visible model capability problem",
			err:  platform.Invalid("model", "does not support tools"),
			want: agentapp.Failure{Kind: agentapp.FailureKindModelError, Retryable: false,
				UserVisible: true, Action: agentapp.FailureActionSwitchModel},
		},
		{
			name: "platform temporary unavailability is a tool failure",
			err:  platform.Unavailable("database is restarting"),
			want: agentapp.Failure{Kind: agentapp.FailureKindToolFailure, Retryable: true,
				UserVisible: true, Action: agentapp.FailureActionToolRetry},
		},
		{
			name: "unknown infrastructure errors are retryable and visible",
			err:  errors.New("connection reset"),
			want: agentapp.Failure{Kind: agentapp.FailureKindToolFailure, Retryable: true,
				UserVisible: true, Action: agentapp.FailureActionRetryOrEscalate},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := agentapp.Classify(testCase.err); got != testCase.want {
				t.Fatalf("Classify(%v) = %+v, want %+v", testCase.err, got, testCase.want)
			}
		})
	}
}

// TestFailedAttemptsRetryable pins R41: a failed 0.2.1 work item with attempts
// below max_attempts stays retryable even though WorkItemStatus.Terminal()
// reports failed as terminal. Retryability must come from attempts, never from
// Terminal().
func TestFailedAttemptsRetryable(t *testing.T) {
	repo, ctx := newContractRepo(t)
	now := time.Now().UTC().Add(time.Second)

	item := createContractWorkItem(t, repo, ctx, 2)
	first, err := repo.ClaimWorkItem(ctx, "worker-1", now)
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
	if stored.Status != agent.WorkItemFailed {
		t.Fatalf("status after fail = %q, want failed", stored.Status)
	}
	if !stored.Status.Terminal() {
		t.Fatalf("precondition: failed status should still report Terminal() so a Terminal()-based retry policy would wrongly settle it")
	}

	retried, err := repo.ClaimWorkItem(ctx, "worker-2", now.Add(time.Second))
	if err != nil {
		t.Fatalf("retry claim: %v", err)
	}
	if retried == nil || retried.ID != item.ID {
		t.Fatalf("failed item with attempts %d/%d was not retryable: %#v", stored.Attempts, stored.MaxAttempts, retried)
	}
	if retried.Attempts != 2 {
		t.Fatalf("retry attempts = %d, want 2", retried.Attempts)
	}

	if err := repo.FailWorkItem(ctx, item.ID, retried.LeaseEpoch, "still unavailable"); err != nil {
		t.Fatalf("second FailWorkItem: %v", err)
	}
	if capped, err := repo.ClaimWorkItem(ctx, "worker-3", now.Add(2*time.Second)); err != nil || capped != nil {
		t.Fatalf("claim at the attempt cap = %#v, %v; the item must stay settled", capped, err)
	}
}

// TestExpiredLeaseCapped pins R41's lease-expiry cap: an expired claimed/running
// row may be taken over only while attempts < max_attempts. At the cap the row
// becomes failed (0.2.1 has no dead state) and is never claimed again.
func TestExpiredLeaseCapped(t *testing.T) {
	repo, ctx := newContractRepo(t)
	now := time.Now().UTC().Add(time.Second)

	item := createContractWorkItem(t, repo, ctx, 2)
	first, err := repo.ClaimWorkItem(ctx, "owner-1", now)
	if err != nil || first == nil || first.ID != item.ID {
		t.Fatalf("first claim = %#v, %v", first, err)
	}
	// The lease is 5 minutes; two minutes later it has expired but attempts
	// (1) is still below max_attempts (2), so the takeover is legal.
	second, err := repo.ClaimWorkItem(ctx, "owner-2", now.Add(6*time.Minute))
	if err != nil {
		t.Fatalf("expired-lease takeover: %v", err)
	}
	if second == nil || second.ID != item.ID {
		t.Fatalf("expired lease at attempts 1/2 was not taken over: %#v", second)
	}
	if second.Attempts != 2 || second.LeaseEpoch != first.LeaseEpoch+1 {
		t.Fatalf("takeover attempts/epoch = %d/%d, want 2/%d", second.Attempts, second.LeaseEpoch, first.LeaseEpoch+1)
	}

	// The second lease also expires. attempts is now at max_attempts, so the
	// item must be settled instead of taken over a third time.
	third, err := repo.ClaimWorkItem(ctx, "owner-3", now.Add(12*time.Minute))
	if err != nil {
		t.Fatalf("claim after lease cap: %v", err)
	}
	if third != nil {
		t.Fatalf("expired lease at the attempt cap was taken over: %#v (attempts would exceed max_attempts)", third)
	}
	stored, err := repo.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("Get after cap: %v", err)
	}
	if stored.Attempts != 2 {
		t.Fatalf("attempts after cap = %d, want 2", stored.Attempts)
	}
	if stored.Status != agent.WorkItemFailed {
		t.Fatalf("status after capped lease expiry = %q, want failed (the 0.2.1 terminal state)", stored.Status)
	}
	if stored.ClaimedBy != nil || stored.LeaseUntil != nil {
		t.Fatalf("capped item still holds a lease: claimed_by=%v lease_until=%v", stored.ClaimedBy, stored.LeaseUntil)
	}

	if again, err := repo.ClaimWorkItem(ctx, "owner-4", now.Add(20*time.Minute)); err != nil || again != nil {
		t.Fatalf("capped item was claimed again: %#v, %v", again, err)
	}
}

// ---------- helpers ----------

func newContractRepo(t *testing.T) (*agentpg.WorkItemRepository, context.Context) {
	t.Helper()
	pool := testutil.NewDB(t)
	ids, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	return agentpg.NewWorkItemRepository(pool, ids), context.Background()
}

func createContractWorkItem(t *testing.T, repo *agentpg.WorkItemRepository, ctx context.Context, maxAttempts int) agent.WorkItem {
	t.Helper()
	item, err := repo.CreateWorkItem(ctx, agent.CreateWorkItemParams{
		VersionID:      42,
		Kind:           agent.WorkItemKindAnalyzeFailure,
		Goal:           "classify an assertion failure",
		CreatedBy:      agent.WorkItemCreatedByTrigger,
		IdempotencyKey: fmt.Sprintf("42:analyze.failure:%d", time.Now().UnixNano()),
		MaxAttempts:    maxAttempts,
	})
	if err != nil {
		t.Fatalf("CreateWorkItem: %v", err)
	}
	return item
}
