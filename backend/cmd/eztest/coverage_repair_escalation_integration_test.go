//go:build integration

package main

// v0.2.4-H T11b (H-F5b, spec SC-E4 "无永久 pending"): the real-DB legs of the
// case-AUTHORING generation budget escalation.
//
// Before this fix, coverageRepairDedupeKey conflated two very different answers
// into "" — "a generation still owes work" and "the bounded authoring budget is
// spent" — and EnsureCoverageRepair returned (false, nil) for both. The second
// case is a STRANDED requirement: coverage stays incomplete, no further authoring
// generation can ever be created, and nothing was written that a human (or an
// operator) could see. Same family as DEV-E2/E3/E4 and H-F5.
//
// The invariant mirrors DEV-E3 / T10's "in flight, otherwise escalate":
//
//	① budget spent, every generation terminal -> an author_exhausted escalation
//	   FACT is appended (durable, readable through the version facts read model)
//	   and the pass is attributable; a second pass does not duplicate it.
//	② a generation still in flight (open / retryable) -> NO escalation. Escalating
//	   a merely queued author would be a false degradation (T10 review I1).
//	③ the requirement is NOT permanently stuck: a new requirement revision starts
//	   a fresh authoring budget, so the human path (revise the requirement, or
//	   author/confirm cases by hand) still advances it.
//
// Run: cd backend && go test -tags=integration -p 1 ./cmd/eztest/ \
//        -run 'TestCoverageRepairBudget' -count=1 -v

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versioninfra "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// coverageRepairFixture is one version with a real queue, the real targeted
// design.requirement source and the real version FactService behind the
// escalation store.
type coverageRepairFixture struct {
	t         *testing.T
	pool      *pgxpool.Pool
	ids       platformid.Generator
	queue     *agentapp.WorkQueue
	facts     *versionapp.FactService
	versionID int64
	enqueuer  designTaskEnqueuer
}

func newCoverageRepairFixture(t *testing.T) *coverageRepairFixture {
	t.Helper()
	pool := testutil.NewDB(t)
	ids, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	facts := versionapp.NewFactService(versionpg.NewFactRepository(pool, ids), ids, nil)
	queue := agentapp.NewWorkQueue(agentpg.NewWorkItemRepository(pool, ids), clock.System{}, agentapp.DefaultLease, agentapp.DefaultMaxAttempts)
	versionID := ids.Next()
	// The escalation fact is a VERSION fact: FactService validates the version
	// exists, so the fixture seeds a real version row (and its bound environment).
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO environment (id, name, status) VALUES (991002, 'coverage repair fixture env', 'active')`); err != nil {
		t.Fatalf("seed fixture environment: %v", err)
	}
	now := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	if err := versioninfra.New(pool, ids).Create(ctx, versiondomain.Version{
		ID: versionID, Title: "Coverage repair version", Status: versiondomain.StatusInitial,
		CreatedBy: 42, CreatedAt: now, UpdatedAt: now, EnvironmentID: 991002,
	}, nil); err != nil {
		t.Fatalf("create fixture version: %v", err)
	}
	return &coverageRepairFixture{
		t: t, pool: pool, ids: ids, queue: queue, facts: facts, versionID: versionID,
		enqueuer: designTaskEnqueuer{
			queue:           queue,
			coverageRepairs: &authorBatchSQLSource{pool: pool},
			authorExhausted: &authorExhaustedFactStore{facts: facts},
		},
	}
}

func (f *coverageRepairFixture) ctx() context.Context { return context.Background() }

// seedAuthorGeneration enqueues one design.requirement generation of the
// requirement version through the real queue with the production payload shape.
func (f *coverageRepairFixture) seedAuthorGeneration(reqID, reqVerID int64, key string) agent.WorkItem {
	f.t.Helper()
	item, created, err := f.queue.Ensure(f.ctx(), agent.NewWorkItem{
		VersionID: f.versionID,
		Team:      string(agent.TeamDesign),
		Type:      designapp.TaskTypeDesignRequirement,
		DedupeKey: key,
		Payload: map[string]any{
			"requirement_id":         platformid.Format(reqID),
			"requirement_version_id": platformid.Format(reqVerID),
			"reason":                 "ac_gap",
		},
	})
	if err != nil || !created {
		f.t.Fatalf("seed authoring generation %q: created=%v err=%v", key, created, err)
	}
	return item
}

// spendAuthorBudget seeds and settles maxDesignRequirementGenerations terminal
// authoring generations of the requirement version.
func (f *coverageRepairFixture) spendAuthorBudget(reqID, reqVerID int64) {
	f.t.Helper()
	f.spendAuthorGenerations(reqID, reqVerID, maxDesignRequirementGenerations)
}

// spendAuthorGenerations seeds and settles n terminal authoring generations of
// the requirement version through the real queue (production payload + repair-key
// shape). n may exceed the budget so a leg can prove the escalation records the
// ACTUAL count, not the budget constant (T11c M4).
func (f *coverageRepairFixture) spendAuthorGenerations(reqID, reqVerID int64, n int) {
	f.t.Helper()
	baseKey := fmt.Sprintf("design:%d:%d", reqID, reqVerID)
	for i := 0; i < n; i++ {
		key := baseKey
		if i > 0 {
			key = fmt.Sprintf("%s:repair:%d", baseKey, i)
		}
		item := f.seedAuthorGeneration(reqID, reqVerID, key)
		if err := poolExecDone(f.ctx(), f.pool, item.ID); err != nil {
			f.t.Fatalf("settle authoring generation %d: %v", i, err)
		}
	}
}

func (f *coverageRepairFixture) ensureRepair(reqID, reqVerID int64) (bool, error) {
	f.t.Helper()
	return f.ensureRepairPayload(reqID, reqVerID, map[string]any{
		"requirement_id":         platformid.Format(reqID),
		"requirement_version_id": platformid.Format(reqVerID),
		"reason":                 "ac_gap",
	})
}

// ensureRepairPayload runs EnsureCoverageRepair with a caller-supplied payload so
// a leg can model a malformed task input (e.g. a missing requirement_id, T11c M3).
func (f *coverageRepairFixture) ensureRepairPayload(reqID, reqVerID int64, payload map[string]any) (bool, error) {
	f.t.Helper()
	return f.enqueuer.EnsureCoverageRepair(f.ctx(), designapp.TaskInput{
		VersionID: f.versionID,
		Team:      string(agent.TeamDesign),
		Type:      designapp.TaskTypeDesignRequirement,
		DedupeKey: fmt.Sprintf("design:%d:%d", reqID, reqVerID),
		Payload:   payload,
	}, reqVerID)
}

// authorExhaustedFacts reads the CURRENT escalation facts of the version through
// the production fact read model (not a hand-written projection).
func (f *coverageRepairFixture) authorExhaustedFacts() []map[string]any {
	f.t.Helper()
	rows, err := f.facts.Current(f.ctx(), f.versionID, authorExhaustedFactKind)
	if err != nil {
		f.t.Fatalf("read author exhaustion facts: %v", err)
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Payload)
	}
	return out
}

// TestCoverageRepairBudgetExhaustedEscalates is leg ①: a spent authoring budget
// must write a durable, attributable escalation instead of silently returning.
func TestCoverageRepairBudgetExhaustedEscalates(t *testing.T) {
	f := newCoverageRepairFixture(t)
	const reqID, reqVerID = int64(3101), int64(3102)
	f.spendAuthorBudget(reqID, reqVerID)

	created, err := f.ensureRepair(reqID, reqVerID)
	if err != nil {
		t.Fatalf("EnsureCoverageRepair: %v", err)
	}
	if created {
		t.Fatal("the bounded authoring budget must not create a fourth generation")
	}
	facts := f.authorExhaustedFacts()
	if len(facts) != 1 {
		t.Fatalf("authoring budget spent with incomplete coverage: %d escalation facts, want 1 "+
			"(a stranded requirement must be escalated, not silently dropped)", len(facts))
	}
	if got := facts[0]["requirement_id"]; got != platformid.Format(reqID) {
		t.Errorf("escalation requirement_id = %v, want %s", got, platformid.Format(reqID))
	}
	if got := facts[0]["requirement_version_id"]; got != platformid.Format(reqVerID) {
		t.Errorf("escalation requirement_version_id = %v, want %s", got, platformid.Format(reqVerID))
	}
	if got := fmt.Sprint(facts[0]["generations"]); got != fmt.Sprint(maxDesignRequirementGenerations) {
		t.Errorf("escalation generations = %v, want %d (the spent budget is the reason)", got, maxDesignRequirementGenerations)
	}

	// Idempotent across reconcile passes: the condition persists, the fact does not
	// duplicate (FactService dedupe on version+kind+key+content_hash).
	again, err := f.ensureRepair(reqID, reqVerID)
	if err != nil || again {
		t.Fatalf("second pass created=%v err=%v, want no new generation", again, err)
	}
	if facts := f.authorExhaustedFacts(); len(facts) != 1 {
		t.Fatalf("second pass produced %d escalation facts, want the same single fact", len(facts))
	}
}

// TestCoverageRepairInFlightNeverEscalates is leg ②, the discriminating one: an
// authoring generation that still owes work defers the repair WITHOUT stamping an
// escalation over a requirement whose author simply has not finished (T10 I1).
func TestCoverageRepairInFlightNeverEscalates(t *testing.T) {
	f := newCoverageRepairFixture(t)
	const reqID, reqVerID = int64(3201), int64(3202)
	f.spendAuthorBudget(reqID, reqVerID)
	// One MORE generation is still open. The budget is spent AND work is owed: the
	// in-flight answer wins, exactly as the pre-fix predicate ordered it.
	f.seedAuthorGeneration(reqID, reqVerID, fmt.Sprintf("design:%d:%d:repair:%d", reqID, reqVerID, maxDesignRequirementGenerations))

	created, err := f.ensureRepair(reqID, reqVerID)
	if err != nil || created {
		t.Fatalf("in-flight pass created=%v err=%v, want no duplicate generation", created, err)
	}
	if facts := f.authorExhaustedFacts(); len(facts) != 0 {
		t.Fatalf("an in-flight authoring generation was escalated (%d facts); a queued/running author is a normal wait, not a degradation", len(facts))
	}
}

// TestCoverageRepairEscalationIsNotPermanent is leg ③: the escalation is not a
// dead end. A new requirement revision (the human edits the requirement) starts a
// fresh authoring budget, so the requirement can still be repaired automatically;
// the old revision's escalation fact stays as append-only history.
func TestCoverageRepairEscalationIsNotPermanent(t *testing.T) {
	f := newCoverageRepairFixture(t)
	const reqID, reqVerID, nextReqVerID = int64(3301), int64(3302), int64(3303)
	f.spendAuthorBudget(reqID, reqVerID)
	if _, err := f.ensureRepair(reqID, reqVerID); err != nil {
		t.Fatalf("escalating pass: %v", err)
	}
	if len(f.authorExhaustedFacts()) != 1 {
		t.Fatal("fixture precondition broken: the first revision must be escalated")
	}

	created, err := f.ensureRepair(reqID, nextReqVerID)
	if err != nil {
		t.Fatalf("new revision pass: %v", err)
	}
	if !created {
		t.Fatal("a new requirement revision must start a fresh authoring budget; the requirement would be permanently stuck")
	}
	var key string
	if err := f.pool.QueryRow(f.ctx(),
		`SELECT dedupe_key FROM work_item WHERE version_id = $1 AND type = $2 AND status = 'open'`,
		f.versionID, designapp.TaskTypeDesignRequirement).Scan(&key); err != nil {
		t.Fatalf("read the created generation: %v", err)
	}
	if want := fmt.Sprintf("design:%d:%d", reqID, nextReqVerID); key != want {
		t.Fatalf("new revision generation key = %q, want %q", key, want)
	}
}

// TestCoverageRepairEscalationStoreUnwiredFailsClosed pins constitution VII.6: an
// assembly without the escalation store must report the gap, never answer "no
// escalation needed" silently.
func TestCoverageRepairEscalationStoreUnwiredFailsClosed(t *testing.T) {
	f := newCoverageRepairFixture(t)
	f.enqueuer.authorExhausted = nil
	const reqID, reqVerID = int64(3401), int64(3402)
	f.spendAuthorBudget(reqID, reqVerID)
	if _, err := f.ensureRepair(reqID, reqVerID); err == nil {
		t.Fatal("an unwired author-exhaustion store returned nil; the stranded requirement would be silent again")
	}
}

// TestCoverageRepairEscalationMissingRequirementIDFailsClosed is T11c M3: the
// escalation fact is keyed requirement:<id>:requirement_version:<id>. A task
// payload without a positive requirement_id must fail closed at the parse
// boundary (platform.Invalid, the same rule as #34) instead of persisting a
// requirement:0:… record that misattributes the stall and would dedupe a second
// id-less escalation sharing the revision key into silence. Both production
// callers always carry the id, so this is unreachable today — but this is the one
// newly-written durable record path, so it is pinned fail-closed.
func TestCoverageRepairEscalationMissingRequirementIDFailsClosed(t *testing.T) {
	f := newCoverageRepairFixture(t)
	const reqID, reqVerID = int64(3501), int64(3502)
	f.spendAuthorBudget(reqID, reqVerID)

	_, err := f.ensureRepairPayload(reqID, reqVerID, map[string]any{
		"requirement_version_id": platformid.Format(reqVerID),
		"reason":                 "ac_gap",
	})
	apiErr, ok := err.(*platform.Error)
	if !ok || apiErr.Status != 400 || apiErr.Code != "invalid_argument" {
		t.Fatalf("missing requirement_id escalation error = %v, want platform.Invalid 400 invalid_argument", err)
	}
	if apiErr.Details["field"] != "requirement_id" {
		t.Fatalf("error field = %v, want requirement_id", apiErr.Details["field"])
	}
	if facts := f.authorExhaustedFacts(); len(facts) != 0 {
		t.Fatalf("a zero-id escalation persisted %d facts; the invalid attribution must fail closed, not write requirement:0:…", len(facts))
	}
}

// TestCoverageRepairEscalationRecordsActualGenerationCount is T11c M4: the
// escalation is an attribution record and must state how many generations
// ACTUALLY ran, not the budget constant. Seed one MORE terminal generation than
// the budget (a row an older build or a race could leave behind); the fact must
// report the actual count. With the constant hardcoded this reports the budget
// instead and the leg fails (the discriminating bad sample).
func TestCoverageRepairEscalationRecordsActualGenerationCount(t *testing.T) {
	f := newCoverageRepairFixture(t)
	const reqID, reqVerID = int64(3601), int64(3602)
	actual := maxDesignRequirementGenerations + 1
	f.spendAuthorGenerations(reqID, reqVerID, actual)

	if _, err := f.ensureRepair(reqID, reqVerID); err != nil {
		t.Fatalf("EnsureCoverageRepair: %v", err)
	}
	facts := f.authorExhaustedFacts()
	if len(facts) != 1 {
		t.Fatalf("spent budget: %d escalation facts, want 1", len(facts))
	}
	if got := fmt.Sprint(facts[0]["generations"]); got != fmt.Sprint(actual) {
		t.Fatalf("escalation generations = %v, want the ACTUAL terminal count %d (not the budget constant %d)",
			got, actual, maxDesignRequirementGenerations)
	}
}

// plannerLegAuthorExhaustedFacts reads the version's CURRENT author_exhausted
// facts through the production fact read model (FactService over the harness's
// real pool), not a hand-written projection.
func plannerLegAuthorExhaustedFacts(t *testing.T, h *judgeLoopHarness) []map[string]any {
	t.Helper()
	facts := versionapp.NewFactService(versionpg.NewFactRepository(h.pool, h.ids), h.ids, nil)
	rows, err := facts.Current(h.ctx(), h.versionID, authorExhaustedFactKind)
	if err != nil {
		t.Fatalf("read author exhaustion facts: %v", err)
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Payload)
	}
	return out
}

// TestCoverageRepairExhaustionThroughPlannerReconcile is T11c M5: the four legs
// above call enqueuer.EnsureCoverageRepair DIRECTLY, so the production chain
// "last generation settles -> real Planner.Reconcile -> ensureCriticTasks ->
// ensureJudgeTasks -> ensureACGapRepairTasks -> EnsureCoverageRepair -> fact" was
// only argued reachable, never driven. This leg drives the WHOLE chain through the
// real reconcile entry against the real design graph / queue / fact store, pinning
// SC-E4's "no permanent pending" at the real entry rather than a direct port call.
//
// The requirement has a PARTIAL draft coverage (AC-1 of AC-1+AC-2), so
// NeedsRedesign is false (drafts exist, none stale) and ONLY the ac_gap clause of
// NeedsACRepair fires — isolating ensureACGapRepairTasks as the repair entry (the
// zero-coverage redesign path stays out of it).
func TestCoverageRepairExhaustionThroughPlannerReconcile(t *testing.T) {
	h := newJudgeLoopHarness(t)
	reqID, reqVerID := h.setupDesigningRequirement(2) // AC-1 + AC-2
	h.setAuthorMode("partial")                        // every authoring pass covers only AC-1
	h.setJudgeMode("pass")

	// Generation 1: a real author run leaves the AC-2 coverage gap.
	h.requireRunDone(h.runWorkItems(designapp.TaskTypeDesignRequirement), 1, "author partial")

	// Generations 2..budget: each real reconcile pass enqueues exactly one ac_gap
	// repair; settle it through the real claim -> MarkRunning -> Complete write-back
	// WITHOUT another author run (the author keeps producing nothing new), so the
	// gap persists and the bounded budget is consumed.
	for i := 1; i < maxDesignRequirementGenerations; i++ {
		h.reconcile()
		var open *agent.WorkItem
		for _, item := range h.workItemsOfType(designapp.TaskTypeDesignRequirement) {
			if item.Status == agent.WorkItemOpen {
				it := item
				open = &it
			}
		}
		if open == nil {
			t.Fatalf("reconcile pass %d: no open ac_gap repair generation was enqueued", i)
		}
		var payload struct {
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(open.Payload, &payload); err != nil || payload.Reason != "ac_gap" {
			t.Fatalf("repair generation reason = %q (err %v), want ac_gap (the partial-coverage gap entry)", payload.Reason, err)
		}
		claimed, ok, err := h.queue.ClaimForTypes(h.ctx(), "coverage-repair-planner-leg", []string{designapp.TaskTypeDesignRequirement}, 0)
		if err != nil || !ok || claimed.ID != open.ID {
			t.Fatalf("claim the repair generation: ok=%v id=%d want=%d err=%v", ok, claimed.ID, open.ID, err)
		}
		if err := h.queue.MarkRunning(h.ctx(), claimed.ID, claimed.LeaseEpoch); err != nil {
			t.Fatalf("mark repair running: %v", err)
		}
		if err := h.queue.Complete(h.ctx(), claimed.ID, claimed.LeaseEpoch); err != nil {
			t.Fatalf("complete repair generation: %v", err)
		}
	}

	// Budget spent, coverage still incomplete: the NEXT real reconcile pass must
	// escalate through the fact store, not strand the requirement silently.
	h.reconcile()
	facts := plannerLegAuthorExhaustedFacts(t, h)
	if len(facts) != 1 {
		t.Fatalf("planner reconcile after a spent authoring budget: %d author_exhausted facts, want 1 "+
			"(the stranded requirement must be escalated at the real reconcile entry)", len(facts))
	}
	if got := facts[0]["requirement_id"]; got != platformid.Format(reqID) {
		t.Errorf("escalation requirement_id = %v, want %s", got, platformid.Format(reqID))
	}
	if got := facts[0]["requirement_version_id"]; got != platformid.Format(reqVerID) {
		t.Errorf("escalation requirement_version_id = %v, want %s", got, platformid.Format(reqVerID))
	}
	if got := fmt.Sprint(facts[0]["generations"]); got != fmt.Sprint(maxDesignRequirementGenerations) {
		t.Errorf("escalation generations = %v, want the actual terminal count %d", got, maxDesignRequirementGenerations)
	}
	// No generation beyond the budget was created.
	if items := h.workItemsOfType(designapp.TaskTypeDesignRequirement); len(items) != maxDesignRequirementGenerations {
		t.Fatalf("design.requirement generations = %d, want exactly the budget %d (no fourth generation)",
			len(items), maxDesignRequirementGenerations)
	}
	// Idempotent across reconcile passes: the condition persists, the fact does not
	// duplicate.
	h.reconcile()
	if again := plannerLegAuthorExhaustedFacts(t, h); len(again) != 1 {
		t.Fatalf("repeat reconcile produced %d author_exhausted facts, want the same single fact", len(again))
	}

	// Not a dead end (SC-E4): a real requirement revision + re-confirm starts a
	// fresh authoring budget, and the next reconcile enqueues a new generation for
	// the new revision — the human path out of the escalation.
	revised := versionapp.RequirementDraft{
		Title:     "登录功能（2 条验收标准）",
		UserStory: versionapp.RequirementUserStoryView{AsA: "注册用户", IWant: "完成登录", SoThat: "使用系统功能"},
		AcceptanceCriteria: []versionapp.RequirementAcceptanceCriterionInput{
			{Given: "已注册账号且前置条件 1 成立", When: "用户执行核心操作 1", Then: "系统返回操作 1 的成功结果"},
			{Given: "已注册账号且前置条件 2 成立", When: "用户执行核心操作 2", Then: "系统返回操作 2 的成功结果（修订补全）"},
		},
		Priority:       "P2",
		SutDescription: "被测系统的登录页面与登录接口，包含账号密码校验与锁定策略。",
	}
	updated, err := h.requirements.Update(h.ctx(), h.versionID, reqID, revised, "人工修订：补全 AC-2", 1)
	if err != nil {
		t.Fatalf("revise the escalated requirement: %v", err)
	}
	if updated.CurrentVersionID == reqVerID {
		t.Fatal("fixture precondition broken: the revision must produce a new requirement_version")
	}
	if _, err := h.requirements.Confirm(h.ctx(), h.versionID, reqID, 1); err != nil {
		t.Fatalf("re-confirm the revised requirement: %v", err)
	}
	h.reconcile()
	wantKey := fmt.Sprintf("design:%d:%d", reqID, updated.CurrentVersionID)
	found := false
	for _, item := range h.workItemsOfType(designapp.TaskTypeDesignRequirement) {
		if item.DedupeKey == wantKey && item.Status == agent.WorkItemOpen {
			found = true
		}
	}
	if !found {
		t.Fatalf("a new requirement revision must start a fresh authoring budget (open generation %q); the requirement would be permanently stuck", wantKey)
	}
}
