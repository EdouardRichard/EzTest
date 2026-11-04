//go:build integration

package agentpg_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// TestContractClaimForTeamsSelectsStageEntryItems pins Task 65's SQL claim
// path: a dispatcher-created stage-entry row (non-empty kind) carries the
// product team as its frozen claim default and is leased by
// ClaimWorkItemForTeams for that team only. The legacy ClaimForTeams path still
// ignores kind rows, and a foreign team cannot take the item:
//
//	kind <> ''
func TestContractClaimForTeamsSelectsStageEntryItems(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewWorkItemRepository(pool, newIDs(t))
	ctx := context.Background()
	now := time.Now().UTC().Add(time.Second)

	design := createContractWorkItem(t, repo, agent.CreateWorkItemParams{
		VersionID:      30,
		Kind:           agent.WorkItemKindDesignCases,
		Inputs:         json.RawMessage(`{"requirement_ids":["501"]}`),
		CreatedBy:      agent.WorkItemCreatedByStateMachine,
		IdempotencyKey: "30:design.cases:entry",
	})
	if design.Team != agent.TeamDesign || design.Type != agent.WorkItemKindDesignCases {
		t.Fatalf("created stage-entry team/type = %q/%q, want design/design.cases", design.Team, design.Type)
	}

	foreign := createContractWorkItem(t, repo, agent.CreateWorkItemParams{
		VersionID:      30,
		Kind:           agent.WorkItemKindDesignCases,
		CreatedBy:      agent.WorkItemCreatedByStateMachine,
		IdempotencyKey: "30:design.cases:foreign-team",
		// Explicit override: the claim filter, not the default mapping, must
		// keep this row out of the agent worker's team set.
		Team: agent.TeamCode,
	})
	if foreign.Team != agent.TeamCode {
		t.Fatalf("foreign team = %q, want code", foreign.Team)
	}

	productTeams := []agent.Team{
		agent.TeamClarification, agent.TeamDesign, agent.TeamExecution,
		agent.TeamDelivery, agent.TeamSteward,
	}
	claimed, err := repo.ClaimWorkItemForTeams(ctx, "agent-worker", productTeams, now)
	if err != nil || claimed == nil {
		t.Fatalf("ClaimWorkItemForTeams = %#v, %v", claimed, err)
	}
	if claimed.ID != design.ID || claimed.Team != agent.TeamDesign || claimed.Kind != agent.WorkItemKindDesignCases {
		t.Fatalf("claimed = %#v, want the design.cases item %d", claimed, design.ID)
	}
	if claimed.Attempts != 1 || claimed.LeaseEpoch != 1 || claimed.Status != agent.WorkItemClaimed {
		t.Fatalf("claim state = %#v", claimed)
	}

	// The code-team contract row is not in the product team set: the next
	// product claim sees nothing, while the code team can still claim it.
	if extra, err := repo.ClaimWorkItemForTeams(ctx, "agent-worker", productTeams, now); err != nil || extra != nil {
		t.Fatalf("product claim took a foreign contract item: %#v, %v", extra, err)
	}
	codeClaimed, err := repo.ClaimWorkItemForTeams(ctx, "code-worker", []agent.Team{agent.TeamCode}, now)
	if err != nil || codeClaimed == nil || codeClaimed.ID != foreign.ID {
		t.Fatalf("code claim = %#v, %v", codeClaimed, err)
	}

	// The legacy team claim must keep ignoring 0.2.1 rows (kind <> '').
	if legacy, ok, err := repo.ClaimForTeams(ctx, "legacy-worker", productTeams, time.Second); err != nil || ok || legacy.ID != 0 {
		t.Fatalf("legacy claim saw a contract row: %#v, %v, %v", legacy, ok, err)
	}
	if empty, err := repo.ClaimWorkItemForTeams(ctx, "agent-worker", nil, now); err != nil || empty != nil {
		t.Fatalf("empty team set claim = %#v, %v", empty, err)
	}
}

func ensureTeamItem(t *testing.T, repo *agentpg.WorkItemRepository, versionID int64, team agent.Team, workType, dedupe string, priority int) agent.WorkItem {
	t.Helper()
	item, created, err := repo.Ensure(context.Background(), agent.NewWorkItem{
		VersionID: versionID,
		Team:      string(team),
		Type:      workType,
		Priority:  priority,
		DedupeKey: dedupe,
		Payload:   map[string]any{"dedupe": dedupe},
	})
	if err != nil {
		t.Fatalf("Ensure(%s/%s): %v", team, dedupe, err)
	}
	if !created {
		t.Fatalf("Ensure(%s/%s) did not create", team, dedupe)
	}
	return item
}

// TestDesignAuthorClaimHonorsRequirementPriority pins Task 79's claim ordering:
// the design planner stamps a design.requirement item with the requirement
// priority, and the queue must lease the P0 item before a lower-priority item
// that was enqueued first. Without this ordering a 67-requirement round would
// drain in import order and starve P0 behind P1/P2.
func TestDesignAuthorClaimHonorsRequirementPriority(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewWorkItemRepository(pool, newIDs(t))
	ctx := context.Background()

	low := ensureTeamItem(t, repo, 31, agent.TeamDesign, "design.requirement", "design:2:2", 1)
	high := ensureTeamItem(t, repo, 31, agent.TeamDesign, "design.requirement", "design:1:1", 3)
	designTeam := []agent.Team{agent.TeamDesign}

	first, ok, err := repo.ClaimForTeams(ctx, "worker", designTeam, 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("first claim = %#v, %v, %v", first, ok, err)
	}
	if first.ID != high.ID || first.Priority != 3 {
		t.Fatalf("first claim = %#v, want the higher-priority P0 item %d", first, high.ID)
	}
	second, ok, err := repo.ClaimForTeams(ctx, "worker", designTeam, 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("second claim = %#v, %v, %v", second, ok, err)
	}
	if second.ID != low.ID {
		t.Fatalf("second claim = %#v, want the lower-priority item %d", second, low.ID)
	}
}

func TestWorkItemClaimForTeamsOnlyRequestedTeam(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewWorkItemRepository(pool, newIDs(t))
	ctx := context.Background()

	clarification := ensureTeamItem(t, repo, 10, agent.TeamClarification, "chat.turn", "clarification-task", 0)
	// The code task has the higher priority so an unfiltered claim would pick it.
	codeItem := ensureTeamItem(t, repo, 10, agent.TeamCode, "index.repo", "code:index:1", 100)
	// Deterministic ops tasks (report.render_pdf) outrank every product task:
	// the generic agent worker must still never claim them.
	opsItem := ensureTeamItem(t, repo, 10, agent.TeamOps, "report.render_pdf", "report:1:rev:1", 200)

	productTeams := []agent.Team{
		agent.TeamClarification,
		agent.TeamDesign,
		agent.TeamExecution,
		agent.TeamDelivery,
		agent.TeamSteward,
	}
	claimed, ok, err := repo.ClaimForTeams(ctx, "agent-worker", productTeams, 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("ClaimForTeams(product) = %#v, %v, %v", claimed, ok, err)
	}
	if claimed.ID != clarification.ID || claimed.Team != agent.TeamClarification {
		t.Fatalf("product worker claimed %#v, want clarification item %d", claimed, clarification.ID)
	}

	claimedOps, ok, err := repo.ClaimForTypes(ctx, "render-worker", []string{"report.render_pdf"}, 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("ClaimForTypes(report.render_pdf) = %#v, %v, %v", claimedOps, ok, err)
	}
	if claimedOps.ID != opsItem.ID || claimedOps.Team != agent.TeamOps || claimedOps.Type != "report.render_pdf" {
		t.Fatalf("render worker claimed %#v, want ops item %d", claimedOps, opsItem.ID)
	}

	claimedCode, ok, err := repo.ClaimForTeams(ctx, "code-worker", []agent.Team{agent.TeamCode}, 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("ClaimForTeams(code) = %#v, %v, %v", claimedCode, ok, err)
	}
	if claimedCode.ID != codeItem.ID || claimedCode.Team != agent.TeamCode {
		t.Fatalf("code worker claimed %#v, want code item %d", claimedCode, codeItem.ID)
	}

	if extra, ok, err := repo.ClaimForTeams(ctx, "agent-worker", productTeams, time.Second); err != nil || ok || extra.ID != 0 {
		t.Fatalf("product worker claimed code task = %#v, %v, %v", extra, ok, err)
	}
	if empty, ok, err := repo.ClaimForTeams(ctx, "worker", nil, time.Second); err != nil || ok || empty.ID != 0 {
		t.Fatalf("empty teams claim = %#v, %v, %v", empty, ok, err)
	}
	if _, _, err := repo.ClaimForTeams(ctx, "worker", []agent.Team{agent.Team("unknown")}, time.Second); err == nil {
		t.Fatal("unknown team was accepted")
	} else if apiErr := platform.AsError(err); apiErr.Status != 400 || apiErr.Code != "invalid_argument" {
		t.Fatalf("invalid team error = %#v", apiErr)
	} else if field := apiErr.Details["field"]; field != "teams" {
		t.Fatalf("invalid team field = %#v", field)
	}
}
