package tools

import "sync"

// P4 tool groups register themselves from domain init() hooks
// (delivery/application issue/report tools, version/application closure tools)
// so parallel P4 streams never edit this file. NewToolGroupRegistry composes every
// registered group on top of the frozen P1/P2/P3 tool set and passes each
// group the per-run deps, letting domain tools bind their application services
// without touching a global.
var (
	toolGroupMu sync.RWMutex
	toolGroups  = []func(deps Deps) []Tool{}
)

// RegisterToolGroup appends one P4 tool group factory. Groups are evaluated
// every time a registry is built with the deps the runner resolved for that
// run, so a domain tool constructor binds services through the deps argument.
// Nil factories are ignored.
func RegisterToolGroup(fn func(deps Deps) []Tool) {
	if fn == nil {
		return
	}
	toolGroupMu.Lock()
	defer toolGroupMu.Unlock()
	toolGroups = append(toolGroups, fn)
}

// NewToolGroupRegistry registers the frozen P1/P2/P3 tool set plus every P4 tool group
// registered by the domain init hooks. P4 groups are initially empty; profiles
// filter the registry by allowed_tools, so earlier phases keep their surface.
// Every group receives deps so service-backed P4 tools never read global state.
func NewToolGroupRegistry(deps Deps) *Registry {
	base := NewExecutionRegistry(deps)
	all := make([]Tool, 0, len(base.byName))
	for _, name := range base.Names() {
		tool, _ := base.Get(name)
		all = append(all, tool)
	}
	toolGroupMu.RLock()
	groups := make([]func(deps Deps) []Tool, len(toolGroups))
	copy(groups, toolGroups)
	toolGroupMu.RUnlock()
	for _, group := range groups {
		all = append(all, group(deps)...)
	}
	return NewRegistry(all...)
}
