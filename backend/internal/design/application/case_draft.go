package designapp

// Draft-only case augmentation. The exploration analysis (spec §11 capability
// #6) proposes draft cases from recorded requirement gaps; those drafts flow
// through this design-side entry point so every write goes through the existing
// case.graph_propose service. The design context never imports the agent
// exploration package: the composition root adapts explore.CaseDraft onto
// AugmentedCaseDraft.
//
// Hard boundary (spec §11, D25): this entry point only ever creates draft
// cases. It has no confirm/execute/requirement-mutation path: it calls Propose
// (which always writes draft case versions) and returns the created
// ids/status. A separate service (W2-11 discovery revision) may auto-confirm a
// discovery_delta draft through the normal confirmation path; this draft-only
// entry point never confirms anything itself.

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
//
// v0.2.4-B (§3.2): the step target is prose. target_version_sut_id /
// target_page_id / target_api_id are gone.
type AugmentedCaseDraftStep struct {
	Seq               int
	Action            string
	Expected          string
	TargetDescription string
}

// AugmentedCaseDraftAssertion is one proposed assertion of an augmentation
// draft. SUTID is only meaningful (and only required) for kind=db.
type AugmentedCaseDraftAssertion struct {
	StepSeq  int
	Kind     string
	Target   string
	Expected string
	SUTID    *int64
}

// AugmentedCaseDraft is one draft case produced by the requirement gap analysis
// or by the discovery revision service. It references the recorded gap fact ids
// so the design side can trace the draft back to the exploration finding.
//
// v0.2.4-B (§5.2): SutDescription, Preconditions and CoveredACs are required
// contract elements -- a caller that cannot state them honestly must not create
// a draft, because the coverage matrix the judge and the union pre-check read is
// built from CoveredACs and a fabricated value would hide a real gap.
type AugmentedCaseDraft struct {
	RequirementID        int64
	RequirementVersionID int64
	Title                string
	Description          string
	SutDescription       string
	Preconditions        string
	CoveredACs           []string
	Priority             string
	// DiscoveryKind/DiscoveryID name the approved catalog resource a
	// discovery-driven draft was generated from ("page"/"api" plus its id).
	// Under the v0.2.4-B contract they are PROVENANCE ONLY: the id is recorded
	// as a page/api case_source and the resource label becomes the step's
	// target_description. They are not a scope binding -- design no longer
	// resolves resources against a version scope (§5.2). An exploration gap
	// draft leaves them zero.
	DiscoveryKind string
	DiscoveryID   int64
	// SourceObservationID is the execution discovery observation that
	// authorized the draft. When set, the draft carries a knowledge source
	// "discovery_observation:<id>" and the version records the observation id.
	SourceObservationID *int64
	Steps               []AugmentedCaseDraftStep
	Assertions          []AugmentedCaseDraftAssertion
	SourceFactIDs       []int64
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
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return AugmentedCaseDraftResult{}, platform.Invalid("title", "is required")
	}
	if len([]rune(title)) > maxAugmentedDraftTitleLength {
		return AugmentedCaseDraftResult{}, platform.Invalid("title", "is too long")
	}
	// The v0.2.4-B case contract (§5.2) is checked here as well as inside
	// Propose, so a caller that cannot state the system under test, its
	// preconditions or the acceptance criteria it covers gets a 400 naming its
	// own field instead of an opaque "cases[0].*" from the proposal gate.
	if sutDescription := strings.TrimSpace(in.SutDescription); sutDescription == "" {
		return AugmentedCaseDraftResult{}, platform.Invalid("sut_description", "is required")
	} else if len([]rune(sutDescription)) > design.CaseSutDescriptionMax {
		return AugmentedCaseDraftResult{}, platform.Invalid("sut_description", "is too long")
	}
	if strings.TrimSpace(in.Preconditions) == "" {
		return AugmentedCaseDraftResult{}, platform.Invalid("preconditions", `is required; state "无" when the case needs no setup`)
	}
	if len(design.NormalizeACIDSet(in.CoveredACs)) == 0 {
		return AugmentedCaseDraftResult{}, platform.Invalid("covered_acs", "must cover at least one acceptance criterion of the requirement")
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
		Ref:         "augmented",
		Name:        AugmentedCaseScenarioName,
		Description: "由探索缺口分析自动生成的用例草案，等待人工确认",
		SortOrder:   100,
	}
	// Reuse the existing augmentation scenario when it exists: mirroring its
	// stored fields makes Propose a no-op for the scenario instead of rewriting
	// a scenario the user may have edited.
	if existing, ok := s.augmentedScenario(ctx, versionID, in.RequirementID, in.RequirementVersionID); ok {
		scenario.ID = &existing.ID
		scenario.Name = existing.Name
		scenario.Description = existing.Description
		scenario.SortOrder = existing.SortOrder
	}

	steps := make([]StepInput, 0, len(in.Steps))
	for _, step := range in.Steps {
		steps = append(steps, StepInput{
			Seq:               step.Seq,
			Action:            step.Action,
			Expected:          step.Expected,
			TargetDescription: step.TargetDescription,
		})
	}
	assertions := make([]AssertionInput, 0, len(in.Assertions))
	for _, assertion := range in.Assertions {
		assertions = append(assertions, AssertionInput{
			StepSeq:  assertion.StepSeq,
			Kind:     assertion.Kind,
			Target:   assertion.Target,
			Expected: assertion.Expected,
			SUTID:    assertion.SUTID,
		})
	}
	sources := make([]SourceInput, 0, len(in.SourceFactIDs)+2)
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
	// The additive draft records why it exists: the requirement version text it
	// extends, the approved discovered target, and the execution observation.
	sources = append(sources, SourceInput{
		Kind:  design.SourceRequirement,
		RefID: strconv.FormatInt(in.RequirementVersionID, 10),
		Path:  "requirement",
	})
	if in.DiscoveryID > 0 {
		kind := design.SourcePage
		if in.DiscoveryKind == "api" {
			kind = design.SourceAPI
		}
		sources = append(sources, SourceInput{
			Kind: kind, RefID: strconv.FormatInt(in.DiscoveryID, 10), Path: "discovery_delta",
		})
	}
	if in.SourceObservationID != nil && *in.SourceObservationID > 0 {
		sources = append(sources, SourceInput{
			Kind:  design.SourceKnowledge,
			RefID: strconv.FormatInt(*in.SourceObservationID, 10),
			Path:  "discovery_observation",
		})
	}

	trigger := ""
	if in.SourceObservationID != nil && *in.SourceObservationID > 0 {
		trigger = design.CaseTriggerDiscoveryDelta
	}
	result, err := s.Propose(ctx, versionID, ProposeInput{
		RequirementID:        in.RequirementID,
		RequirementVersionID: in.RequirementVersionID,
		Scenarios:            []ScenarioInput{scenario},
		Cases: []CaseInput{{
			Ref:                 "augmented-case",
			ScenarioRef:         "augmented",
			Title:               title,
			Description:         in.Description,
			SutDescription:      in.SutDescription,
			Preconditions:       in.Preconditions,
			CoveredACs:          in.CoveredACs,
			Priority:            in.Priority,
			Steps:               steps,
			Assertions:          assertions,
			Sources:             sources,
			Trigger:             trigger,
			SourceObservationID: in.SourceObservationID,
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
