package versionadapter

import (
	"context"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
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
	images := make([]agentapp.AttachmentImageInfo, 0, len(info.Images))
	for _, image := range info.Images {
		images = append(images, agentapp.AttachmentImageInfo{
			ID: image.ID, Seq: image.Seq, SHA256: image.SHA256, MIME: image.MIME,
			Width: image.Width, Height: image.Height, SourceKind: image.SourceKind,
			Status: image.Status, Caption: image.Caption, Error: image.Error,
		})
	}
	imageTokens := info.ImageTokens
	if imageTokens <= 0 && len(images) > 0 {
		imageTokens = len(images) * agentapp.AttachmentImageTokenBudget
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
		Images:         images,
		ImageTokens:    imageTokens,
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
// The version service applies the rune offset/maxChars rules and returns the
// structured parse status (done/pending/failed/skipped) as data, so callers do
// not see a 409 state_conflict for an attachment that is still parsing.
func (a *Adapter) ReadAttachmentText(ctx context.Context, versionID, conversationID, attachmentID int64, offset, maxChars int) (tools.AttachmentText, error) {
	if a.attachments == nil {
		return tools.AttachmentText{}, a.missing("attachments")
	}
	read, err := a.attachments.ReadAttachmentText(ctx, versionID, conversationID, attachmentID, offset, maxChars)
	if err != nil {
		return tools.AttachmentText{}, err
	}
	return tools.AttachmentText{
		AttachmentID: read.AttachmentID,
		Name:         read.Name,
		MIME:         read.MIME,
		Status:       read.Status,
		Offset:       read.Offset,
		Text:         read.Text,
		TotalChars:   read.TotalChars,
		Truncated:    read.Truncated,
		Error:        read.Error,
		Retryable:    read.Retryable,
		Images:       attachmentImageDetails(read.Images),
	}, nil
}

// attachmentImageDetails maps the version-side persisted image rows/captions to
// the tool output shape shared by attachment.read_text and attachment.image_ref
// (C-01).
func attachmentImageDetails(images []versionapp.AttachmentImageInfo) []tools.AttachmentImageDetail {
	details := make([]tools.AttachmentImageDetail, 0, len(images))
	for _, image := range images {
		details = append(details, tools.AttachmentImageDetail{
			ID: image.ID, Seq: image.Seq, SHA256: image.SHA256, MIME: image.MIME,
			Width: image.Width, Height: image.Height,
			Status: image.Status, Caption: image.Caption, Error: image.Error,
		})
	}
	return details
}

// LoadImage implements agentapp.ImageLoader for vision runs: it resolves the
// image part's content data_ref into the original bytes and MIME. The version
// service re-probes the content, so a non-image object can never be sent to the
// model as an image.
func (a *Adapter) LoadImage(ctx context.Context, dataRef string) (agentapp.ImageData, error) {
	if a.attachments == nil {
		return agentapp.ImageData{}, a.missing("attachments")
	}
	mime, data, err := a.attachments.ReadImage(ctx, dataRef)
	if err != nil {
		return agentapp.ImageData{}, err
	}
	return agentapp.ImageData{MIME: mime, Data: data}, nil
}

// AttachmentImageRef implements tools.AttachmentPort for attachment.image_ref.
// The platform already generated the captions; the adapter returns the same
// persisted image rows as attachment.read_text and never re-runs vision.
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
		Images:       attachmentImageDetails(reference.Images),
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
