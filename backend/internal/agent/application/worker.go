package agentapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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
	// DefaultJobProgressWindow is the 0.2.1 per-job coalescing window for
	// job.progress (contracts/events.md §18.2 #4 requires 250–500ms).
	DefaultJobProgressWindow = 300 * time.Millisecond
)

// agentProductTeams is the set of teams claimed by the agent worker. The
// dedicated P5 codeintel worker claims TeamCode; agent profiles never run
// index.repo/code.analyze tasks.
var agentProductTeams = []agent.Team{
	agent.TeamClarification,
	agent.TeamDesign,
	agent.TeamExecution,
	agent.TeamDelivery,
	agent.TeamSteward,
}

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

// WithWorkerJobProgressWindow overrides the job.progress coalescing window. The
// default is DefaultJobProgressWindow; a non-positive value keeps it.
func WithWorkerJobProgressWindow(window time.Duration) WorkerOption {
	return func(w *Worker) {
		if window > 0 {
			w.jobProgressWindow = window
		}
	}
}

// WithWorkerDrainGate injects the replica drain signal. While the gate returns
// true the worker finishes in-flight runs but claims no new work; nil means the
// replica never drains. cmd/eztest wires the orchestration service's
// IsDraining here (P7 §4.5).
func WithWorkerDrainGate(gate func() bool) WorkerOption {
	return func(w *Worker) { w.drainGate = gate }
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
	drainGate  func() bool
	// jobEvents coalesces the 0.2.1 job.progress/finished stream for every run
	// this worker owns. It is nil when no publisher is wired.
	jobEvents *JobProgressEmitter

	pollInterval        time.Duration
	heartbeatInterval   time.Duration
	recoveryInterval    time.Duration
	concurrencyOverride int
	shutdownWait        time.Duration
	jobProgressWindow   time.Duration

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
		jobProgressWindow: DefaultJobProgressWindow,
		running:           map[int64]*workerRun{},
	}
	for _, option := range options {
		option(worker)
	}
	if worker.events != nil {
		worker.jobEvents = NewJobProgressEmitter(worker.events, worker.logger, worker.jobProgressWindow)
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
		if w.draining() {
			return
		}
		select {
		case sem <- struct{}{}:
		default:
			return
		}
		item, ok, err := w.queue.ClaimForTeams(ctx, w.instanceID, agentProductTeams)
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
	// The runner reports its completed turns through the same per-job
	// coalescer, so start/progress/finished cannot race each other.
	runCtx = withJobEvents(runCtx, w.jobEvents)
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
		w.recordFailure(item, err, true, RunResult{})
		return
	}
	w.publish(item, "work_item.claimed", map[string]any{
		"work_item_id": idText(item.ID),
		"type":         item.Type,
		"claimed_by":   w.instanceID,
		"lease_epoch":  item.LeaseEpoch,
	})
	// Job start (contracts/events.md §18.1): the item is now running in this
	// worker. current/total stay 0 ("unknown") until the run reports a real
	// counter; no percentage is ever fabricated.
	w.jobProgress(item, eventingapp.JobProgress{
		Status: eventingapp.JobStatusRunning,
		Total:  0,
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
			// The DB is the source of truth: without a persisted terminal state
			// there is no terminal event to publish.
			w.logger.Error("agent worker: complete failed", "work_item_id", item.ID, "error", err)
			return
		}
		w.jobFinished(item, result, eventingapp.JobStatusDone, nil)
		return
	}
	retryable := IsRetryable(runErr)
	if runErr == nil {
		retryable = true
		runErr = fmt.Errorf("agent run finished with status %s", result.Status)
	}
	w.recordFailure(item, runErr, retryable, result)
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

func (w *Worker) recordFailure(item agent.WorkItem, cause error, retryable bool, result RunResult) {
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
	terminal := false
	if items, err := w.queue.ListByVersion(ctx, item.VersionID, 100); err == nil {
		for _, current := range items {
			if current.ID == item.ID && current.Status == agent.WorkItemDead {
				eventType = "work_item.dead"
				terminal = true
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
	// The job projection follows the persisted work-item outcome: a dead item
	// (attempts exhausted) is terminal failed, while a retryable failed item
	// stays on the version stream so a later attempt can reopen it.
	if terminal {
		w.jobFinished(item, result, eventingapp.JobStatusFailed, cause)
		return
	}
	w.jobProgress(item, eventingapp.JobProgress{
		Status:  eventingapp.JobStatusFailed,
		Total:   0,
		Message: errorText,
	})
}

// draining reports the injected P7 drain signal (false when unwired).
func (w *Worker) draining() bool {
	return w.drainGate != nil && w.drainGate()
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
		// The agent worker claims with the queue default lease, so it renews
		// with the same default (0 selects it).
		if err := w.queue.Heartbeat(ctx, id, entry.epoch, 0); err != nil {
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

// --- 0.2.1 job events (Task 47) ---------------------------------------------

// JobProgressEmitter coalesces job.progress events per work item and publishes
// exactly one job.finished per job. It is safe for concurrent use and never
// returns errors: event publication is best effort and the DB stays the source
// of truth (contracts/events.md §18.2 #7).
//
// The first update for a job anchors a coalescing window (250-500ms); every
// update inside the window replaces the pending snapshot, and the window close
// publishes the latest snapshot once. current never moves backwards for the
// same job and a known total is never downgraded to "unknown" (0), so a late
// start or status event cannot make the card regress. Progress after the
// terminal event is dropped.
type JobProgressEmitter struct {
	publisher eventingapp.Publisher
	logger    *slog.Logger
	window    time.Duration

	mu   sync.Mutex
	jobs map[int64]*jobProgressState
}

// jobProgressState is the per-job coalescing buffer. versionID is fixed by the
// first event so a buggy caller cannot retarget the job to another version.
type jobProgressState struct {
	versionID int64
	latest    eventingapp.JobProgress
	pending   bool
	timer     *time.Timer
	current   int
	total     int
	finished  bool
}

// NewJobProgressEmitter builds the per-job coalescer. A nil logger falls back
// to slog.Default and a non-positive window to DefaultJobProgressWindow.
func NewJobProgressEmitter(publisher eventingapp.Publisher, logger *slog.Logger, window time.Duration) *JobProgressEmitter {
	if window <= 0 {
		window = DefaultJobProgressWindow
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &JobProgressEmitter{
		publisher: publisher,
		logger:    logger,
		window:    window,
		jobs:      map[int64]*jobProgressState{},
	}
}

// Progress records one job.progress update. It validates the contract shape
// before buffering and logs (never returns) a validation or publish failure.
func (e *JobProgressEmitter) Progress(versionID int64, progress eventingapp.JobProgress) {
	if e == nil || e.publisher == nil {
		return
	}
	if versionID <= 0 {
		e.logger.Warn("job progress: invalid version", "version_id", versionID, "job_id", progress.JobID)
		return
	}
	if err := progress.Validate(); err != nil {
		e.logger.Warn("job progress: contract validation failed",
			"version_id", versionID, "job_id", progress.JobID, "error", err)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	state, ok := e.stateLocked(progress.JobID, versionID)
	if !ok || state.finished {
		return
	}
	// Monotonic snapshot: a status-only update (current 0) or a retried run
	// (turn counter reset) must not pull the visible progress backwards, and a
	// total learned once stays known.
	if progress.Current < state.current {
		progress.Current = state.current
	}
	state.current = progress.Current
	switch {
	case progress.Total > 0:
		state.total = progress.Total
	case state.total > 0:
		progress.Total = state.total
	}
	state.latest = progress
	if state.pending {
		return
	}
	state.pending = true
	jobID := progress.JobID
	state.timer = time.AfterFunc(e.window, func() { e.flush(jobID) })
}

// Finished publishes the one-time terminal job.finished. It first flushes the
// pending progress snapshot (so a job that finishes inside one window still
// reports its start/last state), then marks the job terminal; later progress
// and repeated terminal calls are dropped.
func (e *JobProgressEmitter) Finished(versionID int64, finished eventingapp.JobFinished) {
	if e == nil || e.publisher == nil {
		return
	}
	if versionID <= 0 {
		e.logger.Warn("job finished: invalid version", "version_id", versionID, "job_id", finished.JobID)
		return
	}
	if err := finished.Validate(); err != nil {
		e.logger.Warn("job finished: contract validation failed",
			"version_id", versionID, "job_id", finished.JobID, "error", err)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	state, ok := e.stateLocked(finished.JobID, versionID)
	if !ok || state.finished {
		return
	}
	if state.pending {
		state.pending = false
		if state.timer != nil {
			state.timer.Stop()
			state.timer = nil
		}
		e.publishProgressLocked(state)
	}
	if err := eventingapp.PublishJobFinished(context.Background(), e.publisher, versionID, finished); err != nil {
		e.logger.Warn("job finished: publish failed",
			"version_id", versionID, "job_id", finished.JobID, "error", err)
	}
	state.finished = true
}

// stateLocked returns the job state, creating it for the first event. A
// different version for an already-seen job is a caller bug: it is logged and
// dropped rather than leaking the job onto a foreign version stream.
func (e *JobProgressEmitter) stateLocked(jobID, versionID int64) (*jobProgressState, bool) {
	state := e.jobs[jobID]
	if state == nil {
		state = &jobProgressState{versionID: versionID}
		e.jobs[jobID] = state
		return state, true
	}
	if state.versionID != versionID {
		e.logger.Warn("job events: version mismatch ignored",
			"job_id", jobID, "version_id", versionID, "job_version_id", state.versionID)
		return nil, false
	}
	return state, true
}

// flush publishes the pending snapshot once and closes the window. Publication
// runs under the emitter lock so two windows of the same job can never publish
// out of order.
func (e *JobProgressEmitter) flush(jobID int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	state := e.jobs[jobID]
	if state == nil || !state.pending {
		return
	}
	state.pending = false
	state.timer = nil
	if state.finished {
		return
	}
	e.publishProgressLocked(state)
}

func (e *JobProgressEmitter) publishProgressLocked(state *jobProgressState) {
	progress := state.latest
	progress.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := eventingapp.PublishJobProgress(context.Background(), e.publisher, state.versionID, progress); err != nil {
		e.logger.Warn("job progress: publish failed",
			"version_id", state.versionID, "job_id", progress.JobID, "error", err)
	}
}

// jobProgress reports one coalesced progress update for a work item.
func (w *Worker) jobProgress(item agent.WorkItem, progress eventingapp.JobProgress) {
	if w == nil || w.jobEvents == nil {
		return
	}
	progress.JobID = item.ID
	progress.Kind = jobKind(item)
	w.jobEvents.Progress(item.VersionID, progress)
}

// jobFinished reports the one-time terminal event for a work item.
func (w *Worker) jobFinished(item agent.WorkItem, result RunResult, status string, cause error) {
	if w == nil || w.jobEvents == nil {
		return
	}
	finished := eventingapp.JobFinished{
		JobID:     item.ID,
		Kind:      jobKind(item),
		Status:    status,
		ResultRef: jobResultRef(result),
	}
	if cause != nil {
		finished.Error = truncateRunes(cause.Error(), 500)
		finished.Attempt = item.Attempts
		if finished.Attempt < 1 {
			finished.Attempt = 1
		}
	}
	w.jobEvents.Finished(item.VersionID, finished)
}

// jobKind is the 0.2.1 job kind of a work item. Legacy P1-P8 rows carry no Kind
// value; their frozen type is the best available label.
func jobKind(item agent.WorkItem) string {
	if kind := strings.TrimSpace(item.Kind); kind != "" {
		return kind
	}
	return strings.TrimSpace(item.Type)
}

// jobResultRef points the terminal event at the run row that holds the job's
// outputs. It is empty when no run row was created (contract allows it).
func jobResultRef(result RunResult) string {
	if result.AgentRunID <= 0 {
		return ""
	}
	return "agent_run:" + idText(result.AgentRunID)
}

// jobEventsContextKey carries the worker's per-job emitter on a run context.
// The runner reports its turn progress through the same coalescer that the
// worker uses for start/finished, so one job never has two racing producers.
type jobEventsContextKey struct{}

func withJobEvents(ctx context.Context, emitter *JobProgressEmitter) context.Context {
	if emitter == nil || ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, jobEventsContextKey{}, emitter)
}

func jobEventsFromContext(ctx context.Context) *JobProgressEmitter {
	if ctx == nil {
		return nil
	}
	emitter, _ := ctx.Value(jobEventsContextKey{}).(*JobProgressEmitter)
	return emitter
}
