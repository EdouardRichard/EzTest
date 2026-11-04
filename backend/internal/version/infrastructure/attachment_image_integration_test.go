//go:build integration

package versionpg_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
)

// TestAttachmentImageRepositoryLifecycle pins the attachment_image persistence
// contract (R-E3-2): unique (attachment_id, seq) inserts are idempotent, status
// transitions are CAS-like, reanalysis resets only failed/skipped rows, the
// extracted-text reference survives a failed analysis, and rows cascade with
// their attachment.
func TestAttachmentImageRepositoryLifecycle(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.NewAttachmentRepository(pool)
	ctx := context.Background()

	attachment := sampleAttachment(ids, 9001)
	if err := repo.Create(ctx, attachment); err != nil {
		t.Fatalf("Create attachment: %v", err)
	}
	// Image analysis may fail the attachment: the extracted-text reference is
	// stored before analysis and must survive the failure for a reanalyze.
	if err := repo.SetExtractedContentRef(ctx, attachment.ID, 33, testTime.Add(time.Minute)); err != nil {
		t.Fatalf("SetExtractedContentRef: %v", err)
	}
	stored, err := repo.Get(ctx, attachment.ID)
	if err != nil || stored.ExtractedContentRefID == nil || *stored.ExtractedContentRefID != 33 {
		t.Fatalf("extracted ref = %#v, %v", stored.ExtractedContentRefID, err)
	}
	if stored.ExtractionStatus != version.AttachmentPending {
		t.Fatalf("SetExtractedContentRef changed status to %q", stored.ExtractionStatus)
	}

	first := version.AttachmentImageRecord{
		ID: ids.Next(), AttachmentID: attachment.ID, Seq: 1, ContentRefID: 101,
		MIME: "image/png", Width: 8, Height: 6,
		SourceKind: version.AttachmentImageSourceEmbedded,
		Status:     version.AttachmentImageSkipped, Error: "no vision model",
		CreatedAt: testTime, UpdatedAt: testTime,
	}
	if err := repo.CreateImage(ctx, first); err != nil {
		t.Fatalf("CreateImage: %v", err)
	}
	// Unique (attachment_id, seq): a retried extraction with a new ID is a no-op.
	duplicate := first
	duplicate.ID = ids.Next()
	duplicate.Status = version.AttachmentImageExtracted
	duplicate.Width = 0
	duplicate.Height = 0
	if err := repo.CreateImage(ctx, duplicate); err != nil {
		t.Fatalf("duplicate CreateImage: %v", err)
	}
	second := version.AttachmentImageRecord{
		ID: ids.Next(), AttachmentID: attachment.ID, Seq: 2, ContentRefID: 102,
		MIME: "image/jpeg", SourceKind: version.AttachmentImageSourceEmbedded,
		Status: version.AttachmentImageExtracted, CreatedAt: testTime, UpdatedAt: testTime,
	}
	if err := repo.CreateImage(ctx, second); err != nil {
		t.Fatalf("CreateImage second: %v", err)
	}

	images, err := repo.ListImages(ctx, attachment.ID)
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if len(images) != 2 || images[0].Seq != 1 || images[1].Seq != 2 {
		t.Fatalf("images = %#v", images)
	}
	if images[0].ID != first.ID || images[0].Status != version.AttachmentImageSkipped ||
		images[0].Error != "no vision model" || images[0].Width != 8 || images[0].Height != 6 {
		t.Fatalf("first image = %#v", images[0])
	}
	if images[1].Width != 0 || images[1].Height != 0 {
		t.Fatalf("null dimensions = %#v", images[1])
	}

	// skipped -> analyzing -> done with a caption.
	changed, err := repo.MarkImageAnalyzing(ctx, first.ID, testTime.Add(2*time.Minute))
	if err != nil || !changed {
		t.Fatalf("MarkImageAnalyzing = %v/%v", changed, err)
	}
	changed, err = repo.MarkImageAnalyzing(ctx, first.ID, testTime.Add(2*time.Minute))
	if err != nil || changed {
		t.Fatalf("second MarkImageAnalyzing = %v/%v", changed, err)
	}
	if err := repo.MarkImageDone(ctx, first.ID, "登录界面截图：手机号与验证码输入框", testTime.Add(3*time.Minute)); err != nil {
		t.Fatalf("MarkImageDone: %v", err)
	}
	images, _ = repo.ListImages(ctx, attachment.ID)
	if images[0].Status != version.AttachmentImageDone || images[0].Caption != "登录界面截图：手机号与验证码输入框" || images[0].Error != "" {
		t.Fatalf("done image = %#v", images[0])
	}
	if changed, err := repo.MarkImageAnalyzing(ctx, first.ID, testTime.Add(3*time.Minute)); err != nil || changed {
		t.Fatalf("MarkImageAnalyzing(done) = %v/%v", changed, err)
	}

	// A failed row is reset by reanalyze; a done row is untouched.
	if err := repo.MarkImageFailed(ctx, second.ID, "vision upstream returned HTTP 500", testTime.Add(4*time.Minute)); err != nil {
		t.Fatalf("MarkImageFailed: %v", err)
	}
	reset, err := repo.ResetImagesForReanalysis(ctx, attachment.ID, testTime.Add(5*time.Minute))
	if err != nil || reset != 1 {
		t.Fatalf("ResetImagesForReanalysis = %d/%v", reset, err)
	}
	images, _ = repo.ListImages(ctx, attachment.ID)
	if images[0].Status != version.AttachmentImageDone || images[0].Caption == "" {
		t.Fatalf("reset touched a done image: %#v", images[0])
	}
	if images[1].Status != version.AttachmentImageExtracted || images[1].Error != "" {
		t.Fatalf("reset failed image = %#v", images[1])
	}
	for _, image := range images {
		t.Logf("attachment_image DB row: id=%d attachment_id=%d seq=%d status=%s mime=%s width=%d height=%d source_kind=%s content_ref_id=%d caption=%q error=%q",
			image.ID, image.AttachmentID, image.Seq, image.Status, image.MIME, image.Width, image.Height,
			image.SourceKind, image.ContentRefID, image.Caption, image.Error)
	}

	for _, call := range []func() error{
		func() error { _, err := repo.MarkImageAnalyzing(ctx, 999999, testTime); return err },
		func() error { return repo.MarkImageDone(ctx, 999999, "x", testTime) },
		func() error { return repo.MarkImageFailed(ctx, 999999, "x", testTime) },
	} {
		if err := call(); !errors.Is(err, version.ErrAttachmentNotFound) {
			t.Fatalf("missing image error = %v", err)
		}
	}

	// attachment_image cascades with its attachment.
	if _, err := pool.Exec(ctx, `DELETE FROM requirement_attachment WHERE id = $1`, attachment.ID); err != nil {
		t.Fatalf("delete attachment: %v", err)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM attachment_image WHERE attachment_id = $1`, attachment.ID).Scan(&remaining); err != nil {
		t.Fatalf("count images: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("images after cascade = %d", remaining)
	}
}
