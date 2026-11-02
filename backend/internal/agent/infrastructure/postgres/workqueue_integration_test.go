//go:build integration

package agentpg_test

import (
	"context"
	"testing"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

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
