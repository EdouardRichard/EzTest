package application_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/filestore"
)

// pngContent is a minimal stream that http.DetectContentType classifies as PNG.
var pngContent = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x00, 0x00, 0x0d}

// jpegContent is a minimal stream that http.DetectContentType classifies as JPEG.
var jpegContent = []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01}

// attachmentRepo wraps the shared in-memory requirement repository to inject
// write failures the attachment service must handle (cleanup, no event).
type attachmentRepo struct {
	*fakeRequirementRepo
	createAttachmentErr error
	deleteAttachmentErr error
	listAttachmentsErr  error
	countAttachmentsErr error
}

// CreateAttachment delegates unless a failure is scripted.
func (r *attachmentRepo) CreateAttachment(ctx context.Context, attachment *domain.Attachment) error {
	if r.createAttachmentErr != nil {
		return r.createAttachmentErr
	}
	return r.fakeRequirementRepo.CreateAttachment(ctx, attachment)
}

// DeleteAttachment delegates unless a failure is scripted.
func (r *attachmentRepo) DeleteAttachment(ctx context.Context, id int64) error {
	if r.deleteAttachmentErr != nil {
		return r.deleteAttachmentErr
	}
	return r.fakeRequirementRepo.DeleteAttachment(ctx, id)
}

// ListAttachments delegates unless a failure is scripted.
func (r *attachmentRepo) ListAttachments(ctx context.Context, requirementID int64) ([]domain.Attachment, error) {
	if r.listAttachmentsErr != nil {
		return nil, r.listAttachmentsErr
	}
	return r.fakeRequirementRepo.ListAttachments(ctx, requirementID)
}

// CountAttachments delegates unless a failure is scripted.
func (r *attachmentRepo) CountAttachments(ctx context.Context, requirementID, versionID int64) (int, int, error) {
	if r.countAttachmentsErr != nil {
		return 0, 0, r.countAttachmentsErr
	}
	return r.fakeRequirementRepo.CountAttachments(ctx, requirementID, versionID)
}

// attachmentFixture wires the real attachment service to the in-memory
// requirement repository and a real filesystem store under t.TempDir.
type attachmentFixture struct {
	t       *testing.T
	service *application.AttachmentService
	repo    *attachmentRepo
	base    *fakeRequirementRepo
	events  *fakeEvents
	ids     *fixedIDs
	store   application.AttachmentStore
	root    string
	limits  application.Limits
}

// fixtureOption customises the attachment fixture.
type fixtureOption func(*attachmentFixture)

// withLimits overrides the service limits.
func withLimits(limits application.Limits) fixtureOption {
	return func(f *attachmentFixture) { f.limits = limits }
}

// withStore replaces the filesystem store (for best-effort failure tests).
func withStore(store application.AttachmentStore) fixtureOption {
	return func(f *attachmentFixture) { f.store = store }
}

// newAttachmentFixture returns a fixture rooted in a fresh temporary directory.
// The first attachment id is 5000.
func newAttachmentFixture(t *testing.T, opts ...fixtureOption) *attachmentFixture {
	t.Helper()
	base := newFakeRequirementRepo()
	fixture := &attachmentFixture{
		t:      t,
		repo:   &attachmentRepo{fakeRequirementRepo: base},
		base:   base,
		events: &fakeEvents{},
		ids:    &fixedIDs{next: 5000},
		root:   filepath.Join(t.TempDir(), "version-attachments"),
	}
	fixture.store = filestore.New(fixture.root)
	for _, opt := range opts {
		if opt != nil {
			opt(fixture)
		}
	}
	fixture.service = application.NewAttachmentService(
		fixture.repo, fixture.store, fixture.limits, fixture.events,
		application.WithAttachmentIDGenerator(fixture.ids),
	)
	return fixture
}

// seedRequirement stores a requirement with the given id, version and status.
func (f *attachmentFixture) seedRequirement(id, versionID int64, status string) domain.Requirement {
	f.t.Helper()
	requirement := domain.Requirement{
		ID: id, VersionID: versionID, Title: "Checkout flow", Description: "checkout",
		AcceptanceCriteria: "coupons apply", Priority: domain.PriorityShould,
		Status: status, Source: domain.SourceUser, LastUpdateSource: domain.SourceUser,
		Revision: 1, CreatedBy: 7, UpdatedBy: 7, CreatedAt: testNow, UpdatedAt: testNow,
	}
	f.base.requirements[id] = requirement
	return requirement
}

// upload calls the service.
func (f *attachmentFixture) upload(req domain.Requirement, filename, declared string, content []byte) (*application.Attachment, error) {
	f.t.Helper()
	return f.service.Upload(context.Background(), req.VersionID, req.ID, filename, declared, bytes.NewReader(content), 42)
}

// mustUpload uploads and fails the test on error.
func (f *attachmentFixture) mustUpload(req domain.Requirement, filename, declared string, content []byte) *application.Attachment {
	f.t.Helper()
	attachment, err := f.upload(req, filename, declared, content)
	if err != nil {
		f.t.Fatalf("Upload %q: %v", filename, err)
	}
	return attachment
}

// storedPath returns the absolute path of an attachment file.
func (f *attachmentFixture) storedPath(attachment *application.Attachment) string {
	f.t.Helper()
	return filepath.Join(f.root, filepath.FromSlash(attachment.StoragePath))
}

// fileCount returns the number of regular files under the attachment root.
func (f *attachmentFixture) fileCount() int {
	f.t.Helper()
	count := 0
	err := filepath.WalkDir(f.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if !entry.IsDir() {
			count++
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		f.t.Fatalf("walk attachment root: %v", err)
	}
	return count
}

// seedAttachment writes a file under the root and stores metadata directly,
// simulating a row that predates the current upload path (for example a file
// written by an older version or corrupted on disk).
func (f *attachmentFixture) seedAttachment(req domain.Requirement, attachmentID int64, filename, mimeType, kind, storagePath string, content []byte) domain.Attachment {
	f.t.Helper()
	absolute := filepath.Join(f.root, filepath.FromSlash(storagePath))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		f.t.Fatalf("mkdir seeded attachment dir: %v", err)
	}
	if err := os.WriteFile(absolute, content, 0o600); err != nil {
		f.t.Fatalf("write seeded attachment: %v", err)
	}
	sum := sha256.Sum256(content)
	attachment := domain.Attachment{
		ID: attachmentID, VersionID: req.VersionID, RequirementID: req.ID,
		Kind: kind, Filename: filename, MIMEType: mimeType,
		SizeBytes: int64(len(content)), SHA256: hex.EncodeToString(sum[:]),
		StoragePath: storagePath, CreatedBy: 7, CreatedAt: testNow,
	}
	f.base.attachments[attachment.ID] = attachment
	return attachment
}

// sha256Hex returns the hex sha256 of content.
func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// assertNoAttachmentWrites asserts no file, metadata row or event was written.
func (f *attachmentFixture) assertNoAttachmentWrites() {
	f.t.Helper()
	if count := f.fileCount(); count != 0 {
		f.t.Fatalf("failure left %d attachment file(s) behind", count)
	}
	if len(f.base.attachments) != 0 {
		f.t.Fatalf("failure wrote attachment metadata: %+v", f.base.attachments)
	}
	if len(f.events.events) != 0 {
		f.t.Fatalf("failure published events: %v", eventTypeNames(f.events.events))
	}
}

// eventTypeNames projects the published event types for assertions.
func eventTypeNames(events []application.VersionEvent) []string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.Type)
	}
	return names
}

// TestUploadAllowedTypesAndEvents covers every allowed extension/MIME pair and
// the attachment.added event (spec §6.7/§6.11). The stored metadata records the
// canonical MIME type derived from the extension, the sha256 and the size.
func TestUploadAllowedTypesAndEvents(t *testing.T) {
	cases := []struct {
		filename   string
		declared   string
		content    []byte
		wantMIME   string
		wantKind   string
		wantStored string
	}{
		{"notes.txt", "text/plain", []byte("plain text"), "text/plain", domain.AttachmentText, "notes.txt"},
		{"design.md", "text/markdown", []byte("# Design"), "text/markdown", domain.AttachmentText, "design.md"},
		{"payload.json", "application/json", []byte(`{"ok":true}`), "application/json", domain.AttachmentText, "payload.json"},
		{"config.yaml", "application/yaml", []byte("a: 1"), "application/yaml", domain.AttachmentText, "config.yaml"},
		{"config.yml", "text/yaml", []byte("b: 2"), "text/yaml", domain.AttachmentText, "config.yml"},
		{"pixel.png", "image/png", pngContent, "image/png", domain.AttachmentImage, "pixel.png"},
		{"photo.jpg", "image/jpeg", jpegContent, "image/jpeg", domain.AttachmentImage, "photo.jpg"},
		{"photo.jpeg", "image/jpeg", jpegContent, "image/jpeg", domain.AttachmentImage, "photo.jpeg"},
		// Multipart clients routinely send a generic type; the extension and
		// the sniffed content decide.
		{"generic.txt", "application/octet-stream", []byte("generic"), "text/plain", domain.AttachmentText, "generic.txt"},
		{"untyped.txt", "", []byte("untyped"), "text/plain", domain.AttachmentText, "untyped.txt"},
		// Extensions are matched case-insensitively.
		{"NOTES.TXT", "text/plain", []byte("shout"), "text/plain", domain.AttachmentText, "NOTES.TXT"},
	}

	for _, tc := range cases {
		t.Run(tc.filename, func(t *testing.T) {
			f := newAttachmentFixture(t)
			req := f.seedRequirement(9001, 100, domain.RequirementDraft)

			attachment := f.mustUpload(req, tc.filename, tc.declared, tc.content)

			if attachment.ID != 5000 || attachment.VersionID != 100 || attachment.RequirementID != req.ID {
				t.Fatalf("attachment identity = %+v", attachment)
			}
			if attachment.Kind != tc.wantKind || attachment.MIMEType != tc.wantMIME {
				t.Fatalf("kind/mime = %s/%s, want %s/%s",
					attachment.Kind, attachment.MIMEType, tc.wantKind, tc.wantMIME)
			}
			if attachment.Filename != tc.wantStored {
				t.Fatalf("filename = %q, want %q", attachment.Filename, tc.wantStored)
			}
			if attachment.SizeBytes != int64(len(tc.content)) || attachment.SHA256 != sha256Hex(tc.content) {
				t.Fatalf("size/sha = %d/%s, want %d/%s",
					attachment.SizeBytes, attachment.SHA256, len(tc.content), sha256Hex(tc.content))
			}
			if attachment.StoragePath != fmt.Sprintf("100/5000_%s", tc.wantStored) {
				t.Fatalf("storage path = %q", attachment.StoragePath)
			}
			if attachment.CreatedBy != 42 || attachment.CreatedAt.IsZero() {
				t.Fatalf("createdBy/createdAt = %d/%v", attachment.CreatedBy, attachment.CreatedAt)
			}

			stored, err := os.ReadFile(f.storedPath(attachment))
			if err != nil {
				t.Fatalf("read stored file: %v", err)
			}
			if !bytes.Equal(stored, tc.content) {
				t.Fatalf("stored content = %q, want %q", stored, tc.content)
			}
			if count := f.fileCount(); count != 1 {
				t.Fatalf("attachment files = %d, want 1", count)
			}

			if len(f.events.events) != 1 {
				t.Fatalf("events = %v, want [attachment.added]", eventTypeNames(f.events.events))
			}
			event := f.events.events[0]
			if event.Type != "attachment.added" || event.VersionID != 100 ||
				event.ActorID != 42 || event.Source != domain.SourceUser {
				t.Fatalf("event = %+v", event)
			}
			if event.RefAttachmentID == nil || *event.RefAttachmentID != attachment.ID ||
				event.RefRequirementID == nil || *event.RefRequirementID != req.ID {
				t.Fatalf("event refs = %+v", event)
			}
			if event.Detail["attachmentId"] != "5000" ||
				event.Detail["requirementId"] != "9001" ||
				event.Detail["kind"] != tc.wantKind {
				t.Fatalf("event detail = %#v", event.Detail)
			}
		})
	}
}

// TestUploadMIMEAndExtension covers the MIME/extension whitelist and content
// sniffing (spec §6.7): an unsupported extension, a declared type outside the
// allow-list, or content that contradicts the extension is a 422 with no write.
func TestUploadMIMEAndExtension(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		declared string
		content  []byte
		reason   string
	}{
		{"unsupported extension", "manual.pdf", "application/pdf", []byte("%PDF-1.4"), "unsupported_attachment_type"},
		{"unsupported binary extension", "archive.bin", "", []byte("\x00\x01"), "unsupported_attachment_type"},
		{"declared type outside allow list", "notes.txt", "application/pdf", []byte("plain"), "unsupported_attachment_type"},
		{"declared image type disagrees with extension", "pixel.png", "image/jpeg", pngContent, "unsupported_attachment_type"},
		{"declared text type disagrees with image extension", "pixel.png", "text/plain", pngContent, "unsupported_attachment_type"},
		{"image extension with text content", "pixel.png", "image/png", []byte("not an image"), "invalid_attachment_content"},
		{"text extension with image content", "notes.txt", "text/plain", pngContent, "invalid_attachment_content"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAttachmentFixture(t)
			req := f.seedRequirement(9001, 100, domain.RequirementDraft)

			_, err := f.upload(req, tc.filename, tc.declared, tc.content)

			assertDomainReason(t, err, domain.KindInvalid, tc.reason)
			f.assertNoAttachmentWrites()
		})
	}
}

// TestUploadDuplicateSHA covers the per-requirement sha256 uniqueness (409
// duplicate_attachment) and the cleanup of the file written for the duplicate.
func TestUploadDuplicateSHA(t *testing.T) {
	t.Run("second identical upload is a 409", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		content := []byte("same bytes")

		first := f.mustUpload(req, "first.txt", "text/plain", content)
		_, err := f.upload(req, "second.txt", "text/plain", content)

		assertDomainReason(t, err, domain.KindConflict, "duplicate_attachment")
		if len(f.base.attachments) != 1 {
			t.Fatalf("metadata rows = %d, want 1", len(f.base.attachments))
		}
		if count := f.fileCount(); count != 1 {
			t.Fatalf("attachment files = %d, want 1 (duplicate file must be removed)", count)
		}
		if _, statErr := os.Stat(f.storedPath(first)); statErr != nil {
			t.Fatalf("original file was removed: %v", statErr)
		}
		if len(f.events.events) != 1 {
			t.Fatalf("events = %v, want only the successful upload", eventTypeNames(f.events.events))
		}
	})

	t.Run("the same content is allowed on another requirement", func(t *testing.T) {
		f := newAttachmentFixture(t)
		first := f.seedRequirement(9001, 100, domain.RequirementDraft)
		second := f.seedRequirement(9002, 100, domain.RequirementDraft)
		content := []byte("shared bytes")

		f.mustUpload(first, "first.txt", "text/plain", content)
		if _, err := f.upload(second, "second.txt", "text/plain", content); err != nil {
			t.Fatalf("Upload to another requirement: %v", err)
		}
		if len(f.base.attachments) != 2 || f.fileCount() != 2 {
			t.Fatal("second requirement did not get its own attachment")
		}
	})

	t.Run("a lost pre-check race cleans the file and returns the repository 409", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		f.mustUpload(req, "first.txt", "text/plain", []byte("first"))
		f.repo.createAttachmentErr = domain.NewConflict("duplicate_attachment",
			"this requirement already has an attachment with the same sha256")

		_, err := f.upload(req, "raced.txt", "text/plain", []byte("raced"))

		assertDomainReason(t, err, domain.KindConflict, "duplicate_attachment")
		if count := f.fileCount(); count != 1 {
			t.Fatalf("attachment files = %d, want 1 (raced file must be removed)", count)
		}
	})
}

// TestUploadLimits covers the count limits of spec §4.1/§6.7. The default
// per-requirement cap is exercised for real; the version cap is exercised with
// an injected lower bound so the test stays small.
func TestUploadLimits(t *testing.T) {
	t.Run("default limits match the spec constants", func(t *testing.T) {
		limits := application.DefaultLimits()
		if limits.MaxBytesPerFile != domain.MaxAttachmentBytes ||
			limits.MaxPerRequirement != domain.MaxAttachmentsPerRequirement ||
			limits.MaxPerVersion != domain.MaxAttachmentsPerVersion ||
			limits.MaxTextBytes != domain.MaxInjectedTextBytesPerFile ||
			limits.MaxImageBytes != domain.MaxAgentImageBytes {
			t.Fatalf("default limits = %+v", limits)
		}
	})

	t.Run("zero limits fall back to the spec defaults", func(t *testing.T) {
		f := newAttachmentFixture(t, withLimits(application.Limits{}))
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		for i := 0; i < domain.MaxAttachmentsPerRequirement; i++ {
			f.mustUpload(req, fmt.Sprintf("notes-%d.txt", i), "text/plain", []byte(fmt.Sprintf("content %d", i)))
		}
		_, err := f.upload(req, "one-too-many.txt", "text/plain", []byte("too many"))
		assertDomainReason(t, err, domain.KindInvalid, "attachment_limit_exceeded")
		if len(f.base.attachments) != domain.MaxAttachmentsPerRequirement ||
			f.fileCount() != domain.MaxAttachmentsPerRequirement {
			t.Fatalf("attachments = %d files = %d, want %d",
				len(f.base.attachments), f.fileCount(), domain.MaxAttachmentsPerRequirement)
		}
	})

	t.Run("per requirement cap", func(t *testing.T) {
		limits := application.DefaultLimits()
		limits.MaxPerRequirement = 2
		f := newAttachmentFixture(t, withLimits(limits))
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		f.mustUpload(req, "first.txt", "text/plain", []byte("first"))
		f.mustUpload(req, "second.txt", "text/plain", []byte("second"))

		_, err := f.upload(req, "third.txt", "text/plain", []byte("third"))

		assertDomainReason(t, err, domain.KindInvalid, "attachment_limit_exceeded")
		if f.fileCount() != 2 || len(f.base.attachments) != 2 {
			t.Fatal("a rejected limit upload wrote a file or metadata row")
		}
	})

	t.Run("per version cap", func(t *testing.T) {
		limits := application.DefaultLimits()
		limits.MaxPerRequirement = 10
		limits.MaxPerVersion = 3
		f := newAttachmentFixture(t, withLimits(limits))
		first := f.seedRequirement(9001, 100, domain.RequirementDraft)
		second := f.seedRequirement(9002, 100, domain.RequirementDraft)
		f.mustUpload(first, "a.txt", "text/plain", []byte("a"))
		f.mustUpload(first, "b.txt", "text/plain", []byte("b"))
		f.mustUpload(second, "c.txt", "text/plain", []byte("c"))

		_, err := f.upload(second, "d.txt", "text/plain", []byte("d"))

		assertDomainReason(t, err, domain.KindInvalid, "attachment_limit_exceeded")
		if f.fileCount() != 3 || len(f.base.attachments) != 3 {
			t.Fatal("a rejected version-limit upload wrote a file or metadata row")
		}
	})

	t.Run("per file cap", func(t *testing.T) {
		limits := application.DefaultLimits()
		limits.MaxBytesPerFile = 16
		f := newAttachmentFixture(t, withLimits(limits))
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		f.mustUpload(req, "exact.txt", "text/plain", bytes.Repeat([]byte("a"), 16))

		_, err := f.upload(req, "over.txt", "text/plain", bytes.Repeat([]byte("b"), 17))

		assertDomainReason(t, err, domain.KindInvalid, "attachment_too_large")
		if f.fileCount() != 1 {
			t.Fatalf("attachment files = %d, want only the accepted file", f.fileCount())
		}
	})
}

// TestUploadRequiresDraftAndScopesVersion covers the draft-only upload gate and
// version scoping (INV-8), plus the empty-file and non-UTF-8 text rejections.
func TestUploadRequiresDraftAndScopesVersion(t *testing.T) {
	t.Run("missing requirement is a 404", func(t *testing.T) {
		f := newAttachmentFixture(t)
		_, err := f.service.Upload(context.Background(), 100, 999,
			"notes.txt", "text/plain", strings.NewReader("x"), 42)
		assertDomainReason(t, err, domain.KindNotFound, "not_found")
		f.assertNoAttachmentWrites()
	})

	t.Run("requirement of another version is a 404", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 200, domain.RequirementDraft)
		_, err := f.service.Upload(context.Background(), 100, req.ID,
			"notes.txt", "text/plain", strings.NewReader("x"), 42)
		assertDomainReason(t, err, domain.KindNotFound, "not_found")
		f.assertNoAttachmentWrites()
	})

	t.Run("clarified requirement is a 409", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementClarified)
		_, err := f.upload(req, "notes.txt", "text/plain", []byte("x"))
		assertDomainReason(t, err, domain.KindStateConflict, "requirement_state_conflict")
		f.assertNoAttachmentWrites()
	})

	t.Run("empty content is a 422", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		_, err := f.upload(req, "notes.txt", "text/plain", nil)
		assertDomainReason(t, err, domain.KindInvalid, "invalid_attachment_size")
		f.assertNoAttachmentWrites()
	})

	t.Run("non-UTF-8 text content is a 422", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		// Mostly ASCII so http.DetectContentType still reports text/plain, but
		// an invalid UTF-8 sequence rejects the upload.
		_, err := f.upload(req, "notes.txt", "text/plain", []byte("hello \xc3\x28 world"))
		assertDomainReason(t, err, domain.KindInvalid, "invalid_attachment_content")
		f.assertNoAttachmentWrites()
	})
}

// TestUploadSanitizesFilename covers the filename rule of spec §6.7: basename
// only, Unicode preserved, path separators and control characters removed, at
// most 120 runes.
func TestUploadSanitizesFilename(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"windows traversal", `..\..\etc\Notes.txt`, "Notes.txt"},
		{"posix traversal", "../../etc/Notes.txt", "Notes.txt"},
		{"nested path", "dir/sub/report.md", "report.md"},
		{"control characters", "a\x00b\x01c.txt", "abc.txt"},
		{"windows reserved characters", `a*b?c"d<e>f|g.txt`, "abcdefg.txt"},
		{"unicode preserved", "需求 说明.txt", "需求 说明.txt"},
		{"surrounding spaces", "  spaced.txt  ", "spaced.txt"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAttachmentFixture(t)
			req := f.seedRequirement(9001, 100, domain.RequirementDraft)

			attachment := f.mustUpload(req, tc.raw, "", []byte("content"))

			if attachment.Filename != tc.want {
				t.Fatalf("filename = %q, want %q", attachment.Filename, tc.want)
			}
			wantPath := fmt.Sprintf("100/5000_%s", tc.want)
			if attachment.StoragePath != wantPath {
				t.Fatalf("storage path = %q, want %q", attachment.StoragePath, wantPath)
			}
			if _, err := os.Stat(f.storedPath(attachment)); err != nil {
				t.Fatalf("stored file: %v", err)
			}
		})
	}

	t.Run("long names are truncated to 120 runes keeping the extension", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		raw := strings.Repeat("a", 200) + ".txt"

		attachment := f.mustUpload(req, raw, "text/plain", []byte("content"))

		if count := utf8.RuneCountInString(attachment.Filename); count != domain.MaxAttachmentFilenameChars {
			t.Fatalf("filename runes = %d, want %d", count, domain.MaxAttachmentFilenameChars)
		}
		if !strings.HasSuffix(attachment.Filename, ".txt") {
			t.Fatalf("truncated filename dropped its extension: %q", attachment.Filename)
		}
	})

	t.Run("long multi-byte names stay inside filesystem component limits", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		// 120 four-byte runes fit the rune cap but are 480 bytes, which common
		// filesystems reject as a single path component.
		raw := strings.Repeat("😀", domain.MaxAttachmentFilenameChars) + ".txt"

		attachment := f.mustUpload(req, raw, "text/plain", []byte("content"))

		if count := utf8.RuneCountInString(attachment.Filename); count > domain.MaxAttachmentFilenameChars {
			t.Fatalf("filename runes = %d, want at most %d", count, domain.MaxAttachmentFilenameChars)
		}
		if !strings.HasSuffix(attachment.Filename, ".txt") {
			t.Fatalf("truncated filename dropped its extension: %q", attachment.Filename)
		}
		if _, err := os.Stat(f.storedPath(attachment)); err != nil {
			t.Fatalf("stored file: %v", err)
		}
	})

	t.Run("a filename without an allowed extension is a 422", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		_, err := f.upload(req, "README", "", []byte("content"))
		assertDomainReason(t, err, domain.KindInvalid, "unsupported_attachment_type")
	})

	t.Run("a blank filename is a 422", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		for _, raw := range []string{"", "   ", ".", "..", `/`, `\`} {
			_, err := f.upload(req, raw, "", []byte("content"))
			assertDomainReason(t, err, domain.KindInvalid, "invalid_filename")
		}
		f.assertNoAttachmentWrites()
	})
}

// TestUploadCleansFileWhenMetadataWriteFails covers the spec §6.10 ordering:
// the file is written before the metadata insert and removed when the insert
// fails, so no orphan file is reported as an attachment.
func TestUploadCleansFileWhenMetadataWriteFails(t *testing.T) {
	f := newAttachmentFixture(t)
	req := f.seedRequirement(9001, 100, domain.RequirementDraft)
	f.repo.createAttachmentErr = errors.New("database unavailable")

	_, err := f.upload(req, "notes.txt", "text/plain", []byte("content"))

	if err == nil || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("Upload error = %v", err)
	}
	f.assertNoAttachmentWrites()
}

// TestReadText covers the text read contract of spec §6.7/§8.3: text only,
// UTF-8, bounded by maxBytes and the 64 KiB per-file cap.
func TestReadText(t *testing.T) {
	ctx := context.Background()

	t.Run("reads bounded content and trims a split rune", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		attachment := f.mustUpload(req, "notes.txt", "text/plain", []byte("héllo wörld"))

		full, err := f.service.ReadText(ctx, 100, attachment.ID, 0)
		if err != nil {
			t.Fatalf("ReadText full: %v", err)
		}
		if full != "héllo wörld" {
			t.Fatalf("full content = %q", full)
		}
		bounded, err := f.service.ReadText(ctx, 100, attachment.ID, 5)
		if err != nil {
			t.Fatalf("ReadText bounded: %v", err)
		}
		if bounded != "héll" {
			t.Fatalf("bounded content = %q, want %q", bounded, "héll")
		}
		split, err := f.service.ReadText(ctx, 100, attachment.ID, 2)
		if err != nil {
			t.Fatalf("ReadText split rune: %v", err)
		}
		if split != "h" {
			t.Fatalf("split-rune content = %q, want %q", split, "h")
		}
	})

	t.Run("caps at 64 KiB per file", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		content := bytes.Repeat([]byte("x"), domain.MaxInjectedTextBytesPerFile+1024)
		attachment := f.mustUpload(req, "big.txt", "text/plain", content)

		read, err := f.service.ReadText(ctx, 100, attachment.ID, 0)
		if err != nil {
			t.Fatalf("ReadText: %v", err)
		}
		if len(read) != domain.MaxInjectedTextBytesPerFile {
			t.Fatalf("read bytes = %d, want %d", len(read), domain.MaxInjectedTextBytesPerFile)
		}
	})

	t.Run("wrong version is a 404", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		attachment := f.mustUpload(req, "notes.txt", "text/plain", []byte("content"))
		_, err := f.service.ReadText(ctx, 200, attachment.ID, 0)
		assertDomainReason(t, err, domain.KindNotFound, "not_found")
	})

	t.Run("missing attachment and missing file are 404s", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		_, err := f.service.ReadText(ctx, 100, 999, 0)
		assertDomainReason(t, err, domain.KindNotFound, "not_found")

		attachment := f.mustUpload(req, "notes.txt", "text/plain", []byte("content"))
		if err := os.Remove(f.storedPath(attachment)); err != nil {
			t.Fatalf("remove stored file: %v", err)
		}
		_, err = f.service.ReadText(ctx, 100, attachment.ID, 0)
		assertDomainReason(t, err, domain.KindNotFound, "not_found")
	})

	t.Run("image attachment is a 422", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		attachment := f.mustUpload(req, "pixel.png", "image/png", pngContent)
		_, err := f.service.ReadText(ctx, 100, attachment.ID, 0)
		assertDomainReason(t, err, domain.KindInvalid, "unsupported_attachment_type")
	})
}

// TestReadTextRejectsNonUTF8 covers the UTF-8 requirement of attachment text
// injection (spec §6.7/§8.3): a stored file that is not valid UTF-8 is a 422.
func TestReadTextRejectsNonUTF8(t *testing.T) {
	f := newAttachmentFixture(t)
	req := f.seedRequirement(9001, 100, domain.RequirementDraft)
	attachment := f.seedAttachment(req, 9101, "legacy.txt", "text/plain", domain.AttachmentText,
		"100/9101_legacy.txt", []byte{0xff, 0xfe, 0xfd})

	_, err := f.service.ReadText(context.Background(), 100, attachment.ID, 0)

	assertDomainReason(t, err, domain.KindInvalid, "invalid_attachment_content")
}

// TestImageDataURLOnlyImages covers the multimodal resolver contract: only
// image attachments, capped at 2 MiB, returned transiently as a data URL.
func TestImageDataURLOnlyImages(t *testing.T) {
	ctx := context.Background()

	t.Run("encodes an image as a data URL", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		attachment := f.mustUpload(req, "pixel.png", "image/png", pngContent)

		dataURL, err := f.service.ImageDataURL(ctx, attachment.ID)
		if err != nil {
			t.Fatalf("ImageDataURL: %v", err)
		}
		prefix := "data:image/png;base64,"
		if !strings.HasPrefix(dataURL, prefix) {
			t.Fatalf("data URL = %q, want prefix %q", dataURL, prefix)
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURL, prefix))
		if err != nil {
			t.Fatalf("decode data URL: %v", err)
		}
		if !bytes.Equal(decoded, pngContent) {
			t.Fatalf("decoded = %q, want %q", decoded, pngContent)
		}
	})

	t.Run("text attachment is a 422", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		attachment := f.mustUpload(req, "notes.txt", "text/plain", []byte("text"))
		_, err := f.service.ImageDataURL(ctx, attachment.ID)
		assertDomainReason(t, err, domain.KindInvalid, "unsupported_attachment_type")
	})

	t.Run("oversized image is a 422", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		attachment := f.seedAttachment(req, 9101, "huge.png", "image/png", domain.AttachmentImage,
			"100/9101_huge.png", []byte{0x89, 'P', 'N', 'G'})
		attachment.SizeBytes = domain.MaxAgentImageBytes + 1
		f.base.attachments[attachment.ID] = attachment

		_, err := f.service.ImageDataURL(ctx, attachment.ID)
		assertDomainReason(t, err, domain.KindInvalid, "attachment_too_large")
	})

	t.Run("missing attachment and missing file are 404s", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		_, err := f.service.ImageDataURL(ctx, 999)
		assertDomainReason(t, err, domain.KindNotFound, "not_found")

		attachment := f.mustUpload(req, "pixel.png", "image/png", pngContent)
		if err := os.Remove(f.storedPath(attachment)); err != nil {
			t.Fatalf("remove stored file: %v", err)
		}
		_, err = f.service.ImageDataURL(ctx, attachment.ID)
		assertDomainReason(t, err, domain.KindNotFound, "not_found")
	})
}

// TestDeleteRemovesMetadataAndFile covers the delete order of spec §6.7: DB
// metadata first, then a best-effort file removal (an orphan file is allowed;
// the metadata delete is not undone).
func TestDeleteRemovesMetadataAndFile(t *testing.T) {
	ctx := context.Background()

	t.Run("deletes metadata and the file", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		attachment := f.mustUpload(req, "notes.txt", "text/plain", []byte("content"))
		other := f.mustUpload(req, "other.txt", "text/plain", []byte("other content"))
		f.events.events = nil

		if err := f.service.Delete(ctx, 100, req.ID, attachment.ID, 42); err != nil {
			t.Fatalf("Delete: %v", err)
		}

		if _, err := f.repo.GetAttachment(ctx, attachment.ID); err == nil {
			t.Fatal("attachment metadata survived the delete")
		}
		if _, err := os.Stat(f.storedPath(attachment)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("attachment file survived the delete: %v", err)
		}
		if _, err := os.Stat(f.storedPath(other)); err != nil {
			t.Fatalf("delete removed another attachment's file: %v", err)
		}
		if len(f.events.events) != 1 {
			t.Fatalf("events = %v, want [attachment.removed]", eventTypeNames(f.events.events))
		}
		event := f.events.events[0]
		if event.Type != "attachment.removed" || event.VersionID != 100 ||
			event.RefAttachmentID == nil || *event.RefAttachmentID != attachment.ID {
			t.Fatalf("event = %+v", event)
		}
	})

	t.Run("missing, foreign and cross-version attachments are 404s", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		otherReq := f.seedRequirement(9002, 100, domain.RequirementDraft)
		attachment := f.mustUpload(req, "notes.txt", "text/plain", []byte("content"))

		err := f.service.Delete(ctx, 100, req.ID, 999, 42)
		assertDomainReason(t, err, domain.KindNotFound, "not_found")
		err = f.service.Delete(ctx, 100, otherReq.ID, attachment.ID, 42)
		assertDomainReason(t, err, domain.KindNotFound, "not_found")
		err = f.service.Delete(ctx, 200, req.ID, attachment.ID, 42)
		assertDomainReason(t, err, domain.KindNotFound, "not_found")
		if _, err := os.Stat(f.storedPath(attachment)); err != nil {
			t.Fatalf("rejected delete removed the file: %v", err)
		}
	})

	t.Run("clarified requirement is a 409", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		attachment := f.mustUpload(req, "notes.txt", "text/plain", []byte("content"))
		stored := f.base.requirements[req.ID]
		stored.Status = domain.RequirementClarified
		f.base.requirements[req.ID] = stored
		f.events.events = nil

		err := f.service.Delete(ctx, 100, req.ID, attachment.ID, 42)

		assertDomainReason(t, err, domain.KindStateConflict, "requirement_state_conflict")
		if _, err := f.repo.GetAttachment(ctx, attachment.ID); err != nil {
			t.Fatalf("blocked delete removed metadata: %v", err)
		}
		if _, err := os.Stat(f.storedPath(attachment)); err != nil {
			t.Fatalf("blocked delete removed the file: %v", err)
		}
		if len(f.events.events) != 0 {
			t.Fatalf("blocked delete published events: %v", eventTypeNames(f.events.events))
		}
	})

	t.Run("a file removal failure does not undo the metadata delete", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		attachment := f.mustUpload(req, "notes.txt", "text/plain", []byte("content"))
		failing := &removeFailureStore{AttachmentStore: f.store, err: errors.New("file is locked")}
		f.store = failing
		f.service = application.NewAttachmentService(f.repo, failing, f.limits, f.events,
			application.WithAttachmentIDGenerator(f.ids))
		f.events.events = nil

		if err := f.service.Delete(ctx, 100, req.ID, attachment.ID, 42); err != nil {
			t.Fatalf("Delete must succeed when file cleanup fails: %v", err)
		}

		if _, err := f.repo.GetAttachment(ctx, attachment.ID); err == nil {
			t.Fatal("metadata survived a best-effort delete")
		}
		if failing.removeCalls != 1 {
			t.Fatalf("file removal attempts = %d, want 1", failing.removeCalls)
		}
		if _, err := os.Stat(f.storedPath(attachment)); err != nil {
			t.Fatalf("file should be left for the P8 GC: %v", err)
		}
		if len(f.events.events) != 1 || f.events.events[0].Type != "attachment.removed" {
			t.Fatalf("events = %v", eventTypeNames(f.events.events))
		}
	})

	t.Run("a metadata delete failure keeps the file and emits no event", func(t *testing.T) {
		f := newAttachmentFixture(t)
		req := f.seedRequirement(9001, 100, domain.RequirementDraft)
		attachment := f.mustUpload(req, "notes.txt", "text/plain", []byte("content"))
		f.repo.deleteAttachmentErr = errors.New("database unavailable")
		f.events.events = nil

		err := f.service.Delete(ctx, 100, req.ID, attachment.ID, 42)

		if err == nil || !strings.Contains(err.Error(), "database unavailable") {
			t.Fatalf("Delete error = %v", err)
		}
		if _, err := os.Stat(f.storedPath(attachment)); err != nil {
			t.Fatalf("failed metadata delete removed the file: %v", err)
		}
		if len(f.events.events) != 0 {
			t.Fatalf("failed delete published events: %v", eventTypeNames(f.events.events))
		}
	})
}

// TestAttachmentEventPublishFailureIsNonFatal mirrors the other P3 services: a
// committed attachment write or delete never fails because event publication
// failed (spec §6.10).
func TestAttachmentEventPublishFailureIsNonFatal(t *testing.T) {
	ctx := context.Background()
	f := newAttachmentFixture(t)
	req := f.seedRequirement(9001, 100, domain.RequirementDraft)
	f.events.publishErr = errors.New("event hub unavailable")

	attachment, err := f.upload(req, "notes.txt", "text/plain", []byte("content"))
	if err != nil {
		t.Fatalf("Upload must succeed when event publication fails: %v", err)
	}
	if err := f.service.Delete(ctx, 100, req.ID, attachment.ID, 42); err != nil {
		t.Fatalf("Delete must succeed when event publication fails: %v", err)
	}
	if len(f.events.events) != 2 {
		t.Fatalf("publish attempts = %d, want 2", len(f.events.events))
	}
}

// TestListGetAndOpenContent covers the additive read helpers the HTTP layer
// needs (list/detail/download), all scoped to the version and requirement.
func TestListGetAndOpenContent(t *testing.T) {
	ctx := context.Background()
	f := newAttachmentFixture(t)
	req := f.seedRequirement(9001, 100, domain.RequirementDraft)
	first := f.mustUpload(req, "first.txt", "text/plain", []byte("first content"))
	second := f.mustUpload(req, "second.txt", "text/plain", []byte("second content"))

	list, err := f.service.List(ctx, 100, req.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].ID != first.ID || list[1].ID != second.ID {
		t.Fatalf("list = %+v", list)
	}
	if _, err := f.service.List(ctx, 200, req.ID); err == nil {
		t.Fatal("List accepted a foreign version")
	}

	loaded, err := f.service.Get(ctx, 100, req.ID, second.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.ID != second.ID {
		t.Fatalf("loaded id = %d, want %d", loaded.ID, second.ID)
	}
	if _, err := f.service.Get(ctx, 200, req.ID, second.ID); err == nil {
		t.Fatal("Get accepted a foreign version")
	}
	if _, err := f.service.Get(ctx, 100, 9002, second.ID); err == nil {
		t.Fatal("Get accepted a foreign requirement")
	}

	attachment, file, err := f.service.OpenContent(ctx, 100, req.ID, first.ID)
	if err != nil {
		t.Fatalf("OpenContent: %v", err)
	}
	content, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read OpenContent stream: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close OpenContent stream: %v", err)
	}
	if !bytes.Equal(content, []byte("first content")) {
		t.Fatalf("OpenContent content = %q, want %q", content, "first content")
	}
	if attachment.ID != first.ID {
		t.Fatalf("OpenContent attachment = %d, want %d", attachment.ID, first.ID)
	}
	if _, _, err := f.service.OpenContent(ctx, 200, req.ID, first.ID); err == nil {
		t.Fatal("OpenContent accepted a foreign version")
	}
}

// removeFailureStore wraps a store and fails file removals.
type removeFailureStore struct {
	application.AttachmentStore
	err         error
	removeCalls int
}

// Remove records the call and returns the scripted error.
func (s *removeFailureStore) Remove(ctx context.Context, storagePath string) error {
	s.removeCalls++
	return s.err
}
