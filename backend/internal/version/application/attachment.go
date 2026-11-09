package versionapp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ExtractionResult is the outcome of one attachment.ingest attempt. A parse
// failure is a domain outcome (ErrorCode set, no Go error) so the worker can
// finish the task; storage failures are retryable Go errors. The document
// parser also fills Elements/Skipped/Images/Coverage (R-A7-3/3a), so the
// coverage report is visible to callers instead of being lost after the text
// is persisted.
type ExtractionResult struct {
	Attachment AttachmentView
	Text       string
	Chars      int
	// ContentSHA256 is the SHA-256 of the persisted extracted-text object.
	ContentSHA256 string
	// SourceSHA256 is the SHA-256 of the original attachment content object.
	// It is the extraction run's input_sha256 identity: the same document bytes
	// always produce the same deterministic parse plan, and a re-parsed
	// attachment cannot silently reuse another document's run.
	SourceSHA256          string
	ExtractedContentRefID int64
	ErrorCode             string
	Elements              []ExtractionElement
	Skipped               []SkippedElement
	Images                []ExtractionImage
	Coverage              ExtractionCoverage
}

// ExtractionElement is one structured content unit preserved by the parser.
// HeadingPath is the ancestor chain (for example "3.2 用户登录 > 手机号登录"),
// so downstream chunking/requirements extraction can keep source positions
// (R-A7-2). Row holds the cells of a table row when Kind is "table_row".
type ExtractionElement struct {
	Kind        string   `json:"kind"`
	Text        string   `json:"text,omitempty"`
	Level       int      `json:"level,omitempty"`
	HeadingPath string   `json:"heading_path,omitempty"`
	Source      string   `json:"source,omitempty"`
	Row         []string `json:"row,omitempty"`
	Page        int      `json:"page,omitempty"`
	Slide       int      `json:"slide,omitempty"`
	Sheet       string   `json:"sheet,omitempty"`
}

// ExtractionImage is one embedded image reference found by the parser. Data is
// the original object bytes so the vision chain can analyze it; this package
// never runs vision itself (R-E2-1/R-E3-1).
type ExtractionImage struct {
	Seq    int    `json:"seq"`
	Name   string `json:"name,omitempty"`
	Source string `json:"source,omitempty"`
	MIME   string `json:"mime"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	Size   int    `json:"size,omitempty"`
	Data   []byte `json:"-"`
}

// SkippedElement is one human-visible element the parser could not turn into
// text or an image reference. Every skip carries a reason the UI/DB can show
// (R-A7-3a/3b); a skip is never silently dropped.
type SkippedElement struct {
	Kind   string `json:"kind"`
	Source string `json:"source,omitempty"`
	Reason string `json:"reason"`
}

// ExtractionCoverage is the structured found/parsed/skipped report of one
// parse (R-A7-3a). Found always equals Parsed+Skipped.
type ExtractionCoverage struct {
	Found         int            `json:"found"`
	Parsed        int            `json:"parsed"`
	Skipped       int            `json:"skipped"`
	ImagesFound   int            `json:"images_found"`
	ImagesParsed  int            `json:"images_parsed"`
	SkipReasons   []string       `json:"skip_reasons,omitempty"`
	ElementCounts map[string]int `json:"element_counts,omitempty"`
}

// DocumentExtractor is the attachment parser port. It returns the text plus the
// structured elements, skipped elements, embedded image references and
// coverage report in one pass (R-A7-3/3a). It supersedes TextExtractor for the
// document parser; the infrastructure attachments parser implements it.
type DocumentExtractor interface {
	Extract(ctx context.Context, name, mime string, r io.Reader) (ExtractionResult, error)
}

// ImageCaption is the vision-model result for one extracted image. Caption is
// the only required part; Confidence and Model are optional provenance.
type ImageCaption struct {
	Caption    string
	Confidence *float64
	Model      string
}

// ErrVisionUnavailable reports that the vision analyzer could not resolve a
// vision-capable default model (no default model or a model without
// supports_vision). It is a typed configuration outcome, not a transient
// upstream failure.
var ErrVisionUnavailable = errors.New("no vision-capable model is configured")

// Stable vision availability reason codes (M-07). They are part of the error
// details contract and are localized by the frontend, so they must not change
// once released.
const (
	// VisionReasonNoDefaultConfig means there is no usable default LLM
	// configuration at all (none configured or the default config is disabled).
	VisionReasonNoDefaultConfig = "no_default_config"
	// VisionReasonDefaultModelMissing means the default configuration has no
	// default model configured, or that model is no longer in the catalog.
	VisionReasonDefaultModelMissing = "default_model_missing"
	// VisionReasonDefaultModelDisabled means the default model is deprecated or
	// disabled and cannot be selected.
	VisionReasonDefaultModelDisabled = "default_model_disabled"
	// VisionReasonDefaultModelNotVision means the default model is selectable
	// but does not support vision.
	VisionReasonDefaultModelNotVision = "default_model_not_vision"
	// VisionReasonGatewayUnreachable means the availability check could not
	// reach a conclusion (transport/catalog failure); the caller retries.
	VisionReasonGatewayUnreachable = "gateway_unreachable"
)

// ImageAvailability is the typed outcome of the vision model availability probe
// (M-07). A deterministic configuration/capability failure carries the stable
// reason code plus the configuration identity and the missing capabilities; a
// transient failure carries Retryable instead of being reported as a missing
// model. CauseCode keeps the underlying LLM/platform error code for logs.
type ImageAvailability struct {
	Available  bool
	ReasonCode string
	CauseCode  string
	ConfigID   string
	ConfigName string
	ModelID    string
	ModelName  string
	Missing    []string
	Retryable  bool
}

// VisionCaptioner is the version context's port for per-image analysis. The
// production adapter lives in infrastructure and resolves the default LLM model
// with a vision requirement, then sends the original bytes as llm.ChatImage;
// the version application never imports the LLM context (Agent.md §6.2/§6.9.7).
// Available reports whether a vision-capable default model exists and, when it
// does not, why (M-07); it is checked before persistence so a no-vision
// deployment never pretends the images were analyzed (R-E2-2).
type VisionCaptioner interface {
	Available(ctx context.Context) (ImageAvailability, error)
	Caption(ctx context.Context, mime string, data []byte) (ImageCaption, error)
}

// AttachmentFactRecorder is the append-only version_fact port used for
// attachment_image_caption facts. *FactService implements it in production.
type AttachmentFactRecorder interface {
	Record(ctx context.Context, fact version.Fact) (version.Fact, bool, error)
}

// AttachmentImageView is the API/agent-visible state of one extracted image.
// SHA256 resolves the content-ledger reference so clients can preview the
// original bytes through the content endpoint; the bytes themselves are never
// returned in the JSON row.
type AttachmentImageView struct {
	ID           int64
	AttachmentID int64
	Seq          int
	ContentRefID int64
	SHA256       string
	MIME         string
	Width        int
	Height       int
	SourceKind   string
	Status       string
	Caption      string
	Error        string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// VisionRequiredError is the typed 400 returned by the send path when a
// document with embedded images is referenced but no vision-capable model is
// configured. It unwraps both version.ErrVisionRequired (for errors.Is) and the
// platform error (status/code/details for the HTTP envelope).
type VisionRequiredError struct {
	VersionID    int64
	AttachmentID int64
	api          *platform.Error
}

func (e *VisionRequiredError) Error() string { return e.api.Error() }

// Unwrap exposes both the domain sentinel and the API error.
func (e *VisionRequiredError) Unwrap() []error {
	return []error{version.ErrVisionRequired, e.api}
}

func newVisionRequiredError(attachment version.Attachment, availability ImageAvailability) *VisionRequiredError {
	message := visionUnavailableMessage(availability)
	apiErr := platform.NewError(http.StatusBadRequest, version.AttachmentErrorVisionRequired, message)
	model := availability.ModelName
	if model == "" {
		model = availability.ModelID
	}
	missing := availability.Missing
	if missing == nil {
		missing = []string{}
	}
	apiErr.Details = map[string]any{
		"reason_code":          availability.ReasonCode,
		"config_id":            availability.ConfigID,
		"config_name":          availability.ConfigName,
		"model":                model,
		"missing_capabilities": missing,
		"version_id":           platformid.Format(attachment.VersionID),
		"attachment_id":        platformid.Format(attachment.ID),
		"next_action":          visionNextAction(availability.ReasonCode),
	}
	return &VisionRequiredError{
		VersionID: attachment.VersionID, AttachmentID: attachment.ID, api: apiErr,
	}
}

// AttachmentSettings is the narrow settings-center port used for the vision
// extraction limits. It is satisfied by settingsapp.Service; a nil reader keeps
// the catalog defaults.
type AttachmentSettings interface {
	Int(ctx context.Context, key string) (int64, error)
}

// Vision extraction settings keys and fallbacks. They mirror the frozen catalog
// defaults so an unwired deployment behaves exactly as before W1-2.
const (
	SettingMaxImagesPerAttachment = "vision.max_images_per_attachment"
	SettingMaxImageBytes          = "vision.max_image_bytes"
	fallbackMaxImages             = 50
	fallbackMaxImageBytes         = 10 << 20
)

// AttachmentService registers message attachments against the content ledger
// and extracts their text into a second content reference.
type AttachmentService struct {
	repo      version.AttachmentRepository
	storage   ContentStore
	queue     TaskEnqueuer
	extractor DocumentExtractor
	images    ImageProber
	events    eventingapp.Publisher
	ids       platformid.Generator
	clock     clock.Clock
	vision    VisionCaptioner
	facts     AttachmentFactRecorder
	limits    AttachmentSettings
}

// NewAttachmentService wires the attachment use cases.
func NewAttachmentService(
	repo version.AttachmentRepository,
	storage ContentStore,
	queue TaskEnqueuer,
	extractor DocumentExtractor,
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

// SetVisionCaptioner wires the per-image vision analyzer. A nil captioner is a
// deployment without a vision model: documents with embedded images fail
// explicitly with vision_required instead of silently skipping the images.
func (s *AttachmentService) SetVisionCaptioner(vision VisionCaptioner) {
	s.vision = vision
}

// SetFactRecorder wires the append-only fact store used for
// attachment_image_caption facts. Recording a caption is part of analysis
// success: a nil recorder fails the image explicitly.
func (s *AttachmentService) SetFactRecorder(facts AttachmentFactRecorder) {
	s.facts = facts
}

// SetLimits wires the settings-center reader for the vision extraction limits
// (vision.max_images_per_attachment and vision.max_image_bytes). A nil reader or
// an unreadable key keeps the catalog defaults.
func (s *AttachmentService) SetLimits(limits AttachmentSettings) {
	s.limits = limits
}

// visionMaxImages resolves the configured per-attachment image cap.
func (s *AttachmentService) visionMaxImages(ctx context.Context) int {
	if s == nil || s.limits == nil {
		return fallbackMaxImages
	}
	value, err := s.limits.Int(ctx, SettingMaxImagesPerAttachment)
	if err != nil || value <= 0 {
		return fallbackMaxImages
	}
	return int(value)
}

// visionMaxImageBytes resolves the configured single-image byte cap.
func (s *AttachmentService) visionMaxImageBytes(ctx context.Context) int64 {
	if s == nil || s.limits == nil {
		return fallbackMaxImageBytes
	}
	value, err := s.limits.Int(ctx, SettingMaxImageBytes)
	if err != nil || value <= 0 {
		return fallbackMaxImageBytes
	}
	return value
}

// RegisterFromMessage validates one message attachment part against the
// content ledger, stores the requirement_attachment runtime row and ensures
// the ingest task. The conversation post path pre-registers rows via
// PrepareUserMessageParts instead (so the pending gate can see them); this
// entry point remains for direct registration and tests. Image attachments are
// stored as an extraction-skipped row because there is no text to extract.
// Legacy .doc/.ppt are marked failed with unsupported_attachment_format
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
	content, err := s.attachmentContent(ctx, part)
	if err != nil {
		return AttachmentView{}, err
	}
	attachment, err := s.createAttachment(ctx, versionID, conversationID, messageID, part, name, content, actorID)
	if err != nil {
		return AttachmentView{}, err
	}
	return toAttachmentView(attachment), nil
}

// createAttachment inserts one requirement_attachment row and its content-ledger
// reference. messageID is zero for a row pre-registered by the message post
// preflight; LinkFromMessage binds it after the message insert wins. The row is
// persisted before the ingest task is enqueued (R-E4-1) and the ingest task
// resolves the attachment by attachment_id, never through the message join, so
// the worker can run (and attachment.list/read_text can see the row) while the
// message binding is still deferred. conversationID is stored on the row from
// the start so the unbound draft is only visible/readable to its own
// conversation instead of every conversation of the version.
func (s *AttachmentService) createAttachment(ctx context.Context, versionID, conversationID, messageID int64, part version.MessagePart, name string, content ContentObject, actorID int64) (version.Attachment, error) {
	now := s.clock.Now()
	attachmentID := s.ids.Next()
	ref, err := s.storage.AddRef(ctx, part.SHA256, version.AttachmentOwnerKind, attachmentID, version.AttachmentContentRole)
	if err != nil {
		if isPlatformNotFound(err) {
			return version.Attachment{}, platform.Invalid("sha256", "content object was not uploaded")
		}
		return version.Attachment{}, err
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

	var linkedMessageID *int64
	if messageID > 0 {
		value := messageID
		linkedMessageID = &value
	}
	var owningConversationID *int64
	if conversationID > 0 {
		value := conversationID
		owningConversationID = &value
	}
	attachment := version.Attachment{
		ID:                    attachmentID,
		VersionID:             versionID,
		ConversationMessageID: linkedMessageID,
		ConversationID:        owningConversationID,
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
		return version.Attachment{}, mapAttachmentRepositoryError(err)
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
		// v0.22 (Task 36 fix round 1): extraction is owned by the deterministic
		// server-side attachment worker (runAttachmentExtractionWorker), not by
		// an attachment.ingest agent work item. The pending row is the queue:
		// no legacy enqueue here, so a deployment without the agent worker
		// still extracts every upload.
	}
	return attachment, nil
}

// LinkFromMessage binds the pre-registered rows to the inserted message. It
// deliberately does not re-open the content or enqueue extraction: the
// preflight already did both, so the retry reuses the same row and parse.
func (s *AttachmentService) LinkFromMessage(ctx context.Context, versionID, messageID int64, attachmentIDs []int64) error {
	if versionID <= 0 || messageID <= 0 {
		return platform.Invalid("attachment", "version and message are required")
	}
	for _, attachmentID := range attachmentIDs {
		if attachmentID <= 0 {
			continue
		}
		if err := s.repo.LinkMessage(ctx, attachmentID, messageID, s.clock.Now()); err != nil {
			return mapAttachmentRepositoryError(err)
		}
	}
	return nil
}

// PrepareUserMessageParts prepares a message before it is stored: image
// attachment parts become R5 image parts (spec §8.1/§8.3), and every attachment
// part gets an attachment row. Rows for brand-new content are pre-registered
// here, which starts extraction and lets the submit gate observe a real pending
// status (spec §8.5). Any pending/processing reference returns the typed 409
// attachment_pending before the message is inserted; the returned attachment IDs
// are the unlinked rows that must be linked after the insert wins.
func (s *AttachmentService) PrepareUserMessageParts(ctx context.Context, versionID, conversationID int64, parts []version.MessagePart, actorID int64) ([]version.MessagePart, []int64, error) {
	if versionID <= 0 || conversationID <= 0 {
		return nil, nil, platform.Invalid("conversation_id", "must be positive decimal IDs")
	}
	prepared := make([]version.MessagePart, 0, len(parts))
	attachmentIDs := make([]int64, 0)
	for _, part := range parts {
		if part.Type != version.MessagePartAttachment {
			prepared = append(prepared, part)
			continue
		}
		name, err := version.SanitizeAttachmentName(part.Name)
		if err != nil {
			return nil, nil, err
		}
		image := version.DetectAttachmentKind(name, part.MIME).IsImage()
		var imagePart *version.MessagePart
		var content ContentObject
		if image {
			built, object, err := s.imagePartFromAttachment(ctx, part)
			if err != nil {
				return nil, nil, err
			}
			imagePart = &built
			content = object
		} else {
			content, err = s.attachmentContent(ctx, part)
			if err != nil {
				return nil, nil, err
			}
		}

		// A previous message already registered this content: its extraction
		// state is the gate, and a done/failed/skipped row is reused without
		// creating another row or extraction.
		linked, err := s.attachmentBySHA(ctx, versionID, conversationID, part.SHA256)
		if err != nil {
			return nil, nil, err
		}
		if linked != nil {
			if isAttachmentPending(linked.ExtractionStatus) {
				return nil, nil, attachmentPendingError(*linked)
			}
			// A document that already carries extracted images can only be sent
			// when a vision model is available; otherwise the images could never
			// be analyzed and the whole feature would be fake-available (R-E2-2).
			if imagePart == nil {
				if err := s.ensureVisionForImageBearingAttachment(ctx, *linked); err != nil {
					return nil, nil, err
				}
			}
			if imagePart != nil {
				prepared = append(prepared, *imagePart)
			} else {
				prepared = append(prepared, part)
			}
			continue
		}

		// A blocked earlier attempt already pre-registered this content: reuse
		// that row (and its extraction) instead of inserting a duplicate.
		draft, err := s.unlinkedAttachmentBySHA(ctx, versionID, part.SHA256)
		if err != nil {
			return nil, nil, err
		}
		if draft != nil {
			if isAttachmentPending(draft.ExtractionStatus) {
				return nil, nil, attachmentPendingError(*draft)
			}
			if imagePart == nil {
				if err := s.ensureVisionForImageBearingAttachment(ctx, *draft); err != nil {
					return nil, nil, err
				}
			}
			attachmentIDs = append(attachmentIDs, draft.ID)
			if imagePart != nil {
				prepared = append(prepared, *imagePart)
			} else {
				prepared = append(prepared, part)
			}
			continue
		}

		attachment, err := s.createAttachment(ctx, versionID, conversationID, 0, part, name, content, actorID)
		if err != nil {
			return nil, nil, err
		}
		if isAttachmentPending(attachment.ExtractionStatus) {
			// The row now exists and extraction is enqueued; the message is
			// rejected until the retry sees the same row as done.
			return nil, nil, attachmentPendingError(attachment)
		}
		attachmentIDs = append(attachmentIDs, attachment.ID)
		if imagePart != nil {
			prepared = append(prepared, *imagePart)
		} else {
			prepared = append(prepared, part)
		}
	}
	if err := version.ValidateUserMessageParts(prepared); err != nil {
		return nil, nil, err
	}
	return prepared, attachmentIDs, nil
}

// isAttachmentPending reports whether extraction still has to finish.
func isAttachmentPending(status string) bool {
	return status == version.AttachmentPending || status == version.AttachmentProcessing
}

// imagePartFromAttachment reads the uploaded image and builds the validated
// image part that replaces the attachment part in the stored message. It also
// returns the content metadata so the caller can create the attachment row
// without opening the object a second time.
func (s *AttachmentService) imagePartFromAttachment(ctx context.Context, part version.MessagePart) (version.MessagePart, ContentObject, error) {
	content, reader, err := s.storage.Open(ctx, part.SHA256)
	if err != nil {
		if isPlatformNotFound(err) {
			return version.MessagePart{}, ContentObject{}, platform.Invalid("sha256", "content object was not uploaded")
		}
		return version.MessagePart{}, ContentObject{}, err
	}
	defer reader.Close()
	if content.Size != part.Size {
		return version.MessagePart{}, ContentObject{}, platform.Invalid("size", "does not match the uploaded content size")
	}
	if s.images == nil {
		return version.MessagePart{}, ContentObject{}, errors.New("attachment service has no image prober")
	}
	data, err := io.ReadAll(io.LimitReader(reader, version.MaxAttachmentBytes+1))
	if err != nil {
		return version.MessagePart{}, ContentObject{}, err
	}
	if int64(len(data)) > version.MaxAttachmentBytes {
		return version.MessagePart{}, ContentObject{}, platform.Invalid("size", "image exceeds the 10 MiB upload limit")
	}
	mime, width, height, err := s.images.Probe(ctx, part.MIME, data)
	if err != nil {
		return version.MessagePart{}, ContentObject{}, err
	}
	return version.MessagePart{
		Type:    version.MessagePartImage,
		MIME:    mime,
		DataRef: part.SHA256,
		Width:   width,
		Height:  height,
	}, content, nil
}

// attachmentContent verifies that the uploaded object exists and has the size
// the message part declares, returning its metadata for row creation.
func (s *AttachmentService) attachmentContent(ctx context.Context, part version.MessagePart) (ContentObject, error) {
	content, reader, err := s.storage.Open(ctx, part.SHA256)
	if err != nil {
		if isPlatformNotFound(err) {
			return ContentObject{}, platform.Invalid("sha256", "content object was not uploaded")
		}
		return ContentObject{}, err
	}
	_ = reader.Close()
	if content.Size != part.Size {
		return ContentObject{}, platform.Invalid("size", "does not match the uploaded content size")
	}
	return content, nil
}

// attachmentBySHA finds an attachment already linked to this conversation via
// one of its messages whose original content matches sha256. ListByConversation
// also returns this conversation's pre-registered rows whose message binding is
// still deferred (R-E4-1); those are not re-send candidates and are skipped
// here so they can never be mistaken for an already-linked attachment. They are
// reused by unlinkedAttachmentBySHA instead.
func (s *AttachmentService) attachmentBySHA(ctx context.Context, versionID, conversationID int64, sha256 string) (*version.Attachment, error) {
	attachments, err := s.repo.ListByConversation(ctx, versionID, conversationID)
	if err != nil {
		return nil, mapAttachmentRepositoryError(err)
	}
	for _, attachment := range attachments {
		if attachment.ConversationMessageID == nil {
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

// unlinkedAttachmentBySHA finds a row pre-registered by an earlier blocked
// message post for the same version and content.
func (s *AttachmentService) unlinkedAttachmentBySHA(ctx context.Context, versionID int64, sha256 string) (*version.Attachment, error) {
	attachments, err := s.repo.ListUnlinkedByVersion(ctx, versionID)
	if err != nil {
		return nil, mapAttachmentRepositoryError(err)
	}
	for _, attachment := range attachments {
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

// ListImages returns the attachment_image rows of one attachment ordered by
// seq. The status/caption/error fields are the per-image visibility required by
// R-E3-2/R-E3-6; SHA256 lets clients preview the original bytes through the
// content endpoint.
func (s *AttachmentService) ListImages(ctx context.Context, attachmentID int64) ([]AttachmentImageView, error) {
	if attachmentID <= 0 {
		return nil, platform.Invalid("attachment_id", "must be a positive decimal ID")
	}
	if _, err := s.repo.Get(ctx, attachmentID); err != nil {
		return nil, mapAttachmentRepositoryError(err)
	}
	return s.attachmentImageViews(ctx, attachmentID)
}

// Reanalyze re-runs vision analysis for every claimable image row of one
// attachment. Failed/skipped rows are reset in place (no duplicate rows or
// content objects) and rows already sitting in the extracted state are analyzed
// too, so a crash between the reset and the vision calls is recoverable. The
// vision.max_images_per_attachment and vision.max_image_bytes caps are applied
// again on this path, ordered by seq, because the reset re-queues rows the
// extraction had skipped for exactly those limits; the excess rows are written
// back as skipped with the limit as their reason and never reach the model. The
// attachment returns to done only when at least one row was analyzed and every
// image succeeded. Without a vision model the call is rejected with the same
// typed ErrVisionRequired as the send path, so the UI can point at the missing
// configuration; a transient availability failure is a retryable error instead.
func (s *AttachmentService) Reanalyze(ctx context.Context, attachmentID int64) ([]AttachmentImageView, error) {
	if attachmentID <= 0 {
		return nil, platform.Invalid("attachment_id", "must be a positive decimal ID")
	}
	attachment, err := s.repo.Get(ctx, attachmentID)
	if err != nil {
		return nil, mapAttachmentRepositoryError(err)
	}
	existing, err := s.repo.ListImages(ctx, attachmentID)
	if err != nil {
		return nil, mapAttachmentRepositoryError(err)
	}
	if len(existing) == 0 {
		return []AttachmentImageView{}, nil
	}
	availability, err := s.visionStatus(ctx)
	if err != nil {
		return nil, err
	}
	if !availability.Available {
		s.publishVisionUnavailable(ctx, attachment, availability)
		return nil, newVisionRequiredError(attachment, availability)
	}
	if _, err := s.repo.ResetImagesForReanalysis(ctx, attachmentID, s.clock.Now()); err != nil {
		return nil, mapAttachmentRepositoryError(err)
	}
	rows, err := s.repo.ListImages(ctx, attachmentID)
	if err != nil {
		return nil, mapAttachmentRepositoryError(err)
	}
	// The caps are re-applied deterministically in seq order: the reset above
	// re-queued rows the extraction skipped for the limits, so trusting the row
	// status would send cap-excluded images to the model on every retry.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Seq < rows[j].Seq })
	maxImages := s.visionMaxImages(ctx)
	maxImageBytes := s.visionMaxImageBytes(ctx)
	failures := 0
	analyzed := 0
	firstFailure := ""
	for index, image := range rows {
		if image.Status != version.AttachmentImageExtracted {
			continue
		}
		if index >= maxImages {
			skipReason := fmt.Sprintf(
				"image %d of %d skipped: attachment exceeds %s (max %d images)",
				image.Seq, len(rows), SettingMaxImagesPerAttachment, maxImages)
			if err := s.repo.MarkImageSkipped(ctx, image.ID, skipReason, s.clock.Now()); err != nil {
				return nil, mapAttachmentRepositoryError(err)
			}
			continue
		}
		analyzed++
		if err := s.analyzeExtractedImage(ctx, attachment, image, maxImageBytes); err != nil {
			failures++
			if firstFailure == "" {
				firstFailure = err.Error()
			}
		}
	}
	if failures > 0 {
		message := fmt.Sprintf("image analysis failed for %d of %d image(s): %s",
			failures, len(rows), firstFailure)
		if _, err := s.failAttachment(ctx, attachment, version.AttachmentErrorVisionAnalysis, message, nil); err != nil {
			return nil, err
		}
	} else if analyzed > 0 {
		textRefID := derefInt64(attachment.ExtractedContentRefID)
		if textRefID <= 0 {
			return nil, fmt.Errorf("attachment %d has no extracted text reference; re-upload the attachment", attachmentID)
		}
		if err := s.repo.MarkDone(ctx, attachmentID, textRefID, s.clock.Now()); err != nil {
			return nil, mapAttachmentRepositoryError(err)
		}
	}
	return s.attachmentImageViews(ctx, attachmentID)
}

// ensureVisionForImageBearingAttachment is the send-path hard gate for document
// attachments: an attachment that already carries extracted images may only be
// sent when a vision-capable model is configured (R-E2-2/R-E2-3). A document
// without images is unaffected.
func (s *AttachmentService) ensureVisionForImageBearingAttachment(ctx context.Context, attachment version.Attachment) error {
	if version.DetectAttachmentKind(attachment.Name, attachment.MIME).IsImage() {
		return nil
	}
	images, err := s.repo.ListImages(ctx, attachment.ID)
	if err != nil {
		return mapAttachmentRepositoryError(err)
	}
	if len(images) == 0 {
		return nil
	}
	availability, err := s.visionStatus(ctx)
	if err != nil {
		return err
	}
	if !availability.Available {
		s.publishVisionUnavailable(ctx, attachment, availability)
		return newVisionRequiredError(attachment, availability)
	}
	return nil
}

// attachmentImageViews joins the image rows with their content references so one
// API/agent view carries the original object SHA-256 without opening the file.
func (s *AttachmentService) attachmentImageViews(ctx context.Context, attachmentID int64) ([]AttachmentImageView, error) {
	rows, err := s.repo.ListImages(ctx, attachmentID)
	if err != nil {
		return nil, mapAttachmentRepositoryError(err)
	}
	refs, err := s.storage.ListRefs(ctx, version.AttachmentOwnerKind, attachmentID)
	if err != nil {
		return nil, err
	}
	objectByRef := make(map[int64]string, len(refs))
	for _, ref := range refs {
		objectByRef[ref.ID] = ref.ObjectID
	}
	views := make([]AttachmentImageView, 0, len(rows))
	for _, row := range rows {
		views = append(views, AttachmentImageView{
			ID: row.ID, AttachmentID: row.AttachmentID, Seq: row.Seq, ContentRefID: row.ContentRefID,
			SHA256: objectByRef[row.ContentRefID], MIME: row.MIME, Width: row.Width, Height: row.Height,
			SourceKind: row.SourceKind, Status: row.Status, Caption: row.Caption, Error: row.Error,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	return views, nil
}

// attachmentImageInfos projects the joined image views into the compact
// reference/caption shape shared by GetAttachmentInfo, attachment.read_text and
// attachment.image_ref (C-01). It always returns a non-nil slice so JSON callers
// see an empty array instead of null.
func attachmentImageInfos(views []AttachmentImageView) []AttachmentImageInfo {
	infos := make([]AttachmentImageInfo, 0, len(views))
	for _, image := range views {
		infos = append(infos, AttachmentImageInfo{
			ID: image.ID, Seq: image.Seq, SHA256: image.SHA256, MIME: image.MIME,
			Width: image.Width, Height: image.Height, SourceKind: image.SourceKind,
			Status: image.Status, Caption: image.Caption, Error: image.Error,
		})
	}
	return infos
}

// Attachment read limits for the agent tools (character based).
const (
	DefaultAttachmentReadChars = 4000
	MaxAttachmentReadChars     = 20000
)

// AttachmentInfo is the agent-facing attachment view used by the ingest run.
// It carries the extracted text and its total character count. The runtime
// adapts this to its own AttachmentInfo port. Images exposes the extracted
// image references plus captions so task packs can include refs/captions and
// budget them; ImageTokens is len(Images)*AttachmentImageTokenBudget (R-E3-4).
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
	Images                []AttachmentImageInfo
	ImageTokens           int
}

// AttachmentImageInfo is the compact image reference/caption pair exposed to
// the agent runtime (R-E3-4/R-E3-5). Bytes stay in the content ledger.
type AttachmentImageInfo struct {
	ID         int64
	Seq        int
	SHA256     string
	MIME       string
	Width      int
	Height     int
	SourceKind string
	Status     string
	Caption    string
	Error      string
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
	// Images carries the persisted image rows with their platform-generated
	// captions. It lets attachment.read_text expose the analysis even when the
	// attachment text itself is empty (scanned/image-only documents).
	Images []AttachmentImageInfo
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

// VersionAttachmentView is one read-only row of the version attachment panel
// (C-05). It intentionally exposes only display/status fields: the panel links
// to the existing version-scoped image routes by ID, so no content bytes or
// internal ledger references are returned here. ImageCount is the number of
// attachment_image rows extracted from the attachment (all analysis states).
type VersionAttachmentView struct {
	ID               int64
	Name             string
	MIME             string
	Size             int64
	ExtractionStatus string
	ErrorCode        string
	ErrorText        string
	ImageCount       int
}

// AttachmentImageRef identifies the original content object of an image
// attachment so the runtime can pass an image_ref to the model. Images mirrors
// the persisted image rows (with captions) so the tool can return the same
// analysis as attachment.read_text.
type AttachmentImageRef struct {
	AttachmentID int64
	SHA256       string
	Name         string
	MIME         string
	Size         int64
	Images       []AttachmentImageInfo
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
	images, err := s.attachmentImageViews(ctx, attachment.ID)
	if err != nil {
		return AttachmentInfo{}, err
	}
	info.Images = attachmentImageInfos(images)
	info.ImageTokens = len(info.Images) * version.AttachmentImageTokenBudget
	return info, nil
}

// ListAttachments returns the attachments visible to one conversation, in
// attachment-ID order. That is every row linked to one of the conversation's
// messages plus the message attachments pre-registered for this conversation
// before their message insert (conversation_message_id == 0, conversation_id
// set): the attachment.ingest race window must not make an uploaded attachment
// invisible to attachment.list (R-E4-1/R-E4-2). A draft pre-registered by
// another conversation of the same version is not visible here.
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

// ListVersionAttachments is the read-only C-05 attachment panel list: every
// attachment row of one version, newest first (ID descending), with its
// extraction status/error pair and the number of extracted images. The version
// is validated first so an unknown/foreign version is a 404 exactly like the
// version-scoped image routes, not an empty 200. Ids are returned only as
// identity; the panel reads image details through the existing scoped routes.
func (s *AttachmentService) ListVersionAttachments(ctx context.Context, versionID int64) ([]VersionAttachmentView, error) {
	if versionID <= 0 {
		return nil, platform.Invalid("id", "must be a positive decimal ID")
	}
	exists, err := s.repo.VersionExists(ctx, versionID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, platform.NotFound("version")
	}
	attachments, err := s.repo.ListByVersion(ctx, versionID)
	if err != nil {
		return nil, mapAttachmentRepositoryError(err)
	}
	imageCounts, err := s.repo.CountImagesByVersion(ctx, versionID)
	if err != nil {
		return nil, err
	}
	views := make([]VersionAttachmentView, 0, len(attachments))
	for _, attachment := range attachments {
		views = append(views, VersionAttachmentView{
			ID:               attachment.ID,
			Name:             attachment.Name,
			MIME:             attachment.MIME,
			Size:             attachment.Size,
			ExtractionStatus: attachment.ExtractionStatus,
			ErrorCode:        attachment.ExtractionErrorCode,
			ErrorText:        attachment.ExtractionErrorText,
			ImageCount:       imageCounts[attachment.ID],
		})
	}
	sort.SliceStable(views, func(i, j int) bool { return views[i].ID > views[j].ID })
	return views, nil
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
	images, err := s.attachmentImageViews(ctx, attachment.ID)
	if err != nil {
		return AttachmentTextRead{}, err
	}
	result.Images = attachmentImageInfos(images)
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
	images, err := s.attachmentImageViews(ctx, attachment.ID)
	if err != nil {
		return AttachmentImageRef{}, err
	}
	return AttachmentImageRef{
		AttachmentID: attachment.ID,
		SHA256:       sha256,
		Name:         attachment.Name,
		MIME:         attachment.MIME,
		Size:         attachment.Size,
		Images:       attachmentImageInfos(images),
	}, nil
}

// conversationAttachment resolves one attachment by its ID within the
// conversation. The repository also returns rows pre-registered for this
// conversation while their message binding is still deferred, so
// attachment.read_text works by attachment_id during the ingest race window
// instead of returning not_found (R-E4-1) — but a draft owned by another
// conversation of the version stays not_found.
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
		if sha, shaErr := s.originalObjectID(ctx, attachment.ID, attachment.ContentRefID); shaErr == nil {
			result.SourceSHA256 = sha
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

	// Resolve vision availability before the row is claimed: a transient
	// availability-check failure must remain retryable, so the attachment has to
	// stay claimable (pending). The definitive no-vision answer is carried to
	// the image-persistence step below and only affects documents that actually
	// contain images.
	visionAvailability, err := s.visionStatus(ctx)
	if err != nil {
		return ExtractionResult{}, err
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
			if sha, shaErr := s.originalObjectID(ctx, current.ID, current.ContentRefID); shaErr == nil {
				result.SourceSHA256 = sha
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
	parsed, extractErr := s.extractor.Extract(ctx, attachment.Name, attachment.MIME, reader)
	text := parsed.Text
	if extractErr == nil && int64(len(text)) > version.MaxExtractedTextBytes {
		extractErr = version.ErrAttachmentTooLarge
	}
	// Empty text is only a failure when the document yielded no images either.
	// A scanned/image-only PDF legitimately has no text layer; its embedded
	// images are the extraction output and feed the vision chain (spec X-1).
	if extractErr == nil && strings.TrimSpace(text) == "" && len(parsed.Images) == 0 {
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
	// Persist the text reference before image analysis so a failed analysis can
	// fail the attachment without losing the extracted text; a later successful
	// reanalyze then only has to finish the remaining images.
	if err := s.repo.SetExtractedContentRef(ctx, attachmentID, textRef.ID, s.clock.Now()); err != nil {
		return ExtractionResult{}, mapAttachmentRepositoryError(err)
	}
	attachment.ExtractedContentRefID = &textRef.ID

	// Embedded images are persisted one row per image (content refs live in the
	// content ledger, never in the DB) and then analyzed one by one (R-E2-1,
	// R-E3-2). No vision model is an explicit whole-attachment failure, not a
	// silent skip (R-E2-2/R-E3-3).
	imageStatus := version.AttachmentImageExtracted
	imageSkipReason := ""
	if !visionAvailability.Available {
		imageStatus = version.AttachmentImageSkipped
		imageSkipReason = visionUnavailableMessage(visionAvailability)
	}
	maxImages := s.visionMaxImages(ctx)
	maxImageBytes := s.visionMaxImageBytes(ctx)
	imageRows, err := s.persistExtractedImages(ctx, attachment, parsed.Images, imageStatus, imageSkipReason,
		maxImages, maxImageBytes)
	if err != nil {
		return ExtractionResult{}, err
	}
	if len(imageRows) > 0 {
		if !visionAvailability.Available {
			message := "attachment contains embedded images, but " + visionUnavailableMessage(visionAvailability)
			s.publishVisionUnavailable(ctx, attachment, visionAvailability)
			return s.failAttachment(ctx, attachment, version.AttachmentErrorVisionRequired, message, parsed.Images)
		}
		failures := 0
		firstFailure := ""
		for _, image := range imageRows {
			if image.Status != version.AttachmentImageExtracted {
				continue
			}
			if err := s.analyzeExtractedImage(ctx, attachment, image, maxImageBytes); err != nil {
				failures++
				if firstFailure == "" {
					firstFailure = err.Error()
				}
			}
		}
		if failures > 0 {
			message := fmt.Sprintf("image analysis failed for %d of %d image(s): %s",
				failures, len(imageRows), firstFailure)
			return s.failAttachment(ctx, attachment, version.AttachmentErrorVisionAnalysis, message, parsed.Images)
		}
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
		"coverage":      parsed.Coverage,
		"images":        len(imageRows),
	})
	return ExtractionResult{
		Attachment:            toAttachmentView(attachment),
		Text:                  text,
		Chars:                 chars,
		ContentSHA256:         uploaded.SHA256,
		SourceSHA256:          objectID,
		ExtractedContentRefID: textRef.ID,
		Elements:              parsed.Elements,
		Skipped:               parsed.Skipped,
		Images:                parsed.Images,
		Coverage:              parsed.Coverage,
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
	return s.failAttachment(ctx, attachment, code, message, nil)
}

// failAttachment marks one attachment failed with a stable code and reason,
// publishes requirement.attachment_failed and returns the domain outcome (a
// failure is not a retryable Go error). Image failures additionally carry the
// parsed image references so callers still see what was extracted.
func (s *AttachmentService) failAttachment(ctx context.Context, attachment version.Attachment, code, message string, parsedImages []ExtractionImage) (ExtractionResult, error) {
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
	return ExtractionResult{
		Attachment: toAttachmentView(attachment),
		ErrorCode:  code,
		Images:     parsedImages,
	}, nil
}

// visionStatus resolves the typed vision availability (M-07). A nil captioner
// is a deployment without the image-analysis feature: fail closed instead of
// degrading to a silent skip (R-E2-2). A transient availability-check failure is
// returned as a retryable 503 ai_unavailable so every caller neither persists a
// skipped row nor tells the user to fix a configuration; only a definitive
// configuration/capability answer becomes the typed vision_required outcome.
func (s *AttachmentService) visionStatus(ctx context.Context) (ImageAvailability, error) {
	if s.vision == nil {
		return ImageAvailability{ReasonCode: VisionReasonNoDefaultConfig}, nil
	}
	availability, err := s.vision.Available(ctx)
	if err != nil {
		return ImageAvailability{ReasonCode: VisionReasonGatewayUnreachable, Retryable: true},
			visionRetryableError(VisionReasonGatewayUnreachable)
	}
	if !availability.Available && availability.Retryable {
		return availability, visionRetryableError(availability.ReasonCode)
	}
	return availability, nil
}

// visionRetryableError builds the transient 503 the API returns when the vision
// gateway cannot be reached or its availability cannot be determined. It is
// deliberately distinct from the deterministic 400 vision_required.
func visionRetryableError(reasonCode string) error {
	if strings.TrimSpace(reasonCode) == "" {
		reasonCode = VisionReasonGatewayUnreachable
	}
	apiErr := platform.AIUnavailable("视觉模型暂时不可用，请稍后重试")
	apiErr.Details = map[string]any{"reason_code": reasonCode, "retryable": true}
	return apiErr
}

// visionUnavailableMessage renders the cause-specific Chinese message. Every
// branch carries the stable reason code so logs and the UI envelope can explain
// which setting is broken instead of collapsing into "no vision model".
func visionUnavailableMessage(availability ImageAvailability) string {
	message := ""
	switch availability.ReasonCode {
	case VisionReasonNoDefaultConfig:
		message = "当前没有可用的默认 LLM 配置，文档中的图片无法分析；请在 LLM 设置中启用默认配置并选择支持视觉的默认模型"
	case VisionReasonDefaultModelMissing:
		message = "默认模型未设置或已不在模型目录中，文档中的图片无法分析；请在 LLM 设置中设置支持视觉的默认模型"
	case VisionReasonDefaultModelDisabled:
		message = "默认模型已下线或禁用，文档中的图片无法分析；请在 LLM 设置中改用已启用且支持视觉的默认模型"
	case VisionReasonDefaultModelNotVision:
		message = "默认模型不支持视觉能力，文档中的图片无法分析；请在 LLM 设置中改用支持视觉的模型"
	case VisionReasonGatewayUnreachable:
		message = "视觉模型网关暂时不可用，文档中的图片暂时无法分析；请稍后重试"
	default:
		message = "没有可用的视觉模型，文档中的图片无法分析；请检查 LLM 设置"
	}
	reasonCode := availability.ReasonCode
	if strings.TrimSpace(reasonCode) == "" {
		reasonCode = VisionReasonGatewayUnreachable
	}
	return message + "（原因：" + reasonCode + "）"
}

// visionNextAction returns the Chinese operator hint for one reason code; the
// details.next_action contract is localized by the frontend.
func visionNextAction(reasonCode string) string {
	switch reasonCode {
	case VisionReasonNoDefaultConfig:
		return "请到 LLM 设置中启用默认配置，并选择支持视觉的默认模型后重试"
	case VisionReasonDefaultModelMissing:
		return "请到 LLM 设置中设置默认模型（不会自动使用 models[0]），并确认其支持视觉后重试"
	case VisionReasonDefaultModelDisabled:
		return "请到 LLM 设置中把默认模型改为已启用且未下线的模型后重试"
	case VisionReasonDefaultModelNotVision:
		return "请到 LLM 设置中把默认模型改为支持视觉（vision）的模型后重试"
	case VisionReasonGatewayUnreachable:
		return "请稍后重试；若持续失败，请检查 LLM 网关与模型配置"
	default:
		return "请检查 LLM 视觉模型配置后重试"
	}
}

// publishVisionUnavailable emits model.vision.unavailable so the UI can tell
// the user which configuration is broken instead of assuming the images were
// understood. The payload carries the typed reason, configuration identity and
// missing capabilities plus the next action.
func (s *AttachmentService) publishVisionUnavailable(ctx context.Context, attachment version.Attachment, availability ImageAvailability) {
	reasonCode := availability.ReasonCode
	if strings.TrimSpace(reasonCode) == "" {
		reasonCode = VisionReasonGatewayUnreachable
	}
	payload := map[string]any{
		"attachment_id": platformid.Format(attachment.ID),
		"reason":        reasonCode,
		"reason_code":   reasonCode,
		"message":       visionUnavailableMessage(availability),
		"next_action":   visionNextAction(reasonCode),
	}
	if availability.ConfigID != "" {
		payload["config_id"] = availability.ConfigID
	}
	if availability.ConfigName != "" {
		payload["config_name"] = availability.ConfigName
	}
	if availability.ModelName != "" {
		payload["model"] = availability.ModelName
	} else if availability.ModelID != "" {
		payload["model"] = availability.ModelID
	}
	if len(availability.Missing) > 0 {
		payload["missing_capabilities"] = availability.Missing
	}
	s.publishAttachmentEvent(ctx, attachment.VersionID, "model.vision.unavailable", attachment, payload)
}

// persistExtractedImages stores one attachment_image row per parsed image. The
// bytes go through the content ledger under an attachment_image role; the row
// only references them. Rows are created with the status chosen by the caller
// (extracted when a vision model is available, skipped otherwise) and are
// idempotent on (attachment_id, seq): an existing row is reused and no
// duplicate content object/reference is created. When a vision model is
// available, images beyond maxImages or above maxImageBytes are persisted as
// skipped rows with the limit and value in their reason instead of being sent
// to the model.
func (s *AttachmentService) persistExtractedImages(ctx context.Context, attachment version.Attachment, parsed []ExtractionImage, status, skipReason string, maxImages int, maxImageBytes int64) ([]version.AttachmentImageRecord, error) {
	if len(parsed) == 0 {
		return nil, nil
	}
	existing, err := s.repo.ListImages(ctx, attachment.ID)
	if err != nil {
		return nil, mapAttachmentRepositoryError(err)
	}
	bySeq := make(map[int]version.AttachmentImageRecord, len(existing))
	for _, row := range existing {
		bySeq[row.Seq] = row
	}
	sourceKind := version.AttachmentImageSourceEmbedded
	if version.DetectAttachmentKind(attachment.Name, attachment.MIME).IsImage() {
		sourceKind = version.AttachmentImageSourceStandalone
	}
	now := s.clock.Now()
	rows := make([]version.AttachmentImageRecord, 0, len(parsed))
	for index, image := range parsed {
		if row, ok := bySeq[image.Seq]; ok {
			rows = append(rows, row)
			continue
		}
		rowStatus, rowReason := status, skipReason
		if rowStatus == version.AttachmentImageExtracted {
			switch {
			case index >= maxImages:
				rowStatus = version.AttachmentImageSkipped
				rowReason = fmt.Sprintf(
					"image %d of %d skipped: attachment exceeds %s (max %d images)",
					image.Seq, len(parsed), SettingMaxImagesPerAttachment, maxImages)
			case int64(len(image.Data)) > maxImageBytes:
				rowStatus = version.AttachmentImageSkipped
				rowReason = fmt.Sprintf(
					"image %d skipped: %d bytes exceeds %s (max %d bytes)",
					image.Seq, len(image.Data), SettingMaxImageBytes, maxImageBytes)
			}
		}
		name := image.Name
		if strings.TrimSpace(name) == "" {
			name = fmt.Sprintf("attachment-%d-image-%d", attachment.ID, image.Seq)
		}
		uploaded, err := s.storage.Upload(ctx, name, image.MIME, bytes.NewReader(image.Data), version.MaxAttachmentBytes, 0)
		if err != nil {
			return nil, err
		}
		ref, err := s.storage.AddRef(ctx, uploaded.SHA256, version.AttachmentOwnerKind, attachment.ID, version.AttachmentImageRole)
		if err != nil {
			return nil, err
		}
		row := version.AttachmentImageRecord{
			ID: s.ids.Next(), AttachmentID: attachment.ID, Seq: image.Seq, ContentRefID: ref.ID,
			MIME: image.MIME, Width: image.Width, Height: image.Height, SourceKind: sourceKind,
			Status: rowStatus, Error: rowReason, CreatedAt: now, UpdatedAt: now,
		}
		if err := s.repo.CreateImage(ctx, row); err != nil {
			return nil, mapAttachmentRepositoryError(err)
		}
		s.publishImageEvent(ctx, attachment, row, "attachment.image.extracted", map[string]any{
			"mime": image.MIME, "width": image.Width, "height": image.Height, "source_kind": sourceKind,
		})
		rows = append(rows, row)
	}
	return rows, nil
}

// analyzeExtractedImage runs one image through the vision port. Success stores
// the caption, appends the attachment_image_caption fact and only then emits
// attachment.image.analyzed (a consumer must never observe an analyzed image
// whose caption fact was not persisted); any failure marks the image failed
// with the reason (clearing the caption) and emits attachment.image.failed. An
// image whose actual bytes exceed maxImageBytes is written back as skipped with
// the limit and size in the reason and is never sent to the model, so the byte
// cap holds on every path (Extract and Reanalyze), not only where the rows are
// first persisted.
func (s *AttachmentService) analyzeExtractedImage(ctx context.Context, attachment version.Attachment, image version.AttachmentImageRecord, maxImageBytes int64) error {
	now := s.clock.Now()
	started, err := s.repo.MarkImageAnalyzing(ctx, image.ID, now)
	if err != nil {
		return mapAttachmentRepositoryError(err)
	}
	if !started {
		return fmt.Errorf("attachment image %d is not in a re-analyzable state", image.ID)
	}
	if s.vision == nil {
		return s.failAnalyzedImage(ctx, attachment, image, errors.New("vision image analysis is not configured"))
	}
	sha256, data, err := s.loadImageBytes(ctx, attachment.ID, image.ContentRefID)
	if err != nil {
		return s.failAnalyzedImage(ctx, attachment, image, err)
	}
	if maxImageBytes > 0 && int64(len(data)) > maxImageBytes {
		skipReason := fmt.Sprintf(
			"image %d skipped: %d bytes exceeds %s (max %d bytes)",
			image.Seq, len(data), SettingMaxImageBytes, maxImageBytes)
		return mapAttachmentRepositoryError(s.repo.MarkImageSkipped(ctx, image.ID, skipReason, s.clock.Now()))
	}
	caption, err := s.vision.Caption(ctx, image.MIME, data)
	if err != nil {
		return s.failAnalyzedImage(ctx, attachment, image, err)
	}
	text := strings.TrimSpace(caption.Caption)
	if text == "" {
		return s.failAnalyzedImage(ctx, attachment, image, errors.New("vision model returned an empty caption"))
	}
	if err := s.repo.MarkImageDone(ctx, image.ID, text, now); err != nil {
		return mapAttachmentRepositoryError(err)
	}
	image.Status = version.AttachmentImageDone
	image.Caption = text
	image.Error = ""
	if err := s.recordImageCaptionFact(ctx, attachment, image, caption, sha256); err != nil {
		return s.failAnalyzedImage(ctx, attachment, image, fmt.Errorf("record caption fact: %w", err))
	}
	s.publishImageEvent(ctx, attachment, image, "attachment.image.analyzed", map[string]any{
		"caption": text, "model": caption.Model,
	})
	return nil
}

// failAnalyzedImage marks one image failed with the reason, publishes
// attachment.image.failed and returns the reason as a Go error for the caller's
// aggregate decision (the attachment itself is failed by Extract/Reanalyze).
func (s *AttachmentService) failAnalyzedImage(ctx context.Context, attachment version.Attachment, image version.AttachmentImageRecord, cause error) error {
	message := "image analysis failed: " + cause.Error()
	if err := s.repo.MarkImageFailed(ctx, image.ID, message, s.clock.Now()); err != nil {
		return mapAttachmentRepositoryError(err)
	}
	image.Status = version.AttachmentImageFailed
	image.Error = message
	s.publishImageEvent(ctx, attachment, image, "attachment.image.failed", map[string]any{"error": message})
	return errors.New(message)
}

// recordImageCaptionFact appends the version_fact(kind=attachment_image_caption)
// for one analyzed image. A nil recorder is an explicit failure: a caption that
// is not stored as a fact would be invisible to downstream context assembly.
func (s *AttachmentService) recordImageCaptionFact(ctx context.Context, attachment version.Attachment, image version.AttachmentImageRecord, caption ImageCaption, sha256 string) error {
	if s.facts == nil {
		return errors.New("attachment image fact recorder is not configured")
	}
	payload := map[string]any{
		"attachment_id": platformid.Format(attachment.ID),
		"image_id":      platformid.Format(image.ID),
		"seq":           image.Seq,
		"caption":       strings.TrimSpace(caption.Caption),
		"mime":          image.MIME,
		"width":         image.Width,
		"height":        image.Height,
		"source_kind":   image.SourceKind,
	}
	if caption.Model != "" {
		payload["model"] = caption.Model
	}
	if caption.Confidence != nil {
		payload["confidence"] = *caption.Confidence
	}
	fact := version.Fact{
		VersionID:  attachment.VersionID,
		Kind:       "attachment_image_caption",
		Key:        fmt.Sprintf("attachment:%d:image:%d", attachment.ID, image.Seq),
		Payload:    payload,
		Confidence: caption.Confidence,
		Source:     "attachment_vision",
		SourceRef:  sha256,
	}
	_, _, err := s.facts.Record(ctx, fact)
	return err
}

// loadImageBytes resolves one attachment_image content reference back to its
// content object and returns the object's SHA-256 plus the original bytes.
func (s *AttachmentService) loadImageBytes(ctx context.Context, attachmentID, contentRefID int64) (string, []byte, error) {
	refs, err := s.storage.ListRefs(ctx, version.AttachmentOwnerKind, attachmentID)
	if err != nil {
		return "", nil, err
	}
	for _, ref := range refs {
		if ref.ID != contentRefID {
			continue
		}
		_, reader, err := s.storage.Open(ctx, ref.ObjectID)
		if err != nil {
			return "", nil, err
		}
		defer reader.Close()
		data, err := io.ReadAll(io.LimitReader(reader, version.MaxAttachmentBytes+1))
		if err != nil {
			return "", nil, err
		}
		if int64(len(data)) > version.MaxAttachmentBytes {
			return "", nil, version.ErrAttachmentTooLarge
		}
		return ref.ObjectID, data, nil
	}
	return "", nil, fmt.Errorf("attachment %d: image content reference %d is missing", attachmentID, contentRefID)
}

func (s *AttachmentService) publishImageEvent(ctx context.Context, attachment version.Attachment, image version.AttachmentImageRecord, eventType string, extra map[string]any) {
	payload := map[string]any{
		"attachment_id": platformid.Format(attachment.ID),
		"image_id":      platformid.Format(image.ID),
		"seq":           image.Seq,
		"status":        image.Status,
	}
	for key, value := range extra {
		payload[key] = value
	}
	s.publishAttachmentEvent(ctx, attachment.VersionID, eventType, attachment, payload)
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
