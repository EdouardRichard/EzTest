package versionapp_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// fakeVisionLimits is the minimal settings port used by the W1-2 vision-limit
// wiring tests.
type fakeVisionLimits struct {
	ints map[string]int64
	err  error
}

func (f *fakeVisionLimits) Int(_ context.Context, key string) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	value, ok := f.ints[key]
	if !ok {
		return 0, errors.New("setting not found")
	}
	return value, nil
}

// TestVisionMaxImagesPerAttachmentSkipsExcess pins the W1-2 wiring of
// vision.max_images_per_attachment: only the first N embedded images are
// extracted/analyzed, the excess images become explicit skipped rows that
// carry the limit as their reason (never silently dropped).
func TestVisionMaxImagesPerAttachmentSkipsExcess(t *testing.T) {
	env := newAttachmentEnv(t)
	env.svc.SetLimits(&fakeVisionLimits{ints: map[string]int64{
		"vision.max_images_per_attachment": 1,
		"vision.max_image_bytes":           1 << 20,
	}})
	view := registerAttachment(t, env, strings.Repeat("d", 64), "with-images.docx", docxMIME, []byte("docx"))
	env.extractor.result = twoImageExtractionResult()

	if _, err := env.svc.Extract(requestContext(), view.ID); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	rows, err := env.svc.ListImages(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("image rows = %d, want 2 (one extracted + one skipped)", len(rows))
	}
	if rows[0].Status != version.AttachmentImageDone {
		t.Fatalf("first image status = %q, want done after analysis", rows[0].Status)
	}
	if rows[1].Status != version.AttachmentImageSkipped {
		t.Fatalf("excess image status = %q, want skipped", rows[1].Status)
	}
	if !strings.Contains(rows[1].Error, "vision.max_images_per_attachment") {
		t.Fatalf("excess image reason = %q, want it to name vision.max_images_per_attachment", rows[1].Error)
	}
}

// TestVisionMaxImageBytesSkipsOversized pins the W1-2 wiring of
// vision.max_image_bytes: an image above the configured single-image cap is
// skipped with the limit and its size in the reason instead of being sent to
// the vision model.
func TestVisionMaxImageBytesSkipsOversized(t *testing.T) {
	env := newAttachmentEnv(t)
	env.svc.SetLimits(&fakeVisionLimits{ints: map[string]int64{
		"vision.max_images_per_attachment": 50,
		"vision.max_image_bytes":           10,
	}})
	image := fakeExtractedImage()
	image.Data = make([]byte, 11)
	view := registerAttachment(t, env, strings.Repeat("e", 64), "with-image.docx", docxMIME, []byte("docx"))
	env.extractor.result = versionapp.ExtractionResult{
		Text:     "登录界面示意",
		Images:   []versionapp.ExtractionImage{image},
		Coverage: versionapp.ExtractionCoverage{Found: 1, Parsed: 1, ImagesFound: 1, ImagesParsed: 1},
	}

	if _, err := env.svc.Extract(requestContext(), view.ID); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	rows, err := env.svc.ListImages(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("image rows = %d, want 1", len(rows))
	}
	if rows[0].Status != version.AttachmentImageSkipped {
		t.Fatalf("oversized image status = %q, want skipped", rows[0].Status)
	}
	if !strings.Contains(rows[0].Error, "vision.max_image_bytes") {
		t.Fatalf("oversized image reason = %q, want it to name vision.max_image_bytes", rows[0].Error)
	}
	if _, analyzed := payloadOf(env.events, "attachment.image.analyzed"); analyzed {
		t.Fatal("oversized image must not be analyzed by the vision model")
	}
}

// threeImageExtractionResult builds a parser outcome with three embedded images
// so a reanalyze can be checked against a per-attachment image cap.
func threeImageExtractionResult() versionapp.ExtractionResult {
	images := make([]versionapp.ExtractionImage, 0, 3)
	for seq := 1; seq <= 3; seq++ {
		image := fakeExtractedImage()
		image.Seq = seq
		image.Name = fmt.Sprintf("image%d.png", seq)
		image.Source = fmt.Sprintf("word/media/image%d.png", seq)
		images = append(images, image)
	}
	return versionapp.ExtractionResult{
		Text:   "三张图片",
		Images: images,
		Coverage: versionapp.ExtractionCoverage{
			Found: 4, Parsed: 4, ImagesFound: 3, ImagesParsed: 3,
		},
	}
}

// noVisionSkippedAttachment extracts one image-bearing document while no
// vision model is configured, leaving every image row skipped (the state a
// later reanalyze has to re-apply the caps to).
func noVisionSkippedAttachment(t *testing.T, env *attachmentEnv, sha, name string, result versionapp.ExtractionResult) versionapp.AttachmentView {
	t.Helper()
	env.vision.available = false
	view := registerAttachment(t, env, sha, name, docxMIME, []byte("docx"))
	env.extractor.result = result
	extracted, err := env.svc.Extract(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if extracted.Attachment.ExtractionStatus != version.AttachmentFailed {
		t.Fatalf("extraction = %#v, want the no-vision failure", extracted)
	}
	return view
}

// TestReanalyzeReappliesMaxImagesCap covers the cap bypass on the reanalyze
// path: after a no-vision extraction every row is skipped, and reanalyze resets
// them all to extracted. The configured vision.max_images_per_attachment must
// still be applied, so only the first N images by seq are sent to the model and
// the excess rows are written back as skipped with the limit as their reason.
func TestReanalyzeReappliesMaxImagesCap(t *testing.T) {
	env := newAttachmentEnv(t)
	view := noVisionSkippedAttachment(t, env, strings.Repeat("1", 64), "three-images.docx", threeImageExtractionResult())
	env.svc.SetLimits(&fakeVisionLimits{ints: map[string]int64{
		"vision.max_images_per_attachment": 2,
		"vision.max_image_bytes":           1 << 20,
	}})

	env.vision.available = true
	env.vision.caption = "重新分析后的截图说明"
	rows, err := env.svc.Reanalyze(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("Reanalyze: %v", err)
	}
	if env.vision.calls != 2 {
		t.Fatalf("vision calls = %d, want 2 (cap is 2)", env.vision.calls)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	if rows[0].Status != version.AttachmentImageDone || rows[1].Status != version.AttachmentImageDone {
		t.Fatalf("first two images = %#v, want done", rows[:2])
	}
	if rows[2].Status != version.AttachmentImageSkipped {
		t.Fatalf("third image status = %q, want it still skipped", rows[2].Status)
	}
	if !strings.Contains(rows[2].Error, "vision.max_images_per_attachment") {
		t.Fatalf("third image reason = %q, want it to name vision.max_images_per_attachment", rows[2].Error)
	}
	if stored := env.repo.attachments[view.ID]; stored.ExtractionStatus != version.AttachmentDone {
		t.Fatalf("attachment = %#v, want done after the capped reanalyze", stored)
	}
}

// TestReanalyzeReappliesMaxImageBytesCap covers the same bypass for
// vision.max_image_bytes: an image above the configured single-image cap is
// written back as skipped with the key and its actual size instead of being sent
// to the vision model.
func TestReanalyzeReappliesMaxImageBytesCap(t *testing.T) {
	env := newAttachmentEnv(t)
	image := fakeExtractedImage()
	size := len(image.Data)
	view := noVisionSkippedAttachment(t, env, strings.Repeat("2", 64), "with-image.docx", versionapp.ExtractionResult{
		Text:     "登录界面示意",
		Images:   []versionapp.ExtractionImage{image},
		Coverage: versionapp.ExtractionCoverage{Found: 2, Parsed: 2, ImagesFound: 1, ImagesParsed: 1},
	})
	env.svc.SetLimits(&fakeVisionLimits{ints: map[string]int64{
		"vision.max_images_per_attachment": 50,
		"vision.max_image_bytes":           10,
	}})

	env.vision.available = true
	rows, err := env.svc.Reanalyze(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("Reanalyze: %v", err)
	}
	if env.vision.calls != 0 {
		t.Fatalf("vision calls = %d, want the oversized image never analyzed", env.vision.calls)
	}
	if len(rows) != 1 || rows[0].Status != version.AttachmentImageSkipped {
		t.Fatalf("rows = %#v, want the oversized image skipped", rows)
	}
	if !strings.Contains(rows[0].Error, "vision.max_image_bytes") {
		t.Fatalf("reason = %q, want it to name vision.max_image_bytes", rows[0].Error)
	}
	if !strings.Contains(rows[0].Error, fmt.Sprintf("%d bytes", size)) {
		t.Fatalf("reason = %q, want it to carry the actual %d-byte size", rows[0].Error, size)
	}
}
