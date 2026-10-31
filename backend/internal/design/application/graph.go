// Package designapp implements the P2 case-design use cases: graph reads,
// case.graph_propose writes, user confirmations, stale propagation, the design
// planner/reconciler and case exports. It depends only on domain interfaces and
// cross-context application ports.
package designapp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

// GraphService serves graph/case reads, the case.graph_propose write path, user
// confirmations, review/data-plan submissions.
type GraphService struct {
	repo     design.Repository
	ids      platformid.Generator
	clock    clock.Clock
	audit    auditapp.Recorder
	events   eventingapp.Publisher
	teamRoom TeamRoom
}

// NewGraphService wires the graph service. audit, events and teamRoom are
// best-effort sinks and may be nil in unit tests.
func NewGraphService(
	repo design.Repository,
	ids platformid.Generator,
	clk clock.Clock,
	audit auditapp.Recorder,
	events eventingapp.Publisher,
	teamRoom TeamRoom,
) *GraphService {
	if clk == nil {
		clk = clock.System{}
	}
	return &GraphService{repo: repo, ids: ids, clock: clk, audit: audit, events: events, teamRoom: teamRoom}
}

// ReadGraph returns the full version design graph snapshot.
func (s *GraphService) ReadGraph(ctx context.Context, versionID int64) (GraphView, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return GraphView{}, err
	}
	data, err := s.repo.ReadGraph(ctx, versionID)
	if err != nil {
		return GraphView{}, mapDesignError(err, "version")
	}
	hash, err := design.ComputeGraphHash(data.Requirements)
	if err != nil {
		return GraphView{}, err
	}
	view := GraphView{
		VersionID:                 data.VersionID,
		GeneratedAt:               s.clock.Now().UTC(),
		GraphHash:                 hash,
		PendingDesignCount:        data.PendingDesignCount,
		AutoDesignNewRequirements: data.AutoDesignNewRequirements,
		Stats:                     graphStatsView(data.Stats),
		Requirements:              make([]RequirementGraphView, 0, len(data.Requirements)),
	}
	for _, requirement := range data.Requirements {
		requirementView := RequirementGraphView{
			RequirementID:        requirement.Requirement.ID,
			RequirementVersionID: requirement.Requirement.CurrentVersionID,
			Revision:             requirement.Requirement.Revision,
			Title:                requirement.Requirement.Title,
			Status:               requirement.Requirement.Status,
			DesignStatus:         requirement.DesignStatus,
			TargetVersionSUTIDs:  requirement.Requirement.TargetVersionSUTIDs,
			Scenarios:            make([]ScenarioGraphView, 0, len(requirement.Scenarios)),
		}
		for _, scenario := range requirement.Scenarios {
			scenarioView := ScenarioGraphView{
				ID:                   scenario.Scenario.ID,
				RequirementVersionID: scenario.Scenario.RequirementVersionID,
				Name:                 scenario.Scenario.Name,
				Description:          scenario.Scenario.Description,
				VersionSUTIDs:        scenario.Scenario.VersionSUTIDs,
				PageIDs:              scenario.Scenario.PageIDs,
				APIIDs:               scenario.Scenario.APIIDs,
				SortOrder:            scenario.Scenario.SortOrder,
				Cases:                make([]CaseCardView, 0, len(scenario.Cases)),
			}
			for _, item := range scenario.Cases {
				scenarioView.Cases = append(scenarioView.Cases, CaseCardView{
					CaseID:              item.Case.ID,
					CurrentVersionID:    derefInt64(item.Case.CurrentVersionID),
					Revision:            item.Current.Revision,
					Title:               item.Current.Title,
					Priority:            item.Current.Priority,
					Status:              item.Current.Status,
					DefaultVersionSUTID: item.Current.DefaultVersionSUTID,
					VersionSUTIDs:       item.Current.VersionSUTIDs,
					PageIDs:             item.Current.PageIDs,
					APIIDs:              item.Current.APIIDs,
					StepCount:           item.StepCount,
					AssertionCount:      item.AssertionCount,
					CreatedAt:           utcNow(item.Current.CreatedAt),
				})
			}
			requirementView.Scenarios = append(requirementView.Scenarios, scenarioView)
		}
		view.Requirements = append(view.Requirements, requirementView)
	}
	return view, nil
}

// GetCase returns one case detail with its version history and a selected
// version (the requested one or the current version).
func (s *GraphService) GetCase(ctx context.Context, versionID, caseID int64, caseVersionID *int64) (CaseDetailView, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return CaseDetailView{}, err
	}
	if err := requirePositiveID("case_id", caseID); err != nil {
		return CaseDetailView{}, err
	}
	if caseVersionID != nil {
		if err := requirePositiveID("version_id", *caseVersionID); err != nil {
			return CaseDetailView{}, err
		}
	}
	data, err := s.repo.ReadCase(ctx, versionID, caseID, caseVersionID)
	if err != nil {
		return CaseDetailView{}, mapDesignError(err, "case")
	}
	view := CaseDetailView{
		CaseID:               data.Case.ID,
		Status:               data.Case.Status,
		CurrentVersionID:     data.Case.CurrentVersionID,
		RequirementID:        data.Requirement.ID,
		RequirementVersionID: data.Requirement.CurrentVersionID,
		ScenarioID:           data.Scenario.ID,
		ScenarioName:         data.Scenario.Name,
		Versions:             make([]CaseVersionSummaryView, 0, len(data.Versions)),
		Selected:             caseVersionDetailView(data.Selected),
		Confirmable: data.Selected.ID > 0 &&
			data.Case.CurrentVersionID != nil &&
			data.Selected.ID == *data.Case.CurrentVersionID &&
			data.Selected.Status == design.CaseStatusDraft &&
			data.Case.Status == design.CaseStatusDraft &&
			data.Scenario.RequirementVersionID == data.Requirement.CurrentVersionID,
	}
	for _, version := range data.Versions {
		summary := CaseVersionSummaryView{
			CaseVersionID: version.ID,
			Revision:      version.Revision,
			Status:        version.Status,
			Title:         version.Title,
			CreatedAt:     utcNow(version.CreatedAt),
		}
		if version.ConfirmedAt != nil {
			confirmedAt := utcNow(*version.ConfirmedAt)
			summary.ConfirmedAt = &confirmedAt
		}
		view.Versions = append(view.Versions, summary)
	}
	return view, nil
}

// Overview returns the version design overview.
func (s *GraphService) Overview(ctx context.Context, versionID int64) (OverviewView, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return OverviewView{}, err
	}
	data, err := s.repo.Overview(ctx, versionID)
	if err != nil {
		return OverviewView{}, mapDesignError(err, "version")
	}
	view := OverviewView{
		PendingDesignCount:        data.PendingDesignCount,
		AutoDesignNewRequirements: data.AutoDesignNewRequirements,
		RoundCount:                data.RoundCount,
	}
	if data.ActiveRound != nil {
		round := roundView(*data.ActiveRound, data.ActiveRoundNo)
		view.ActiveRound = &round
	}
	return view, nil
}

// createdCaseVersion records one inserted draft for events/audit.
type createdCaseVersion struct {
	caseID        int64
	caseVersionID int64
	revision      int
	created       bool
}

// Propose validates and applies one case.graph_propose batch inside the
// per-requirement advisory lock.
func (s *GraphService) Propose(ctx context.Context, versionID int64, in ProposeInput, actorID, runID int64) (ProposeResult, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return ProposeResult{}, err
	}
	if err := requirePositiveID("requirement_id", in.RequirementID); err != nil {
		return ProposeResult{}, err
	}
	if err := requirePositiveID("requirement_version_id", in.RequirementVersionID); err != nil {
		return ProposeResult{}, err
	}
	versionContext, err := s.repo.VersionContext(ctx, versionID)
	if err != nil {
		return ProposeResult{}, mapDesignError(err, "version")
	}
	requirement, err := s.repo.RequirementContext(ctx, versionID, in.RequirementID)
	if err != nil {
		return ProposeResult{}, mapDesignError(err, "requirement")
	}
	if requirement.Status != "clarified" {
		return ProposeResult{}, platform.StateConflict("the requirement must be clarified before it can be designed")
	}
	if requirement.CurrentVersionID == 0 || requirement.CurrentVersionID != in.RequirementVersionID {
		return ProposeResult{}, platform.StateConflict("the requirement version is stale; re-read the graph and retry")
	}

	graph, err := s.repo.ReadGraph(ctx, versionID)
	if err != nil {
		return ProposeResult{}, mapDesignError(err, "version")
	}
	currentHash, err := design.ComputeGraphHash(graph.Requirements)
	if err != nil {
		return ProposeResult{}, err
	}

	proposal := toDomainProposal(in)
	validationContext := design.ProposalContext{
		VersionID:            versionID,
		RequirementID:        in.RequirementID,
		RequirementVersionID: in.RequirementVersionID,
		CurrentHash:          currentHash,
		AllowedVersionSUTs:   versionContext.AllowedVersionSUTs,
		PageToSUT:            versionContext.PageToSUT,
		APIToSUT:             versionContext.APIToSUT,
		DBTOSUT:              versionContext.DBTOSUT,
	}
	if err := design.ValidateProposal(proposal, validationContext); err != nil {
		return ProposeResult{}, err
	}

	now := s.clock.Now().UTC()
	result := ProposeResult{ScenarioIDs: []int64{}, CaseIDs: []int64{}, CaseVersionIDs: []int64{}}
	created := make([]createdCaseVersion, 0)
	mutated := false

	err = s.repo.WithRequirementLock(ctx, versionID, in.RequirementID, func(ctx context.Context, tx design.Tx) error {
		snapshot, err := tx.RequirementSnapshot(ctx, versionID, in.RequirementID, in.RequirementVersionID)
		if err != nil {
			return err
		}
		if baseHash := strings.TrimSpace(in.BaseGraphHash); baseHash != "" {
			lockedHash, err := tx.CurrentGraphHash(ctx)
			if err != nil {
				return err
			}
			if lockedHash != baseHash {
				return platform.StateConflict("the design graph changed since it was read; re-read the graph and retry")
			}
		}
		scenarioByID := make(map[int64]design.Scenario, len(snapshot.Scenarios))
		scenarioByKey := make(map[string]design.Scenario, len(snapshot.Scenarios))
		for _, scenario := range snapshot.Scenarios {
			scenarioByID[scenario.ID] = scenario
			scenarioByKey[design.ScenarioNaturalKey(scenario.RequirementVersionID, scenario.Name)] = scenario
		}
		caseByID := make(map[int64]design.CaseWithCurrent, len(snapshot.Cases))
		caseByKey := make(map[string]design.CaseWithCurrent, len(snapshot.Cases))
		for _, item := range snapshot.Cases {
			caseByID[item.Case.ID] = item
			if item.Current != nil {
				caseByKey[design.CaseNaturalKey(item.Case.ScenarioID, item.Current.Title)] = item
			}
		}
		if err := checkAccumulatedLimits(in, snapshot, scenarioByKey, caseByKey); err != nil {
			return err
		}

		scenarioIDs := make(map[string]int64, len(in.Scenarios))
		for index, scenarioInput := range in.Scenarios {
			field := fmt.Sprintf("scenarios[%d]", index)
			existing, found := design.Scenario{}, false
			if scenarioInput.ID != nil {
				existing, found = scenarioByID[*scenarioInput.ID]
				if !found {
					return platform.Invalid(field+".id", "scenario does not exist in this requirement version")
				}
			} else {
				existing, found = scenarioByKey[design.ScenarioNaturalKey(in.RequirementVersionID, scenarioInput.Name)]
			}
			if found {
				updated := existing
				updated.Name = strings.TrimSpace(scenarioInput.Name)
				updated.Description = scenarioInput.Description
				updated.VersionSUTIDs = scenarioInput.VersionSUTIDs
				updated.PageIDs = scenarioInput.PageIDs
				updated.APIIDs = scenarioInput.APIIDs
				updated.SortOrder = scenarioInput.SortOrder
				updated.UpdatedAt = now
				if !sameScenarioContent(existing, updated) {
					if _, err := tx.UpdateScenario(ctx, updated); err != nil {
						return err
					}
					mutated = true
				}
				scenarioIDs[scenarioInput.Ref] = existing.ID
				result.ScenarioIDs = append(result.ScenarioIDs, existing.ID)
				continue
			}
			scenario := design.Scenario{
				ID:                   s.ids.Next(),
				VersionID:            versionID,
				RequirementID:        in.RequirementID,
				RequirementVersionID: in.RequirementVersionID,
				Name:                 strings.TrimSpace(scenarioInput.Name),
				Description:          scenarioInput.Description,
				VersionSUTIDs:        scenarioInput.VersionSUTIDs,
				PageIDs:              scenarioInput.PageIDs,
				APIIDs:               scenarioInput.APIIDs,
				SortOrder:            scenarioInput.SortOrder,
				CreatedAt:            now,
				UpdatedAt:            now,
			}
			scenario = withActorAudit(scenario, actorID, runID)
			stored, err := tx.InsertScenario(ctx, scenario)
			if err != nil {
				return err
			}
			scenarioIDs[scenarioInput.Ref] = stored.ID
			result.ScenarioIDs = append(result.ScenarioIDs, stored.ID)
			mutated = true
		}

		for index, caseInput := range in.Cases {
			field := fmt.Sprintf("cases[%d]", index)
			scenarioID, ok := scenarioIDs[caseInput.ScenarioRef]
			if !ok {
				return platform.Invalid(field+".scenario_ref", "scenario ref was not resolved")
			}
			existing, found := design.CaseWithCurrent{}, false
			if caseInput.ID != nil {
				existing, found = caseByID[*caseInput.ID]
				if !found {
					return platform.Invalid(field+".id", "case does not exist in this requirement")
				}
				if existing.Case.ScenarioID != scenarioID {
					return platform.Invalid(field+".scenario_ref", "an existing case cannot move to another scenario within the same requirement version")
				}
			} else {
				existing, found = caseByKey[design.CaseNaturalKey(scenarioID, caseInput.Title)]
			}
			if found && existing.Current != nil {
				next, changed, err := s.buildCaseVersion(caseInput, existing.Case.ID, existing.Current, now, actorID, runID)
				if err != nil {
					return err
				}
				if !changed {
					result.CaseIDs = append(result.CaseIDs, existing.Case.ID)
					result.CaseVersionIDs = append(result.CaseVersionIDs, existing.Current.ID)
					continue
				}
				if _, err := tx.InsertCaseVersion(ctx, next); err != nil {
					return err
				}
				if err := tx.SetCurrentVersion(ctx, existing.Case.ID, next.ID, design.CaseStatusDraft, now); err != nil {
					return err
				}
				result.CaseIDs = append(result.CaseIDs, existing.Case.ID)
				result.CaseVersionIDs = append(result.CaseVersionIDs, next.ID)
				created = append(created, createdCaseVersion{
					caseID: existing.Case.ID, caseVersionID: next.ID, revision: next.Revision, created: false,
				})
				mutated = true
				continue
			}

			caseID := s.ids.Next()
			entity := design.CaseEntity{
				ID: caseID, ScenarioID: scenarioID, Status: design.CaseStatusDraft,
				CreatedAt: now, UpdatedAt: now,
			}
			if actorID > 0 {
				entity.CreatedBy = &actorID
			}
			if runID > 0 {
				entity.CreatedByRunID = &runID
			}
			if err := tx.InsertCase(ctx, entity); err != nil {
				return err
			}
			version, _, err := s.buildCaseVersion(caseInput, caseID, nil, now, actorID, runID)
			if err != nil {
				return err
			}
			if _, err := tx.InsertCaseVersion(ctx, version); err != nil {
				return err
			}
			if err := tx.SetCurrentVersion(ctx, caseID, version.ID, design.CaseStatusDraft, now); err != nil {
				return err
			}
			result.CaseIDs = append(result.CaseIDs, caseID)
			result.CaseVersionIDs = append(result.CaseVersionIDs, version.ID)
			created = append(created, createdCaseVersion{
				caseID: caseID, caseVersionID: version.ID, revision: version.Revision, created: true,
			})
			mutated = true
		}
		return nil
	})
	if err != nil {
		return ProposeResult{}, mapDesignError(err, "requirement")
	}

	updatedGraph, err := s.repo.ReadGraph(ctx, versionID)
	if err != nil {
		return ProposeResult{}, mapDesignError(err, "version")
	}
	result.GraphHash, err = design.ComputeGraphHash(updatedGraph.Requirements)
	if err != nil {
		return ProposeResult{}, err
	}
	if mutated {
		s.publishGraphDelta(ctx, versionID, in.RequirementID, in.RequirementVersionID,
			result.ScenarioIDs, result.CaseIDs, result.CaseVersionIDs, "propose", actorID)
		for _, version := range created {
			s.publishCaseVersionCreated(ctx, versionID, in.RequirementID, version, actorID)
		}
		s.recordAudit(ctx, in.RequirementID, result.CaseIDs, created, actorID)
	}
	return result, nil
}

// buildCaseVersion maps one proposal case onto a CaseVersion. When current is
// nil a revision-1 version is built; otherwise revision is current.Revision+1
// and changed reports whether the content hash actually moved.
func (s *GraphService) buildCaseVersion(in CaseInput, caseID int64, current *design.CaseVersion, now time.Time, actorID, runID int64) (design.CaseVersion, bool, error) {
	revision := 1
	if current != nil {
		revision = current.Revision + 1
	}
	version := design.CaseVersion{
		ID:                  s.ids.Next(),
		CaseID:              caseID,
		Revision:            revision,
		Title:               strings.TrimSpace(in.Title),
		Description:         in.Description,
		Preconditions:       in.Preconditions,
		TestData:            in.TestData,
		Priority:            in.Priority,
		DefaultVersionSUTID: in.DefaultVersionSUTID,
		VersionSUTIDs:       in.VersionSUTIDs,
		PageIDs:             in.PageIDs,
		APIIDs:              in.APIIDs,
		Status:              design.CaseStatusDraft,
		CreatedAt:           now,
		UpdatedAt:           now,
		Steps:               make([]design.CaseStep, 0, len(in.Steps)),
		Assertions:          make([]design.CaseAssertion, 0, len(in.Assertions)),
		Sources:             make([]design.CaseSource, 0, len(in.Sources)),
	}
	if actorID > 0 {
		version.CreatedBy = &actorID
	}
	if runID > 0 {
		version.CreatedByRunID = &runID
	}
	for _, step := range in.Steps {
		version.Steps = append(version.Steps, design.CaseStep{
			ID: s.ids.Next(), Seq: step.Seq, Action: step.Action, Expected: step.Expected,
			TargetVersionSUTID: step.TargetVersionSUTID, TargetPageID: step.TargetPageID,
			TargetAPIID: step.TargetAPIID, AccountRole: step.AccountRole, Data: step.Data,
			CreatedAt: now,
		})
	}
	for _, assertion := range in.Assertions {
		version.Assertions = append(version.Assertions, design.CaseAssertion{
			ID: s.ids.Next(), StepSeq: assertion.StepSeq, Kind: assertion.Kind,
			Target: assertion.Target, Expected: assertion.Expected,
			TargetVersionSUTID: assertion.TargetVersionSUTID, TargetAPIID: assertion.TargetAPIID,
			DatabaseID: assertion.DatabaseID, CreatedAt: now,
		})
	}
	for _, source := range in.Sources {
		version.Sources = append(version.Sources, design.CaseSource{
			ID: s.ids.Next(), Kind: source.Kind, RefID: source.RefID,
			Path: source.Path, Line: source.Line, SHA: source.SHA, CreatedAt: now,
		})
	}
	hash, err := design.ComputeCaseContentHash(version)
	if err != nil {
		return design.CaseVersion{}, false, err
	}
	version.ContentHash = hash
	if current != nil && current.ContentHash == hash {
		return version, false, nil
	}
	return version, true, nil
}

func sameScenarioContent(left, right design.Scenario) bool {
	return left.Name == right.Name &&
		left.Description == right.Description &&
		left.SortOrder == right.SortOrder &&
		equalInt64Sets(left.VersionSUTIDs, right.VersionSUTIDs) &&
		equalInt64Sets(left.PageIDs, right.PageIDs) &&
		equalInt64Sets(left.APIIDs, right.APIIDs)
}

func equalInt64Sets(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy := append([]int64(nil), left...)
	rightCopy := append([]int64(nil), right...)
	sort.Slice(leftCopy, func(i, j int) bool { return leftCopy[i] < leftCopy[j] })
	sort.Slice(rightCopy, func(i, j int) bool { return rightCopy[i] < rightCopy[j] })
	for index := range leftCopy {
		if leftCopy[index] != rightCopy[index] {
			return false
		}
	}
	return true
}

func withActorAudit(scenario design.Scenario, actorID, runID int64) design.Scenario {
	if actorID > 0 {
		scenario.CreatedBy = &actorID
	}
	if runID > 0 {
		scenario.CreatedByRunID = &runID
	}
	return scenario
}

func caseVersionDetailView(version design.CaseVersion) CaseVersionDetailView {
	view := CaseVersionDetailView{
		CaseVersionID:       version.ID,
		Revision:            version.Revision,
		Title:               version.Title,
		Description:         version.Description,
		Preconditions:       version.Preconditions,
		TestData:            version.TestData,
		Priority:            version.Priority,
		DefaultVersionSUTID: version.DefaultVersionSUTID,
		VersionSUTIDs:       version.VersionSUTIDs,
		PageIDs:             version.PageIDs,
		APIIDs:              version.APIIDs,
		Status:              version.Status,
		ContentHash:         version.ContentHash,
		CreatedAt:           utcNow(version.CreatedAt),
		Steps:               make([]CaseStepView, 0, len(version.Steps)),
		Assertions:          make([]CaseAssertionView, 0, len(version.Assertions)),
		Sources:             make([]CaseSourceView, 0, len(version.Sources)),
	}
	for _, step := range version.Steps {
		view.Steps = append(view.Steps, CaseStepView{
			Seq: step.Seq, Action: step.Action, Expected: step.Expected,
			TargetVersionSUTID: step.TargetVersionSUTID, TargetPageID: step.TargetPageID,
			TargetAPIID: step.TargetAPIID, AccountRole: step.AccountRole, Data: step.Data,
		})
	}
	for _, assertion := range version.Assertions {
		view.Assertions = append(view.Assertions, CaseAssertionView{
			StepSeq: assertion.StepSeq, Kind: assertion.Kind, Target: assertion.Target,
			Expected: assertion.Expected, TargetVersionSUTID: assertion.TargetVersionSUTID,
			TargetAPIID: assertion.TargetAPIID, DatabaseID: assertion.DatabaseID,
		})
	}
	for _, source := range version.Sources {
		view.Sources = append(view.Sources, CaseSourceView{
			Kind: source.Kind, RefID: source.RefID, Path: source.Path, Line: source.Line, SHA: source.SHA,
		})
	}
	return view
}

func (s *GraphService) publishGraphDelta(ctx context.Context, versionID, requirementID, requirementVersionID int64, scenarioIDs, caseIDs, caseVersionIDs []int64, op string, actorID int64) {
	if s.events == nil {
		return
	}
	payload := map[string]any{
		"requirement_id":         platformid.Format(requirementID),
		"requirement_version_id": platformid.Format(requirementVersionID),
		"scenario_ids":           formatIDList(scenarioIDs),
		"case_ids":               formatIDList(caseIDs),
		"case_version_ids":       formatIDList(caseVersionIDs),
		"op":                     op,
	}
	s.publishEvent(ctx, versionID, "design.graph_delta", requirementID, payload, actorID)
}

func (s *GraphService) publishCaseVersionCreated(ctx context.Context, versionID, requirementID int64, created createdCaseVersion, actorID int64) {
	if s.events == nil {
		return
	}
	payload := map[string]any{
		"requirement_id":  platformid.Format(requirementID),
		"case_id":         platformid.Format(created.caseID),
		"case_version_id": platformid.Format(created.caseVersionID),
		"revision":        created.revision,
		"status":          design.CaseStatusDraft,
	}
	s.publishEvent(ctx, versionID, "design.case_version_created", requirementID, payload, actorID)
}

func (s *GraphService) publishEvent(ctx context.Context, versionID int64, eventType string, requirementID int64, payload map[string]any, actorID int64) {
	if s.events == nil {
		return
	}
	scope := map[string]any{
		"version_id":     platformid.Format(versionID),
		"requirement_id": platformid.Format(requirementID),
	}
	event := eventingapp.Event{
		Channel:   "version:" + platformid.Format(versionID),
		Type:      eventType,
		Scope:     scope,
		Payload:   payload,
		RequestID: requestid.From(ctx),
	}
	if actorID > 0 {
		actor := actorID
		event.ActorID = &actor
	}
	_ = s.events.Publish(ctx, event)
}

func (s *GraphService) recordAudit(ctx context.Context, requirementID int64, caseIDs []int64, created []createdCaseVersion, actorID int64) {
	if s.audit == nil {
		return
	}
	action := "case.updated"
	auditCaseID := caseIDs[len(caseIDs)-1]
	auditVersionID := ""
	for _, version := range created {
		if version.created {
			action = "case.created"
		}
		auditCaseID = version.caseID
		auditVersionID = platformid.Format(version.caseVersionID)
	}
	event := auditapp.Event{
		Action:     action,
		TargetKind: "case",
		TargetID:   platformid.Format(auditCaseID),
		RequestID:  requestid.From(ctx),
		Metadata: map[string]any{
			"requirement_id":  platformid.Format(requirementID),
			"case_id":         platformid.Format(auditCaseID),
			"case_version_id": auditVersionID,
		},
	}
	if actorID > 0 {
		actor := actorID
		event.ActorID = &actor
	}
	_ = s.audit.Record(ctx, event)
}

func formatIDList(ids []int64) []string {
	values := make([]string, 0, len(ids))
	for _, id := range ids {
		values = append(values, platformid.Format(id))
	}
	return values
}

// checkAccumulatedLimits enforces the per-requirement version caps (P2-D-18)
// inside the proposal transaction. Only genuinely new scenarios/cases count;
// updates addressed by ID or natural key do not consume additional slots.
func checkAccumulatedLimits(
	in ProposeInput,
	snapshot design.RequirementSnapshot,
	scenarioByKey map[string]design.Scenario,
	caseByKey map[string]design.CaseWithCurrent,
) error {
	limits := design.DefaultLimits.Effective()
	newScenarios := 0
	scenarioIDsByRef := make(map[string]int64, len(in.Scenarios))
	for _, scenarioInput := range in.Scenarios {
		var existing design.Scenario
		found := false
		if scenarioInput.ID != nil {
			for _, scenario := range snapshot.Scenarios {
				if scenario.ID == *scenarioInput.ID {
					existing, found = scenario, true
					break
				}
			}
		} else {
			existing, found = scenarioByKey[design.ScenarioNaturalKey(in.RequirementVersionID, scenarioInput.Name)]
		}
		if found {
			scenarioIDsByRef[scenarioInput.Ref] = existing.ID
		} else {
			newScenarios++
		}
	}
	newCases := 0
	for _, caseInput := range in.Cases {
		if caseInput.ID != nil {
			continue
		}
		scenarioID, existingScenario := scenarioIDsByRef[caseInput.ScenarioRef]
		if existingScenario {
			if _, found := caseByKey[design.CaseNaturalKey(scenarioID, caseInput.Title)]; found {
				continue
			}
		}
		newCases++
	}
	if len(snapshot.Scenarios)+newScenarios > limits.MaxScenariosPerRequirement {
		return platform.StateConflict("the requirement has reached its scenario limit")
	}
	if len(snapshot.Cases)+newCases > limits.MaxCasesPerRequirement {
		return platform.StateConflict("the requirement has reached its case limit")
	}
	return nil
}
