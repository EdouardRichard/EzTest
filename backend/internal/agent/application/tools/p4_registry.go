package tools

import "sync"

// P4 tool groups register themselves from domain init() hooks
// (delivery/application issue/report tools, version/application closure tools)
// so parallel P4 streams never edit this file. NewP4Registry composes every
// registered group on top of the frozen P1/P2/P3 tool set.
var (
	p4ToolGroupMu sync.RWMutex
	p4ToolGroups  = []func() []Tool{}
)

// RegisterP4ToolGroup appends one P4 tool group factory. Groups are evaluated
// every time a registry is built, so a group may capture the deps it needs.
// Nil factories are ignored.
func RegisterP4ToolGroup(fn func() []Tool) {
	if fn == nil {
		return
	}
	p4ToolGroupMu.Lock()
	defer p4ToolGroupMu.Unlock()
	p4ToolGroups = append(p4ToolGroups, fn)
}

// NewP4Registry registers the frozen P1/P2/P3 tool set plus every P4 tool group
// registered by the domain init hooks. P4 groups are initially empty; profiles
// filter the registry by allowed_tools, so earlier phases keep their surface.
func NewP4Registry(deps Deps) *Registry {
	base := NewP3Registry(deps)
	all := make([]Tool, 0, len(base.byName))
	for _, name := range base.Names() {
		tool, _ := base.Get(name)
		all = append(all, tool)
	}
	p4ToolGroupMu.RLock()
	groups := make([]func() []Tool, len(p4ToolGroups))
	copy(groups, p4ToolGroups)
	p4ToolGroupMu.RUnlock()
	for _, group := range groups {
		all = append(all, group()...)
	}
	return NewRegistry(all...)
}
