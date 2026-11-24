package tools

// P4 report-writer tool names (contracts/agent-profiles-tools.md §7.3). The
// delivery application context implements the tools (deliveryapp.ReportTools);
// the constants stay here so the agent registry, the profile seeds and the
// delivery-context tests share one frozen spelling.
const (
	ToolReportDataRead         = "report.data_read"
	ToolReportNarrativePropose = "report.narrative_propose"
)

// ReportTools wraps the delivery context's report tool group factory into the
// deps-aware factory RegisterToolGroup expects. The delivery application
// package imports this package for Tool/Scope, so importing it here would create
// an import cycle; instead the composition root (Task 23) captures the wired
// service:
//
//	tools.RegisterToolGroup(tools.ReportTools(func() []Tool {
//		return deliveryapp.ReportTools(reportService)
//	}))
//
// A nil factory yields a nil group, which RegisterToolGroup ignores.
func ReportTools(factory func() []Tool) func(Deps) []Tool {
	if factory == nil {
		return nil
	}
	return func(Deps) []Tool { return factory() }
}
