package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

const (
	// JobKindIndex and JobKindAnalysis are the two async run kinds of design
	// spec §6.8.
	JobKindIndex    = "index"
	JobKindAnalysis = "analysis"

	// defaultWorkerQueueSize, defaultWorkerConcurrency and defaultWorkerTimeout
	// mirror the P4 defaults (queue 16, concurrency 1, 180s analysis timeout).
	defaultWorkerQueueSize   = 16
	defaultWorkerConcurrency = 1
	defaultWorkerTimeout     = 180 * time.Second

	// recoveryWindow is the startup recovery threshold: queued/running runs
	// older than ten minutes are failed(interrupted) (design spec §6.8).
	recoveryWindow = 10 * time.Minute
	// recoveryTimeout bounds the startup recovery queries.
	recoveryTimeout = 10 * time.Second
)

var (
	// ErrQueueFull is returned by Enqueue when the bounded worker queue has no
	// free slot (design spec §6.8, HTTP reason analysis_queue_full).
	ErrQueueFull = errors.New("codeintel: analysis queue is full")
	// ErrWorkerStopped is returned by Enqueue after Stop: a stopping worker
	// accepts no new work.
	ErrWorkerStopped = errors.New("codeintel: worker is stopped")
)

// Job is one async run dispatch. Kind is JobKindIndex or JobKindAnalysis and ID
// is the run/analysis snowflake id.
type Job struct {
	Kind string
	ID   int64
}

// Enqueuer is the narrow queue port the services depend on, keeping the
// services↔worker dependency acyclic (design spec §3.6).
type Enqueuer interface {
	Enqueue(job Job) error
}

// Runner executes one async run. The composition root injects an adapter over
// the index and analysis services via Worker.SetRunner.
type Runner interface {
	RunIndex(ctx context.Context, runID int64) error
	RunAnalysis(ctx context.Context, analysisID int64) error
}

// Recoverer marks stale async runs failed(interrupted) at worker startup
// (design spec §6.8). The composition root supplies one; keeping it a port
// means the worker never depends on concrete repositories.
type Recoverer interface {
	RecoverStale(ctx context.Context, before time.Time) error
}

// RecoverStaleFunc adapts a function to Recoverer.
type RecoverStaleFunc func(ctx context.Context, before time.Time) error

// RecoverStale implements Recoverer.
func (f RecoverStaleFunc) RecoverStale(ctx context.Context, before time.Time) error {
	if f == nil {
		return nil
	}
	return f(ctx, before)
}

// StaleRunRecoverer composes the index-run and analysis repositories into the
// worker's startup recovery hook. Either repository may be nil; a nil one is
// skipped.
type StaleRunRecoverer struct {
	IndexRuns domain.IndexRunRepository
	Analyses  domain.AnalysisRepository
}

// RecoverStale recovers both run kinds. It stops at the first error so a broken
// database is reported instead of silently skipping one kind.
func (r StaleRunRecoverer) RecoverStale(ctx context.Context, before time.Time) error {
	if r.IndexRuns != nil {
		if _, err := r.IndexRuns.RecoverStale(ctx, before); err != nil {
			return fmt.Errorf("codeintel: recover stale index runs: %w", err)
		}
	}
	if r.Analyses != nil {
		if _, err := r.Analyses.RecoverStale(ctx, before); err != nil {
			return fmt.Errorf("codeintel: recover stale analyses: %w", err)
		}
	}
	return nil
}

// WorkerConfig configures the in-process async worker (design spec §3.6). Zero
// values fall back to the P4 defaults.
type WorkerConfig struct {
	QueueSize   int
	Concurrency int
	Timeout     time.Duration
	Logger      *slog.Logger
}

// Worker is the single-instance bounded async worker of design spec §6.8. It
// owns a bounded job channel plus a slot semaphore sized QueueSize, so a slot
// stays reserved for the whole run and a full queue rejects new work instead of
// blocking. Start recovers stale runs before serving jobs; Stop stops accepting
// work, drains the accepted jobs and waits for them bounded by the caller's
// context.
type Worker struct {
	cfg    WorkerConfig
	jobs   chan Job
	slots  chan struct{}
	logger *slog.Logger

	mu        sync.Mutex
	runner    Runner
	recoverer Recoverer
	stopped   bool

	startOnce sync.Once
	stopOnce  sync.Once
	wg        sync.WaitGroup
}

// NewWorker builds a worker with a bounded queue. QueueSize and Concurrency
// default to 16 and 1; Timeout defaults to 180s and Logger to slog.Default.
func NewWorker(cfg WorkerConfig) *Worker {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = defaultWorkerQueueSize
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = defaultWorkerConcurrency
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultWorkerTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Worker{
		cfg:    cfg,
		jobs:   make(chan Job, cfg.QueueSize),
		slots:  make(chan struct{}, cfg.QueueSize),
		logger: cfg.Logger,
	}
}

// SetRunner registers the run executor. It must be called before Start (the
// composition root builds all services first to break the services↔worker
// cycle).
func (w *Worker) SetRunner(r Runner) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.runner = r
}

// SetRecoverer registers the startup recovery hook. It must be called before
// Start. When no recoverer is set, startup recovery is skipped.
func (w *Worker) SetRecoverer(r Recoverer) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.recoverer = r
}

// Enqueue accepts one job if a slot is free. It never blocks: a full worker
// returns ErrQueueFull and a stopped worker returns ErrWorkerStopped.
func (w *Worker) Enqueue(job Job) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return ErrWorkerStopped
	}
	select {
	case w.slots <- struct{}{}:
	default:
		return ErrQueueFull
	}
	// The slot count never exceeds the jobs channel capacity, so this send
	// cannot block while the mutex is held.
	w.jobs <- job
	return nil
}

// Start performs the one-time startup recovery and launches Concurrency worker
// goroutines. It never accepts a second start.
func (w *Worker) Start() {
	w.startOnce.Do(func() {
		w.mu.Lock()
		stopped := w.stopped
		w.mu.Unlock()
		if stopped {
			return
		}
		w.recoverStale()
		for index := 0; index < w.cfg.Concurrency; index++ {
			w.wg.Add(1)
			go w.loop()
		}
	})
}

// Stop stops accepting jobs, closes the queue and waits for the accepted jobs to
// finish or for ctx to expire. Accepted jobs keep their own per-job timeout.
func (w *Worker) Stop(ctx context.Context) {
	w.stopOnce.Do(func() {
		w.mu.Lock()
		w.stopped = true
		close(w.jobs)
		w.mu.Unlock()
	})
	if ctx == nil {
		ctx = context.Background()
	}
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// recoverStale fails the leftover queued/running runs of a previous process
// (design spec §6.8). Recovery problems are logged, never fatal: the worker
// still serves new jobs.
func (w *Worker) recoverStale() {
	w.mu.Lock()
	recoverer := w.recoverer
	runner := w.runner
	w.mu.Unlock()
	if recoverer == nil {
		// Also accept a Runner that implements Recoverer, so a composition root
		// that wraps the services into one type need not call SetRecoverer.
		if candidate, ok := runner.(Recoverer); ok {
			recoverer = candidate
		}
	}
	if recoverer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), recoveryTimeout)
	defer cancel()
	before := time.Now().Add(-recoveryWindow)
	if err := recoverer.RecoverStale(ctx, before); err != nil {
		w.logger.Error("codeintel worker startup recovery failed", "error", err)
		return
	}
	w.logger.Info("codeintel worker recovered stale runs", "before", before)
}

// loop consumes jobs until the queue is closed by Stop.
func (w *Worker) loop() {
	defer w.wg.Done()
	for job := range w.jobs {
		w.execute(job)
	}
}

// execute runs one job and always releases its queue slot, even on panic.
func (w *Worker) execute(job Job) {
	defer func() { <-w.slots }()
	defer func() {
		if recovered := recover(); recovered != nil {
			w.logger.Error("codeintel worker recovered a panicking job",
				"kind", job.Kind, "id", job.ID, "panic", fmt.Sprint(recovered))
		}
	}()
	w.runJob(job)
}

// runJob dispatches one job with the configured per-job timeout. Failures are
// logged and never stop the worker; the run state is persisted by the services.
func (w *Worker) runJob(job Job) {
	w.mu.Lock()
	runner := w.runner
	w.mu.Unlock()
	if runner == nil {
		w.logger.Error("codeintel worker has no runner configured", "kind", job.Kind, "id", job.ID)
		return
	}

	ctx := context.Background()
	var cancel context.CancelFunc
	if w.cfg.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, w.cfg.Timeout)
		defer cancel()
	}

	startedAt := time.Now()
	var err error
	switch job.Kind {
	case JobKindIndex:
		err = runner.RunIndex(ctx, job.ID)
	case JobKindAnalysis:
		err = runner.RunAnalysis(ctx, job.ID)
	default:
		err = fmt.Errorf("codeintel: unknown job kind %q", job.Kind)
	}
	if err != nil {
		w.logger.Error("codeintel worker job failed",
			"kind", job.Kind, "id", job.ID,
			"durationMs", time.Since(startedAt).Milliseconds(), "error", err)
		return
	}
	w.logger.Debug("codeintel worker job finished",
		"kind", job.Kind, "id", job.ID,
		"durationMs", time.Since(startedAt).Milliseconds())
}
