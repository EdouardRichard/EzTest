package designapp

// v0.2.4-H T10 — E backend polish minors (H-F10, spec §3.6 / §4.1l(c)):
//   - T16: ListJudgments read the same case's latest judgment twice per case
//     (ReviewStateFor + its own read). One read now feeds both.
//   - T6: the override condition value set lives with the Override value object
//     in the domain; the application names are compile-time aliases.
//   - T8: the planner fake treated EVERY failed generation as retry-in-flight,
//     while production distinguishes attempts < maxAttempts. The fake now makes
//     the same distinction, pinned here so the divergence cannot come back.
//   - T19: judgmentFindingSummary skipped minor findings on the assumption that
//     a fail always carries a blocking one. SubmitJudgment only enforces the
//     PASS-side rubric, so a minor-only fail is reachable and used to produce an
//     empty summary (empty rework findings payload).

import (
	"context"
	"strings"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

// countingJudgmentRepo counts the latest-fact reads of one report call.
type countingJudgmentRepo struct {
	JudgmentRepository
	latestReads map[int64]int
}

func (r *countingJudgmentRepo) LatestJudgment(ctx context.Context, caseID int64) (*design.Judgment, error) {
	if r.latestReads == nil {
		r.latestReads = map[int64]int{}
	}
	r.latestReads[caseID]++
	return r.JudgmentRepository.LatestJudgment(ctx, caseID)
}

// TestListJudgmentsReadsLatestJudgmentOncePerCase pins the N+1 fix: the report
// derives review_state and fills the entry from ONE latest-fact read per case.
func TestListJudgmentsReadsLatestJudgmentOncePerCase(t *testing.T) {
	f := newJudgmentFixture()
	f.seedCase(7001, 5, 10, strings.Repeat("a", 64))
	f.seedJudgment(1, 7001, 5, 1, design.VerdictPass)
	f.seedCase(7003, 8, 10, strings.Repeat("b", 64))
	f.seedJudgment(2, 7003, 7, 1, design.VerdictFail) // stale binding
	f.seedCase(7005, 9, 10, strings.Repeat("c", 64))  // never judged

	repo := &countingJudgmentRepo{JudgmentRepository: f.repo}
	graphs := judgmentGraphReader{graph: judgmentTestGraph(10,
		judgmentTestCase(7001, 5), judgmentTestCase(7003, 8), judgmentTestCase(7005, 9),
	)}
	svc := NewJudgmentService(repo, f.cases, &fakeIDs{}, newFixedClock(), f.events, f.settings).SetGraphReader(graphs)
	view, err := svc.ListJudgments(context.Background(), 1001, nil)
	if err != nil {
		t.Fatalf("ListJudgments: %v", err)
	}
	if len(view.Cases) != 3 {
		t.Fatalf("cases = %d, want 3", len(view.Cases))
	}
	for _, caseID := range []int64{7001, 7003, 7005} {
		if got := repo.latestReads[caseID]; got != 1 {
			t.Fatalf("case %d latest-fact reads = %d, want exactly 1 per case", caseID, got)
		}
	}
	// The single read must not change the projection: the stale verdict still
	// derives pending and never leaks into the entry.
	for _, item := range view.Cases {
		switch item.CaseID {
		case 7001:
			if item.ReviewState != design.ReviewStatePass || item.Judgment == nil {
				t.Fatalf("case 7001 = %+v, want pass with its judgment", item)
			}
		default:
			if item.ReviewState != design.ReviewStatePending || item.Judgment != nil {
				t.Fatalf("case %d = %+v, want pending with no current-version judgment", item.CaseID, item)
			}
		}
	}
}

// TestOverrideConditionAliasesMatchDomain pins that the application names are
// aliases of the domain authority, not a second copy of the value set.
func TestOverrideConditionAliasesMatchDomain(t *testing.T) {
	if OverrideConditionFailExhausted != design.OverrideConditionFailExhausted ||
		OverrideConditionJudgeExhausted != design.OverrideConditionJudgeExhausted {
		t.Fatal("the application override conditions must alias the domain authority")
	}
	for _, value := range design.OverrideConditions() {
		if !ValidOverrideCondition(value) || !design.ValidOverrideCondition(value) {
			t.Fatalf("condition %q must be valid through both the alias and the authority", value)
		}
	}
	if ValidOverrideCondition("not-a-condition") || design.ValidOverrideCondition("not-a-condition") {
		t.Fatal("an unknown condition must be rejected by both")
	}
}

// TestOptionalIDConventionLivesInTheDomain pins review M5: the zero-id "absent"
// convention is a property of the domain value objects, so the mapping lives
// there — the store no longer imports the application layer for a four-line
// utility, and neither layer keeps its own copy.
func TestOptionalIDConventionLivesInTheDomain(t *testing.T) {
	for _, value := range []int64{0, -1} {
		if design.OptionalID(value) != nil {
			t.Fatalf("OptionalID(%d) must be nil (absent)", value)
		}
		if asInt64Ptr(value) != nil {
			t.Fatalf("the application projection of OptionalID(%d) must be nil", value)
		}
	}
	got := design.OptionalID(42)
	if got == nil || *got != 42 {
		t.Fatalf("OptionalID(42) = %v, want a pointer to 42", got)
	}
	if projected := asInt64Ptr(42); projected == nil || *projected != 42 {
		t.Fatalf("asInt64Ptr(42) = %v, want the domain mapping", projected)
	}
}

// TestJudgmentBoundToIsTheSinglePredicate pins the shared version-binding
// predicate the review-state derivation, the override gate, the report and the
// scheduler all consume.
func TestJudgmentBoundToIsTheSinglePredicate(t *testing.T) {
	if design.JudgmentBoundTo(nil, 5) {
		t.Fatal("no fact is never bound to a version")
	}
	if design.JudgmentBoundTo(&design.Judgment{CaseVersionID: 4}, 5) {
		t.Fatal("a fact bound to a superseded version is not a fact about the current one")
	}
	if !design.JudgmentBoundTo(&design.Judgment{CaseVersionID: 5}, 5) {
		t.Fatal("a fact bound to the current version must match")
	}
}

// TestFakeEnqueuerFailureIsAttemptsAware pins the planner fake's fidelity to the
// production terminal predicate: a retry-backed failure still blocks a new
// generation, a failure at the attempt cap is a spent generation.
func TestFakeEnqueuerFailureIsAttemptsAware(t *testing.T) {
	ctx := context.Background()
	authorTask := func(dedupe string) TaskInput {
		return TaskInput{
			VersionID: 1001, Team: TeamDesign, Type: TaskTypeDesignRequirement, DedupeKey: dedupe,
			Payload: map[string]any{"requirement_id": "10", "requirement_version_id": "100"},
		}
	}
	t.Run("retryable failure blocks the next coverage repair", func(t *testing.T) {
		_, _, enqueuer, _, _ := newJudgeSchedulerFixture()
		enqueuer.seedDone(authorTask("design:10:100"))
		enqueuer.statuses["design:10:100"] = "failed" // attempts 0 < cap
		created, err := enqueuer.EnsureCoverageRepair(ctx, authorTask("design:10:100"), 100)
		if err != nil || created {
			t.Fatalf("retryable failure created=%v err=%v, want no new generation", created, err)
		}
	})
	t.Run("failure at the cap is a spent coverage generation", func(t *testing.T) {
		_, _, enqueuer, _, _ := newJudgeSchedulerFixture()
		enqueuer.seedFailedAtCap(authorTask("design:10:100"))
		created, err := enqueuer.EnsureCoverageRepair(ctx, authorTask("design:10:100"), 100)
		if err != nil || !created {
			t.Fatalf("exhausted failure created=%v err=%v, want the next generation", created, err)
		}
		if got := enqueuer.byType(TaskTypeDesignRequirement)[1].DedupeKey; got != "design:10:100:repair:1" {
			t.Fatalf("repair key = %q, want design:10:100:repair:1", got)
		}
	})
	t.Run("failure at the cap is a spent judge generation", func(t *testing.T) {
		_, _, enqueuer, _, _ := newJudgeSchedulerFixture()
		in := judgeGenerationInput()
		in.Payload["draft_case_version_ids"] = "5"
		enqueuer.seedFailedAtCap(in)
		drafts := judgeRefs([2]int64{7001, 5})
		created, err := enqueuer.EnsureJudgeGeneration(ctx, judgeGenerationInput(), drafts, drafts)
		if err != nil || !created {
			t.Fatalf("exhausted judge failure created=%v err=%v, want the next generation", created, err)
		}
		if got := enqueuer.byType(TaskTypeDesignJudge)[1].DedupeKey; got != "judge:10:100:judge:1" {
			t.Fatalf("judge key = %q, want judge:10:100:judge:1", got)
		}
	})
}

// TestJudgmentFindingSummaryQuotesMinorOnlyFail pins that a fail carrying only
// minor findings still states its reasons (the rework payload and the 409
// message must not be empty), while blocking findings keep priority and the
// quote stays bounded.
func TestJudgmentFindingSummaryQuotesMinorOnlyFail(t *testing.T) {
	minorOnly := []design.JudgmentFinding{
		{Dimension: "coverage", Severity: design.SeverityMinor, Message: "断言可更精确"},
	}
	if got := judgmentFindingSummary(minorOnly); got != "coverage: 断言可更精确" {
		t.Fatalf("minor-only summary = %q, want the minor finding quoted", got)
	}
	mixed := []design.JudgmentFinding{
		{Dimension: "coverage", Severity: design.SeverityMinor, Message: "次要"},
		{Dimension: "executable", Severity: design.SeverityBlocker, Message: "步骤不可执行"},
	}
	if got := judgmentFindingSummary(mixed); got != "executable: 步骤不可执行" {
		t.Fatalf("mixed summary = %q, want the blocking finding only", got)
	}
	many := []design.JudgmentFinding{
		{Dimension: "d1", Severity: design.SeverityBlocker, Message: "m1"},
		{Dimension: "d2", Severity: design.SeverityBlocker, Message: "m2"},
		{Dimension: "d3", Severity: design.SeverityImportant, Message: "m3"},
		{Dimension: "d4", Severity: design.SeverityBlocker, Message: "m4"},
	}
	if got := judgmentFindingSummary(many); got != "d1: m1; d2: m2; d3: m3; ..." {
		t.Fatalf("bounded summary = %q, want three quotes and an ellipsis", got)
	}
	if got := judgmentFindingSummary(nil); got != "" {
		t.Fatalf("no findings summary = %q, want empty", got)
	}
}
