package versionapp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/attachments"
)

// ---------- fakes ----------

type fakeAttachmentRepo struct {
	attachments          map[int64]version.Attachment
	images               map[int64]version.AttachmentImageRecord
	imageStatusLog       []string
	conversationMessages map[int64]int64 // message ID -> conversation ID
	conversations        map[int64]int64 // conversation ID -> version ID
	pendingForIngest     []version.PendingAttachment
	lastPendingLimit     int
	createErr            error
	// existingVersions backs the version existence probe of the version
	// attachment list. A nil map keeps the legacy fixtures permissive (every
	// version exists) so unrelated tests do not have to seed the version table.
	existingVersions map[int64]bool
}

func newFakeAttachmentRepo() *fakeAttachmentRepo {
	return &fakeAttachmentRepo{
		attachments: map[int64]version.Attachment{},
		images:      map[int64]version.AttachmentImageRecord{},
	}
}

func (f *fakeAttachmentRepo) Create(_ context.Context, attachment version.Attachment) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.attachments[attachment.ID] = attachment
	return nil
}

func (f *fakeAttachmentRepo) Get(_ context.Context, attachmentID int64) (version.Attachment, error) {
	attachment, ok := f.attachments[attachmentID]
	if !ok {
		return version.Attachment{}, version.ErrAttachmentNotFound
	}
	return attachment, nil
}

func (f *fakeAttachmentRepo) GetInVersion(_ context.Context, versionID, attachmentID int64) (version.Attachment, error) {
	attachment, ok := f.attachments[attachmentID]
	if !ok || attachment.VersionID != versionID {
		return version.Attachment{}, version.ErrAttachmentNotFound
	}
	return attachment, nil
}

func (f *fakeAttachmentRepo) ListByRequirement(_ context.Context, versionID, requirementID int64) ([]version.Attachment, error) {
	items := make([]version.Attachment, 0)
	for _, attachment := range f.attachments {
		if attachment.VersionID == versionID && attachment.RequirementID != nil && *attachment.RequirementID == requirementID {
			items = append(items, attachment)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

// ListByConversation mirrors the SQL: linked rows of the conversation plus
// pre-registered message attachments owned by this conversation whose message
// binding is still deferred. A draft with no conversation_id (legacy row) is
// never returned; a draft of another conversation is not returned either.
// Requirement attachments stay requirement-scoped and never appear here.
func (f *fakeAttachmentRepo) ListByConversation(_ context.Context, versionID, conversationID int64) ([]version.Attachment, error) {
	items := make([]version.Attachment, 0)
	for _, attachment := range f.attachments {
		if attachment.VersionID != versionID {
			continue
		}
		if attachment.ConversationMessageID == nil {
			if attachment.RequirementID != nil || attachment.RequirementVersionID != nil {
				continue
			}
			if attachment.ConversationID == nil || *attachment.ConversationID != conversationID {
				continue
			}
			items = append(items, attachment)
			continue
		}
		if f.conversationMessages != nil {
			if messageConversation, ok := f.conversationMessages[*attachment.ConversationMessageID]; ok && messageConversation != conversationID {
				continue
			}
		}
		items = append(items, attachment)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func (f *fakeAttachmentRepo) ListByVersion(_ context.Context, versionID int64) ([]version.Attachment, error) {
	items := make([]version.Attachment, 0)
	for _, attachment := range f.attachments {
		if attachment.VersionID == versionID {
			items = append(items, attachment)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func (f *fakeAttachmentRepo) VersionExists(_ context.Context, versionID int64) (bool, error) {
	if f.existingVersions == nil {
		return true, nil
	}
	return f.existingVersions[versionID], nil
}

func (f *fakeAttachmentRepo) CountImagesByVersion(_ context.Context, versionID int64) (map[int64]int, error) {
	counts := make(map[int64]int)
	for _, image := range f.images {
		attachment, ok := f.attachments[image.AttachmentID]
		if !ok || attachment.VersionID != versionID {
			continue
		}
		counts[image.AttachmentID]++
	}
	return counts, nil
}

func (f *fakeAttachmentRepo) ListUnlinkedByVersion(_ context.Context, versionID int64) ([]version.Attachment, error) {
	items := make([]version.Attachment, 0)
	for _, attachment := range f.attachments {
		if attachment.VersionID == versionID && attachment.ConversationMessageID == nil {
			items = append(items, attachment)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func (f *fakeAttachmentRepo) LinkMessage(_ context.Context, attachmentID, messageID int64, updatedAt time.Time) error {
	attachment, ok := f.attachments[attachmentID]
	if !ok {
		return version.ErrAttachmentNotFound
	}
	if attachment.ConversationMessageID != nil {
		return nil
	}
	attachment.ConversationMessageID = &messageID
	// Mirror the SQL backfill: a legacy unscoped draft gets the conversation of
	// the message it is bound to.
	if attachment.ConversationID == nil && f.conversationMessages != nil {
		if conversationID, ok := f.conversationMessages[messageID]; ok {
			attachment.ConversationID = &conversationID
		}
	}
	attachment.UpdatedAt = updatedAt
	f.attachments[attachmentID] = attachment
	return nil
}

func (f *fakeAttachmentRepo) ConversationExists(_ context.Context, versionID, conversationID int64) (bool, error) {
	if len(f.conversations) == 0 {
		return true, nil
	}
	conversationVersion, ok := f.conversations[conversationID]
	return ok && conversationVersion == versionID, nil
}

func (f *fakeAttachmentRepo) ListPendingForIngest(_ context.Context, limit int) ([]version.PendingAttachment, error) {
	f.lastPendingLimit = limit
	return f.pendingForIngest, nil
}

func (f *fakeAttachmentRepo) MarkProcessing(_ context.Context, attachmentID int64, updatedAt time.Time) (bool, error) {
	attachment, ok := f.attachments[attachmentID]
	if !ok {
		return false, version.ErrAttachmentNotFound
	}
	if attachment.ExtractionStatus == version.AttachmentPending {
		attachment.ExtractionStatus = version.AttachmentProcessing
		attachment.UpdatedAt = updatedAt
		f.attachments[attachmentID] = attachment
		return true, nil
	}
	return false, nil
}

func (f *fakeAttachmentRepo) MarkDone(_ context.Context, attachmentID int64, extractedContentRefID int64, updatedAt time.Time) error {
	attachment, ok := f.attachments[attachmentID]
	if !ok {
		return version.ErrAttachmentNotFound
	}
	attachment.ExtractionStatus = version.AttachmentDone
	attachment.ExtractedContentRefID = &extractedContentRefID
	attachment.ExtractionErrorCode = ""
	attachment.ExtractionErrorText = ""
	attachment.UpdatedAt = updatedAt
	f.attachments[attachmentID] = attachment
	return nil
}

func (f *fakeAttachmentRepo) MarkFailed(_ context.Context, attachmentID int64, code, message string, updatedAt time.Time) error {
	attachment, ok := f.attachments[attachmentID]
	if !ok {
		return version.ErrAttachmentNotFound
	}
	attachment.ExtractionStatus = version.AttachmentFailed
	attachment.ExtractionErrorCode = code
	attachment.ExtractionErrorText = message
	attachment.UpdatedAt = updatedAt
	f.attachments[attachmentID] = attachment
	return nil
}

type fakeContentStore struct {
	objects   map[string]versionapp.ContentObject
	data      map[string][]byte
	refs      []versionapp.ContentRef
	nextRefID int64
	uploads   []string
	openErr   error
	uploadErr error
}

func newFakeContentStore() *fakeContentStore {
	return &fakeContentStore{objects: map[string]versionapp.ContentObject{}, data: map[string][]byte{}}
}

func (f *fakeContentStore) put(sha string, data []byte, mime string) {
	f.objects[sha] = versionapp.ContentObject{SHA256: sha, Size: int64(len(data)), MIME: mime}
	f.data[sha] = data
}

func (f *fakeContentStore) Open(_ context.Context, sha string) (versionapp.ContentObject, io.ReadCloser, error) {
	if f.openErr != nil {
		return versionapp.ContentObject{}, nil, f.openErr
	}
	object, ok := f.objects[sha]
	if !ok {
		return versionapp.ContentObject{}, nil, platform.NotFound("content")
	}
	return object, io.NopCloser(strings.NewReader(string(f.data[sha]))), nil
}

func (f *fakeContentStore) AddRef(_ context.Context, objectID, ownerKind string, ownerID int64, role string) (versionapp.ContentRef, error) {
	if _, ok := f.objects[objectID]; !ok {
		return versionapp.ContentRef{}, platform.NotFound("content")
	}
	f.nextRefID++
	ref := versionapp.ContentRef{ID: f.nextRefID, ObjectID: objectID, OwnerKind: ownerKind, OwnerID: ownerID, Role: role}
	f.refs = append(f.refs, ref)
	return ref, nil
}

func (f *fakeContentStore) ListRefs(_ context.Context, ownerKind string, ownerID int64) ([]versionapp.ContentRef, error) {
	refs := make([]versionapp.ContentRef, 0)
	for _, ref := range f.refs {
		if ref.OwnerKind == ownerKind && ref.OwnerID == ownerID {
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

func (f *fakeContentStore) Upload(_ context.Context, filename, mime string, r io.Reader, maxBytes int64, actorID int64) (versionapp.ContentObject, error) {
	if f.uploadErr != nil {
		return versionapp.ContentObject{}, f.uploadErr
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return versionapp.ContentObject{}, err
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	f.objects[sha] = versionapp.ContentObject{SHA256: sha, Size: int64(len(data)), MIME: mime}
	f.data[sha] = data
	f.uploads = append(f.uploads, filename+"|"+mime+"|"+string(data))
	return f.objects[sha], nil
}

type fakeExtractor struct {
	text     string
	err      error
	result   versionapp.ExtractionResult
	calls    int
	lastName string
}

func (f *fakeExtractor) Extract(_ context.Context, name, _ string, r io.Reader) (versionapp.ExtractionResult, error) {
	f.calls++
	f.lastName = name
	_, _ = io.ReadAll(r)
	if f.err != nil {
		return versionapp.ExtractionResult{}, f.err
	}
	result := f.result
	if result.Text == "" {
		result.Text = f.text
	}
	if result.Coverage.Found == 0 && result.Text != "" {
		result.Coverage = versionapp.ExtractionCoverage{Found: 1, Parsed: 1}
	}
	return result, nil
}

// ---------- environment ----------

type attachmentEnv struct {
	svc       *versionapp.AttachmentService
	repo      *fakeAttachmentRepo
	store     *fakeContentStore
	queue     *fakeQueue
	extractor *fakeExtractor
	events    *fakePublisher
	vision    *fakeVisionCaptioner
	facts     *fakeFactRecorder
	versionID int64
}

func newAttachmentEnv(t *testing.T) *attachmentEnv {
	t.Helper()
	env := &attachmentEnv{
		repo: newFakeAttachmentRepo(), store: newFakeContentStore(), queue: &fakeQueue{},
		extractor: &fakeExtractor{text: "extracted text"}, events: &fakePublisher{},
		vision:    &fakeVisionCaptioner{available: true, caption: "登录界面截图：手机号与验证码输入框"},
		facts:     &fakeFactRecorder{},
		versionID: 1,
	}
	env.svc = versionapp.NewAttachmentService(
		env.repo, env.store, env.queue, env.extractor, attachments.NewImageProber(), env.events, &seqIDs{}, fixedClock{at: testTime})
	env.svc.SetVisionCaptioner(env.vision)
	env.svc.SetFactRecorder(env.facts)
	return env
}

func attachmentPart(sha, name, mime string, size int64) version.MessagePart {
	return version.MessagePart{Type: "attachment", SHA256: sha, Name: name, MIME: mime, Size: size}
}

func registerAttachment(t *testing.T, env *attachmentEnv, sha, name, mime string, data []byte) versionapp.AttachmentView {
	t.Helper()
	env.store.put(sha, data, mime)
	view, err := env.svc.RegisterFromMessage(requestContext(), env.versionID, 6001, 7001, attachmentPart(sha, name, mime, int64(len(data))), 42)
	if err != nil {
		t.Fatalf("RegisterFromMessage: %v", err)
	}
	return view
}

// ---------- tests ----------

func TestRegisterFromMessageCreatesPendingAttachmentAndIngestTask(t *testing.T) {
	env := newAttachmentEnv(t)
	data := []byte("# Spec\nbody")
	view := registerAttachment(t, env, strings.Repeat("a", 64), "../../spec.md", "text/markdown", data)

	if view.ID <= 0 || view.Name != "spec.md" || view.MIME != "text/markdown" || view.Size != int64(len(data)) {
		t.Fatalf("view = %#v", view)
	}
	if view.ExtractionStatus != version.AttachmentPending || view.ExtractionErrorCode != "" {
		t.Fatalf("status = %#v", view)
	}
	stored := env.repo.attachments[view.ID]
	if stored.VersionID != env.versionID || stored.ConversationMessageID == nil || *stored.ConversationMessageID != 7001 {
		t.Fatalf("stored = %#v", stored)
	}
	if len(env.store.refs) != 1 || env.store.refs[0].OwnerKind != "requirement_attachment" ||
		env.store.refs[0].OwnerID != view.ID || env.store.refs[0].Role != "attachment" {
		t.Fatalf("refs = %#v", env.store.refs)
	}
	if stored.ContentRefID != env.store.refs[0].ID {
		t.Fatalf("content ref = %d", stored.ContentRefID)
	}

	// v0.22 fix round 1: extraction is deterministic (the pending row is the
	// queue); no attachment.ingest agent work item is enqueued any more.
	if len(env.queue.tasks) != 0 {
		t.Fatalf("pending attachment enqueued a legacy ingest task: %#v", env.queue.tasks)
	}
	if len(env.events.events) != 0 {
		t.Fatalf("events = %#v", env.events.events)
	}
}

func TestRegisterFromMessageImageIsSkipped(t *testing.T) {
	env := newAttachmentEnv(t)
	view := registerAttachment(t, env, strings.Repeat("b", 64), "screenshot.png", "image/png", []byte("png"))
	if view.ExtractionStatus != version.AttachmentSkipped {
		t.Fatalf("status = %q", view.ExtractionStatus)
	}
	if len(env.queue.tasks) != 0 {
		t.Fatalf("image enqueued an ingest task: %#v", env.queue.tasks)
	}
	if len(env.events.events) != 0 {
		t.Fatalf("events = %#v", env.events.events)
	}
}

func TestRegisterFromMessageLegacyFormatFailsWithoutBlocking(t *testing.T) {
	env := newAttachmentEnv(t)
	view := registerAttachment(t, env, strings.Repeat("c", 64), "legacy.doc", "application/msword", []byte("doc"))
	if view.ExtractionStatus != version.AttachmentFailed || view.ExtractionErrorCode != version.AttachmentErrorUnsupported {
		t.Fatalf("view = %#v", view)
	}
	if len(env.queue.tasks) != 0 {
		t.Fatalf("unsupported attachment enqueued an ingest task: %#v", env.queue.tasks)
	}
	if len(env.events.events) != 1 || env.events.events[0].Type != "requirement.attachment_failed" {
		t.Fatalf("events = %#v", env.events.events)
	}
	event := env.events.events[0]
	if event.Payload["attachment_id"] != idString(view.ID) || event.Payload["code"] != version.AttachmentErrorUnsupported {
		t.Fatalf("payload = %#v", event.Payload)
	}
}

func TestRegisterFromMessageValidatesContent(t *testing.T) {
	env := newAttachmentEnv(t)
	sha := strings.Repeat("d", 64)
	env.store.put(sha, []byte("data"), "text/plain")

	cases := []struct {
		name  string
		part  version.MessagePart
		field string
	}{
		{"missing content", attachmentPart(strings.Repeat("e", 64), "a.md", "text/markdown", 4), "sha256"},
		{"size mismatch", attachmentPart(sha, "a.md", "text/markdown", 99), "size"},
		{"empty name", attachmentPart(sha, "", "text/markdown", 4), "name"},
		{"bad sha", attachmentPart("nope", "a.md", "text/markdown", 4), "sha256"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.svc.RegisterFromMessage(requestContext(), env.versionID, 6001, 7001, tc.part, 42)
			if err == nil {
				t.Fatal("invalid attachment accepted")
			}
			apiErr := apiError(t, err)
			if apiErr.Status != http.StatusBadRequest {
				t.Fatalf("status = %d", apiErr.Status)
			}
			if field, _ := apiErr.Details["field"].(string); !strings.Contains(field, tc.field) {
				t.Fatalf("field = %q, want %q", field, tc.field)
			}
			if len(env.repo.attachments) != 0 {
				t.Fatal("invalid attachment persisted")
			}
		})
	}
}

func TestExtractStoresTextAsContentReference(t *testing.T) {
	env := newAttachmentEnv(t)
	view := registerAttachment(t, env, strings.Repeat("a", 64), "spec.md", "text/markdown", []byte("# Spec"))
	env.extractor.text = "hello extracted"

	result, err := env.svc.Extract(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Attachment.ExtractionStatus != version.AttachmentDone || result.Text != "hello extracted" || result.Chars != len("hello extracted") {
		t.Fatalf("result = %#v", result)
	}
	if env.extractor.calls != 1 || env.extractor.lastName != "spec.md" {
		t.Fatalf("extractor = %#v", env.extractor)
	}
	if len(env.store.uploads) != 1 || !strings.HasPrefix(env.store.uploads[0], "spec.md.txt|") {
		t.Fatalf("uploads = %#v", env.store.uploads)
	}
	stored := env.repo.attachments[view.ID]
	if stored.ExtractionStatus != version.AttachmentDone || stored.ExtractedContentRefID == nil {
		t.Fatalf("stored = %#v", stored)
	}
	foundTextRef := false
	for _, ref := range env.store.refs {
		if ref.Role == "extracted_text" && ref.OwnerID == view.ID {
			foundTextRef = true
			if ref.ID != *stored.ExtractedContentRefID {
				t.Fatalf("text ref id = %d", ref.ID)
			}
		}
	}
	if !foundTextRef {
		t.Fatalf("extracted text ref missing: %#v", env.store.refs)
	}
	if len(env.events.events) != 1 || env.events.events[0].Type != "requirement.attachment_ingested" {
		t.Fatalf("events = %#v", env.events.events)
	}
	if env.events.events[0].Payload["chars"] != len("hello extracted") {
		t.Fatalf("payload = %#v", env.events.events[0].Payload)
	}
}

func TestExtractReturnsCoverageAndImages(t *testing.T) {
	env := newAttachmentEnv(t)
	view := registerAttachment(t, env, strings.Repeat("a", 64), "requirements.docx", "application/octet-stream", []byte("docx"))
	env.extractor.text = "3.2 用户登录"
	env.extractor.result = versionapp.ExtractionResult{
		Elements: []versionapp.ExtractionElement{
			{Kind: "heading", Text: "3.2 用户登录", Level: 2, HeadingPath: "3.2 用户登录"},
		},
		Images: []versionapp.ExtractionImage{
			{Seq: 1, Name: "image1.png", Source: "word/media/image1.png", MIME: "image/png", Width: 8, Height: 6, Data: []byte("png-bytes")},
		},
		Coverage: versionapp.ExtractionCoverage{
			Found: 2, Parsed: 2, ImagesFound: 1, ImagesParsed: 1,
		},
	}

	result, err := env.svc.Extract(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Coverage.Found != 2 || result.Coverage.Parsed != 2 || result.Coverage.Skipped != 0 {
		t.Fatalf("coverage = %+v", result.Coverage)
	}
	if len(result.Elements) != 1 || result.Elements[0].HeadingPath != "3.2 用户登录" {
		t.Fatalf("elements = %+v", result.Elements)
	}
	if len(result.Images) != 1 || string(result.Images[0].Data) != "png-bytes" || result.Images[0].MIME != "image/png" {
		t.Fatalf("images = %+v", result.Images)
	}
}

// TestExtractImageOnlyDocumentReturnsImages covers spec X-1: a document with
// no text layer but with embedded images (a scanned/image-only PDF) must not
// be failed as "no text could be extracted". Its images are the extraction
// output and flow to the vision chain; only a text-less document without any
// image is a failure.
func TestExtractImageOnlyDocumentReturnsImages(t *testing.T) {
	env := newAttachmentEnv(t)
	env.extractor.text = "" // the parser produced no text at all
	view := registerAttachment(t, env, strings.Repeat("b", 64), "scan.pdf", "application/pdf", []byte("%PDF-1.7"))
	env.extractor.result = versionapp.ExtractionResult{
		Images: []versionapp.ExtractionImage{
			{Seq: 1, Name: "pdf-image-6.png", Source: "pdf:object:6", MIME: "image/png", Width: 240, Height: 120, Data: []byte("png-bytes")},
		},
		Skipped: []versionapp.SkippedElement{
			{Kind: "page", Source: "pdf:page:1", Reason: "page has no extractable text layer"},
		},
		Coverage: versionapp.ExtractionCoverage{
			Found: 2, Parsed: 1, Skipped: 1, ImagesFound: 1, ImagesParsed: 1,
			SkipReasons: []string{"page has no extractable text layer"},
		},
	}

	result, err := env.svc.Extract(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Attachment.ExtractionStatus != version.AttachmentDone || result.ErrorCode != "" {
		t.Fatalf("image-only document failed: %#v", result)
	}
	if len(result.Images) != 1 || string(result.Images[0].Data) != "png-bytes" || result.Images[0].MIME != "image/png" {
		t.Fatalf("images = %+v", result.Images)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].Source != "pdf:page:1" {
		t.Fatalf("skips = %+v", result.Skipped)
	}
	stored := env.repo.attachments[view.ID]
	if stored.ExtractionStatus != version.AttachmentDone || stored.ExtractionErrorCode != "" {
		t.Fatalf("stored = %#v", stored)
	}
}

// TestExtractEmptyTextWithoutImagesStillFails pins the other half of the X-1
// contract: an empty extraction with no image at all remains a failure.
func TestExtractEmptyTextWithoutImagesStillFails(t *testing.T) {
	env := newAttachmentEnv(t)
	env.extractor.text = ""
	view := registerAttachment(t, env, strings.Repeat("c", 64), "empty.docx", "application/octet-stream", []byte("docx"))

	result, err := env.svc.Extract(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.ErrorCode != version.AttachmentErrorExtraction {
		t.Fatalf("error code = %q, want %q", result.ErrorCode, version.AttachmentErrorExtraction)
	}
	stored := env.repo.attachments[view.ID]
	if stored.ExtractionStatus != version.AttachmentFailed || !strings.Contains(stored.ExtractionErrorText, "no text") {
		t.Fatalf("stored = %#v", stored)
	}
}

func TestExtractIsIdempotentForCompletedAndFailedAttachments(t *testing.T) {
	env := newAttachmentEnv(t)
	view := registerAttachment(t, env, strings.Repeat("a", 64), "spec.md", "text/markdown", []byte("# Spec"))
	if _, err := env.svc.Extract(requestContext(), view.ID); err != nil {
		t.Fatalf("first Extract: %v", err)
	}
	extractorCalls := env.extractor.calls
	uploads := len(env.store.uploads)
	events := len(env.events.events)

	second, err := env.svc.Extract(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("second Extract: %v", err)
	}
	if second.Attachment.ExtractionStatus != version.AttachmentDone {
		t.Fatalf("second = %#v", second)
	}
	if env.extractor.calls != extractorCalls || len(env.store.uploads) != uploads || len(env.events.events) != events {
		t.Fatal("idempotent extract repeated side effects")
	}

	legacy := registerAttachment(t, env, strings.Repeat("c", 64), "legacy.doc", "application/msword", []byte("doc"))
	failed, err := env.svc.Extract(requestContext(), legacy.ID)
	if err != nil {
		t.Fatalf("failed Extract: %v", err)
	}
	if failed.Attachment.ExtractionStatus != version.AttachmentFailed || failed.ErrorCode != version.AttachmentErrorUnsupported {
		t.Fatalf("failed = %#v", failed)
	}
}

func TestExtractParserFailureMarksFailedAndDoesNotReturnError(t *testing.T) {
	env := newAttachmentEnv(t)
	view := registerAttachment(t, env, strings.Repeat("a", 64), "spec.docx", "application/octet-stream", []byte("docx"))
	env.extractor.err = version.ErrAttachmentCorrupt

	result, err := env.svc.Extract(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("Extract returned error for a parse failure: %v", err)
	}
	if result.Attachment.ExtractionStatus != version.AttachmentFailed || result.ErrorCode != version.AttachmentErrorExtraction {
		t.Fatalf("result = %#v", result)
	}
	stored := env.repo.attachments[view.ID]
	if stored.ExtractionErrorCode != version.AttachmentErrorExtraction || stored.ExtractionErrorText == "" {
		t.Fatalf("stored = %#v", stored)
	}
	if len(env.events.events) != 1 || env.events.events[0].Type != "requirement.attachment_failed" {
		t.Fatalf("events = %#v", env.events.events)
	}
	if env.events.events[0].Payload["code"] != version.AttachmentErrorExtraction {
		t.Fatalf("payload = %#v", env.events.events[0].Payload)
	}
}

func TestExtractStorageErrorKeepsRetryableState(t *testing.T) {
	env := newAttachmentEnv(t)
	view := registerAttachment(t, env, strings.Repeat("a", 64), "spec.md", "text/markdown", []byte("# Spec"))
	env.store.openErr = errors.New("storage down")

	if _, err := env.svc.Extract(requestContext(), view.ID); err == nil {
		t.Fatal("storage error was swallowed")
	}
	stored := env.repo.attachments[view.ID]
	if stored.ExtractionStatus != version.AttachmentPending {
		t.Fatalf("status = %q", stored.ExtractionStatus)
	}
}

func TestAttachmentGetScopesVersion(t *testing.T) {
	env := newAttachmentEnv(t)
	view := registerAttachment(t, env, strings.Repeat("a", 64), "spec.md", "text/markdown", []byte("# Spec"))
	got, err := env.svc.Get(requestContext(), env.versionID, view.ID)
	if err != nil || got.ID != view.ID {
		t.Fatalf("Get = %#v, %v", got, err)
	}
	if _, err := env.svc.Get(requestContext(), env.versionID+1, view.ID); err == nil {
		t.Fatal("cross-version attachment returned")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("status = %d", apiError(t, err).Status)
	}
}

func TestListAttachmentsAndReadExtractedText(t *testing.T) {
	env := newAttachmentEnv(t)
	env.repo.conversationMessages = map[int64]int64{7001: 8001}
	env.repo.conversations = map[int64]int64{8001: env.versionID}
	markdown := registerAttachment(t, env, strings.Repeat("a", 64), "spec.md", "text/markdown", []byte("# Spec"))
	image := registerAttachment(t, env, strings.Repeat("b", 64), "shot.png", "image/png", []byte("png"))
	env.extractor.text = "hello extracted text"
	if _, err := env.svc.Extract(requestContext(), markdown.ID); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	attachments, err := env.svc.ListAttachments(requestContext(), env.versionID, 8001)
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(attachments) != 2 || attachments[0].ID != markdown.ID || attachments[1].ID != image.ID {
		t.Fatalf("attachments = %#v", attachments)
	}
	if attachments[0].ConversationMessageID != 7001 || attachments[0].ExtractionStatus != version.AttachmentDone {
		t.Fatalf("meta = %#v", attachments[0])
	}
	if attachments[1].ExtractionStatus != version.AttachmentSkipped {
		t.Fatalf("image meta = %#v", attachments[1])
	}

	text, err := env.svc.ReadAttachmentText(requestContext(), env.versionID, 8001, markdown.ID, 6, 9)
	if err != nil {
		t.Fatalf("ReadAttachmentText: %v", err)
	}
	if text.Text != "extracted" || text.TotalChars != len("hello extracted text") || text.Status != version.AttachmentDone {
		t.Fatalf("text = %#v", text)
	}
	if _, err := env.svc.ReadAttachmentText(requestContext(), env.versionID, 8001, markdown.ID, 0, 0); err != nil {
		t.Fatalf("default max chars: %v", err)
	}
	if _, err := env.svc.ReadAttachmentText(requestContext(), env.versionID, 8001, image.ID, 0, 10); err != nil {
		t.Fatalf("image read: %v", err)
	}
	if _, err := env.svc.ReadAttachmentText(requestContext(), env.versionID, 8001, 999999, 0, 10); err == nil {
		t.Fatal("missing attachment text read")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("status = %d", apiError(t, err).Status)
	}
	if _, err := env.svc.ListAttachments(requestContext(), env.versionID+1, 8001); err == nil {
		t.Fatal("cross-version attachment list returned")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("status = %d", apiError(t, err).Status)
	}
}

func TestGetAttachmentInfoIncludesExtractedText(t *testing.T) {
	env := newAttachmentEnv(t)
	view := registerAttachment(t, env, strings.Repeat("a", 64), "spec.md", "text/markdown", []byte("# Spec"))
	env.extractor.text = "hello"
	if _, err := env.svc.Extract(requestContext(), view.ID); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	info, err := env.svc.GetAttachmentInfo(requestContext(), env.versionID, view.ID)
	if err != nil {
		t.Fatalf("GetAttachmentInfo: %v", err)
	}
	if info.ID != view.ID || info.Status != version.AttachmentDone || info.Text != "hello" || info.TotalChars != 5 {
		t.Fatalf("info = %#v", info)
	}
	if info.ConversationMessageID == nil || *info.ConversationMessageID != 7001 {
		t.Fatalf("message id = %#v", info.ConversationMessageID)
	}
	if _, err := env.svc.GetAttachmentInfo(requestContext(), env.versionID+1, view.ID); err == nil {
		t.Fatal("cross-version attachment info returned")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("status = %d", apiError(t, err).Status)
	}
}

func TestAttachmentImageRef(t *testing.T) {
	env := newAttachmentEnv(t)
	image := registerAttachment(t, env, strings.Repeat("b", 64), "shot.png", "image/png", []byte("png"))
	ref, err := env.svc.AttachmentImageRef(requestContext(), env.versionID, 8001, image.ID)
	if err != nil {
		t.Fatalf("AttachmentImageRef: %v", err)
	}
	if ref.SHA256 != strings.Repeat("b", 64) || ref.Name != "shot.png" || ref.MIME != "image/png" {
		t.Fatalf("ref = %#v", ref)
	}
	// A standalone image upload is registered skipped and has no persisted
	// attachment_image row (it is sent as an image part). That is metadata plus
	// an explicit empty-payload note, not an error.
	if len(ref.Images) != 0 {
		t.Fatalf("standalone image images = %#v, want empty", ref.Images)
	}
	if strings.TrimSpace(ref.Note) == "" {
		t.Fatalf("standalone image note = %q, want the explicit empty-caption note", ref.Note)
	}
	markdown := registerAttachment(t, env, strings.Repeat("a", 64), "spec.md", "text/markdown", []byte("# Spec"))
	if _, err := env.svc.AttachmentImageRef(requestContext(), env.versionID, 8001, markdown.ID); err == nil {
		t.Fatal("image ref returned for a markdown attachment without persisted images")
	}
}

// TestReadAttachmentTextReturnsImageCaptions pins C-01's primary exit: the
// attachment-level read exposes the persisted image rows with their captions so
// the Host can use the platform-generated analysis instead of a fact stream
// that ranks captions last.
func TestReadAttachmentTextReturnsImageCaptions(t *testing.T) {
	env := newParsingAttachmentEnv(t)
	data := docxWithImageFixture(t)
	sha := strings.Repeat("8", 64)
	env.store.put(sha, data, docxMIME)
	view, err := env.svc.RegisterFromMessage(requestContext(), env.versionID, 6001, 7001,
		attachmentPart(sha, "with-image.docx", docxMIME, int64(len(data))), 42)
	if err != nil {
		t.Fatalf("RegisterFromMessage: %v", err)
	}
	if _, err := env.svc.Extract(requestContext(), view.ID); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	read, err := env.svc.ReadAttachmentText(requestContext(), env.versionID, 8001, view.ID, 0, 100)
	if err != nil {
		t.Fatalf("ReadAttachmentText: %v", err)
	}
	if len(read.Images) != 1 {
		t.Fatalf("images = %#v, want the persisted image row", read.Images)
	}
	image := read.Images[0]
	if image.Seq != 1 || image.Status != version.AttachmentImageDone || image.Caption == "" {
		t.Fatalf("image = %#v, want seq/status/caption", image)
	}
	if image.SHA256 == "" || image.MIME != "image/png" || image.Width != 8 || image.Height != 6 {
		t.Fatalf("image metadata = %#v", image)
	}
	if read.ImageCount != 1 {
		t.Fatalf("image count = %d, want 1", read.ImageCount)
	}
}

// TestReadAttachmentTextPaginatesImages pins the C-01/M-07 paging contract: the
// image payload is only attached to the first page. Later pages must not repeat
// the captions, but still report image_count so the Host knows they exist.
func TestReadAttachmentTextPaginatesImages(t *testing.T) {
	env := newParsingAttachmentEnv(t)
	data := docxWithImageFixture(t)
	sha := strings.Repeat("7", 64)
	env.store.put(sha, data, docxMIME)
	view, err := env.svc.RegisterFromMessage(requestContext(), env.versionID, 6001, 7001,
		attachmentPart(sha, "with-image.docx", docxMIME, int64(len(data))), 42)
	if err != nil {
		t.Fatalf("RegisterFromMessage: %v", err)
	}
	if _, err := env.svc.Extract(requestContext(), view.ID); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	first, err := env.svc.ReadAttachmentText(requestContext(), env.versionID, 8001, view.ID, 0, 2)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Images) != 1 || first.ImageCount != 1 {
		t.Fatalf("first page images = %#v count = %d", first.Images, first.ImageCount)
	}
	if first.Text == "" || !first.Truncated {
		t.Fatalf("first page = %#v, want a truncated text window", first)
	}
	second, err := env.svc.ReadAttachmentText(requestContext(), env.versionID, 8001, view.ID, 2, 2)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Images) != 0 {
		t.Fatalf("second page repeated images: %#v", second.Images)
	}
	if second.ImageCount != 1 {
		t.Fatalf("second page image_count = %d, want 1", second.ImageCount)
	}
}

// TestReadAttachmentTextReturnsAllImageCaptions pins R2-C-01's version-layer
// exit: the caption budget/pagination now lives in the agent tools layer, so
// ReadAttachmentText returns every persisted image row with its complete
// caption on the first page. A 12-image document used to stop around image 10
// with a caption cut in half; that made images 11/12 unreachable.
func TestReadAttachmentTextReturnsAllImageCaptions(t *testing.T) {
	env := newAttachmentEnv(t)
	sha := strings.Repeat("6", 64)
	view := registerAttachment(t, env, sha, "many-images.docx", docxMIME, []byte("docx"))
	const imageTotal = 12
	for index := 1; index <= imageTotal; index++ {
		env.repo.images[int64(9100+index)] = version.AttachmentImageRecord{
			ID: int64(9100 + index), AttachmentID: view.ID, Seq: index,
			MIME: "image/png", Width: 8, Height: 6, SourceKind: version.AttachmentImageSourceEmbedded,
			Status: version.AttachmentImageDone, Caption: strings.Repeat("图", 800),
		}
	}

	read, err := env.svc.ReadAttachmentText(requestContext(), env.versionID, 8001, view.ID, 0, 8000)
	if err != nil {
		t.Fatalf("ReadAttachmentText: %v", err)
	}
	if read.ImageCount != imageTotal {
		t.Fatalf("image_count = %d, want %d", read.ImageCount, imageTotal)
	}
	if len(read.Images) != imageTotal {
		t.Fatalf("images = %d, want all %d captions (the version layer must not budget them)",
			len(read.Images), imageTotal)
	}
	for index, image := range read.Images {
		if got := len([]rune(image.Caption)); got != 800 {
			t.Fatalf("image %d caption runes = %d, want 800 (caption was truncated at the version layer)",
				index+1, got)
		}
	}
	if read.Images[0].ID != 9101 || read.Images[imageTotal-1].ID != int64(9100+imageTotal) {
		t.Fatalf("image ids = %d..%d, want 9101..%d",
			read.Images[0].ID, read.Images[imageTotal-1].ID, 9100+imageTotal)
	}

	// Later text pages still do not repeat the image payload, but they keep
	// reporting image_count so the Host knows images exist.
	later, err := env.svc.ReadAttachmentText(requestContext(), env.versionID, 8001, view.ID, 8000, 8000)
	if err != nil {
		t.Fatalf("later page: %v", err)
	}
	if len(later.Images) != 0 {
		t.Fatalf("later page repeated %d image captions", len(later.Images))
	}
	if later.ImageCount != imageTotal {
		t.Fatalf("later page image_count = %d, want %d", later.ImageCount, imageTotal)
	}
}

// TestReadAttachmentTextToleratesNilStorage pins the graceful degradation for
// partial services: a test (or degraded wiring) may construct the service with
// no content store, and reading image rows must still return the persisted
// metadata views with an empty SHA instead of panicking on the refs join.
func TestReadAttachmentTextToleratesNilStorage(t *testing.T) {
	env := newAttachmentEnv(t)
	sha := strings.Repeat("5", 64)
	view := registerAttachment(t, env, sha, "with-images.docx", docxMIME, []byte("docx"))
	env.repo.images[9101] = version.AttachmentImageRecord{
		ID: 9101, AttachmentID: view.ID, Seq: 1,
		MIME: "image/png", Width: 8, Height: 6, SourceKind: version.AttachmentImageSourceEmbedded,
		Status: version.AttachmentImageDone, Caption: "登录界面",
	}

	svc := versionapp.NewAttachmentService(
		env.repo, nil, env.queue, env.extractor, attachments.NewImageProber(), env.events, &seqIDs{}, fixedClock{at: testTime})
	read, err := svc.ReadAttachmentText(requestContext(), env.versionID, 8001, view.ID, 0, 100)
	if err != nil {
		t.Fatalf("ReadAttachmentText with nil storage: %v", err)
	}
	if read.ImageCount != 1 || len(read.Images) != 1 {
		t.Fatalf("images = %#v, want the persisted image row", read.Images)
	}
	if read.Images[0].SHA256 != "" {
		t.Fatalf("sha256 = %q, want empty without a content store", read.Images[0].SHA256)
	}
	if read.Images[0].Seq != 1 || read.Images[0].Caption != "登录界面" {
		t.Fatalf("image = %#v, want seq/caption preserved", read.Images[0])
	}
}

// TestAttachmentImageRefToleratesNilStorage mirrors the ReadAttachmentText
// degradation for the docx-with-images path: without a content store the
// original SHA cannot be resolved, but the attachment metadata and persisted
// image captions still return instead of panicking on the refs lookup.
func TestAttachmentImageRefToleratesNilStorage(t *testing.T) {
	env := newAttachmentEnv(t)
	sha := strings.Repeat("6", 64)
	view := registerAttachment(t, env, sha, "with-images.docx", docxMIME, []byte("docx"))
	env.repo.images[9102] = version.AttachmentImageRecord{
		ID: 9102, AttachmentID: view.ID, Seq: 1,
		MIME: "image/png", Width: 8, Height: 6, SourceKind: version.AttachmentImageSourceEmbedded,
		Status: version.AttachmentImageDone, Caption: "登录界面",
	}

	svc := versionapp.NewAttachmentService(
		env.repo, nil, env.queue, env.extractor, attachments.NewImageProber(), env.events, &seqIDs{}, fixedClock{at: testTime})
	ref, err := svc.AttachmentImageRef(requestContext(), env.versionID, 8001, view.ID)
	if err != nil {
		t.Fatalf("AttachmentImageRef with nil storage: %v", err)
	}
	if ref.SHA256 != "" {
		t.Fatalf("sha256 = %q, want empty without a content store", ref.SHA256)
	}
	if ref.Name != "with-images.docx" || ref.MIME != docxMIME {
		t.Fatalf("metadata = %#v, want the attachment name/MIME preserved", ref)
	}
	if len(ref.Images) != 1 || ref.Images[0].Caption != "登录界面" || ref.Images[0].Seq != 1 {
		t.Fatalf("images = %#v, want the persisted image row", ref.Images)
	}
}

// TestAttachmentImageRefIncludesPersistedCaption pins the second C-01 exit on
// the real UAT path: a docx attachment whose embedded images were extracted and
// analyzed returns those persisted rows/captions. The previous version of this
// test fabricated a row on a standalone image, a state no upload can produce.
func TestAttachmentImageRefIncludesPersistedCaption(t *testing.T) {
	env := newParsingAttachmentEnv(t)
	data := docxWithImageFixture(t)
	sha := strings.Repeat("8", 64)
	env.store.put(sha, data, docxMIME)
	view, err := env.svc.RegisterFromMessage(requestContext(), env.versionID, 6001, 7001,
		attachmentPart(sha, "with-image.docx", docxMIME, int64(len(data))), 42)
	if err != nil {
		t.Fatalf("RegisterFromMessage: %v", err)
	}
	if _, err := env.svc.Extract(requestContext(), view.ID); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	ref, err := env.svc.AttachmentImageRef(requestContext(), env.versionID, 8001, view.ID)
	if err != nil {
		t.Fatalf("AttachmentImageRef: %v", err)
	}
	if len(ref.Images) != 1 {
		t.Fatalf("images = %#v, want the persisted caption", ref.Images)
	}
	if ref.Images[0].Caption != "登录界面截图：手机号与验证码输入框" ||
		ref.Images[0].Status != version.AttachmentImageDone || ref.Images[0].Seq != 1 {
		t.Fatalf("image = %#v", ref.Images[0])
	}
	if ref.Images[0].SHA256 == "" || ref.Images[0].MIME != "image/png" {
		t.Fatalf("image metadata = %#v", ref.Images[0])
	}
	if ref.Name != "with-image.docx" || ref.MIME != docxMIME || ref.SHA256 != sha {
		t.Fatalf("attachment metadata = %#v", ref)
	}
	if ref.Note != "" {
		t.Fatalf("note = %q, want empty for a document with persisted captions", ref.Note)
	}
}

// TestAttachmentImageRefByImageResolvesParentAttachment pins R2-C-02: the image
// id returned in attachment.read_text images[] (attachment_image.id) resolves
// its parent attachment inside the same conversation. A foreign image id or an
// image row reachable only through another conversation of the same version
// stays not_found, so the image path cannot leak cross-conversation attachments.
func TestAttachmentImageRefByImageResolvesParentAttachment(t *testing.T) {
	env := newAttachmentEnv(t)
	env.repo.conversationMessages = map[int64]int64{7001: 8001}
	env.repo.conversations = map[int64]int64{8001: env.versionID, 8002: env.versionID}
	view := registerAttachment(t, env, strings.Repeat("a", 64), "with-image.docx", docxMIME, []byte("docx"))
	env.repo.images[9301] = version.AttachmentImageRecord{
		ID: 9301, AttachmentID: view.ID, Seq: 1,
		MIME: "image/png", Width: 8, Height: 6, SourceKind: version.AttachmentImageSourceEmbedded,
		Status: version.AttachmentImageDone, Caption: "登录界面截图",
	}

	ref, err := env.svc.AttachmentImageRefByImage(requestContext(), env.versionID, 8001, 9301)
	if err != nil {
		t.Fatalf("AttachmentImageRefByImage: %v", err)
	}
	if ref.AttachmentID != view.ID {
		t.Fatalf("attachment id = %d, want the parent %d", ref.AttachmentID, view.ID)
	}
	if len(ref.Images) != 1 || ref.Images[0].ID != 9301 || ref.Images[0].Caption != "登录界面截图" {
		t.Fatalf("images = %#v, want the persisted parent row/caption", ref.Images)
	}
	if ref.Images[0].Seq != 1 || ref.Images[0].Status != version.AttachmentImageDone {
		t.Fatalf("image metadata = %#v", ref.Images[0])
	}
	if ref.Name != "with-image.docx" || ref.MIME != docxMIME {
		t.Fatalf("metadata = %#v, want the parent attachment metadata", ref)
	}

	// Unknown image id: not_found, never an empty success.
	if _, err := env.svc.AttachmentImageRefByImage(requestContext(), env.versionID, 8001, 999999); err == nil {
		t.Fatal("unknown image id returned a reference")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("unknown image status = %d, want 404", apiError(t, err).Status)
	}
	// An image id owned by another conversation of the same version must stay
	// invisible: the scan runs over ListByConversation only.
	if _, err := env.svc.AttachmentImageRefByImage(requestContext(), env.versionID, 8002, 9301); err == nil {
		t.Fatal("cross-conversation image id returned a reference")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("cross-conversation image status = %d, want 404", apiError(t, err).Status)
	}
}

func TestListPendingForIngestClampsLimit(t *testing.T) {
	env := newAttachmentEnv(t)
	env.repo.pendingForIngest = []version.PendingAttachment{{ID: 5, VersionID: 2, ConversationID: 3, MessageID: 4}}

	pending, err := env.svc.ListPendingForIngest(requestContext(), 500)
	if err != nil {
		t.Fatalf("ListPendingForIngest: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != 5 || pending[0].ConversationID != 3 || pending[0].MessageID != 4 {
		t.Fatalf("pending = %#v", pending)
	}
	if env.repo.lastPendingLimit != 100 {
		t.Fatalf("limit = %d, want clamped 100", env.repo.lastPendingLimit)
	}
	if _, err := env.svc.ListPendingForIngest(requestContext(), 0); err != nil {
		t.Fatalf("zero limit: %v", err)
	}
	if env.repo.lastPendingLimit != 1 {
		t.Fatalf("zero limit = %d, want clamped 1", env.repo.lastPendingLimit)
	}
}

// TestReadPendingAttachmentReturnsStructuredStatus covers the 0.2.1 §8.5
// contract: a non-done attachment is a structured status, not a 409
// "attachment text is not available".
func TestReadPendingAttachmentReturnsStructuredStatus(t *testing.T) {
	env := newAttachmentEnv(t)
	env.repo.conversationMessages = map[int64]int64{7001: 8001}
	env.repo.conversations = map[int64]int64{8001: env.versionID}
	sha := strings.Repeat("a", 64)
	pending := registerAttachment(t, env, sha, "spec.md", "text/markdown", []byte("# Spec"))

	result, err := env.svc.ReadAttachmentText(requestContext(), env.versionID, 8001, pending.ID, 0, 10)
	if err != nil {
		t.Fatalf("pending read returned a Go error: %v", err)
	}
	if result.Status != version.AttachmentPending || !result.Retryable || result.Text != "" {
		t.Fatalf("pending result = %#v", result)
	}
	if !strings.Contains(result.Error, "解析中") {
		t.Fatalf("pending error = %q, want a retry hint", result.Error)
	}
	if result.AttachmentID != pending.ID || result.Name != "spec.md" || result.MIME != "text/markdown" {
		t.Fatalf("pending metadata = %#v", result)
	}

	env.extractor.text = "hello extracted text"
	if _, err := env.svc.Extract(requestContext(), pending.ID); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	done, err := env.svc.ReadAttachmentText(requestContext(), env.versionID, 8001, pending.ID, 6, 9)
	if err != nil {
		t.Fatalf("done read: %v", err)
	}
	if done.Status != version.AttachmentDone || done.Text != "extracted" || done.Retryable ||
		done.TotalChars != len("hello extracted text") {
		t.Fatalf("done result = %#v", done)
	}

	image := registerAttachment(t, env, strings.Repeat("b", 64), "shot.png", "image/png", []byte("png"))
	skipped, err := env.svc.ReadAttachmentText(requestContext(), env.versionID, 8001, image.ID, 0, 10)
	if err != nil {
		t.Fatalf("skipped read returned a Go error: %v", err)
	}
	if skipped.Status != version.AttachmentSkipped || skipped.Retryable || skipped.Error == "" {
		t.Fatalf("skipped result = %#v", skipped)
	}

	legacy := registerAttachment(t, env, strings.Repeat("c", 64), "legacy.doc", "application/msword", []byte("doc"))
	failed, err := env.svc.ReadAttachmentText(requestContext(), env.versionID, 8001, legacy.ID, 0, 10)
	if err != nil {
		t.Fatalf("failed read returned a Go error: %v", err)
	}
	if failed.Status != version.AttachmentFailed || failed.Retryable {
		t.Fatalf("failed result = %#v", failed)
	}
	if !strings.Contains(strings.ToLower(failed.Error), "docx") {
		t.Fatalf("failed error = %q, want the save-as hint", failed.Error)
	}
}

// TestPDFNoTextLayerFlowsThroughRead covers R33: the contract enum only allows
// unsupported_attachment_format|extraction_failed, so a PDF sentinel raised by
// the parser maps to extraction_failed while its "no text layer" reason still
// flows through failExtraction -> attachment row -> ReadAttachmentText.
func TestPDFNoTextLayerFlowsThroughRead(t *testing.T) {
	env := newAttachmentEnv(t)
	env.repo.conversationMessages = map[int64]int64{7001: 8001}
	env.repo.conversations = map[int64]int64{8001: env.versionID}
	sha := strings.Repeat("d", 64)
	view := registerAttachment(t, env, sha, "scan.pdf", "application/pdf", []byte("%PDF-1.7"))

	env.extractor.err = fmt.Errorf("%w: scanned or image-only PDFs are not supported", attachments.ErrPDFNoTextLayer)
	result, err := env.svc.Extract(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.ErrorCode != version.AttachmentErrorExtraction {
		t.Fatalf("error code = %q, want %q", result.ErrorCode, version.AttachmentErrorExtraction)
	}
	stored := env.repo.attachments[view.ID]
	if stored.ExtractionStatus != version.AttachmentFailed ||
		stored.ExtractionErrorCode != version.AttachmentErrorExtraction {
		t.Fatalf("stored = %#v", stored)
	}
	if !strings.Contains(strings.ToLower(stored.ExtractionErrorText), "no text layer") {
		t.Fatalf("stored error = %q, want the parser's no-text-layer reason", stored.ExtractionErrorText)
	}

	read, err := env.svc.ReadAttachmentText(requestContext(), env.versionID, 8001, view.ID, 0, 10)
	if err != nil {
		t.Fatalf("ReadAttachmentText: %v", err)
	}
	if read.Status != version.AttachmentFailed || read.Retryable {
		t.Fatalf("read = %#v", read)
	}
	if !strings.Contains(strings.ToLower(read.Error), "no text layer") {
		t.Fatalf("read error = %q, want the no-text-layer message", read.Error)
	}
	if strings.Contains(strings.ToLower(read.Error), "text is not available") {
		t.Fatalf("read error = %q, must not be the generic unavailable message", read.Error)
	}
}

// TestUserMessageImagePartPayload pins the R5 image part shape on the
// conversation message: {type:"image", mime, data_ref, width, height}.
func TestUserMessageImagePartPayload(t *testing.T) {
	sha := strings.Repeat("e", 64)
	part := version.MessagePart{Type: version.MessagePartImage, MIME: "image/png", DataRef: sha, Width: 800, Height: 600}
	if err := version.ValidateUserMessageParts([]version.MessagePart{part}); err != nil {
		t.Fatalf("valid image part rejected: %v", err)
	}
	raw, err := json.Marshal(part)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["type"] != "image" || payload["mime"] != "image/png" || payload["data_ref"] != sha ||
		payload["width"] != float64(800) || payload["height"] != float64(600) {
		t.Fatalf("payload = %#v", payload)
	}
	if _, ok := payload["sha256"]; ok {
		t.Fatalf("image payload leaked attachment fields: %#v", payload)
	}
	var decoded version.MessagePart
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Type != version.MessagePartImage || decoded.DataRef != sha || decoded.Width != 800 || decoded.Height != 600 {
		t.Fatalf("decoded = %#v", decoded)
	}

	bad := []version.MessagePart{
		{Type: version.MessagePartImage, MIME: "text/plain", DataRef: sha, Width: 1, Height: 1},
		{Type: version.MessagePartImage, MIME: "image/png", DataRef: "nope", Width: 1, Height: 1},
		{Type: version.MessagePartImage, MIME: "image/png", DataRef: sha, Width: 0, Height: 1},
	}
	for _, candidate := range bad {
		if err := version.ValidateUserMessageParts([]version.MessagePart{candidate}); err == nil {
			t.Fatalf("invalid image part accepted: %#v", candidate)
		}
	}
}

// TestLegacyAttachmentSaveAsHint covers R26: the upload path must use
// version.IsLegacyOfficeFormat instead of a generic unsupported-format
// message, and failExtraction must preserve a detailed parser error.
func TestLegacyAttachmentSaveAsHint(t *testing.T) {
	env := newAttachmentEnv(t)
	generic := "upload md/txt/docx/pptx/pdf or an image"
	for index, tc := range []struct {
		name string
		mime string
	}{
		{"requirements.doc", "application/msword"},
		{"deck.ppt", "application/vnd.ms-powerpoint"},
	} {
		view := registerAttachment(t, env, strings.Repeat(fmt.Sprintf("%d", index+1), 64), tc.name, tc.mime, []byte("legacy"))
		if view.ExtractionStatus != version.AttachmentFailed || view.ExtractionErrorCode != version.AttachmentErrorUnsupported {
			t.Fatalf("%s view = %#v", tc.name, view)
		}
		message := strings.ToLower(view.ExtractionErrorText)
		for _, want := range []string{"save", "docx", "pptx"} {
			if !strings.Contains(message, want) {
				t.Fatalf("%s error = %q, want %q", tc.name, view.ExtractionErrorText, want)
			}
		}
		if strings.Contains(message, generic) {
			t.Fatalf("%s kept the generic unsupported-format message: %q", tc.name, view.ExtractionErrorText)
		}
	}

	// A parser that still yields the detailed legacy error must have that
	// detail persisted by failExtraction instead of the generic message.
	docx := registerAttachment(t, env, strings.Repeat("f", 64), "broken.docx", "application/octet-stream", []byte("docx"))
	env.extractor.err = fmt.Errorf("%w: requirements.doc", version.ErrUnsupportedLegacyAttachment)
	result, err := env.svc.Extract(requestContext(), docx.ID)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.ErrorCode != version.AttachmentErrorUnsupported {
		t.Fatalf("error code = %q", result.ErrorCode)
	}
	stored := env.repo.attachments[docx.ID]
	if stored.ExtractionErrorText == "" || !strings.Contains(strings.ToLower(stored.ExtractionErrorText), "requirements.doc") {
		t.Fatalf("parser detail overwritten: %q", stored.ExtractionErrorText)
	}
	if strings.Contains(stored.ExtractionErrorText, generic) {
		t.Fatalf("parser detail replaced by generic message: %q", stored.ExtractionErrorText)
	}
	lastEvent := env.events.events[len(env.events.events)-1]
	if lastEvent.Type != "requirement.attachment_failed" || !strings.Contains(lastEvent.Payload["message"].(string), "requirements.doc") {
		t.Fatalf("event = %#v", lastEvent)
	}
}

// ---------- upload-then-immediate-chat harness (R-E4-1/R-E4-2) ----------

// uploadHarness reproduces the Composer path for "upload a document, then chat
// immediately": the content object is already uploaded and the user message is
// posted at once while extraction runs asynchronously. A brand-new document is
// pre-registered by the message post preflight (its conversation_message_id is
// still NULL) and the first post is gated with the retryable 409 while ingest
// is pending. Everything the attachment.ingest run reads during that window
// must still see the attachment instead of an empty conversation list or a
// not_found.
type uploadHarness struct {
	t              *testing.T
	env            *imageConversationEnv
	conversationID int64
	parts          []version.MessagePart
	attachmentID   int64
}

func newAttachmentHarness(t *testing.T) *uploadHarness {
	t.Helper()
	env := newImageConversationEnv(t)
	return &uploadHarness{t: t, env: env, conversationID: env.conversation.ID}
}

// Upload stores the document content and posts the user message. The message
// post pre-registers the attachment row and enqueues ingest; while the parse is
// pending the post is rejected with the typed 409, so the row stays unlinked
// (message binding deferred) exactly like the live race.
func (h *uploadHarness) Upload(name string) {
	h.t.Helper()
	data := []byte("# 需求\n登录、退款")
	sha := strings.Repeat("7", 64)
	h.env.attachments.store.put(sha, data, docxMIME)
	h.parts = []version.MessagePart{
		{Type: version.MessagePartText, Text: "请读附件"},
		attachmentPart(sha, name, docxMIME, int64(len(data))),
	}
	_, err := h.env.svc.PostUserMessage(
		requestContext(), h.env.versionID, h.conversationID, h.parts, "upload-immediate-1", 42)
	if err == nil {
		h.t.Fatal("first post of a still-parsing attachment was accepted")
	}
	apiErr := apiError(h.t, err)
	if apiErr.Code != versionapp.AttachmentPendingCode {
		h.t.Fatalf("first post code = %q, want %q", apiErr.Code, versionapp.AttachmentPendingCode)
	}
	rawID, _ := apiErr.Details["attachment_id"].(string)
	id, parseErr := strconv.ParseInt(rawID, 10, 64)
	if parseErr != nil || id <= 0 {
		h.t.Fatalf("attachment_id detail = %q", rawID)
	}
	h.attachmentID = id
}

// List is the attachment.list read of the conversation.
func (h *uploadHarness) List() []versionapp.AttachmentMeta {
	h.t.Helper()
	metas, err := h.env.attachments.svc.ListAttachments(requestContext(), h.env.versionID, h.conversationID)
	if err != nil {
		h.t.Fatalf("ListAttachments: %v", err)
	}
	return metas
}

// ReadText is the attachment.read_text read by attachment ID.
func (h *uploadHarness) ReadText() versionapp.AttachmentTextRead {
	h.t.Helper()
	read, err := h.env.attachments.svc.ReadAttachmentText(
		requestContext(), h.env.versionID, h.conversationID, h.attachmentID, 0, 0)
	if err != nil {
		h.t.Fatalf("ReadAttachmentText: %v", err)
	}
	return read
}

// Ingest runs the attachment.ingest worker while the message binding may still
// be deferred.
func (h *uploadHarness) Ingest(text string) {
	h.t.Helper()
	h.env.attachments.extractor.text = text
	if _, err := h.env.attachments.svc.Extract(requestContext(), h.attachmentID); err != nil {
		h.t.Fatalf("Extract: %v", err)
	}
}

// Retry posts the same user message again once ingest finished; this is the
// client retry after the 409 and must bind the pre-registered row.
func (h *uploadHarness) Retry(clientMessageID string) versionapp.MessageView {
	h.t.Helper()
	message, err := h.env.svc.PostUserMessage(
		requestContext(), h.env.versionID, h.conversationID, h.parts, clientMessageID, 42)
	if err != nil {
		h.t.Fatalf("retry post: %v", err)
	}
	return message
}

// ingestTasks returns the attachment.ingest tasks queued for this attachment.
func (h *uploadHarness) ingestTasks() []versionapp.TaskInput {
	h.t.Helper()
	tasks := make([]versionapp.TaskInput, 0, 1)
	for _, task := range h.env.attachments.queue.tasks {
		if task.Type == "attachment.ingest" && task.Payload["attachment_id"] == idString(h.attachmentID) {
			tasks = append(tasks, task)
		}
	}
	return tasks
}

// TestUploadThenImmediateList covers R-E4-2: immediately after uploading a
// document and attempting the first chat, attachment.list returns the
// pre-registered attachment (not 0) and attachment.read_text returns the
// structured "still parsing, retry" result (not not_found, and not a Go error).
func TestUploadThenImmediateList(t *testing.T) {
	h := newAttachmentHarness(t)
	h.Upload("spec.docx")

	listed := h.List()
	if len(listed) != 1 {
		t.Fatalf("list=%d, want 1", len(listed))
	}
	if listed[0].ID != h.attachmentID || listed[0].Name != "spec.docx" {
		t.Fatalf("listed = %#v", listed[0])
	}
	if listed[0].ConversationMessageID != 0 {
		t.Fatalf("pre-registered attachment was already bound: %#v", listed[0])
	}
	if listed[0].ExtractionStatus != version.AttachmentPending {
		t.Fatalf("status = %q, want pending", listed[0].ExtractionStatus)
	}

	read := h.ReadText()
	if read.Status != version.AttachmentPending || !read.Retryable {
		t.Fatalf("read_text = %#v, want a retryable pending result", read)
	}
	if read.AttachmentID != h.attachmentID || read.Name != "spec.docx" {
		t.Fatalf("read metadata = %#v", read)
	}
	if !strings.Contains(read.Error, "解析中") {
		t.Fatalf("read error = %q, want the parsing hint", read.Error)
	}
	t.Logf("attachment.list -> %+v", listed)
	t.Logf("attachment.read_text -> %+v", read)
}

// TestPreRegisteredAttachmentNotVisibleToOtherConversation covers the Task 20
// follow-up scope leak: a document pre-registered by a blocked post in
// conversation A is unbound until the retry, but that must not make it visible
// to another conversation of the same version. Conversation A keeps the R-E4-2
// behavior (its own draft is listed); conversation B must neither list nor read
// it. This is the regression test for the version-scoped unbound-row branch.
func TestPreRegisteredAttachmentNotVisibleToOtherConversation(t *testing.T) {
	env := newAttachmentEnv(t)
	const conversationA, conversationB = int64(6001), int64(6002)
	data := []byte("# 需求\n登录")
	sha := strings.Repeat("9", 64)
	env.store.put(sha, data, "text/markdown")
	parts := []version.MessagePart{attachmentPart(sha, "spec.md", "text/markdown", int64(len(data)))}

	_, _, err := env.svc.PrepareUserMessageParts(requestContext(), env.versionID, conversationA, parts, 42)
	if err == nil {
		t.Fatal("first post of a still-parsing attachment was accepted")
	}
	apiErr := apiError(t, err)
	if apiErr.Code != versionapp.AttachmentPendingCode {
		t.Fatalf("first post code = %q, want %q", apiErr.Code, versionapp.AttachmentPendingCode)
	}
	rawID, _ := apiErr.Details["attachment_id"].(string)
	attachmentID, parseErr := strconv.ParseInt(rawID, 10, 64)
	if parseErr != nil || attachmentID <= 0 {
		t.Fatalf("attachment_id detail = %q", rawID)
	}

	// Conversation A still sees its own pre-registered draft (R-E4-2).
	own, err := env.svc.ListAttachments(requestContext(), env.versionID, conversationA)
	if err != nil {
		t.Fatalf("ListAttachments(A): %v", err)
	}
	if len(own) != 1 || own[0].ID != attachmentID || own[0].ConversationMessageID != 0 {
		t.Fatalf("conversation A list = %#v", own)
	}

	// Conversation B must never see conversation A's unbound draft.
	other, err := env.svc.ListAttachments(requestContext(), env.versionID, conversationB)
	if err != nil {
		t.Fatalf("ListAttachments(B): %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("conversation B sees conversation A's pre-registered draft: %#v", other)
	}

	// read_text by attachment id in conversation B must not resolve it either.
	if _, err := env.svc.ReadAttachmentText(requestContext(), env.versionID, conversationB, attachmentID, 0, 10); err == nil {
		t.Fatal("conversation B read conversation A's pre-registered draft")
	} else if readErr := apiError(t, err); readErr.Status != http.StatusNotFound {
		t.Fatalf("read_text error = %#v, want 404 not_found", readErr)
	}
}

// TestIngestByAttachmentIDWhileBindingDeferred covers R-E4-1's second clause:
// attachment.ingest resolves and extracts the row by attachment_id only, so it
// completes even though the conversation message binding has not happened yet;
// the extracted text is then readable through the conversation without a
// not_found.
func TestIngestByAttachmentIDWhileBindingDeferred(t *testing.T) {
	h := newAttachmentHarness(t)
	h.Upload("spec.docx")

	h.Ingest("完整需求正文：登录、退款")
	stored := h.env.attachments.repo.attachments[h.attachmentID]
	if stored.ConversationMessageID != nil {
		t.Fatalf("binding was not deferred: %#v", stored)
	}
	if stored.ExtractionStatus != version.AttachmentDone {
		t.Fatalf("stored status = %q", stored.ExtractionStatus)
	}

	read := h.ReadText()
	if read.Status != version.AttachmentDone || !strings.Contains(read.Text, "完整需求正文") {
		t.Fatalf("read_text = %#v", read)
	}
	listed := h.List()
	if len(listed) != 1 || listed[0].ExtractionStatus != version.AttachmentDone {
		t.Fatalf("list = %#v", listed)
	}
}

// TestRetryBindsOnceWithoutLegacyIngestTask covers the ordering/idempotency
// half of R-E4-1: the deterministic worker owns extraction (no legacy ingest
// work item), the retry binds the pre-registered row to the inserted message
// (no duplicate row), and a repeated post with the same client_message_id does
// not re-bind.
func TestRetryBindsOnceWithoutLegacyIngestTask(t *testing.T) {
	h := newAttachmentHarness(t)
	h.Upload("spec.docx")
	if tasks := h.ingestTasks(); len(tasks) != 0 {
		t.Fatalf("upload enqueued a legacy ingest task: %#v", tasks)
	}

	h.Ingest("附件正文")
	message := h.Retry("upload-immediate-2")
	if message.ID <= 0 || len(h.env.repo.messages[h.conversationID]) != 1 {
		t.Fatalf("retry message = %#v", message)
	}
	rows := h.env.attachments.repo.attachments
	if len(rows) != 1 {
		t.Fatalf("attachment rows = %d, want 1", len(rows))
	}
	bound := rows[h.attachmentID]
	if bound.ConversationMessageID == nil || *bound.ConversationMessageID != message.ID {
		t.Fatalf("bound = %#v (message %d)", bound, message.ID)
	}
	if tasks := h.ingestTasks(); len(tasks) != 0 {
		t.Fatalf("retry enqueued a legacy ingest task: %#v", tasks)
	}

	read := h.ReadText()
	if read.Status != version.AttachmentDone || read.Text != "附件正文" {
		t.Fatalf("read after binding = %#v", read)
	}
	listed := h.List()
	if len(listed) != 1 || listed[0].ConversationMessageID != message.ID {
		t.Fatalf("list after binding = %#v", listed)
	}

	// The idempotent client retry returns the stored message without touching
	// the binding or the ingest queue.
	again := h.Retry("upload-immediate-2")
	if again.ID != message.ID {
		t.Fatalf("idempotent retry message = %d, want %d", again.ID, message.ID)
	}
	rows = h.env.attachments.repo.attachments
	if len(rows) != 1 || rows[h.attachmentID].ConversationMessageID == nil ||
		*rows[h.attachmentID].ConversationMessageID != message.ID {
		t.Fatalf("rows after idempotent retry = %#v", rows)
	}
	if tasks := h.ingestTasks(); len(tasks) != 0 {
		t.Fatalf("idempotent retry enqueued a legacy ingest task: %#v", tasks)
	}
}

// ---------- version attachment list (C-05) ----------

// TestListVersionAttachments pins the version attachment panel read (C-05): the
// rows are newest-first, carry the stable extraction error pair and the number
// of extracted images, and attachments of another version are never listed.
func TestListVersionAttachments(t *testing.T) {
	env := newAttachmentEnv(t)
	env.repo.attachments[101] = version.Attachment{
		ID: 101, VersionID: env.versionID, Name: "spec.pdf", MIME: "application/pdf", Size: 2048,
		ExtractionStatus:    version.AttachmentFailed,
		ExtractionErrorCode: version.AttachmentErrorExtraction,
		ExtractionErrorText: "parser crashed",
	}
	env.repo.attachments[102] = version.Attachment{
		ID: 102, VersionID: env.versionID, Name: "design.docx",
		MIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		Size: 4096, ExtractionStatus: version.AttachmentDone,
	}
	// Another version's attachment must not leak into this version's panel.
	env.repo.attachments[103] = version.Attachment{
		ID: 103, VersionID: env.versionID + 1, Name: "foreign.txt", MIME: "text/plain",
		Size: 64, ExtractionStatus: version.AttachmentDone,
	}
	for seq := 1; seq <= 2; seq++ {
		env.repo.images[int64(200+seq)] = version.AttachmentImageRecord{
			ID: int64(200 + seq), AttachmentID: 102, Seq: seq, ContentRefID: int64(300 + seq),
			MIME: "image/png", SourceKind: version.AttachmentImageSourceEmbedded,
			Status: version.AttachmentImageDone, CreatedAt: testTime, UpdatedAt: testTime,
		}
	}

	views, err := env.svc.ListVersionAttachments(requestContext(), env.versionID)
	if err != nil {
		t.Fatalf("ListVersionAttachments: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("views = %#v, want the two rows of this version", views)
	}
	if views[0].ID != 102 || views[1].ID != 101 {
		t.Fatalf("order = %d,%d, want newest (ID desc)", views[0].ID, views[1].ID)
	}
	done := views[0]
	if done.Name != "design.docx" || done.MIME != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" ||
		done.Size != 4096 || done.ExtractionStatus != version.AttachmentDone ||
		done.ErrorCode != "" || done.ErrorText != "" || done.ImageCount != 2 {
		t.Fatalf("done view = %#v", done)
	}
	failed := views[1]
	if failed.Name != "spec.pdf" || failed.MIME != "application/pdf" || failed.Size != 2048 ||
		failed.ExtractionStatus != version.AttachmentFailed ||
		failed.ErrorCode != version.AttachmentErrorExtraction || failed.ErrorText != "parser crashed" ||
		failed.ImageCount != 0 {
		t.Fatalf("failed view = %#v", failed)
	}
}

// TestListVersionAttachmentsEmptyAndUnknownVersion covers the empty panel (a
// non-nil empty slice so JSON is [] not null), an unknown/foreign version (404,
// matching the image route semantics) and an invalid ID (400).
func TestListVersionAttachmentsEmptyAndUnknownVersion(t *testing.T) {
	env := newAttachmentEnv(t)
	env.repo.existingVersions = map[int64]bool{env.versionID: true}

	views, err := env.svc.ListVersionAttachments(requestContext(), env.versionID)
	if err != nil {
		t.Fatalf("ListVersionAttachments empty: %v", err)
	}
	if views == nil || len(views) != 0 {
		t.Fatalf("empty list = %#v, want a non-nil empty slice", views)
	}

	if _, err := env.svc.ListVersionAttachments(requestContext(), env.versionID+1); err == nil {
		t.Fatal("unknown version was listed")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("unknown version status = %d, want 404", apiError(t, err).Status)
	}
	if _, err := env.svc.ListVersionAttachments(requestContext(), 0); err == nil {
		t.Fatal("invalid version id was accepted")
	} else if apiError(t, err).Status != http.StatusBadRequest {
		t.Fatalf("invalid id status = %d, want 400", apiError(t, err).Status)
	}
}
