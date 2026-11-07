package agentapp

// Budget is the resource envelope of one work item (spec §5.7, task-types.md
// §9.4; v0.22 spec R-A2). Completion criteria are primary; wall-clock time is a
// secondary budget; the turn count is only a circuit breaker.
//
// Token accounting in Usage is telemetry and cost observability only: since
// v0.22 no cumulative token count can terminate a run (R-A2-1). MaxTokens and
// UnlimitedTokens are kept as the explicit historical shape and as a future
// quota hook (R-A2-4/R-A2-5); ExceededBy never consults them.
//
// Zero or negative values fall back to the contract defaults in normalized, so
// a caller can pass the zero Budget for a normal task.
type Budget struct {
	// MaxTokens is retained for compatibility and future token quotas. It is
	// never a termination basis; Usage.Tokens is telemetry only.
	MaxTokens int
	// UnlimitedTokens records the explicit unlimited-token shape
	// (MaxTokens=0 + UnlimitedTokens=true) used by the seeded profiles and the
	// case executor. It no longer changes any decision.
	UnlimitedTokens bool
	RunSeconds      int
	MaxTurnsFuse    int
}

// Default resource budgets from spec §15 / task-types.md §9.4. There is no
// token default: tokens are telemetry and never terminate a run (R-A2-1).
const (
	DefaultBudgetRunSeconds   = 120
	DefaultBudgetMaxTurnsFuse = 24
)

// Stable settlement causes written to agent_run.terminal_reason and handed to
// the execution context. They name the budget that actually ended the run, so a
// tool-less settlement turn is never mistaken for a time/turn cap (QA Q2).
const (
	// BudgetCauseTokens is never produced by ExceededBy since v0.22 (R-A2-3).
	// It stays only as the legacy export mapping for historical exports and
	// terminal_reason=token_budget rows.
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
	// Tokens is cumulative token telemetry (cost/observability). It never
	// influences Decide or ExceededBy (R-A2-1).
	Tokens  int
	Seconds int
	Turns   int
	// Completed reports that the task's completion criterion has been met.
	// Completion wins over resource budgets (spec §5.7).
	Completed bool
	// Settling reports that the settlement turn is already in progress.
	Settling bool
}

// Decide applies the v0.22 budget policy (spec R-A2):
//
//  1. A met completion criterion always settles, even when resources are
//     exhausted ("完成判据优先于资源预算").
//  2. Inside every enforced budget the loop continues; token usage never
//     decides.
//  3. A hit wall-clock or turn budget asks for settlement so partial artifacts
//     are written back before the run ends.
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

// ExceededBy reports the first normalized enforced budget the usage has
// reached, in a stable precedence (wall clock, turns). An empty string means
// every budget still has headroom. Tokens are deliberately absent: v0.22 uses
// them for telemetry/cost only, so a token-heavy run never reports a cause
// (R-A2-1). The value is a stable cause code, not display text.
func (b Budget) ExceededBy(usage Usage) string {
	budget := b.normalized()
	switch {
	case usage.Seconds >= budget.RunSeconds:
		return BudgetCauseTime
	case usage.Turns >= budget.MaxTurnsFuse:
		return BudgetCauseTurns
	default:
		return ""
	}
}

// normalized applies the contract defaults to unset enforced fields. Token
// fields are intentionally left untouched: they never decide.
func (b Budget) normalized() Budget {
	if b.RunSeconds <= 0 {
		b.RunSeconds = DefaultBudgetRunSeconds
	}
	if b.MaxTurnsFuse <= 0 {
		b.MaxTurnsFuse = DefaultBudgetMaxTurnsFuse
	}
	return b
}
