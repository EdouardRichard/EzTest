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
)

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

// AttachmentRepository persists requirement_attachment runtime rows and their
// idempotent extraction state transitions.
type AttachmentRepository interface {
	Create(ctx context.Context, attachment Attachment) error
	Get(ctx context.Context, attachmentID int64) (Attachment, error)
	GetInVersion(ctx context.Context, versionID, attachmentID int64) (Attachment, error)
	ListByRequirement(ctx context.Context, versionID, requirementID int64) ([]Attachment, error)
	ListByConversation(ctx context.Context, versionID, conversationID int64) ([]Attachment, error)
	ConversationExists(ctx context.Context, versionID, conversationID int64) (bool, error)
	MarkProcessing(ctx context.Context, attachmentID int64, updatedAt time.Time) (bool, error)
	MarkDone(ctx context.Context, attachmentID int64, extractedContentRefID int64, updatedAt time.Time) error
	MarkFailed(ctx context.Context, attachmentID int64, code, message string, updatedAt time.Time) error
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
