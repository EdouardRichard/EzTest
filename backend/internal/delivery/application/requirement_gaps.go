package application

import (
	"context"
	"fmt"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// RequirementGapEscalation is one validated supplement requirement-gap
// escalation (spec §6.4.4/§9.2): the missing requirement coverage reported by
// the supplement agent. It has no case and no code reference; the issue is
// anchored by its requirement refs.
type RequirementGapEscalation struct {
	Title          string
	Detail         string
	RequirementIDs []int64
}

// ApplyRequirementGaps materializes the supplement agent's requirement-gap
// escalations as AI issues (spec §6.4.4): every escalation becomes a
// requirement_gap issue with severity major, medium confidence, the
// deterministic §5.4 requirement-gap dedupe key and the escalated requirement
// refs. It reuses ApplyDrafts, so the hard dedupe, replay semantics, events and
// audit are the same as the triage path; a repeated escalation is a no-op. The
// caller (the autonomous worker) logs a failure and degrades instead of
// blocking the batch chain.
func (s *IssueManager) ApplyRequirementGaps(ctx context.Context, versionID, batchID, actor int64, escalations []RequirementGapEscalation) ([]domain.Issue, error) {
	if len(escalations) == 0 {
		return []domain.Issue{}, nil
	}
	if versionID <= 0 {
		return nil, domain.NewInvalid("versionId", domain.ReasonInvalidArgument,
			"versionId must be a positive id")
	}
	drafts := make([]domain.IssueDraft, 0, len(escalations))
	for index, escalation := range escalations {
		title, err := domain.ValidateTitle(escalation.Title)
		if err != nil {
			return nil, err
		}
		requirementIDs := domain.NormalizeRequirementGapIDs(escalation.RequirementIDs)
		if len(requirementIDs) == 0 {
			return nil, domain.NewInvalid("requirementIds", domain.ReasonInvalidArgument,
				fmt.Sprintf("escalation %d must reference at least one requirement", index+1))
		}
		drafts = append(drafts, domain.IssueDraft{
			DedupeKey:      domain.RequirementGapDedupeKey(versionID, requirementIDs),
			Title:          title,
			Description:    escalation.Detail,
			Severity:       domain.SeverityMajor,
			Confidence:     domain.ConfidenceMedium,
			Category:       domain.CategoryRequirementGap,
			RequirementIDs: requirementIDs,
		})
	}
	return s.ApplyDrafts(ctx, versionID, batchID, actor, drafts)
}
