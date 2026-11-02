// Package designadapter adapts the design bounded context's application
// services to the agent tools.DesignPort. It is the only bridge between the
// agent tool layer and design/**.
//
// The adapter renders tool results as the snake_case design view shapes defined
// by spec §7.2 (IDs are decimal strings), so the model sees the same graph
// vocabulary as the HTTP API. Optional IDs are exposed as null/omitted by using
// pointers on the way in and skipping nil pointers in the rendered payload.
package designadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
)

// GraphGateway is the design application surface consumed by the agent tools.
// *designapp.GraphService satisfies it; tests use fakes.
type GraphGateway interface {
	ReadGraph(ctx context.Context, versionID int64) (designapp.GraphView, error)
	GetCase(ctx context.Context, versionID, caseID int64, caseVersionID *int64) (designapp.CaseDetailView, error)
	Propose(ctx context.Context, versionID int64, in designapp.ProposeInput, actorID, runID int64) (designapp.ProposeResult, error)
	SubmitReview(ctx context.Context, versionID, requirementID, runID int64, in designapp.ReviewInput) (designapp.ReviewResult, error)
	SubmitDataPlan(ctx context.Context, versionID, caseVersionID, runID int64, in designapp.DataPlanInput) (designapp.DataPlanResult, error)
}

// Adapter implements tools.DesignPort on top of the design application
// services.
type Adapter struct {
	graph GraphGateway
}

// New wires the design adapter. main passes the design GraphService; a nil
// gateway makes every tool fail as an internal configuration error.
func New(graph GraphGateway) *Adapter {
	return &Adapter{graph: graph}
}

var _ tools.DesignPort = (*Adapter)(nil)

// The design GraphService is asserted at compile time; the assembly root passes
// it directly to New.
var _ GraphGateway = (*designapp.GraphService)(nil)

// ReadGraph returns the version graph snapshot, optionally narrowed to one
// requirement (requirement_id) or to one case's current version detail
// (case_id), matching the design.graph_read contract.
func (a *Adapter) ReadGraph(ctx context.Context, versionID, requirementID, caseID int64) (any, error) {
	if a.graph == nil {
		return nil, fmt.Errorf("designadapter: design graph service is not configured")
	}
	if caseID > 0 {
		detail, err := a.graph.GetCase(ctx, versionID, caseID, nil)
		if err != nil {
			return nil, err
		}
		return caseDetailPayload(detail), nil
	}
	graph, err := a.graph.ReadGraph(ctx, versionID)
	if err != nil {
		return nil, err
	}
	return graphPayload(graph, requirementID), nil
}

// ProposeGraph maps the tool-validated proposal into the design application
// input (optional IDs as pointers) and returns the created IDs as decimal
// strings plus the new graph hash.
func (a *Adapter) ProposeGraph(ctx context.Context, versionID int64, in tools.GraphProposal, actorID, runID int64) (any, error) {
	if a.graph == nil {
		return nil, fmt.Errorf("designadapter: design graph service is not configured")
	}
	result, err := a.graph.Propose(ctx, versionID, proposeInput(in), actorID, runID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"scenario_ids":     idStrings(result.ScenarioIDs),
		"case_ids":         idStrings(result.CaseIDs),
		"case_version_ids": idStrings(result.CaseVersionIDs),
		"graph_hash":       result.GraphHash,
	}, nil
}

// SubmitReview maps a Critic review into the design application input.
func (a *Adapter) SubmitReview(ctx context.Context, versionID, requirementID int64, runID int64, in tools.ReviewInput) (any, error) {
	if a.graph == nil {
		return nil, fmt.Errorf("designadapter: design graph service is not configured")
	}
	findings := make([]designapp.ReviewFinding, 0, len(in.Findings))
	for _, finding := range in.Findings {
		findings = append(findings, designapp.ReviewFinding{
			Severity:      finding.Severity,
			Message:       finding.Message,
			RequirementID: optionalID(finding.RequirementID),
			CaseID:        optionalID(finding.CaseID),
		})
	}
	result, err := a.graph.SubmitReview(ctx, versionID, requirementID, runID, designapp.ReviewInput{
		Summary:  in.Summary,
		Findings: findings,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"posted": result.Posted, "count": result.Count}, nil
}

// SubmitDataPlan maps the Data Planner input into the design application input.
func (a *Adapter) SubmitDataPlan(ctx context.Context, versionID, caseVersionID int64, runID int64, in tools.DataPlanInput) (any, error) {
	if a.graph == nil {
		return nil, fmt.Errorf("designadapter: design graph service is not configured")
	}
	result, err := a.graph.SubmitDataPlan(ctx, versionID, caseVersionID, runID, designapp.DataPlanInput{
		RequirementID: in.RequirementID,
		Preconditions: in.Preconditions,
		TestData:      append(json.RawMessage(nil), in.TestData...),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"case_id":         formatID(result.CaseID),
		"case_version_id": formatID(result.CaseVersionID),
		"revision":        result.Revision,
	}, nil
}

// ---------- tools -> design application ------------------------------------

func proposeInput(in tools.GraphProposal) designapp.ProposeInput {
	proposal := designapp.ProposeInput{
		RequirementID:        in.RequirementID,
		RequirementVersionID: in.RequirementVersionID,
		BaseGraphHash:        in.BaseGraphHash,
		Scenarios:            make([]designapp.ScenarioInput, 0, len(in.Scenarios)),
		Cases:                make([]designapp.CaseInput, 0, len(in.Cases)),
	}
	for _, scenario := range in.Scenarios {
		proposal.Scenarios = append(proposal.Scenarios, designapp.ScenarioInput{
			Ref:           scenario.Ref,
			ID:            optionalID(scenario.ID),
			Name:          scenario.Name,
			Description:   scenario.Description,
			VersionSUTIDs: copyIDs(scenario.VersionSUTIDs),
			PageIDs:       copyIDs(scenario.PageIDs),
			APIIDs:        copyIDs(scenario.APIIDs),
			SortOrder:     scenario.SortOrder,
		})
	}
	for _, item := range in.Cases {
		mapped := designapp.CaseInput{
			Ref:                 item.Ref,
			ID:                  optionalID(item.ID),
			ScenarioRef:         item.ScenarioRef,
			Title:               item.Title,
			Description:         item.Description,
			Preconditions:       item.Preconditions,
			TestData:            append(json.RawMessage(nil), item.TestData...),
			Priority:            item.Priority,
			DefaultVersionSUTID: optionalID(item.DefaultVersionSUTID),
			VersionSUTIDs:       copyIDs(item.VersionSUTIDs),
			PageIDs:             copyIDs(item.PageIDs),
			APIIDs:              copyIDs(item.APIIDs),
			Steps:               make([]designapp.StepInput, 0, len(item.Steps)),
			Assertions:          make([]designapp.AssertionInput, 0, len(item.Assertions)),
			Sources:             make([]designapp.SourceInput, 0, len(item.Sources)),
		}
		for _, step := range item.Steps {
			mapped.Steps = append(mapped.Steps, designapp.StepInput{
				Seq:                step.Seq,
				Action:             step.Action,
				Expected:           step.Expected,
				TargetVersionSUTID: optionalID(step.TargetVersionSUTID),
				TargetPageID:       optionalID(step.TargetPageID),
				TargetAPIID:        optionalID(step.TargetAPIID),
				AccountRole:        step.AccountRole,
				Data:               append(json.RawMessage(nil), step.Data...),
			})
		}
		for _, assertion := range item.Assertions {
			mapped.Assertions = append(mapped.Assertions, designapp.AssertionInput{
				StepSeq:            assertion.StepSeq,
				Kind:               assertion.Kind,
				Target:             assertion.Target,
				Expected:           assertion.Expected,
				TargetVersionSUTID: optionalID(assertion.TargetVersionSUTID),
				TargetAPIID:        optionalID(assertion.TargetAPIID),
				DatabaseID:         optionalID(assertion.DatabaseID),
			})
		}
		for _, source := range item.Sources {
			mapped.Sources = append(mapped.Sources, designapp.SourceInput{
				Kind:  source.Kind,
				RefID: source.RefID,
				Path:  source.Path,
				Line:  source.Line,
				SHA:   source.SHA,
			})
		}
		proposal.Cases = append(proposal.Cases, mapped)
	}
	return proposal
}

// ---------- design application -> tool result payloads ---------------------

func graphPayload(view designapp.GraphView, requirementID int64) map[string]any {
	requirements := make([]any, 0, len(view.Requirements))
	for _, requirement := range view.Requirements {
		if requirementID > 0 && requirement.RequirementID != requirementID {
			continue
		}
		requirements = append(requirements, requirementPayload(requirement))
	}
	return map[string]any{
		"version_id":                   formatID(view.VersionID),
		"generated_at":                 formatTime(view.GeneratedAt),
		"graph_hash":                   view.GraphHash,
		"pending_design_count":         view.PendingDesignCount,
		"auto_design_new_requirements": view.AutoDesignNewRequirements,
		"stats": map[string]any{
			"requirements": view.Stats.Requirements,
			"scenarios":    view.Stats.Scenarios,
			"cases":        view.Stats.Cases,
			"draft":        view.Stats.Draft,
			"confirmed":    view.Stats.Confirmed,
			"stale":        view.Stats.Stale,
		},
		"requirements": requirements,
	}
}

func requirementPayload(view designapp.RequirementGraphView) map[string]any {
	scenarios := make([]any, 0, len(view.Scenarios))
	for _, scenario := range view.Scenarios {
		cases := make([]any, 0, len(scenario.Cases))
		for _, item := range scenario.Cases {
			cases = append(cases, map[string]any{
				"case_id":                formatID(item.CaseID),
				"current_version_id":     formatID(item.CurrentVersionID),
				"revision":               item.Revision,
				"title":                  item.Title,
				"priority":               item.Priority,
				"status":                 item.Status,
				"default_version_sut_id": formatOptionalID(item.DefaultVersionSUTID),
				"version_sut_ids":        idStrings(item.VersionSUTIDs),
				"page_ids":               idStrings(item.PageIDs),
				"api_ids":                idStrings(item.APIIDs),
				"step_count":             item.StepCount,
				"assertion_count":        item.AssertionCount,
				"created_at":             formatTime(item.CreatedAt),
			})
		}
		scenarios = append(scenarios, map[string]any{
			"id":                     formatID(scenario.ID),
			"requirement_version_id": formatID(scenario.RequirementVersionID),
			"name":                   scenario.Name,
			"description":            scenario.Description,
			"version_sut_ids":        idStrings(scenario.VersionSUTIDs),
			"page_ids":               idStrings(scenario.PageIDs),
			"api_ids":                idStrings(scenario.APIIDs),
			"sort":                   scenario.SortOrder,
			"cases":                  cases,
		})
	}
	return map[string]any{
		"requirement_id":         formatID(view.RequirementID),
		"requirement_version_id": formatID(view.RequirementVersionID),
		"revision":               view.Revision,
		"title":                  view.Title,
		"status":                 view.Status,
		"design_status":          view.DesignStatus,
		"target_version_sut_ids": idStrings(view.TargetVersionSUTIDs),
		"scenarios":              scenarios,
	}
}

func caseDetailPayload(view designapp.CaseDetailView) map[string]any {
	versions := make([]any, 0, len(view.Versions))
	for _, item := range view.Versions {
		payload := map[string]any{
			"case_version_id": formatID(item.CaseVersionID),
			"revision":        item.Revision,
			"status":          item.Status,
			"title":           item.Title,
			"created_at":      formatTime(item.CreatedAt),
			"confirmed_at":    nil,
		}
		if item.ConfirmedAt != nil {
			payload["confirmed_at"] = formatTime(*item.ConfirmedAt)
		}
		versions = append(versions, payload)
	}
	selected := view.Selected

	steps := make([]any, 0, len(selected.Steps))
	for _, step := range selected.Steps {
		steps = append(steps, map[string]any{
			"seq":                   step.Seq,
			"action":                step.Action,
			"expected":              step.Expected,
			"target_version_sut_id": formatOptionalID(step.TargetVersionSUTID),
			"target_page_id":        formatOptionalID(step.TargetPageID),
			"target_api_id":         formatOptionalID(step.TargetAPIID),
			"account_role":          step.AccountRole,
			"data":                  rawObject(step.Data),
		})
	}
	assertions := make([]any, 0, len(selected.Assertions))
	for _, assertion := range selected.Assertions {
		assertions = append(assertions, map[string]any{
			"step_seq":              assertion.StepSeq,
			"kind":                  assertion.Kind,
			"target":                assertion.Target,
			"expected":              assertion.Expected,
			"target_version_sut_id": formatOptionalID(assertion.TargetVersionSUTID),
			"target_api_id":         formatOptionalID(assertion.TargetAPIID),
			"database_id":           formatOptionalID(assertion.DatabaseID),
		})
	}
	sources := make([]any, 0, len(selected.Sources))
	for _, source := range selected.Sources {
		sources = append(sources, map[string]any{
			"kind":   source.Kind,
			"ref_id": source.RefID,
			"path":   source.Path,
			"line":   optionalInt(source.Line),
			"sha":    source.SHA,
		})
	}
	return map[string]any{
		"case_id":                formatID(view.CaseID),
		"status":                 view.Status,
		"current_version_id":     formatOptionalID(view.CurrentVersionID),
		"requirement_id":         formatID(view.RequirementID),
		"requirement_version_id": formatID(view.RequirementVersionID),
		"scenario_id":            formatID(view.ScenarioID),
		"scenario_name":          view.ScenarioName,
		"versions":               versions,
		"confirmable":            view.Confirmable,
		"selected": map[string]any{
			"case_version_id":        formatID(selected.CaseVersionID),
			"revision":               selected.Revision,
			"title":                  selected.Title,
			"description":            selected.Description,
			"preconditions":          selected.Preconditions,
			"test_data":              rawObject(selected.TestData),
			"priority":               selected.Priority,
			"default_version_sut_id": formatOptionalID(selected.DefaultVersionSUTID),
			"version_sut_ids":        idStrings(selected.VersionSUTIDs),
			"page_ids":               idStrings(selected.PageIDs),
			"api_ids":                idStrings(selected.APIIDs),
			"status":                 selected.Status,
			"content_hash":           selected.ContentHash,
			"created_at":             formatTime(selected.CreatedAt),
			"steps":                  steps,
			"assertions":             assertions,
			"sources":                sources,
		},
	}
}

// ---------- small conversions ----------------------------------------------

func optionalID(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	copied := value
	return &copied
}

func formatID(value int64) string { return strconv.FormatInt(value, 10) }

func formatOptionalID(value *int64) any {
	if value == nil || *value <= 0 {
		return nil
	}
	return formatID(*value)
}

func optionalInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func idStrings(ids []int64) []string {
	values := make([]string, 0, len(ids))
	for _, id := range ids {
		values = append(values, formatID(id))
	}
	return values
}

func copyIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return []int64{}
	}
	copied := make([]int64, len(ids))
	copy(copied, ids)
	return copied
}

func rawObject(raw json.RawMessage) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	return raw
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}
