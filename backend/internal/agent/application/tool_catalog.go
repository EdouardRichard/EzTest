package application

import (
	"sort"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// ToolCatalog exposes the tools registered in the run-time ToolRegistry to the
// HTTP layer. The registry keeps registration order internally; the catalog
// sorts by name so GET /api/agents/tools has a stable, alphabetical contract.
type ToolCatalog struct {
	registry *ToolRegistry
}

// NewToolCatalog returns the tool catalog for registry. A nil registry produces
// an empty catalog.
func NewToolCatalog(registry *ToolRegistry) *ToolCatalog {
	return &ToolCatalog{registry: registry}
}

// Infos returns every registered tool's metadata sorted by name. The slice is
// freshly allocated by the registry, so sorting it never mutates the registry.
func (c *ToolCatalog) Infos() []domain.ToolInfo {
	if c == nil || c.registry == nil {
		return nil
	}
	infos := c.registry.ToolInfos()
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}
