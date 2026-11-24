package execution

// Quota is the concurrency budget for case_run dispatch: ONE global limit on
// running case_runs (design P5, v0.2.4-F). The retired three-dimension budget
// (global / per-SUT / cross-system, frozen at 3/2/1) is gone: a case names its
// target in prose, so execution cannot know its SUT set before the run, and
// over-approximating it with the version's whole bound set made one saturated
// SUT hold back every run of the version (head-of-line blocking) while the
// cross-system dimension carried a latent collapse trap. A single global limit
// has neither failure mode. Zero (the default) means unlimited.
type Quota struct {
	Global int
}

// RunningCase is one currently running case_run as seen by the quota check.
// Under the single global limit a running run consumes exactly one slot; no
// per-SUT or cross-system accounting exists.
type RunningCase struct {
	CaseRunID int64
}

// Dispatch rejection reasons. They are stable strings so tests and logs can
// assert on them.
const (
	ReasonGlobalConcurrency = "global_concurrency"
	ReasonExecutorCapacity  = "executor_capacity"
	// ReasonContextRequirementExceedsCapacity is the one rejection that waiting
	// can never clear: the run's own context requirement is larger than the
	// executor's ENTIRE capacity, so the gate below fails for every possible
	// execActive, including a completely idle executor. It is deliberately a
	// distinct reason from ReasonExecutorCapacity so callers can tell "busy now,
	// retry next tick" apart from "structurally impossible, escalate to a human"
	// -- conflating them left a run pending forever with a wave that could never
	// complete (review I-new-1). The same string is reused as the run's
	// CaseRunSummary.TerminalCause; it is one constant on purpose so the dispatch
	// reason and the persisted cause cannot drift apart.
	ReasonContextRequirementExceedsCapacity = "context_requirement_exceeds_capacity"
)

// CanDispatch applies the one-shot reservation rules: the candidate must fit
// the single global limit and the executor context capacity. A zero/negative
// global limit is unlimited; a zero executor capacity means capacity is unknown
// and does not block. candidateContexts is the run's own context requirement
// (the scheduler derives it from the case snapshot's distinct account roles).
func CanDispatch(q Quota, running []RunningCase, execCapacity, execActive, candidateContexts int) (bool, string) {
	// The structural capacity check comes FIRST, before the concurrency
	// dimension (review round 4, Minor 2). It depends only on candidateContexts
	// and execCapacity, neither of which the concurrency block reads, so hoisting
	// it changes no other outcome -- but it does change which reason is reported.
	// A run that can never be dispatched by ANY tick must not be told to "wait for
	// a quota slot to free up", because no amount of waiting helps and the message
	// sends the operator to the wrong remedy.
	contexts := candidateContexts
	if contexts <= 0 {
		// Deliberately NOT a SUT-count fallback (review round 4, Minor 5). A
		// roles x bound-SUTs Cartesian reservation was the C1 livelock. A caller
		// that cannot say how many contexts a run needs reserves one, the
		// minimum, never a product.
		contexts = 1
	}
	if execCapacity > 0 && contexts > execCapacity {
		// Structurally unsatisfiable on THIS executor: even an idle one
		// (execActive == 0) cannot serve the run, and no future tick changes
		// either number. The scheduler must escalate rather than skip, or the run
		// stays pending forever -- Attempts is only incremented after this gate,
		// so the redispatch budget never applies, and LeaseUntil is likewise set
		// after it, so lease-expiry recovery has nothing to reclaim.
		//
		// NOTE for the caller: execCapacity is the SELECTED executor's, and
		// Registry.Select drops full and degraded executors before sorting, so a
		// smaller idle executor can be selected while a larger one is momentarily
		// full. Confirm against the fleet's maximum dispatchable capacity before
		// treating this as permanent -- see Scheduler.terminateUnsatisfiableRun.
		return false, ReasonContextRequirementExceedsCapacity
	}

	if q.Global > 0 && len(running)+1 > q.Global {
		return false, ReasonGlobalConcurrency
	}

	if execCapacity > 0 && execActive+contexts > execCapacity {
		return false, ReasonExecutorCapacity
	}
	return true, ""
}

// QuotaFromSettings builds the Quota from execution.max_concurrent_case_runs.
// Zero means unlimited (the catalog default, design P5); a negative value
// normalizes to unlimited as well. There is deliberately no finite fallback:
// the retired QuotaFromSettings coerced non-positive values back to 3/2/1,
// which made "unlimited" unreachable through every production entry point.
func QuotaFromSettings(maxConcurrentCaseRuns int) Quota {
	if maxConcurrentCaseRuns < 0 {
		maxConcurrentCaseRuns = 0
	}
	return Quota{Global: maxConcurrentCaseRuns}
}
