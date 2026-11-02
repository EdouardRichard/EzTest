package versionapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	images    ImageProber
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
	images ImageProber,
	events eventingapp.Publisher,
	ids platformid.Generator,
	clk clock.Clock,
) *AttachmentService {
	return &AttachmentService{
		repo: repo, storage: storage, queue: queue, extractor: extractor, images: images,
		events: events, ids: ids, clock: clk,
	}
}

// RegisterFromMessage validates one message attachment part against the
// content ledger, stores the requirement_attachment runtime row and ensures
// the ingest task. Image attachments also become image parts in the stored
// message (PrepareUserMessageParts); registering the original attachment part
// keeps the content ref alive as an extraction-skipped row because there is no
// text to extract. Legacy .doc/.ppt are marked failed with
// unsupported_attachment_format without blocking the message.
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
	case version.IsLegacyOfficeFormat(name):
		// The pre-OOXML .doc/.ppt binary formats never reach the parser: the
		// upload path must still return the same save-as hint the parser would
		// (R26), not the generic unsupported-format message.
		status = version.AttachmentFailed
		errorCode = version.AttachmentErrorUnsupported
		errorText = version.ErrUnsupportedLegacyAttachment.Error() + ": " + name
	case !kind.SupportsExtraction():
		status = version.AttachmentFailed
		errorCode = version.AttachmentErrorUnsupported
		errorText = "unsupported attachment format; upload md/txt/html/docx/pptx/xlsx/pdf or an image"
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

// PrepareUserMessageParts converts an image attachment part into the R5 image
// part stored in the message (spec §8.1/§8.3) and rejects a message that
// references an attachment already registered in the conversation while its
// extraction is still running (typed 409 attachment_pending, spec §8.5). It
// runs before the message insert so a rejected message leaves no rows behind.
func (s *AttachmentService) PrepareUserMessageParts(ctx context.Context, versionID, conversationID int64, parts []version.MessagePart) ([]version.MessagePart, error) {
	if versionID <= 0 || conversationID <= 0 {
		return nil, platform.Invalid("conversation_id", "must be positive decimal IDs")
	}
	prepared := make([]version.MessagePart, 0, len(parts))
	for _, part := range parts {
		if part.Type != version.MessagePartAttachment {
			prepared = append(prepared, part)
			continue
		}
		if version.DetectAttachmentKind(part.Name, part.MIME).IsImage() {
			image, err := s.imagePartFromAttachment(ctx, part)
			if err != nil {
				return nil, err
			}
			prepared = append(prepared, image)
			continue
		}
		// Documents are extracted asynchronously after the message is stored;
		// only an already-registered reference can be observed as pending here.
		if err := s.validateAttachmentContent(ctx, part); err != nil {
			return nil, err
		}
		pending, err := s.pendingAttachmentReference(ctx, versionID, conversationID, part.SHA256)
		if err != nil {
			return nil, err
		}
		if pending != nil {
			return nil, attachmentPendingError(*pending)
		}
		prepared = append(prepared, part)
	}
	if err := version.ValidateUserMessageParts(prepared); err != nil {
		return nil, err
	}
	return prepared, nil
}

// imagePartFromAttachment reads the uploaded image and builds the validated
// image part that replaces the attachment part in the stored message.
func (s *AttachmentService) imagePartFromAttachment(ctx context.Context, part version.MessagePart) (version.MessagePart, error) {
	content, reader, err := s.storage.Open(ctx, part.SHA256)
	if err != nil {
		if isPlatformNotFound(err) {
			return version.MessagePart{}, platform.Invalid("sha256", "content object was not uploaded")
		}
		return version.MessagePart{}, err
	}
	defer reader.Close()
	if content.Size != part.Size {
		return version.MessagePart{}, platform.Invalid("size", "does not match the uploaded content size")
	}
	if s.images == nil {
		return version.MessagePart{}, errors.New("attachment service has no image prober")
	}
	data, err := io.ReadAll(io.LimitReader(reader, version.MaxAttachmentBytes+1))
	if err != nil {
		return version.MessagePart{}, err
	}
	if int64(len(data)) > version.MaxAttachmentBytes {
		return version.MessagePart{}, platform.Invalid("size", "image exceeds the 10 MiB upload limit")
	}
	mime, width, height, err := s.images.Probe(ctx, part.MIME, data)
	if err != nil {
		return version.MessagePart{}, err
	}
	return version.MessagePart{
		Type:    version.MessagePartImage,
		MIME:    mime,
		DataRef: part.SHA256,
		Width:   width,
		Height:  height,
	}, nil
}

// validateAttachmentContent verifies that the uploaded object exists and has
// the size the message part declares.
func (s *AttachmentService) validateAttachmentContent(ctx context.Context, part version.MessagePart) error {
	content, reader, err := s.storage.Open(ctx, part.SHA256)
	if err != nil {
		if isPlatformNotFound(err) {
			return platform.Invalid("sha256", "content object was not uploaded")
		}
		return err
	}
	_ = reader.Close()
	if content.Size != part.Size {
		return platform.Invalid("size", "does not match the uploaded content size")
	}
	return nil
}

// pendingAttachmentReference finds an attachment in the conversation with the
// same content whose extraction is still pending/processing.
func (s *AttachmentService) pendingAttachmentReference(ctx context.Context, versionID, conversationID int64, sha256 string) (*version.Attachment, error) {
	attachments, err := s.repo.ListByConversation(ctx, versionID, conversationID)
	if err != nil {
		return nil, mapAttachmentRepositoryError(err)
	}
	for _, attachment := range attachments {
		if attachment.ExtractionStatus != version.AttachmentPending &&
			attachment.ExtractionStatus != version.AttachmentProcessing {
			continue
		}
		objectID, err := s.originalObjectID(ctx, attachment.ID, attachment.ContentRefID)
		if err != nil {
			return nil, err
		}
		if objectID == sha256 {
			found := attachment
			return &found, nil
		}
	}
	return nil, nil
}

// attachmentPendingError is the typed 409 for a message that references an
// attachment still being parsed; the client can retry after the parse finishes.
func attachmentPendingError(attachment version.Attachment) error {
	return &platform.Error{
		Status:    http.StatusConflict,
		Code:      AttachmentPendingCode,
		Message:   AttachmentParsingMessage,
		Retryable: true,
		Details: map[string]any{
			"attachment_id": platformid.Format(attachment.ID),
			"name":          attachment.Name,
		},
	}
}

// ReadImage resolves an image part's content data_ref into the original bytes
// plus the sniffed MIME for a vision model. The data_ref is the content-ledger
// SHA-256; the bytes are re-probed so a non-image object can never reach the
// model as an image.
func (s *AttachmentService) ReadImage(ctx context.Context, dataRef string) (string, []byte, error) {
	dataRef = strings.ToLower(strings.TrimSpace(dataRef))
	if !isContentSHA256(dataRef) {
		return "", nil, platform.Invalid("data_ref", "must be the 64 lowercase hex content sha256")
	}
	content, reader, err := s.storage.Open(ctx, dataRef)
	if err != nil {
		if isPlatformNotFound(err) {
			return "", nil, platform.NotFound("content")
		}
		return "", nil, err
	}
	defer reader.Close()
	if content.Size > version.MaxAttachmentBytes {
		return "", nil, platform.Invalid("data_ref", "image exceeds the 10 MiB upload limit")
	}
	data, err := io.ReadAll(io.LimitReader(reader, version.MaxAttachmentBytes+1))
	if err != nil {
		return "", nil, err
	}
	if int64(len(data)) > version.MaxAttachmentBytes {
		return "", nil, platform.Invalid("data_ref", "image exceeds the 10 MiB upload limit")
	}
	if s.images == nil {
		return "", nil, errors.New("attachment service has no image prober")
	}
	mime, _, _, err := s.images.Probe(ctx, content.MIME, data)
	if err != nil {
		return "", nil, err
	}
	return mime, data, nil
}

func isContentSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
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

// AttachmentPendingCode is the stable error code of the typed 409 returned when
// a message references an attachment whose extraction is still running
// (spec §8.5: "解析中，可重试" instead of silently ignoring the attachment).
const AttachmentPendingCode = "attachment_pending"

// AttachmentTextRead is the structured outcome of reading attachment text. A
// non-done status is a normal result: callers report it to the model instead
// of turning it into a 409 state_conflict (§8.5).
type AttachmentTextRead struct {
	AttachmentID int64
	Name         string
	MIME         string
	Status       string
	Text         string
	Offset       int
	TotalChars   int
	Truncated    bool
	Error        string
	Retryable    bool
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

// ReadAttachmentText returns the structured status and a character window of
// the extracted text of one conversation attachment. offset is a rune offset;
// maxChars defaults to 4000 and is capped at 20000. A non-done status is
// returned as data, never as a state_conflict error.
func (s *AttachmentService) ReadAttachmentText(ctx context.Context, versionID, conversationID, attachmentID int64, offset, maxChars int) (AttachmentTextRead, error) {
	if offset < 0 {
		return AttachmentTextRead{}, platform.Invalid("offset", "must be zero or positive")
	}
	if maxChars <= 0 {
		maxChars = DefaultAttachmentReadChars
	}
	if maxChars > MaxAttachmentReadChars {
		maxChars = MaxAttachmentReadChars
	}
	attachment, err := s.conversationAttachment(ctx, versionID, conversationID, attachmentID)
	if err != nil {
		return AttachmentTextRead{}, err
	}
	result := AttachmentTextRead{
		AttachmentID: attachment.ID,
		Name:         attachment.Name,
		MIME:         attachment.MIME,
		Status:       attachment.ExtractionStatus,
		Offset:       offset,
	}
	switch attachment.ExtractionStatus {
	case version.AttachmentDone:
		text, err := s.readExtractedText(ctx, attachment)
		if err != nil {
			return AttachmentTextRead{}, err
		}
		runes := []rune(text)
		result.TotalChars = len(runes)
		if offset < len(runes) {
			end := offset + maxChars
			if end > len(runes) {
				end = len(runes)
			}
			result.Text = string(runes[offset:end])
			result.Truncated = end < len(runes)
		}
	case version.AttachmentPending, version.AttachmentProcessing:
		result.Error = AttachmentParsingMessage
		result.Retryable = true
	case version.AttachmentFailed:
		result.Error = attachmentFailureText(attachment)
	case version.AttachmentSkipped:
		result.Error = "attachment was skipped because it has no extractable text; use an image part for vision"
	default:
		result.Error = "attachment text is not available yet"
	}
	return result, nil
}

// AttachmentParsingMessage is the explicit "still parsing, retry later" result
// returned by attachment.read_text instead of a 409 (§8.5).
const AttachmentParsingMessage = "附件解析中，可重试"

// attachmentFailureText renders the stored parse failure reason. It keeps the
// parser detail (for example the legacy .doc/.ppt save-as hint) instead of
// collapsing it into a generic "text is not available" message.
func attachmentFailureText(attachment version.Attachment) string {
	switch {
	case attachment.ExtractionErrorCode == "":
		return attachment.ExtractionErrorText
	case attachment.ExtractionErrorText == "":
		return attachment.ExtractionErrorCode
	default:
		return attachment.ExtractionErrorCode + ": " + attachment.ExtractionErrorText
	}
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
	message := "attachment text extraction failed: " + extractErr.Error()
	if errors.Is(extractErr, version.ErrUnsupportedAttachmentFormat) {
		code = version.AttachmentErrorUnsupported
		// Keep the parser's detailed reason (the legacy .doc/.ppt save-as hint
		// wraps this sentinel) instead of overwriting it.
		message = extractErr.Error()
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
