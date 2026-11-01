package main

// P6 Agent Center composition: the logged-in read model and the admin profile /
// prompt surface. The quality latest-eval read is adapted to the agent
// center's narrow EvalSummary port so the two contexts stay decoupled.

import (
	"context"
	"fmt"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	agentapi "github.com/EdouardRichard/EzTest/backend/internal/agent/interfaces/httpapi"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	qualitypg "github.com/EdouardRichard/EzTest/backend/internal/quality/infrastructure/postgres"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

// p6AgentRuntime is the constructed Agent Center surface.
type p6AgentRuntime struct {
	AgentCenter *agentapi.AgentCenterHandler
	Profiles    *agentapi.ProfileHandler
}

// p6LatestEvalAdapter maps the quality repository's latest run to the agent
// center's compact EvalSummary. A nil run stays nil (the UI shows "not run").
type p6LatestEvalAdapter struct {
	repo *qualitypg.Repository
}

func (a p6LatestEvalAdapter) LatestEval(ctx context.Context) (*agentapp.EvalSummary, error) {
	if a.repo == nil {
		return nil, nil
	}
	run, err := a.repo.Latest(ctx)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, nil
	}
	passed := 0
	for _, metric := range run.Metrics {
		if metric.Passed {
			passed++
		}
	}
	return &agentapp.EvalSummary{
		ID:            run.ID,
		Mode:          run.Mode,
		Status:        run.Status,
		CreatedAt:     run.CreatedAt,
		MetricsCount:  len(run.Metrics),
		MetricsPassed: passed,
	}, nil
}

// newP6AgentRuntime builds the Agent Center read model and the admin profile
// service over the already-seeded runtime repository.
func newP6AgentRuntime(
	pool *pgxpool.Pool,
	runtimeRepo *agentpg.RuntimeRepository,
	versions *versionapp.QueryService,
	llmSvc *llmapp.Service,
	audit auditapp.Recorder,
) (*p6AgentRuntime, error) {
	if pool == nil {
		return nil, fmt.Errorf("p6 agent runtime: nil pool")
	}
	if runtimeRepo == nil {
		return nil, fmt.Errorf("p6 agent runtime: nil runtime repository")
	}

	centerReader, err := agentpg.NewAgentCenterReader(pool)
	if err != nil {
		return nil, err
	}
	centerSvc := agentapp.NewAgentCenterService(
		centerReader,
		versions,
		p6LatestEvalAdapter{repo: qualitypg.NewRepository(pool)},
	)

	profileSvc := agentapp.NewProfileAdminService(
		runtimeRepo,
		// P7 adds knowledge.propose so the admin catalog and allowed_tools
		// validation know the frozen P7 tool set (S6 report §5.6).
		tools.NewP7Registry(tools.Deps{}),
		llmSvc,
		audit,
	)

	return &p6AgentRuntime{
		AgentCenter: agentapi.NewAgentCenterHandler(centerSvc),
		Profiles:    agentapi.NewProfileHandler(profileSvc),
	}, nil
}

// ensurePromptRevisions seeds the first prompt revision for every profile that
// has none. It is idempotent and never overwrites an administrator edit.
func ensurePromptRevisions(ctx context.Context, runtimeRepo *agentpg.RuntimeRepository) error {
	return agentapp.EnsurePromptRevisions(ctx, runtimeRepo)
}
