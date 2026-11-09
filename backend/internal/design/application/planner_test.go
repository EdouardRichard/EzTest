package designapp

import (
	"context"
	"errors"
	"fmt"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

// assertNoDesignRoundTasks pins D-04: v0.22 removed the design.round runner
// profile and makes it fail closed, so every design.round work item the planner
// enqueues can only end as an invalid_work_type job card in the design thread.
// The round state/projection is owned by the design_round row and the
// design.round_started event, never by a work item.
func assertNoDesignRoundTasks(t *testing.T, enqueuer *fakeEnqueuer) {
	t.Helper()
	if tasks := enqueuer.byType(TaskTypeDesignRound); len(tasks) != 0 {
		t.Fatalf("planner enqueued %d design.round task(s) %v; the work type has no runner profile",
			len(tasks), sortedTaskDedupeKeys(tasks))
	}
}

func TestStartRoundActivatesAllPendingAndCreatesTasks(t *testing.T) {
	planner, repo, enqueuer, teamRoom, _, versions, _ := newPlannerFixture()
	repo.clarified = []design.RequirementRef{
		plannerRequirement(3001, 3002),
		plannerRequirement(3005, 3006),
	}

	round, err := planner.StartRound(context.Background(), 1001, 9001, 42)
	if err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	if round.ID == 0 || round.RoundNo != 1 || round.Activation != design.ActivationManual {
		t.Fatalf("round = %+v", round)
	}
	if len(round.RequirementIDs) != 2 {
		t.Fatalf("round requirements = %v", round.RequirementIDs)
	}
	if len(repo.activeScopes) != 2 {
		t.Fatalf("active scopes = %d", len(repo.activeScopes))
	}
	for _, scope := range repo.activeScopes {
		if scope.ActivatedByRoundID == nil || *scope.ActivatedByRoundID != round.ID {
			t.Fatalf("scope = %+v", scope)
		}
		if scope.Status != design.ScopeStatusActive {
			t.Fatalf("scope status = %q", scope.Status)
		}
	}
	assertNoDesignRoundTasks(t, enqueuer)
	requirementTasks := enqueuer.byType("design.requirement")
	if len(requirementTasks) != 2 {
		t.Fatalf("requirement tasks = %d", len(requirementTasks))
	}
	if requirementTasks[0].DedupeKey != "design:3001:3002" {
		t.Fatalf("dedupe keys = %v", sortedTaskDedupeKeys(requirementTasks))
	}
	if len(versions.calls) != 1 || versions.calls[0] != 1001 {
		t.Fatalf("version starter calls = %v", versions.calls)
	}
	if len(teamRoom.posts) != 1 {
		t.Fatalf("team posts = %d", len(teamRoom.posts))
	}
	if teamRoom.posts[0].Kind != "status" || teamRoom.posts[0].Team != "design" {
		t.Fatalf("team post = %+v", teamRoom.posts[0])
	}
}

func TestStartRoundIdempotent(t *testing.T) {
	planner, repo, enqueuer, _, _, versions, _ := newPlannerFixture()
	repo.clarified = []design.RequirementRef{plannerRequirement(3001, 3002)}

	first, err := planner.StartRound(context.Background(), 1001, 9001, 42)
	if err != nil {
		t.Fatalf("first StartRound: %v", err)
	}
	taskCount := len(enqueuer.tasks)
	second, err := planner.StartRound(context.Background(), 1001, 9001, 42)
	if err != nil {
		t.Fatalf("second StartRound: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("round id changed: %d vs %d", first.ID, second.ID)
	}
	if len(repo.rounds) != 1 {
		t.Fatalf("rounds = %d", len(repo.rounds))
	}
	if len(repo.activeScopes) != 1 {
		t.Fatalf("scopes = %d", len(repo.activeScopes))
	}
	if len(enqueuer.tasks) != taskCount {
		t.Fatalf("tasks grew on idempotent StartRound: %v", sortedTaskDedupeKeys(enqueuer.tasks))
	}
	if len(versions.calls) != 1 {
		t.Fatalf("MarkRunning calls = %d, want 1", len(versions.calls))
	}
}

func TestAutoJoinIntoLatestRound(t *testing.T) {
	planner, repo, enqueuer, _, conversations, _, _ := newPlannerFixture()
	repo.hasConversation = true
	repo.latestConversation = 9001
	roundID := int64(8001)
	repo.rounds = []design.Round{{
		ID: roundID, VersionID: 1001, ConversationID: 9001, Activation: design.ActivationManual,
		RequirementIDs: []int64{3001}, RequirementVersionIDs: []int64{3002}, Status: design.RoundStatusActive,
	}}
	repo.activeScopes = []design.Scope{{
		ID: 6001, VersionID: 1001, RequirementID: 3001, ActivatedByRoundID: &roundID,
		Status: design.ScopeStatusActive,
	}}
	repo.clarified = []design.RequirementRef{
		plannerRequirement(3001, 3002),
		plannerRequirement(3005, 3006),
	}
	repo.coverage = []design.RequirementCoverage{{
		Requirement: plannerRequirement(3001, 3002), ScenarioCount: 1, DraftCount: 1,
	}}

	if err := planner.ScanVersion(context.Background(), 1001); err != nil {
		t.Fatalf("ScanVersion: %v", err)
	}
	if conversations.calls != 0 {
		t.Fatalf("auto-created a conversation despite existing design conversation")
	}
	if len(repo.rounds) != 1 {
		t.Fatalf("rounds = %d, want no new round", len(repo.rounds))
	}
	if len(repo.activeScopes) != 2 {
		t.Fatalf("active scopes = %d", len(repo.activeScopes))
	}
	joined := false
	for _, scope := range repo.activeScopes {
		if scope.RequirementID == 3005 {
			joined = true
			if scope.ActivatedByRoundID == nil || *scope.ActivatedByRoundID != roundID {
				t.Fatalf("joined scope = %+v", scope)
			}
		}
	}
	if !joined {
		t.Fatal("pending requirement was not joined into the latest round")
	}
	if len(repo.updatedRoundIDs) != 1 || repo.updatedRoundIDs[0] != roundID {
		t.Fatalf("round snapshot updates = %v", repo.updatedRoundIDs)
	}
	if tasks := enqueuer.byType("design.requirement"); len(tasks) != 1 || tasks[0].DedupeKey != "design:3005:3006" {
		t.Fatalf("requirement tasks = %v", sortedTaskDedupeKeys(enqueuer.tasks))
	}
}

func TestAutoDesignOnlyWhenNoConversation(t *testing.T) {
	planner, repo, _, _, conversations, versions, _ := newPlannerFixture()
	repo.clarified = []design.RequirementRef{
		plannerRequirement(3001, 3002),
		plannerRequirement(3005, 3006),
	}
	if err := planner.ScanVersion(context.Background(), 1001); err != nil {
		t.Fatalf("ScanVersion without auto flag: %v", err)
	}
	if conversations.calls != 0 || len(repo.rounds) != 0 {
		t.Fatalf("created design conversation/round with auto flag off")
	}

	repo.autoDesign = true
	if err := planner.ScanVersion(context.Background(), 1001); err != nil {
		t.Fatalf("ScanVersion auto: %v", err)
	}
	if conversations.calls != 1 {
		t.Fatalf("conversation creates = %d", conversations.calls)
	}
	if !autoTitleHasDate(conversations.titles[0]) {
		t.Fatalf("auto title = %q", conversations.titles[0])
	}
	if len(repo.rounds) != 1 || repo.rounds[0].Activation != design.ActivationAutoNew {
		t.Fatalf("rounds = %+v", repo.rounds)
	}
	if len(repo.activeScopes) != 2 {
		t.Fatalf("scopes = %d", len(repo.activeScopes))
	}
	if len(versions.calls) != 1 {
		t.Fatalf("MarkRunning calls = %v", versions.calls)
	}

	// A second scan sees the auto-created conversation and must not duplicate.
	if err := planner.ScanVersion(context.Background(), 1001); err != nil {
		t.Fatalf("second ScanVersion: %v", err)
	}
	if conversations.calls != 1 || len(repo.rounds) != 1 {
		t.Fatalf("second scan duplicated auto conversation: calls=%d rounds=%d", conversations.calls, len(repo.rounds))
	}
}

// TestStartStageRoundEnsuresConversationAndTasks pins Task 65's design.cases
// consumer: the requirement hard gate confirms the scope, and the stage-entry
// job must open the design round without depending on the legacy auto-design
// toggle. It creates the design conversation when missing, scopes the confirmed
// requirements, and enqueues the case-author tasks with the conversation
// association so their proposal calls land in the design thread.
func TestStartStageRoundEnsuresConversationAndTasks(t *testing.T) {
	planner, repo, enqueuer, _, conversations, versions, _ := newPlannerFixture()
	repo.clarified = []design.RequirementRef{
		plannerRequirement(3001, 3002),
		plannerRequirement(3005, 3006),
	}

	conversationID, err := planner.StartStageRound(context.Background(), 1001, []int64{3001, 3005})
	if err != nil {
		t.Fatalf("StartStageRound: %v", err)
	}
	if conversationID == 0 || conversations.calls != 1 || conversations.titles[0] == "" {
		t.Fatalf("conversation = %d calls=%d titles=%v", conversationID, conversations.calls, conversations.titles)
	}
	if len(repo.rounds) != 1 || repo.rounds[0].ConversationID != conversationID {
		t.Fatalf("rounds = %+v", repo.rounds)
	}
	if len(repo.rounds[0].RequirementIDs) != 2 {
		t.Fatalf("round requirements = %v", repo.rounds[0].RequirementIDs)
	}
	if len(repo.activeScopes) != 2 {
		t.Fatalf("active scopes = %d", len(repo.activeScopes))
	}
	requirementTasks := enqueuer.byType("design.requirement")
	if len(requirementTasks) != 2 {
		t.Fatalf("requirement tasks = %v", sortedTaskDedupeKeys(enqueuer.tasks))
	}
	for _, task := range requirementTasks {
		if taskPayloadString(task, "conversation_id") != fmt.Sprint(conversationID) {
			t.Fatalf("requirement task conversation association = %v", task.Payload)
		}
		if taskPayloadString(task, "round_id") != fmt.Sprint(repo.rounds[0].ID) {
			t.Fatalf("requirement task round association = %v", task.Payload)
		}
	}
	// D-04: the design.round work item was removed together with its v0.22
	// runner profile. The stage entry must only enqueue the per-requirement
	// authoring task; the round itself is carried by the round row/events.
	assertNoDesignRoundTasks(t, enqueuer)
	if len(versions.calls) != 1 || versions.calls[0] != 1001 {
		t.Fatalf("version starter calls = %v", versions.calls)
	}

	// A repeated stage entry (R49 recovery or a replayed event) must reuse the
	// same conversation/round and add no tasks.
	taskCount := len(enqueuer.tasks)
	again, err := planner.StartStageRound(context.Background(), 1001, []int64{3001, 3005})
	if err != nil {
		t.Fatalf("second StartStageRound: %v", err)
	}
	if again != conversationID {
		t.Fatalf("conversation changed: %d vs %d", again, conversationID)
	}
	if conversations.calls != 1 || len(repo.rounds) != 1 {
		t.Fatalf("repeat duplicated conversation/round: calls=%d rounds=%d", conversations.calls, len(repo.rounds))
	}
	if len(enqueuer.tasks) != taskCount {
		t.Fatalf("repeat duplicated tasks: %v", sortedTaskDedupeKeys(enqueuer.tasks))
	}
}

// TestPlannerNeverEnqueuesDesignRound pins D-04 across every activation path.
// The manual round, the design.cases stage entry, the D25 auto-start and the
// reconciler's round repair each used to enqueue one design.round work item per
// conversation; because v0.22 removed that runner profile the item failed with
// invalid_work_type on every single round, leaving a dead job card in the
// thread. Requirement-level authoring tasks are the only design work items.
func TestPlannerNeverEnqueuesDesignRound(t *testing.T) {
	ctx := context.Background()

	t.Run("manual round", func(t *testing.T) {
		planner, repo, enqueuer, _, _, _, _ := newPlannerFixture()
		repo.clarified = []design.RequirementRef{plannerRequirement(3001, 3002)}

		if _, err := planner.StartRound(ctx, 1001, 9001, 42); err != nil {
			t.Fatalf("StartRound: %v", err)
		}
		if tasks := enqueuer.byType(TaskTypeDesignRequirement); len(tasks) != 1 {
			t.Fatalf("requirement tasks = %v", sortedTaskDedupeKeys(enqueuer.tasks))
		}
		assertNoDesignRoundTasks(t, enqueuer)
	})

	t.Run("stage entry round", func(t *testing.T) {
		planner, repo, enqueuer, _, _, _, _ := newPlannerFixture()
		repo.clarified = []design.RequirementRef{plannerRequirement(3001, 3002)}

		if _, err := planner.StartStageRound(ctx, 1001, []int64{3001}); err != nil {
			t.Fatalf("StartStageRound: %v", err)
		}
		if tasks := enqueuer.byType(TaskTypeDesignRequirement); len(tasks) != 1 {
			t.Fatalf("requirement tasks = %v", sortedTaskDedupeKeys(enqueuer.tasks))
		}
		assertNoDesignRoundTasks(t, enqueuer)
	})

	t.Run("auto-start round", func(t *testing.T) {
		planner, repo, enqueuer, _, _, _, _ := newPlannerFixture()
		repo.clarified = []design.RequirementRef{plannerRequirement(3001, 3002)}
		repo.autoDesign = true

		if err := planner.ScanVersion(ctx, 1001); err != nil {
			t.Fatalf("ScanVersion: %v", err)
		}
		if tasks := enqueuer.byType(TaskTypeDesignRequirement); len(tasks) != 1 {
			t.Fatalf("requirement tasks = %v", sortedTaskDedupeKeys(enqueuer.tasks))
		}
		assertNoDesignRoundTasks(t, enqueuer)
	})

	t.Run("round repair", func(t *testing.T) {
		planner, repo, enqueuer, _, _, _, _ := newPlannerFixture()
		repo.hasConversation = true
		repo.latestConversation = 9001
		repo.clarified = []design.RequirementRef{plannerRequirement(3001, 3002)}

		if err := planner.ScanVersion(ctx, 1001); err != nil {
			t.Fatalf("ScanVersion: %v", err)
		}
		if len(repo.rounds) != 1 {
			t.Fatalf("repaired rounds = %d, want the conversation's missing round", len(repo.rounds))
		}
		if tasks := enqueuer.byType(TaskTypeDesignRequirement); len(tasks) != 1 {
			t.Fatalf("requirement tasks = %v", sortedTaskDedupeKeys(enqueuer.tasks))
		}
		assertNoDesignRoundTasks(t, enqueuer)
	})
}

func TestPendingCountExcludesActiveScope(t *testing.T) {
	planner, repo, _, _, _, _, _ := newPlannerFixture()
	repo.clarified = []design.RequirementRef{
		plannerRequirement(3001, 3002),
		plannerRequirement(3005, 3006),
	}
	repo.activeScopes = []design.Scope{{
		ID: 6001, VersionID: 1001, RequirementID: 3001, Status: design.ScopeStatusActive,
	}}
	count, err := planner.CountPendingDesign(context.Background(), 1001)
	if err != nil {
		t.Fatalf("CountPendingDesign: %v", err)
	}
	if count != 1 {
		t.Fatalf("pending = %d, want 1", count)
	}
	repo.caseCounts = design.CaseCounts{Draft: 4, Confirmed: 3, Stale: 2}
	counts, err := planner.CaseCounts(context.Background(), 1001)
	if err != nil {
		t.Fatalf("CaseCounts: %v", err)
	}
	if counts != (CaseCounts{Draft: 4, Confirmed: 3, Stale: 2}) {
		t.Fatalf("case counts = %+v", counts)
	}
}

func TestScanCreatesCriticAndDataPlanOnlyForDrafts(t *testing.T) {
	planner, repo, enqueuer, _, _, _, _ := newPlannerFixture()
	repo.hasConversation = true
	repo.latestConversation = 9001
	repo.rounds = []design.Round{{
		ID: 8001, VersionID: 1001, ConversationID: 9001,
		Activation: design.ActivationManual, Status: design.RoundStatusActive,
	}}
	repo.coverage = []design.RequirementCoverage{
		{Requirement: plannerRequirement(3001, 3002), ScenarioCount: 1, DraftCount: 1, ConfirmedCount: 1},
		{Requirement: plannerRequirement(3005, 3006), ScenarioCount: 1, ConfirmedCount: 1},
		{Requirement: plannerRequirement(3007, 3008), ScenarioCount: 1, StaleCount: 1},
	}

	if err := planner.ScanVersion(context.Background(), 1001); err != nil {
		t.Fatalf("ScanVersion: %v", err)
	}
	criticTasks := enqueuer.byType("design.critic")
	if len(criticTasks) != 2 {
		t.Fatalf("critic tasks = %v", sortedTaskDedupeKeys(enqueuer.tasks))
	}
	dataPlanTasks := enqueuer.byType("design.data_plan")
	if len(dataPlanTasks) != 1 || dataPlanTasks[0].DedupeKey != "dataplan:3001:3002" {
		t.Fatalf("data plan tasks = %v", sortedTaskDedupeKeys(enqueuer.tasks))
	}
	requirementTasks := enqueuer.byType("design.requirement")
	if len(requirementTasks) != 1 || requirementTasks[0].DedupeKey != "design:3007:3008" {
		t.Fatalf("requirement tasks = %v", sortedTaskDedupeKeys(enqueuer.tasks))
	}
	if requirementTasks[0].Payload["reason"] != "coverage_repair" {
		t.Fatalf("redesign payload = %v", requirementTasks[0].Payload)
	}

	// Idempotent: no new tasks on a second scan.
	taskCount := len(enqueuer.tasks)
	if err := planner.ScanVersion(context.Background(), 1001); err != nil {
		t.Fatalf("second ScanVersion: %v", err)
	}
	if len(enqueuer.tasks) != taskCount {
		t.Fatalf("scan duplicated tasks: %v", sortedTaskDedupeKeys(enqueuer.tasks))
	}
}

func TestRequestGapEnsuresScopeAndTask(t *testing.T) {
	planner, repo, enqueuer, _, _, _, _ := newPlannerFixture()
	repo.clarified = []design.RequirementRef{plannerRequirement(3001, 3002)}
	caseID := int64(7001)
	caseVersionID := int64(7002)

	input := GapInput{
		VersionID: 1001, RequirementID: 3001,
		CaseID: &caseID, CaseVersionID: &caseVersionID, Reason: "执行缺口",
	}
	if err := planner.RequestGap(context.Background(), input); err != nil {
		t.Fatalf("RequestGap: %v", err)
	}
	if len(repo.activeScopes) != 1 || repo.activeScopes[0].RequirementID != 3001 {
		t.Fatalf("scopes = %+v", repo.activeScopes)
	}
	tasks := enqueuer.byType("design.gap")
	if len(tasks) != 1 {
		t.Fatalf("gap tasks = %v", sortedTaskDedupeKeys(enqueuer.tasks))
	}
	if tasks[0].DedupeKey != "gap:3001:7002" {
		t.Fatalf("gap dedupe = %q", tasks[0].DedupeKey)
	}
	if taskPayloadString(tasks[0], "case_version_id") != "7002" {
		t.Fatalf("gap payload = %v", tasks[0].Payload)
	}
	// Repeated request is idempotent.
	if err := planner.RequestGap(context.Background(), input); err != nil {
		t.Fatalf("repeat RequestGap: %v", err)
	}
	if len(enqueuer.byType("design.gap")) != 1 {
		t.Fatalf("gap task duplicated")
	}
}

// TestDesignConvergesAfterCoverage pins Task 70's generation-gap repair: a
// design.requirement task can finish "done" without producing any usable case
// (a failed case.graph_propose, a stale graph hash, a model that stops). The
// static dedupe key used to make that empty job block every later attempt, so
// the design pass never reached complete coverage and the version never left
// designing. The reconciler must request one bounded fresh authoring generation
// while coverage is missing, and still only converge once every activated
// requirement really has coverage.
func TestDesignConvergesAfterCoverage(t *testing.T) {
	planner, repo, enqueuer, _, _, _, _ := newPlannerFixture()
	repo.hasConversation = true
	repo.latestConversation = 9001
	roundID := int64(8001)
	repo.rounds = []design.Round{{
		ID: roundID, VersionID: 1001, ConversationID: 9001,
		Activation: design.ActivationAutoNew, Status: design.RoundStatusActive,
		RequirementIDs: []int64{3001, 3005}, RequirementVersionIDs: []int64{3002, 3006},
	}}
	repo.activeScopes = []design.Scope{
		{ID: 6001, VersionID: 1001, RequirementID: 3001, ActivatedByRoundID: &roundID, Status: design.ScopeStatusActive},
		{ID: 6002, VersionID: 1001, RequirementID: 3005, ActivatedByRoundID: &roundID, Status: design.ScopeStatusActive},
	}
	repo.coverage = []design.RequirementCoverage{
		{Requirement: plannerRequirement(3001, 3002), ScenarioCount: 1, DraftCount: 1},
		{Requirement: plannerRequirement(3005, 3006)},
	}
	// The uncovered requirement's original authoring task already finished
	// without writing a scenario (the observed S3 gap).
	enqueuer.seedDone(TaskInput{
		VersionID: 1001, Team: TeamDesign, Type: TaskTypeDesignRequirement,
		DedupeKey: "design:3005:3006",
		Payload: map[string]any{
			"requirement_id": "3005", "requirement_version_id": "3006",
			"reason": "requirement_confirmed", "conversation_id": "9001", "round_id": "8001",
		},
	})
	converger := &fakeDesignConverger{}
	planner.SetDesignConverger(converger)
	ctx := context.Background()

	if err := planner.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile with an empty authoring task: %v", err)
	}
	if len(converger.versionIDs) != 0 {
		t.Fatalf("uncovered requirement converged the version: %v", converger.versionIDs)
	}
	repairTasks := enqueuer.byType(TaskTypeDesignRequirement)
	if len(repairTasks) != 2 {
		t.Fatalf("requirement tasks = %v, want the done task plus one repair generation",
			sortedTaskDedupeKeys(repairTasks))
	}
	repair := repairTasks[1]
	if repair.DedupeKey != "design:3005:3006:repair:1" {
		t.Fatalf("repair dedupe = %q, want design:3005:3006:repair:1 (the done static key must not block)", repair.DedupeKey)
	}
	if taskPayloadString(repair, "reason") != "coverage_repair" {
		t.Fatalf("repair payload = %v", repair.Payload)
	}
	if taskPayloadString(repair, "requirement_version_id") != "3006" {
		t.Fatalf("repair payload = %v", repair.Payload)
	}

	// A repeated pass while the repair generation is open must not stampede it.
	taskCount := len(enqueuer.tasks)
	if err := planner.Reconcile(ctx); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if len(enqueuer.tasks) != taskCount {
		t.Fatalf("open repair generation was duplicated: %v", sortedTaskDedupeKeys(enqueuer.tasks))
	}
	if len(converger.versionIDs) != 0 {
		t.Fatalf("still-uncovered requirement converged the version: %v", converger.versionIDs)
	}

	// Once the repair generation writes real coverage, the next pass converges.
	repo.coverage[1] = design.RequirementCoverage{
		Requirement: plannerRequirement(3005, 3006), ScenarioCount: 1, DraftCount: 1,
	}
	if err := planner.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile after coverage: %v", err)
	}
	if len(converger.versionIDs) != 1 || converger.versionIDs[0] != 1001 {
		t.Fatalf("converged versions = %v, want [1001] once coverage is complete", converger.versionIDs)
	}
	if len(enqueuer.byType(TaskTypeDesignRequirement)) != 2 {
		t.Fatalf("covered requirement still requested repair: %v", sortedTaskDedupeKeys(enqueuer.tasks))
	}
}

func TestReconcileIdempotent(t *testing.T) {
	planner, repo, enqueuer, _, _, _, staler := newPlannerFixture()
	repo.candidateVersions = []int64{1001}
	repo.clarified = []design.RequirementRef{plannerRequirement(3001, 3002)}
	repo.outdated = []design.OutdatedRequirement{{
		VersionID: 1001, RequirementID: 3001, CurrentRequirementVersionID: 3002,
	}}

	if err := planner.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(staler.calls) != 1 {
		t.Fatalf("stale safety net calls = %v", staler.calls)
	}
	taskCount := len(enqueuer.tasks)
	if err := planner.Reconcile(context.Background()); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if len(enqueuer.tasks) != taskCount {
		t.Fatalf("reconcile duplicated tasks: %v", sortedTaskDedupeKeys(enqueuer.tasks))
	}
}

// TestDesigningToCaseReviewTransitionByDesignReconciler pins Task 69's
// production caller of the designing -> case_review edge: the design reconciler
// converges the version once every activated requirement has usable case
// coverage, and leaves it designing while any requirement is uncovered, stale or
// has no scenarios.
func TestDesigningToCaseReviewTransitionByDesignReconciler(t *testing.T) {
	planner, repo, _, _, _, _, _ := newPlannerFixture()
	repo.hasConversation = true
	repo.latestConversation = 9001
	roundID := int64(8001)
	repo.rounds = []design.Round{{
		ID: roundID, VersionID: 1001, ConversationID: 9001,
		Activation: design.ActivationAutoNew, Status: design.RoundStatusActive,
		RequirementIDs: []int64{3001, 3005}, RequirementVersionIDs: []int64{3002, 3006},
	}}
	repo.activeScopes = []design.Scope{
		{ID: 6001, VersionID: 1001, RequirementID: 3001, ActivatedByRoundID: &roundID, Status: design.ScopeStatusActive},
		{ID: 6002, VersionID: 1001, RequirementID: 3005, ActivatedByRoundID: &roundID, Status: design.ScopeStatusActive},
	}
	repo.coverage = []design.RequirementCoverage{
		{Requirement: plannerRequirement(3001, 3002), ScenarioCount: 1, DraftCount: 1},
		{Requirement: plannerRequirement(3005, 3006), ScenarioCount: 1, DraftCount: 1},
	}
	converger := &fakeDesignConverger{}
	planner.SetDesignConverger(converger)
	ctx := context.Background()

	if err := planner.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(converger.versionIDs) != 1 || converger.versionIDs[0] != 1001 {
		t.Fatalf("converged versions = %v, want [1001]", converger.versionIDs)
	}

	// A requirement without cases keeps the version designing.
	repo.coverage[1] = design.RequirementCoverage{Requirement: plannerRequirement(3005, 3006)}
	if err := planner.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile with an uncovered requirement: %v", err)
	}
	if len(converger.versionIDs) != 1 {
		t.Fatalf("uncovered requirement converged the version: %v", converger.versionIDs)
	}

	// A stale current case is not usable coverage either.
	repo.coverage[1] = design.RequirementCoverage{
		Requirement: plannerRequirement(3005, 3006), ScenarioCount: 1, StaleCount: 1,
	}
	if err := planner.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile with a stale case: %v", err)
	}
	if len(converger.versionIDs) != 1 {
		t.Fatalf("stale case converged the version: %v", converger.versionIDs)
	}

	// A convergence failure is reported by the reconciler (the next pass
	// retries; the stage is still the source of truth).
	repo.coverage[1] = design.RequirementCoverage{Requirement: plannerRequirement(3005, 3006), ScenarioCount: 1, DraftCount: 1}
	converger.err = errors.New("stage write failed")
	if err := planner.Reconcile(ctx); err == nil {
		t.Fatal("Reconcile swallowed the convergence failure")
	}
	converger.err = nil

	// A degraded assembly without a converger still reconciles.
	planner.SetDesignConverger(nil)
	if err := planner.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile without a converger: %v", err)
	}
}
