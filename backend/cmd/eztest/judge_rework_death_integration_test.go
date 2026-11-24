//go:build integration

package main

// DEV-E3 (spec SC-E4 "无永久 pending", rework side): a design.rework work item
// that retries to exhaustion is TERMINAL but produces no revised draft and no
// new judgment round. The round-derived rework count (max(round)-1 over
// design_judgment) therefore never sees it: the planner keeps asking for the
// same dedupe key rework:<case>:<failedVersion>, Ensure answers created=false
// (it never revives a dead row), and the case strands in review_state reworking
// with both advance edges 409 and the human override rejected.
//
// Two tests pin the fix against the real assembly:
//   - TestJudgeReworkBudgetSQLIntegration: the composition-root terminal-rework
//     read over the real work_item table (attempts-aware, targeted query).
//   - TestJudgeClosedLoopReworkDeathEscalates: the full production loop — real
//     author/judge runs over the fake LLM, a real LLM outage that drives the
//     rework runs to dead through the real retry predicates, then the derived
//     escalation, the reachable human override and the confirmed case.
//
// Run: bash .superpowers/sdd/2026-11-18-eztest-v0.2.4-e-quality-gate/final-fix/with-env.sh \
//        go test -tags=integration -p 1 ./cmd/eztest/ -run 'TestJudgeReworkBudget|TestJudgeClosedLoopReworkDeath' -count=1 -v

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// TestJudgeReworkBudgetSQLIntegration pins the escalation-budget read against
// the real work_item table and the REAL queue transitions: an open rework has
// not spent budget, a rework driven to dead by the real claim/fail/retry path
// has, and the count is attempts-aware (a failed row below its attempt cap is
// still owed work). It also proves the targeted query reads attempts /
// max_attempts at all — without those columns the predicate cannot distinguish
// a retryable failure from an exhausted one.
func TestJudgeReworkBudgetSQLIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	ids, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	versionID := ids.Next()
	const caseID, failedVersion = int64(7001), int64(8001)

	queue := agentapp.NewWorkQueue(agentpg.NewWorkItemRepository(pool, ids), clock.System{}, 30*time.Second, 3)
	reader := &judgeReworkBudgetReader{source: &judgeReworkSQLSource{pool: pool}}

	count := func(label string, want int) {
		t.Helper()
		got, err := reader.TerminalReworkCount(ctx, versionID, caseID)
		if err != nil {
			t.Fatalf("%s: TerminalReworkCount: %v", label, err)
		}
		if got != want {
			t.Fatalf("%s: terminal rework count = %d, want %d", label, got, want)
		}
	}

	count("no rework yet", 0)

	// The production writer's payload shape (handleJudgeFail).
	rework, created, err := queue.Ensure(ctx, agent.NewWorkItem{
		VersionID: versionID,
		Team:      string(agent.TeamDesign),
		Type:      designapp.TaskTypeDesignRework,
		DedupeKey: fmt.Sprintf("rework:%d:%d", caseID, failedVersion),
		Payload: map[string]any{
			"case_id":         platformid.Format(caseID),
			"case_version_id": platformid.Format(failedVersion),
			"round":           2,
		},
	})
	if err != nil || !created {
		t.Fatalf("ensure rework: created=%v err=%v", created, err)
	}
	count("open rework has not spent budget", 0)

	// REAL retry path: claim -> mark running -> retryable fail, until the
	// attempt cap turns the row dead. Only the backoff WAIT is fast-forwarded.
	deadline := 12
	for {
		item, ok, err := queue.ClaimForTypes(ctx, "rework-budget-test", []string{designapp.TaskTypeDesignRework}, 0)
		if err != nil {
			t.Fatalf("claim rework: %v", err)
		}
		if !ok {
			break
		}
		deadline--
		if deadline < 0 {
			t.Fatal("rework never reached its attempt cap")
		}
		if err := queue.MarkRunning(ctx, item.ID, item.LeaseEpoch); err != nil {
			t.Fatalf("mark running: %v", err)
		}
		if err := queue.Fail(ctx, item.ID, item.LeaseEpoch, fmt.Errorf("author run failed"), true); err != nil {
			t.Fatalf("fail rework: %v", err)
		}
		if _, err := pool.Exec(ctx,
			`UPDATE work_item SET available_at = now() WHERE id = $1 AND status = 'open'`, item.ID); err != nil {
			t.Fatalf("fast-forward backoff: %v", err)
		}
	}
	var status string
	var attempts, maxAttempts int
	if err := pool.QueryRow(ctx,
		`SELECT status, attempts, max_attempts FROM work_item WHERE id = $1`, rework.ID).
		Scan(&status, &attempts, &maxAttempts); err != nil {
		t.Fatalf("read rework row: %v", err)
	}
	if status != string(agent.WorkItemDead) || attempts < maxAttempts {
		t.Fatalf("fixture precondition broken: rework status=%s attempts=%d/%d, want dead at the cap",
			status, attempts, maxAttempts)
	}
	count("dead rework spent the budget", 1)

	// Ensure with the SAME dedupe key never revives the dead row (the stranding
	// mechanism): still one item, still dead, still exactly one budget spent.
	again, created, err := queue.Ensure(ctx, agent.NewWorkItem{
		VersionID: versionID,
		Team:      string(agent.TeamDesign),
		Type:      designapp.TaskTypeDesignRework,
		DedupeKey: fmt.Sprintf("rework:%d:%d", caseID, failedVersion),
		Payload:   map[string]any{"case_id": platformid.Format(caseID), "case_version_id": platformid.Format(failedVersion)},
	})
	if err != nil || created || again.ID != rework.ID {
		t.Fatalf("re-ensure: created=%v id=%d err=%v, want the existing dead row", created, again.ID, err)
	}
	count("re-ensure does not change the budget", 1)

	// ATTEMPTS-AWARE: a failed row below its cap is retryable, not terminal.
	// (The type-path queue re-opens retryable failures, so the row is set
	// directly to pin that the SELECT really reads attempts/max_attempts.)
	retryableID := ids.Next()
	if _, err := pool.Exec(ctx, `
		INSERT INTO work_item (id, version_id, team, type, dedupe_key, payload, status, attempts, max_attempts)
		VALUES ($1, $2, 'design', 'design.rework', $3, $4::jsonb, 'failed', 1, 3)
	`, retryableID, versionID, fmt.Sprintf("rework:%d:%d", caseID, failedVersion+1),
		fmt.Sprintf(`{"case_id":"%d","case_version_id":"%d"}`, caseID, failedVersion+1)); err != nil {
		t.Fatalf("insert retryable failed rework: %v", err)
	}
	count("retryable failed rework is still owed", 1)
	if _, err := pool.Exec(ctx, `UPDATE work_item SET attempts = max_attempts WHERE id = $1`, retryableID); err != nil {
		t.Fatalf("exhaust the retryable rework: %v", err)
	}
	count("failed rework at the cap spent the budget", 2)

	// Another case's rework never counts here.
	if _, err := pool.Exec(ctx, `
		INSERT INTO work_item (id, version_id, team, type, dedupe_key, payload, status, attempts, max_attempts)
		VALUES ($1, $2, 'design', 'design.rework', $3, $4::jsonb, 'dead', 3, 3)
	`, ids.Next(), versionID, fmt.Sprintf("rework:%d:%d", caseID+1, failedVersion),
		fmt.Sprintf(`{"case_id":"%d","case_version_id":"%d"}`, caseID+1, failedVersion)); err != nil {
		t.Fatalf("insert other-case rework: %v", err)
	}
	count("another case does not spend this case's budget", 2)

	// Nil pool fails closed (never a silent "no budget spent").
	nilReader := &judgeReworkBudgetReader{source: &judgeReworkSQLSource{}}
	if _, err := nilReader.TerminalReworkCount(ctx, versionID, caseID); err == nil {
		t.Fatal("nil pool must fail closed")
	}
}

// takeLLMDown points the default LLM configuration at an unreachable endpoint
// through the real admin update use case, so subsequent agent runs fail at the
// transport and the work item retries to its attempt cap.
func (h *judgeLoopHarness) takeLLMDown() {
	h.t.Helper()
	baseURL := "http://127.0.0.1:1/v1"
	if _, err := h.llm.UpdateConfig(h.ctx(), h.llmConfigID, llmapp.ConfigInput{BaseURL: &baseURL}, 1); err != nil {
		h.t.Fatalf("take the LLM endpoint down: %v", err)
	}
}

// TestJudgeClosedLoopReworkDeathEscalates is the DEV-E3 reproduction against the
// real assembly. Before the fix the case stranded: review_state stayed
// reworking after the rework died, both advance edges 409'd and the admin
// override was rejected (state_conflict) — a permanent pending. After the fix
// the dead rework spends the escalation budget, the case derives escalated_fail,
// reaches case_review, and the human override + confirm path is reachable.
func TestJudgeClosedLoopReworkDeathEscalates(t *testing.T) {
	h := newJudgeLoopHarness(t)
	reqID, _ := h.setupDesigningRequirement(1)
	h.setAuthorMode("full")
	h.setJudgeMode("fail")

	h.requireRunDone(h.runWorkItems("design.requirement"), 1, "author")
	drafts := h.draftCards()
	if len(drafts) != 2 {
		t.Fatalf("author produced %d drafts, want 2", len(drafts))
	}
	h.reconcile()
	h.requireRunDone(h.runWorkItems("design.judge"), 1, "judge round 1")
	for _, card := range drafts {
		if judgment := h.latestJudgment(card.CaseID); judgment == nil ||
			judgment.Verdict != design.VerdictFail || judgment.Round != 1 {
			t.Fatalf("round-1 judgment = %+v, want fail round 1", judgment)
		}
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStateReworking {
			t.Fatalf("post-fail review state = %s, want reworking", state)
		}
	}
	h.reconcile() // the scheduler enqueues the one allowed rework per case
	reworks := h.workItemsOfType(agentapp.WorkTypeDesignRework)
	if len(reworks) != 2 {
		t.Fatalf("rework items = %d, want 2", len(reworks))
	}

	// The rework runs fail (LLM unreachable) and retry to their attempt cap:
	// every design.rework row ends dead, having consumed budget without
	// producing a revised draft or a new judgment round.
	h.takeLLMDown()
	runs := h.runWorkItems(agentapp.WorkTypeDesignRework)
	if len(runs) == 0 {
		t.Fatal("no rework runs were attempted")
	}
	for _, run := range runs {
		if run.err == nil {
			t.Fatalf("rework run unexpectedly succeeded with the LLM down: %+v", run.result)
		}
	}
	dead := h.workItemsOfType(agentapp.WorkTypeDesignRework)
	if len(dead) != 2 {
		t.Fatalf("rework items after the outage = %d, want 2 (Ensure never revives a dead row)", len(dead))
	}
	for _, item := range dead {
		if item.Status != agent.WorkItemDead {
			t.Fatalf("rework %d status = %s, want dead (attempts %d/%d)", item.ID, item.Status, item.Attempts, item.MaxAttempts)
		}
	}
	// The round-derived counter still cannot see the dead reworks: this is the
	// exact authority drift the escalation budget reconciles.
	for _, card := range drafts {
		count, err := h.judge.Store.CountReworks(h.ctx(), card.CaseID)
		if err != nil || count != 0 {
			t.Fatalf("CountReworks(case %d) = %d/%v, want 0 (no new judgment round)", card.CaseID, count, err)
		}
		terminal, err := (&judgeReworkBudgetReader{source: &judgeReworkSQLSource{pool: h.pool}}).
			TerminalReworkCount(h.ctx(), h.versionID, card.CaseID)
		if err != nil || terminal != 1 {
			t.Fatalf("TerminalReworkCount(case %d) = %d/%v, want 1", card.CaseID, terminal, err)
		}
	}

	// NO PERMANENT PENDING: the derived review state escalates to a human.
	h.reconcile()
	for _, card := range drafts {
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStateEscalatedFail {
			t.Fatalf("review state after rework death = %s, want escalated_fail", state)
		}
	}
	// Further passes neither revive the dead rework nor re-announce it.
	h.reconcile()
	h.reconcile()
	if items := h.workItemsOfType(agentapp.WorkTypeDesignRework); len(items) != 2 {
		t.Fatalf("rework items after extra reconciles = %d, want 2", len(items))
	}
	if got := h.eventCount("design.rework_requested"); got != 2 {
		t.Fatalf("rework_requested events = %d, want 2 (one per case, no re-announce)", got)
	}

	// The escalation reaches case_review through the shared review gate, and
	// the manual edge agrees (one predicate).
	h.reconcile()
	if stage := h.stage(); stage != versiondomain.StageCaseReview {
		t.Fatalf("stage = %s, want case_review (the human decision point must be reachable)", stage)
	}

	// Confirm without an override is still rejected — escalation is not a
	// bypass of the human judgement point.
	_, err := h.graph.Confirm(h.ctx(), h.versionID, designapp.ConfirmInput{
		Scope: design.ConfirmScopeRequirement, TargetID: reqID,
	}, 1)
	if err == nil || decodePlatformCode(err) != designapp.JudgmentFailedCode {
		t.Fatalf("confirm without override: err=%v, want 409 %s", err, designapp.JudgmentFailedCode)
	}

	// The human escape: admin override on the escalated fail, then confirm.
	for _, card := range drafts {
		recorder := h.overrideRoute(card.CaseID, "admin", "打回执行不可用，人工审查后接受该用例")
		if recorder.Code != http.StatusOK {
			t.Fatalf("override after rework death: status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
		}
		override := decodeDataEnvelope(t, recorder)
		if override["condition"] != designapp.OverrideConditionFailExhausted {
			t.Fatalf("override condition = %v, want fail_exhausted", override["condition"])
		}
		versionID, contentHash := h.currentContentHash(card.CaseID)
		if override["case_version_id"] != platformid.Format(versionID) || override["content_hash"] != contentHash {
			t.Fatalf("override not bound to the current content: %v", override)
		}
	}
	report := h.judgmentsRoute("user")
	if report.Code != http.StatusOK {
		t.Fatalf("judgment report status = %d, want 200", report.Code)
	}
	data := decodeDataEnvelope(t, report)
	cases, _ := data["cases"].([]any)
	if len(cases) != 2 {
		t.Fatalf("report cases = %d, want 2", len(cases))
	}
	for _, entry := range cases {
		card := entry.(map[string]any)
		if card["review_state"] != string(design.ReviewStateEscalatedFail) {
			t.Fatalf("report review_state = %v, want escalated_fail", card["review_state"])
		}
	}
	if result := h.confirmRequirement(reqID); len(result.Confirmed) != 2 {
		t.Fatalf("confirmed %d cases after the overrides, want 2", len(result.Confirmed))
	}
	for _, card := range drafts {
		if h.confirmationCount(card.CurrentVersionID) != 1 {
			t.Fatalf("case %d missing its confirmation row", card.CaseID)
		}
	}
	h.requireNoLiveRework()
}

// setMaxReworkRounds changes the registered rework bound through the real
// settings use case (catalog: design.max_rework_rounds, Integer, Min 0, NO
// upper bound — an admin can set 2 or more, so the no-permanent-pending
// guarantee must hold for every value, not only the default 1).
func (h *judgeLoopHarness) setMaxReworkRounds(rounds int) {
	h.t.Helper()
	if _, err := h.settings.Update(h.ctx(), designapp.MaxReworkRoundsKey, rounds, 1); err != nil {
		h.t.Fatalf("set %s=%d: %v", designapp.MaxReworkRoundsKey, rounds, err)
	}
	value, err := h.settings.Int(h.ctx(), designapp.MaxReworkRoundsKey)
	if err != nil || int(value) != rounds {
		h.t.Fatalf("rework bound read back %d/%v, want %d", value, err, rounds)
	}
}

// requireNoLiveRework asserts that no design.rework of the version still owes
// work: every row is done, dead, or failed at its attempt cap. An escalation
// with a live rework outstanding would be a premature human hand-off; a
// non-escalated case with only dead reworks is the permanent pending.
func (h *judgeLoopHarness) requireNoLiveRework() {
	h.t.Helper()
	for _, item := range h.workItemsOfType(agentapp.WorkTypeDesignRework) {
		switch item.Status {
		case agent.WorkItemDone, agent.WorkItemDead:
		case agent.WorkItemFailed:
			if item.Attempts < item.MaxAttempts {
				h.t.Fatalf("rework %d is retryable-failed (%d/%d): work is still owed", item.ID, item.Attempts, item.MaxAttempts)
			}
		default:
			h.t.Fatalf("rework %d status = %s: a live rework is still owed", item.ID, item.Status)
		}
	}
}

// TestJudgeClosedLoopReworkDeathBudgetTwoEscalates is the reviewer's
// max_rework_rounds=2 probe against the real assembly (review fix round 1).
// The rework request key rework:<case>:<failedVersion> has NO generation and
// Ensure never revives a dead row, so with budget 2 the count test alone
// (max(0,1)=1 < 2) keeps "allowing" a rework that can never be created: no new
// item, no escalation, review_state reworking, override 409 — the same permanent
// pending as DEV-E3, one configuration away. The terminal-rework branch closes it.
func TestJudgeClosedLoopReworkDeathBudgetTwoEscalates(t *testing.T) {
	h := newJudgeLoopHarness(t)
	h.setMaxReworkRounds(2)
	reqID, _ := h.setupDesigningRequirement(1)
	h.setAuthorMode("full")
	h.setJudgeMode("fail")

	h.requireRunDone(h.runWorkItems("design.requirement"), 1, "author")
	drafts := h.draftCards()
	if len(drafts) != 2 {
		t.Fatalf("author produced %d drafts, want 2", len(drafts))
	}
	h.reconcile()
	h.requireRunDone(h.runWorkItems("design.judge"), 1, "judge round 1")
	h.reconcile() // one rework per case
	if items := h.workItemsOfType(agentapp.WorkTypeDesignRework); len(items) != 2 {
		t.Fatalf("rework items = %d, want 2", len(items))
	}

	// The rework runs fail (LLM unreachable) and retry to their attempt cap.
	h.takeLLMDown()
	runs := h.runWorkItems(agentapp.WorkTypeDesignRework)
	if len(runs) == 0 {
		t.Fatal("no rework runs were attempted")
	}
	for _, item := range h.workItemsOfType(agentapp.WorkTypeDesignRework) {
		if item.Status != agent.WorkItemDead {
			t.Fatalf("rework %d status = %s, want dead", item.ID, item.Status)
		}
	}
	h.requireNoLiveRework()

	// The budget alone does NOT escalate here — this is what makes the probe
	// discriminating against a budget-only fix.
	budgetReader := &judgeReworkBudgetReader{source: &judgeReworkSQLSource{pool: h.pool}}
	for _, card := range drafts {
		count, err := h.judge.Store.CountReworks(h.ctx(), card.CaseID)
		if err != nil || count != 0 {
			t.Fatalf("CountReworks(case %d) = %d/%v, want 0", card.CaseID, count, err)
		}
		terminal, err := budgetReader.TerminalReworkCount(h.ctx(), h.versionID, card.CaseID)
		if err != nil || terminal != 1 {
			t.Fatalf("TerminalReworkCount(case %d) = %d/%v, want 1", card.CaseID, terminal, err)
		}
		if !design.ReworkAllowed(terminal, 2) {
			t.Fatal("fixture precondition broken: budget 2 is already spent, the probe would not discriminate")
		}
		dead, err := budgetReader.HasTerminalReworkForVersion(h.ctx(), h.versionID, card.CaseID, card.CurrentVersionID)
		if err != nil || !dead {
			t.Fatalf("HasTerminalReworkForVersion(case %d) = %v/%v, want true", card.CaseID, dead, err)
		}
	}

	// Three reconcile passes: the dead rows occupy the dedupe keys, so a
	// budget-only planner would re-Ensure them forever with created=false.
	h.reconcile()
	h.reconcile()
	h.reconcile()
	for _, card := range drafts {
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStateEscalatedFail {
			t.Fatalf("review state with budget 2 after rework death = %s, want escalated_fail", state)
		}
	}
	if items := h.workItemsOfType(agentapp.WorkTypeDesignRework); len(items) != 2 {
		t.Fatalf("rework items after extra reconciles = %d, want 2 (no revival, no new generation)", len(items))
	}
	if got := h.eventCount("design.rework_requested"); got != 2 {
		t.Fatalf("rework_requested events = %d, want 2", got)
	}
	h.requireNoLiveRework()

	// The human decision point is reachable end to end.
	h.reconcile()
	if stage := h.stage(); stage != versiondomain.StageCaseReview {
		t.Fatalf("stage = %s, want case_review", stage)
	}
	for _, card := range drafts {
		recorder := h.overrideRoute(card.CaseID, "admin", "打回执行死亡（预算 2），人工审查后接受")
		if recorder.Code != http.StatusOK {
			t.Fatalf("override status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
		}
		if condition := decodeDataEnvelope(t, recorder)["condition"]; condition != designapp.OverrideConditionFailExhausted {
			t.Fatalf("override condition = %v, want fail_exhausted", condition)
		}
	}
	if result := h.confirmRequirement(reqID); len(result.Confirmed) != 2 {
		t.Fatalf("confirmed %d cases, want 2", len(result.Confirmed))
	}
}

// TestJudgeClosedLoopBudgetTwoSecondRework pins that the terminal-rework branch does
// NOT break the normal multi-round loop: with budget 2, fail -> rework(done) ->
// round 2 fail is still REWORKING (a done rework bound to an earlier fail is not
// a dead end) and the scheduler enqueues a SECOND rework per case under the NEW
// failed-version key. That second rework is then driven dead to close the loop
// at the human decision point (override -> confirm).
func TestJudgeClosedLoopBudgetTwoSecondRework(t *testing.T) {
	h := newJudgeLoopHarness(t)
	h.setMaxReworkRounds(2)
	reqID, _ := h.setupDesigningRequirement(1)
	h.setAuthorMode("full")
	h.setJudgeMode("fail")

	h.requireRunDone(h.runWorkItems("design.requirement"), 1, "author")
	h.reconcile()
	h.requireRunDone(h.runWorkItems("design.judge"), 1, "judge round 1")
	h.reconcile()
	h.requireRunDone(h.runWorkItems(agentapp.WorkTypeDesignRework), 2, "rework round 1")

	// Round 2 fails on the revised versions: budget 2 still allows a rework, and
	// the DONE round-1 rework must not be mistaken for a dead end.
	h.reconcile()
	h.requireRunDone(h.runWorkItems("design.judge"), 1, "judge round 2")
	for _, card := range h.draftCards() {
		judgment := h.latestJudgment(card.CaseID)
		if judgment == nil || judgment.Round != 2 || judgment.Verdict != design.VerdictFail {
			t.Fatalf("round-2 judgment = %+v, want fail round 2", judgment)
		}
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStateReworking {
			t.Fatalf("round-2 review state = %s, want reworking (a done rework is not a dead end)", state)
		}
	}
	h.reconcile()
	reworks := h.workItemsOfType(agentapp.WorkTypeDesignRework)
	if len(reworks) != 4 {
		t.Fatalf("rework items = %d, want 4 (a second rework per case under the new failed-version key)", len(reworks))
	}
	keys := map[string]bool{}
	for _, item := range reworks {
		keys[item.DedupeKey] = true
	}
	if len(keys) != 4 {
		t.Fatalf("distinct rework dedupe keys = %d, want 4", len(keys))
	}
	// The second rework is now driven dead (LLM outage) to close the loop at the
	// human decision point. Note the fake author's revision is deterministic: a
	// second SUCCESSFUL revision of the same case reproduces identical content
	// and writes no new version — that ending is pinned separately by
	// TestJudgeClosedLoopReworkDoneNoRevisionEscalates (DEV-E4). What is pinned
	// here is the multi-round scheduling invariant above: a rework that advanced
	// the case never escalates, and the next fail gets its own rework key.
	h.takeLLMDown()
	if runs := h.runWorkItems(agentapp.WorkTypeDesignRework); len(runs) == 0 {
		t.Fatal("no second-rework runs were attempted")
	}
	h.reconcile()
	h.reconcile()
	drafts := h.draftCards()
	for _, card := range drafts {
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStateEscalatedFail {
			t.Fatalf("review state after the second rework died = %s, want escalated_fail", state)
		}
	}
	h.requireNoLiveRework()
	if items := h.workItemsOfType(agentapp.WorkTypeDesignRework); len(items) != 4 {
		t.Fatalf("rework items = %d, want 4 (no fifth rework)", len(items))
	}

	h.reconcile()
	if stage := h.stage(); stage != versiondomain.StageCaseReview {
		t.Fatalf("stage = %s, want case_review", stage)
	}
	for _, card := range drafts {
		if recorder := h.overrideRoute(card.CaseID, "admin", "两轮打回后仍失败，人工接受"); recorder.Code != http.StatusOK {
			t.Fatalf("override status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
		}
	}
	if result := h.confirmRequirement(reqID); len(result.Confirmed) != 2 {
		t.Fatalf("confirmed %d cases, want 2", len(result.Confirmed))
	}
}

// TestJudgeClosedLoopBudgetTwoReworkPass pins the other normal path under
// budget 2: fail -> rework(done) -> round 2 PASS -> pass -> human confirm. The
// terminal-rework branch must not escalate a case whose rework succeeded.
func TestJudgeClosedLoopBudgetTwoReworkPass(t *testing.T) {
	h := newJudgeLoopHarness(t)
	h.setMaxReworkRounds(2)
	reqID, _ := h.setupDesigningRequirement(1)
	h.setAuthorMode("full")
	h.setJudgeMode("fail")

	h.requireRunDone(h.runWorkItems("design.requirement"), 1, "author")
	h.reconcile()
	h.requireRunDone(h.runWorkItems("design.judge"), 1, "judge round 1")
	h.reconcile()
	h.setJudgeMode("pass")
	h.requireRunDone(h.runWorkItems(agentapp.WorkTypeDesignRework), 2, "rework")
	h.reconcile()
	h.requireRunDone(h.runWorkItems("design.judge"), 1, "judge round 2")
	for _, card := range h.draftCards() {
		judgment := h.latestJudgment(card.CaseID)
		if judgment == nil || judgment.Round != 2 || judgment.Verdict != design.VerdictPass {
			t.Fatalf("round-2 judgment = %+v, want pass round 2", judgment)
		}
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStatePass {
			t.Fatalf("review state = %s, want pass", state)
		}
	}
	h.requireNoLiveRework()
	h.reconcile()
	if stage := h.stage(); stage != versiondomain.StageCaseReview {
		t.Fatalf("stage = %s, want case_review", stage)
	}
	if result := h.confirmRequirement(reqID); len(result.Confirmed) != 2 {
		t.Fatalf("confirmed %d cases, want 2", len(result.Confirmed))
	}
}

// TestJudgeClosedLoopReworkDoneNoRevisionEscalates is the DEV-E4 reproduction
// (review fix round 2) against the real assembly. A design.rework run can end
// DONE without a verified completion, i.e. without proposing a revised case
// version. The latest fail then stays bound to the CURRENT version and the
// generation-less request key rework:<case>:<failedVersion> is occupied by a
// DONE row that Ensure never revives. With design.max_rework_rounds=3 the budget
// test still allows a rework (max(1,2)=2 < 3) that can never be created:
// reworking forever, override 409. The generalized TERMINAL-rework branch
// (done OR dead OR failed-at-cap) escalates it.
//
// The no-revision ending is produced here by the fake author's deterministic
// revision: the second rework proposes content identical to the current version,
// so the content-addressed graph writes NO new case version while the work item
// completes done — exactly the production shape.
func TestJudgeClosedLoopReworkDoneNoRevisionEscalates(t *testing.T) {
	h := newJudgeLoopHarness(t)
	h.setMaxReworkRounds(3)
	reqID, _ := h.setupDesigningRequirement(1)
	h.setAuthorMode("full")
	h.setJudgeMode("fail")

	h.requireRunDone(h.runWorkItems("design.requirement"), 1, "author")
	h.reconcile()
	h.requireRunDone(h.runWorkItems("design.judge"), 1, "judge round 1")
	h.reconcile()
	// First rework revises: a NEW current version, judged fail at round 2.
	h.requireRunDone(h.runWorkItems(agentapp.WorkTypeDesignRework), 2, "rework 1")
	h.reconcile()
	h.requireRunDone(h.runWorkItems("design.judge"), 1, "judge round 2")
	h.reconcile()
	// Second rework completes DONE but proposes identical content: no new
	// version, so the round-2 fail is still bound to the current version.
	h.requireRunDone(h.runWorkItems(agentapp.WorkTypeDesignRework), 2, "rework 2 (no revision)")

	drafts := h.draftCards()
	if len(drafts) != 2 {
		t.Fatalf("drafts = %d, want 2", len(drafts))
	}
	reader := &judgeReworkBudgetReader{source: &judgeReworkSQLSource{pool: h.pool}}
	for _, card := range drafts {
		judgment := h.latestJudgment(card.CaseID)
		if judgment == nil || judgment.CaseVersionID != card.CurrentVersionID || judgment.Verdict != design.VerdictFail {
			t.Fatalf("fixture precondition broken: the latest fail must still bind the current version: %+v (current %d)",
				judgment, card.CurrentVersionID)
		}
		count, err := h.judge.Store.CountReworks(h.ctx(), card.CaseID)
		if err != nil || count != 1 {
			t.Fatalf("CountReworks(case %d) = %d/%v, want 1", card.CaseID, count, err)
		}
		terminal, err := reader.TerminalReworkCount(h.ctx(), h.versionID, card.CaseID)
		if err != nil || terminal != 2 {
			t.Fatalf("TerminalReworkCount(case %d) = %d/%v, want 2 (both reworks done)", card.CaseID, terminal, err)
		}
		if !design.ReworkAllowed(terminal, 3) {
			t.Fatal("fixture precondition broken: budget 3 is already spent, the probe would not discriminate")
		}
		isTerminal, err := reader.HasTerminalReworkForVersion(h.ctx(), h.versionID, card.CaseID, card.CurrentVersionID)
		if err != nil || !isTerminal {
			t.Fatalf("HasTerminalReworkForVersion(case %d) = %v/%v, want true (done, no revision)", card.CaseID, isTerminal, err)
		}
	}

	// Three passes: a budget-only (or dead-only) predicate would keep re-Ensuring
	// the occupied key with created=false and never escalate.
	h.reconcile()
	h.reconcile()
	h.reconcile()
	for _, card := range drafts {
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStateEscalatedFail {
			t.Fatalf("review state after a no-revision done rework = %s, want escalated_fail", state)
		}
	}
	if items := h.workItemsOfType(agentapp.WorkTypeDesignRework); len(items) != 4 {
		t.Fatalf("rework items = %d, want 4 (no revival, no new generation)", len(items))
	}
	if got := h.eventCount("design.rework_requested"); got != 4 {
		t.Fatalf("rework_requested events = %d, want 4 (two per case, no re-announce)", got)
	}
	h.requireNoLiveRework()

	h.reconcile()
	if stage := h.stage(); stage != versiondomain.StageCaseReview {
		t.Fatalf("stage = %s, want case_review (the human decision point must be reachable)", stage)
	}
	for _, card := range drafts {
		recorder := h.overrideRoute(card.CaseID, "admin", "打回未产出修订，人工审查后接受")
		if recorder.Code != http.StatusOK {
			t.Fatalf("override status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
		}
		if condition := decodeDataEnvelope(t, recorder)["condition"]; condition != designapp.OverrideConditionFailExhausted {
			t.Fatalf("override condition = %v, want fail_exhausted", condition)
		}
	}
	if result := h.confirmRequirement(reqID); len(result.Confirmed) != 2 {
		t.Fatalf("confirmed %d cases, want 2", len(result.Confirmed))
	}
}
