package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

const (
	// analysisWorkerInterval is the fixed poll interval of the delivery analysis
	// worker (spec §6.7: 2s).
	analysisWorkerInterval = 2 * time.Second
	// analysisWorkerBatch is the maximum number of active runs processed per
	// tick (spec §5.6 worker quota: 5).
	analysisWorkerBatch = 5
	// analysisRecoveryTimeout bounds the boot-time stale recovery query.
	analysisRecoveryTimeout = 10 * time.Second
	// analysisQueuedStaleAfter is the grace period after which a queued run
	// without an agent session is failed stale: it means the in-flight start lost
	// its queued->running CAS and will never advance, so the unique active slot
	// must be released (spec §6.9).
	analysisQueuedStaleAfter = 30 * time.Second
)

// ProcessOnce performs one worker tick: it reads at most five active analysis
// runs and advances each from its agent session state. A handled terminal
// transition is not an error; infrastructure failures are returned after every
// run had its chance.
func (s *AnalysisService) ProcessOnce(ctx context.Context) error {
	if s.deps.Runs == nil {
		return errors.New("delivery: analysis store is not configured")
	}
	runs, err := s.deps.Runs.ListActive(ctx, analysisWorkerBatch)
	if err != nil {
		return fmt.Errorf("delivery: list active analysis runs: %w", err)
	}
	var firstErr error
	for _, run := range runs {
		if err := s.processRun(ctx, run); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Worker starts the in-process 2s poll loop of spec §6.7. The boot-time
// stale-run recovery (and the §5.6 fallback of recovered triage runs) runs
// synchronously before the first tick: this process is the single owner of the
// analysis runs, so every queued/running leftover is failed with
// stale_run_recovered before polling starts. Starting twice is a no-op; Stop
// terminates the loop.
func (s *AnalysisService) Worker(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	workerCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.running = true
	s.wg.Add(1)
	s.mu.Unlock()

	// Synchronous boot recovery: callers (and tests) can rely on the recovered
	// rows and their fallback being materialized as soon as Worker returns.
	s.recoverStaleAtBoot(workerCtx)

	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
		}()
		ticker := time.NewTicker(analysisWorkerInterval)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
				if err := s.ProcessOnce(workerCtx); err != nil {
					slog.WarnContext(workerCtx, "delivery analysis worker tick failed", "error", err)
				}
			}
		}
	}()
}

// Stop terminates the worker loop and waits for its goroutine. It is safe to
// call without a running worker and more than once.
func (s *AnalysisService) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.running = false
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

// RecoverStale fails every queued/running analysis_run left behind by a previous
// process (spec §6.7), runs the §5.6 fallback of every recovered triage run and
// returns the number recovered. The fallback is replay-idempotent through
// IssueService.ApplyDrafts' dedupe key.
func (s *AnalysisService) RecoverStale(ctx context.Context) (int64, error) {
	if s.deps.Runs == nil {
		return 0, errors.New("delivery: analysis store is not configured")
	}
	recovered, err := s.deps.Runs.RecoverStale(ctx)
	if err != nil {
		return 0, fmt.Errorf("delivery: recover stale analysis runs: %w", err)
	}
	for _, run := range recovered {
		if run.Kind != domain.AnalysisKindIssueTriage {
			continue
		}
		if _, err := s.EnsureFallbackIssues(ctx, run.VersionID, run.BatchID, run.CreatedBy); err != nil {
			slog.WarnContext(ctx, "delivery: recovered triage fallback materialization failed",
				"analysis_id", run.ID, "batch_id", run.BatchID, "error", err)
		}
	}
	return int64(len(recovered)), nil
}

// recoverStaleAtBoot runs the startup recovery bounded by a timeout. A failure
// is logged and never fatal: the worker still polls new runs.
func (s *AnalysisService) recoverStaleAtBoot(ctx context.Context) {
	recoveryCtx, cancel := context.WithTimeout(ctx, analysisRecoveryTimeout)
	defer cancel()
	recovered, err := s.RecoverStale(recoveryCtx)
	if err != nil {
		slog.ErrorContext(recoveryCtx, "delivery analysis worker startup recovery failed", "error", err)
		return
	}
	if recovered > 0 {
		slog.DebugContext(recoveryCtx, "delivery analysis worker recovered stale runs", "recovered", recovered)
	}
}
