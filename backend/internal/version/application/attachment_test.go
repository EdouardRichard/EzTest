package versionapp_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ---------- fakes ----------

type fakeAttachmentRepo struct {
	attachments          map[int64]version.Attachment
	conversationMessages map[int64]int64 // message ID -> conversation ID
	conversations        map[int64]int64 // conversation ID -> version ID
	createErr            error
}

func newFakeAttachmentRepo() *fakeAttachmentRepo {
	return &fakeAttachmentRepo{attachments: map[int64]version.Attachment{}}
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

func (f *fakeAttachmentRepo) ListByConversation(_ context.Context, versionID, conversationID int64) ([]version.Attachment, error) {
	items := make([]version.Attachment, 0)
	for _, attachment := range f.attachments {
		if attachment.VersionID != versionID || attachment.ConversationMessageID == nil {
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

func (f *fakeAttachmentRepo) ConversationExists(_ context.Context, versionID, conversationID int64) (bool, error) {
	if len(f.conversations) == 0 {
		return true, nil
	}
	conversationVersion, ok := f.conversations[conversationID]
	return ok && conversationVersion == versionID, nil
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
	sha := strings.Repeat("f", 64)
	f.objects[sha] = versionapp.ContentObject{SHA256: sha, Size: int64(len(data)), MIME: mime}
	f.data[sha] = data
	f.uploads = append(f.uploads, filename+"|"+mime+"|"+string(data))
	return f.objects[sha], nil
}

type fakeExtractor struct {
	text     string
	err      error
	calls    int
	lastName string
}

func (f *fakeExtractor) Extract(_ context.Context, name, _ string, r io.Reader) (string, error) {
	f.calls++
	f.lastName = name
	_, _ = io.ReadAll(r)
	if f.err != nil {
		return "", f.err
	}
	return f.text, nil
}

// ---------- environment ----------

type attachmentEnv struct {
	svc       *versionapp.AttachmentService
	repo      *fakeAttachmentRepo
	store     *fakeContentStore
	queue     *fakeQueue
	extractor *fakeExtractor
	events    *fakePublisher
	versionID int64
}

func newAttachmentEnv(t *testing.T) *attachmentEnv {
	t.Helper()
	repo := newFakeAttachmentRepo()
	store := newFakeContentStore()
	queue := &fakeQueue{}
	extractor := &fakeExtractor{text: "extracted text"}
	events := &fakePublisher{}
	svc := versionapp.NewAttachmentService(repo, store, queue, extractor, events, &seqIDs{}, fixedClock{at: testTime})
	return &attachmentEnv{svc: svc, repo: repo, store: store, queue: queue, extractor: extractor, events: events, versionID: 1}
}

func attachmentPart(sha, name, mime string, size int64) version.MessagePart {
	return version.MessagePart{Type: "attachment", SHA256: sha, Name: name, MIME: mime, Size: size}
}

func registerAttachment(t *testing.T, env *attachmentEnv, sha, name, mime string, data []byte) versionapp.AttachmentView {
	t.Helper()
	env.store.put(sha, data, mime)
	view, err := env.svc.RegisterFromMessage(requestContext(), env.versionID, 7001, attachmentPart(sha, name, mime, int64(len(data))), 42)
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

	task, ok := env.queue.find("attachment.ingest", "attachment:"+idString(view.ID))
	if !ok {
		t.Fatalf("ingest task missing: %#v", env.queue.tasks)
	}
	if task.VersionID != env.versionID || task.Payload["attachment_id"] != idString(view.ID) || task.Payload["message_id"] != "7001" {
		t.Fatalf("task = %#v", task)
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
			_, err := env.svc.RegisterFromMessage(requestContext(), env.versionID, 7001, tc.part, 42)
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

	text, total, err := env.svc.ReadAttachmentText(requestContext(), env.versionID, 8001, markdown.ID, 6, 9)
	if err != nil {
		t.Fatalf("ReadAttachmentText: %v", err)
	}
	if text != "extracted" || total != len("hello extracted text") {
		t.Fatalf("text = %q, total = %d", text, total)
	}
	if _, _, err := env.svc.ReadAttachmentText(requestContext(), env.versionID, 8001, markdown.ID, 0, 0); err != nil {
		t.Fatalf("default max chars: %v", err)
	}
	if _, _, err := env.svc.ReadAttachmentText(requestContext(), env.versionID, 8001, image.ID, 0, 10); err == nil {
		t.Fatal("text read from an image attachment")
	}
	if _, _, err := env.svc.ReadAttachmentText(requestContext(), env.versionID, 8001, 999999, 0, 10); err == nil {
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
	markdown := registerAttachment(t, env, strings.Repeat("a", 64), "spec.md", "text/markdown", []byte("# Spec"))
	if _, err := env.svc.AttachmentImageRef(requestContext(), env.versionID, 8001, markdown.ID); err == nil {
		t.Fatal("image ref returned for a markdown attachment")
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
