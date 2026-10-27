//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/filestore"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
)

// attachmentIntegrationPNG is a minimal stream that http.DetectContentType
// classifies as PNG.
var attachmentIntegrationPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x00, 0x00, 0x0d}

// attachmentIntegrationFixture wires the real attachment service to the real
// PostgreSQL repositories and a real filesystem store rooted in t.TempDir.
type attachmentIntegrationFixture struct {
	pool         *pgxpool.Pool
	requirements *postgres.RequirementRepository
	versions     *postgres.VersionRepository
	store        *filestore.Store
	root         string
	events       *integrationEvents
	ids          *integrationIDs
	service      *application.AttachmentService
}

// newAttachmentIntegrationFixture opens the migrated test database and wires
// the service. The first generated id is 96001.
func newAttachmentIntegrationFixture(t *testing.T) *attachmentIntegrationFixture {
	t.Helper()
	pool := testutil.OpenTestDB(t)
	requirementRepo := postgres.NewRequirementRepository(pool)
	versionRepo := postgres.NewVersionRepository(pool)
	root := filepath.Join(t.TempDir(), "version-attachments")
	store := filestore.New(root)
	events := &integrationEvents{}
	ids := &integrationIDs{next: 96001}
	service := application.NewAttachmentService(
		requirementRepo, store, application.DefaultLimits(), events,
		application.WithAttachmentIDGenerator(ids),
	)
	return &attachmentIntegrationFixture{
		pool:         pool,
		requirements: requirementRepo,
		versions:     versionRepo,
		store:        store,
		root:         root,
		events:       events,
		ids:          ids,
		service:      service,
	}
}

// seedVersion creates one version row.
func (f *attachmentIntegrationFixture) seedVersion(t *testing.T, id int64, status string) domain.Version {
	t.Helper()
	version := domain.Version{
		ID: id, Name: "Order v2", Status: status, Revision: 1, CreatedBy: 7, UpdatedBy: 7,
	}
	if err := f.versions.Create(context.Background(), &version); err != nil {
		t.Fatalf("seed version: %v", err)
	}
	return version
}

// seedRequirement creates one requirement row directly, so the fixture needs no
// requirement service and its ids stay independent from attachment ids.
func (f *attachmentIntegrationFixture) seedRequirement(t *testing.T, versionID int64, status string) domain.Requirement {
	t.Helper()
	requirement := domain.Requirement{
		ID: f.ids.Next(), VersionID: versionID,
		Title: "Checkout flow", Description: "checkout",
		AcceptanceCriteria: "coupons apply", Priority: domain.PriorityShould,
		Status: status, Source: domain.SourceUser, LastUpdateSource: domain.SourceUser,
		Revision: 1, CreatedBy: 7, UpdatedBy: 7,
	}
	if err := f.requirements.Create(context.Background(), &requirement); err != nil {
		t.Fatalf("seed requirement: %v", err)
	}
	return requirement
}

// upload runs one service upload.
func (f *attachmentIntegrationFixture) upload(requirement domain.Requirement, filename, declared string, content []byte) (*application.Attachment, error) {
	return f.service.Upload(context.Background(), requirement.VersionID, requirement.ID,
		filename, declared, bytes.NewReader(content), 42)
}

// mustUpload uploads and fails the test on error.
func (f *attachmentIntegrationFixture) mustUpload(t *testing.T, requirement domain.Requirement, filename, declared string, content []byte) *application.Attachment {
	t.Helper()
	attachment, err := f.upload(requirement, filename, declared, content)
	if err != nil {
		t.Fatalf("Upload %q: %v", filename, err)
	}
	return attachment
}

// storedPath returns the absolute path of an attachment file.
func (f *attachmentIntegrationFixture) storedPath(attachment *application.Attachment) string {
	return filepath.Join(f.root, filepath.FromSlash(attachment.StoragePath))
}

// assertOnDisk reads the file behind attachment and compares it with content.
func (f *attachmentIntegrationFixture) assertOnDisk(t *testing.T, attachment *application.Attachment, content []byte) {
	t.Helper()
	stored, err := os.ReadFile(f.storedPath(attachment))
	if err != nil {
		t.Fatalf("read stored attachment %q: %v", attachment.StoragePath, err)
	}
	if !bytes.Equal(stored, content) {
		t.Fatalf("stored content = %q, want %q", stored, content)
	}
}

// TestAttachmentFilesystemLifecycle drives the whole attachment chain against
// the real schema and a real filesystem: upload metadata and bytes, text and
// image reads, duplicate rejection, delete order and the event stream
// (spec §6.7/§6.10/§6.11).
func TestAttachmentFilesystemLifecycle(t *testing.T) {
	ctx := context.Background()
	f := newAttachmentIntegrationFixture(t)
	version := f.seedVersion(t, 9001, domain.VersionDraft)
	requirement := f.seedRequirement(t, version.ID, domain.RequirementDraft)

	textContent := []byte("checkout flow notes\n")
	text := f.mustUpload(t, requirement, "notes.txt", "text/plain", textContent)
	if text.ID != 96002 || text.VersionID != version.ID || text.RequirementID != requirement.ID {
		t.Fatalf("text attachment identity = %+v", text)
	}
	if text.Kind != domain.AttachmentText || text.MIMEType != "text/plain" ||
		text.Filename != "notes.txt" || text.SizeBytes != int64(len(textContent)) ||
		text.SHA256 != sha256Hex(textContent) || text.CreatedBy != 42 {
		t.Fatalf("text attachment metadata = %+v", text)
	}
	if !strings.HasPrefix(text.StoragePath, "9001/") || strings.Contains(text.StoragePath, f.root) {
		t.Fatalf("storage path must be root-relative, got %q", text.StoragePath)
	}
	f.assertOnDisk(t, text, textContent)

	stored, err := f.requirements.GetAttachment(ctx, text.ID)
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}
	if stored.StoragePath != text.StoragePath || stored.SHA256 != text.SHA256 ||
		stored.MIMEType != text.MIMEType || stored.CreatedAt.IsZero() {
		t.Fatalf("stored attachment = %+v", stored)
	}

	image := f.mustUpload(t, requirement, "pixel.png", "image/png", attachmentIntegrationPNG)
	if image.Kind != domain.AttachmentImage || image.MIMEType != "image/png" {
		t.Fatalf("image attachment = %+v", image)
	}
	f.assertOnDisk(t, image, attachmentIntegrationPNG)

	// Text reads are bounded and UTF-8.
	textRead, err := f.service.ReadText(ctx, version.ID, text.ID, 0)
	if err != nil {
		t.Fatalf("ReadText: %v", err)
	}
	if textRead != string(textContent) {
		t.Fatalf("ReadText = %q, want %q", textRead, textContent)
	}
	bounded, err := f.service.ReadText(ctx, version.ID, text.ID, 8)
	if err != nil {
		t.Fatalf("ReadText bounded: %v", err)
	}
	if bounded != "checkout" {
		t.Fatalf("bounded ReadText = %q, want %q", bounded, "checkout")
	}

	// The image is available transiently as a data URL.
	dataURL, err := f.service.ImageDataURL(ctx, image.ID)
	if err != nil {
		t.Fatalf("ImageDataURL: %v", err)
	}
	prefix := "data:image/png;base64,"
	if !strings.HasPrefix(dataURL, prefix) {
		t.Fatalf("data URL = %q", dataURL)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURL, prefix))
	if err != nil || !bytes.Equal(decoded, attachmentIntegrationPNG) {
		t.Fatalf("decoded data URL = %q, err = %v", decoded, err)
	}

	// The same content on the same requirement is a 409 and leaves no extra
	// metadata row or file behind.
	_, err = f.upload(requirement, "notes-copy.txt", "text/plain", textContent)
	assertIntegrationReason(t, err, domain.KindConflict, "duplicate_attachment")
	attachments, err := f.requirements.ListAttachments(ctx, requirement.ID)
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(attachments) != 2 {
		t.Fatalf("attachments = %d, want 2", len(attachments))
	}
	if entries, err := os.ReadDir(filepath.Join(f.root, "9001")); err != nil || len(entries) != 2 {
		t.Fatalf("version attachment dir = %v, err = %v, want 2 files", entries, err)
	}

	// Deleting removes the metadata first and the file best effort; the sibling
	// attachment is untouched.
	if err := f.service.Delete(ctx, version.ID, requirement.ID, text.ID, 43); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := f.requirements.GetAttachment(ctx, text.ID); err == nil {
		t.Fatal("attachment metadata survived the delete")
	}
	if _, err := os.Stat(f.storedPath(text)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("attachment file survived the delete: %v", err)
	}
	if _, err := os.Stat(f.storedPath(image)); err != nil {
		t.Fatalf("delete removed the sibling file: %v", err)
	}

	wantTypes := []string{"attachment.added", "attachment.added", "attachment.removed"}
	if len(f.events.events) != len(wantTypes) {
		t.Fatalf("events = %v, want %v", eventTypes(f.events.events), wantTypes)
	}
	for i, want := range wantTypes {
		if f.events.events[i].Type != want || f.events.events[i].VersionID != version.ID {
			t.Fatalf("event %d = %+v, want %s for version %d", i, f.events.events[i], want, version.ID)
		}
	}
	added := f.events.events[0]
	if added.RefAttachmentID == nil || *added.RefAttachmentID != text.ID ||
		added.RefRequirementID == nil || *added.RefRequirementID != requirement.ID ||
		added.ActorID != 42 || added.Detail["kind"] != domain.AttachmentText {
		t.Fatalf("attachment.added event = %+v", added)
	}
	removed := f.events.events[2]
	if removed.RefAttachmentID == nil || *removed.RefAttachmentID != text.ID || removed.ActorID != 43 {
		t.Fatalf("attachment.removed event = %+v", removed)
	}
}

// TestAttachmentDraftGateAndLimit covers the draft-only upload/delete rule and
// the per-requirement count cap against the real schema (INV-8).
func TestAttachmentDraftGateAndLimit(t *testing.T) {
	ctx := context.Background()
	f := newAttachmentIntegrationFixture(t)
	version := f.seedVersion(t, 9001, domain.VersionDraft)
	requirement := f.seedRequirement(t, version.ID, domain.RequirementDraft)

	kept := f.mustUpload(t, requirement, "notes.txt", "text/plain", []byte("notes"))
	if _, err := f.requirements.UpdateStatus(ctx, requirement.ID,
		domain.RequirementDraft, domain.RequirementClarified, 42); err != nil {
		t.Fatalf("clarify requirement: %v", err)
	}

	_, err := f.upload(requirement, "later.txt", "text/plain", []byte("later"))
	assertIntegrationReason(t, err, domain.KindStateConflict, "requirement_state_conflict")
	if err := f.service.Delete(ctx, version.ID, requirement.ID, kept.ID, 42); err == nil {
		t.Fatal("Delete accepted a clarified requirement")
	}
	if _, err := f.requirements.GetAttachment(ctx, kept.ID); err != nil {
		t.Fatalf("blocked delete removed metadata: %v", err)
	}

	// Draft-only per-requirement cap: reopen, then fill the requirement.
	if _, err := f.requirements.UpdateStatus(ctx, requirement.ID,
		domain.RequirementClarified, domain.RequirementDraft, 42); err != nil {
		t.Fatalf("reopen requirement: %v", err)
	}
	for i := 1; i < domain.MaxAttachmentsPerRequirement; i++ {
		f.mustUpload(t, requirement, "note-"+string(rune('a'+i))+".txt", "text/plain",
			[]byte("content number "+string(rune('a'+i))))
	}
	if _, err := f.upload(requirement, "one-too-many.txt", "text/plain", []byte("too many")); err == nil {
		t.Fatal("Upload accepted an attachment over the per-requirement cap")
	} else {
		assertIntegrationReason(t, err, domain.KindInvalid, "attachment_limit_exceeded")
	}
	var count int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*)::int FROM requirement_attachments WHERE requirement_id = $1`,
		requirement.ID).Scan(&count); err != nil {
		t.Fatalf("count attachments: %v", err)
	}
	if count != domain.MaxAttachmentsPerRequirement {
		t.Fatalf("attachment rows = %d, want %d", count, domain.MaxAttachmentsPerRequirement)
	}
}

// TestAttachmentDeleteWithMissingFile covers the best-effort file removal of
// spec §6.7: a metadata delete still commits when the file has already
// disappeared (the orphan is the file, not the row).
func TestAttachmentDeleteWithMissingFile(t *testing.T) {
	ctx := context.Background()
	f := newAttachmentIntegrationFixture(t)
	version := f.seedVersion(t, 9001, domain.VersionDraft)
	requirement := f.seedRequirement(t, version.ID, domain.RequirementDraft)
	attachment := f.mustUpload(t, requirement, "notes.txt", "text/plain", []byte("notes"))
	if err := os.Remove(f.storedPath(attachment)); err != nil {
		t.Fatalf("remove stored file: %v", err)
	}
	f.events.events = nil

	if err := f.service.Delete(ctx, version.ID, requirement.ID, attachment.ID, 42); err != nil {
		t.Fatalf("Delete must tolerate a missing file: %v", err)
	}

	if _, err := f.requirements.GetAttachment(ctx, attachment.ID); err == nil {
		t.Fatal("attachment metadata survived the delete")
	}
	if len(f.events.events) != 1 || f.events.events[0].Type != "attachment.removed" {
		t.Fatalf("events = %v", eventTypes(f.events.events))
	}
}

// sha256Hex returns the hex sha256 of content.
func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
