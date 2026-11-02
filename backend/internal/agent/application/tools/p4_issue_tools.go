package tools

// P4 issue-triager tool names (contracts/agent-profiles-tools.md §7.3). The
// delivery application context implements the tools (deliveryapp.IssueTools);
// the constants stay here so the agent registry, the profile seeds and the
// delivery-context tests share one frozen spelling.
const (
	ToolIssueRead    = "issue.read"
	ToolIssuePropose = "issue.propose"
)

// IssueTools wraps the delivery context's issue tool group factory into the
// deps-aware factory RegisterP4ToolGroup expects. The delivery application
// package imports this package for Tool/Scope, so importing it here would
// create an import cycle; instead the composition root (Task 23) captures the
// wired services:
//
//	tools.RegisterP4ToolGroup(tools.IssueTools(func() []Tool {
//		return deliveryapp.IssueTools(issueService, analysisService)
//	}))
//
// A nil factory yields a nil group, which RegisterP4ToolGroup ignores.
func IssueTools(factory func() []Tool) func(Deps) []Tool {
	if factory == nil {
		return nil
	}
	return func(Deps) []Tool { return factory() }
}
