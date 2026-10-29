package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Attachment kinds, mirrored from the requirement_attachment.kind CHECK
// constraint.
const (
	AttachmentText  = "text"
	AttachmentImage = "image"
)

// attachmentMIMETypes is the frozen allow-list of spec §6.7. Both the MIME type
// and the filename extension are checked by the attachment service; this map
// owns the MIME half and the text/image classification.
var attachmentMIMETypes = map[string]string{
	"text/plain":       AttachmentText,
	"text/markdown":    AttachmentText,
	"application/json": AttachmentText,
	"application/yaml": AttachmentText,
	"text/yaml":        AttachmentText,
	"image/png":        AttachmentImage,
	"image/jpeg":       AttachmentImage,
}

// sha256RE mirrors the sha256 CHECK constraint: 64 lowercase hexadecimal
// characters.
var sha256RE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Attachment is one filesystem-backed requirement attachment. StoragePath is a
// path relative to EZTEST_VERSION_ATTACHMENT_DIR; absolute paths are resolved
// only inside the filesystem adapter.
type Attachment struct {
	ID            int64
	VersionID     int64
	RequirementID int64
	Kind          string
	Filename      string
	MIMEType      string
	SizeBytes     int64
	SHA256        string
	StoragePath   string
	CreatedBy     int64
	CreatedAt     time.Time
}

// KindForMIMEType classifies an allowed attachment MIME type as text or image.
// Anything outside the frozen allow-list is rejected as unsupported.
func KindForMIMEType(mimeType string) (string, error) {
	kind, ok := attachmentMIMETypes[mimeType]
	if !ok {
		return "", NewInvalid("mimeType", "unsupported_attachment_type",
			"attachment type must be txt, md, json, yaml, png or jpg")
	}
	return kind, nil
}

// Validate enforces the attachment bounds of spec §6.7 and the
// requirement_attachment CHECK constraints.
func (a *Attachment) Validate() error {
	if a == nil {
		return NewInvalid("", "invalid_attachment", "attachment is required")
	}
	if a.VersionID <= 0 {
		return NewInvalid("versionId", "invalid_id", "versionId must be a positive id")
	}
	if a.RequirementID <= 0 {
		return NewInvalid("requirementId", "invalid_id", "requirementId must be a positive id")
	}
	if a.Kind != AttachmentText && a.Kind != AttachmentImage {
		return NewInvalid("kind", "invalid_kind", "kind must be text or image")
	}
	kind, err := KindForMIMEType(a.MIMEType)
	if err != nil {
		return err
	}
	if kind != a.Kind {
		return NewInvalid("kind", "invalid_kind",
			fmt.Sprintf("kind %q does not match mime type %q", a.Kind, a.MIMEType))
	}
	if strings.TrimSpace(a.Filename) == "" {
		return NewInvalid("filename", "invalid_filename", "filename is required")
	}
	if utf8.RuneCountInString(a.Filename) > MaxAttachmentFilenameChars {
		return NewInvalid("filename", "invalid_filename",
			fmt.Sprintf("filename must be at most %d characters", MaxAttachmentFilenameChars))
	}
	if a.SizeBytes <= 0 {
		return NewInvalid("sizeBytes", "invalid_attachment_size", "sizeBytes must be positive")
	}
	if a.SizeBytes > MaxAttachmentBytes {
		return NewInvalid("sizeBytes", "attachment_too_large",
			fmt.Sprintf("sizeBytes must be at most %d bytes", MaxAttachmentBytes))
	}
	if !sha256RE.MatchString(a.SHA256) {
		return NewInvalid("sha256", "invalid_sha256",
			"sha256 must be 64 lowercase hexadecimal characters")
	}
	if strings.TrimSpace(a.StoragePath) == "" {
		return NewInvalid("storagePath", "invalid_storage_path", "storagePath is required")
	}
	return nil
}
