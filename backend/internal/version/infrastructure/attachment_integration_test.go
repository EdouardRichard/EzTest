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

func sampleAttachment(ids interface{ Next() int64 }, versionID int64) version.Attachment {
	attachmentID := ids.Next()
	messageID := int64(7001)
	creator := int64(42)
	return version.Attachment{
		ID: attachmentID, VersionID: versionID, ConversationMessageID: &messageID,
		ContentRefID: 11, Name: "spec.md", MIME: "text/markdown", Size: 128,
		ExtractionStatus: version.AttachmentPending, CreatedBy: &creator,
		CreatedAt: testTime, UpdatedAt: testTime,
	}
}

func TestAttachmentRepositoryLifecycle(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.NewAttachmentRepository(pool)
	ctx := context.Background()

	// requirement_attachment has no cross-context FK, so a version row is not
	// required for the runtime row itself.
	attachment := sampleAttachment(ids, 9001)
	if err := repo.Create(ctx, attachment); err != nil {
		t.Fatalf("Create: %v", err)
	}
	stored, err := repo.Get(ctx, attachment.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Name != "spec.md" || stored.ContentRefID != 11 || stored.ExtractionStatus != version.AttachmentPending {
		t.Fatalf("stored = %#v", stored)
	}
	if stored.ConversationMessageID == nil || *stored.ConversationMessageID != 7001 {
		t.Fatalf("message id = %#v", stored.ConversationMessageID)
	}
	if _, err := repo.GetInVersion(ctx, 9002, attachment.ID); !errors.Is(err, version.ErrAttachmentNotFound) {
		t.Fatalf("cross-version Get error = %v", err)
	}

	// pending -> processing is idempotent.
	changed, err := repo.MarkProcessing(ctx, attachment.ID, testTime.Add(time.Minute))
	if err != nil || !changed {
		t.Fatalf("MarkProcessing = %v/%v", changed, err)
	}
	changed, err = repo.MarkProcessing(ctx, attachment.ID, testTime.Add(2*time.Minute))
	if err != nil || changed {
		t.Fatalf("second MarkProcessing = %v/%v", changed, err)
	}
	stored, _ = repo.Get(ctx, attachment.ID)
	if stored.ExtractionStatus != version.AttachmentProcessing {
		t.Fatalf("status = %q", stored.ExtractionStatus)
	}

	// A parse failure stores the stable code and message.
	if err := repo.MarkFailed(ctx, attachment.ID, version.AttachmentErrorExtraction, "attachment text extraction failed", testTime.Add(3*time.Minute)); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	stored, _ = repo.Get(ctx, attachment.ID)
	if stored.ExtractionStatus != version.AttachmentFailed ||
		stored.ExtractionErrorCode != version.AttachmentErrorExtraction ||
		stored.ExtractionErrorText != "attachment text extraction failed" {
		t.Fatalf("failed attachment = %#v", stored)
	}

	// MarkDone stores the extracted text reference and clears the error.
	second := sampleAttachment(ids, 9001)
	if err := repo.Create(ctx, second); err != nil {
		t.Fatalf("Create second: %v", err)
	}
	if err := repo.MarkDone(ctx, second.ID, 22, testTime.Add(4*time.Minute)); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}
	stored, _ = repo.Get(ctx, second.ID)
	if stored.ExtractionStatus != version.AttachmentDone || stored.ExtractedContentRefID == nil || *stored.ExtractedContentRefID != 22 {
		t.Fatalf("done attachment = %#v", stored)
	}
	if stored.ExtractionErrorCode != "" {
		t.Fatalf("done attachment kept an error: %#v", stored)
	}
	// Repeated done transitions are idempotent and keep the first ref.
	if err := repo.MarkDone(ctx, second.ID, 99, testTime.Add(5*time.Minute)); err != nil {
		t.Fatalf("second MarkDone: %v", err)
	}
	stored, _ = repo.Get(ctx, second.ID)
	if stored.ExtractedContentRefID == nil || *stored.ExtractedContentRefID != 22 {
		t.Fatalf("extracted ref = %#v", stored.ExtractedContentRefID)
	}

	for _, call := range []func() error{
		func() error { _, err := repo.Get(ctx, 999999); return err },
		func() error { _, err := repo.MarkProcessing(ctx, 999999, testTime); return err },
		func() error { return repo.MarkDone(ctx, 999999, 1, testTime) },
		func() error { return repo.MarkFailed(ctx, 999999, "x", "y", testTime) },
	} {
		if err := call(); !errors.Is(err, version.ErrAttachmentNotFound) {
			t.Fatalf("missing attachment error = %v", err)
		}
	}
}

func TestAttachmentRepositoryListsByConversation(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.NewAttachmentRepository(pool)
	versionRepo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 1)
	if err := versionRepo.Create(ctx, v, sampleBindings(ids, v.ID)); err != nil {
		t.Fatalf("create version: %v", err)
	}
	conversationID := ids.Next()
	messageID := ids.Next()
	if _, err := pool.Exec(ctx, `INSERT INTO conversation (id, version_id, kind, title) VALUES ($1, $2, 'clarification', 'Chat')`, conversationID, v.ID); err != nil {
		t.Fatalf("insert conversation: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO conversation_message (id, conversation_id, role) VALUES ($1, $2, 'user')`, messageID, conversationID); err != nil {
		t.Fatalf("insert message: %v", err)
	}

	attachment := sampleAttachment(ids, v.ID)
	attachment.ConversationMessageID = &messageID
	if err := repo.Create(ctx, attachment); err != nil {
		t.Fatalf("Create: %v", err)
	}

	attachments, err := repo.ListByConversation(ctx, v.ID, conversationID)
	if err != nil {
		t.Fatalf("ListByConversation: %v", err)
	}
	if len(attachments) != 1 || attachments[0].ID != attachment.ID {
		t.Fatalf("attachments = %#v", attachments)
	}
	if exists, err := repo.ConversationExists(ctx, v.ID, conversationID); err != nil || !exists {
		t.Fatalf("ConversationExists = %v/%v", exists, err)
	}
	if exists, err := repo.ConversationExists(ctx, v.ID+1, conversationID); err != nil || exists {
		t.Fatalf("cross-version ConversationExists = %v/%v", exists, err)
	}
	if crossVersion, err := repo.ListByConversation(ctx, v.ID+1, conversationID); err != nil || len(crossVersion) != 0 {
		t.Fatalf("cross-version = %#v, %v", crossVersion, err)
	}
}

func TestAttachmentRepositoryListsByRequirement(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.NewAttachmentRepository(pool)
	ctx := context.Background()

	requirementID := int64(3001)
	first := sampleAttachment(ids, 9001)
	first.RequirementID = &requirementID
	second := sampleAttachment(ids, 9001)
	second.RequirementID = &requirementID
	other := sampleAttachment(ids, 9001)
	otherRequirementID := int64(3002)
	other.RequirementID = &otherRequirementID
	for _, attachment := range []version.Attachment{first, second, other} {
		if err := repo.Create(ctx, attachment); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	attachments, err := repo.ListByRequirement(ctx, 9001, requirementID)
	if err != nil || len(attachments) != 2 {
		t.Fatalf("attachments = %#v, %v", attachments, err)
	}
	if attachments[0].ID != first.ID || attachments[1].ID != second.ID {
		t.Fatalf("order = %#v", attachments)
	}
	if crossVersion, err := repo.ListByRequirement(ctx, 9002, requirementID); err != nil || len(crossVersion) != 0 {
		t.Fatalf("cross-version = %#v, %v", crossVersion, err)
	}
}
