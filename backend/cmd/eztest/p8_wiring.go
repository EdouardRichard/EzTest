package main

import (
	"context"
	"log"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	orchestrationapp "github.com/EdouardRichard/EzTest/backend/internal/orchestration/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/load"
	settingsapp "github.com/EdouardRichard/EzTest/backend/internal/settings/application"
)

// p8LoadTracker adapts the two process-local counters to the orchestration
// LoadTracker port. It is read once per heartbeat.
type p8LoadTracker struct {
	requests *load.Counter
	work     *load.Counter
}

func (t p8LoadTracker) InflightRequests() int { return t.requests.Value() }
func (t p8LoadTracker) InflightWork() int     { return t.work.Value() }

// p8RetentionBatch reads retention.gc_batch_size (the GC batch size is reused
// for event/audit cleanup so the settings surface stays small).
func p8RetentionBatch(ctx context.Context, settings *settingsapp.Service) int {
	if settings == nil {
		return 500
	}
	value, err := settings.Int(ctx, "retention.gc_batch_size")
	if err != nil || value <= 0 {
		return 500
	}
	return int(value)
}

// runP8RetentionWorker runs the event/audit cleanup on the leader at the GC
// interval (daily by default). It runs one pass immediately after the first
// interval; a failure is logged and retried on the next tick.
func runP8RetentionWorker(
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
		now := time.Now().UTC()
		auditCutoff := now.Add(-time.Duration(auditDays(ctx)) * 24 * time.Hour)
		eventCutoff := now.Add(-time.Duration(eventDays(ctx)) * 24 * time.Hour)
		p7RunGated(gate, func() {
			result, err := service.Run(ctx, auditCutoff, eventCutoff)
			if err != nil && ctx.Err() == nil && logf != nil {
				logf("retention worker: run failed: %v", err)
			}
			if logf != nil {
				logf("retention worker: events=%d audit=%d", result.EventsDeleted, result.AuditDeleted)
			}
		})
	}
}

// p8IntSetting resolves one positive integer setting with a fallback.
func p8IntSetting(ctx context.Context, settings *settingsapp.Service, key string, fallback int) int {
	if settings == nil {
		return fallback
	}
	value, err := settings.Int(ctx, key)
	if err != nil || value < 0 {
		return fallback
	}
	return int(value)
}

// p8RetentionInterval resolves retention.gc_interval_minutes as a duration.
func p8RetentionInterval(ctx context.Context, settings *settingsapp.Service) time.Duration {
	minutes := p8IntSetting(ctx, settings, "retention.gc_interval_minutes", 1440)
	if minutes <= 0 {
		minutes = 1440
	}
	return time.Duration(minutes) * time.Minute
}

func p8Logf(format string, args ...any) { log.Printf("eztest: "+format, args...) }
