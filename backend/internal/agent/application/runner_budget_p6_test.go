package agentapp

import (
	"encoding/json"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// TestMaxLoopIterationsDefensiveFloor pins the P6 defensive constant: an
// unlimited turn budget is still bounded by a hard loop floor so a runaway loop
// can never spin forever (design §4.5 兜底).
func TestMaxLoopIterationsDefensiveFloor(t *testing.T) {
	if maxLoopIterations != 10000 {
		t.Fatalf("maxLoopIterations = %d, want the 10000 defensive floor", maxLoopIterations)
	}
}

// TestCaseExecutorTurnsUnlimitedByDefault pins R-C5/P6: the case executor seeds
// MaxTurns 0 and reads execution.max_case_turns. With the setting absent or 0 the
// turn budget is unlimited (the 10000 defensive floor still applies), NOT the
// chat-scale agent.max_turns=12 fallback (the T13 disclosed window this task
// closes).
func TestCaseExecutorTurnsUnlimitedByDefault(t *testing.T) {
	seed := agent.AgentProfile{
		Key:     ProfileKeyCaseExecutor,
		Budgets: agent.ProfileBudgets{MaxTurns: 0, SoftSeconds: 1200, HardSeconds: 2400},
	}
	for name, settings := range map[string]executionSettings{
		"absent":        {"agent.max_turns": 12},
		"explicit zero": {"agent.max_turns": 12, "execution.max_case_turns": 0},
	} {
		runner := &Runner{settings: settings}
		if got := runner.maxTurns(seed); got != maxLoopIterations {
			t.Fatalf("%s: case_executor maxTurns = %d, want the unlimited floor %d (not agent.max_turns=12)",
				name, got, maxLoopIterations)
		}
		item := agent.WorkItem{VersionID: 1, Type: WorkTypeExecuteCase, Budget: json.RawMessage(`{}`)}
		budget := runner.resourceBudget(item, seed)
		if budget.MaxTurnsFuse != maxLoopIterations-1 {
			t.Fatalf("%s: turn fuse = %d, want %d (one below the unlimited floor)",
				name, budget.MaxTurnsFuse, maxLoopIterations-1)
		}
		// A productive 60-turn / 50M-token run is well inside the unlimited budget.
		if got := budget.Decide(Usage{Turns: 60, Tokens: 50_000_000}); got != BudgetContinue {
			t.Fatalf("%s: decision at 60 turns = %q, want continue (unlimited)", name, got)
		}
		// The defensive floor still settles at the fuse.
		if got := budget.Decide(Usage{Turns: maxLoopIterations - 1}); got != BudgetSettle {
			t.Fatalf("%s: decision at the floor fuse = %q, want settle", name, got)
		}
	}
}

// TestCaseExecutorTurnsSettingConfigurable pins that execution.max_case_turns
// is the configurable knob: =3 gives a 3-turn cap with a 2-turn fuse, so the 3rd
// model turn is the settlement turn and a still-over-budget settlement exhausts.
func TestCaseExecutorTurnsSettingConfigurable(t *testing.T) {
	runner := &Runner{settings: executionSettings{"agent.max_turns": 12, "execution.max_case_turns": 3}}
	seed := agent.AgentProfile{
		Key:     ProfileKeyCaseExecutor,
		Budgets: agent.ProfileBudgets{MaxTurns: 0, SoftSeconds: 1200, HardSeconds: 2400},
	}
	if got := runner.maxTurns(seed); got != 3 {
		t.Fatalf("case_executor maxTurns = %d, want the configured 3", got)
	}
	item := agent.WorkItem{VersionID: 1, Type: WorkTypeExecuteCase, Budget: json.RawMessage(`{}`)}
	budget := runner.resourceBudget(item, seed)
	if budget.MaxTurnsFuse != 2 {
		t.Fatalf("turn fuse = %d, want 2 (one below the 3-turn cap)", budget.MaxTurnsFuse)
	}
	if got := budget.Decide(Usage{Turns: 1}); got != BudgetContinue {
		t.Fatalf("decision at 1 turn = %q, want continue", got)
	}
	if got := budget.Decide(Usage{Turns: 2}); got != BudgetSettle {
		t.Fatalf("decision at the fuse = %q, want settle (settlement winding)", got)
	}
	if got := budget.Decide(Usage{Turns: 3, Settling: true}); got != BudgetExhaust {
		t.Fatalf("decision in an over-budget settlement = %q, want exhaust", got)
	}
	if cause := budget.ExceededBy(Usage{Turns: 2}); cause != BudgetCauseTurns {
		t.Fatalf("cause at the fuse = %q, want %q", cause, BudgetCauseTurns)
	}
}

// TestCaseExecutorTurnsClampedToFloor pins that an over-large configured value
// can never exceed the defensive loop floor.
func TestCaseExecutorTurnsClampedToFloor(t *testing.T) {
	runner := &Runner{settings: executionSettings{"execution.max_case_turns": 50000}}
	seed := agent.AgentProfile{Key: ProfileKeyCaseExecutor, Budgets: agent.ProfileBudgets{MaxTurns: 0}}
	if got := runner.maxTurns(seed); got != maxLoopIterations {
		t.Fatalf("case_executor maxTurns = %d, want clamped to %d", got, maxLoopIterations)
	}
}

// TestCaseExecutorSoftWindingNotHardKill pins that the case executor keeps its
// 1200s soft winding budget (settlement, not a hard kill) and that the hard case
// timeout is opt-in: execution.case_timeout_seconds=0 means no hard deadline.
func TestCaseExecutorSoftWindingNotHardKill(t *testing.T) {
	runner := &Runner{settings: executionSettings{"execution.case_timeout_seconds": 0}}
	seed := agent.AgentProfile{
		Key:     ProfileKeyCaseExecutor,
		Budgets: agent.ProfileBudgets{MaxTurns: 0, SoftSeconds: 1200, HardSeconds: 2400},
	}
	if got := runner.softSeconds(seed); got != 1200 {
		t.Fatalf("case_executor softSeconds = %d, want the 1200s soft winding budget", got)
	}
	if got := runner.caseHardTimeoutSeconds(seed); got != 0 {
		t.Fatalf("case hard timeout = %d, want 0 (opt-in off, no hard kill)", got)
	}
	enabled := &Runner{settings: executionSettings{"execution.case_timeout_seconds": 600}}
	if got := enabled.caseHardTimeoutSeconds(seed); got != 600 {
		t.Fatalf("case hard timeout = %d, want the opt-in 600", got)
	}
}
