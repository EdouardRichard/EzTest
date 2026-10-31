package versionapp

import (
	"context"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ReconcilerLimitMax caps every P1 reconciler read.
const ReconcilerLimitMax = 100

// ReconcilerConversation is one clarification conversation with its messages,
// consumed by the P1 reconciler.
type ReconcilerConversation = version.ReconcilerConversation

// ReconcilerMessage is one message plus the agent profile that wrote it.
type ReconcilerMessage = version.ReconcilerMessage

// PendingAttachment is the attachment.ingest reconciler row.
type PendingAttachment = version.PendingAttachment

// ListInitialVersionsWithContent returns initial versions that already have a
// conversation message or requirement, so the reconciler can repair a missed
// initial -> running projection. IDs are ascending; limit is clamped to 1..100.
func (s *Service) ListInitialVersionsWithContent(ctx context.Context, limit int) ([]int64, error) {
	return s.repo.ListInitialVersionsWithContent(ctx, clampReconcilerLimit(limit))
}

// ListRunningVersions returns the newest running version IDs for the P4
// reconciler rules (spec §7.3–§7.5), at most limit of them. Completed versions
// are deliberately excluded: their only P4 rule is the reopen scan, which the
// requirement/design/execution hooks own, so they must never receive an ensure
// pass. The scan pages through the version list because the P1 repository only
// exposes the newest-first summary page; limit is clamped to 1..100.
func (s *Service) ListRunningVersions(ctx context.Context, limit int) ([]int64, error) {
	limit = clampReconcilerLimit(limit)
	ids := make([]int64, 0, limit)
	var beforeID *int64
	for len(ids) < limit {
		summaries, err := s.repo.List(ctx, limit, beforeID)
		if err != nil {
			return nil, err
		}
		for _, summary := range summaries {
			if summary.Status != version.StatusRunning {
				continue
			}
			ids = append(ids, summary.ID)
			if len(ids) == limit {
				break
			}
		}
		if len(summaries) < limit {
			break
		}
		lastID := summaries[len(summaries)-1].ID
		beforeID = &lastID
	}
	return ids, nil
}

// ListReconcilable returns the most recent clarification conversations that
// have messages (newest first) with their messages in ascending ID order.
func (s *ConversationService) ListReconcilable(ctx context.Context, limit int) ([]ReconcilerConversation, error) {
	return s.repo.ListReconcilable(ctx, clampReconcilerLimit(limit))
}

// ListPendingForIngest returns the oldest pending attachments for the
// attachment.ingest reconciler, with the owning conversation/message when
// linked (zero otherwise).
func (s *AttachmentService) ListPendingForIngest(ctx context.Context, limit int) ([]PendingAttachment, error) {
	return s.repo.ListPendingForIngest(ctx, clampReconcilerLimit(limit))
}

func clampReconcilerLimit(limit int) int {
	if limit < 1 {
		return 1
	}
	if limit > ReconcilerLimitMax {
		return ReconcilerLimitMax
	}
	return limit
}
