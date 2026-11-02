package versionadapter

import (
	"context"
	"unicode/utf8"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
)

// GetAttachment implements agentapp.AttachmentReader for attachment.ingest
// runs. It first runs the idempotent version-side extraction (parse failures
// become a failed status/error, not a Go error), then returns the refreshed
// attachment with its extracted text. The version service exposes the
// originating message ID (not the conversation ID), so ConversationID stays
// zero here; ingest runs take the conversation scope from the work-item payload.
func (a *Adapter) GetAttachment(ctx context.Context, versionID, attachmentID int64) (agentapp.AttachmentInfo, error) {
	if a.attachments == nil {
		return agentapp.AttachmentInfo{}, a.missing("attachments")
	}
	// Extraction is idempotent: pending/processing rows are parsed, done/failed
	// rows return their stored state. Only infrastructure errors are retryable.
	if _, err := a.attachments.Extract(ctx, attachmentID); err != nil {
		return agentapp.AttachmentInfo{}, err
	}
	info, err := a.attachments.GetAttachmentInfo(ctx, versionID, attachmentID)
	if err != nil {
		return agentapp.AttachmentInfo{}, err
	}
	requirementID := int64(0)
	if info.RequirementID != nil {
		requirementID = *info.RequirementID
	}
	return agentapp.AttachmentInfo{
		ID:             info.ID,
		ConversationID: 0,
		RequirementID:  requirementID,
		Name:           info.Name,
		MIME:           info.MIME,
		Status:         info.Status,
		Error:          attachmentError(info.ErrorCode, info.ErrorText),
		Text:           info.Text,
		TotalChars:     info.TotalChars,
	}, nil
}

// ListAttachments implements tools.AttachmentPort for the attachment.list tool.
func (a *Adapter) ListAttachments(ctx context.Context, versionID, conversationID int64) ([]tools.AttachmentMeta, error) {
	if a.attachments == nil {
		return nil, a.missing("attachments")
	}
	metas, err := a.attachments.ListAttachments(ctx, versionID, conversationID)
	if err != nil {
		return nil, err
	}
	result := make([]tools.AttachmentMeta, 0, len(metas))
	for _, meta := range metas {
		requirementID := int64(0)
		if meta.RequirementID != nil {
			requirementID = *meta.RequirementID
		}
		result = append(result, tools.AttachmentMeta{
			ID:             meta.ID,
			ConversationID: conversationID,
			RequirementID:  requirementID,
			Name:           meta.Name,
			MIME:           meta.MIME,
			Size:           meta.Size,
			Status:         meta.ExtractionStatus,
		})
	}
	return result, nil
}

// ReadAttachmentText implements tools.AttachmentPort for attachment.read_text.
// The version service applies the rune offset/maxChars rules and only serves
// extracted text for done attachments (state_conflict otherwise).
func (a *Adapter) ReadAttachmentText(ctx context.Context, versionID, conversationID, attachmentID int64, offset, maxChars int) (tools.AttachmentText, error) {
	if a.attachments == nil {
		return tools.AttachmentText{}, a.missing("attachments")
	}
	info, err := a.attachments.GetAttachmentInfo(ctx, versionID, attachmentID)
	if err != nil {
		return tools.AttachmentText{}, err
	}
	text, totalChars, err := a.attachments.ReadAttachmentText(ctx, versionID, conversationID, attachmentID, offset, maxChars)
	if err != nil {
		return tools.AttachmentText{}, err
	}
	return tools.AttachmentText{
		AttachmentID: attachmentID,
		Name:         info.Name,
		MIME:         info.MIME,
		Offset:       offset,
		Text:         text,
		TotalChars:   totalChars,
		Truncated:    offset+utf8.RuneCountInString(text) < totalChars,
	}, nil
}

// AttachmentImageRef implements tools.AttachmentPort for attachment.image_ref.
// P1 does not perform vision understanding; the SHA-256 is the content-ledger
// reference the UI can resolve.
func (a *Adapter) AttachmentImageRef(ctx context.Context, versionID, conversationID, attachmentID int64) (tools.AttachmentImage, error) {
	if a.attachments == nil {
		return tools.AttachmentImage{}, a.missing("attachments")
	}
	reference, err := a.attachments.AttachmentImageRef(ctx, versionID, conversationID, attachmentID)
	if err != nil {
		return tools.AttachmentImage{}, err
	}
	return tools.AttachmentImage{
		AttachmentID: reference.AttachmentID,
		SHA256:       reference.SHA256,
		URL:          "/api/content/" + reference.SHA256,
		MIME:         reference.MIME,
	}, nil
}

func attachmentError(code, text string) string {
	switch {
	case code == "":
		return text
	case text == "":
		return code
	default:
		return code + ": " + text
	}
}
