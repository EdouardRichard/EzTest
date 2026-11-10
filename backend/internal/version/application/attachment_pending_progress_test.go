package versionapp

// C-04 409 attachment_pending details: the stage/percent/ETA computation is a
// pure function of the attachment row and its image rows, so every stage is
// directly testable (analysis-only stages are hard to reach through the message
// post race).

import (
	"testing"
	"time"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func TestAttachmentPendingProgressStages(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

	queued := attachmentPendingDetails(version.Attachment{
		ID: 1, ExtractionStatus: version.AttachmentPending,
	}, nil, now)
	if queued["stage"] != "queued" || queued["images_total"] != 0 {
		t.Fatalf("queued details = %#v", queued)
	}
	if _, ok := queued["percent"]; ok {
		t.Fatalf("queued details reported a percent: %#v", queued)
	}
	if queued["retry_after_seconds"] != 2 {
		t.Fatalf("queued retry_after_seconds = %#v", queued["retry_after_seconds"])
	}

	parsing := attachmentPendingDetails(version.Attachment{
		ID: 1, ExtractionStatus: version.AttachmentProcessing,
	}, nil, now)
	if parsing["stage"] != "parsing" {
		t.Fatalf("parsing stage = %#v, want parsing before image rows exist", parsing["stage"])
	}
	if _, ok := parsing["eta_seconds"]; ok {
		t.Fatalf("parsing details reported an ETA: %#v", parsing)
	}

	images := make([]version.AttachmentImageRecord, 0, 12)
	for seq := 1; seq <= 12; seq++ {
		image := version.AttachmentImageRecord{
			ID: int64(seq), AttachmentID: 1, Seq: seq,
			Status:    version.AttachmentImageExtracted,
			CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute),
		}
		switch seq {
		case 1, 2, 3, 4:
			image.Status = version.AttachmentImageDone
			image.UpdatedAt = now
		case 5:
			image.Status = version.AttachmentImageFailed
			image.Error = "model timeout"
			image.UpdatedAt = now
		case 6:
			image.Status = version.AttachmentImageSkipped
			image.UpdatedAt = now
		}
		images = append(images, image)
	}
	analyzing := attachmentPendingDetails(version.Attachment{
		ID: 1, ExtractionStatus: version.AttachmentProcessing,
	}, images, now)
	if analyzing["stage"] != "analyzing_images" {
		t.Fatalf("stage = %#v", analyzing["stage"])
	}
	if analyzing["images_total"] != 12 || analyzing["images_done"] != 4 ||
		analyzing["images_failed"] != 1 || analyzing["images_skipped"] != 1 {
		t.Fatalf("image counters = %#v", analyzing)
	}
	if analyzing["percent"] != 50 {
		t.Fatalf("percent = %#v, want 50 for 6/12 terminal images", analyzing["percent"])
	}
	if eta, ok := analyzing["eta_seconds"].(int); !ok || eta <= 0 {
		t.Fatalf("eta_seconds = %#v, want a positive ETA", analyzing["eta_seconds"])
	}

	failed := attachmentPendingDetails(version.Attachment{
		ID: 1, ExtractionStatus: version.AttachmentFailed,
		ExtractionErrorCode: version.AttachmentErrorVisionAnalysis,
		ExtractionErrorText: "image analysis failed",
	}, nil, now)
	if failed["stage"] != "failed" || failed["error"] != "image analysis failed" {
		t.Fatalf("failed details = %#v", failed)
	}

	done := attachmentPendingDetails(version.Attachment{
		ID: 1, ExtractionStatus: version.AttachmentDone,
	}, images, now)
	if done["stage"] != "done" {
		t.Fatalf("done stage = %#v", done["stage"])
	}
}

// TestAttachmentPendingETADecaysFromHeartbeat pins the polling behavior: the
// ETA is the remaining analysis estimate minus the elapsed time since the last
// parent heartbeat, so repeated 409 polls see it shrink instead of staying
// frozen (floored at 1 second). percent counts terminal rows including
// failed/skipped because those images are no longer pending work.
func TestAttachmentPendingETADecaysFromHeartbeat(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	images := []version.AttachmentImageRecord{
		{ID: 1, AttachmentID: 1, Seq: 1, Status: version.AttachmentImageDone,
			CreatedAt: now.Add(-10 * time.Minute), UpdatedAt: now.Add(-5 * time.Minute)},
		{ID: 2, AttachmentID: 1, Seq: 2, Status: version.AttachmentImageDone,
			CreatedAt: now.Add(-10 * time.Minute), UpdatedAt: now.Add(-5 * time.Minute)},
		{ID: 3, AttachmentID: 1, Seq: 3, Status: version.AttachmentImageExtracted,
			CreatedAt: now.Add(-10 * time.Minute), UpdatedAt: now.Add(-10 * time.Minute)},
		{ID: 4, AttachmentID: 1, Seq: 4, Status: version.AttachmentImageExtracted,
			CreatedAt: now.Add(-10 * time.Minute), UpdatedAt: now.Add(-10 * time.Minute)},
	}
	details := attachmentPendingDetails(version.Attachment{
		ID: 1, ExtractionStatus: version.AttachmentProcessing,
		UpdatedAt: now.Add(-2 * time.Minute),
	}, images, now)
	if details["percent"] != 50 {
		t.Fatalf("percent = %#v, want 50 for 2/4 terminal images", details["percent"])
	}
	// 2 remaining x 5m average - 2m elapsed = 8m.
	if eta := details["eta_seconds"]; eta != 480 {
		t.Fatalf("eta_seconds = %#v, want 480 after the heartbeat decay", eta)
	}
	staleHeartbeat := attachmentPendingDetails(version.Attachment{
		ID: 1, ExtractionStatus: version.AttachmentProcessing,
		UpdatedAt: now.Add(-time.Hour),
	}, images, now)
	if eta := staleHeartbeat["eta_seconds"]; eta != 1 {
		t.Fatalf("eta_seconds = %#v, want the 1 second floor", eta)
	}
}
