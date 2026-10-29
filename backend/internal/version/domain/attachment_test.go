package domain

import (
	"strings"
	"testing"
)

func validAttachment() Attachment {
	return Attachment{
		ID:            41,
		VersionID:     11,
		RequirementID: 21,
		Kind:          AttachmentText,
		Filename:      "notes.txt",
		MIMEType:      "text/plain",
		SizeBytes:     12,
		SHA256:        strings.Repeat("a", 64),
		StoragePath:   "11/41_notes.txt",
		CreatedBy:     1,
	}
}

func TestAttachmentValidate(t *testing.T) {
	tests := []struct {
		name   string
		build  func(*Attachment)
		valid  bool
		field  string
		reason string
	}{
		{name: "valid text attachment", valid: true},
		{
			name: "valid image attachment",
			build: func(a *Attachment) {
				a.Kind = AttachmentImage
				a.Filename = "screen.png"
				a.MIMEType = "image/png"
			},
			valid: true,
		},
		{
			name:   "kind must be text/image",
			build:  func(a *Attachment) { a.Kind = "video" },
			field:  "kind",
			reason: "invalid_kind",
		},
		{
			name: "mime type must be supported",
			build: func(a *Attachment) {
				a.MIMEType = "application/pdf"
			},
			field:  "mimeType",
			reason: "unsupported_attachment_type",
		},
		{
			name: "kind must match the mime type",
			build: func(a *Attachment) {
				a.MIMEType = "image/png"
			},
			field:  "kind",
			reason: "invalid_kind",
		},
		{
			name:   "version id required",
			build:  func(a *Attachment) { a.VersionID = 0 },
			field:  "versionId",
			reason: "invalid_id",
		},
		{
			name:   "requirement id required",
			build:  func(a *Attachment) { a.RequirementID = 0 },
			field:  "requirementId",
			reason: "invalid_id",
		},
		{
			name:   "filename required",
			build:  func(a *Attachment) { a.Filename = "" },
			field:  "filename",
			reason: "invalid_filename",
		},
		{
			name:  "filename at limit",
			build: func(a *Attachment) { a.Filename = strings.Repeat("f", MaxAttachmentFilenameChars) },
			valid: true,
		},
		{
			name:   "filename too long",
			build:  func(a *Attachment) { a.Filename = strings.Repeat("f", MaxAttachmentFilenameChars+1) },
			field:  "filename",
			reason: "invalid_filename",
		},
		{
			name:   "size must be positive",
			build:  func(a *Attachment) { a.SizeBytes = 0 },
			field:  "sizeBytes",
			reason: "invalid_attachment_size",
		},
		{
			name:  "size at limit",
			build: func(a *Attachment) { a.SizeBytes = MaxAttachmentBytes },
			valid: true,
		},
		{
			name:   "size over limit",
			build:  func(a *Attachment) { a.SizeBytes = MaxAttachmentBytes + 1 },
			field:  "sizeBytes",
			reason: "attachment_too_large",
		},
		{
			name:   "sha256 must be 64 lowercase hex characters",
			build:  func(a *Attachment) { a.SHA256 = strings.Repeat("a", 63) },
			field:  "sha256",
			reason: "invalid_sha256",
		},
		{
			name:   "sha256 must be lowercase",
			build:  func(a *Attachment) { a.SHA256 = strings.Repeat("A", 64) },
			field:  "sha256",
			reason: "invalid_sha256",
		},
		{
			name:   "storage path required",
			build:  func(a *Attachment) { a.StoragePath = "" },
			field:  "storagePath",
			reason: "invalid_storage_path",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attachment := validAttachment()
			if tc.build != nil {
				tc.build(&attachment)
			}
			err := attachment.Validate()
			if tc.valid {
				if err != nil {
					t.Fatalf("want valid attachment, got %v", err)
				}
				return
			}
			assertDomainError(t, err, tc.field, tc.reason)
		})
	}
}

func TestKindForMIMEType(t *testing.T) {
	tests := []struct {
		mimeType string
		kind     string
	}{
		{mimeType: "text/plain", kind: AttachmentText},
		{mimeType: "text/markdown", kind: AttachmentText},
		{mimeType: "application/json", kind: AttachmentText},
		{mimeType: "application/yaml", kind: AttachmentText},
		{mimeType: "text/yaml", kind: AttachmentText},
		{mimeType: "image/png", kind: AttachmentImage},
		{mimeType: "image/jpeg", kind: AttachmentImage},
	}
	for _, tc := range tests {
		t.Run(tc.mimeType, func(t *testing.T) {
			kind, err := KindForMIMEType(tc.mimeType)
			if err != nil {
				t.Fatalf("KindForMIMEType(%q): %v", tc.mimeType, err)
			}
			if kind != tc.kind {
				t.Fatalf("KindForMIMEType(%q) = %q, want %q", tc.mimeType, kind, tc.kind)
			}
		})
	}

	for _, mimeType := range []string{"application/pdf", "image/gif", "text/html", ""} {
		if _, err := KindForMIMEType(mimeType); err == nil {
			t.Fatalf("KindForMIMEType(%q) want error", mimeType)
		}
	}
}
