package storageapp

import (
	"context"
	"log/slog"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

// GCServiceRunner is the subset of *GCService the periodic worker drives.
type GCServiceRunner interface {
	Run(ctx context.Context, mode storage.GCMode, actorID int64) (storage.GCRun, error)
}

// GCWorker periodically triggers a GC pass. It is leader-only: the caller
// (cmd/eztest wiring, Task 19) must run it behind the orchestration leader
// gate so a single replica schedules GC (spec §4.4).
type GCWorker struct {
	service          GCServiceRunner
	settings         GCSettingsReader
	logger           *slog.Logger
	intervalOverride time.Duration
}

// GCWorkerOption customizes a GCWorker.
type GCWorkerOption func(*GCWorker)

// WithGCWorkerInterval overrides the settings-driven interval (tests /演练).
func WithGCWorkerInterval(interval time.Duration) GCWorkerOption {
	return func(worker *GCWorker) {
		if interval > 0 {
			worker.intervalOverride = interval
		}
	}
}

// NewGCWorker wires the periodic GC worker.
func NewGCWorker(service GCServiceRunner, settings GCSettingsReader, logger *slog.Logger, options ...GCWorkerOption) *GCWorker {
	if logger == nil {
		logger = slog.Default()
	}
	worker := &GCWorker{service: service, settings: settings, logger: logger}
	for _, option := range options {
		option(worker)
	}
	return worker
}

// Run triggers a GC pass on every interval until ctx is cancelled. The mode
// comes from retention.gc_dry_run (fail-safe: dry_run) and the run is recorded
// with actor_kind=system.
func (w *GCWorker) Run(ctx context.Context) error {
	if ctx.Err() != nil {
		return nil
	}
	if w == nil || w.service == nil {
		return nil
	}
	w.logger.Info("gc worker started")
	for {
		timer := time.NewTimer(w.interval(ctx))
		select {
		case <-ctx.Done():
			timer.Stop()
			w.logger.Info("gc worker stopping")
			return nil
		case <-timer.C:
		}
		run, err := w.RunOnce(ctx)
		if err != nil {
			if ctx.Err() == nil {
				w.logger.Warn("gc worker: run failed", "error", err)
			}
			continue
		}
		w.logger.Info("gc worker: run finished",
			"gc_run_id", run.ID,
			"mode", string(run.Mode),
			"status", string(run.Status),
			"deleted_objects", run.DeletedObjects,
			"deleted_bytes", run.DeletedBytes,
		)
	}
}

// RunOnce executes exactly one pass using the configured mode.
func (w *GCWorker) RunOnce(ctx context.Context) (storage.GCRun, error) {
	if w == nil || w.service == nil {
		return storage.GCRun{}, nil
	}
	mode := storage.GCModeDryRun
	if w.settings != nil {
		dryRun, err := w.settings.Bool(ctx, GCSettingDryRun)
		if err != nil {
			w.logger.Warn("gc worker: read retention.gc_dry_run failed; defaulting to dry_run", "error", err)
		} else if !dryRun {
			mode = storage.GCModeApply
		}
	}
	return w.service.Run(ctx, mode, 0)
}

// interval resolves the next wait from retention.gc_interval_minutes.
func (w *GCWorker) interval(ctx context.Context) time.Duration {
	if w.intervalOverride > 0 {
		return w.intervalOverride
	}
	if w.settings != nil {
		minutes, err := w.settings.Int(ctx, GCSettingIntervalMinutes)
		if err == nil && minutes > 0 {
			interval := time.Duration(minutes) * time.Minute
			if interval < MinGCInterval {
				return MinGCInterval
			}
			return interval
		}
		if err != nil && ctx.Err() == nil {
			w.logger.Warn("gc worker: read retention.gc_interval_minutes failed; using the default", "error", err)
		}
	}
	return DefaultGCInterval
}
