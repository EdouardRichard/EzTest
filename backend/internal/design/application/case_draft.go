package designapp

// Draft-only case augmentation. The exploration analysis (spec §11 capability
// #6) proposes draft cases from recorded requirement gaps; those drafts flow
// through this design-side entry point so every write goes through the existing
// case.graph_propose service. The design context never imports the agent
// exploration package: the composition root adapts explore.CaseDraft onto
// AugmentedCaseDraft.
//
// Hard boundary (spec §11, D25): augmentation only ever creates draft cases.
// This entry point has no confirm/execute/requirement-mutation path: it calls
// Propose (which always writes draft case versions) and returns the created
// ids/status. Confirmation stays a human action.

import (
	"context"
	"strconv"
	"strings"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// AugmentedCaseScenarioName is the single scenario that groups augmentation
// drafts inside a requirement version. Reusing one scenario keeps automatic
// augmentation from inflating the requirement's scenario count.
const AugmentedCaseScenarioName = "探索补用例"

// Case augmentation bounds. One draft stays inside the P2 proposal limits.
const (
	maxAugmentedDraftSteps       = 10
	maxAugmentedDraftAssertions  = 10
	maxAugmentedDraftSources     = 20
	maxAugmentedDraftTitleLength = design.CaseTitleMax
)

// AugmentedCaseDraftStep is one proposed step of an augmentation draft.
type AugmentedCaseDraftStep struct {
	Seq      int
	Action   string
	Expected string
}

// AugmentedCaseDraftAssertion is one proposed assertion of an augmentation
// draft.
type AugmentedCaseDraftAssertion struct {
	StepSeq  int
	Kind     string
	Target   string
	Expected string
}

// AugmentedCaseDraft is one draft case produced by the requirement gap analysis.
// It references the recorded gap fact ids so the design side can trace the
// draft back to the exploration finding.
type AugmentedCaseDraft struct {
	RequirementID        int64
	RequirementVersionID int64
	VersionSUTID         int64
	Title                string
	Description          string
	Preconditions        string
	Priority             string
	Steps                []AugmentedCaseDraftStep
	Assertions           []AugmentedCaseDraftAssertion
	SourceFactIDs        []int64
}

// AugmentedCaseDraftResult is the persisted result of one augmentation draft.
// Status is always draft.
type AugmentedCaseDraftResult struct {
	CaseID        int64
	CaseVersionID int64
	Status        string
}

// CreateAugmentedDraftCase persists one augmentation draft as a draft case
// version through case.graph_propose. It reuses the requirement's single
// "探索补用例" scenario when present, never confirms anything and never mutates a
// requirement. An identical draft is idempotent: Propose resolves the natural
// key and returns the existing case/version.
func (s *GraphService) CreateAugmentedDraftCase(ctx context.Context, versionID int64, in AugmentedCaseDraft) (AugmentedCaseDraftResult, error) {
	if versionID <= 0 {
		return AugmentedCaseDraftResult{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if in.RequirementID <= 0 || in.RequirementVersionID <= 0 {
		return AugmentedCaseDraftResult{}, platform.Invalid("requirement_id", "requirement and requirement version are required")
	}
	if in.VersionSUTID <= 0 {
		return AugmentedCaseDraftResult{}, platform.Invalid("version_sut_id", "must be a positive decimal ID")
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return AugmentedCaseDraftResult{}, platform.Invalid("title", "is required")
	}
	if len([]rune(title)) > maxAugmentedDraftTitleLength {
		return AugmentedCaseDraftResult{}, platform.Invalid("title", "is too long")
	}
	if len(in.Steps) == 0 || len(in.Steps) > maxAugmentedDraftSteps {
		return AugmentedCaseDraftResult{}, platform.Invalid("steps", "must contain 1 to 10 steps")
	}
	if len(in.Assertions) > maxAugmentedDraftAssertions {
		return AugmentedCaseDraftResult{}, platform.Invalid("assertions", "may contain at most 10 assertions")
	}
	if len(in.SourceFactIDs) > maxAugmentedDraftSources {
		in.SourceFactIDs = in.SourceFactIDs[:maxAugmentedDraftSources]
	}

	scenario := ScenarioInput{
		Ref:           "augmented",
		Name:          AugmentedCaseScenarioName,
		Description:   "由探索缺口分析自动生成的用例草案，等待人工确认",
		VersionSUTIDs: []int64{in.VersionSUTID},
		SortOrder:     100,
	}
	// Reuse the existing augmentation scenario when it exists: mirroring its
	// stored fields makes Propose a no-op for the scenario instead of rewriting
	// a scenario the user may have edited.
	if existing, ok := s.augmentedScenario(ctx, versionID, in.RequirementID, in.RequirementVersionID); ok {
		scenario.ID = &existing.ID
		scenario.Name = existing.Name
		scenario.Description = existing.Description
		scenario.VersionSUTIDs = append([]int64(nil), existing.VersionSUTIDs...)
		scenario.PageIDs = append([]int64(nil), existing.PageIDs...)
		scenario.APIIDs = append([]int64(nil), existing.APIIDs...)
		scenario.SortOrder = existing.SortOrder
	}

	steps := make([]StepInput, 0, len(in.Steps))
	for _, step := range in.Steps {
		steps = append(steps, StepInput{
			Seq:      step.Seq,
			Action:   step.Action,
			Expected: step.Expected,
		})
	}
	assertions := make([]AssertionInput, 0, len(in.Assertions))
	for _, assertion := range in.Assertions {
		assertions = append(assertions, AssertionInput{
			StepSeq:  assertion.StepSeq,
			Kind:     assertion.Kind,
			Target:   assertion.Target,
			Expected: assertion.Expected,
		})
	}
	sources := make([]SourceInput, 0, len(in.SourceFactIDs))
	seen := make(map[int64]bool, len(in.SourceFactIDs))
	for _, factID := range in.SourceFactIDs {
		if factID <= 0 || seen[factID] {
			continue
		}
		seen[factID] = true
		sources = append(sources, SourceInput{
			Kind:  design.SourceKnowledge,
			RefID: strconv.FormatInt(factID, 10),
			Path:  "exploration_gap",
		})
	}

	versionSUTID := in.VersionSUTID
	result, err := s.Propose(ctx, versionID, ProposeInput{
		RequirementID:        in.RequirementID,
		RequirementVersionID: in.RequirementVersionID,
		Scenarios:            []ScenarioInput{scenario},
		Cases: []CaseInput{{
			Ref:                 "augmented-case",
			ScenarioRef:         "augmented",
			Title:               title,
			Description:         in.Description,
			Preconditions:       in.Preconditions,
			Priority:            in.Priority,
			DefaultVersionSUTID: &versionSUTID,
			VersionSUTIDs:       []int64{in.VersionSUTID},
			Steps:               steps,
			Assertions:          assertions,
			Sources:             sources,
		}},
	}, 0, 0)
	if err != nil {
		return AugmentedCaseDraftResult{}, err
	}
	if len(result.CaseIDs) == 0 || len(result.CaseVersionIDs) == 0 {
		return AugmentedCaseDraftResult{}, platform.NewError(500, "internal", "case augmentation produced no draft case")
	}
	return AugmentedCaseDraftResult{
		CaseID:        result.CaseIDs[0],
		CaseVersionID: result.CaseVersionIDs[0],
		Status:        string(design.CaseStatusDraft),
	}, nil
}

// augmentedScenario finds the single augmentation scenario of the requirement
// version. A read failure degrades to "no scenario" (Propose then creates it),
// so a transient read error never fails the draft.
func (s *GraphService) augmentedScenario(ctx context.Context, versionID, requirementID, requirementVersionID int64) (ScenarioGraphView, bool) {
	if s == nil || s.repo == nil {
		return ScenarioGraphView{}, false
	}
	graph, err := s.ReadGraph(ctx, versionID)
	if err != nil {
		return ScenarioGraphView{}, false
	}
	for _, requirement := range graph.Requirements {
		if requirement.RequirementID != requirementID || requirement.RequirementVersionID != requirementVersionID {
			continue
		}
		for _, scenario := range requirement.Scenarios {
			if strings.TrimSpace(scenario.Name) == AugmentedCaseScenarioName {
				return scenario, true
			}
		}
	}
	return ScenarioGraphView{}, false
}
