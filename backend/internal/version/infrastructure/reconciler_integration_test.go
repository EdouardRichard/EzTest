//go:build integration

package versionpg_test

import (
	"context"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
)

func TestReconcilerReadPorts(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	versionRepo := versionpg.New(pool, ids)
	conversationRepo := versionpg.NewConversationRepository(pool, ids)
	requirementRepo := versionpg.NewRequirementRepository(pool)
	attachmentRepo := versionpg.NewAttachmentRepository(pool)
	ctx := context.Background()

	withMessage := sampleVersion(ids, 1)
	withRequirement := sampleVersion(ids, 1)
	alreadyRunning := sampleVersion(ids, 1)
	emptyInitial := sampleVersion(ids, 1)
	for _, v := range []version.Version{withMessage, withRequirement, alreadyRunning, emptyInitial} {
		if err := versionRepo.Create(ctx, v, sampleBindings(ids, v.ID)); err != nil {
			t.Fatalf("create version: %v", err)
		}
	}
	if _, err := versionRepo.MarkRunning(ctx, alreadyRunning.ID, testTime); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	requirement, requirementRevision := sampleRequirement(ids, withRequirement.ID)
	if err := requirementRepo.Create(ctx, requirement, requirementRevision); err != nil {
		t.Fatalf("create requirement: %v", err)
	}

	// A conversation with a message projects the version; an empty
	// conversation must not be returned.
	conversation := version.Conversation{
		ID: ids.Next(), VersionID: withMessage.ID, Kind: "clarification", Title: "Chat",
		Status: version.ConversationActive, CreatedAt: testTime, UpdatedAt: testTime,
	}
	if err := conversationRepo.Create(ctx, conversation); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	userMessage := version.Message{
		ID: ids.Next(), ConversationID: conversation.ID, Role: version.MessageRoleUser,
		Parts: []version.MessagePart{{Type: "text", Text: "question"}}, Refs: []version.MessageRef{},
		TokenUsage: map[string]any{}, CreatedAt: testTime,
	}
	if _, _, err := conversationRepo.PostUserMessage(ctx, conversation.ID, userMessage); err != nil {
		t.Fatalf("post user message: %v", err)
	}
	runID := ids.Next()
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_run (id, profile_key, conversation_id, owner_instance)
		VALUES ($1, 'clarification_host', $2, 'test')
	`, runID, conversation.ID); err != nil {
		t.Fatalf("insert agent run: %v", err)
	}
	assistant := version.Message{
		ID: ids.Next(), ConversationID: conversation.ID, Role: version.MessageRoleAssistant,
		Parts: []version.MessagePart{{Type: "text", Text: "answer"}}, Refs: []version.MessageRef{},
		TokenUsage: map[string]any{}, AgentRunID: &runID, CreatedAt: testTime.Add(time.Minute),
	}
	if err := conversationRepo.AppendAssistantMessage(ctx, conversation.ID, assistant); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}

	// A design conversation with a message is not reconcilable.
	design := version.Conversation{
		ID: ids.Next(), VersionID: withMessage.ID, Kind: "design", Title: "Design",
		Status: version.ConversationActive, CreatedAt: testTime, UpdatedAt: testTime,
	}
	if err := conversationRepo.Create(ctx, design); err != nil {
		t.Fatalf("create design conversation: %v", err)
	}
	if err := conversationRepo.AppendAssistantMessage(ctx, design.ID, version.Message{
		ID: ids.Next(), ConversationID: design.ID, Role: version.MessageRoleAssistant,
		Parts: []version.MessagePart{{Type: "text", Text: "design"}}, Refs: []version.MessageRef{},
		TokenUsage: map[string]any{}, CreatedAt: testTime,
	}); err != nil {
		t.Fatalf("append design message: %v", err)
	}

	emptyConversation := version.Conversation{
		ID: ids.Next(), VersionID: emptyInitial.ID, Kind: "clarification", Title: "Empty",
		Status: version.ConversationActive, CreatedAt: testTime, UpdatedAt: testTime,
	}
	if err := conversationRepo.Create(ctx, emptyConversation); err != nil {
		t.Fatalf("create empty conversation: %v", err)
	}

	// 1) initial versions with content: message or requirement, ascending.
	initialIDs, err := versionRepo.ListInitialVersionsWithContent(ctx, 100)
	if err != nil {
		t.Fatalf("ListInitialVersionsWithContent: %v", err)
	}
	wantInitial := []int64{withMessage.ID, withRequirement.ID}
	if len(initialIDs) != len(wantInitial) || initialIDs[0] != wantInitial[0] || initialIDs[1] != wantInitial[1] {
		t.Fatalf("initial ids = %#v, want %#v", initialIDs, wantInitial)
	}
	limited, err := versionRepo.ListInitialVersionsWithContent(ctx, 1)
	if err != nil || len(limited) != 1 || limited[0] != withMessage.ID {
		t.Fatalf("limited initial ids = %#v, %v", limited, err)
	}

	// 2) reconcilable conversations: clarification + messages, newest first,
	// messages ascending with the assistant run's profile key.
	reconcilable, err := conversationRepo.ListReconcilable(ctx, 100)
	if err != nil {
		t.Fatalf("ListReconcilable: %v", err)
	}
	if len(reconcilable) != 1 || reconcilable[0].ID != conversation.ID || reconcilable[0].VersionID != withMessage.ID {
		t.Fatalf("reconcilable = %#v", reconcilable)
	}
	messages := reconcilable[0].Messages
	if len(messages) != 2 || messages[0].ID != userMessage.ID || messages[1].ID != assistant.ID {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0].Role != "user" || messages[0].AgentProfileKey != "" {
		t.Fatalf("user message = %#v", messages[0])
	}
	if messages[1].Role != "assistant" || messages[1].AgentProfileKey != "clarification_host" {
		t.Fatalf("assistant message = %#v", messages[1])
	}
	if limitedReconcilable, err := conversationRepo.ListReconcilable(ctx, 1); err != nil || len(limitedReconcilable) != 1 {
		t.Fatalf("limited reconcilable = %#v, %v", limitedReconcilable, err)
	}

	// 3) pending attachments with conversation/message linkage.
	messageID := userMessage.ID
	pending := version.Attachment{
		ID: ids.Next(), VersionID: withMessage.ID, ConversationMessageID: &messageID,
		ContentRefID: 11, Name: "spec.md", MIME: "text/markdown", Size: 12,
		ExtractionStatus: version.AttachmentPending, CreatedAt: testTime, UpdatedAt: testTime,
	}
	unlinked := version.Attachment{
		ID: ids.Next(), VersionID: withMessage.ID, ContentRefID: 12, Name: "loose.md",
		MIME: "text/markdown", Size: 12, ExtractionStatus: version.AttachmentPending,
		CreatedAt: testTime, UpdatedAt: testTime,
	}
	done := version.Attachment{
		ID: ids.Next(), VersionID: withMessage.ID, ContentRefID: 13, Name: "done.md",
		MIME: "text/markdown", Size: 12, ExtractionStatus: version.AttachmentDone,
		CreatedAt: testTime, UpdatedAt: testTime,
	}
	for _, attachment := range []version.Attachment{pending, unlinked, done} {
		if err := attachmentRepo.Create(ctx, attachment); err != nil {
			t.Fatalf("create attachment: %v", err)
		}
	}
	pendingItems, err := attachmentRepo.ListPendingForIngest(ctx, 100)
	if err != nil {
		t.Fatalf("ListPendingForIngest: %v", err)
	}
	if len(pendingItems) != 2 {
		t.Fatalf("pending = %#v", pendingItems)
	}
	if pendingItems[0].ID != pending.ID || pendingItems[0].ConversationID != conversation.ID || pendingItems[0].MessageID != messageID {
		t.Fatalf("linked pending = %#v", pendingItems[0])
	}
	if pendingItems[1].ID != unlinked.ID || pendingItems[1].ConversationID != 0 || pendingItems[1].MessageID != 0 {
		t.Fatalf("unlinked pending = %#v", pendingItems[1])
	}
	if limitedPending, err := attachmentRepo.ListPendingForIngest(ctx, 1); err != nil || len(limitedPending) != 1 {
		t.Fatalf("limited pending = %#v, %v", limitedPending, err)
	}
}
