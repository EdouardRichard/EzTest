package agentapp

// Budget is the resource envelope of one work item (spec §5.7, task-types.md
// §9.4). Completion criteria are primary; tokens and wall-clock time are
// secondary budgets; the turn count is only a circuit breaker.
//
// Zero or negative values fall back to the contract defaults in Decide, so a
// caller can pass the zero Budget for a normal task.
type Budget struct {
	MaxTokens    int
	// UnlimitedTokens removes the token ceiling (CaseExecutorRunMaxTokens=0).
	// The turn fuse and wall-clock limit remain loop guards; an explicit
	// per-item max_tokens still wins.
	UnlimitedTokens bool
	RunSeconds   int
	MaxTurnsFuse int
}

// Default resource budgets from spec §15 / task-types.md §9.4.
const (
	DefaultBudgetMaxTokens    = 20000
	DefaultBudgetRunSeconds   = 120
	DefaultBudgetMaxTurnsFuse = 24
)

// Stable settlement causes written to agent_run.terminal_reason and handed to
// the execution context. They name the budget that actually ended the run, so a
// tool-less settlement turn is never mistaken for a time/turn cap (QA Q2: the
// settlement prompt accused the clock while the token budget was the trigger).
const (
	BudgetCauseTokens = "token_budget"
	BudgetCauseTime   = "time_budget"
	BudgetCauseTurns  = "turn_budget"
)

// BudgetDecision is what the agent loop should do after one turn.
type BudgetDecision string

const (
	// BudgetContinue means the run is inside its budget and may keep going.
	BudgetContinue BudgetDecision = "continue"
	// BudgetSettle means a budget was hit (or the completion criterion was
	// met): write the partial artifacts/summary (settlement) and end.
	BudgetSettle BudgetDecision = "settle"
	// BudgetExhaust means the run is already in settlement and is still over
	// budget: stop immediately, without another settlement turn.
	BudgetExhaust BudgetDecision = "exhaust"
)

// Usage is the resource consumption observed up to (and including) the last
// completed turn.
type Usage struct {
	Tokens  int
	Seconds int
	Turns   int
	// Completed reports that the task's completion criterion has been met.
	// Completion wins over resource budgets (spec §5.7).
	Completed bool
	// Settling reports that the settlement turn is already in progress.
	Settling bool
}

// Decide applies the P2-T4 budget policy:
//
//  1. A met completion criterion always settles, even when resources are
//     exhausted ("完成判据优先于资源预算").
//  2. Inside every budget the loop continues.
//  3. A hit budget (tokens, run_seconds or the turn fuse) asks for settlement
//     so partial artifacts are written back before the run ends.
//  4. If the settlement turn itself is still over budget, the run is exhausted
//     and must stop immediately.
func (b Budget) Decide(usage Usage) BudgetDecision {
	if usage.Completed {
		return BudgetSettle
	}
	if !b.exceeded(usage) {
		return BudgetContinue
	}
	if usage.Settling {
		return BudgetExhaust
	}
	return BudgetSettle
}

// exceeded reports whether any normalized budget is reached.
func (b Budget) exceeded(usage Usage) bool {
	return b.ExceededBy(usage) != ""
}

// ExceededBy reports the first normalized budget the usage has reached, in a
// stable precedence (tokens, wall clock, turns). An empty string means every
// budget still has headroom. The value is a stable cause code, not display text.
func (b Budget) ExceededBy(usage Usage) string {
	budget := b.normalized()
	switch {
	case !budget.UnlimitedTokens && usage.Tokens >= budget.MaxTokens:
		return BudgetCauseTokens
	case usage.Seconds >= budget.RunSeconds:
		return BudgetCauseTime
	case usage.Turns >= budget.MaxTurnsFuse:
		return BudgetCauseTurns
	default:
		return ""
	}
}

// normalized applies the contract defaults to unset fields.
func (b Budget) normalized() Budget {
	if !b.UnlimitedTokens && b.MaxTokens <= 0 {
		b.MaxTokens = DefaultBudgetMaxTokens
	}
	if b.RunSeconds <= 0 {
		b.RunSeconds = DefaultBudgetRunSeconds
	}
	if b.MaxTurnsFuse <= 0 {
		b.MaxTurnsFuse = DefaultBudgetMaxTurnsFuse
	}
	return b
}
