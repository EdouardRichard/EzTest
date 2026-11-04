package main

// Task 37 fix round 1: production adapter that persists exploration case
// augmentation drafts through the design context. The adapter is deliberately
// draft-only: its only design capability is CreateAugmentedDraftCase (a
// case.graph_propose wrapper), so it has no confirm, execute or
// requirement-mutation path at all.

import (
	"context"
	"errors"

	exploreapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/explore"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/versionadapter"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
)

// p9AugmentedDraftDesigner is the draft-only design-side write surface. Using a
// narrow interface (instead of the whole design service) keeps confirmation,
// execution and requirement mutation out of this adapter by construction.
type p9AugmentedDraftDesigner interface {
	CreateAugmentedDraftCase(ctx context.Context, versionID int64, in designapp.AugmentedCaseDraft) (designapp.AugmentedCaseDraftResult, error)
}

// p9DraftCaseWriter adapts the exploration CaseDraft onto the design draft
// service. The exploration draft carries no version SUT (a gap is requirement
// scoped), so the first SUT bound to the version is used; a version without a
// bound SUT cannot persist a case and fails closed.
type p9DraftCaseWriter struct {
	design   p9AugmentedDraftDesigner
	versions versionadapter.VersionService
}

// newP9DraftCaseWriter wires the adapter. A nil designer/version reader is
// allowed at construction and rejected at use, so a degraded assembly cannot
// panic the analysis loop.
func newP9DraftCaseWriter(design p9AugmentedDraftDesigner, versions versionadapter.VersionService) *p9DraftCaseWriter {
	return &p9DraftCaseWriter{design: design, versions: versions}
}

// CreateDraftCase persists one draft case through the design service and
// reports the draft status back to the augmentation contract.
func (w *p9DraftCaseWriter) CreateDraftCase(ctx context.Context, versionID int64, draft exploreapp.CaseDraft) (exploreapp.CaseDraftResult, error) {
	if w == nil || w.design == nil {
		return exploreapp.CaseDraftResult{}, errors.New("p9 draft writer: design service is not configured")
	}
	versionSUTID, err := w.versionSUTID(ctx, versionID)
	if err != nil {
		return exploreapp.CaseDraftResult{}, err
	}
	steps := make([]designapp.AugmentedCaseDraftStep, 0, len(draft.Steps))
	for _, step := range draft.Steps {
		steps = append(steps, designapp.AugmentedCaseDraftStep{
			Seq: step.Seq, Action: step.Action, Expected: step.Expected,
		})
	}
	assertions := make([]designapp.AugmentedCaseDraftAssertion, 0, len(draft.Assertions))
	for _, assertion := range draft.Assertions {
		assertions = append(assertions, designapp.AugmentedCaseDraftAssertion{
			StepSeq: assertion.StepSeq, Kind: assertion.Kind,
			Target: assertion.Target, Expected: assertion.Expected,
		})
	}
	factIDs := append([]int64(nil), draft.SourceFactIDs...)
	created, err := w.design.CreateAugmentedDraftCase(ctx, versionID, designapp.AugmentedCaseDraft{
		RequirementID:        draft.RequirementID,
		RequirementVersionID: draft.RequirementVersionID,
		VersionSUTID:         versionSUTID,
		Title:                draft.Title,
		Description:          draft.Description,
		Preconditions:        draft.Preconditions,
		Priority:             draft.Priority,
		Steps:                steps,
		Assertions:           assertions,
		SourceFactIDs:        factIDs,
	})
	if err != nil {
		return exploreapp.CaseDraftResult{}, err
	}
	return exploreapp.CaseDraftResult{
		CaseID:        created.CaseID,
		CaseVersionID: created.CaseVersionID,
		Status:        created.Status,
	}, nil
}

// versionSUTID resolves the version SUT an augmented draft belongs to: the
// exploration gap is requirement scoped, so the first binding is the
// deterministic target.
func (w *p9DraftCaseWriter) versionSUTID(ctx context.Context, versionID int64) (int64, error) {
	if w.versions == nil {
		return 0, errors.New("p9 draft writer: version binding reader is not configured")
	}
	if versionID <= 0 {
		return 0, errors.New("p9 draft writer: invalid version id")
	}
	view, err := w.versions.Get(ctx, versionID)
	if err != nil {
		return 0, err
	}
	for _, binding := range view.Bindings {
		if binding.ID > 0 {
			return binding.ID, nil
		}
	}
	return 0, errors.New("p9 draft writer: the version has no bound SUT")
}

var _ exploreapp.DraftCaseWriter = (*p9DraftCaseWriter)(nil)
