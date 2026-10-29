package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// Autonomous worker defaults (spec §6.2).
const (
	// DefaultAutonomyTick is the autonomous worker poll interval.
	DefaultAutonomyTick = 2 * time.Second
	// DefaultAutonomyBatchesPerTick is the per-tick batch budget used when
	// EZTEST_AUTONOMY_BATCHES_PER_TICK is not configured.
	DefaultAutonomyBatchesPerTick = 4
	// MinAutonomyBatchesPerTick and MaxAutonomyBatchesPerTick are the spec §16.1
	// bounds of the per-tick batch budget.
	MinAutonomyBatchesPerTick = 1
	MaxAutonomyBatchesPerTick = 20
	// envAutonomyBatchesPerTick overrides the per-tick batch budget.
	envAutonomyBatchesPerTick = "EZTEST_AUTONOMY_BATCHES_PER_TICK"
)

// AutonomyWorkerDeps are the ports of the autonomous worker. Store and Versions
// are required; analysis, report, supplement, impact, finalizer and delivery may
// be nil, in which case the corresponding step degrades and the chain still
// settles the batch (spec §6.7). Actor is the fallback system actor used for
// writes when a batch has no creator (a P6 legacy row); a batch with a creator
// attributes its autonomous writes to that user. Clock is injectable for the
// pass-duration diagnostic. BatchesPerTick overrides the environment budget.
type AutonomyWorkerDeps struct {
	Store          AutonomyBatchStore
	Versions       VersionPort
	Analysis       AnalysisPort
	Reports        ReportPort
	Supplements    SupplementPort
	Impact         CodeImpactPort
	Finalizer      VersionFinalizer
	Delivery       DeliveryPort
	Clock          Clock
	Actor          int64
	BatchesPerTick int
	// Leader gates every worker pass behind the multi-instance advisory lock
	// (design spec §10.5). A nil lock keeps the historical single-instance
	// behavior of running every pass.
	Leader TickLock
	// LeaderKey is the advisory lock key of the autonomous loop; normally
	// AutonomyLeaderKey(EZTEST_LEADER_LOCK_KEY).
	LeaderKey int64
}

// AutonomyWorker is the execution-context autonomous orchestrator of spec §6.2.
// It is deliberately single-instance: the scheduler and the composition root
// start exactly one loop, and every advance is derived from stored state plus a
// compare-and-swap (supplement_attempted / autonomy_done) so a restart resumes
// the chain and a duplicate tick is harmless.
type AutonomyWorker struct {
	deps           AutonomyWorkerDeps
	tick           time.Duration
	batchesPerTick int
	notify         chan struct{}

	// staleMu guards staleRegenerated: the per-chain stale-report marker that
	// keeps a status which never clears from regenerating on every tick. It is
	// cleared when the chain settles so a manually reopened chain starts fresh.
	staleMu          sync.Mutex
	staleRegenerated map[int64]struct{}

	// escalationMu guards escalationsMaterialized: the in-memory one-shot marker
	// of supplement/incremental-design runs whose requirement-gap escalations were
	// already handed to delivery. A restart re-calls delivery, whose hard dedupe
	// makes the replay a no-op.
	escalationMu    sync.Mutex
	escalationsDone map[int64]struct{}

	mu      sync.Mutex
	cancel  context.CancelFunc
	running bool
	wg      sync.WaitGroup
}

// NewAutonomyWorker wires the worker. A zero BatchesPerTick reads
// EZTEST_AUTONOMY_BATCHES_PER_TICK and otherwise falls back to
// DefaultAutonomyBatchesPerTick; every value is clamped to spec §16.1's 1–20
// range so a typo cannot disable the chain or scan the whole database in one
// tick.
func NewAutonomyWorker(deps AutonomyWorkerDeps) *AutonomyWorker {
	if deps.Clock == nil {
		deps.Clock = wallClock{}
	}
	if deps.BatchesPerTick <= 0 {
		deps.BatchesPerTick = configuredAutonomyBatchesPerTick()
	}
	deps.BatchesPerTick = clampAutonomyBatchesPerTick(deps.BatchesPerTick)
	return &AutonomyWorker{
		deps:             deps,
		tick:             DefaultAutonomyTick,
		batchesPerTick:   deps.BatchesPerTick,
		notify:           make(chan struct{}, 1),
		staleRegenerated: make(map[int64]struct{}),
		escalationsDone:  make(map[int64]struct{}),
	}
}

// Start runs the 2s loop until ctx is cancelled or Stop is called. Starting
// twice is a no-op, which keeps the single-instance guarantee in-process. The
// scheduler's OnBatchTerminal signal wakes the loop immediately.
func (w *AutonomyWorker) Start(ctx context.Context) {
	if w == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return
	}
	workerCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.running = true
	w.wg.Add(1)
	w.mu.Unlock()

	go func() {
		defer w.wg.Done()
		defer func() {
			w.mu.Lock()
			w.running = false
			w.mu.Unlock()
		}()
		// Run one pass immediately: it retries the finalize sweep for a crash
		// window left by the previous process and picks up batches that are
		// already pending, instead of waiting a full tick.
		if err := w.RunOnce(workerCtx); err != nil && workerCtx.Err() == nil {
			slog.WarnContext(workerCtx, "execution autonomy worker tick failed", "error", err)
		}
		ticker := time.NewTicker(w.tick)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			case <-w.notify:
			}
			if err := w.RunOnce(workerCtx); err != nil && workerCtx.Err() == nil {
				slog.WarnContext(workerCtx, "execution autonomy worker tick failed", "error", err)
			}
		}
	}()
}

// Stop terminates the worker loop and waits for its goroutine. It is safe to
// call without a running worker and more than once.
func (w *AutonomyWorker) Stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	cancel := w.cancel
	w.cancel = nil
	w.running = false
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	w.wg.Wait()
}

// OnBatchTerminal implements AutonomyTrigger. It wakes the loop without
// blocking; the batch is advanced from its stored state on the next pass, so a
// lost signal (worker not started yet, stop in progress) is harmless.
func (w *AutonomyWorker) OnBatchTerminal(ctx context.Context, batch domain.RunBatch) {
	if w == nil {
		return
	}
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

// MarkDone settles one batch chain with the autonomy_done CAS of spec §6.2 step
// 6. won=false means another advance already settled the batch. Scheduler and
// tests may call it directly; the worker itself only calls it after every
// preceding step settled.
func (w *AutonomyWorker) MarkDone(ctx context.Context, batchID int64) (bool, error) {
	if w == nil || w.deps.Store == nil {
		return false, errors.New("execution: autonomy store is not configured")
	}
	return w.deps.Store.MarkAutonomyDone(ctx, batchID)
}

// ResetAutonomyDone reopens the chain of an already settled terminal batch
// (autonomy_done true -> false). The manual-analysis handler (T17) calls it
// after it starts a new failure analysis for a completed chain, so the worker
// re-enters the batch: the newer failure run is triaged again, the report is
// re-checked and the chain is settled again. won=false means the chain was not
// settled (a plain T16 retry) and nothing was reset.
func (w *AutonomyWorker) ResetAutonomyDone(ctx context.Context, batchID int64) (bool, error) {
	if w == nil || w.deps.Store == nil {
		return false, errors.New("execution: autonomy store is not configured")
	}
	return w.deps.Store.ResetAutonomyDone(ctx, batchID)
}

// RunOnce performs one worker pass: it retries the DB-derived finalize sweep,
// then advances at most BatchesPerTick terminal batches whose autonomy chain is
// not settled and at most BatchesPerTick continuing+designing versions. Every
// run is advanced from its stored state; handled transitions are not errors,
// infrastructure failures are returned after every batch had its chance.
func (w *AutonomyWorker) RunOnce(ctx context.Context) error {
	if w == nil || w.deps.Store == nil {
		return errors.New("execution: autonomy store is not configured")
	}
	if w.deps.Versions == nil {
		return errors.New("execution: autonomy version port is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// A pass that outlives the tick delays every following pass; surface it once
	// per overrun instead of silently drifting. The clock is injectable so tests
	// observe no diagnostic.
	clock := w.deps.Clock
	if clock == nil {
		clock = wallClock{}
	}
	startedAt := clock.Now()
	defer func() {
		if elapsed := clock.Now().Sub(startedAt); elapsed > w.tick {
			slog.WarnContext(ctx, "execution autonomy worker pass exceeded the tick interval",
				"elapsed", elapsed, "tick", w.tick)
		}
	}()
	// One autonomous pass at a time across API instances (spec §10.5). A
	// non-leader skips the pass entirely; the chain is derived from stored state,
	// so the next leader pass resumes it safely.
	release, ok, err := acquireAutonomyTick(ctx, w.deps)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	defer release()

	limit := w.batchesPerTick
	if limit <= 0 {
		limit = DefaultAutonomyBatchesPerTick
	}

	var firstErr error
	if sweepErr := w.finalizeSweep(ctx); sweepErr != nil {
		firstErr = sweepErr
	}

	batches, err := w.deps.Store.PendingAutonomyBatches(ctx, limit)
	if err != nil {
		if firstErr != nil {
			// The scan failure is logged by the caller; the sweep error keeps its
			// turn as the returned retry signal.
			slog.WarnContext(ctx, "execution autonomy: list unsettled terminal batches failed",
				"error", err)
			return firstErr
		}
		return fmt.Errorf("execution autonomy: list unsettled terminal batches: %w", err)
	}
	for i := range batches {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if batchErr := w.processBatch(ctx, batches[i]); batchErr != nil {
			slog.WarnContext(ctx, "execution autonomy batch failed",
				"batch_id", batches[i].ID, "version_id", batches[i].VersionID, "error", batchErr)
			if firstErr == nil {
				firstErr = batchErr
			}
		}
	}

	designing, err := w.deps.Store.DesigningVersions(ctx, limit)
	if err != nil {
		if firstErr == nil {
			firstErr = fmt.Errorf("execution autonomy: list designing versions: %w", err)
		}
		return firstErr
	}
	for i := range designing {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if designErr := w.processDesigningVersion(ctx, designing[i]); designErr != nil {
			slog.WarnContext(ctx, "execution autonomy designing version failed",
				"version_id", designing[i].Version.ID, "error", designErr)
			if firstErr == nil {
				firstErr = designErr
			}
		}
	}
	return firstErr
}

// finalizeSweep retries TryFinalize for versions whose batches all settled but
// whose version did not complete: a crash between the batch's autonomy_done CAS
// and the finalize call, or a finalize error, otherwise leaves the version
// executing forever because the pending scan only reads autonomy_done=false.
// The scan is DB-derived and bounded by the per-tick budget; a successful
// finalize removes the version from the next scan, a failed one is retried on
// the next tick.
func (w *AutonomyWorker) finalizeSweep(ctx context.Context) error {
	if w == nil || w.deps.Store == nil || w.deps.Finalizer == nil {
		return nil
	}
	limit := w.batchesPerTick
	if limit <= 0 {
		limit = DefaultAutonomyBatchesPerTick
	}
	versions, err := w.deps.Store.VersionsAwaitingFinalize(ctx, limit)
	if err != nil {
		return fmt.Errorf("execution autonomy: list versions awaiting finalize: %w", err)
	}
	var firstErr error
	for _, versionID := range versions {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if _, err := w.deps.Finalizer.TryFinalize(ctx, versionID); err != nil {
			slog.WarnContext(ctx, "execution autonomy: finalize sweep failed",
				"version_id", versionID, "error", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("execution autonomy: finalize version %d: %w", versionID, err)
			}
		}
	}
	return firstErr
}

// configuredAutonomyBatchesPerTick reads and clamps
// EZTEST_AUTONOMY_BATCHES_PER_TICK (spec §16.1): an unset or unparsable value
// selects the default, a value below 1 also selects the default (the chain needs
// at least one batch per tick), and a value above 20 is clamped to 20.
func configuredAutonomyBatchesPerTick() int {
	raw := os.Getenv(envAutonomyBatchesPerTick)
	if raw == "" {
		return DefaultAutonomyBatchesPerTick
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < MinAutonomyBatchesPerTick {
		return DefaultAutonomyBatchesPerTick
	}
	if value > MaxAutonomyBatchesPerTick {
		return MaxAutonomyBatchesPerTick
	}
	return value
}

// clampAutonomyBatchesPerTick bounds an explicitly configured budget.
func clampAutonomyBatchesPerTick(value int) int {
	if value < MinAutonomyBatchesPerTick {
		return DefaultAutonomyBatchesPerTick
	}
	if value > MaxAutonomyBatchesPerTick {
		return MaxAutonomyBatchesPerTick
	}
	return value
}
