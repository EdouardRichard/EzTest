//go:build integration

package versionpg_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
)

func TestConversationRepositoryLifecycle(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.NewConversationRepository(pool, ids)
	versionRepo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 42)
	if err := versionRepo.Create(ctx, v, sampleBindings(ids, v.ID)); err != nil {
		t.Fatalf("create version: %v", err)
	}

	createdBy := int64(42)
	conversation := version.Conversation{
		ID: ids.Next(), VersionID: v.ID, Kind: "clarification", Title: "Chat",
		Status: version.ConversationActive, CreatedBy: &createdBy, CreatedAt: testTime, UpdatedAt: testTime,
	}
	if err := repo.Create(ctx, conversation); err != nil {
		t.Fatalf("Create: %v", err)
	}
	stored, summary, err := repo.Get(ctx, v.ID, conversation.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Title != "Chat" || stored.MessageCount != 0 || stored.LastMessageAt != nil || summary != nil {
		t.Fatalf("stored = %#v, summary = %#v", stored, summary)
	}
	if _, _, err := repo.Get(ctx, v.ID+1, conversation.ID); !errors.Is(err, version.ErrConversationNotFound) {
		t.Fatalf("cross-version Get error = %v", err)
	}

	// Design conversations are seeded directly (P1 API only creates
	// clarification) and must be filterable.
	design := version.Conversation{
		ID: ids.Next(), VersionID: v.ID, Kind: "design", Title: "Design",
		Status: version.ConversationActive, CreatedAt: testTime, UpdatedAt: testTime,
	}
	if err := repo.Create(ctx, design); err != nil {
		t.Fatalf("Create design: %v", err)
	}
	all, err := repo.List(ctx, v.ID, "", 10, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 || all[0].ID != design.ID {
		t.Fatalf("all = %#v", all)
	}
	clarification, err := repo.List(ctx, v.ID, "clarification", 10, nil)
	if err != nil || len(clarification) != 1 || clarification[0].ID != conversation.ID {
		t.Fatalf("clarification = %#v, %v", clarification, err)
	}
	older, err := repo.List(ctx, v.ID, "", 10, &design.ID)
	if err != nil || len(older) != 1 || older[0].ID != conversation.ID {
		t.Fatalf("older = %#v, %v", older, err)
	}

	// User message insert with client id idempotency.
	clientID := "client-1"
	firstMessage := version.Message{
		ID: ids.Next(), ConversationID: conversation.ID, Role: version.MessageRoleUser,
		Parts: []version.MessagePart{
			{Type: "text", Text: "Please check"},
			{Type: "attachment", SHA256: strings.Repeat("a", 64), Name: "spec.md", MIME: "text/markdown", Size: 42},
		},
		Refs:            []version.MessageRef{{Kind: "requirement", ID: "9001"}},
		TokenUsage:      map[string]any{"input": float64(10)},
		ActorID:         &createdBy,
		ClientMessageID: &clientID,
		CreatedAt:       testTime.Add(time.Hour),
	}
	existing, created, err := repo.PostUserMessage(ctx, conversation.ID, firstMessage)
	if err != nil || !created || existing != nil {
		t.Fatalf("PostUserMessage = %#v/%v/%v", existing, created, err)
	}
	duplicate := firstMessage
	duplicate.ID = ids.Next()
	existing, created, err = repo.PostUserMessage(ctx, conversation.ID, duplicate)
	if err != nil {
		t.Fatalf("duplicate PostUserMessage: %v", err)
	}
	if created || existing == nil || existing.ID != firstMessage.ID {
		t.Fatalf("duplicate = %#v, created = %v", existing, created)
	}
	if len(existing.Parts) != 2 || existing.Parts[1].SHA256 != strings.Repeat("a", 64) {
		t.Fatalf("parts = %#v", existing.Parts)
	}
	if len(existing.Refs) != 1 || existing.Refs[0].Kind != "requirement" {
		t.Fatalf("refs = %#v", existing.Refs)
	}
	if existing.TokenUsage["input"] != float64(10) {
		t.Fatalf("token usage = %#v", existing.TokenUsage)
	}

	found, err := repo.FindMessageByClientID(ctx, conversation.ID, "client-1")
	if err != nil || found.ID != firstMessage.ID {
		t.Fatalf("FindMessageByClientID = %#v, %v", found, err)
	}
	if _, err := repo.FindMessageByClientID(ctx, conversation.ID, "missing"); !errors.Is(err, version.ErrMessageNotFound) {
		t.Fatalf("missing client id error = %v", err)
	}

	// Assistant messages append to the same thread.
	assistant := version.Message{
		ID: ids.Next(), ConversationID: conversation.ID, Role: version.MessageRoleAssistant,
		Parts:      []version.MessagePart{{Type: "text", Text: "Done"}, {Type: "requirement_card", RequirementID: 3001, Title: "Coupons", Status: "draft", Revision: 1}},
		Refs:       []version.MessageRef{},
		TokenUsage: map[string]any{},
		CreatedAt:  testTime.Add(2 * time.Hour),
	}
	if err := repo.AppendAssistantMessage(ctx, conversation.ID, assistant); err != nil {
		t.Fatalf("AppendAssistantMessage: %v", err)
	}
	missingConversation := assistant
	missingConversation.ID = ids.Next()
	if err := repo.AppendAssistantMessage(ctx, 999999, missingConversation); !errors.Is(err, version.ErrConversationNotFound) {
		t.Fatalf("missing conversation append error = %v", err)
	}

	messages, err := repo.Messages(ctx, conversation.ID, 0, 10)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(messages) != 2 || messages[0].ID != firstMessage.ID || messages[1].ID != assistant.ID {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[1].Parts[1].Type != "requirement_card" {
		t.Fatalf("assistant parts = %#v", messages[1].Parts)
	}
	after, err := repo.Messages(ctx, conversation.ID, firstMessage.ID, 10)
	if err != nil || len(after) != 1 || after[0].ID != assistant.ID {
		t.Fatalf("after = %#v, %v", after, err)
	}

	stored, _, err = repo.Get(ctx, v.ID, conversation.ID)
	if err != nil {
		t.Fatalf("Get after messages: %v", err)
	}
	if stored.MessageCount != 2 || stored.LastMessageAt == nil || !stored.LastMessageAt.Equal(assistant.CreatedAt) {
		t.Fatalf("stored = %#v", stored)
	}
}

func TestConversationRepositorySummaryVersioning(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.NewConversationRepository(pool, ids)
	versionRepo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 1)
	if err := versionRepo.Create(ctx, v, sampleBindings(ids, v.ID)); err != nil {
		t.Fatalf("create version: %v", err)
	}
	conversation := version.Conversation{
		ID: ids.Next(), VersionID: v.ID, Kind: "clarification", Title: "Chat",
		Status: version.ConversationActive, CreatedAt: testTime, UpdatedAt: testTime,
	}
	if err := repo.Create(ctx, conversation); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if summary, err := repo.Summary(ctx, conversation.ID); err != nil || summary != nil {
		t.Fatalf("empty summary = %#v, %v", summary, err)
	}

	first, err := repo.SaveSummary(ctx, version.ConversationSummary{
		ConversationID: conversation.ID,
		Summary:        "First",
		PinnedFacts:    []string{"fact one", "fact two"},
		TokenEstimate:  3,
		UpdatedAt:      testTime.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	if first.Version != 1 || first.ID <= 0 {
		t.Fatalf("first = %#v", first)
	}
	second, err := repo.SaveSummary(ctx, version.ConversationSummary{
		ConversationID: conversation.ID,
		Summary:        "Second",
		PinnedFacts:    []string{"fact one"},
		TokenEstimate:  4,
		UpdatedAt:      testTime.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("SaveSummary second: %v", err)
	}
	if second.Version != 2 {
		t.Fatalf("second version = %d", second.Version)
	}

	latest, err := repo.Summary(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if latest.Version != 2 || latest.Summary != "Second" || len(latest.PinnedFacts) != 1 {
		t.Fatalf("latest = %#v", latest)
	}

	_, summary, err := repo.Get(ctx, v.ID, conversation.ID)
	if err != nil || summary == nil || summary.Summary != "Second" {
		t.Fatalf("Get summary = %#v, %v", summary, err)
	}

	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT pinned_facts FROM conversation_summary WHERE conversation_id = $1 AND version = 2`, conversation.ID).Scan(&raw); err != nil {
		t.Fatalf("query pinned facts: %v", err)
	}
	var pinned []string
	if err := json.Unmarshal(raw, &pinned); err != nil || len(pinned) != 1 || pinned[0] != "fact one" {
		t.Fatalf("pinned = %#v, %v", pinned, err)
	}
}

func TestListReconcilableIncludesKind(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.NewConversationRepository(pool, ids)
	versionRepo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 7)
	if err := versionRepo.Create(ctx, v, sampleBindings(ids, v.ID)); err != nil {
		t.Fatalf("create version: %v", err)
	}
	clarification := version.Conversation{
		ID: ids.Next(), VersionID: v.ID, Kind: version.ConversationClarification, Title: "Chat",
		Status: version.ConversationActive, CreatedAt: testTime, UpdatedAt: testTime,
	}
	design := version.Conversation{
		ID: ids.Next(), VersionID: v.ID, Kind: version.ConversationDesign, Title: "Design",
		Status: version.ConversationActive, CreatedAt: testTime, UpdatedAt: testTime,
	}
	for _, conversation := range []version.Conversation{clarification, design} {
		if err := repo.Create(ctx, conversation); err != nil {
			t.Fatalf("Create %s: %v", conversation.Kind, err)
		}
	}

	items, err := repo.ListReconcilable(ctx, 10)
	if err != nil {
		t.Fatalf("ListReconcilable: %v", err)
	}
	byID := make(map[int64]version.ReconcilerConversation, len(items))
	for _, item := range items {
		byID[item.ID] = item
		if item.Messages == nil {
			t.Fatalf("messages must be a non-nil empty slice: %#v", item)
		}
	}
	if got := byID[clarification.ID]; got.Kind != version.ConversationClarification || got.VersionID != v.ID {
		t.Fatalf("clarification row = %#v", got)
	}
	if got := byID[design.ID]; got.Kind != version.ConversationDesign || got.VersionID != v.ID {
		t.Fatalf("design row = %#v", got)
	}
}

// TestConversationSummaryWatermarkAndCompactionMeta covers the v0.22 R-A5-12
// persistence contract (Task 36 fix round 1): the covered_message_id watermark,
// the compaction id/meta and the monotonic guard of the real PostgreSQL store.
func TestConversationSummaryWatermarkAndCompactionMeta(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.NewConversationRepository(pool, ids)
	versionRepo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 42)
	if err := versionRepo.Create(ctx, v, sampleBindings(ids, v.ID)); err != nil {
		t.Fatalf("create version: %v", err)
	}
	conversation := version.Conversation{
		ID: ids.Next(), VersionID: v.ID, Kind: "clarification", Title: "Chat",
		Status: version.ConversationActive, CreatedAt: testTime, UpdatedAt: testTime,
	}
	if err := repo.Create(ctx, conversation); err != nil {
		t.Fatalf("Create: %v", err)
	}

	first, err := repo.SaveSummary(ctx, version.ConversationSummary{
		ConversationID: conversation.ID, Summary: "summary one", PinnedFacts: []string{"fact"},
		TokenEstimate: 3, CoveredMessageID: 100, CompactionID: "compaction-1",
		SummaryMeta: map[string]any{"trigger": "pressure"}, UpdatedAt: testTime,
	})
	if err != nil {
		t.Fatalf("SaveSummary(first): %v", err)
	}
	if first.Version != 1 || first.CoveredMessageID != 100 || first.CompactionID != "compaction-1" {
		t.Fatalf("first summary = %#v", first)
	}
	second, err := repo.SaveSummary(ctx, version.ConversationSummary{
		ConversationID: conversation.ID, Summary: "summary two",
		TokenEstimate: 3, CoveredMessageID: 150, CompactionID: "compaction-2",
		SummaryMeta: map[string]any{"trigger": "overflow"}, UpdatedAt: testTime,
	})
	if err != nil {
		t.Fatalf("SaveSummary(second): %v", err)
	}
	if second.Version != 2 || second.CoveredMessageID != 150 {
		t.Fatalf("second summary = %#v", second)
	}
	if _, err := repo.SaveSummary(ctx, version.ConversationSummary{
		ConversationID: conversation.ID, Summary: "stale", TokenEstimate: 3,
		CoveredMessageID: 120, UpdatedAt: testTime,
	}); !errors.Is(err, version.ErrSummaryWatermarkStale) {
		t.Fatalf("stale watermark error = %v, want ErrSummaryWatermarkStale", err)
	}
	latest, err := repo.Summary(ctx, conversation.ID)
	if err != nil || latest == nil {
		t.Fatalf("Summary: %#v, %v", latest, err)
	}
	if latest.Version != 2 || latest.Summary != "summary two" || latest.CoveredMessageID != 150 ||
		latest.CompactionID != "compaction-2" || latest.SummaryMeta["trigger"] != "overflow" {
		t.Fatalf("latest summary = %#v", latest)
	}
}

// TestConversationQuestionAndJobCardSnapshotRead is the Critical 1 PostgreSQL
// regression: the persisted question card snapshot and a subsequent job-card
// message both read back through the version domain (the pre-fix direct INSERT
// of `question_card` corrupted the read path).
func TestConversationQuestionAndJobCardSnapshotRead(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.NewConversationRepository(pool, ids)
	versionRepo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 42)
	if err := versionRepo.Create(ctx, v, sampleBindings(ids, v.ID)); err != nil {
		t.Fatalf("create version: %v", err)
	}
	conversation := version.Conversation{
		ID: ids.Next(), VersionID: v.ID, Kind: "clarification", Title: "Chat",
		Status: version.ConversationActive, CreatedAt: testTime, UpdatedAt: testTime,
	}
	if err := repo.Create(ctx, conversation); err != nil {
		t.Fatalf("Create: %v", err)
	}
	const runID = int64(701)
	if err := repo.AcquireWriter(ctx, conversation.ID, runID, "clarification_host", testTime, testTime.Add(time.Minute)); err != nil {
		t.Fatalf("AcquireWriter: %v", err)
	}
	question := version.MessagePart{
		Type: version.MessagePartQuestion,
		Question: json.RawMessage(`{"question_id":"9001","batch_id":"qb-9001",` +
			`"questions":[{"id":"q1","text":"Which login method?"}],"expires_at":"2026-11-06T08:00:00Z"}`),
	}
	clientID := "question-ask:9001"
	runIDValue := runID
	message := version.Message{
		ID: ids.Next(), ConversationID: conversation.ID, Role: version.MessageRoleAssistant,
		Parts: []version.MessagePart{question}, Kind: version.MessageKindQuestion,
		ClientMessageID: &clientID, AgentRunID: &runIDValue, AuthorAgent: "clarification_host",
		CreatedAt: testTime,
	}
	if _, created, err := repo.AppendAssistantMessageAsWriter(ctx, conversation.ID, message, runID,
		"clarification_host", testTime, testTime.Add(time.Minute)); err != nil || !created {
		t.Fatalf("append question card = %v/%v", created, err)
	}
	// A later job card message in the same conversation must stay readable too.
	jobCard := version.MessagePart{
		Type: version.MessagePartJobCard, JobID: 9001, JobKind: "chat.turn", Status: "done",
	}
	cardMessage := version.Message{
		ID: ids.Next(), ConversationID: conversation.ID, Role: version.MessageRoleAssistant,
		Parts: []version.MessagePart{jobCard}, Kind: version.MessageKindArtifact, CreatedAt: testTime,
	}
	if err := repo.AppendAssistantMessage(ctx, conversation.ID, cardMessage); err != nil {
		t.Fatalf("append job card: %v", err)
	}

	messages, err := repo.Messages(ctx, conversation.ID, 0, 10)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	var questionRead, cardRead bool
	for _, stored := range messages {
		if stored.Kind == version.MessageKindQuestion {
			if len(stored.Parts) != 1 || stored.Parts[0].Type != version.MessagePartQuestion ||
				!strings.Contains(string(stored.Parts[0].Question), `"question_id":"9001"`) {
				t.Fatalf("question card read = %#v", stored)
			}
			questionRead = true
		}
		if len(stored.Parts) == 1 && stored.Parts[0].Type == version.MessagePartJobCard {
			if stored.Parts[0].JobID != 9001 {
				t.Fatalf("job card read = %#v", stored.Parts[0])
			}
			cardRead = true
		}
	}
	if !questionRead || !cardRead {
		t.Fatalf("question/job card read = %v/%v", questionRead, cardRead)
	}
}
