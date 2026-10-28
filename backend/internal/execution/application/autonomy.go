package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// The autonomous batch chain of spec §6.2:
//
//	terminal batch
//	  -> failure analysis (only when the batch has failed(application) cases)
//	  -> issue triage (only after a done failure analysis with candidates)
//	  -> report ensure/regenerate-stale
//	  -> post-batch supplement (baseline batches only, once per batch)
//	  -> supplement batch creation from the run's new cases
//	  -> autonomy_done CAS
//	  -> version TryFinalize
//
// Every step returns ready=false when it started work or must wait for an
// asynchronous run; the next tick resumes from the stored state. A step that
// cannot start degrades (logs) and reports ready=true so an AI outage never
// wedges the chain (spec §6.7). Steps that own execution-context writes (the
// supplement batch, the autonomy_done CAS) return their errors for retry. The
// worker also runs a DB-derived finalize sweep on every pass (finalizeSweep) so
// a crash or error after the autonomy_done CAS cannot leave a version executing.

// processBatch advances one settled batch chain. Paused/completed versions and
// already settled batches are skipped, which makes the scan race-safe.
func (w *AutonomyWorker) processBatch(ctx context.Context, batch domain.RunBatch) error {
	if !isTerminalBatch(batch.Status) || batch.AutonomyDone || batch.Paused {
		return nil
	}
	version, err := w.deps.Versions.GetVersion(ctx, batch.VersionID)
	if err != nil {
		return fmt.Errorf("execution autonomy: read version %d: %w", batch.VersionID, err)
	}
	if version.Paused {
		return nil
	}
	if version.Status != versionStatusExecuting && version.Status != versionStatusContinuing {
		return nil
	}
	return w.advanceBatchChain(ctx, batch, version)
}

// advanceBatchChain advances the batch by at most one asynchronous step.
func (w *AutonomyWorker) advanceBatchChain(ctx context.Context, batch domain.RunBatch, version VersionRef) error {
	ready, err := w.advanceAnalysis(ctx, batch)
	if err != nil || !ready {
		return err
	}
	ready, err = w.advanceReport(ctx, batch, version)
	if err != nil || !ready {
		return err
	}
	// The post-batch supplement cycle is baseline-only: incremental and
	// supplement batches never trigger it, which prevents an endless loop
	// (spec §6.2 rules). A historical kind is baseline.
	if batch.Kind == "" || batch.Kind == domain.BatchKindBaseline {
		ready, err = w.advanceSupplement(ctx, batch, version)
		if err != nil || !ready {
			return err
		}
	}
	return w.finishChain(ctx, batch, version)
}

// advanceAnalysis advances the failure-analysis and issue-triage steps. It
// returns ready=true when the report step may run: no applicable failure
// analysis, a failed/degraded analysis, or a settled triage (including a failed
// one whose fallback delivery already materialized). The worker never
// re-materializes the triage fallback (T4 owns it). Read errors are returned so
// the next tick retries them; only start errors degrade (spec §6.7).
func (w *AutonomyWorker) advanceAnalysis(ctx context.Context, batch domain.RunBatch) (bool, error) {
	if w.deps.Analysis == nil {
		return true, nil
	}
	actor := w.batchActor(batch)
	failure, err := w.deps.Analysis.GetAnalysis(ctx, batch.ID, AutonomyAnalysisKindFailure)
	if err != nil {
		return false, fmt.Errorf("execution autonomy: read failure analysis of batch %d: %w", batch.ID, err)
	}
	if failure == nil {
		_, startErr := w.deps.Analysis.StartFailureAnalysis(ctx, batch.VersionID, batch.ID, actor)
		switch {
		case startErr == nil:
			return false, nil
		case errors.Is(startErr, ErrAutonomyAnalysisNotApplicable):
			return true, nil
		case errors.Is(startErr, ErrAutonomyAnalysisActive):
			existing, readErr := w.deps.Analysis.GetAnalysis(ctx, batch.ID, AutonomyAnalysisKindFailure)
			if readErr != nil {
				return false, fmt.Errorf("execution autonomy: read existing failure analysis of batch %d: %w",
					batch.ID, readErr)
			}
			if existing == nil {
				return false, nil
			}
			failure = existing
		default:
			slog.WarnContext(ctx, "execution autonomy: start failure analysis degraded",
				"batch_id", batch.ID, "error", startErr)
			return true, nil
		}
	}
	if isAutonomyActiveStatus(failure.Status) {
		return false, nil
	}
	if failure.Status != AutonomyStatusDone {
		// failed/degraded: skip triage and generate a degraded report.
		return true, nil
	}

	triage, err := w.deps.Analysis.GetAnalysis(ctx, batch.ID, AutonomyAnalysisKindIssueTriage)
	if err != nil {
		return false, fmt.Errorf("execution autonomy: read issue triage of batch %d: %w", batch.ID, err)
	}
	if triage != nil && !isAutonomyActiveStatus(triage.Status) && autonomyAnalysisNewer(failure, triage) {
		// A manual re-analysis reopened the chain (ResetAutonomyDone) and produced
		// a newer failure run than the stored triage. The old triage result is
		// stale, so run the triage step again for the new candidates. An active
		// older triage is awaited first; it is replaced after it settles.
		triage = nil
	}
	if triage == nil {
		if failure.CandidateCount <= 0 {
			return true, nil
		}
		_, startErr := w.deps.Analysis.StartIssueTriage(ctx, batch.VersionID, batch.ID, actor)
		switch {
		case startErr == nil:
			return false, nil
		case errors.Is(startErr, ErrAutonomyAnalysisNotApplicable):
			return true, nil
		case errors.Is(startErr, ErrAutonomyAnalysisActive):
			existing, readErr := w.deps.Analysis.GetAnalysis(ctx, batch.ID, AutonomyAnalysisKindIssueTriage)
			if readErr != nil {
				return false, fmt.Errorf("execution autonomy: read existing issue triage of batch %d: %w",
					batch.ID, readErr)
			}
			if existing == nil {
				return false, nil
			}
			triage = existing
		default:
			slog.WarnContext(ctx, "execution autonomy: start issue triage degraded",
				"batch_id", batch.ID, "error", startErr)
			return true, nil
		}
	}
	if isAutonomyActiveStatus(triage.Status) {
		return false, nil
	}
	return true, nil
}

// advanceReport ensures the batch report exists and is fresh (spec §6.2 step 3).
// A missing report is generated and confirmed on the next tick; a stale latest
// report is regenerated at most once per chain: if the status does not clear, a
// marker lets the chain settle instead of regenerating on every tick.
func (w *AutonomyWorker) advanceReport(ctx context.Context, batch domain.RunBatch, version VersionRef) (bool, error) {
	if w.deps.Reports == nil {
		return true, nil
	}
	actor := w.batchActor(batch)
	report, err := w.deps.Reports.LatestReport(ctx, version.ID, batch.ID)
	if err != nil {
		return false, fmt.Errorf("execution autonomy: read latest report of batch %d: %w", batch.ID, err)
	}
	if report == nil {
		if _, _, err := w.deps.Reports.EnsureReport(ctx, version.ID, batch.ID, actor); err != nil {
			return false, fmt.Errorf("execution autonomy: ensure report of batch %d: %w", batch.ID, err)
		}
		return false, nil
	}
	if report.Status == AutonomyReportStatusStale {
		if w.staleReportRegenerated(batch.ID) {
			// The chain already regenerated this report once and the status still
			// did not clear; continuing is bounded, never an infinite regeneration
			// loop.
			return true, nil
		}
		if _, err := w.deps.Reports.RegenerateReport(ctx, version.ID, report.ID, actor); err != nil {
			return false, fmt.Errorf("execution autonomy: regenerate report %d of batch %d: %w",
				report.ID, batch.ID, err)
		}
		w.markStaleReportRegenerated(batch.ID)
		return false, nil
	}
	return true, nil
}

// advanceSupplement runs the baseline post-batch supplement cycle. The
// supplement_attempted CAS makes the cycle run at most once per batch.
func (w *AutonomyWorker) advanceSupplement(ctx context.Context, batch domain.RunBatch, version VersionRef) (bool, error) {
	if w.deps.Supplements == nil {
		return true, nil
	}
	if !batch.SupplementAttempted {
		won, err := w.deps.Store.MarkSupplementAttempted(ctx, batch.ID)
		if err != nil {
			return false, fmt.Errorf("execution autonomy: mark supplement attempted for batch %d: %w",
				batch.ID, err)
		}
		if won {
			if _, err := w.deps.Supplements.StartSupplement(ctx, version.ID, batch.ID, w.batchActor(batch)); err != nil {
				if errors.Is(err, ErrAutonomyRunInProgress) {
					// The run already occupies the single-flight slot; wait for
					// the stored run instead of starting a second one.
					return false, nil
				}
				slog.WarnContext(ctx, "execution autonomy: start supplement run degraded",
					"batch_id", batch.ID, "error", err)
				return true, nil
			}
			return false, nil
		}
		// The CAS lost: another advance already started the cycle; inspect the
		// stored run in this tick.
	}
	return w.advanceSupplementResult(ctx, batch, version)
}

// advanceSupplementResult processes the stored supplement run of the batch:
// wait while active, create the child supplement batch when the run produced new
// cases and none exists yet, otherwise settle the chain. A failed/degraded run
// and a run without new cases settle the chain without creating a batch.
func (w *AutonomyWorker) advanceSupplementResult(ctx context.Context, batch domain.RunBatch, version VersionRef) (bool, error) {
	run, err := w.deps.Supplements.GetSupplementRun(ctx, batch.ID)
	if err != nil {
		return false, fmt.Errorf("execution autonomy: read supplement run of batch %d: %w", batch.ID, err)
	}
	if run == nil || run.Kind != AutonomyRunKindSupplement {
		return true, nil
	}
	if isAutonomyActiveStatus(run.Status) {
		return false, nil
	}
	if run.Status != AutonomyStatusDone || len(run.NewCaseIDs) == 0 {
		return true, nil
	}

	child, err := w.deps.Store.SupplementChild(ctx, batch.ID)
	if err != nil {
		return false, fmt.Errorf("execution autonomy: read supplement child of batch %d: %w", batch.ID, err)
	}
	if child != nil {
		return true, nil
	}
	actor := w.batchActor(batch)
	if _, err := w.deps.Supplements.CreateSupplementBatch(ctx, version.ID, batch.ID, run.NewCaseIDs, actor); err != nil {
		// A concurrent creator may have won the single-active-batch race; adopt
		// its child instead of failing the chain.
		existing, readErr := w.deps.Store.SupplementChild(ctx, batch.ID)
		if readErr == nil && existing != nil {
			return true, nil
		}
		if errors.Is(err, ErrAutonomySupplementRejected) {
			// The case set is permanently invalid (invalid_case_ids /
			// too_many_cases); retrying it on every tick cannot succeed, so settle
			// the chain degraded without a supplement child.
			slog.WarnContext(ctx, "execution autonomy: supplement batch permanently rejected; settling without child",
				"batch_id", batch.ID, "version_id", version.ID, "new_cases", len(run.NewCaseIDs), "error", err)
			return true, nil
		}
		return false, fmt.Errorf("execution autonomy: create supplement batch for %d: %w", batch.ID, err)
	}
	if err := w.deps.Supplements.MarkNodesMerged(ctx, version.ID, run.NewCaseIDs, actor); err != nil {
		// BatchService.Create already marks the selected nodes merged; the second
		// call is idempotent and only logged (spec §5.3 post-commit behavior).
		slog.WarnContext(ctx, "execution autonomy: mark supplement nodes merged failed",
			"batch_id", batch.ID, "version_id", version.ID, "error", err)
	}
	return false, nil
}

// finishChain settles the batch with the autonomy_done CAS and lets the version
// service decide whether the version may complete. A lost CAS means another
// advance already settled the chain and owns the finalize call, so the worker
// never finalizes twice for one batch.
func (w *AutonomyWorker) finishChain(ctx context.Context, batch domain.RunBatch, version VersionRef) error {
	won, err := w.MarkDone(ctx, batch.ID)
	if err != nil {
		return fmt.Errorf("execution autonomy: mark autonomy done for batch %d: %w", batch.ID, err)
	}
	if !won {
		return nil
	}
	w.clearStaleReportRegenerated(batch.ID)
	if w.deps.Finalizer == nil {
		return nil
	}
	// The chain is already settled and TryFinalize is idempotent; the finalize
	// must still run when the tick context was cancelled between the CAS and this
	// call (a shutdown window), so the post-CAS write ignores cancellation.
	if _, err := w.deps.Finalizer.TryFinalize(context.WithoutCancel(ctx), version.ID); err != nil {
		// The batch is already settled; the version service evaluates its own
		// conditions and TryFinalize is idempotent, so an error is logged for the
		// next operational retry instead of reopening the batch.
		slog.WarnContext(ctx, "execution autonomy: finalize version failed",
			"version_id", version.ID, "batch_id", batch.ID, "error", err)
	}
	return nil
}

// processDesigningVersion advances the continuing+designing segment of spec
// §6.1/§6.2: it waits for the P4 impact analysis and starts the incremental
// design run. It never creates the incremental batch; the user confirms the
// plan and calls the batch endpoint (spec §6.1 step 4/5).
func (w *AutonomyWorker) processDesigningVersion(ctx context.Context, designing DesigningVersion) error {
	version, err := w.deps.Versions.GetVersion(ctx, designing.Version.ID)
	if err != nil {
		return fmt.Errorf("execution autonomy: read designing version %d: %w", designing.Version.ID, err)
	}
	if version.Paused || version.Status != versionStatusContinuing ||
		version.ContinuePhase != continuePhaseDesigning {
		return nil
	}
	if designing.Parent == nil || designing.Parent.ID <= 0 {
		return nil
	}
	if w.deps.Supplements == nil {
		return nil
	}
	actor := w.batchActor(*designing.Parent)
	existing, err := w.deps.Supplements.GetSupplementRun(ctx, designing.Parent.ID)
	if err != nil {
		return fmt.Errorf("execution autonomy: read design run of batch %d: %w", designing.Parent.ID, err)
	}
	if existing != nil && existing.Kind == AutonomyRunKindIncrementalDesign {
		// Active runs are awaited; a done/degraded run waits for the user's plan
		// confirmation. Neither path auto-creates the incremental batch.
		return nil
	}
	if w.deps.Impact != nil {
		ready, err := w.ensureDesignImpact(ctx, version, designing.Parent)
		if err != nil {
			return err
		}
		if !ready {
			return nil
		}
	}
	if _, err := w.deps.Supplements.StartIncrementalDesign(ctx, version.ID, designing.Parent.ID, actor); err != nil {
		if errors.Is(err, ErrAutonomyRunInProgress) {
			return nil
		}
		// An unavailable supplement agent leaves the plan empty with a warning;
		// it never blocks the version or auto-creates a batch (spec §6.7).
		slog.WarnContext(ctx, "execution autonomy: start incremental design run degraded",
			"version_id", version.ID, "parent_batch_id", designing.Parent.ID, "error", err)
	}
	return nil
}

// ensureDesignImpact triggers and awaits the P4 impact analysis of the parent
// batch head. It returns ready=true when the design run may start: the analysis
// is done, is degraded/failed (impact_unavailable), the version has no code
// history, or the impact port is missing. It waits while the analysis is active.
func (w *AutonomyWorker) ensureDesignImpact(ctx context.Context, version VersionRef, parent *domain.RunBatch) (bool, error) {
	base := ""
	if parent.HeadCommit != nil {
		base = *parent.HeadCommit
	}
	if base == "" {
		// No pinned parent head means no analysable range; the design run
		// proceeds with an empty impact and a warning (spec §6.1 "无代码仓").
		slog.WarnContext(ctx, "execution autonomy: parent batch has no head commit; continuing design without code impact",
			"version_id", version.ID, "parent_batch_id", parent.ID)
		return true, nil
	}
	latest, err := w.deps.Impact.LatestImpact(ctx, version.ID)
	if err != nil {
		slog.WarnContext(ctx, "execution autonomy: read latest impact analysis failed; degrading",
			"version_id", version.ID, "error", err)
		return true, nil
	}
	if latest != nil && latest.BaseCommit == base {
		if isAutonomyActiveStatus(latest.Status) {
			return false, nil
		}
		if latest.Status == AutonomyStatusDone || latest.Status == AutonomyStatusFailed ||
			latest.Status == AutonomyStatusDegraded {
			return true, nil
		}
	}

	id, err := w.deps.Impact.StartImpact(ctx, version.ID, base)
	if err != nil {
		switch {
		case errors.Is(err, ErrAutonomyImpactInProgress):
			return false, nil
		case errors.Is(err, ErrAutonomyImpactUnavailable):
			slog.WarnContext(ctx, "execution autonomy: code impact unavailable; continuing without impact",
				"version_id", version.ID, "error", err)
			return true, nil
		default:
			slog.WarnContext(ctx, "execution autonomy: start impact analysis degraded",
				"version_id", version.ID, "error", err)
			return true, nil
		}
	}
	view, err := w.deps.Impact.GetImpact(ctx, version.ID, id)
	if err != nil {
		// The analysis is queued; the next tick re-reads the latest one.
		return false, nil
	}
	if view == nil {
		return false, nil
	}
	if isAutonomyActiveStatus(view.Status) {
		return false, nil
	}
	return true, nil
}

// batchActor is the actor recorded on one batch chain's autonomous writes: the
// user who created the batch, so delivery issues, reports and the supplement
// child carry the same attribution as the batch. A zero creator (P6 legacy row)
// falls back to the configured system actor.
func (w *AutonomyWorker) batchActor(batch domain.RunBatch) int64 {
	if batch.CreatedBy > 0 {
		return batch.CreatedBy
	}
	return w.deps.Actor
}

// autonomyAnalysisNewer reports whether candidate was created after reference.
// Timestamps are authoritative when both runs project them; the snowflake id
// ordering is the fallback because both runs are minted by the same generator.
func autonomyAnalysisNewer(candidate, reference *AnalysisView) bool {
	if candidate == nil || reference == nil {
		return false
	}
	if !candidate.CreatedAt.IsZero() && !reference.CreatedAt.IsZero() {
		return candidate.CreatedAt.After(reference.CreatedAt)
	}
	return candidate.ID > reference.ID
}

// staleReportRegenerated reports whether this chain already regenerated its
// stale report once.
func (w *AutonomyWorker) staleReportRegenerated(batchID int64) bool {
	w.staleMu.Lock()
	defer w.staleMu.Unlock()
	_, ok := w.staleRegenerated[batchID]
	return ok
}

// markStaleReportRegenerated records the bounded regeneration of a chain.
func (w *AutonomyWorker) markStaleReportRegenerated(batchID int64) {
	w.staleMu.Lock()
	defer w.staleMu.Unlock()
	w.staleRegenerated[batchID] = struct{}{}
}

// clearStaleReportRegenerated drops the marker once the chain settled, so a
// manually reopened chain (ResetAutonomyDone) may regenerate a fresh stale
// report once again.
func (w *AutonomyWorker) clearStaleReportRegenerated(batchID int64) {
	w.staleMu.Lock()
	defer w.staleMu.Unlock()
	delete(w.staleRegenerated, batchID)
}

// isAutonomyActiveStatus reports whether a run still occupies its single-flight
// slot.
func isAutonomyActiveStatus(status string) bool {
	return status == AutonomyStatusQueued || status == AutonomyStatusRunning
}
