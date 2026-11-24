package designapp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

// newJudgeSchedulerFixture wires a planner with the judge scheduling ports
// (judgment facts + the rework-bound settings reader). The production assembly
// injects these in T13; until then the planner's judge path is a no-op, so the
// existing planner tests (which never wire them) stay green.
func newJudgeSchedulerFixture() (*Planner, *fakeRepo, *fakeEnqueuer, *fakeJudgmentRepo, fakeJudgmentSettings) {
	planner, repo, enqueuer, _, _, _, _ := newPlannerFixture()
	judgments := &fakeJudgmentRepo{exhausted: map[[2]int64]bool{}}
	settings := fakeJudgmentSettings{maxReworkRoundsKey: 1}
	planner.SetJudgeScheduler(judgments, settings)
	return planner, repo, enqueuer, judgments, settings
}

// seedJudgeGraph seeds one activated requirement with a scenario bound to its
// current revision and one current case per covered-AC group. confirmedACs and
// draftACs are the per-case covered_acs sets, split by lifecycle status, so a
// test can drive the coverage union (spec SC-E2). Draft case i gets logical
// case id 7001+i and case version id 8001+i (after the confirmed cases).
func seedJudgeGraph(repo *fakeRepo, reqID, reqVerID int64, acIDs []string, confirmedACs, draftACs [][]string) {
	repo.requirement = design.RequirementRef{
		ID: reqID, VersionID: 1001, CurrentVersionID: reqVerID, Revision: 1,
		Title: "需求", Status: "clarified", ACIDs: acIDs,
	}
	scenarioID := int64(5000)
	repo.scenarios = []design.Scenario{{
		ID: scenarioID, VersionID: 1001, RequirementID: reqID, RequirementVersionID: reqVerID, Name: "场景",
	}}
	repo.cases = map[int64]*fakeCase{}
	caseID := int64(7000)
	verID := int64(8000)
	add := func(acs []string, status string) {
		caseID++
		verID++
		current := design.CaseVersion{
			ID: verID, CaseID: caseID, Revision: 1, Status: status,
			CoveredACs: acs, ContentHash: strings.Repeat("a", 64),
			StepCount: 1, AssertionCount: 1,
		}
		currentID := verID
		repo.cases[caseID] = &fakeCase{
			entity: design.CaseEntity{
				ID: caseID, ScenarioID: scenarioID, CurrentVersionID: &currentID, Status: status,
			},
			current:  &current,
			versions: []design.CaseVersion{current},
		}
	}
	for _, acs := range confirmedACs {
		add(acs, design.CaseStatusConfirmed)
	}
	for _, acs := range draftACs {
		add(acs, design.CaseStatusDraft)
	}
}

// judgeTaskCaseIDs extracts the sorted case_id list from a design.judge task's
// per-case review scope (payload "cases").
func judgeTaskCaseIDs(t *testing.T, task TaskInput) []string {
	t.Helper()
	cases, ok := task.Payload["cases"].([]map[string]any)
	if !ok {
		t.Fatalf("judge task payload has no per-case scope: %v", task.Payload)
	}
	ids := make([]string, 0, len(cases))
	for _, item := range cases {
		ids = append(ids, fmt.Sprint(item["case_id"]))
	}
	sort.Strings(ids)
	return ids
}

// seedJudgePass appends a pass fact bound to the exact current version content.
func seedJudgePass(judgments *fakeJudgmentRepo, id, caseID, caseVersionID int64) {
	judgments.judgments = append(judgments.judgments, design.Judgment{
		ID: id, VersionID: 1001, RequirementID: 10, CaseID: caseID, CaseVersionID: caseVersionID,
		ContentHash: strings.Repeat("a", 64), Round: 1, Verdict: design.VerdictPass,
	})
}

// TestEnsureJudgeTasksGatedByCoverage pins spec SC-E2/SC-E4: the judge is only
// enqueued once the confirmed-union-draft coverage covers every requirement AC;
// a partial union triggers an AC-gap repair instead (the partial-coverage
// livelock fix), never a judge run. ONE judge run per requirement-version
// generation, and its review scope covers EVERY current draft (no stranding).
func TestEnsureJudgeTasksGatedByCoverage(t *testing.T) {
	planner, repo, enqueuer, _, _ := newJudgeSchedulerFixture()
	ctx := context.Background()

	// Draft covers AC-1 only; AC-2 is missing -> no judge, AC-gap repair.
	seedJudgeGraph(repo, 10, 100, []string{"AC-1", "AC-2"}, nil, [][]string{{"AC-1"}})
	if err := planner.ensureJudgeTasks(ctx, 1001); err != nil {
		t.Fatalf("ensureJudgeTasks (gap): %v", err)
	}
	if got := len(enqueuer.byType(TaskTypeDesignJudge)); got != 0 {
		t.Fatalf("judge enqueued despite coverage gap: %d", got)
	}
	repair := enqueuer.byType(TaskTypeDesignRequirement)
	if len(repair) == 0 {
		t.Fatal("AC-gap repair not triggered")
	}
	if taskPayloadString(repair[0], "reason") != "ac_gap" {
		t.Fatalf("AC-gap repair reason = %v, want ac_gap", repair[0].Payload)
	}

	// A second draft completes the union -> exactly one judge run covering BOTH
	// drafts (7001/8001 and 7002/8002), not one anchor.
	seedJudgeGraph(repo, 10, 100, []string{"AC-1", "AC-2"}, nil, [][]string{{"AC-1"}, {"AC-2"}})
	if err := planner.ensureJudgeTasks(ctx, 1001); err != nil {
		t.Fatalf("ensureJudgeTasks (complete): %v", err)
	}
	judges := enqueuer.byType(TaskTypeDesignJudge)
	if len(judges) != 1 {
		t.Fatalf("judge runs = %d, want exactly one per requirement-version generation", len(judges))
	}
	if judges[0].DedupeKey != "judge:10:100" {
		t.Fatalf("judge dedupe = %q, want judge:10:100", judges[0].DedupeKey)
	}
	if ids := judgeTaskCaseIDs(t, judges[0]); len(ids) != 2 || ids[0] != "7001" || ids[1] != "7002" {
		t.Fatalf("judge review scope = %v, want both drafts [7001 7002] (no stranding)", ids)
	}
	if got := taskPayloadString(judges[0], "draft_case_version_ids"); got != "8001,8002" {
		t.Fatalf("generation anchor = %q, want the full draft set 8001,8002", got)
	}

	// Idempotent: a repeat pass does not stampede the in-flight judge generation.
	taskCount := len(enqueuer.tasks)
	if err := planner.ensureJudgeTasks(ctx, 1001); err != nil {
		t.Fatalf("ensureJudgeTasks (repeat): %v", err)
	}
	if len(enqueuer.tasks) != taskCount {
		t.Fatalf("repeat duplicated tasks: %v", sortedTaskDedupeKeys(enqueuer.tasks))
	}
}

// TestEnsureJudgeTasksConfirmedUnionComplete pins that confirmed cases count
// toward the union: a requirement whose confirmed cases already cover every AC
// and whose only draft covers a subset is still complete (spec SC-E2).
func TestEnsureJudgeTasksConfirmedUnionComplete(t *testing.T) {
	planner, repo, enqueuer, _, _ := newJudgeSchedulerFixture()
	ctx := context.Background()
	seedJudgeGraph(repo, 10, 100, []string{"AC-1", "AC-2"}, [][]string{{"AC-2"}}, [][]string{{"AC-1"}})
	if err := planner.ensureJudgeTasks(ctx, 1001); err != nil {
		t.Fatalf("ensureJudgeTasks: %v", err)
	}
	if len(enqueuer.byType(TaskTypeDesignJudge)) != 1 {
		t.Fatalf("confirmed union did not complete coverage: %v", sortedTaskDedupeKeys(enqueuer.tasks))
	}
	if len(enqueuer.byType(TaskTypeDesignRequirement)) != 0 {
		t.Fatal("complete coverage triggered an AC-gap repair")
	}
}

// TestEnsureJudgeTasksMultiCaseVerdictsClearNeedsJudge pins the multi-case
// no-spurious-exhaustion rule (review fix round 1): one judge run covers both
// drafts; once the run submits verdicts for BOTH current versions, needsJudge
// is false even though the judge generation is terminal -- no re-enqueue, no
// generation advance, no judge_exhausted against passed cases.
func TestEnsureJudgeTasksMultiCaseVerdictsClearNeedsJudge(t *testing.T) {
	planner, repo, enqueuer, judgments, _ := newJudgeSchedulerFixture()
	ctx := context.Background()
	seedJudgeGraph(repo, 10, 100, []string{"AC-1", "AC-2"}, nil, [][]string{{"AC-1"}, {"AC-2"}})
	if err := planner.ensureJudgeTasks(ctx, 1001); err != nil {
		t.Fatalf("ensureJudgeTasks: %v", err)
	}
	judges := enqueuer.byType(TaskTypeDesignJudge)
	if len(judges) != 1 {
		t.Fatalf("judge runs = %d, want 1", len(judges))
	}

	// The run completes and submits a pass verdict for EVERY draft version.
	enqueuer.statuses[judges[0].DedupeKey] = "done"
	seedJudgePass(judgments, 1, 7001, 8001)
	seedJudgePass(judgments, 2, 7002, 8002)
	for pass := 0; pass < 2; pass++ {
		if err := planner.ensureJudgeTasks(ctx, 1001); err != nil {
			t.Fatalf("ensureJudgeTasks after verdicts: %v", err)
		}
	}
	if got := len(enqueuer.byType(TaskTypeDesignJudge)); got != 1 {
		t.Fatalf("judged drafts were re-judged: %d judge runs, want 1", got)
	}
	if len(enqueuer.byType(TaskTypeDesignRework)) != 0 {
		t.Fatal("pass verdicts triggered rework")
	}
	if len(enqueuer.judgeExhausted) != 0 {
		t.Fatalf("spurious judge_exhausted against judged drafts: %v", enqueuer.judgeExhausted)
	}
}

// TestEnsureJudgeTasksNonAnchorNotStranded pins the stranding fix (review fix
// round 1): with draft A unjudged and draft B already judged pass, the judge
// run must still be scheduled and cover A (the old max-ID anchor scheduled B
// again and stranded A forever). B is not re-judged.
func TestEnsureJudgeTasksNonAnchorNotStranded(t *testing.T) {
	planner, repo, enqueuer, judgments, _ := newJudgeSchedulerFixture()
	ctx := context.Background()
	// A = 7001/8001 (lower id, unjudged), B = 7002/8002 (max id, judged pass).
	seedJudgeGraph(repo, 10, 100, []string{"AC-1", "AC-2"}, nil, [][]string{{"AC-1"}, {"AC-2"}})
	seedJudgePass(judgments, 1, 7002, 8002)
	if err := planner.ensureJudgeTasks(ctx, 1001); err != nil {
		t.Fatalf("ensureJudgeTasks: %v", err)
	}
	judges := enqueuer.byType(TaskTypeDesignJudge)
	if len(judges) != 1 {
		t.Fatalf("unjudged draft A did not get a judge run: %d runs", len(judges))
	}
	if ids := judgeTaskCaseIDs(t, judges[0]); len(ids) != 1 || ids[0] != "7001" {
		t.Fatalf("judge review scope = %v, want [7001] (A covered, passed B not re-judged)", ids)
	}

	// Once A is judged too, no draft needs a run: no re-enqueue at all.
	enqueuer.statuses[judges[0].DedupeKey] = "done"
	seedJudgePass(judgments, 2, 7001, 8001)
	if err := planner.ensureJudgeTasks(ctx, 1001); err != nil {
		t.Fatalf("ensureJudgeTasks after A judged: %v", err)
	}
	if got := len(enqueuer.byType(TaskTypeDesignJudge)); got != 1 {
		t.Fatalf("fully judged requirement re-enqueued a judge run: %d", got)
	}
	if len(enqueuer.judgeExhausted) != 0 {
		t.Fatalf("spurious exhaustion: %v", enqueuer.judgeExhausted)
	}
}

// TestEnsureJudgeTasksSkipsJudgedPass pins content binding (spec SC-E4): a
// current draft already judged pass for its current version is not re-judged.
// Re-judging it would burn the generation budget into a spurious escalation.
func TestEnsureJudgeTasksSkipsJudgedPass(t *testing.T) {
	planner, repo, enqueuer, judgments, _ := newJudgeSchedulerFixture()
	ctx := context.Background()
	// One draft case (id 7001, version 8001) covers every AC -> union complete.
	seedJudgeGraph(repo, 10, 100, []string{"AC-1", "AC-2"}, nil, [][]string{{"AC-1", "AC-2"}})
	seedJudgePass(judgments, 1, 7001, 8001)
	if err := planner.ensureJudgeTasks(ctx, 1001); err != nil {
		t.Fatalf("ensureJudgeTasks: %v", err)
	}
	if got := len(enqueuer.byType(TaskTypeDesignJudge)); got != 0 {
		t.Fatalf("a passed current draft was re-judged: %d judge tasks", got)
	}
}

// TestReworkDefaultAllowsExactlyOne pins the off-by-one fix and round authority
// (spec SC-E4, controller rulings T8-4/T8-7): with the default bound of 1, the
// first fail reworks exactly once (round 1->2); the second fail escalates to a
// human (review_state escalated_fail) and enqueues no further rework.
func TestReworkDefaultAllowsExactlyOne(t *testing.T) {
	planner, repo, enqueuer, judgments, settings := newJudgeSchedulerFixture()
	settings[maxReworkRoundsKey] = 1
	ctx := context.Background()
	hash := strings.Repeat("a", 64)

	// Round 1 fail on current version 5; rework count 0 -> one rework.
	repo.caseDataByID = map[int64]design.CaseData{
		7001: {
			Case:        design.CaseEntity{ID: 7001},
			Requirement: design.RequirementRef{ID: 10, VersionID: 1001},
			Selected:    design.CaseVersion{ID: 5, CaseID: 7001, ContentHash: hash},
		},
	}
	judgments.judgments = append(judgments.judgments, design.Judgment{
		ID: 1, VersionID: 1001, RequirementID: 10, CaseID: 7001, CaseVersionID: 5,
		ContentHash: hash, Round: 1, Verdict: design.VerdictFail,
	})
	if err := planner.handleJudgeFail(ctx, 1001, 7001); err != nil {
		t.Fatalf("handleJudgeFail (first): %v", err)
	}
	reworks := enqueuer.byType(TaskTypeDesignRework)
	if len(reworks) != 1 {
		t.Fatalf("first fail must rework exactly once: %d", len(reworks))
	}
	if taskPayloadString(reworks[0], "round") != "2" {
		t.Fatalf("rework payload round = %v, want 2 (next judgment round)", reworks[0].Payload)
	}
	if taskPayloadString(reworks[0], "case_id") != "7001" {
		t.Fatalf("rework payload case_id = %v", reworks[0].Payload)
	}

	// Round 2 fail on the revised current version 6; rework count 1 -> escalate.
	revisedHash := strings.Repeat("b", 64)
	repo.caseDataByID[7001] = design.CaseData{
		Case:        design.CaseEntity{ID: 7001},
		Requirement: design.RequirementRef{ID: 10, VersionID: 1001},
		Selected:    design.CaseVersion{ID: 6, CaseID: 7001, ContentHash: revisedHash},
	}
	judgments.judgments = append(judgments.judgments, design.Judgment{
		ID: 2, VersionID: 1001, RequirementID: 10, CaseID: 7001, CaseVersionID: 6,
		ContentHash: revisedHash, Round: 2, Verdict: design.VerdictFail,
	})
	if err := planner.handleJudgeFail(ctx, 1001, 7001); err != nil {
		t.Fatalf("handleJudgeFail (second): %v", err)
	}
	if len(enqueuer.byType(TaskTypeDesignRework)) != 1 {
		t.Fatal("second fail must not enqueue another rework")
	}

	// The escalation is derived (no extra fact): review_state is escalated_fail.
	svc := NewJudgmentService(judgments, repo, &fakeIDs{}, newFixedClock(), nil, settings)
	state, err := svc.ReviewStateFor(ctx, 1001, 7001, 6)
	if err != nil {
		t.Fatalf("ReviewStateFor: %v", err)
	}
	if state != design.ReviewStateEscalatedFail {
		t.Fatalf("second fail review_state = %q, want escalated_fail", state)
	}
}

// TestHandleJudgeFailHostEditKeepsRound pins that a harmless host edit (a new
// case version judged at the SAME round) does not consume rework budget: the
// rework count derives from max(round)-1, so a round-1 fail on a new version
// still reworks (controller ruling T8-7).
func TestHandleJudgeFailHostEditKeepsRound(t *testing.T) {
	planner, repo, enqueuer, judgments, settings := newJudgeSchedulerFixture()
	settings[maxReworkRoundsKey] = 1
	ctx := context.Background()
	hash := strings.Repeat("c", 64)
	repo.caseDataByID = map[int64]design.CaseData{
		7001: {
			Case:        design.CaseEntity{ID: 7001},
			Requirement: design.RequirementRef{ID: 10, VersionID: 1001},
			Selected:    design.CaseVersion{ID: 9, CaseID: 7001, ContentHash: hash},
		},
	}
	// A round-1 fail bound to the new current version 9 (host edit rejudge).
	judgments.judgments = append(judgments.judgments, design.Judgment{
		ID: 1, VersionID: 1001, RequirementID: 10, CaseID: 7001, CaseVersionID: 9,
		ContentHash: hash, Round: 1, Verdict: design.VerdictFail,
	})
	if err := planner.handleJudgeFail(ctx, 1001, 7001); err != nil {
		t.Fatalf("handleJudgeFail: %v", err)
	}
	if len(enqueuer.byType(TaskTypeDesignRework)) != 1 {
		t.Fatal("a round-1 fail must still rework after a harmless host edit")
	}
}

// TestHandleJudgeFailReworkDeathEscalates pins spec SC-E4 "无永久 pending" on the
// scheduling side: when the one allowed design.rework died at its attempt cap it
// consumed the budget without producing a new judgment round, so CountReworks
// (max(round)-1) is still 0. ReworkAllowed(0,1) then kept asking for the SAME
// dedupe key rework:<case>:<failedVersion>, which Ensure answers with
// created=false (it never revives a dead row) — no new rework, no escalation,
// and the case strands in reworking forever. With the terminal-rework budget
// wired the planner enqueues nothing and the derived review state is
// escalated_fail, so the human decision point is reachable.
func TestHandleJudgeFailReworkDeathEscalates(t *testing.T) {
	planner, repo, enqueuer, judgments, settings := newJudgeSchedulerFixture()
	settings[maxReworkRoundsKey] = 1
	ctx := context.Background()
	hash := strings.Repeat("d", 64)
	repo.caseDataByID = map[int64]design.CaseData{
		7001: {
			Case:        design.CaseEntity{ID: 7001},
			Requirement: design.RequirementRef{ID: 10, VersionID: 1001},
			Selected:    design.CaseVersion{ID: 5, CaseID: 7001, ContentHash: hash},
		},
	}
	judgments.judgments = append(judgments.judgments, design.Judgment{
		ID: 1, VersionID: 1001, RequirementID: 10, CaseID: 7001, CaseVersionID: 5,
		ContentHash: hash, Round: 1, Verdict: design.VerdictFail,
	})
	budget := &fakeReworkBudgetReader{terminal: 1} // the rework died: budget spent
	if got := planner.SetReworkBudgetReader(budget); got != planner {
		t.Fatal("SetReworkBudgetReader must return the planner")
	}
	if err := planner.handleJudgeFail(ctx, 1001, 7001); err != nil {
		t.Fatalf("handleJudgeFail: %v", err)
	}
	if items := enqueuer.byType(TaskTypeDesignRework); len(items) != 0 {
		t.Fatalf("rework items enqueued after rework death = %d, want 0 (escalate instead)", len(items))
	}
	if budget.gotVersionID != 1001 || budget.gotCaseID != 7001 {
		t.Fatalf("budget reader queried with (%d,%d), want (1001,7001)", budget.gotVersionID, budget.gotCaseID)
	}
	svc := NewJudgmentService(judgments, repo, &fakeIDs{}, newFixedClock(), nil, settings).
		SetReworkBudgetReader(budget)
	state, err := svc.ReviewStateFor(ctx, 1001, 7001, 5)
	if err != nil {
		t.Fatalf("ReviewStateFor: %v", err)
	}
	if state != design.ReviewStateEscalatedFail {
		t.Fatalf("review state = %q, want escalated_fail", state)
	}
}

// TestHandleJudgeFailReworkBudgetUnwiredOrInFlight pins the two boundaries of
// the escalation budget: without the reader the planner keeps today's
// round-derived behavior, and an in-flight (non-terminal) rework still leaves
// budget for the round it is working on.
func TestHandleJudgeFailReworkBudgetUnwiredOrInFlight(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reader ReworkBudgetReader
	}{
		{"nil reader falls back to CountReworks", nil},
		{"in-flight rework has not spent the budget", &fakeReworkBudgetReader{terminal: 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			planner, repo, enqueuer, judgments, settings := newJudgeSchedulerFixture()
			settings[maxReworkRoundsKey] = 1
			ctx := context.Background()
			hash := strings.Repeat("e", 64)
			repo.caseDataByID = map[int64]design.CaseData{
				7001: {
					Case:        design.CaseEntity{ID: 7001},
					Requirement: design.RequirementRef{ID: 10, VersionID: 1001},
					Selected:    design.CaseVersion{ID: 5, CaseID: 7001, ContentHash: hash},
				},
			}
			judgments.judgments = append(judgments.judgments, design.Judgment{
				ID: 1, VersionID: 1001, RequirementID: 10, CaseID: 7001, CaseVersionID: 5,
				ContentHash: hash, Round: 1, Verdict: design.VerdictFail,
			})
			planner.SetReworkBudgetReader(tc.reader)
			if err := planner.handleJudgeFail(ctx, 1001, 7001); err != nil {
				t.Fatalf("handleJudgeFail: %v", err)
			}
			if items := enqueuer.byType(TaskTypeDesignRework); len(items) != 1 {
				t.Fatalf("rework items = %d, want 1 (budget still available)", len(items))
			}
		})
	}
}

// TestHandleJudgeFailReworkBudgetReadFailure pins constitution VII.6: an
// unreadable rework budget must surface as an error, never as a silent
// "budget available" that enqueues another doomed rework.
func TestHandleJudgeFailReworkBudgetReadFailure(t *testing.T) {
	planner, repo, enqueuer, judgments, settings := newJudgeSchedulerFixture()
	settings[maxReworkRoundsKey] = 1
	ctx := context.Background()
	hash := strings.Repeat("f", 64)
	repo.caseDataByID = map[int64]design.CaseData{
		7001: {
			Case:        design.CaseEntity{ID: 7001},
			Requirement: design.RequirementRef{ID: 10, VersionID: 1001},
			Selected:    design.CaseVersion{ID: 5, CaseID: 7001, ContentHash: hash},
		},
	}
	judgments.judgments = append(judgments.judgments, design.Judgment{
		ID: 1, VersionID: 1001, RequirementID: 10, CaseID: 7001, CaseVersionID: 5,
		ContentHash: hash, Round: 1, Verdict: design.VerdictFail,
	})
	planner.SetReworkBudgetReader(&fakeReworkBudgetReader{err: fmt.Errorf("work item store down")})
	if err := planner.handleJudgeFail(ctx, 1001, 7001); err == nil {
		t.Fatal("an unreadable rework budget must fail closed")
	}
	if items := enqueuer.byType(TaskTypeDesignRework); len(items) != 0 {
		t.Fatalf("failed budget read must not enqueue rework: %d", len(items))
	}
}

// TestHandleJudgeFailReworkDeathBudgetTwoEscalates is the reviewer's
// max_rework_rounds=2 probe (review fix round 1): one dead rework spends only
// one of two budget points, so ReworkAllowed(1,2) is still true — but the rework
// request key rework:<case>:<failedVersion> has no generation and Ensure never
// revives a dead row, so re-Ensuring creates nothing. Without the terminal-rework
// branch the planner silently does nothing forever (permanent pending).
func TestHandleJudgeFailReworkDeathBudgetTwoEscalates(t *testing.T) {
	planner, repo, enqueuer, judgments, settings := newJudgeSchedulerFixture()
	settings[maxReworkRoundsKey] = 2
	ctx := context.Background()
	hash := strings.Repeat("g", 64)
	repo.caseDataByID = map[int64]design.CaseData{
		7001: {
			Case:        design.CaseEntity{ID: 7001},
			Requirement: design.RequirementRef{ID: 10, VersionID: 1001},
			Selected:    design.CaseVersion{ID: 5, CaseID: 7001, ContentHash: hash},
		},
	}
	judgments.judgments = append(judgments.judgments, design.Judgment{
		ID: 1, VersionID: 1001, RequirementID: 10, CaseID: 7001, CaseVersionID: 5,
		ContentHash: hash, Round: 1, Verdict: design.VerdictFail,
	})
	budget := &fakeReworkBudgetReader{terminal: 1, terminalForVersion: true}
	planner.SetReworkBudgetReader(budget)
	// Three passes: the dead row occupies the dedupe key, so Ensure would report
	// created=false every time and nothing would ever progress.
	for pass := 0; pass < 3; pass++ {
		if err := planner.handleJudgeFail(ctx, 1001, 7001); err != nil {
			t.Fatalf("handleJudgeFail pass %d: %v", pass, err)
		}
	}
	if items := enqueuer.byType(TaskTypeDesignRework); len(items) != 0 {
		t.Fatalf("rework items = %d, want 0 (the rework for this fail is dead; escalate)", len(items))
	}
	if budget.gotTerminalVersion != 5 {
		t.Fatalf("terminal-rework read asked for version %d, want the failed/current version 5", budget.gotTerminalVersion)
	}
	svc := NewJudgmentService(judgments, repo, &fakeIDs{}, newFixedClock(), nil, settings).
		SetReworkBudgetReader(budget)
	state, err := svc.ReviewStateFor(ctx, 1001, 7001, 5)
	if err != nil {
		t.Fatalf("ReviewStateFor: %v", err)
	}
	if state != design.ReviewStateEscalatedFail {
		t.Fatalf("review state = %q, want escalated_fail", state)
	}
	if _, err := svc.Override(ctx, 1001, 7001, 42, "打回死亡，人工决断"); err != nil {
		t.Fatalf("override must be reachable: %v", err)
	}
}

// TestHandleJudgeFailLiveReworkBudgetTwoStillReworks is the discriminating
// counterpart: with budget 2 the done rework is bound to an OLDER version (5),
// not the current fail (6), so the terminal-rework branch does not fire and the
// planner still enqueues the rework for the new failed version. A done rework
// bound to the CURRENT fail IS terminal and would escalate — the version
// binding, not "doneness", is what keeps the normal multi-round loop here.
func TestHandleJudgeFailLiveReworkBudgetTwoStillReworks(t *testing.T) {
	planner, repo, enqueuer, judgments, settings := newJudgeSchedulerFixture()
	settings[maxReworkRoundsKey] = 2
	ctx := context.Background()
	hash := strings.Repeat("h", 64)
	repo.caseDataByID = map[int64]design.CaseData{
		7001: {
			Case:        design.CaseEntity{ID: 7001},
			Requirement: design.RequirementRef{ID: 10, VersionID: 1001},
			Selected:    design.CaseVersion{ID: 6, CaseID: 7001, ContentHash: hash},
		},
	}
	// Round 1 fail on the OLD version 5 (its rework is done), round 2 fail on the
	// CURRENT version 6: the rework for version 6 has not been requested yet.
	judgments.judgments = append(judgments.judgments,
		design.Judgment{
			ID: 1, VersionID: 1001, RequirementID: 10, CaseID: 7001, CaseVersionID: 5,
			ContentHash: strings.Repeat("i", 64), Round: 1, Verdict: design.VerdictFail,
		},
		design.Judgment{
			ID: 2, VersionID: 1001, RequirementID: 10, CaseID: 7001, CaseVersionID: 6,
			ContentHash: hash, Round: 2, Verdict: design.VerdictFail,
		},
	)
	// terminal 1 (the done rework of version 5) < budget 2, and no terminal rework
	// bound to the current fail.
	planner.SetReworkBudgetReader(&fakeReworkBudgetReader{terminal: 1, terminalForVersion: false})
	if err := planner.handleJudgeFail(ctx, 1001, 7001); err != nil {
		t.Fatalf("handleJudgeFail: %v", err)
	}
	reworks := enqueuer.byType(TaskTypeDesignRework)
	if len(reworks) != 1 {
		t.Fatalf("rework items = %d, want 1 (round 3 rework still allowed)", len(reworks))
	}
	if taskPayloadString(reworks[0], "round") != "3" {
		t.Fatalf("rework payload round = %v, want 3", reworks[0].Payload)
	}
	if reworks[0].DedupeKey != "rework:7001:6" {
		t.Fatalf("rework dedupe key = %q, want the new failed version's key", reworks[0].DedupeKey)
	}
}

// TestHandleJudgeFailDeadReworkReadFailure pins fail-closed on the terminal-rework
// branch (the test name is historical; the predicate is now terminal, not dead-only).
func TestHandleJudgeFailDeadReworkReadFailure(t *testing.T) {
	planner, repo, enqueuer, judgments, settings := newJudgeSchedulerFixture()
	settings[maxReworkRoundsKey] = 2
	ctx := context.Background()
	hash := strings.Repeat("j", 64)
	repo.caseDataByID = map[int64]design.CaseData{
		7001: {
			Case:        design.CaseEntity{ID: 7001},
			Requirement: design.RequirementRef{ID: 10, VersionID: 1001},
			Selected:    design.CaseVersion{ID: 5, CaseID: 7001, ContentHash: hash},
		},
	}
	judgments.judgments = append(judgments.judgments, design.Judgment{
		ID: 1, VersionID: 1001, RequirementID: 10, CaseID: 7001, CaseVersionID: 5,
		ContentHash: hash, Round: 1, Verdict: design.VerdictFail,
	})
	planner.SetReworkBudgetReader(&fakeReworkBudgetReader{terminal: 1, terminalErr: fmt.Errorf("work item store down")})
	if err := planner.handleJudgeFail(ctx, 1001, 7001); err == nil {
		t.Fatal("an unreadable terminal-rework fact must fail closed")
	}
	if items := enqueuer.byType(TaskTypeDesignRework); len(items) != 0 {
		t.Fatalf("failed terminal-rework read must not enqueue rework: %d", len(items))
	}
}

// TestHandleJudgeFailReworkDoneNoRevisionEscalates is the DEV-E4 reproduction at
// the scheduling layer (review fix round 2): the rework completed DONE but
// proposed no revision, so the latest fail is still bound to the current version
// and its generation-less request key is occupied by a terminal row. With
// design.max_rework_rounds=3 the budget test still allows a rework
// (max(1,2)=2 < 3) that can never be created — escalate instead.
func TestHandleJudgeFailReworkDoneNoRevisionEscalates(t *testing.T) {
	planner, repo, enqueuer, judgments, settings := newJudgeSchedulerFixture()
	settings[maxReworkRoundsKey] = 3
	ctx := context.Background()
	hash := strings.Repeat("k", 64)
	repo.caseDataByID = map[int64]design.CaseData{
		7001: {
			Case:        design.CaseEntity{ID: 7001},
			Requirement: design.RequirementRef{ID: 10, VersionID: 1001},
			Selected:    design.CaseVersion{ID: 6, CaseID: 7001, ContentHash: hash},
		},
	}
	judgments.judgments = append(judgments.judgments,
		design.Judgment{
			ID: 1, VersionID: 1001, RequirementID: 10, CaseID: 7001, CaseVersionID: 5,
			ContentHash: strings.Repeat("l", 64), Round: 1, Verdict: design.VerdictFail,
		},
		design.Judgment{
			ID: 2, VersionID: 1001, RequirementID: 10, CaseID: 7001, CaseVersionID: 6,
			ContentHash: hash, Round: 2, Verdict: design.VerdictFail,
		},
	)
	// Two terminal reworks (both done), the second produced NO new version; the
	// one bound to the current fail is terminal, not dead.
	budget := &fakeReworkBudgetReader{terminal: 2, terminalForVersion: true}
	planner.SetReworkBudgetReader(budget)
	for pass := 0; pass < 3; pass++ {
		if err := planner.handleJudgeFail(ctx, 1001, 7001); err != nil {
			t.Fatalf("handleJudgeFail pass %d: %v", pass, err)
		}
	}
	if items := enqueuer.byType(TaskTypeDesignRework); len(items) != 0 {
		t.Fatalf("rework items = %d, want 0 (the rework for this fail is terminal; escalate)", len(items))
	}
	if budget.gotTerminalVersion != 6 {
		t.Fatalf("terminal-rework read asked for version %d, want the current/failed version 6", budget.gotTerminalVersion)
	}
	svc := NewJudgmentService(judgments, repo, &fakeIDs{}, newFixedClock(), nil, settings).
		SetReworkBudgetReader(budget)
	state, err := svc.ReviewStateFor(ctx, 1001, 7001, 6)
	if err != nil {
		t.Fatalf("ReviewStateFor: %v", err)
	}
	if state != design.ReviewStateEscalatedFail {
		t.Fatalf("review state = %q, want escalated_fail", state)
	}
	if _, err := svc.Override(ctx, 1001, 7001, 42, "打回未产出修订，人工决断"); err != nil {
		t.Fatalf("override must be reachable: %v", err)
	}
}

// judgeRefs builds JudgeCaseRef values from (caseID, caseVersionID) pairs.
func judgeRefs(pairs ...[2]int64) []JudgeCaseRef {
	refs := make([]JudgeCaseRef, 0, len(pairs))
	for _, pair := range pairs {
		refs = append(refs, JudgeCaseRef{
			CaseID: pair[0], CaseVersionID: pair[1], ContentHash: strings.Repeat("a", 64),
		})
	}
	return refs
}

// seedJudgeGeneration stores one terminal design.judge task for a draft set.
func seedJudgeGeneration(enqueuer *fakeEnqueuer, anchor, dedupe string) {
	enqueuer.seedDone(TaskInput{
		VersionID: 1001, Team: TeamDesign, Type: TaskTypeDesignJudge,
		DedupeKey: dedupe,
		Payload:   map[string]any{"requirement_version_id": "100", "draft_case_version_ids": anchor},
	})
}

// judgeGenerationInput is the base judge TaskInput for generation tests.
func judgeGenerationInput() TaskInput {
	return TaskInput{
		VersionID: 1001, Team: TeamDesign, Type: TaskTypeDesignJudge,
		DedupeKey: "judge:10:100",
		Payload:   map[string]any{"requirement_id": "10", "requirement_version_id": "100"},
	}
}

// TestJudgeGenerationExhaustionWritesFact pins the judge-run-failure degradation
// path (spec SC-E4, controller ruling T8-5): once the bounded generation budget
// of the current draft set is spent, EnsureJudgeGeneration creates nothing and
// records the judge_exhausted escalation fact instead of a permanent pending.
func TestJudgeGenerationExhaustionWritesFact(t *testing.T) {
	_, _, enqueuer, _, _ := newJudgeSchedulerFixture()
	ctx := context.Background()
	for i := 0; i < fakeJudgeGenerationLimit; i++ {
		seedJudgeGeneration(enqueuer, "5", fmt.Sprintf("judge:10:100:judge:%d", i))
	}
	drafts := judgeRefs([2]int64{7001, 5})
	created, err := enqueuer.EnsureJudgeGeneration(ctx, judgeGenerationInput(), drafts, drafts)
	if err != nil {
		t.Fatalf("EnsureJudgeGeneration: %v", err)
	}
	if created {
		t.Fatal("exhausted budget must not create a judge generation")
	}
	if !enqueuer.judgeExhausted[[2]int64{7001, 5}] {
		t.Fatal("exhaustion must write the judge_exhausted fact bound to the case version")
	}
}

// TestJudgeGenerationMultiCaseExhaustionWritesEveryDraft pins review-fix ruling
// 3: on exhaustion the judge_exhausted fact is written for EVERY unjudged
// current draft case version of the requirement, and each derives
// judge_unavailable (no draft stranded in permanent pending).
func TestJudgeGenerationMultiCaseExhaustionWritesEveryDraft(t *testing.T) {
	_, _, enqueuer, judgments, settings := newJudgeSchedulerFixture()
	ctx := context.Background()
	for i := 0; i < fakeJudgeGenerationLimit; i++ {
		seedJudgeGeneration(enqueuer, "5,6", fmt.Sprintf("judge:10:100:judge:%d", i))
	}
	drafts := judgeRefs([2]int64{7001, 5}, [2]int64{7002, 6})
	created, err := enqueuer.EnsureJudgeGeneration(ctx, judgeGenerationInput(), drafts, drafts)
	if err != nil {
		t.Fatalf("EnsureJudgeGeneration: %v", err)
	}
	if created {
		t.Fatal("exhausted budget must not create a judge generation")
	}
	for _, pair := range [][2]int64{{7001, 5}, {7002, 6}} {
		if !enqueuer.judgeExhausted[pair] {
			t.Fatalf("exhaustion must cover every draft; missing %v (facts=%v)", pair, enqueuer.judgeExhausted)
		}
	}
	// Each escalated draft derives judge_unavailable through the T6 projection
	// (ReviewStateFor reads only facts; no case reader is needed).
	svc := NewJudgmentService(judgments, nil, &fakeIDs{}, newFixedClock(), nil, settings)
	for _, pair := range [][2]int64{{7001, 5}, {7002, 6}} {
		if _, err := judgments.InsertJudgeExhausted(ctx, 1001, 10, pair[0], pair[1]); err != nil {
			t.Fatalf("seed escalation fact: %v", err)
		}
		state, err := svc.ReviewStateFor(ctx, 1001, pair[0], pair[1])
		if err != nil {
			t.Fatalf("ReviewStateFor: %v", err)
		}
		if state != design.ReviewStateJudgeUnavailable {
			t.Fatalf("draft %v review_state = %q, want judge_unavailable", pair, state)
		}
	}
}

// TestJudgeGenerationExhaustionSparesJudgedDrafts pins that exhaustion facts go
// only to drafts WITHOUT a matching judgment: a draft already judged pass keeps
// its pass (T6 DeriveReviewState ranks judge_exhausted above pass, so a false
// escalation fact over a real pass would demote a reviewed case).
func TestJudgeGenerationExhaustionSparesJudgedDrafts(t *testing.T) {
	_, _, enqueuer, _, _ := newJudgeSchedulerFixture()
	ctx := context.Background()
	for i := 0; i < fakeJudgeGenerationLimit; i++ {
		seedJudgeGeneration(enqueuer, "5,6", fmt.Sprintf("judge:10:100:judge:%d", i))
	}
	drafts := judgeRefs([2]int64{7001, 5}, [2]int64{7002, 6})
	pending := judgeRefs([2]int64{7001, 5}) // 7002/6 already judged pass
	created, err := enqueuer.EnsureJudgeGeneration(ctx, judgeGenerationInput(), drafts, pending)
	if err != nil {
		t.Fatalf("EnsureJudgeGeneration: %v", err)
	}
	if created {
		t.Fatal("exhausted budget must not create a judge generation")
	}
	if !enqueuer.judgeExhausted[[2]int64{7001, 5}] {
		t.Fatal("the unjudged draft must be escalated")
	}
	if enqueuer.judgeExhausted[[2]int64{7002, 6}] {
		t.Fatal("a judged-pass draft must not get a false judge_exhausted fact")
	}
}

// TestJudgeGenerationResetsPerCaseVersion pins that the generation budget is
// keyed on the current-draft case-version SET (review-fix ruling 4): a new
// case_version_id among the drafts -- a revised version or an added draft --
// starts a fresh budget (spec SC-E4 v1.2 dead-end fix).
func TestJudgeGenerationResetsPerCaseVersion(t *testing.T) {
	ctx := context.Background()
	exhaustedFixture := func() *fakeEnqueuer {
		_, _, enqueuer, _, _ := newJudgeSchedulerFixture()
		for i := 0; i < fakeJudgeGenerationLimit; i++ {
			seedJudgeGeneration(enqueuer, "5", fmt.Sprintf("judge:10:100:judge:%d", i))
		}
		return enqueuer
	}
	t.Run("revised version", func(t *testing.T) {
		enqueuer := exhaustedFixture()
		drafts := judgeRefs([2]int64{7001, 6})
		created, err := enqueuer.EnsureJudgeGeneration(ctx, judgeGenerationInput(), drafts, drafts)
		if err != nil {
			t.Fatalf("EnsureJudgeGeneration: %v", err)
		}
		if !created {
			t.Fatal("a revised case version must get a fresh judge generation budget")
		}
		if enqueuer.judgeExhausted[[2]int64{7001, 6}] {
			t.Fatal("a fresh case version must not be marked exhausted")
		}
	})
	t.Run("added draft changes the set", func(t *testing.T) {
		enqueuer := exhaustedFixture()
		drafts := judgeRefs([2]int64{7001, 5}, [2]int64{7002, 7})
		created, err := enqueuer.EnsureJudgeGeneration(ctx, judgeGenerationInput(), drafts, drafts)
		if err != nil {
			t.Fatalf("EnsureJudgeGeneration: %v", err)
		}
		if !created {
			t.Fatal("a changed draft set must get a fresh judge generation budget")
		}
		// The reset budget must not reuse an occupied dedupe key (the old set's
		// terminal base key would silently dedupe the new set into stranding).
		judges := enqueuer.byType(TaskTypeDesignJudge)
		last := judges[len(judges)-1]
		if last.DedupeKey == "judge:10:100" {
			t.Fatalf("reset generation reused the occupied base key: %v", sortedTaskDedupeKeys(judges))
		}
	})
}

// TestJudgeDraftSetKey pins the generation anchor format: case_version_ids
// sorted NUMERICALLY ascending, deduplicated, comma-joined (the single
// authority both the planner payload writer and the adapter matcher use).
func TestJudgeDraftSetKey(t *testing.T) {
	refs := []JudgeCaseRef{
		{CaseID: 1, CaseVersionID: 10}, {CaseID: 2, CaseVersionID: 2},
		{CaseID: 3, CaseVersionID: 2}, {CaseID: 4, CaseVersionID: 9},
	}
	if got := JudgeDraftSetKey(refs); got != "2,9,10" {
		t.Fatalf("JudgeDraftSetKey = %q, want 2,9,10 (numeric order, deduped)", got)
	}
	if got := JudgeDraftSetKey(nil); got != "" {
		t.Fatalf("empty set key = %q, want empty", got)
	}
}

// TestNeedsACRepair pins the AC-gap predicate (spec SC-E4 partial-coverage
// livelock fix): a requirement with usable drafts but an incomplete union still
// needs repair, while a complete union does not.
func TestNeedsACRepair(t *testing.T) {
	reqACs := []string{"AC-1", "AC-2"}
	covered := design.RequirementCoverage{ScenarioCount: 1, DraftCount: 1}
	if !NeedsACRepair(covered, []string{"AC-1"}, reqACs) {
		t.Fatal("draft>0 with an incomplete union must need AC repair")
	}
	if NeedsACRepair(covered, []string{"AC-1", "AC-2"}, reqACs) {
		t.Fatal("a complete union must not need AC repair")
	}
	// The legacy zero-coverage trigger is preserved.
	if !NeedsACRepair(design.RequirementCoverage{}, nil, reqACs) {
		t.Fatal("zero coverage must still need repair")
	}
}
