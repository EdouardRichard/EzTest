package main

// P6 composition root: quality, search and workspace read models plus the
// global delivery centres. Agent-centre wiring lives in p6_agent_wiring.go so
// the shared server package stays free of the agent HTTP surface until the
// Agent Center task lands.

import (
	"fmt"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	deliverypg "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/postgres"
	deliveryapi "github.com/EdouardRichard/EzTest/backend/internal/delivery/interfaces/httpapi"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	designpg "github.com/EdouardRichard/EzTest/backend/internal/design/infrastructure/postgres"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	eventingpg "github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executionpg "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	qualityapp "github.com/EdouardRichard/EzTest/backend/internal/quality/application"
	qualityadapter "github.com/EdouardRichard/EzTest/backend/internal/quality/infrastructure/adapters"
	qualitymetrics "github.com/EdouardRichard/EzTest/backend/internal/quality/infrastructure/metrics"
	qualitypg "github.com/EdouardRichard/EzTest/backend/internal/quality/infrastructure/postgres"
	qualityapi "github.com/EdouardRichard/EzTest/backend/internal/quality/interfaces/httpapi"
	searchapp "github.com/EdouardRichard/EzTest/backend/internal/search/application"
	searchproviders "github.com/EdouardRichard/EzTest/backend/internal/search/infrastructure/providers"
	searchapi "github.com/EdouardRichard/EzTest/backend/internal/search/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/server"
	settingsapp "github.com/EdouardRichard/EzTest/backend/internal/settings/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
	visualapp "github.com/EdouardRichard/EzTest/backend/internal/visual/application"
	visualpg "github.com/EdouardRichard/EzTest/backend/internal/visual/infrastructure/postgres"
	workspaceapp "github.com/EdouardRichard/EzTest/backend/internal/workspace/application"
	workspaceproviders "github.com/EdouardRichard/EzTest/backend/internal/workspace/infrastructure/providers"
	workspaceapi "github.com/EdouardRichard/EzTest/backend/internal/workspace/interfaces/httpapi"
	"github.com/jackc/pgx/v5/pgxpool"
)

// p6ReadDeps are the shared P0-P5 services the P6 read models consume.
type p6ReadDeps struct {
	Pool     *pgxpool.Pool
	IDs      platformid.Generator
	Clock    clock.Clock
	Settings *settingsapp.Service
	Audit    auditapp.Recorder
	Events   eventingapp.Publisher
	Issues   *deliveryapp.IssueService
	Reports  *deliveryapp.ReportService
}

// p6ReadRuntime is the constructed P6 read-model surface plus the query
// services the Agent Center wiring reuses.
type p6ReadRuntime struct {
	Handlers     server.P6Handlers
	VersionQuery *versionapp.QueryService
	DesignQuery  *designapp.QueryService
	AgentQuery   *agentapp.QueryService
}

// newP6ReadRuntime builds the quality/search/workspace/global read models over
// the P0-P5 infrastructure. Every reader is read-only and every cross-context
// lookup goes through the owning application query port.
func newP6ReadRuntime(deps p6ReadDeps) (*p6ReadRuntime, error) {
	if deps.Pool == nil {
		return nil, fmt.Errorf("p6 read runtime: nil pool")
	}

	designReader, err := designpg.NewQueryReader(deps.Pool)
	if err != nil {
		return nil, err
	}
	designQuery := designapp.NewQueryService(designReader)

	agentReader, err := agentpg.NewQueryReader(deps.Pool)
	if err != nil {
		return nil, err
	}
	agentQuery := agentapp.NewQueryService(agentReader)

	versionReader, err := versionpg.NewQueryReader(deps.Pool, designQuery)
	if err != nil {
		return nil, err
	}
	versionQuery := versionapp.NewQueryService(versionReader)
	versionRead := versionapp.NewVersionReadService(versionReader)

	executionReader, err := executionpg.NewQueryReader(deps.Pool, agentQuery)
	if err != nil {
		return nil, err
	}
	executionQuery := executionapp.NewQueryService(executionReader)

	deliveryReader, err := deliverypg.NewQueryReader(deps.Pool)
	if err != nil {
		return nil, err
	}
	deliveryQuery := deliveryapp.NewQueryService(deliveryReader)

	eventingReader, err := eventingpg.NewQueryReader(deps.Pool)
	if err != nil {
		return nil, err
	}
	eventingQuery := eventingapp.NewQueryService(eventingReader)

	qualityRepo := qualitypg.NewRepository(deps.Pool)
	aggregator := qualitymetrics.NewAggregator(
		versionQuery, designQuery, executionQuery, deliveryQuery, agentQuery, eventingQuery)
	// W2-14: the snapshot trigger reads the same version/design/execution ports
	// as the operational overview plus the visual diff classification counts.
	visualRepo := visualpg.NewRepository(deps.Pool)
	visualOverview := visualapp.NewOverviewService(visualapp.OverviewServiceDeps{
		Diffs: visualRepo.Diffs, Clock: deps.Clock,
	})
	qualitySvc := qualityapp.NewService(qualityRepo, deps.IDs, deps.Clock, aggregator, deps.Settings, deps.Audit).
		WithSnapshotPorts(executionQuery, versionQuery, designQuery,
			qualityadapter.NewVisualClassification(visualOverview)).
		WithEvents(qualityadapter.NewEventPublisher(deps.Events))

	searchSvc := searchapp.NewService(
		searchproviders.New(versionQuery, designQuery, deliveryQuery),
	).WithVersionTitles(versionQuery).SetLimits(deps.Settings)

	todosSvc := workspaceapp.NewService(
		workspaceproviders.NewVersionProvider(versionQuery),
		workspaceproviders.NewDesignProvider(designQuery),
		workspaceproviders.NewExecutionProvider(executionQuery),
		workspaceproviders.NewDeliveryProvider(deliveryQuery),
	).WithVersionTitles(versionQuery).SetLimits(deps.Settings)

	globalRepo := deliverypg.NewGlobalRepository(deps.Pool)
	if deps.Issues != nil {
		deps.Issues.WithGlobalReader(globalRepo, versionQuery)
	}
	if deps.Reports != nil {
		deps.Reports.WithGlobalReader(globalRepo, versionQuery)
	}

	return &p6ReadRuntime{
		Handlers: server.P6Handlers{
			Quality: qualityapi.New(qualitySvc),
			Search:  searchapi.New(searchSvc),
			Todos:   workspaceapi.New(todosSvc),
			Global:  deliveryapi.NewGlobal(deps.Issues, deps.Reports),
			// 0.2.1 version-scoped reads: facts/plan items come from the
			// version read source, work items from the agent query service.
			VersionRead: versionapi.NewVersionReadHandler(versionRead, agentQuery),
		},
		VersionQuery: versionQuery,
		DesignQuery:  designQuery,
		AgentQuery:   agentQuery,
	}, nil
}
