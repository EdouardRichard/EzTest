package platform

import "context"

type agentRunKey struct{}

// AgentRunContext carries the agent run/work item provenance across bounded
// context boundaries so write services can attribute agent-authored rows and
// audit records without adding agent-only fields to their public ports.
type AgentRunContext struct {
	RunID      int64
	WorkItemID int64
}

// WithAgentRun marks the context as an agent run. Zero IDs are ignored.
func WithAgentRun(ctx context.Context, runID, workItemID int64) context.Context {
	if runID <= 0 {
		return ctx
	}
	return context.WithValue(ctx, agentRunKey{}, AgentRunContext{RunID: runID, WorkItemID: workItemID})
}

// AgentRunFrom reads the agent run provenance, if any.
func AgentRunFrom(ctx context.Context) (AgentRunContext, bool) {
	value, ok := ctx.Value(agentRunKey{}).(AgentRunContext)
	if !ok || value.RunID <= 0 {
		return AgentRunContext{}, false
	}
	return value, true
}
