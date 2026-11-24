package agentapp

import (
	"context"
	"sync"
)

// TaskRef is the complete input a P4 work-item task-context provider receives:
// the version, the work-item type and the raw work-item payload (IDs stay
// decimal strings). Each domain parses only the keys it owns.
//
// Ruling: P4 task contexts are payload-derived only. A provider returns JSON
// derived from this TaskRef and nothing else; it gets no service deps and must
// not capture application ports from init-time globals. Data beyond the
// work-item reference is loaded by the domain's scoped tools during the run, so
// tool scope stays the single access path for domain data.
type TaskRef struct {
	VersionID int64
	Type      string
	Payload   map[string]any
}

// P4 task-context providers are registered from domain init() hooks so adding
// a P4 task type never requires editing the P3 switch in runner.go. Providers
// are consulted in registration order and the first match wins.
//
// The registration hook deliberately has no deps parameter (see TaskRef): init()
// runs before application services are wired, and a provider that needed ports
// would either force a shared-file startup hook or bypass tool scope.
var (
	taskContextMu        sync.RWMutex
	taskContextProviders = []func(ctx context.Context, task TaskRef) (any, bool){}
)

// RegisterDeliveryTaskContext appends one payload-only P4 task-context provider. A
// provider returns its JSON-ready task payload derived from the TaskRef and
// true when it owns the work-item type, and (nil, false) otherwise. Nil
// providers are ignored.
func RegisterDeliveryTaskContext(fn func(ctx context.Context, task TaskRef) (any, bool)) {
	if fn == nil {
		return
	}
	taskContextMu.Lock()
	defer taskContextMu.Unlock()
	taskContextProviders = append(taskContextProviders, fn)
}

// DeliveryTaskContext returns the first registered payload for the task. ok is false
// when no registered domain claims the work-item type.
func DeliveryTaskContext(ctx context.Context, task TaskRef) (any, bool) {
	taskContextMu.RLock()
	providers := make([]func(ctx context.Context, task TaskRef) (any, bool), len(taskContextProviders))
	copy(providers, taskContextProviders)
	taskContextMu.RUnlock()
	for _, provider := range providers {
		if payload, ok := provider(ctx, task); ok {
			return payload, true
		}
	}
	return nil, false
}
