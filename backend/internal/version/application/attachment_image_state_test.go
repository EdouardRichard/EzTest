package versionapp_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/attachments"
)

const docxMIME = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

// ---------- image state fakes ----------

// SetExtractedContentRef persists the extracted-text reference before image
// analysis so a later analysis failure can still mark the attachment failed
// while keeping the text for a successful reanalyze.
func (f *fakeAttachmentRepo) SetExtractedContentRef(_ context.Context, attachmentID int64, contentRefID int64, updatedAt time.Time) error {
	attachment, ok := f.attachments[attachmentID]
	if !ok {
		return version.ErrAttachmentNotFound
	}
	ref := contentRefID
	attachment.ExtractedContentRefID = &ref
	attachment.UpdatedAt = updatedAt
	f.attachments[attachmentID] = attachment
	return nil
}

func (f *fakeAttachmentRepo) CreateImage(_ context.Context, image version.AttachmentImageRecord) error {
	for _, existing := range f.images {
		if existing.AttachmentID == image.AttachmentID && existing.Seq == image.Seq {
			return nil
		}
	}
	f.images[image.ID] = image
	f.imageStatusLog = append(f.imageStatusLog, idString(int64(image.Seq))+":"+image.Status)
	return nil
}

func (f *fakeAttachmentRepo) ListImages(_ context.Context, attachmentID int64) ([]version.AttachmentImageRecord, error) {
	items := make([]version.AttachmentImageRecord, 0)
	for _, image := range f.images {
		if image.AttachmentID == attachmentID {
			items = append(items, image)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Seq < items[j].Seq })
	return items, nil
}

func (f *fakeAttachmentRepo) MarkImageAnalyzing(_ context.Context, imageID int64, updatedAt time.Time) (bool, error) {
	image, ok := f.images[imageID]
	if !ok {
		return false, version.ErrAttachmentNotFound
	}
	switch image.Status {
	case version.AttachmentImageExtracted, version.AttachmentImageSkipped, version.AttachmentImageFailed:
		image.Status = version.AttachmentImageAnalyzing
		image.UpdatedAt = updatedAt
		f.images[imageID] = image
		f.imageStatusLog = append(f.imageStatusLog, idString(int64(image.Seq))+":"+image.Status)
		return true, nil
	default:
		return false, nil
	}
}

func (f *fakeAttachmentRepo) MarkImageDone(_ context.Context, imageID int64, caption string, updatedAt time.Time) error {
	image, ok := f.images[imageID]
	if !ok {
		return version.ErrAttachmentNotFound
	}
	image.Status = version.AttachmentImageDone
	image.Caption = caption
	image.Error = ""
	image.UpdatedAt = updatedAt
	f.images[imageID] = image
	f.imageStatusLog = append(f.imageStatusLog, idString(int64(image.Seq))+":"+image.Status)
	return nil
}

func (f *fakeAttachmentRepo) MarkImageFailed(_ context.Context, imageID int64, message string, updatedAt time.Time) error {
	image, ok := f.images[imageID]
	if !ok {
		return version.ErrAttachmentNotFound
	}
	image.Status = version.AttachmentImageFailed
	image.Error = message
	image.UpdatedAt = updatedAt
	f.images[imageID] = image
	f.imageStatusLog = append(f.imageStatusLog, idString(int64(image.Seq))+":"+image.Status)
	return nil
}

func (f *fakeAttachmentRepo) ResetImagesForReanalysis(_ context.Context, attachmentID int64, updatedAt time.Time) (int64, error) {
	var reset int64
	for id, image := range f.images {
		if image.AttachmentID != attachmentID {
			continue
		}
		if image.Status != version.AttachmentImageFailed && image.Status != version.AttachmentImageSkipped {
			continue
		}
		image.Status = version.AttachmentImageExtracted
		image.Caption = ""
		image.Error = ""
		image.UpdatedAt = updatedAt
		f.images[id] = image
		reset++
		f.imageStatusLog = append(f.imageStatusLog, idString(int64(image.Seq))+":"+image.Status)
	}
	return reset, nil
}

// fakeVisionCaptioner is the version-side port fake: the application never
// imports the LLM context, and the real adapter is exercised separately.
type fakeVisionCaptioner struct {
	available    bool
	availableErr error
	caption      string
	confidence   *float64
	err          error
	calls        int
	lastMIME     string
	lastData     []byte
}

func (f *fakeVisionCaptioner) Available(context.Context) (bool, error) {
	return f.available, f.availableErr
}

func (f *fakeVisionCaptioner) Caption(_ context.Context, mime string, data []byte) (versionapp.ImageCaption, error) {
	f.calls++
	f.lastMIME = mime
	f.lastData = append([]byte(nil), data...)
	if f.err != nil {
		return versionapp.ImageCaption{}, f.err
	}
	return versionapp.ImageCaption{Caption: f.caption, Confidence: f.confidence, Model: "fake-vision"}, nil
}

// fakeFactRecorder is the append-only version_fact port fake.
type fakeFactRecorder struct {
	facts []version.Fact
	err   error
}

func (f *fakeFactRecorder) Record(_ context.Context, fact version.Fact) (version.Fact, bool, error) {
	if f.err != nil {
		return version.Fact{}, false, f.err
	}
	fact.ID = int64(len(f.facts) + 1)
	f.facts = append(f.facts, fact)
	return fact, false, nil
}

// newParsingAttachmentEnv wires the attachment service to the real parser so
// the DOCX path is exercised end to end (parse -> image rows -> vision ->
// fact), with fakes only for the LLM port and the fact store.
func newParsingAttachmentEnv(t *testing.T) *attachmentEnv {
	t.Helper()
	env := &attachmentEnv{
		repo: newFakeAttachmentRepo(), store: newFakeContentStore(), queue: &fakeQueue{},
		events:    &fakePublisher{},
		vision:    &fakeVisionCaptioner{available: true, caption: "登录界面截图：手机号与验证码输入框"},
		facts:     &fakeFactRecorder{},
		versionID: 1,
	}
	env.svc = versionapp.NewAttachmentService(
		env.repo, env.store, env.queue, attachments.NewParser(),
		attachments.NewImageProber(), env.events, &seqIDs{}, fixedClock{at: testTime})
	env.svc.SetVisionCaptioner(env.vision)
	env.svc.SetFactRecorder(env.facts)
	return env
}

// docxWithImageFixture builds a minimal DOCX whose only content is one
// paragraph and one embedded PNG declared through word/_rels.
func docxWithImageFixture(t *testing.T) []byte {
	t.Helper()
	document := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
<w:body><w:p><w:r><w:t>登录界面示意</w:t></w:r><w:r><w:drawing><a:blip r:embed="rId10"/></w:drawing></w:r></w:p></w:body>
</w:document>`
	rels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId10" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/image1.png"/>
</Relationships>`
	entries := []struct {
		name string
		data []byte
	}{
		{"[Content_Types].xml", []byte(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`)},
		{"word/document.xml", []byte(document)},
		{"word/_rels/document.xml.rels", []byte(rels)},
		{"word/media/image1.png", pngImageBytes(8, 6)},
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		file, err := writer.Create(entry.name)
		if err != nil {
			t.Fatalf("create docx entry: %v", err)
		}
		if _, err := file.Write(entry.data); err != nil {
			t.Fatalf("write docx entry: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close docx fixture: %v", err)
	}
	return buffer.Bytes()
}

func fakeExtractedImage() versionapp.ExtractionImage {
	return versionapp.ExtractionImage{
		Seq: 1, Name: "image1.png", Source: "word/media/image1.png",
		MIME: "image/png", Width: 8, Height: 6, Data: pngImageBytes(8, 6),
	}
}

func eventTypes(events *fakePublisher) []string {
	types := make([]string, 0, len(events.events))
	for _, event := range events.events {
		types = append(types, event.Type)
	}
	return types
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestDocxImagesExtractedAndAnalyzed is the Task 19 acceptance test: a DOCX
// with an embedded image produces one attachment_image row that runs through
// vision, stores a caption plus its content reference, and appends the
// attachment_image_caption version fact.
func TestDocxImagesExtractedAndAnalyzed(t *testing.T) {
	env := newParsingAttachmentEnv(t)
	data := docxWithImageFixture(t)
	sha := strings.Repeat("7", 64)
	env.store.put(sha, data, docxMIME)
	view, err := env.svc.RegisterFromMessage(requestContext(), env.versionID, 6001, 7001,
		attachmentPart(sha, "with-image.docx", docxMIME, int64(len(data))), 42)
	if err != nil {
		t.Fatalf("RegisterFromMessage: %v", err)
	}

	result, err := env.svc.Extract(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Attachment.ExtractionStatus != version.AttachmentDone || result.ErrorCode != "" {
		t.Fatalf("extraction = %#v", result)
	}
	if len(result.Images) != 1 || result.Images[0].Width != 8 || result.Images[0].Height != 6 {
		t.Fatalf("parsed images = %#v", result.Images)
	}

	rows, err := env.svc.ListImages(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("image rows = %#v", rows)
	}
	row := rows[0]
	if row.Status != version.AttachmentImageDone || row.Caption == "" {
		t.Fatalf("image row = %#v", row)
	}
	if row.ContentRefID == 0 || row.SHA256 == "" || row.MIME != "image/png" ||
		row.Width != 8 || row.Height != 6 || row.SourceKind != version.AttachmentImageSourceEmbedded {
		t.Fatalf("image row metadata = %#v", row)
	}
	// The bytes live in the content ledger under an attachment_image reference,
	// never in the database row.
	foundRef := false
	for _, ref := range env.store.refs {
		if ref.Role == version.AttachmentImageRole && ref.OwnerID == view.ID && ref.ObjectID == row.SHA256 {
			foundRef = true
			if ref.ID != row.ContentRefID {
				t.Fatalf("content ref id = %d, want %d", ref.ID, row.ContentRefID)
			}
		}
	}
	if !foundRef {
		t.Fatalf("image content ref missing: %#v", env.store.refs)
	}
	if env.vision.calls != 1 || env.vision.lastMIME != "image/png" || len(env.vision.lastData) == 0 {
		t.Fatalf("vision calls = %d mime = %q bytes = %d", env.vision.calls, env.vision.lastMIME, len(env.vision.lastData))
	}

	if len(env.facts.facts) != 1 {
		t.Fatalf("facts = %#v", env.facts.facts)
	}
	fact := env.facts.facts[0]
	if fact.Kind != "attachment_image_caption" || fact.VersionID != env.versionID || fact.Source == "" {
		t.Fatalf("fact = %#v", fact)
	}
	if fact.Key == "" || fact.Payload["caption"] != row.Caption ||
		fact.Payload["attachment_id"] != idString(view.ID) || fact.Payload["image_id"] != idString(row.ID) {
		t.Fatalf("fact payload = %#v", fact.Payload)
	}
	t.Logf("attachment_image row: id=%d attachment_id=%d seq=%d status=%s mime=%s %dx%d source_kind=%s content_ref_id=%d sha256=%s",
		row.ID, row.AttachmentID, row.Seq, row.Status, row.MIME, row.Width, row.Height,
		row.SourceKind, row.ContentRefID, row.SHA256)
	t.Logf("caption: %s", row.Caption)
	t.Logf("version_fact: kind=%s key=%s caption=%v model=%v", fact.Kind, fact.Key, fact.Payload["caption"], fact.Payload["model"])

	types := eventTypes(env.events)
	for _, want := range []string{"attachment.image.extracted", "attachment.image.analyzed"} {
		if !containsString(types, want) {
			t.Fatalf("events = %#v, want %s", types, want)
		}
	}
}

// TestImageAnalysisFailureMarksAttachmentFailed pins R-E3-3: a vision failure
// fails the image row with its reason and the whole attachment, never a silent
// skip.
func TestImageAnalysisFailureMarksAttachmentFailed(t *testing.T) {
	env := newAttachmentEnv(t)
	env.vision.err = errors.New("vision upstream returned HTTP 500")
	sha := strings.Repeat("8", 64)
	view := registerAttachment(t, env, sha, "with-image.docx", docxMIME, []byte("docx"))
	env.extractor.result = versionapp.ExtractionResult{
		Text:   "登录界面示意",
		Images: []versionapp.ExtractionImage{fakeExtractedImage()},
		Coverage: versionapp.ExtractionCoverage{
			Found: 2, Parsed: 2, ImagesFound: 1, ImagesParsed: 1,
		},
	}

	result, err := env.svc.Extract(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Attachment.ExtractionStatus != version.AttachmentFailed ||
		result.ErrorCode != version.AttachmentErrorVisionAnalysis {
		t.Fatalf("result = %#v", result)
	}
	stored := env.repo.attachments[view.ID]
	if stored.ExtractionStatus != version.AttachmentFailed ||
		stored.ExtractionErrorCode != version.AttachmentErrorVisionAnalysis ||
		!strings.Contains(stored.ExtractionErrorText, "vision upstream returned HTTP 500") {
		t.Fatalf("stored attachment = %#v", stored)
	}
	rows, err := env.svc.ListImages(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if len(rows) != 1 || rows[0].Status != version.AttachmentImageFailed ||
		!strings.Contains(rows[0].Error, "vision upstream returned HTTP 500") {
		t.Fatalf("image rows = %#v", rows)
	}
	if len(env.facts.facts) != 0 {
		t.Fatalf("failed image wrote a fact: %#v", env.facts.facts)
	}
	types := eventTypes(env.events)
	for _, want := range []string{"attachment.image.failed", "requirement.attachment_failed"} {
		if !containsString(types, want) {
			t.Fatalf("events = %#v, want %s", types, want)
		}
	}
}

// TestNoVisionModelRejectsImageDoc covers R-E2-2/R-E2-3: with no vision model
// a document containing images fails explicitly (rows skipped + attachment
// failed, model.vision.unavailable published) and the send path rejects it
// with the typed ErrVisionRequired. A non-image document is unaffected.
func TestNoVisionModelRejectsImageDoc(t *testing.T) {
	env := newAttachmentEnv(t)
	env.vision.available = false
	sha := strings.Repeat("9", 64)
	view := registerAttachment(t, env, sha, "with-image.docx", docxMIME, []byte("docx"))
	env.extractor.result = versionapp.ExtractionResult{
		Text: "登录界面示意", Images: []versionapp.ExtractionImage{fakeExtractedImage()},
		Coverage: versionapp.ExtractionCoverage{Found: 2, Parsed: 2, ImagesFound: 1, ImagesParsed: 1},
	}
	if _, err := env.svc.Extract(requestContext(), view.ID); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	stored := env.repo.attachments[view.ID]
	if stored.ExtractionStatus != version.AttachmentFailed ||
		stored.ExtractionErrorCode != version.AttachmentErrorVisionRequired {
		t.Fatalf("stored = %#v", stored)
	}
	rows, err := env.svc.ListImages(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if len(rows) != 1 || rows[0].Status != version.AttachmentImageSkipped || rows[0].Error == "" {
		t.Fatalf("image rows = %#v", rows)
	}
	if !containsString(eventTypes(env.events), "model.vision.unavailable") {
		t.Fatalf("events = %#v, want model.vision.unavailable", eventTypes(env.events))
	}

	part := attachmentPart(sha, "with-image.docx", docxMIME, int64(len(env.store.data[sha])))
	_, _, err = env.svc.PrepareUserMessageParts(requestContext(), env.versionID, 6001,
		[]version.MessagePart{{Type: version.MessagePartText, Text: "看下这个文档"}, part}, 42)
	if !errors.Is(err, version.ErrVisionRequired) {
		t.Fatalf("send err = %v, want ErrVisionRequired", err)
	}
	apiErr := apiError(t, err)
	if apiErr.Status != http.StatusBadRequest || apiErr.Code != version.AttachmentErrorVisionRequired {
		t.Fatalf("error = %#v, want typed 400 vision_required", apiErr)
	}

	// A document without images keeps working without any vision model.
	plain := newAttachmentEnv(t)
	plain.vision.available = false
	mdSHA := strings.Repeat("a", 64)
	md := registerAttachment(t, plain, mdSHA, "spec.md", "text/markdown", []byte("# Spec"))
	plain.extractor.text = "需求正文"
	if result, err := plain.svc.Extract(requestContext(), md.ID); err != nil || result.Attachment.ExtractionStatus != version.AttachmentDone {
		t.Fatalf("plain extraction = %#v, %v", result, err)
	}
	_, _, err = plain.svc.PrepareUserMessageParts(requestContext(), plain.versionID, 6001,
		[]version.MessagePart{{
			Type: version.MessagePartText, Text: "看下这个文档",
		}, attachmentPart(mdSHA, "spec.md", "text/markdown", int64(len("# Spec")))}, 42)
	if err != nil {
		t.Fatalf("plain document send err = %v", err)
	}
}

// TestReanalyzeIdempotent covers R-E3-6: reanalyze re-queues skipped/failed
// image rows, re-runs vision and finishes the attachment, while a second call
// adds neither rows nor content objects.
func TestReanalyzeIdempotent(t *testing.T) {
	env := newAttachmentEnv(t)
	env.vision.available = false
	sha := strings.Repeat("b", 64)
	view := registerAttachment(t, env, sha, "with-image.docx", docxMIME, []byte("docx"))
	env.extractor.result = versionapp.ExtractionResult{
		Text: "登录界面示意", Images: []versionapp.ExtractionImage{fakeExtractedImage()},
		Coverage: versionapp.ExtractionCoverage{Found: 2, Parsed: 2, ImagesFound: 1, ImagesParsed: 1},
	}
	if _, err := env.svc.Extract(requestContext(), view.ID); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	refsBefore := len(env.store.refs)
	uploadsBefore := len(env.store.uploads)
	rowsBefore := len(env.repo.images)

	env.vision.available = true
	env.vision.caption = "登录界面：手机号与验证码"
	rows, err := env.svc.Reanalyze(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("Reanalyze: %v", err)
	}
	if len(rows) != 1 || rows[0].Status != version.AttachmentImageDone || rows[0].Caption == "" {
		t.Fatalf("rows = %#v", rows)
	}
	if stored := env.repo.attachments[view.ID]; stored.ExtractionStatus != version.AttachmentDone {
		t.Fatalf("attachment after reanalyze = %#v", stored)
	}
	if len(env.store.refs) != refsBefore || len(env.store.uploads) != uploadsBefore || len(env.repo.images) != rowsBefore {
		t.Fatalf("reanalyze duplicated storage: refs %d->%d uploads %d->%d rows %d->%d",
			refsBefore, len(env.store.refs), uploadsBefore, len(env.store.uploads), rowsBefore, len(env.repo.images))
	}
	log := strings.Join(env.repo.imageStatusLog, ",")
	if !strings.Contains(log, "1:"+version.AttachmentImageAnalyzing) || !strings.Contains(log, "1:"+version.AttachmentImageDone) {
		t.Fatalf("status transitions = %q", log)
	}
	calls := env.vision.calls

	again, err := env.svc.Reanalyze(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("second Reanalyze: %v", err)
	}
	if len(again) != 1 || again[0].Status != version.AttachmentImageDone {
		t.Fatalf("second rows = %#v", again)
	}
	if env.vision.calls != calls || len(env.store.refs) != refsBefore || len(env.repo.images) != rowsBefore {
		t.Fatalf("idempotent reanalyze repeated side effects: calls %d->%d", calls, env.vision.calls)
	}
}
