package agentapp

import (
	"context"
	"sync"
)

// TaskRef is the P4 work-item reference handed to domain task-context
// providers. Payload is the raw work-item payload (IDs stay decimal strings);
// each domain parses only the keys it owns.
type TaskRef struct {
	VersionID int64
	Type      string
	Payload   map[string]any
}

// P4 task-context providers are registered from domain init() hooks so adding
// a P4 task type never requires editing the P3 switch in runner.go. Providers
// are consulted in registration order and the first match wins.
var (
	p4TaskContextMu        sync.RWMutex
	p4TaskContextProviders = []func(ctx context.Context, task TaskRef) (any, bool){}
)

// RegisterP4TaskContext appends one P4 task-context provider. A provider
// returns its JSON-ready task payload and true when it owns the work-item type,
// and (nil, false) otherwise. Nil providers are ignored.
func RegisterP4TaskContext(fn func(ctx context.Context, task TaskRef) (any, bool)) {
	if fn == nil {
		return
	}
	p4TaskContextMu.Lock()
	defer p4TaskContextMu.Unlock()
	p4TaskContextProviders = append(p4TaskContextProviders, fn)
}

// P4TaskContext returns the first registered payload for the task. ok is false
// when no registered domain claims the work-item type.
func P4TaskContext(ctx context.Context, task TaskRef) (any, bool) {
	p4TaskContextMu.RLock()
	providers := make([]func(ctx context.Context, task TaskRef) (any, bool), len(p4TaskContextProviders))
	copy(providers, p4TaskContextProviders)
	p4TaskContextMu.RUnlock()
	for _, provider := range providers {
		if payload, ok := provider(ctx, task); ok {
			return payload, true
		}
	}
	return nil, false
}
