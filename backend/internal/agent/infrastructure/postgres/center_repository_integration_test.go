//go:build integration

package postgres_test

import (
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// TestCenterRepositoryAggregatesRunStatsAndSessions pins the P8 Agent-centre
// read model against the real schema: per-profile run statistics (including the
// zero row), the derived session status and the role/status filters.
func TestCenterRepositoryAggregatesRunStatsAndSessions(t *testing.T) {
	pool := testutil.OpenTestDB(t)

	profiles := postgres.NewProfileRepository(pool)
	design := agent.BuiltinProfile()
	design.Key = "case-design"
	design.Name = "测试设计 Agent"
	design.Builtin = false
	if _, err := profiles.EnsureBuiltin(ctx, design); err != nil {
		t.Fatalf("seed case-design profile: %v", err)
	}
	// One profile with runs and one profile without runs proves the LEFT JOIN
	// zero row instead of a missing statistic.
	execution := agent.BuiltinProfile()
	execution.Key = "execution-agent"
	execution.Name = "执行 Agent"
	execution.Builtin = false
	if _, err := profiles.EnsureBuiltin(ctx, execution); err != nil {
		t.Fatalf("seed execution-agent profile: %v", err)
	}
	all, err := profiles.List(ctx)
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	var designID, executionID int64
	for _, profile := range all {
		switch profile.Key {
		case "case-design":
			designID = profile.ID
		case "execution-agent":
			executionID = profile.ID
		}
	}

	sessions := postgres.NewSessionRepository(pool)
	withRuns, err := sessions.Create(ctx, designID, "设计会话", nil)
	if err != nil {
		t.Fatalf("create session with runs: %v", err)
	}
	if _, err := sessions.Create(ctx, executionID, "执行会话", nil); err != nil {
		t.Fatalf("create session without runs: %v", err)
	}

	runs := postgres.NewRunRepository(pool)
	finish := func(status agent.RunStatus, reason agent.TerminalReason) {
		t.Helper()
		ids, err := runs.CreateTurn(ctx, withRuns.ID, []agent.Part{{Type: "text", Text: "go"}}, "go")
		if err != nil {
			t.Fatalf("create turn: %v", err)
		}
		if err := runs.Finish(ctx, ids.RunID, agent.RunResult{
			Status: status, TerminalReason: reason, Turns: 1,
		}); err != nil {
			t.Fatalf("finish run: %v", err)
		}
	}
	finish(agent.RunDone, agent.ReasonCompleted)
	finish(agent.RunFailed, agent.ReasonModelError)

	center := postgres.NewCenterRepository(pool)
	stats, err := center.ProfileRunStats(ctx)
	if err != nil {
		t.Fatalf("ProfileRunStats: %v", err)
	}
	byKey := map[string]agent.ProfileRunStats{}
	for _, row := range stats {
		byKey[row.ProfileKey] = row
	}
	designStats := byKey["case-design"]
	if designStats.RunCount != 2 || designStats.SucceededCount != 1 || designStats.FailedCount != 1 {
		t.Fatalf("case-design stats = %+v, want 2 runs / 1 succeeded / 1 failed", designStats)
	}
	executionStats := byKey["execution-agent"]
	if executionStats.RunCount != 0 || executionStats.SucceededCount != 0 || executionStats.FailedCount != 0 {
		t.Fatalf("execution-agent stats = %+v, want the zero row not a fake count", executionStats)
	}

	// The newest terminal run determines the derived status.
	allRows, err := center.CenterSessions(ctx, agent.CenterSessionFilter{Limit: 10})
	if err != nil {
		t.Fatalf("CenterSessions(all): %v", err)
	}
	if len(allRows) != 2 {
		t.Fatalf("all sessions = %d rows, want 2: %+v", len(allRows), allRows)
	}
	statuses := map[int64]string{}
	for _, row := range allRows {
		statuses[row.ID] = row.Status
	}
	if got := statuses[withRuns.ID]; got != "failed" {
		t.Errorf("session %d status = %q, want failed", withRuns.ID, got)
	}

	// The role filter only returns the matching profile and the idle session
	// reports status idle with running=false.
	idle, err := center.CenterSessions(ctx, agent.CenterSessionFilter{RoleKey: "execution-agent", Status: "idle", Limit: 10})
	if err != nil {
		t.Fatalf("CenterSessions(idle): %v", err)
	}
	if len(idle) != 1 || idle[0].ProfileKey != "execution-agent" || idle[0].Running {
		t.Fatalf("idle rows = %+v, want the execution-agent idle session", idle)
	}

	done, err := center.CenterSessions(ctx, agent.CenterSessionFilter{Status: "done", Limit: 10})
	if err != nil {
		t.Fatalf("CenterSessions(done): %v", err)
	}
	if len(done) != 0 {
		t.Fatalf("done rows = %+v, want none (the newest run is failed)", done)
	}

	limited, err := center.CenterSessions(ctx, agent.CenterSessionFilter{Limit: 1})
	if err != nil {
		t.Fatalf("CenterSessions(limit): %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("limit=1 rows = %d, want 1", len(limited))
	}
}
