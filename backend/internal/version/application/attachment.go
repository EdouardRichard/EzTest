package versionapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ExtractionResult is the outcome of one attachment.ingest attempt. A parse
// failure is a domain outcome (ErrorCode set, no Go error) so the worker can
// finish the task; storage failures are retryable Go errors.
type ExtractionResult struct {
	Attachment            AttachmentView
	Text                  string
	Chars                 int
	ContentSHA256         string
	ExtractedContentRefID int64
	ErrorCode             string
}

// AttachmentService registers message attachments against the content ledger
// and extracts their text into a second content reference.
type AttachmentService struct {
	repo      version.AttachmentRepository
	storage   ContentStore
	queue     TaskEnqueuer
	extractor TextExtractor
	events    eventingapp.Publisher
	ids       platformid.Generator
	clock     clock.Clock
}

// NewAttachmentService wires the attachment use cases.
func NewAttachmentService(
	repo version.AttachmentRepository,
	storage ContentStore,
	queue TaskEnqueuer,
	extractor TextExtractor,
	events eventingapp.Publisher,
	ids platformid.Generator,
	clk clock.Clock,
) *AttachmentService {
	return &AttachmentService{
		repo: repo, storage: storage, queue: queue, extractor: extractor,
		events: events, ids: ids, clock: clk,
	}
}

// RegisterFromMessage validates one message attachment part against the
// content ledger, stores the requirement_attachment runtime row and ensures
// the ingest task. Images are stored as references (skipped extraction);
// legacy .doc/.ppt are marked failed with unsupported_attachment_format
// without blocking the message.
func (s *AttachmentService) RegisterFromMessage(ctx context.Context, versionID, conversationID, messageID int64, part version.MessagePart, actorID int64) (AttachmentView, error) {
	if versionID <= 0 || messageID <= 0 {
		return AttachmentView{}, platform.Invalid("attachment", "version and message are required")
	}
	if part.Type != version.MessagePartAttachment {
		return AttachmentView{}, platform.Invalid("type", "part must be an attachment")
	}
	if err := version.ValidateUserMessageParts([]version.MessagePart{part}); err != nil {
		return AttachmentView{}, err
	}
	name, err := version.SanitizeAttachmentName(part.Name)
	if err != nil {
		return AttachmentView{}, err
	}
	content, reader, err := s.storage.Open(ctx, part.SHA256)
	if err != nil {
		if isPlatformNotFound(err) {
			return AttachmentView{}, platform.Invalid("sha256", "content object was not uploaded")
		}
		return AttachmentView{}, err
	}
	_ = reader.Close()
	if content.Size != part.Size {
		return AttachmentView{}, platform.Invalid("size", "does not match the uploaded content size")
	}

	now := s.clock.Now()
	attachmentID := s.ids.Next()
	ref, err := s.storage.AddRef(ctx, part.SHA256, version.AttachmentOwnerKind, attachmentID, version.AttachmentContentRole)
	if err != nil {
		if isPlatformNotFound(err) {
			return AttachmentView{}, platform.Invalid("sha256", "content object was not uploaded")
		}
		return AttachmentView{}, err
	}

	kind := version.DetectAttachmentKind(name, part.MIME)
	status := version.AttachmentPending
	errorCode := ""
	errorText := ""
	switch {
	case kind.IsImage():
		status = version.AttachmentSkipped
	case !kind.SupportsExtraction():
		status = version.AttachmentFailed
		errorCode = version.AttachmentErrorUnsupported
		errorText = "unsupported attachment format; upload md/txt/docx/pptx/pdf or an image"
	}

	attachment := version.Attachment{
		ID:                    attachmentID,
		VersionID:             versionID,
		ConversationMessageID: &messageID,
		ContentRefID:          ref.ID,
		Name:                  name,
		MIME:                  part.MIME,
		Size:                  content.Size,
		ExtractionStatus:      status,
		ExtractionErrorCode:   errorCode,
		ExtractionErrorText:   errorText,
		CreatedBy:             optionalActor(actorID),
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if err := s.repo.Create(ctx, attachment); err != nil {
		return AttachmentView{}, mapAttachmentRepositoryError(err)
	}

	switch status {
	case version.AttachmentFailed:
		s.publishAttachmentEvent(ctx, versionID, "requirement.attachment_failed", attachment, map[string]any{
			"attachment_id": platformid.Format(attachmentID),
			"message_id":    platformid.Format(messageID),
			"code":          errorCode,
			"message":       errorText,
		})
	case version.AttachmentPending:
		s.enqueueIngest(ctx, versionID, conversationID, messageID, attachmentID)
	}
	return toAttachmentView(attachment), nil
}

// Get returns one attachment scoped to its version.
func (s *AttachmentService) Get(ctx context.Context, versionID, attachmentID int64) (AttachmentView, error) {
	if versionID <= 0 {
		return AttachmentView{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	if attachmentID <= 0 {
		return AttachmentView{}, platform.Invalid("attachment_id", "must be a positive decimal ID")
	}
	attachment, err := s.repo.GetInVersion(ctx, versionID, attachmentID)
	if err != nil {
		return AttachmentView{}, mapAttachmentRepositoryError(err)
	}
	return toAttachmentView(attachment), nil
}

// Attachment read limits for the agent tools (character based).
const (
	DefaultAttachmentReadChars = 4000
	MaxAttachmentReadChars     = 20000
)

// AttachmentInfo is the agent-facing attachment view used by the ingest run.
// It carries the extracted text and its total character count. The runtime
// adapts this to its own AttachmentInfo port.
type AttachmentInfo struct {
	ID                    int64
	VersionID             int64
	ConversationMessageID *int64
	RequirementID         *int64
	Name                  string
	MIME                  string
	Size                  int64
	Status                string
	ErrorCode             string
	ErrorText             string
	Text                  string
	TotalChars            int
}

// AttachmentMeta is the compact attachment list entry for agent tools.
type AttachmentMeta struct {
	ID                    int64
	ConversationMessageID int64
	RequirementID         *int64
	Name                  string
	MIME                  string
	Size                  int64
	ExtractionStatus      string
	ErrorCode             string
	ErrorText             string
}

// AttachmentImageRef identifies the original content object of an image
// attachment so the runtime can pass an image_ref to the model.
type AttachmentImageRef struct {
	AttachmentID int64
	SHA256       string
	Name         string
	MIME         string
	Size         int64
}

// GetAttachmentInfo returns one attachment with its extracted text (when the
// extraction succeeded), scoped to the version.
func (s *AttachmentService) GetAttachmentInfo(ctx context.Context, versionID, attachmentID int64) (AttachmentInfo, error) {
	if versionID <= 0 || attachmentID <= 0 {
		return AttachmentInfo{}, platform.Invalid("attachment_id", "must be positive decimal IDs")
	}
	attachment, err := s.repo.GetInVersion(ctx, versionID, attachmentID)
	if err != nil {
		return AttachmentInfo{}, mapAttachmentRepositoryError(err)
	}
	info := AttachmentInfo{
		ID:                    attachment.ID,
		VersionID:             attachment.VersionID,
		ConversationMessageID: attachment.ConversationMessageID,
		RequirementID:         attachment.RequirementID,
		Name:                  attachment.Name,
		MIME:                  attachment.MIME,
		Size:                  attachment.Size,
		Status:                attachment.ExtractionStatus,
		ErrorCode:             attachment.ExtractionErrorCode,
		ErrorText:             attachment.ExtractionErrorText,
	}
	if attachment.ExtractionStatus == version.AttachmentDone {
		text, err := s.readExtractedText(ctx, attachment)
		if err != nil {
			return AttachmentInfo{}, err
		}
		info.Text = text
		info.TotalChars = len([]rune(text))
	}
	return info, nil
}

// ListAttachments returns the attachments linked to one conversation, in
// attachment-ID order.
func (s *AttachmentService) ListAttachments(ctx context.Context, versionID, conversationID int64) ([]AttachmentMeta, error) {
	if versionID <= 0 || conversationID <= 0 {
		return nil, platform.Invalid("conversation_id", "must be positive decimal IDs")
	}
	if err := s.ensureConversation(ctx, versionID, conversationID); err != nil {
		return nil, err
	}
	attachments, err := s.repo.ListByConversation(ctx, versionID, conversationID)
	if err != nil {
		return nil, mapAttachmentRepositoryError(err)
	}
	metas := make([]AttachmentMeta, 0, len(attachments))
	for _, attachment := range attachments {
		messageID := derefInt64(attachment.ConversationMessageID)
		metas = append(metas, AttachmentMeta{
			ID:                    attachment.ID,
			ConversationMessageID: messageID,
			RequirementID:         attachment.RequirementID,
			Name:                  attachment.Name,
			MIME:                  attachment.MIME,
			Size:                  attachment.Size,
			ExtractionStatus:      attachment.ExtractionStatus,
			ErrorCode:             attachment.ExtractionErrorCode,
			ErrorText:             attachment.ExtractionErrorText,
		})
	}
	return metas, nil
}

// ReadAttachmentText returns a character window of the extracted text of one
// conversation attachment. offset is a rune offset; maxChars defaults to 4000
// and is capped at 20000.
func (s *AttachmentService) ReadAttachmentText(ctx context.Context, versionID, conversationID, attachmentID int64, offset, maxChars int) (string, int, error) {
	if offset < 0 {
		return "", 0, platform.Invalid("offset", "must be zero or positive")
	}
	if maxChars <= 0 {
		maxChars = DefaultAttachmentReadChars
	}
	if maxChars > MaxAttachmentReadChars {
		maxChars = MaxAttachmentReadChars
	}
	attachment, err := s.conversationAttachment(ctx, versionID, conversationID, attachmentID)
	if err != nil {
		return "", 0, err
	}
	if attachment.ExtractionStatus != version.AttachmentDone {
		return "", 0, platform.StateConflict("attachment text is not available")
	}
	text, err := s.readExtractedText(ctx, attachment)
	if err != nil {
		return "", 0, err
	}
	runes := []rune(text)
	total := len(runes)
	if offset >= total {
		return "", total, nil
	}
	end := offset + maxChars
	if end > total {
		end = total
	}
	return string(runes[offset:end]), total, nil
}

// AttachmentImageRef returns the original content reference of an image
// attachment (metadata only; no OCR/vision analysis in P1).
func (s *AttachmentService) AttachmentImageRef(ctx context.Context, versionID, conversationID, attachmentID int64) (AttachmentImageRef, error) {
	attachment, err := s.conversationAttachment(ctx, versionID, conversationID, attachmentID)
	if err != nil {
		return AttachmentImageRef{}, err
	}
	if !version.DetectAttachmentKind(attachment.Name, attachment.MIME).IsImage() {
		return AttachmentImageRef{}, platform.Invalid("attachment_id", "is not an image attachment")
	}
	sha256, err := s.originalObjectID(ctx, attachment.ID, attachment.ContentRefID)
	if err != nil {
		return AttachmentImageRef{}, err
	}
	return AttachmentImageRef{
		AttachmentID: attachment.ID,
		SHA256:       sha256,
		Name:         attachment.Name,
		MIME:         attachment.MIME,
		Size:         attachment.Size,
	}, nil
}

func (s *AttachmentService) conversationAttachment(ctx context.Context, versionID, conversationID, attachmentID int64) (version.Attachment, error) {
	if versionID <= 0 || conversationID <= 0 || attachmentID <= 0 {
		return version.Attachment{}, platform.Invalid("attachment_id", "must be positive decimal IDs")
	}
	if err := s.ensureConversation(ctx, versionID, conversationID); err != nil {
		return version.Attachment{}, err
	}
	attachments, err := s.repo.ListByConversation(ctx, versionID, conversationID)
	if err != nil {
		return version.Attachment{}, mapAttachmentRepositoryError(err)
	}
	for _, attachment := range attachments {
		if attachment.ID == attachmentID {
			return attachment, nil
		}
	}
	return version.Attachment{}, platform.NotFound("attachment")
}

func (s *AttachmentService) ensureConversation(ctx context.Context, versionID, conversationID int64) error {
	exists, err := s.repo.ConversationExists(ctx, versionID, conversationID)
	if err != nil {
		return err
	}
	if !exists {
		return platform.NotFound("conversation")
	}
	return nil
}

// Extract performs the attachment.ingest step. It is idempotent: pending and
// processing rows are parsed, done/failed/skipped rows return their stored
// state without side effects.
func (s *AttachmentService) Extract(ctx context.Context, attachmentID int64) (ExtractionResult, error) {
	if attachmentID <= 0 {
		return ExtractionResult{}, platform.Invalid("attachment_id", "must be a positive decimal ID")
	}
	attachment, err := s.repo.Get(ctx, attachmentID)
	if err != nil {
		return ExtractionResult{}, mapAttachmentRepositoryError(err)
	}
	switch attachment.ExtractionStatus {
	case version.AttachmentDone:
		result := ExtractionResult{
			Attachment:            toAttachmentView(attachment),
			ExtractedContentRefID: derefInt64(attachment.ExtractedContentRefID),
		}
		if text, err := s.readExtractedText(ctx, attachment); err == nil {
			result.Text = text
			result.Chars = len([]rune(text))
		}
		return result, nil
	case version.AttachmentFailed:
		return ExtractionResult{
			Attachment: toAttachmentView(attachment),
			ErrorCode:  attachment.ExtractionErrorCode,
		}, nil
	case version.AttachmentSkipped:
		return ExtractionResult{Attachment: toAttachmentView(attachment)}, nil
	}

	objectID, err := s.originalObjectID(ctx, attachmentID, attachment.ContentRefID)
	if err != nil {
		return ExtractionResult{}, err
	}
	_, reader, err := s.storage.Open(ctx, objectID)
	if err != nil {
		return ExtractionResult{}, err
	}
	defer reader.Close()

	changed, err := s.repo.MarkProcessing(ctx, attachmentID, s.clock.Now())
	if err != nil {
		return ExtractionResult{}, mapAttachmentRepositoryError(err)
	}
	if !changed {
		current, err := s.repo.Get(ctx, attachmentID)
		if err != nil {
			return ExtractionResult{}, mapAttachmentRepositoryError(err)
		}
		switch current.ExtractionStatus {
		case version.AttachmentDone:
			result := ExtractionResult{
				Attachment:            toAttachmentView(current),
				ExtractedContentRefID: derefInt64(current.ExtractedContentRefID),
			}
			if text, err := s.readExtractedText(ctx, current); err == nil {
				result.Text = text
				result.Chars = len([]rune(text))
			}
			return result, nil
		case version.AttachmentFailed:
			return ExtractionResult{
				Attachment: toAttachmentView(current),
				ErrorCode:  current.ExtractionErrorCode,
			}, nil
		case version.AttachmentSkipped:
			return ExtractionResult{Attachment: toAttachmentView(current)}, nil
		default:
			return ExtractionResult{}, fmt.Errorf("attachment extraction is already in progress")
		}
	}
	text, extractErr := s.extractor.Extract(ctx, attachment.Name, attachment.MIME, reader)
	if extractErr == nil && int64(len(text)) > version.MaxExtractedTextBytes {
		extractErr = version.ErrAttachmentTooLarge
	}
	if extractErr == nil && strings.TrimSpace(text) == "" {
		extractErr = fmt.Errorf("%w: no text could be extracted", version.ErrAttachmentCorrupt)
	}
	if extractErr != nil {
		return s.failExtraction(ctx, attachment, extractErr)
	}

	uploaded, err := s.storage.Upload(ctx, attachment.Name+".txt", "text/plain; charset=utf-8",
		strings.NewReader(text), version.MaxExtractedTextBytes+1, 0)
	if err != nil {
		return ExtractionResult{}, err
	}
	textRef, err := s.storage.AddRef(ctx, uploaded.SHA256, version.AttachmentOwnerKind, attachmentID, version.AttachmentTextRole)
	if err != nil {
		return ExtractionResult{}, err
	}
	now := s.clock.Now()
	if err := s.repo.MarkDone(ctx, attachmentID, textRef.ID, now); err != nil {
		return ExtractionResult{}, mapAttachmentRepositoryError(err)
	}
	attachment.ExtractionStatus = version.AttachmentDone
	attachment.ExtractedContentRefID = &textRef.ID
	attachment.ExtractionErrorCode = ""
	attachment.ExtractionErrorText = ""
	attachment.UpdatedAt = now
	chars := len([]rune(text))
	s.publishAttachmentEvent(ctx, attachment.VersionID, "requirement.attachment_ingested", attachment, map[string]any{
		"attachment_id": platformid.Format(attachment.ID),
		"message_id":    messageIDString(attachment.ConversationMessageID),
		"chars":         chars,
	})
	return ExtractionResult{
		Attachment:            toAttachmentView(attachment),
		Text:                  text,
		Chars:                 chars,
		ContentSHA256:         uploaded.SHA256,
		ExtractedContentRefID: textRef.ID,
	}, nil
}

func (s *AttachmentService) failExtraction(ctx context.Context, attachment version.Attachment, extractErr error) (ExtractionResult, error) {
	code := version.AttachmentErrorExtraction
	message := "attachment text extraction failed"
	if errors.Is(extractErr, version.ErrUnsupportedAttachmentFormat) {
		code = version.AttachmentErrorUnsupported
		message = "unsupported attachment format"
	}
	now := s.clock.Now()
	if err := s.repo.MarkFailed(ctx, attachment.ID, code, message, now); err != nil {
		return ExtractionResult{}, mapAttachmentRepositoryError(err)
	}
	attachment.ExtractionStatus = version.AttachmentFailed
	attachment.ExtractionErrorCode = code
	attachment.ExtractionErrorText = message
	attachment.UpdatedAt = now
	s.publishAttachmentEvent(ctx, attachment.VersionID, "requirement.attachment_failed", attachment, map[string]any{
		"attachment_id": platformid.Format(attachment.ID),
		"message_id":    messageIDString(attachment.ConversationMessageID),
		"code":          code,
		"message":       message,
	})
	return ExtractionResult{Attachment: toAttachmentView(attachment), ErrorCode: code}, nil
}

// originalObjectID resolves the attachment's content reference back to the
// content object SHA-256 through the storage ledger port.
func (s *AttachmentService) originalObjectID(ctx context.Context, attachmentID, contentRefID int64) (string, error) {
	refs, err := s.storage.ListRefs(ctx, version.AttachmentOwnerKind, attachmentID)
	if err != nil {
		return "", err
	}
	for _, ref := range refs {
		if ref.ID == contentRefID {
			return ref.ObjectID, nil
		}
	}
	return "", fmt.Errorf("attachment %d: original content reference %d is missing", attachmentID, contentRefID)
}

func (s *AttachmentService) readExtractedText(ctx context.Context, attachment version.Attachment) (string, error) {
	if attachment.ExtractedContentRefID == nil {
		return "", version.ErrAttachmentNotFound
	}
	refs, err := s.storage.ListRefs(ctx, version.AttachmentOwnerKind, attachment.ID)
	if err != nil {
		return "", err
	}
	for _, ref := range refs {
		if ref.ID != *attachment.ExtractedContentRefID {
			continue
		}
		_, reader, err := s.storage.Open(ctx, ref.ObjectID)
		if err != nil {
			return "", err
		}
		defer reader.Close()
		data := make([]byte, 0, attachment.Size)
		buffer := make([]byte, 32*1024)
		for {
			read, err := reader.Read(buffer)
			if read > 0 {
				data = append(data, buffer[:read]...)
				if int64(len(data)) > version.MaxExtractedTextBytes {
					return "", version.ErrAttachmentTooLarge
				}
			}
			if err != nil {
				break
			}
		}
		return string(data), nil
	}
	return "", version.ErrAttachmentNotFound
}

func (s *AttachmentService) enqueueIngest(ctx context.Context, versionID, conversationID, messageID, attachmentID int64) {
	if s.queue == nil {
		return
	}
	payload := map[string]any{
		"attachment_id": platformid.Format(attachmentID),
		"version_id":    platformid.Format(versionID),
		"message_id":    platformid.Format(messageID),
	}
	if conversationID > 0 {
		payload["conversation_id"] = platformid.Format(conversationID)
	}
	_ = s.queue.Ensure(ctx, TaskInput{
		VersionID: versionID,
		Team:      "clarification",
		Type:      "attachment.ingest",
		DedupeKey: "attachment:" + platformid.Format(attachmentID),
		Payload:   payload,
	})
}

func (s *AttachmentService) publishAttachmentEvent(ctx context.Context, versionID int64, eventType string, attachment version.Attachment, payload map[string]any) {
	if s.events == nil {
		return
	}
	scope := map[string]any{
		"version_id":    platformid.Format(versionID),
		"attachment_id": platformid.Format(attachment.ID),
	}
	if attachment.ConversationMessageID != nil {
		scope["message_id"] = platformid.Format(*attachment.ConversationMessageID)
	}
	_ = s.events.Publish(ctx, eventingapp.Event{
		Channel:   "version:" + platformid.Format(versionID),
		Type:      eventType,
		Scope:     scope,
		Payload:   payload,
		RequestID: requestid.From(ctx),
	})
}

func mapAttachmentRepositoryError(err error) error {
	if errors.Is(err, version.ErrAttachmentNotFound) {
		return platform.NotFound("attachment")
	}
	return err
}

func isPlatformNotFound(err error) bool {
	var apiErr *platform.Error
	return errors.As(err, &apiErr) && apiErr.Status == 404
}

func messageIDString(messageID *int64) string {
	if messageID == nil {
		return ""
	}
	return platformid.Format(*messageID)
}

func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
