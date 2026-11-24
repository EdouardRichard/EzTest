package main

import (
	"context"
	"errors"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/domain"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// p4Clock is documented at construction: platform clock.System satisfies the
// deliveryapp.Clock port structurally, so the composition root passes it
// directly and no adapter is needed.

// deliveryTriagePoster projects an accepted triage proposal into the delivery
// team room. Team-room failures are already ignored by the analysis service.
type deliveryTriagePoster struct {
	room *agentapp.TeamRoom
}

func (p deliveryTriagePoster) PostTriageFinding(ctx context.Context, finding deliveryapp.TriageFinding) error {
	if p.room == nil {
		return nil
	}
	refs := make([]agent.PostRef, 0, len(finding.Refs))
	for _, ref := range finding.Refs {
		refs = append(refs, agent.PostRef{Kind: ref.Kind, ID: platformid.Format(ref.RefID)})
	}
	var agentRunID *int64
	if finding.AgentRunID > 0 {
		id := finding.AgentRunID
		agentRunID = &id
	}
	_, err := p.room.Post(ctx, agent.TeamRoomPost{
		VersionID:  finding.VersionID,
		Team:       agent.TeamDelivery,
		Kind:       agent.PostKindFinding,
		Content:    finding.Content,
		Refs:       refs,
		AgentRunID: agentRunID,
	})
	return err
}

// deliveryReportFinalizer is the runner finalizer of report.wave/report.version
// runs: it publishes the staged draft (with the narrative candidate proposed
// through report.narrative_propose) and then verifies the revision is the
// published current one. A failure makes the work item retry.
type deliveryReportFinalizer struct {
	service *deliveryapp.ReportService
}

func (f deliveryReportFinalizer) Finalize(ctx context.Context, reportID int64, revision int32) error {
	if f.service == nil {
		return errors.New("report finalizer has no report service")
	}
	return f.service.FinalizeRevision(ctx, reportID, revision)
}

// registerDeliveryPlugins wires the P4 tool groups and payload-only task contexts.
// It is the single registration point of the composition root (no init hooks);
// project-scoped tools capture the constructed services so a model can never
// reach another version's data. GD-17 removed the version-steward tool group and
// the close task context: closure is a deterministic reconciler action.
func registerDeliveryPlugins(
	issueService *deliveryapp.IssueService,
	analysisService *deliveryapp.AnalysisService,
	reportService *deliveryapp.ReportService,
) {
	tools.RegisterToolGroup(tools.IssueTools(func() []tools.Tool {
		return deliveryapp.IssueTools(issueService, analysisService)
	}))
	tools.RegisterToolGroup(tools.ReportTools(func() []tools.Tool {
		return deliveryapp.ReportTools(reportService)
	}))

	agentapp.RegisterDeliveryTaskContext(func(_ context.Context, ref agentapp.TaskRef) (any, bool) {
		return agentapp.IssueTaskContext(ref)
	})
	agentapp.RegisterDeliveryTaskContext(func(_ context.Context, ref agentapp.TaskRef) (any, bool) {
		return agentapp.ReportTaskContext(ref)
	})
}

// deliverySectionOptions reads the report.section_* toggles (spec §4.5). A missing or
// unreadable key keeps the section enabled.
func deliverySectionOptions(ctx context.Context, settings interface {
	Bool(ctx context.Context, key string) (bool, error)
}) delivery.SectionOptions {
	enabled := func(key string) bool {
		if settings == nil {
			return true
		}
		value, err := settings.Bool(ctx, key)
		if err != nil {
			return true
		}
		return value
	}
	return delivery.SectionOptions{
		Summary:  enabled("report.section_summary"),
		Results:  enabled("report.section_results"),
		Issues:   enabled("report.section_issues"),
		Coverage: enabled("report.section_coverage"),
		Evidence: enabled("report.section_evidence"),
		// Template v2 appendix (P7). Template v1 ignores it, keeping its bytes
		// frozen (S1 report §5.6).
		Appendix: enabled("report.section_appendix"),
	}
}

// deliveryChainBudget reads the chain.max_* settings tuple (spec §4.5). Out-of-range
// or missing values fall back to the domain defaults through ClampBudget, so an
// invalid setting can never disable the chain.
func deliveryChainBudget(ctx context.Context, settings interface {
	Int(ctx context.Context, key string) (int64, error)
}) chain.Budget {
	value := func(key string, fallback int) int {
		if settings == nil {
			return fallback
		}
		parsed, err := settings.Int(ctx, key)
		if err != nil || parsed < 1 || parsed > int64(^uint(0)>>1) {
			return fallback
		}
		return int(parsed)
	}
	return chain.ClampBudget(chain.Budget{
		MaxNodes:        value("chain.max_nodes", chain.DefaultMaxNodes),
		MaxEdges:        value("chain.max_edges", chain.DefaultMaxEdges),
		MaxRequirements: value("chain.max_requirements", chain.DefaultMaxRequirements),
	})
}
