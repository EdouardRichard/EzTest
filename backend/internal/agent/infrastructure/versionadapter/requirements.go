package versionadapter

import (
	"context"
	"encoding/json"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

const (
	requirementPageLimit = 100
	requirementMaxRows   = 1000
)

// ListRequirements returns every requirement of the version (id-descending
// pagination is drained), mapped to the tool DTO.
func (a *Adapter) ListRequirements(ctx context.Context, versionID int64) ([]tools.RequirementSummary, error) {
	if versionID <= 0 {
		return nil, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	views, err := a.listAllRequirements(ctx, versionID)
	if err != nil {
		return nil, err
	}
	summaries := make([]tools.RequirementSummary, 0, len(views))
	for _, view := range views {
		summaries = append(summaries, requirementSummary(view))
	}
	return summaries, nil
}

// GetRequirement returns the current revision of one requirement. A requirement
// from another version is reported as not-found by the version service.
func (a *Adapter) GetRequirement(ctx context.Context, versionID, requirementID int64) (tools.RequirementDetail, error) {
	if a.requirements == nil {
		return tools.RequirementDetail{}, a.missing("requirements")
	}
	detail, err := a.requirements.Get(ctx, versionID, requirementID)
	if err != nil {
		return tools.RequirementDetail{}, err
	}
	return requirementDetail(detail), nil
}

// CreateDraft persists a model-proposed draft requirement as actor 0 (system).
func (a *Adapter) CreateDraft(ctx context.Context, versionID int64, draft tools.RequirementDraft) (tools.RequirementSummary, error) {
	if a.requirements == nil {
		return tools.RequirementSummary{}, a.missing("requirements")
	}
	detail, err := a.requirements.Create(ctx, versionID, versionDraft(draft), 0)
	if err != nil {
		return tools.RequirementSummary{}, err
	}
	return requirementSummary(detail.RequirementView), nil
}

// UpdateDraft creates a new draft revision (clarified requirements reopen as
// draft). Only the version service decides state conflicts.
func (a *Adapter) UpdateDraft(ctx context.Context, versionID, requirementID int64, draft tools.RequirementDraft) (tools.RequirementSummary, error) {
	if a.requirements == nil {
		return tools.RequirementSummary{}, a.missing("requirements")
	}
	detail, err := a.requirements.Update(ctx, versionID, requirementID, versionDraft(draft), "agent draft update", 0)
	if err != nil {
		return tools.RequirementSummary{}, err
	}
	return requirementSummary(detail.RequirementView), nil
}

// ProposeRelation stores a model-proposed relation candidate.
func (a *Adapter) ProposeRelation(ctx context.Context, versionID, fromID, toID int64, kind, description string) error {
	if a.requirements == nil {
		return a.missing("requirements")
	}
	_, err := a.requirements.CreateRelation(ctx, versionID, versionapp.RelationInput{
		FromRequirementID: fromID,
		ToRequirementID:   toID,
		Kind:              kind,
		Description:       description,
	}, 0)
	return err
}

// listAllRequirements drains the id-descending requirement pages. The cap keeps
// a corrupt cursor from looping forever; P1 versions are small.
func (a *Adapter) listAllRequirements(ctx context.Context, versionID int64) ([]versionapp.RequirementView, error) {
	if a.requirements == nil {
		return nil, a.missing("requirements")
	}
	all := make([]versionapp.RequirementView, 0)
	var before *int64
	for len(all) < requirementMaxRows {
		views, hasMore, err := a.requirements.List(ctx, versionID, "", requirementPageLimit, before)
		if err != nil {
			return nil, err
		}
		all = append(all, views...)
		if !hasMore || len(views) == 0 {
			break
		}
		last := views[len(views)-1].ID
		before = &last
	}
	return all, nil
}

func requirementSummary(view versionapp.RequirementView) tools.RequirementSummary {
	return tools.RequirementSummary{
		ID:               view.ID,
		CurrentVersionID: view.CurrentVersionID,
		Revision:         view.Revision,
		Title:            view.Title,
		Status:           view.Status,
		Priority:         view.Priority,
	}
}

func requirementDetail(detail versionapp.RequirementDetail) tools.RequirementDetail {
	constraints := detail.CurrentVersion.Constraints
	if len(constraints) == 0 {
		constraints = json.RawMessage(`{}`)
	}
	criteria := make([]tools.RequirementAcceptanceCriterion, 0, len(detail.CurrentVersion.AcceptanceCriteria))
	for _, criterion := range detail.CurrentVersion.AcceptanceCriteria {
		criteria = append(criteria, tools.RequirementAcceptanceCriterion{
			ID:    criterion.ID,
			Given: criterion.Given,
			When:  criterion.When,
			Then:  criterion.Then,
		})
	}
	return tools.RequirementDetail{
		RequirementSummary: requirementSummary(detail.RequirementView),
		UserStory: tools.RequirementUserStory{
			AsA:    detail.CurrentVersion.UserStory.AsA,
			IWant:  detail.CurrentVersion.UserStory.IWant,
			SoThat: detail.CurrentVersion.UserStory.SoThat,
		},
		Rules:              detail.CurrentVersion.Rules,
		AcceptanceCriteria: criteria,
		Constraints:        constraints,
		SutDescription:     detail.CurrentVersion.SutDescription,
		ScopeOut:           detail.CurrentVersion.ScopeOut,
		AccountRolesHint:   detail.CurrentVersion.AccountRolesHint,
	}
}

func versionDraft(draft tools.RequirementDraft) versionapp.RequirementDraft {
	constraints := draft.Constraints
	if len(constraints) == 0 {
		constraints = json.RawMessage(`{}`)
	}
	criteria := make([]versionapp.RequirementAcceptanceCriterionInput, 0, len(draft.AcceptanceCriteria))
	for _, criterion := range draft.AcceptanceCriteria {
		criteria = append(criteria, versionapp.RequirementAcceptanceCriterionInput{
			Given: criterion.Given,
			When:  criterion.When,
			Then:  criterion.Then,
		})
	}
	return versionapp.RequirementDraft{
		Title: draft.Title,
		UserStory: versionapp.RequirementUserStoryView{
			AsA:    draft.UserStory.AsA,
			IWant:  draft.UserStory.IWant,
			SoThat: draft.UserStory.SoThat,
		},
		Rules:              draft.Rules,
		AcceptanceCriteria: criteria,
		Priority:           draft.Priority,
		Constraints:        constraints,
		SutDescription:     draft.SutDescription,
		ScopeOut:           draft.ScopeOut,
		AccountRolesHint:   draft.AccountRolesHint,
	}
}
