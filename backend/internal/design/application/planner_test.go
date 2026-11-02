package designapp

import (
	"context"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

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
	if tasks := enqueuer.byType("design.round"); len(tasks) != 1 {
		t.Fatalf("round tasks = %d", len(tasks))
	}
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
	if requirementTasks[0].Payload["reason"] != "requirement_changed" {
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
