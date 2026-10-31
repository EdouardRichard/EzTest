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
