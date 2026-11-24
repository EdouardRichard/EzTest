package main

import (
	"context"
	"log"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	orchestrationapp "github.com/EdouardRichard/EzTest/backend/internal/orchestration/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/load"
	settingsapp "github.com/EdouardRichard/EzTest/backend/internal/settings/application"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
)

// retentionLoadTracker adapts the two process-local counters to the orchestration
// LoadTracker port. It is read once per heartbeat.
type retentionLoadTracker struct {
	requests *load.Counter
	work     *load.Counter
}

func (t retentionLoadTracker) InflightRequests() int { return t.requests.Value() }
func (t retentionLoadTracker) InflightWork() int     { return t.work.Value() }

// retentionBatch reads retention.gc_batch_size (the GC batch size is reused
// for event/audit cleanup so the settings surface stays small).
func retentionBatch(ctx context.Context, settings *settingsapp.Service) int {
	if settings == nil {
		return 500
	}
	value, err := settings.Int(ctx, storageapp.GCSettingBatchSize)
	if err != nil || value <= 0 {
		return 500
	}
	return int(value)
}

// runRetentionWorker runs the event/audit cleanup on the leader at the GC
// interval (daily by default). It runs one pass immediately after the first
// interval; a failure is logged and retried on the next tick.
func runRetentionWorker(
	ctx context.Context,
	gate orchestrationapp.LeaderGate,
	service *auditapp.RetentionService,
	interval func(context.Context) time.Duration,
	auditDays func(context.Context) int,
	eventDays func(context.Context) int,
	logf func(string, ...any),
) {
	if service == nil {
		return
	}
	// One pass at startup makes the retention effect observable without waiting
	// for a full interval; subsequent passes follow the configured daily loop.
	// The startup pass retries until this replica wins the leader gate, otherwise
	// a replica that starts follower would not clean up for a whole interval.
	runOnce := func() bool {
		now := time.Now().UTC()
		auditCutoff := now.Add(-time.Duration(auditDays(ctx)) * 24 * time.Hour)
		eventCutoff := now.Add(-time.Duration(eventDays(ctx)) * 24 * time.Hour)
		return orchestrationRunGated(gate, func() {
			result, err := service.Run(ctx, auditCutoff, eventCutoff)
			if err != nil && ctx.Err() == nil && logf != nil {
				logf("retention worker: run failed: %v", err)
			}
			if logf != nil {
				logf("retention worker: events=%d audit=%d", result.EventsDeleted, result.AuditDeleted)
			}
		})
	}
	for !runOnce() {
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	for {
		wait := 24 * time.Hour
		if interval != nil {
			if resolved := interval(ctx); resolved > 0 {
				wait = resolved
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		runOnce()
	}
}

// retentionIntSetting resolves one positive integer setting with a fallback.
func retentionIntSetting(ctx context.Context, settings *settingsapp.Service, key string, fallback int) int {
	if settings == nil {
		return fallback
	}
	value, err := settings.Int(ctx, key)
	if err != nil || value < 0 {
		return fallback
	}
	return int(value)
}

// retentionInterval resolves retention.gc_interval_minutes as a duration.
func retentionInterval(ctx context.Context, settings *settingsapp.Service) time.Duration {
	minutes := retentionIntSetting(ctx, settings, storageapp.GCSettingIntervalMinutes, 1440)
	if minutes <= 0 {
		minutes = 1440
	}
	return time.Duration(minutes) * time.Minute
}

func retentionLogf(format string, args ...any) { log.Printf("eztest: "+format, args...) }
