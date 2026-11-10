//go:build integration

package versionpg_test

import (
	"context"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
)

// TestAttachmentVersionListQueries pins the C-05 repository reads against
// PostgreSQL: the version existence probe used to 404 an unknown version, and
// the single grouped image-count query (one row per attachment, all analysis
// states, other versions excluded).
func TestAttachmentVersionListQueries(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.NewAttachmentRepository(pool)
	ctx := context.Background()

	const versionID, foreignVersionID int64 = 9001, 9002
	storedVersion := sampleVersion(ids, 42)
	storedVersion.ID = versionID
	if err := versionpg.New(pool, ids).Create(ctx, storedVersion, nil); err != nil {
		t.Fatalf("create version: %v", err)
	}

	if exists, err := repo.VersionExists(ctx, versionID); err != nil || !exists {
		t.Fatalf("VersionExists(existing) = %v/%v", exists, err)
	}
	if exists, err := repo.VersionExists(ctx, 999999); err != nil || exists {
		t.Fatalf("VersionExists(unknown) = %v/%v", exists, err)
	}

	withImages := sampleAttachment(ids, versionID)
	withoutImages := sampleAttachment(ids, versionID)
	foreign := sampleAttachment(ids, foreignVersionID)
	for _, attachment := range []version.Attachment{withImages, withoutImages, foreign} {
		if err := repo.Create(ctx, attachment); err != nil {
			t.Fatalf("create attachment %d: %v", attachment.ID, err)
		}
	}

	for seq := 1; seq <= 3; seq++ {
		image := version.AttachmentImageRecord{
			ID: ids.Next(), AttachmentID: withImages.ID, Seq: seq, ContentRefID: ids.Next(),
			MIME: "image/png", SourceKind: version.AttachmentImageSourceEmbedded,
			Status: version.AttachmentImageExtracted, CreatedAt: testTime, UpdatedAt: testTime,
		}
		if err := repo.CreateImage(ctx, image); err != nil {
			t.Fatalf("create image seq %d: %v", seq, err)
		}
	}
	if err := repo.CreateImage(ctx, version.AttachmentImageRecord{
		ID: ids.Next(), AttachmentID: foreign.ID, Seq: 1, ContentRefID: ids.Next(),
		MIME: "image/png", SourceKind: version.AttachmentImageSourceEmbedded,
		Status: version.AttachmentImageExtracted, CreatedAt: testTime, UpdatedAt: testTime,
	}); err != nil {
		t.Fatalf("create foreign image: %v", err)
	}

	counts, err := repo.CountImagesByVersion(ctx, versionID)
	if err != nil {
		t.Fatalf("CountImagesByVersion: %v", err)
	}
	if len(counts) != 1 || counts[withImages.ID] != 3 {
		t.Fatalf("counts = %#v, want only attachment %d with 3 images", counts, withImages.ID)
	}
	if _, ok := counts[withoutImages.ID]; ok {
		t.Fatalf("attachments without images must be absent from counts: %#v", counts)
	}
	if _, ok := counts[foreign.ID]; ok {
		t.Fatalf("another version's images leaked into counts: %#v", counts)
	}
}
