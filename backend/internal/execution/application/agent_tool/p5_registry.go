package agenttool

import (
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
)

// Register the execution.visual_assert tool in the P5 agent registry. The group
// is evaluated with the per-run deps, so the tool binds the CaseRunner the
// wiring supplies as tools.Deps.P5.VisualAssert and never reads global state.
// The package imports tools (for Scope), so this init hook is the only
// cycle-free way to reuse NewVisualAssertTool from the agent tools registry.
func init() {
	tools.RegisterP5ToolGroup(func(deps tools.Deps) []tools.Tool {
		return []tools.Tool{NewVisualAssertTool(deps.P5.VisualAssert)}
	})
}
