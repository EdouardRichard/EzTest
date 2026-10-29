package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// DefaultMaintenanceInterval is the daily interval of the evidence maintenance
// entry (design spec §10.2: "每日一次").
const DefaultMaintenanceInterval = 24 * time.Hour

// MaintenanceGC is the subset of the evidence GC used by the scheduled
// maintenance. *GCService satisfies it.
type MaintenanceGC interface {
	Run(ctx context.Context, req GCRequest) (GCReport, error)
}

// MaintenanceDeps wires the daily maintenance entry.
type MaintenanceDeps struct {
	// GC is the evidence GC use case. It is required.
	GC MaintenanceGC
	// Leader is the multi-instance leader lock (design spec §10.5). A nil lock
	// keeps single-instance behavior. LeaderKey is the loop's advisory key,
	// normally MaintenanceLeaderKey(EZTEST_LEADER_LOCK_KEY).
	Leader    TickLock
	LeaderKey int64
	// Purge enables the non-dry-run GC. The zero value keeps the scheduled run
	// read-only (spec §10.2: default dry-run only); the composition root sets it
	// from EZTEST_GC_ENABLED so a production opt-in also enables the purge.
	Purge bool
	// Interval overrides DefaultMaintenanceInterval.
	Interval time.Duration
	Logger   *slog.Logger
}

// DailyMaintenance is the leader-only, daily evidence maintenance entry. It
// acquires the leader lock for one pass, runs the GC (dry-run by default) and
// releases the lock; a non-leader instance skips the pass and logs at debug
// level in the lock implementation.
type DailyMaintenance struct {
	deps     MaintenanceDeps
	logger   *slog.Logger
	interval time.Duration

	mu      sync.Mutex
	cancel  context.CancelFunc
	running bool
	wg      sync.WaitGroup
}

// NewDailyMaintenance wires the maintenance loop. A nil logger falls back to
// the default logger and a non-positive interval falls back to the daily
// default.
func NewDailyMaintenance(deps MaintenanceDeps) *DailyMaintenance {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	interval := deps.Interval
	if interval <= 0 {
		interval = DefaultMaintenanceInterval
	}
	return &DailyMaintenance{deps: deps, logger: logger, interval: interval}
}

// Start runs one pass immediately and then every interval until ctx is
// canceled or Stop is called. Starting twice is a no-op.
func (m *DailyMaintenance) Start(ctx context.Context) {
	if m == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	loopCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.running = true
	m.wg.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.wg.Done()
		defer func() {
			m.mu.Lock()
			m.running = false
			m.mu.Unlock()
		}()
		// Run a first pass immediately: a restarted single instance records its
		// dry-run without waiting a full day.
		m.tick(loopCtx)
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
			}
			m.tick(loopCtx)
		}
	}()
}

// Stop terminates the loop and waits for the pass in flight. It is safe to call
// without a running loop and more than once.
func (m *DailyMaintenance) Stop() {
	if m == nil {
		return
	}
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.running = false
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
}

// tick runs one pass and logs its failure; a shutdown cancellation is not a
// failure.
func (m *DailyMaintenance) tick(ctx context.Context) {
	if err := m.RunOnce(ctx); err != nil && ctx.Err() == nil {
		m.logger.WarnContext(ctx, "execution maintenance tick failed", "error", err)
	}
}

// RunOnce performs one leader-gated maintenance pass. A non-leader returns
// (nil) without touching the GC. The GC report is logged with counts only: no
// evidence path, sha256 or content reaches the log.
func (m *DailyMaintenance) RunOnce(ctx context.Context) error {
	if m == nil || m.deps.GC == nil {
		return errors.New("execution: maintenance GC is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	release, ok, err := acquireTickLock(ctx, m.deps.Leader, m.deps.LeaderKey)
	if err != nil {
		return fmt.Errorf("execution: acquire maintenance leader lock: %w", err)
	}
	if !ok {
		return nil
	}
	defer release()

	report, err := m.deps.GC.Run(ctx, GCRequest{DryRun: !m.deps.Purge})
	if err != nil {
		return fmt.Errorf("execution: daily maintenance GC: %w", err)
	}
	m.logger.InfoContext(ctx, "execution maintenance GC completed",
		"dry_run", report.DryRun,
		"candidates", report.Candidates,
		"protected", report.Protected,
		"bytes", report.Bytes,
		"deleted", report.Deleted,
		"failed", report.Failed,
		"skipped", report.Skipped,
		"retention_days", report.RetentionDays,
		"batch_keep", report.BatchKeep,
		"candidate_limit", report.CandidateLimit,
	)
	return nil
}
