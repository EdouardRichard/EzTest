package agentapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
)

// Worker polling constants (P1-D4). The poll interval and heartbeat are
// constants in production; tests may shorten them through WorkerOption.
const (
	DefaultWorkerPollInterval     = 300 * time.Millisecond
	DefaultWorkerRecoveryInterval = 3 * time.Second
	defaultWorkerConcurrency      = 4
	workerShutdownWait            = 5 * time.Second
	workerWriteBackTimeout        = 5 * time.Second
)

// WorkerOption customizes worker timings (used by tests and future tuning).
type WorkerOption func(*Worker)

// WithWorkerPollInterval overrides the claim polling interval.
func WithWorkerPollInterval(interval time.Duration) WorkerOption {
	return func(w *Worker) {
		if interval > 0 {
			w.pollInterval = interval
		}
	}
}

// WithWorkerHeartbeatInterval overrides the lease heartbeat interval.
func WithWorkerHeartbeatInterval(interval time.Duration) WorkerOption {
	return func(w *Worker) {
		if interval > 0 {
			w.heartbeatInterval = interval
		}
	}
}

// WithWorkerRecoveryInterval overrides how often expired leases are reclaimed.
func WithWorkerRecoveryInterval(interval time.Duration) WorkerOption {
	return func(w *Worker) {
		if interval > 0 {
			w.recoveryInterval = interval
		}
	}
}

// WithWorkerConcurrency overrides the configured concurrency limit.
func WithWorkerConcurrency(limit int) WorkerOption {
	return func(w *Worker) {
		if limit > 0 {
			w.concurrencyOverride = limit
		}
	}
}

// WithWorkerEvents enables work_item.* event publishing.
func WithWorkerEvents(publisher eventingapp.Publisher) WorkerOption {
	return func(w *Worker) { w.events = publisher }
}

// RunSink observes every finished agent run (result and/or classified error).
// It is used by P3 to let the execution context finalize the case_run after the
// run loop ends. Sink failures are logged and never change the work item outcome.
type RunSink interface {
	OnRunFinished(ctx context.Context, item agent.WorkItem, result RunResult, runErr error) error
}

// WithWorkerRunSink registers a post-run observer. A nil sink disables it.
func WithWorkerRunSink(sink RunSink) WorkerOption {
	return func(w *Worker) { w.runSink = sink }
}

// Worker claims work items, runs them with bounded concurrency and heartbeats
// their leases. It is started by cmd/eztest and stopped by cancelling its
// context; the P1 deployment assumes a single instance.
type Worker struct {
	queue      WorkerQueue
	runner     RunExecutor
	settings   SettingsReader
	logger     *slog.Logger
	instanceID string
	events     eventingapp.Publisher
	runSink    RunSink

	pollInterval        time.Duration
	heartbeatInterval   time.Duration
	recoveryInterval    time.Duration
	concurrencyOverride int
	shutdownWait        time.Duration

	mu      sync.Mutex
	running map[int64]*workerRun
}

type workerRun struct {
	epoch  int64
	cancel context.CancelFunc
	lost   atomic.Bool
}

// NewWorker wires the P1 worker. runner is normally *Runner.
func NewWorker(queue WorkerQueue, runner RunExecutor, settings SettingsReader, logger *slog.Logger, instanceID string, options ...WorkerOption) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	worker := &Worker{
		queue:             queue,
		runner:            runner,
		settings:          settings,
		logger:            logger,
		instanceID:        instanceID,
		pollInterval:      DefaultWorkerPollInterval,
		heartbeatInterval: DefaultHeartbeat,
		recoveryInterval:  DefaultWorkerRecoveryInterval,
		shutdownWait:      workerShutdownWait,
		running:           map[int64]*workerRun{},
	}
	for _, option := range options {
		option(worker)
	}
	return worker
}

// Run processes work items until ctx is cancelled. Cancellation stops new
// claims and waits (bounded) for in-flight runs to unwind.
func (w *Worker) Run(ctx context.Context) error {
	if ctx.Err() != nil {
		return nil
	}
	if recovered, err := w.queue.RecoverExpired(ctx); err != nil {
		w.logger.Warn("agent worker: recover expired leases failed", "error", err)
	} else if recovered > 0 {
		w.logger.Info("agent worker: recovered expired leases", "count", recovered)
	}

	limit := w.concurrency()
	w.logger.Info("agent worker started", "instance_id", w.instanceID, "concurrency", limit)

	poll := time.NewTicker(w.pollInterval)
	defer poll.Stop()
	heartbeat := time.NewTicker(w.heartbeatInterval)
	defer heartbeat.Stop()
	recovery := time.NewTicker(w.recoveryInterval)
	defer recovery.Stop()

	sem := make(chan struct{}, limit)
	var inflight sync.WaitGroup

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("agent worker stopping", "instance_id", w.instanceID)
			w.waitForInflight(&inflight)
			return nil
		case <-poll.C:
			w.claim(ctx, sem, &inflight)
		case <-heartbeat.C:
			w.heartbeatAll(ctx)
		case <-recovery.C:
			recovered, err := w.queue.RecoverExpired(ctx)
			if err != nil {
				if ctx.Err() == nil {
					w.logger.Warn("agent worker: recover expired leases failed", "error", err)
				}
			} else if recovered > 0 {
				w.logger.Info("agent worker: recovered expired leases", "count", recovered)
			}
		}
	}
}

func (w *Worker) claim(ctx context.Context, sem chan struct{}, inflight *sync.WaitGroup) {
	for {
		select {
		case sem <- struct{}{}:
		default:
			return
		}
		item, ok, err := w.queue.Claim(ctx, w.instanceID)
		if err != nil {
			<-sem
			if ctx.Err() == nil {
				w.logger.Warn("agent worker: claim failed", "error", err)
			}
			return
		}
		if !ok {
			<-sem
			return
		}
		inflight.Add(1)
		go func(item agent.WorkItem) {
			defer inflight.Done()
			defer func() { <-sem }()
			w.execute(ctx, item)
		}(item)
	}
}

func (w *Worker) execute(parent context.Context, item agent.WorkItem) {
	runCtx, cancel := context.WithCancel(parent)
	entry := &workerRun{epoch: item.LeaseEpoch, cancel: cancel}
	w.track(item.ID, entry)
	defer w.untrack(item.ID)
	defer cancel()

	if err := w.queue.MarkRunning(runCtx, item.ID, item.LeaseEpoch); err != nil {
		if IsLeaseLost(err) {
			w.logger.Warn("agent worker: lease lost before run", "work_item_id", item.ID, "error", err)
			return
		}
		w.logger.Error("agent worker: mark running failed", "work_item_id", item.ID, "error", err)
		w.recordFailure(item, err, true)
		return
	}
	w.publish(item, "work_item.claimed", map[string]any{
		"work_item_id": idText(item.ID),
		"type":         item.Type,
		"claimed_by":   w.instanceID,
		"lease_epoch":  item.LeaseEpoch,
	})

	result, runErr := w.runner.Run(runCtx, item)
	if entry.lost.Load() {
		w.logger.Warn("agent worker: dropping result after lease loss", "work_item_id", item.ID)
		return
	}
	// A lease-lost run has no authority to finalize its case_run: the new owner
	// will. Observing it here would double-finalize/overwrite the redispatch.
	if !isRunLeaseLost(runErr) {
		w.notifyRunFinished(item, result, runErr)
	}
	if runErr == nil && result.Status == agent.AgentRunDone {
		if err := w.complete(item); err != nil {
			w.logger.Error("agent worker: complete failed", "work_item_id", item.ID, "error", err)
		}
		return
	}
	retryable := IsRetryable(runErr)
	if runErr == nil {
		retryable = true
		runErr = fmt.Errorf("agent run finished with status %s", result.Status)
	}
	w.recordFailure(item, runErr, retryable)
}

// isRunLeaseLost reports whether a finished run lost its lease, either as a
// classified RunError or as a platform lease_lost error.
func isRunLeaseLost(err error) bool {
	if IsLeaseLost(err) {
		return true
	}
	var runErr *RunError
	return errors.As(err, &runErr) && runErr.Reason == "lease_lost"
}

func (w *Worker) complete(item agent.WorkItem) error {
	ctx, cancel := context.WithTimeout(context.Background(), workerWriteBackTimeout)
	defer cancel()
	err := w.queue.Complete(ctx, item.ID, item.LeaseEpoch)
	if IsLeaseLost(err) {
		w.logger.Warn("agent worker: lease lost while completing", "work_item_id", item.ID)
	}
	return err
}

// notifyRunFinished calls the optional RunSink with a fresh timeout context (the
// run context may already be canceled). A sink failure is only logged.
func (w *Worker) notifyRunFinished(item agent.WorkItem, result RunResult, runErr error) {
	if w.runSink == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), workerWriteBackTimeout)
	defer cancel()
	if err := w.runSink.OnRunFinished(ctx, item, result, runErr); err != nil {
		w.logger.Warn("agent worker: run sink failed", "work_item_id", item.ID, "error", err)
	}
}

func (w *Worker) recordFailure(item agent.WorkItem, cause error, retryable bool) {
	ctx, cancel := context.WithTimeout(context.Background(), workerWriteBackTimeout)
	defer cancel()
	if err := w.queue.Fail(ctx, item.ID, item.LeaseEpoch, cause, retryable); err != nil {
		if IsLeaseLost(err) {
			w.logger.Warn("agent worker: lease lost while failing", "work_item_id", item.ID)
			return
		}
		w.logger.Error("agent worker: fail write-back failed", "work_item_id", item.ID, "error", err)
		return
	}
	// The repository decides dead vs retryable open; the event must reflect the
	// persisted outcome, so read the row back after the write.
	eventType := "work_item.failed"
	if items, err := w.queue.ListByVersion(ctx, item.VersionID, 100); err == nil {
		for _, current := range items {
			if current.ID == item.ID && current.Status == agent.WorkItemDead {
				eventType = "work_item.dead"
			}
		}
	}
	attempt := item.Attempts
	if attempt < 1 {
		attempt = 1
	}
	errorText := ""
	if cause != nil {
		errorText = truncateRunes(cause.Error(), 500)
	}
	w.publish(item, eventType, map[string]any{
		"work_item_id": idText(item.ID),
		"type":         item.Type,
		"attempt":      attempt,
		"error":        errorText,
	})
}

func (w *Worker) track(id int64, entry *workerRun) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.running[id] = entry
}

func (w *Worker) untrack(id int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.running, id)
}

// heartbeatAll renews every lease held by this worker. A lease-lost heartbeat
// cancels the run and suppresses its write-back so a new owner is never
// overwritten.
func (w *Worker) heartbeatAll(ctx context.Context) {
	w.mu.Lock()
	entries := make(map[int64]*workerRun, len(w.running))
	for id, entry := range w.running {
		entries[id] = entry
	}
	w.mu.Unlock()

	for id, entry := range entries {
		if err := w.queue.Heartbeat(ctx, id, entry.epoch); err != nil {
			if IsLeaseLost(err) {
				entry.lost.Store(true)
				entry.cancel()
				w.logger.Warn("agent worker: lease_lost, abandoning run", "work_item_id", id)
				continue
			}
			w.logger.Warn("agent worker: heartbeat failed", "work_item_id", id, "error", err)
		}
	}
}

func (w *Worker) waitForInflight(inflight *sync.WaitGroup) {
	done := make(chan struct{})
	go func() {
		inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(w.shutdownWait):
		w.logger.Warn("agent worker: shutdown wait timed out", "timeout", w.shutdownWait)
	}
}

func (w *Worker) concurrency() int {
	if w.concurrencyOverride > 0 {
		return w.concurrencyOverride
	}
	limit := defaultWorkerConcurrency
	if w.settings != nil {
		if value, err := w.settings.Int(context.Background(), "agent.max_concurrent_runs"); err == nil && value > 0 {
			limit = int(value)
		}
	}
	if limit < 1 {
		limit = 1
	}
	return limit
}

func (w *Worker) publish(item agent.WorkItem, eventType string, payload map[string]any) {
	if w.events == nil {
		return
	}
	_ = w.events.Publish(context.Background(), eventingapp.Event{
		Channel: "version:" + idText(item.VersionID),
		Type:    eventType,
		Scope: map[string]any{
			"version_id":   idText(item.VersionID),
			"work_item_id": idText(item.ID),
		},
		Payload: payload,
	})
}
