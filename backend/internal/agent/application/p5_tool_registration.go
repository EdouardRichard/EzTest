package agentapp

// The execution.visual_assert tool implements itself in the execution agent
// tool package and joins the P5 registry from that package's init hook (the
// tool package imports agent/application/tools for Scope, so that package
// cannot import this one). Importing it here guarantees the tool is present in
// every agent process and test binary that builds the P5 registry; the actual
// CaseRunner binding is supplied per run through tools.Deps.P5.VisualAssert.
import (
	_ "github.com/EdouardRichard/EzTest/backend/internal/execution/application/agent_tool"
)
