//go:build integration

package versionpg_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformclock "github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
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

// TestAttachmentRepositoryConversationIncludesPreRegistered covers R-E4-1: a
// message attachment pre-registered for a conversation before its message
// insert (nil conversation_message_id, conversation_id set) must already be
// visible to ListByConversation while ingest runs, without leaking requirement
// attachments or attachments of other conversations into the result.
func TestAttachmentRepositoryConversationIncludesPreRegistered(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.NewAttachmentRepository(pool)
	ctx := context.Background()

	versionID := ids.Next()
	if _, err := pool.Exec(ctx, `INSERT INTO version (id, title, created_by) VALUES ($1, 'attachment race', 42)`, versionID); err != nil {
		t.Fatalf("insert version: %v", err)
	}
	conversationID := ids.Next()
	messageID := ids.Next()
	if _, err := pool.Exec(ctx, `INSERT INTO conversation (id, version_id, kind, title) VALUES ($1, $2, 'clarification', 'Chat')`, conversationID, versionID); err != nil {
		t.Fatalf("insert conversation: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO conversation_message (id, conversation_id, role) VALUES ($1, $2, 'user')`, messageID, conversationID); err != nil {
		t.Fatalf("insert message: %v", err)
	}
	otherConversationID := ids.Next()
	otherMessageID := ids.Next()
	if _, err := pool.Exec(ctx, `INSERT INTO conversation (id, version_id, kind, title) VALUES ($1, $2, 'clarification', 'Other')`, otherConversationID, versionID); err != nil {
		t.Fatalf("insert other conversation: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO conversation_message (id, conversation_id, role) VALUES ($1, $2, 'user')`, otherMessageID, otherConversationID); err != nil {
		t.Fatalf("insert other message: %v", err)
	}

	linked := sampleAttachment(ids, versionID)
	linked.ConversationMessageID = &messageID
	linked.ConversationID = &conversationID
	preRegistered := sampleAttachment(ids, versionID)
	preRegistered.ConversationMessageID = nil
	preRegistered.ConversationID = &conversationID
	requirementID := int64(3001)
	requirementAttachment := sampleAttachment(ids, versionID)
	requirementAttachment.ConversationMessageID = nil
	requirementAttachment.RequirementID = &requirementID
	otherConversation := sampleAttachment(ids, versionID)
	otherConversation.ConversationMessageID = &otherMessageID
	otherConversation.ConversationID = &otherConversationID
	for _, attachment := range []version.Attachment{linked, preRegistered, requirementAttachment, otherConversation} {
		if err := repo.Create(ctx, attachment); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	attachments, err := repo.ListByConversation(ctx, versionID, conversationID)
	if err != nil {
		t.Fatalf("ListByConversation: %v", err)
	}
	if len(attachments) != 2 || attachments[0].ID != linked.ID || attachments[1].ID != preRegistered.ID {
		t.Fatalf("attachments = %#v, want the linked and pre-registered rows", attachments)
	}
	if attachments[1].ConversationMessageID != nil {
		t.Fatalf("pre-registered row was bound: %#v", attachments[1])
	}

	byRequirement, err := repo.ListByRequirement(ctx, versionID, requirementID)
	if err != nil || len(byRequirement) != 1 || byRequirement[0].ID != requirementAttachment.ID {
		t.Fatalf("requirement attachments = %#v, %v", byRequirement, err)
	}
}

// TestAttachmentRepositoryConversationScopesPreRegisteredDrafts covers the
// Task 20 follow-up scope leak: two conversations of the same version each
// pre-register a message attachment (unbound draft), and neither conversation
// may see or read the other's draft. Both attachment.list and
// attachment.read_text resolve visibility through ListByConversation, so this
// pins the repository contract directly and then exercises the read_text path
// through the real application service.
func TestAttachmentRepositoryConversationScopesPreRegisteredDrafts(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.NewAttachmentRepository(pool)
	ctx := context.Background()

	versionID := ids.Next()
	if _, err := pool.Exec(ctx, `INSERT INTO version (id, title, created_by) VALUES ($1, 'attachment scope', 42)`, versionID); err != nil {
		t.Fatalf("insert version: %v", err)
	}
	conversationA := ids.Next()
	conversationB := ids.Next()
	for _, conversation := range []struct {
		id    int64
		title string
	}{{conversationA, "Chat A"}, {conversationB, "Chat B"}} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO conversation (id, version_id, kind, title)
			VALUES ($1, $2, 'clarification', $3)
		`, conversation.id, versionID, conversation.title); err != nil {
			t.Fatalf("insert conversation %d: %v", conversation.id, err)
		}
	}
	messageA := ids.Next()
	messageB := ids.Next()
	for _, message := range []struct {
		id             int64
		conversationID int64
	}{{messageA, conversationA}, {messageB, conversationB}} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO conversation_message (id, conversation_id, role)
			VALUES ($1, $2, 'user')
		`, message.id, message.conversationID); err != nil {
			t.Fatalf("insert message %d: %v", message.id, err)
		}
	}

	// One bound row in A (the normal list path) plus one unbound draft in each
	// conversation (the 409 pre-registration window).
	linkedA := sampleAttachment(ids, versionID)
	linkedA.ConversationMessageID = &messageA
	linkedA.ConversationID = &conversationA
	draftA := sampleAttachment(ids, versionID)
	draftA.ConversationMessageID = nil
	draftA.ConversationID = &conversationA
	draftB := sampleAttachment(ids, versionID)
	draftB.ConversationMessageID = nil
	draftB.ConversationID = &conversationB
	for _, attachment := range []version.Attachment{linkedA, draftA, draftB} {
		if err := repo.Create(ctx, attachment); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	// The owning conversation round-trips through Create/scan.
	stored, err := repo.Get(ctx, draftB.ID)
	if err != nil {
		t.Fatalf("Get draft B: %v", err)
	}
	if stored.ConversationID == nil || *stored.ConversationID != conversationB {
		t.Fatalf("draft B conversation = %#v, want %d", stored.ConversationID, conversationB)
	}

	// Conversation A sees its linked row and its own draft, never B's draft.
	listA, err := repo.ListByConversation(ctx, versionID, conversationA)
	if err != nil {
		t.Fatalf("ListByConversation(A): %v", err)
	}
	if len(listA) != 2 || listA[0].ID != linkedA.ID || listA[1].ID != draftA.ID {
		t.Fatalf("conversation A list = %#v, want the linked row and its own draft", listA)
	}
	for _, attachment := range listA {
		if attachment.ID == draftB.ID {
			t.Fatalf("conversation A sees conversation B's draft: %#v", attachment)
		}
	}

	listB, err := repo.ListByConversation(ctx, versionID, conversationB)
	if err != nil {
		t.Fatalf("ListByConversation(B): %v", err)
	}
	if len(listB) != 1 || listB[0].ID != draftB.ID {
		t.Fatalf("conversation B list = %#v, want only its own draft", listB)
	}

	// attachment.read_text path: B must not resolve A's draft (404), while A
	// still reads its own pending draft as the retryable R-E4-1 result.
	svc := versionapp.NewAttachmentService(repo, nil, nil, nil, nil, nil, ids, platformclock.System{})
	if _, err := svc.ReadAttachmentText(ctx, versionID, conversationB, draftA.ID, 0, 10); err == nil {
		t.Fatal("conversation B read conversation A's pre-registered draft")
	} else {
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
			t.Fatalf("read_text in B error = %v, want 404 not_found", err)
		}
	}
	read, err := svc.ReadAttachmentText(ctx, versionID, conversationA, draftA.ID, 0, 10)
	if err != nil {
		t.Fatalf("read_text in A: %v", err)
	}
	if read.AttachmentID != draftA.ID || read.Status != version.AttachmentPending || !read.Retryable {
		t.Fatalf("conversation A read_text = %#v, want its own retryable pending draft", read)
	}

	// Binding derives the ownership from the inserted message: a legacy unscoped
	// draft is backfilled with the message's conversation, while an already
	// scoped draft keeps the conversation it was pre-registered for.
	legacy := sampleAttachment(ids, versionID)
	legacy.ConversationMessageID = nil
	legacy.ConversationID = nil
	if err := repo.Create(ctx, legacy); err != nil {
		t.Fatalf("Create legacy draft: %v", err)
	}
	if err := repo.LinkMessage(ctx, legacy.ID, messageA, testTime); err != nil {
		t.Fatalf("LinkMessage legacy draft: %v", err)
	}
	boundLegacy, err := repo.Get(ctx, legacy.ID)
	if err != nil {
		t.Fatalf("Get bound legacy draft: %v", err)
	}
	if boundLegacy.ConversationMessageID == nil || *boundLegacy.ConversationMessageID != messageA ||
		boundLegacy.ConversationID == nil || *boundLegacy.ConversationID != conversationA {
		t.Fatalf("bound legacy draft = %#v, want message %d in conversation %d", boundLegacy, messageA, conversationA)
	}
	if err := repo.LinkMessage(ctx, draftB.ID, messageB, testTime); err != nil {
		t.Fatalf("LinkMessage draft B: %v", err)
	}
	boundB, err := repo.Get(ctx, draftB.ID)
	if err != nil {
		t.Fatalf("Get bound draft B: %v", err)
	}
	if boundB.ConversationID == nil || *boundB.ConversationID != conversationB {
		t.Fatalf("bound draft B conversation = %#v, want %d", boundB.ConversationID, conversationB)
	}
	listAAfterBind, err := repo.ListByConversation(ctx, versionID, conversationA)
	if err != nil {
		t.Fatalf("ListByConversation(A) after bind: %v", err)
	}
	if len(listAAfterBind) != 3 || listAAfterBind[2].ID != legacy.ID {
		t.Fatalf("conversation A list after bind = %#v, want the backfilled legacy row", listAAfterBind)
	}
}

// TestAttachmentStaleProcessingRecovery pins the C-04 worker-crash recovery:
// the leader-gated ListPendingForIngest sweep returns an abandoned processing
// row to pending, keeps a freshly touched slow parse untouched, and image
// transitions touch the parent attachment so a legitimate slow parse is not
// reset.
func TestAttachmentStaleProcessingRecovery(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.NewAttachmentRepository(pool)
	ctx := context.Background()
	now := time.Now().UTC()

	stale := sampleAttachment(ids, 9001)
	fresh := sampleAttachment(ids, 9001)
	for _, attachment := range []version.Attachment{stale, fresh} {
		if err := repo.Create(ctx, attachment); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	for _, row := range []struct {
		id      int64
		updated time.Time
	}{
		{stale.ID, now.Add(-20 * time.Minute)},
		{fresh.ID, now.Add(-time.Minute)},
	} {
		if _, err := pool.Exec(ctx, `
			UPDATE requirement_attachment
			SET extraction_status = 'processing', updated_at = $2
			WHERE id = $1
		`, row.id, row.updated); err != nil {
			t.Fatalf("age processing attachment: %v", err)
		}
	}

	pending, err := repo.ListPendingForIngest(ctx, 10)
	if err != nil {
		t.Fatalf("ListPendingForIngest: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != stale.ID {
		t.Fatalf("pending=%+v, want only the stale row reset", pending)
	}
	storedStale, err := repo.Get(ctx, stale.ID)
	if err != nil {
		t.Fatalf("Get stale: %v", err)
	}
	storedFresh, err := repo.Get(ctx, fresh.ID)
	if err != nil {
		t.Fatalf("Get fresh: %v", err)
	}
	if storedStale.ExtractionStatus != version.AttachmentPending {
		t.Fatalf("stale status = %q, want pending", storedStale.ExtractionStatus)
	}
	if storedFresh.ExtractionStatus != version.AttachmentProcessing {
		t.Fatalf("fresh slow parse was reset: %q", storedFresh.ExtractionStatus)
	}

	// Image transitions are the heartbeat that keeps the fresh row alive.
	image := version.AttachmentImageRecord{
		ID: ids.Next(), AttachmentID: fresh.ID, Seq: 1, ContentRefID: 77,
		MIME: "image/png", SourceKind: version.AttachmentImageSourceEmbedded,
		Status: version.AttachmentImageExtracted, CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute),
	}
	if err := repo.CreateImage(ctx, image); err != nil {
		t.Fatalf("CreateImage: %v", err)
	}
	if changed, err := repo.MarkImageAnalyzing(ctx, image.ID, now); err != nil || !changed {
		t.Fatalf("MarkImageAnalyzing=%v err=%v", changed, err)
	}
	touched, err := repo.Get(ctx, fresh.ID)
	if err != nil {
		t.Fatalf("Get touched: %v", err)
	}
	if !touched.UpdatedAt.After(now.Add(-time.Second)) {
		t.Fatalf("image transition did not touch the parent attachment: %v", touched.UpdatedAt)
	}
}
