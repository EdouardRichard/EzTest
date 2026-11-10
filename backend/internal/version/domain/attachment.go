package version

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Attachment limits from spec §7.
const (
	MaxAttachmentBytes     int64 = 10 << 20 // upload cap (POST /api/content)
	MaxExtractedTextBytes  int64 = 2 << 20  // extracted text cap
	MaxOOXMLBytes          int64 = 20 << 20 // uncompressed OOXML XML cap
	MaxPDFPages                  = 50
	MaxAttachmentNameRunes       = 255
	// Image parts are sent to vision models, so their pixel dimensions are
	// bounded instead of downscaled by the server (spec §8.3).
	MaxImageDimension = 16384
	MaxImagePixels    = 40_000_000
)

// Attachment extraction statuses.
const (
	AttachmentPending    = "pending"
	AttachmentProcessing = "processing"
	AttachmentDone       = "done"
	AttachmentFailed     = "failed"
	AttachmentSkipped    = "skipped"
)

// AttachmentView error codes are stable and exposed to clients.
const (
	AttachmentErrorUnsupported = "unsupported_attachment_format"
	AttachmentErrorExtraction  = "extraction_failed"
	// AttachmentErrorVisionRequired marks a document whose embedded images
	// cannot be analyzed because no vision-capable model is configured. The
	// attachment is failed explicitly instead of pretending the images were
	// understood (R-E2-2, no fake-available degradation).
	AttachmentErrorVisionRequired = "vision_required"
	// AttachmentErrorVisionAnalysis marks a document whose embedded image
	// analysis failed after a vision model was available (R-E3-3: the whole
	// attachment is failed with the reason, never silently skipped).
	AttachmentErrorVisionAnalysis = "vision_analysis_failed"
)

// Attachment image statuses (spec §4.26 R-E3-2). extracted -> analyzing ->
// done|failed; skipped means analysis was not attempted (for example because
// no vision model is configured) and can be re-queued by reanalyze.
const (
	AttachmentImageExtracted = "extracted"
	AttachmentImageAnalyzing = "analyzing"
	AttachmentImageDone      = "done"
	AttachmentImageFailed    = "failed"
	AttachmentImageSkipped   = "skipped"
)

// Attachment image source kinds, pinned by the attachment_image CHECK.
const (
	AttachmentImageSourceEmbedded   = "embedded"
	AttachmentImageSourcePageRender = "page_render"
	AttachmentImageSourceStandalone = "standalone"
)

// AttachmentImageRole is the content-ledger role of one extracted image. The
// bytes live in the content ledger; attachment_image only stores the reference
// and the analysis state (Agent.md §6.8, R-E3-2).
const AttachmentImageRole = "attachment_image"

// AttachmentImageTokenBudget is the per-image context budget (R-E3-4). The
// agent context can wire it into the task pack; the version application exposes
// it as a constant so both sides use the same number.
const AttachmentImageTokenBudget = 1500

// ErrVisionRequired reports that a document with embedded images cannot be
// sent because no vision-capable model is configured. It is wrapped by the
// typed 400 the send path returns so callers can use errors.Is.
var ErrVisionRequired = errors.New("vision-capable model is required for document images")

// Attachment content roles in the storage ledger.
const (
	AttachmentContentRole = "attachment"
	AttachmentTextRole    = "extracted_text"
	AttachmentOwnerKind   = "requirement_attachment"
)

// AttachmentKind classifies the supported formats.
type AttachmentKind string

// Supported attachment kinds.
const (
	AttachmentMarkdown    AttachmentKind = "markdown"
	AttachmentText        AttachmentKind = "text"
	AttachmentHTML        AttachmentKind = "html"
	AttachmentDOCX        AttachmentKind = "docx"
	AttachmentPPTX        AttachmentKind = "pptx"
	AttachmentXLSX        AttachmentKind = "xlsx"
	AttachmentPDF         AttachmentKind = "pdf"
	AttachmentImage       AttachmentKind = "image"
	AttachmentUnsupported AttachmentKind = "unsupported"
)

// IsImage reports whether the attachment is an image reference only.
func (k AttachmentKind) IsImage() bool { return k == AttachmentImage }

// SupportsExtraction reports whether the parser can extract text for the kind.
func (k AttachmentKind) SupportsExtraction() bool {
	switch k {
	case AttachmentMarkdown, AttachmentText, AttachmentHTML, AttachmentDOCX,
		AttachmentPPTX, AttachmentXLSX, AttachmentPDF:
		return true
	default:
		return false
	}
}

// Attachment parser sentinel errors. Infrastructure parser implementations
// wrap these so the application can classify the extraction outcome.
var (
	ErrUnsupportedAttachmentFormat = errors.New("unsupported attachment format")
	// ErrUnsupportedLegacyAttachment is raised for the pre-OOXML .doc/.ppt
	// binary formats. It wraps ErrUnsupportedAttachmentFormat so callers that
	// only classify unsupported formats keep working, while callers can still
	// surface the save-as hint to users.
	ErrUnsupportedLegacyAttachment = fmt.Errorf(
		"%w: legacy .doc/.ppt is not supported; save it as .docx/.pptx and upload again",
		ErrUnsupportedAttachmentFormat,
	)
	ErrAttachmentTooLarge = errors.New("attachment exceeds the extraction limit")
	ErrAttachmentCorrupt  = errors.New("attachment cannot be parsed")
	ErrAttachmentNotFound = errors.New("attachment not found")
)

// Attachment is the runtime state of one message attachment. The binary lives
// in the content ledger; extracted text is stored as a second content object
// and referenced here, never copied into a requirement version.
type Attachment struct {
	ID                    int64
	VersionID             int64
	RequirementID         *int64
	RequirementVersionID  *int64
	ConversationMessageID *int64
	// ConversationID is the conversation that owns a message attachment. It is
	// set from pre-registration onwards (before the message exists, R-E4-1) so
	// an unbound draft is only visible/readable to its own conversation and
	// never leaks into another conversation of the same version. It is nil for
	// requirement-scoped rows and for legacy/unattributable drafts.
	ConversationID        *int64
	ContentRefID          int64
	Name                  string
	MIME                  string
	Size                  int64
	ExtractionStatus      string
	ExtractedContentRefID *int64
	ExtractionErrorCode   string
	ExtractionErrorText   string
	CreatedBy             *int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// AttachmentImageRecord is one image extracted from a document attachment.
// content_ref_id is the content-ledger reference holding the original bytes;
// the row itself only carries identity, dimensions, extraction provenance and
// the vision state (R-E3-2). Unique (attachment_id, seq) is enforced by the DB.
type AttachmentImageRecord struct {
	ID           int64
	AttachmentID int64
	Seq          int
	ContentRefID int64
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

// AttachmentRepository persists requirement_attachment runtime rows and their
// idempotent extraction state transitions.
type AttachmentRepository interface {
	Create(ctx context.Context, attachment Attachment) error
	Get(ctx context.Context, attachmentID int64) (Attachment, error)
	GetInVersion(ctx context.Context, versionID, attachmentID int64) (Attachment, error)
	ListByRequirement(ctx context.Context, versionID, requirementID int64) ([]Attachment, error)
	// ListByVersion returns every attachment row of one version ordered by ID.
	// The v0.22 requirement-extraction source uses it to resolve the ready
	// document attachments of a version for the extraction start endpoint and
	// the ingest auto-enqueue hook.
	ListByVersion(ctx context.Context, versionID int64) ([]Attachment, error)
	// VersionExists reports whether the version row exists. The C-05 version
	// attachment list turns an unknown/foreign version into a 404 instead of
	// returning an empty list, matching the version-scoped image routes.
	VersionExists(ctx context.Context, versionID int64) (bool, error)
	// CountImagesByVersion returns the attachment_image row count of every
	// attachment of one version in a single grouped query, so the C-05 panel can
	// show image_count without an N+1 read. Attachments without images are
	// absent from the map.
	CountImagesByVersion(ctx context.Context, versionID int64) (map[int64]int, error)
	// ListByConversation returns rows bound to one of the conversation's
	// messages plus unbound drafts pre-registered for this conversation
	// (conversation_id matches). Drafts with no conversation_id (legacy rows)
	// are never returned through a conversation; they remain reusable by
	// ListUnlinkedByVersion on retry and get scoped when they are bound.
	ListByConversation(ctx context.Context, versionID, conversationID int64) ([]Attachment, error)
	// ListUnlinkedByVersion returns attachment rows that were pre-registered
	// before their message was inserted (nil conversation_message_id). The
	// message post preflight reuses them on retry instead of inserting a
	// duplicate row or re-running extraction.
	ListUnlinkedByVersion(ctx context.Context, versionID int64) ([]Attachment, error)
	ConversationExists(ctx context.Context, versionID, conversationID int64) (bool, error)
	// LinkMessage binds a pre-registered attachment row to the message that was
	// inserted after the extraction pending gate passed.
	LinkMessage(ctx context.Context, attachmentID, messageID int64, updatedAt time.Time) error
	MarkProcessing(ctx context.Context, attachmentID int64, updatedAt time.Time) (bool, error)
	MarkDone(ctx context.Context, attachmentID int64, extractedContentRefID int64, updatedAt time.Time) error
	MarkFailed(ctx context.Context, attachmentID int64, code, message string, updatedAt time.Time) error
	// SetExtractedContentRef persists the extracted-text reference before the
	// image analysis runs. A failed analysis then marks the attachment failed
	// without losing the text reference, so a later successful reanalyze can
	// finish the attachment with MarkDone.
	SetExtractedContentRef(ctx context.Context, attachmentID, contentRefID int64, updatedAt time.Time) error
	// CreateImage inserts one attachment_image row. It is idempotent on the
	// unique (attachment_id, seq): an existing row is left untouched so a
	// retried extraction never duplicates rows or content references.
	CreateImage(ctx context.Context, image AttachmentImageRecord) error
	// ListImages returns the image rows of one attachment ordered by seq.
	ListImages(ctx context.Context, attachmentID int64) ([]AttachmentImageRecord, error)
	// MarkImageAnalyzing flips an extracted/skipped/failed row to analyzing and
	// reports whether this call changed it. done/analyzing rows are untouched.
	MarkImageAnalyzing(ctx context.Context, imageID int64, updatedAt time.Time) (bool, error)
	// MarkImageDone stores the caption and clears any previous error.
	MarkImageDone(ctx context.Context, imageID int64, caption string, updatedAt time.Time) error
	// MarkImageFailed stores the failure reason and clears any caption written
	// before the failure, so a failed row never exposes a stale caption.
	MarkImageFailed(ctx context.Context, imageID int64, message string, updatedAt time.Time) error
	// MarkImageSkipped writes a row back to skipped with the reason it was
	// excluded from vision analysis. Reanalyze uses it to deterministically
	// re-apply the vision image caps after ResetImagesForReanalysis re-queued
	// every limit-skipped row.
	MarkImageSkipped(ctx context.Context, imageID int64, reason string, updatedAt time.Time) error
	// ResetImagesForReanalysis re-queues failed/skipped image rows as extracted,
	// clearing caption/error. The content reference is preserved, so a reanalyze
	// never creates a duplicate content object. It returns how many rows were
	// reset; rows already in the extracted state remain claimable, which is what
	// lets a reanalyze recover from a crash after the reset.
	ResetImagesForReanalysis(ctx context.Context, attachmentID int64, updatedAt time.Time) (int64, error)
	// ListPendingForIngest returns the oldest pending attachments for the P1
	// attachment.ingest reconciler.
	ListPendingForIngest(ctx context.Context, limit int) ([]PendingAttachment, error)
}

// SanitizeAttachmentName strips directory components and control characters.
// The name is display-only; the server never writes files under it.
func SanitizeAttachmentName(raw string) (string, error) {
	name := strings.ReplaceAll(raw, "\\", "/")
	name = path.Base(name)
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "", platform.Invalid("name", "attachment name is required")
	}
	if utf8.RuneCountInString(name) > MaxAttachmentNameRunes {
		return "", platform.Invalid("name", "must be at most 255 characters")
	}
	return name, nil
}

// DetectAttachmentKind classifies an attachment by extension, falling back to
// the MIME type. Legacy binary .doc/.ppt are deliberately unsupported; see
// IsLegacyOfficeFormat for the explicit rejection path.
func DetectAttachmentKind(name, mime string) AttachmentKind {
	name = strings.ToLower(strings.TrimSpace(name))
	mime = strings.ToLower(strings.TrimSpace(mime))
	switch {
	case strings.HasSuffix(name, ".md"), strings.HasSuffix(name, ".markdown"):
		return AttachmentMarkdown
	case strings.HasSuffix(name, ".txt"):
		return AttachmentText
	case strings.HasSuffix(name, ".html"), strings.HasSuffix(name, ".htm"):
		return AttachmentHTML
	case strings.HasSuffix(name, ".docx"):
		return AttachmentDOCX
	case strings.HasSuffix(name, ".pptx"):
		return AttachmentPPTX
	case strings.HasSuffix(name, ".xlsx"):
		return AttachmentXLSX
	case strings.HasSuffix(name, ".pdf"):
		return AttachmentPDF
	case strings.HasSuffix(name, ".png"), strings.HasSuffix(name, ".jpg"), strings.HasSuffix(name, ".jpeg"),
		strings.HasSuffix(name, ".webp"):
		return AttachmentImage
	case strings.HasSuffix(name, ".doc"), strings.HasSuffix(name, ".ppt"):
		return AttachmentUnsupported
	}
	switch {
	case strings.HasPrefix(mime, "image/"):
		return AttachmentImage
	case mime == "text/plain", mime == "text/markdown":
		return AttachmentText
	case mime == "text/html":
		return AttachmentHTML
	case mime == "application/pdf":
		return AttachmentPDF
	case strings.Contains(mime, "wordprocessingml"):
		return AttachmentDOCX
	case strings.Contains(mime, "presentationml"):
		return AttachmentPPTX
	case strings.Contains(mime, "spreadsheetml"):
		return AttachmentXLSX
	default:
		return AttachmentUnsupported
	}
}

// IsLegacyOfficeFormat reports whether name is a pre-OOXML Word (.doc) or
// PowerPoint (.ppt) file. They are rejected instead of parsed so the user is
// told to save as .docx/.pptx. .xls is outside the supported matrix too, but
// the spec only requires the .doc/.ppt save-as hint, so .xls keeps the generic
// unsupported-format error.
func IsLegacyOfficeFormat(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.HasSuffix(name, ".doc") || strings.HasSuffix(name, ".ppt")
}

// IsSupportedImageMIME reports whether an image part can be sent to a vision
// model (spec §8.1/§8.3: png/jpg/webp).
func IsSupportedImageMIME(mime string) bool {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/png", "image/jpeg", "image/jpg", "image/webp":
		return true
	default:
		return false
	}
}

// ValidateImageDimensions enforces the vision image bounds. The server rejects
// rather than silently downscales oversized images (spec §8.3).
func ValidateImageDimensions(width, height int) error {
	if width <= 0 || height <= 0 {
		return platform.Invalid("width", "image width and height must be positive")
	}
	if width > MaxImageDimension || height > MaxImageDimension {
		return platform.Invalid("width", "image dimensions must not exceed 16384 pixels")
	}
	if int64(width)*int64(height) > MaxImagePixels {
		return platform.Invalid("width", "image must not exceed 40 megapixels")
	}
	return nil
}

// PendingAttachment is the ingest reconciler row: a pending attachment plus the
// conversation/message that owns it when linked.
type PendingAttachment struct {
	ID             int64
	VersionID      int64
	ConversationID int64
	MessageID      int64
}
