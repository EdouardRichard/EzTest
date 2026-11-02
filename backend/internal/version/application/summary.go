package versionapp

import (
	"context"
	"strings"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Summary returns the latest thread memory of one conversation. A conversation
// without a generated summary returns a zero-value view (Version 0).
func (s *ConversationService) Summary(ctx context.Context, conversationID int64) (SummaryView, error) {
	if conversationID <= 0 {
		return SummaryView{}, platform.Invalid("cid", "must be a positive decimal ID")
	}
	if _, err := s.repo.GetByID(ctx, conversationID); err != nil {
		return SummaryView{}, mapConversationRepositoryError(err)
	}
	summary, err := s.repo.Summary(ctx, conversationID)
	if err != nil {
		return SummaryView{}, mapConversationRepositoryError(err)
	}
	if summary == nil {
		return SummaryView{ConversationID: conversationID, PinnedFacts: []string{}}, nil
	}
	return summaryView(*summary), nil
}

// UpdateSummary writes a new summary revision (version+1) and publishes
// conversation.summary_updated. Pinned facts are capped at 20 entries of 500
// characters each.
func (s *ConversationService) UpdateSummary(ctx context.Context, conversationID int64, summary string, pinned []string) error {
	if conversationID <= 0 {
		return platform.Invalid("cid", "must be a positive decimal ID")
	}
	if _, err := s.repo.GetByID(ctx, conversationID); err != nil {
		return mapConversationRepositoryError(err)
	}
	summary = strings.TrimSpace(summary)
	pinnedFacts := make([]string, 0, len(pinned))
	for _, fact := range pinned {
		trimmed := strings.TrimSpace(fact)
		if trimmed == "" {
			continue
		}
		pinnedFacts = append(pinnedFacts, trimmed)
	}
	if err := version.ValidateConversationSummary(summary, pinnedFacts); err != nil {
		return err
	}

	saved, err := s.repo.SaveSummary(ctx, version.ConversationSummary{
		ConversationID: conversationID,
		Summary:        summary,
		PinnedFacts:    pinnedFacts,
		TokenEstimate:  version.EstimateTokens(summary),
		UpdatedAt:      s.clock.Now(),
	})
	if err != nil {
		return mapConversationRepositoryError(err)
	}

	conversation, err := s.repo.GetByID(ctx, conversationID)
	if err != nil {
		return mapConversationRepositoryError(err)
	}
	if s.events != nil {
		conversationIDString := platformid.Format(conversationID)
		_ = s.events.Publish(ctx, eventingapp.Event{
			Channel: "version:" + platformid.Format(conversation.VersionID),
			Type:    "conversation.summary_updated",
			Scope: map[string]any{
				"version_id":      platformid.Format(conversation.VersionID),
				"conversation_id": conversationIDString,
			},
			Payload: map[string]any{
				"conversation_id": conversationIDString,
				"version":         saved.Version,
				"token_estimate":  saved.TokenEstimate,
			},
			RequestID: requestid.From(ctx),
		})
	}
	return nil
}

func summaryView(summary version.ConversationSummary) SummaryView {
	pinned := summary.PinnedFacts
	if pinned == nil {
		pinned = []string{}
	}
	return SummaryView{
		ConversationID: summary.ConversationID,
		Summary:        summary.Summary,
		PinnedFacts:    pinned,
		TokenEstimate:  summary.TokenEstimate,
		Version:        summary.Version,
		UpdatedAt:      summary.UpdatedAt,
	}
}
