package codeintelapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Worker defaults (spec §5.3): the dedicated codeintel worker claims only the
// `code` team, recovers expired leases every 3s and heartbeats every 10s.
const (
	defaultWorkerPollInterval     = 300 * time.Millisecond
	defaultWorkerHeartbeat        = 10 * time.Second
	defaultWorkerRecoveryInterval = 3 * time.Second
	defaultWorkerConcurrency      = 1
	workerShutdownWait            = 5 * time.Second
)

// Handler executes one claimed codeintel work item. A returned error uses the
// work item retry policy: retryable errors reopen the item with backoff, all
// others go dead.
type Handler interface {
	Handle(ctx context.Context, item WorkItem) error
}

// WorkerOption customizes the codeintel worker (tests and future tuning).
type WorkerOption func(*Worker)

// WithWorkerInstanceID sets the claimant identity recorded on leases.
func WithWorkerInstanceID(value string) WorkerOption {
	return func(w *Worker) {
		if value != "" {
			w.instanceID = value
		}
	}
}

// WithWorkerLogger sets the structured logger.
func WithWorkerLogger(logger *slog.Logger) WorkerOption {
	return func(w *Worker) {
		if logger != nil {
			w.logger = logger
		}
	}
}

// WithWorkerPollInterval overrides the claim polling interval.
func WithWorkerPollInterval(value time.Duration) WorkerOption {
	return func(w *Worker) {
		if value > 0 {
			w.pollInterval = value
		}
	}
}

// WithWorkerHeartbeatInterval overrides the lease heartbeat interval.
func WithWorkerHeartbeatInterval(value time.Duration) WorkerOption {
	return func(w *Worker) {
		if value > 0 {
			w.heartbeatInterval = value
		}
	}
}

// WithWorkerRecoveryInterval overrides the expired-lease recovery interval.
func WithWorkerRecoveryInterval(value time.Duration) WorkerOption {
	return func(w *Worker) {
		if value > 0 {
			w.recoveryInterval = value
		}
	}
}

// WithWorkerConcurrency overrides the concurrent work-item limit.
func WithWorkerConcurrency(value int) WorkerOption {
	return func(w *Worker) {
		if value > 0 {
			w.concurrency = value
		}
	}
}

// WithWorkerDrainGate injects the replica drain signal (P7 §4.5). While the
// gate returns true the worker finishes in-flight handlers but claims no new
// code-team items; nil means the replica never drains.
func WithWorkerDrainGate(gate func() bool) WorkerOption {
	return func(w *Worker) { w.drainGate = gate }
}

// Worker is the dedicated codeintel worker. It claims only the `code` team, so
// the product agent worker can never pick up index.repo/code.analyze tasks.
type Worker struct {
	queue      WorkQueue
	handlers   map[string]Handler
	logger     *slog.Logger
	instanceID string
	drainGate  func() bool

	pollInterval      time.Duration
	heartbeatInterval time.Duration
	recoveryInterval  time.Duration
	concurrency       int

	mu      sync.Mutex
	running map[int64]*workerRun
}

type workerRun struct {
	epoch  int64
	cancel context.CancelFunc
	lost   atomic.Bool
}

// NewWorker wires the codeintel worker. The handlers map is keyed by work item
// type ("index.repo", "code.analyze").
func NewWorker(queue WorkQueue, handlers map[string]Handler, options ...WorkerOption) *Worker {
	worker := &Worker{
		queue:             queue,
		handlers:          handlers,
		logger:            slog.Default(),
		instanceID:        "codeintel",
		pollInterval:      defaultWorkerPollInterval,
		heartbeatInterval: defaultWorkerHeartbeat,
		recoveryInterval:  defaultWorkerRecoveryInterval,
		concurrency:       defaultWorkerConcurrency,
		running:           map[int64]*workerRun{},
	}
	for _, option := range options {
		option(worker)
	}
	return worker
}

// Run processes work items until ctx is cancelled. Cancellation stops new
// claims and waits (bounded) for in-flight handlers.
func (w *Worker) Run(ctx context.Context) error {
	if ctx.Err() != nil {
		return nil
	}
	w.recoverExpired(ctx)
	w.logger.Info("codeintel worker started", "instance_id", w.instanceID, "concurrency", w.concurrency)

	poll := time.NewTicker(w.pollInterval)
	defer poll.Stop()
	heartbeat := time.NewTicker(w.heartbeatInterval)
	defer heartbeat.Stop()
	recovery := time.NewTicker(w.recoveryInterval)
	defer recovery.Stop()

	sem := make(chan struct{}, w.concurrency)
	var inflight sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			w.logger.Info("codeintel worker stopping", "instance_id", w.instanceID)
			w.waitForInflight(&inflight)
			return nil
		case <-poll.C:
			w.drain(ctx, sem, &inflight)
		case <-heartbeat.C:
			w.heartbeatAll(ctx)
		case <-recovery.C:
			w.recoverExpired(ctx)
		}
	}
}

// drain claims until the queue is empty or the concurrency semaphore is full.
func (w *Worker) drain(ctx context.Context, sem chan struct{}, inflight *sync.WaitGroup) {
	for {
		if w.draining() {
			return
		}
		select {
		case sem <- struct{}{}:
		default:
			return
		}
		item, ok, err := w.claim(ctx)
		if err != nil {
			<-sem
			if ctx.Err() == nil {
				w.logger.Warn("codeintel worker: claim failed", "error", err)
			}
			return
		}
		if !ok {
			<-sem
			return
		}
		inflight.Add(1)
		go func(item WorkItem) {
			defer inflight.Done()
			defer func() { <-sem }()
			w.process(ctx, item)
		}(item)
	}
}

// claim leases the next code-team work item.
func (w *Worker) claim(ctx context.Context) (WorkItem, bool, error) {
	return w.queue.ClaimForTeams(ctx, w.instanceID, []string{CodeTeam})
}

// process runs one claimed item, then completes it or records a retryable/dead
// failure. Handlers are selected strictly by work item type.
func (w *Worker) process(ctx context.Context, item WorkItem) {
	if err := w.queue.MarkRunning(ctx, item.ID, item.LeaseEpoch); err != nil {
		w.logger.Warn("codeintel worker: mark running failed", "work_item_id", item.ID, "error", err)
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	entry := &workerRun{epoch: item.LeaseEpoch, cancel: cancel}
	w.track(item.ID, entry)
	defer w.untrack(item.ID)
	defer cancel()

	handler, ok := w.handlers[item.Type]
	if !ok {
		w.fail(ctx, item, platform.NewError(http.StatusUnprocessableEntity, "unknown_work_type", "no codeintel handler is registered for this work item type"), false)
		return
	}
	handleErr := handler.Handle(runCtx, item)
	if entry.lost.Load() {
		return
	}
	if handleErr == nil {
		if err := w.queue.Complete(ctx, item.ID, item.LeaseEpoch); err != nil {
			w.logger.Warn("codeintel worker: complete failed", "work_item_id", item.ID, "error", err)
		}
		return
	}
	retryable := retryableError(handleErr)
	w.fail(ctx, item, handleErr, retryable)
}

// fail records a work item failure with the P1 backoff policy.
func (w *Worker) fail(ctx context.Context, item WorkItem, cause error, retryable bool) {
	message := safeErrorMessage(cause)
	if err := w.queue.Fail(ctx, item.ID, item.LeaseEpoch, message, retryable, backoffForAttempts(item.Attempts)); err != nil {
		w.logger.Warn("codeintel worker: fail write-back failed", "work_item_id", item.ID, "error", err)
		return
	}
	w.logger.Warn("codeintel worker: work item failed", "work_item_id", item.ID, "type", item.Type, "retryable", retryable)
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

// heartbeatAll renews every lease currently held by this worker.
func (w *Worker) heartbeatAll(ctx context.Context) {
	w.mu.Lock()
	entries := make(map[int64]*workerRun, len(w.running))
	for id, entry := range w.running {
		entries[id] = entry
	}
	w.mu.Unlock()

	for id, entry := range entries {
		if err := w.queue.Renew(ctx, id, entry.epoch, w.heartbeatInterval); err != nil {
			w.logger.Warn("codeintel worker: heartbeat failed", "work_item_id", id, "error", err)
		}
	}
}

// recoverExpired reclaims expired leases when the queue adapter exposes the
// optional recovery surface (the P1 work queue does).
func (w *Worker) recoverExpired(ctx context.Context) {
	recoverer, ok := w.queue.(interface {
		RecoverExpired(ctx context.Context) (int64, error)
	})
	if !ok {
		return
	}
	recovered, err := recoverer.RecoverExpired(ctx)
	if err != nil {
		if ctx.Err() == nil {
			w.logger.Warn("codeintel worker: recover expired leases failed", "error", err)
		}
		return
	}
	if recovered > 0 {
		w.logger.Info("codeintel worker: recovered expired leases", "count", recovered)
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
	case <-time.After(workerShutdownWait):
		w.logger.Warn("codeintel worker: shutdown wait timed out")
	}
}

// retryableError classifies handler errors. Context deadlines and transient
// platform errors are retried; everything else is treated as a permanent
// failure so a malformed payload cannot loop forever.
func retryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		return apiErr.Retryable
	}
	return false
}

// safeErrorMessage returns a bounded, path-free message for the work item row.
func safeErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) && strings.TrimSpace(apiErr.Message) != "" {
		return truncateString(apiErr.Message, 500)
	}
	return "codeintel work item failed"
}

// backoffForAttempts implements min(2^attempts, 30) seconds.
func backoffForAttempts(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts >= 5 {
		return 30 * time.Second
	}
	return time.Duration(1<<uint(attempts)) * time.Second
}

// IndexWorkHandler adapts IndexService.Execute to the index.repo work type.
type IndexWorkHandler struct {
	Service *IndexService
}

// Handle decodes {index_run_id} and executes that T0 run.
func (h IndexWorkHandler) Handle(ctx context.Context, item WorkItem) error {
	runID, err := decodeWorkItemID(item.Payload, "index_run_id")
	if err != nil {
		return err
	}
	if h.Service == nil {
		return platform.Unavailable("codeintel index service is not configured")
	}
	return h.Service.Execute(ctx, runID)
}

// decodeWorkItemID parses an ID payload field that may be a JSON decimal string
// or a number (P0-P4 payloads used both forms).
func decodeWorkItemID(payload json.RawMessage, key string) (int64, error) {
	if len(payload) == 0 {
		return 0, platform.Invalid(key, "work item payload is missing")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return 0, platform.Invalid(key, "work item payload must be a JSON object")
	}
	raw, ok := fields[key]
	if !ok {
		return 0, platform.Invalid(key, "work item payload is missing the ID")
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		value, parseErr := strconv.ParseInt(text, 10, 64)
		if parseErr != nil || value <= 0 {
			return 0, platform.Invalid(key, "must be a positive decimal ID")
		}
		return value, nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return 0, platform.Invalid(key, "must be a decimal ID")
	}
	number, ok = value.(json.Number)
	if !ok {
		return 0, platform.Invalid(key, "must be a decimal ID")
	}
	parsed, parseErr := number.Int64()
	if parseErr != nil || parsed <= 0 {
		return 0, platform.Invalid(key, "must be a positive decimal ID")
	}
	return parsed, nil
}
