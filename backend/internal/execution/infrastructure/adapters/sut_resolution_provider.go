package adapters

import (
	"context"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

// ResolutionTaskProvider decorates the execution task provider with the P16 SUT
// resolution (v0.2.4-F T9, SC-F2). After CaseRunner.Prepare claims the run and
// returns the immutable snapshot, it resolves the case's natural-language
// sut_description against the version's effective SUT catalog and records the
// sut_resolution fact (with the touched/verified page/API asset ids) BEFORE the
// agent run starts. The directory-hit path thus has a knowledge-dimension truth
// source even when the run never explores; a missing match or asset-level gap
// (R-E8) records method=exploration and leaves the agent to explore autonomously.
//
// It reuses the same Prepare + mapTaskSnapshot chain as TaskProviderAdapter (no
// second mapping authority); the only addition is the resolution side effect.
// Resolution is not a gate (§6.6 full autonomy): an exploration outcome proceeds
// normally; only an infra failure (catalog read / fact write) is returned, which
// the work queue retries — never a silent empty resolution.
type ResolutionTaskProvider struct {
	runner   *executionapp.CaseRunner
	resolver *executionapp.SUTResolutionService
}

// NewResolutionTaskProvider wires the resolution-decorated task provider. A nil
// resolver degrades to the plain provider behavior (no resolution recorded) so a
// half-assembled process still dispatches; production always wires it.
func NewResolutionTaskProvider(runner *executionapp.CaseRunner, resolver *executionapp.SUTResolutionService) *ResolutionTaskProvider {
	return &ResolutionTaskProvider{runner: runner, resolver: resolver}
}

var _ agentapp.TaskContextProvider = (*ResolutionTaskProvider)(nil)

// ExecutionTask prepares the run, records its SUT resolution, then maps the task
// snapshot for the agent context.
func (p *ResolutionTaskProvider) ExecutionTask(ctx context.Context, item agent.WorkItem) (agentapp.ExecutionTask, error) {
	if p.runner == nil {
		return agentapp.ExecutionTask{}, errNotConfigured("case runner")
	}
	snapshot, err := p.runner.Prepare(ctx, item)
	if err != nil {
		return agentapp.ExecutionTask{}, err
	}
	if p.resolver != nil {
		if _, resolveErr := p.resolver.ResolveForRun(ctx, item.VersionID, snapshot.CaseRun.ID, snapshot.Case.SUTDescription); resolveErr != nil {
			return agentapp.ExecutionTask{}, resolveErr
		}
	}
	return mapTaskSnapshot(snapshot), nil
}
