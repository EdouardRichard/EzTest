package versionapp_test

import (
	"context"
	"errors"
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
