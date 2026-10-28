package application

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// Case runner defaults (spec §5.4, §5.5).
const (
	// DefaultCaseTimeout is the per-case wall-clock budget
	// (EZTEST_EXEC_CASE_TIMEOUT_MS, spec §5.5 default 15 minutes).
	DefaultCaseTimeout = 15 * time.Minute
	// DefaultAgentPollInterval is how often the runner polls AgentRuntimePort.
	DefaultAgentPollInterval = 500 * time.Millisecond
	// envCaseTimeoutMS overrides DefaultCaseTimeout.
	envCaseTimeoutMS = "EZTEST_EXEC_CASE_TIMEOUT_MS"
	// cleanupWait bounds the best-effort executor/lease cleanup after a run.
	cleanupWait = 10 * time.Second

	// agentRunStatusRunning is the only non-terminal agent run status.
	agentRunStatusRunning = "running"
	// runPauseReason is stored when the engine parks or interrupts a run
	// because of a pause request (spec §5.4).
	runPauseReason = "paused"
)

// Terminal agent run statuses (P1 runtime, spec §7.3).
var terminalAgentRunStatuses = map[string]struct{}{
	"done":        {},
	"failed":      {},
	"aborted":     {},
	"interrupted": {},
	"canceled":    {},
	"cancelled":   {},
}

// CaseRunnerDeps are the ports of one case run. Browser is the controller of
// the tools' per-case executor context; the runner never creates a context
// itself. It may be nil in tests that only exercise pause/completion; Runtime,
// CaseRuns, Steps and Sessions are required.
type CaseRunnerDeps struct {
	CaseRuns CaseRunRepository
	Steps    StepRepository
	Batches  BatchRepository
	Sessions SessionPort
	Runtime  AgentRuntimePort
	Mindmap  MindmapPort
	Events   EventPublisher
	Clock    Clock
	// Browser cancels and closes the per-case context the execution agent
	// tools create lazily. The tools own that context; the runner only acts on
	// it through this controller.
	Browser CaseBrowserController
	// Timeout overrides DefaultCaseTimeout. A positive value wins; otherwise
	// EZTEST_EXEC_CASE_TIMEOUT_MS is read.
	Timeout time.Duration
	// PollInterval overrides DefaultAgentPollInterval.
	PollInterval time.Duration
}

// CaseRunner executes one dispatched case run: it starts the execution agent
// run, polls it until a terminal state or the case timeout, and releases the
// agent's per-case browser context and the session lease on every exit path. The
// browser context belongs to the execution agent tools (they create one lazily
// per case run); the runner cancels it through CaseBrowserController on an
// immediate pause and closes it on teardown. A pause is observed at the poll
// (step) boundary: graceful parks the run as pending, immediate interrupts it
// and records the interrupted step (spec §5.4).
type CaseRunner struct {
	deps         CaseRunnerDeps
	now          func() time.Time
	timeout      time.Duration
	pollInterval time.Duration
}

// NewCaseRunner wires one case runner.
func NewCaseRunner(deps CaseRunnerDeps) *CaseRunner {
	if deps.Clock == nil {
		deps.Clock = wallClock{}
	}
	timeout := deps.Timeout
	if timeout <= 0 {
		timeout = configuredCaseTimeout()
	}
	poll := deps.PollInterval
	if poll <= 0 {
		poll = DefaultAgentPollInterval
	}
	return &CaseRunner{deps: deps, now: deps.Clock.Now, timeout: timeout, pollInterval: poll}
}

// Run executes one case run. It is invoked by the scheduler in its own
// goroutine and never returns an error: every outcome is persisted on the case
// run and announced as an event. The session lease and the browser context are
// always released.
func (r *CaseRunner) Run(ctx context.Context, run domain.CaseRun, session SessionRef, environment EnvironmentRef) {
	if r == nil || r.deps.Runtime == nil || r.deps.CaseRuns == nil {
		slog.WarnContext(ctx, "execution: case runner is not configured",
			"case_run_id", run.ID)
		if r != nil && r.deps.Sessions != nil && session.SessionID > 0 {
			_ = r.deps.Sessions.Release(context.WithoutCancel(ctx), session.SessionID,
				caseRunHolder(run.ID), releaseReason(run.Status))
		}
		return
	}
	if ctx.Err() != nil {
		// Shutdown raced the launch: do not start an agent, just free the lease
		// and leave the run for crash recovery (spec §5.12 invariant 7).
		r.releaseResources(ctx, run, session.SessionID)
		return
	}

	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	completedSeqs, interruptedStepSeq := r.checkpoint(runCtx, run)
	agent, err := r.deps.Runtime.StartCaseRun(runCtx, CaseRunRequest{
		VersionID:          run.VersionID,
		BatchID:            run.BatchID,
		CaseRunID:          run.ID,
		CaseNodeID:         run.CaseNodeID,
		CaseNodeTitle:      run.Snapshot.Node.Title,
		Snapshot:           run.Snapshot,
		Session:            session,
		Environment:        environment,
		CompletedSeqs:      completedSeqs,
		InterruptedStepSeq: interruptedStepSeq,
		CleanupRequired:    run.CleanupRequired,
	})
	if err != nil {
		if r.honorPauseBeforeTerminal(ctx, run, session) {
			return
		}
		r.finishOutcome(ctx, run, toolCodeForError(err), err.Error(), 0, session)
		return
	}

	run = r.pinAgentRun(ctx, run, agent)

	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()
	for {
		state, err := r.deps.Runtime.GetRun(runCtx, agent.SessionID)
		if err != nil {
			if runCtx.Err() != nil {
				if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
					r.finishTimeout(ctx, run, agent.SessionID, session)
					return
				}
				r.handleShutdown(ctx, run, agent.SessionID, session)
				return
			}
			if r.honorPauseBeforeTerminal(ctx, run, session) {
				return
			}
			r.finishOutcome(ctx, run, toolCodeForError(err), err.Error(), 0, session)
			return
		}

		if isTerminalAgentStatus(state.Status) {
			r.afterAgentTerminal(ctx, run, state, session)
			return
		}

		if batch, ok := r.pauseRequested(ctx, run); ok && batch.Paused {
			r.handlePause(ctx, run, pauseModeOf(batch), agent.SessionID, session)
			return
		}

		select {
		case <-runCtx.Done():
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				r.finishTimeout(ctx, run, agent.SessionID, session)
				return
			}
			r.handleShutdown(ctx, run, agent.SessionID, session)
			return
		case <-ticker.C:
		}
	}
}

// afterAgentTerminal resolves the case run after the agent run ended. A finish
// submitted by the agent is authoritative: the run is already terminal and only
// cleanup remains. A pause that ended (canceled/aborted) the agent run must be
// honored first: the terminal state came from the pause, so it must not be
// classified. Otherwise the terminal agent state is classified server side (an
// agent that stops without case_finish must not become failed).
func (r *CaseRunner) afterAgentTerminal(ctx context.Context, run domain.CaseRun, state AgentRunState, session SessionRef) {
	if r.honorPauseBeforeTerminal(ctx, run, session) {
		return
	}
	current, err := r.deps.CaseRuns.GetCaseRun(ctx, run.BatchID, run.ID)
	if err == nil && isTerminalCaseRun(current.Status) {
		r.teardown(ctx, current, session.SessionID)
		return
	}
	if err != nil && !errors.Is(err, ErrCaseRunNotFound) {
		slog.WarnContext(ctx, "execution: reload finished case run failed",
			"case_run_id", run.ID, "error", err)
	}
	if current.ID != 0 {
		run = current
	}

	// A shutdown aborts the agent run without a verdict. Classifying it would
	// record an inconclusive case for an interrupted process, so the run is left
	// running: boot recovery owns the interrupted transition. Only the tools'
	// execution context and the session lease are released (spec §5.4, §5.12
	// invariant 7).
	if isShutdownTerminalReason(state.TerminalReason) {
		r.teardown(ctx, run, session.SessionID)
		return
	}

	code, reason := agentTerminalCode(state)
	r.finishOutcome(ctx, run, code, reason, r.stepEvidenceCount(ctx, run.ID), session)
}

// isShutdownTerminalReason reports whether a terminal agent run ended because
// the process shut down, rather than because of a model or case outcome.
func isShutdownTerminalReason(reason string) bool {
	return strings.EqualFold(strings.TrimSpace(reason), "shutdown")
}

// handlePause applies a pause request observed at a step boundary. A terminal
// case run that completed before the pause was observed is left untouched
// (Review Focus #1).
func (r *CaseRunner) handlePause(ctx context.Context, run domain.CaseRun, mode domain.PauseMode, agentSessionID int64, session SessionRef) {
	current, err := r.deps.CaseRuns.GetCaseRun(ctx, run.BatchID, run.ID)
	if err == nil && isTerminalCaseRun(current.Status) {
		r.teardown(ctx, current, session.SessionID)
		return
	}
	if current.ID != 0 {
		run = current
	}

	_ = r.deps.Runtime.CancelRun(context.WithoutCancel(ctx), agentSessionID)
	r.applyPause(ctx, run, mode, session)
}

// honorPauseBeforeTerminal re-checks the authoritative batch pause flag before a
// terminal/error path classifies the case run. A pause may have canceled or
// aborted the agent run before the runner observed the flag; classifying that
// terminal state would turn the requested park/interrupt into inconclusive. It
// reports whether the pause consumed the path.
func (r *CaseRunner) honorPauseBeforeTerminal(ctx context.Context, run domain.CaseRun, session SessionRef) bool {
	batch, ok := r.pauseRequested(ctx, run)
	if !ok || !batch.Paused {
		return false
	}
	current, err := r.deps.CaseRuns.GetCaseRun(ctx, run.BatchID, run.ID)
	if err == nil && isTerminalCaseRun(current.Status) {
		// The case finished before the pause landed; keep its verdict.
		r.teardown(ctx, current, session.SessionID)
		return true
	}
	if current.ID != 0 {
		run = current
	}
	r.applyPause(ctx, run, pauseModeOf(batch), session)
	return true
}

// applyPause parks (graceful) or interrupts (immediate) a running case run. The
// agent run may already be terminal because the pause canceled it; the caller
// owns canceling it when needed.
func (r *CaseRunner) applyPause(ctx context.Context, run domain.CaseRun, mode domain.PauseMode, session SessionRef) {
	if mode == domain.PauseModeImmediate {
		r.interruptForPause(ctx, run, session)
		return
	}
	r.parkForPause(ctx, run, session)
}

// interruptForPause records an immediate pause: the case run becomes interrupted
// with the in-flight step and the cleanup marker, and run.paused is published
// (spec §5.4).
func (r *CaseRunner) interruptForPause(ctx context.Context, run domain.CaseRun, session SessionRef) {
	if r.deps.Browser != nil {
		_ = r.deps.Browser.CancelCase(context.WithoutCancel(ctx), run.ID)
	}
	now := r.now()
	interruptedStep := r.interruptedStepSeq(ctx, run)
	interrupted := run
	interrupted.Status = domain.CaseRunStatusInterrupted
	interrupted.FailureClass = nil
	interrupted.FailureReason = runPauseReason
	interrupted.CleanupRequired = true
	interrupted.InterruptedStepSeq = interruptedStep
	interrupted.FinishedAt = timePointer(now)
	interrupted.UpdatedAt = now
	if run.StartedAt != nil {
		interrupted.DurationMS = nonNegativeMillis(now.Sub(*run.StartedAt))
	}
	stored, err := r.deps.CaseRuns.FinishCaseRun(ctx, interrupted)
	if err != nil {
		slog.WarnContext(ctx, "execution: interrupt case run failed",
			"case_run_id", run.ID, "error", err)
		r.teardown(ctx, run, session.SessionID)
		return
	}
	detail := map[string]any{
		"case_run_id": eventID(run.ID),
		"mode":        string(domain.PauseModeImmediate),
		"status":      string(stored.Status),
	}
	if interruptedStep != nil {
		detail["interrupted_step_seq"] = *interruptedStep
	}
	r.publish(ctx, Event{
		Type:      "run.paused",
		VersionID: stored.VersionID,
		BatchID:   stored.BatchID,
		CaseID:    stored.CaseNodeID,
		Summary:   "case run interrupted",
		Detail:    detail,
		Refs:      EventRefs{CaseIDs: []int64{stored.CaseNodeID}},
	})
	r.teardown(ctx, stored, session.SessionID)
}

// parkForPause applies a graceful pause: keep the completed steps and park the
// same attempt as pending so a resume continues from the checkpoint (spec §5.4
// invariant 4). The lease and the tools' context are released before the pending
// CAS: otherwise a scheduler tick could observe the pending row while the old
// runner still owns the resources and start a second runner for the same attempt.
func (r *CaseRunner) parkForPause(ctx context.Context, run domain.CaseRun, session SessionRef) {
	parked := run
	parked.Status = domain.CaseRunStatusPending
	r.releaseResources(ctx, parked, session.SessionID)

	now := r.now()
	stored, err := r.deps.CaseRuns.SetStatus(ctx, run.ID,
		domain.CaseRunStatusRunning, domain.CaseRunStatusPending, now)
	if err != nil {
		if errors.Is(err, domain.ErrCaseRunTerminal) {
			if latest, getErr := r.deps.CaseRuns.GetCaseRun(ctx, run.BatchID, run.ID); getErr == nil {
				r.afterResourcesReleased(ctx, latest)
				return
			}
		}
		slog.WarnContext(ctx, "execution: park paused case run failed",
			"case_run_id", run.ID, "error", err)
		r.afterResourcesReleased(ctx, run)
		return
	}
	r.publish(ctx, Event{
		Type:      "run.paused",
		VersionID: stored.VersionID,
		BatchID:   stored.BatchID,
		CaseID:    stored.CaseNodeID,
		Summary:   "case run parked by graceful pause",
		Detail: map[string]any{
			"case_run_id": eventID(stored.ID),
			"mode":        string(domain.PauseModeGraceful),
			"status":      string(stored.Status),
		},
		Refs: EventRefs{CaseIDs: []int64{stored.CaseNodeID}},
	})
	r.afterResourcesReleased(ctx, stored)
}

// finishTimeout classifies a case that exhausted its wall-clock budget as
// inconclusive(model): a budget-exhausted case never becomes failed
// (spec §5.8 invariant 8).
func (r *CaseRunner) finishTimeout(ctx context.Context, run domain.CaseRun, agentSessionID int64, session SessionRef) {
	_ = r.deps.Runtime.CancelRun(context.WithoutCancel(ctx), agentSessionID)
	if r.honorPauseBeforeTerminal(ctx, run, session) {
		return
	}
	r.finishOutcome(ctx, run, "budget_exhausted", "case_timeout", r.stepEvidenceCount(ctx, run.ID), session)
}

// handleShutdown releases the executor context and the session lease after the
// process context was cancelled, but leaves the case run running: crash/restart
// recovery (T18) owns the interrupted transition and does not auto-start a
// browser (spec §5.4, §5.12 invariant 7).
func (r *CaseRunner) handleShutdown(ctx context.Context, run domain.CaseRun, agentSessionID int64, session SessionRef) {
	_ = r.deps.Runtime.CancelRun(context.WithoutCancel(ctx), agentSessionID)
	r.releaseResources(ctx, run, session.SessionID)
}

// finishOutcome persists a server-derived terminal outcome and applies the
// batch-entry side effects.
func (r *CaseRunner) finishOutcome(ctx context.Context, run domain.CaseRun, toolCode, reason string, evidenceCount int, session SessionRef) {
	verdict, class, audit := domain.Classify(toolCode, "", evidenceCount)
	if strings.TrimSpace(reason) == "" {
		reason = audit
	}
	now := r.now()
	finished := run
	finished.Status = caseRunStatusForVerdict(verdict)
	finished.FailureClass = failureClassPointer(class)
	finished.FailureReason = reason
	finished.CleanupRequired = false
	finished.InterruptedStepSeq = nil
	finished.FinishedAt = timePointer(now)
	finished.UpdatedAt = now
	if run.StartedAt != nil {
		finished.DurationMS = nonNegativeMillis(now.Sub(*run.StartedAt))
	}

	stored, err := r.deps.CaseRuns.FinishCaseRun(ctx, finished)
	if err != nil {
		if errors.Is(err, domain.ErrCaseRunTerminal) {
			current, getErr := r.deps.CaseRuns.GetCaseRun(ctx, run.BatchID, run.ID)
			if getErr == nil {
				r.teardown(ctx, current, session.SessionID)
				return
			}
		}
		slog.WarnContext(ctx, "execution: finish case run failed",
			"case_run_id", run.ID, "error", err)
		r.teardown(ctx, run, session.SessionID)
		return
	}

	slog.InfoContext(ctx, "execution case run finished",
		"case_run_id", stored.ID, "batch_id", stored.BatchID, "status", stored.Status,
		"failure_class", class, "reason", toolCode, "duration_ms", stored.DurationMS)
	r.publishTerminal(ctx, stored)
	r.teardown(ctx, stored, session.SessionID)
}

// publishTerminal emits run.finished and case.status_changed after a
// server-derived terminal outcome committed.
func (r *CaseRunner) publishTerminal(ctx context.Context, run domain.CaseRun) {
	detail := map[string]any{
		"case_run_id": eventID(run.ID),
		"status":      string(run.Status),
		"attempt":     run.Attempt,
		"duration_ms": run.DurationMS,
	}
	if run.FailureClass != nil {
		detail["failure_class"] = string(*run.FailureClass)
	}
	if run.FailureReason != "" {
		detail["failure_reason"] = run.FailureReason
	}
	r.publish(ctx, Event{
		Type:      "run.finished",
		VersionID: run.VersionID,
		BatchID:   run.BatchID,
		CaseID:    run.CaseNodeID,
		Summary:   "case run finished",
		Detail:    detail,
		Refs:      EventRefs{CaseIDs: []int64{run.CaseNodeID}},
	})
	r.publish(ctx, Event{
		Type:      "case.status_changed",
		VersionID: run.VersionID,
		BatchID:   run.BatchID,
		CaseID:    run.CaseNodeID,
		Summary:   "case status changed",
		Detail:    detail,
		Refs:      EventRefs{CaseIDs: []int64{run.CaseNodeID}},
	})
}

// teardown applies the terminal side effects and always releases the tools'
// executor context and the session lease, including when an earlier step failed.
func (r *CaseRunner) teardown(ctx context.Context, run domain.CaseRun, sessionID int64) {
	r.afterResourcesReleased(ctx, run)
	r.releaseResources(ctx, run, sessionID)
}

// afterResourcesReleased applies the side effects that follow a run or park
// whose executor context and lease were already released.
func (r *CaseRunner) afterResourcesReleased(ctx context.Context, run domain.CaseRun) {
	r.markExecuted(ctx, run)
	if r.deps.Batches == nil {
		return
	}
	if _, err := r.deps.Batches.UpdateCounts(ctx, run.BatchID); err != nil {
		slog.WarnContext(ctx, "execution: recount batch failed",
			"batch_id", run.BatchID, "case_run_id", run.ID, "error", err)
	}
}

// markExecuted moves a reviewed case node to executed. Failed and blocked cases
// are marked executed too; the outcome lives on the case run (spec §5.2).
func (r *CaseRunner) markExecuted(ctx context.Context, run domain.CaseRun) {
	if r.deps.Mindmap == nil {
		return
	}
	switch run.Status {
	case domain.CaseRunStatusPassed, domain.CaseRunStatusFailed,
		domain.CaseRunStatusBlocked, domain.CaseRunStatusInconclusive:
	default:
		return
	}
	if err := r.deps.Mindmap.MarkExecuted(context.WithoutCancel(ctx), run.VersionID, []int64{run.CaseNodeID}); err != nil {
		slog.WarnContext(ctx, "execution: mark case node executed failed",
			"case_node_id", run.CaseNodeID, "error", err)
	}
}

// releaseResources closes the tools' executor context and releases the session
// lease. Cleanup runs on a detached context so a cancelled run still frees
// resources. A case run that never opened a browser context closes as a no-op.
func (r *CaseRunner) releaseResources(ctx context.Context, run domain.CaseRun, sessionID int64) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupWait)
	defer cancel()

	if r.deps.Browser != nil {
		if err := r.deps.Browser.CloseCase(cleanupCtx, run.ID); err != nil {
			slog.WarnContext(ctx, "execution: close case browser context failed",
				"case_run_id", run.ID, "error", err)
		}
	}
	if r.deps.Sessions == nil {
		return
	}
	if sessionID <= 0 {
		sessionID = 0
		if run.SessionID != nil {
			sessionID = *run.SessionID
		}
	}
	if sessionID <= 0 {
		return
	}
	if err := r.deps.Sessions.Release(cleanupCtx, sessionID, caseRunHolder(run.ID), releaseReason(run.Status)); err != nil {
		slog.WarnContext(ctx, "execution: release session lease failed",
			"case_run_id", run.ID, "session_id", sessionID, "error", err)
	}
}

// pinAgentRun persists the agent session/run ids of the started run. It writes
// the freshly read row so a pause/cancel that landed while StartCaseRun was in
// flight is not overwritten with a stale running status (Review Focus #1); a
// run that is no longer running keeps its stored state.
func (r *CaseRunner) pinAgentRun(ctx context.Context, run domain.CaseRun, agent AgentRunRef) domain.CaseRun {
	if r.deps.CaseRuns == nil {
		return run
	}
	current, err := r.deps.CaseRuns.GetCaseRun(ctx, run.BatchID, run.ID)
	if err != nil {
		slog.WarnContext(ctx, "execution: read case run before pinning agent run failed",
			"case_run_id", run.ID, "error", err)
		return run
	}
	if current.Status != domain.CaseRunStatusRunning {
		return current
	}
	current.AgentSessionID = int64Pointer(agent.SessionID)
	current.AgentRunID = int64Pointer(agent.RunID)
	stored, err := r.deps.CaseRuns.UpdateCaseRun(ctx, current)
	if err != nil {
		slog.WarnContext(ctx, "execution: pin agent run failed",
			"case_run_id", run.ID, "error", err)
		return run
	}
	return stored
}

// checkpoint derives the resume point handed to the new agent run.
func (r *CaseRunner) checkpoint(ctx context.Context, run domain.CaseRun) ([]int, *int) {
	completed := []int{}
	if r.deps.Steps == nil {
		return completed, run.InterruptedStepSeq
	}
	steps, err := r.deps.Steps.ListByCaseRun(ctx, run.ID)
	if err != nil {
		slog.WarnContext(ctx, "execution: read checkpoint steps failed",
			"case_run_id", run.ID, "error", err)
		return completed, run.InterruptedStepSeq
	}
	completed, _ = domain.Checkpoint(steps)
	return completed, run.InterruptedStepSeq
}

// pauseRequested reads the authoritative batch pause flag.
func (r *CaseRunner) pauseRequested(ctx context.Context, run domain.CaseRun) (domain.RunBatch, bool) {
	if r.deps.Batches == nil {
		return domain.RunBatch{}, false
	}
	batch, err := r.deps.Batches.GetBatch(ctx, run.VersionID, run.BatchID)
	if err != nil {
		return domain.RunBatch{}, false
	}
	return batch, true
}

// stepEvidenceCount is the evidence attached to the run's recorded steps. It
// feeds the classification downgrade that never blames the application without
// evidence (spec §5.8).
func (r *CaseRunner) stepEvidenceCount(ctx context.Context, caseRunID int64) int {
	if r.deps.Steps == nil {
		return 0
	}
	steps, err := r.deps.Steps.ListByCaseRun(ctx, caseRunID)
	if err != nil {
		return 0
	}
	total := 0
	for _, step := range steps {
		total += step.EvidenceCount
	}
	return total
}

// interruptedStepSeq records the step that was in flight when an immediate
// pause landed: the running step, or the next unfinished snapshot step.
func (r *CaseRunner) interruptedStepSeq(ctx context.Context, run domain.CaseRun) *int {
	if r.deps.Steps == nil {
		return run.InterruptedStepSeq
	}
	steps, err := r.deps.Steps.ListByCaseRun(ctx, run.ID)
	if err != nil {
		return run.InterruptedStepSeq
	}
	for _, step := range steps {
		if step.Status == domain.StepStatusRunning {
			return intPointer(step.Seq)
		}
	}
	_, next := domain.Checkpoint(steps)
	if next > 0 && next <= len(run.Snapshot.Steps) {
		return intPointer(next)
	}
	return nil
}

// publish emits one execution event, logging and swallowing publisher errors.
func (r *CaseRunner) publish(ctx context.Context, event Event) {
	if r.deps.Events == nil {
		return
	}
	if err := r.deps.Events.Publish(ctx, event); err != nil {
		slog.WarnContext(ctx, "execution event publish failed",
			"type", event.Type,
			"version_id", event.VersionID,
			"batch_id", event.BatchID,
			"error", err,
		)
	}
}

// agentTerminalCode maps a terminal agent run state to a classification tool
// code and an audit reason. The P1 terminal reason is authoritative when it
// names a cause: an LLM/model failure is inconclusive(model) even though the
// run's raw error message is free-form, and a budget cause is
// inconclusive(model, budget_exhausted). A normal end without case_finish is
// inconclusive(case), never failed. A shutdown/interrupted reason falls through
// to the status mapping so a paused run keeps its interrupted semantics.
func agentTerminalCode(state AgentRunState) (code, reason string) {
	status := strings.ToLower(strings.TrimSpace(state.Status))
	terminalReason := strings.ToLower(strings.TrimSpace(state.TerminalReason))
	switch terminalReason {
	case "model_error", "llm_error", "gateway_error":
		return "model_error", terminalReason
	case "context_overflow", "max_turns", "run_hard_timeout", "timeout":
		return "budget_exhausted", terminalReason
	case "shutdown", "interrupted":
		// A pause/shutdown ends the run without a model fault; keep the
		// status-derived classification instead of reclassifying it.
	}
	switch status {
	case "done":
		return "agent_output_invalid", "case run ended without case_finish"
	case "failed", "aborted":
		detail := strings.TrimSpace(state.Error)
		if knownAgentToolCode(detail) {
			return detail, detail
		}
		// A failed/aborted run whose cause is not a recognized tool code is an
		// infrastructure/model fault, never a case defect (spec §5.8).
		return "internal", detail
	case "interrupted", "canceled", "cancelled":
		return "internal", "agent run " + status
	default:
		return "internal", "agent run ended as " + status
	}
}

// knownAgentToolCodes mirrors the tool error codes domain.Classify recognizes
// (spec §5.8). A failed/aborted P1 run carries a tool code when the agent
// produced one, but a model/infrastructure failure carries a free-form message;
// the lookup keeps such a message from being mistaken for a case defect.
var knownAgentToolCodes = map[string]struct{}{
	"host_not_allowed":           {},
	"dns_failed":                 {},
	"dns_error":                  {},
	"connection_refused":         {},
	"connection_reset":           {},
	"network_error":              {},
	"tls_error":                  {},
	"env_unreachable":            {},
	"executor_unavailable":       {},
	"executor_unreachable":       {},
	"session_unauthorized":       {},
	"session_expired":            {},
	"session_invalid":            {},
	"no_session":                 {},
	"budget_exhausted":           {},
	"budget_exceeded":            {},
	"context_overflow":           {},
	"max_turns":                  {},
	"model_error":                {},
	"llm_error":                  {},
	"gateway_error":              {},
	"internal":                   {},
	"unsupported_assertion_kind": {},
	"invalid_arguments":          {},
	"invalid_selector":           {},
	"invalid_finish":             {},
	"target_ambiguous":           {},
	"case_invalid":               {},
	"invalid_case":               {},
	"agent_output_invalid":       {},
	"target_not_found":           {},
	"timeout":                    {},
	"assertion_mismatch":         {},
	"assertion_failed":           {},
	"step_failed":                {},
}

// knownAgentToolCode reports whether code is a tool error code the failure
// classifier understands.
func knownAgentToolCode(code string) bool {
	if code == "" {
		return false
	}
	_, ok := knownAgentToolCodes[strings.ToLower(code)]
	return ok
}

// toolCodeForError maps an infrastructure error to the classification tool code
// (spec §5.8). Only target/assertion evidence problems may become application
// failures, so unknown codes fall back to model/internal.
func toolCodeForError(err error) string {
	if err == nil {
		return "internal"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "budget_exhausted"
	}
	var browserErr *BrowserError
	if errors.As(err, &browserErr) {
		switch browserErr.Code {
		case "connection_failed":
			return "connection_refused"
		case "unauthorized":
			return "session_unauthorized"
		case "context_limit":
			return "executor_unavailable"
		}
		if browserErr.Code != "" {
			return browserErr.Code
		}
	}
	var reasonErr *domain.ReasonError
	if errors.As(err, &reasonErr) {
		if reasonErr.Reason != "" {
			return reasonErr.Reason
		}
		if reasonErr.Code != "" {
			return reasonErr.Code
		}
	}
	return "internal"
}

// isTerminalAgentStatus reports whether an agent run reached a terminal state.
func isTerminalAgentStatus(status string) bool {
	_, ok := terminalAgentRunStatuses[strings.ToLower(strings.TrimSpace(status))]
	return ok
}

// pauseModeOf returns the batch pause mode with the graceful default.
func pauseModeOf(batch domain.RunBatch) domain.PauseMode {
	if batch.PauseMode == domain.PauseModeImmediate {
		return domain.PauseModeImmediate
	}
	return domain.PauseModeGraceful
}

// caseRunStatusForVerdict maps a terminal verdict to the case run status.
func caseRunStatusForVerdict(verdict domain.Verdict) domain.CaseRunStatus {
	switch verdict {
	case domain.VerdictPassed:
		return domain.CaseRunStatusPassed
	case domain.VerdictFailed:
		return domain.CaseRunStatusFailed
	case domain.VerdictBlocked:
		return domain.CaseRunStatusBlocked
	default:
		return domain.CaseRunStatusInconclusive
	}
}

// releaseReason records why a lease was released in the resource audit trail.
func releaseReason(status domain.CaseRunStatus) string {
	switch status {
	case domain.CaseRunStatusPassed, domain.CaseRunStatusFailed,
		domain.CaseRunStatusBlocked, domain.CaseRunStatusInconclusive:
		return "finished"
	case domain.CaseRunStatusPending:
		return "paused"
	case domain.CaseRunStatusInterrupted:
		return "interrupted"
	default:
		return "released"
	}
}

// nonNegativeMillis clamps a duration to a non-negative millisecond count.
func nonNegativeMillis(duration time.Duration) int64 {
	ms := duration.Milliseconds()
	if ms < 0 {
		return 0
	}
	return ms
}

// intPointer returns a pointer to a copy of value.
func intPointer(value int) *int { return &value }

// configuredCaseTimeout reads and validates EZTEST_EXEC_CASE_TIMEOUT_MS.
func configuredCaseTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv(envCaseTimeoutMS))
	if raw == "" {
		return DefaultCaseTimeout
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms <= 0 {
		return DefaultCaseTimeout
	}
	return time.Duration(ms) * time.Millisecond
}
