package eventingapp

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

// systemEventCounts is the process-local per-type counter of system-channel
// events. It is intentionally package-scoped so a health check (or a test) can
// observe the generic consumer without holding a reference to every observer
// registered on the event service.
var systemEventCounts sync.Map // map[string]*atomic.Uint64

// SystemEventCount returns how many system-channel events of eventType this
// process has observed since start. Unknown types return zero.
func SystemEventCount(eventType string) uint64 {
	value, ok := systemEventCounts.Load(eventType)
	if !ok {
		return 0
	}
	return value.(*atomic.Uint64).Load()
}

// NewSystemEventConsumer returns the generic system-channel observer. It is
// the W1-5 consumer for events that have no business UI: resource catalogs,
// instances, LLM configuration, settings, stored content, GC and knowledge.
//
// The observer only reacts to event.Channel == "system"; every other channel
// returns immediately with no side effect. Counters are atomic and the log is
// best effort, so the observer never blocks and never fails a publish (the
// EventObserver contract ignores returned errors).
func NewSystemEventConsumer(logger *slog.Logger) EventObserver {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, event eventing.Event) error {
		if event.Channel != "system" {
			return nil
		}
		if ctx == nil {
			ctx = context.Background()
		}
		counter, _ := systemEventCounts.LoadOrStore(event.Type, new(atomic.Uint64))
		count := counter.(*atomic.Uint64).Add(1)
		logger.InfoContext(ctx, "system event observed",
			slog.String("channel", event.Channel),
			slog.String("type", event.Type),
			slog.Int64("seq", event.Seq),
			slog.Uint64("count", count),
		)
		return nil
	}
}
