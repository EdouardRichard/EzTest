package versionapp_test

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ---------- fake conversation repository ----------

type fakeConversationRepo struct {
	conversations map[int64]version.Conversation
	messages      map[int64][]version.Message
	summaries     map[int64][]version.ConversationSummary
	forceExisting *version.Message
}

func newFakeConversationRepo() *fakeConversationRepo {
	return &fakeConversationRepo{
		conversations: map[int64]version.Conversation{},
		messages:      map[int64][]version.Message{},
		summaries:     map[int64][]version.ConversationSummary{},
	}
}

func (f *fakeConversationRepo) Create(_ context.Context, conversation version.Conversation) error {
	f.conversations[conversation.ID] = conversation
	return nil
}

func (f *fakeConversationRepo) Get(_ context.Context, versionID, conversationID int64) (version.Conversation, *version.ConversationSummary, error) {
	conversation, ok := f.conversations[conversationID]
	if !ok || conversation.VersionID != versionID {
		return version.Conversation{}, nil, version.ErrConversationNotFound
	}
	return conversation, f.latestSummary(conversationID), nil
}

func (f *fakeConversationRepo) GetByID(_ context.Context, conversationID int64) (version.Conversation, error) {
	conversation, ok := f.conversations[conversationID]
	if !ok {
		return version.Conversation{}, version.ErrConversationNotFound
	}
	return conversation, nil
}

func (f *fakeConversationRepo) List(_ context.Context, versionID int64, kind string, limit int, beforeID *int64) ([]version.Conversation, error) {
	items := make([]version.Conversation, 0)
	for _, conversation := range f.conversations {
		if conversation.VersionID != versionID {
			continue
		}
		if kind != "" && conversation.Kind != kind {
			continue
		}
		if beforeID != nil && conversation.ID >= *beforeID {
			continue
		}
		items = append(items, conversation)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (f *fakeConversationRepo) FindMessageByClientID(_ context.Context, conversationID int64, clientMessageID string) (*version.Message, error) {
	for _, message := range f.messages[conversationID] {
		if message.ClientMessageID != nil && *message.ClientMessageID == clientMessageID {
			copied := message
			return &copied, nil
		}
	}
	return nil, version.ErrMessageNotFound
}

func (f *fakeConversationRepo) Messages(_ context.Context, conversationID int64, after int64, limit int) ([]version.Message, error) {
	items := make([]version.Message, 0)
	for _, message := range f.messages[conversationID] {
		if message.ID > after {
			items = append(items, message)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (f *fakeConversationRepo) PostUserMessage(_ context.Context, conversationID int64, message version.Message) (*version.Message, bool, error) {
	if f.forceExisting != nil {
		return f.forceExisting, false, nil
	}
	if message.ClientMessageID != nil {
		for _, existing := range f.messages[conversationID] {
			if existing.ClientMessageID != nil && *existing.ClientMessageID == *message.ClientMessageID {
				copied := existing
				return &copied, false, nil
			}
		}
	}
	f.messages[conversationID] = append(f.messages[conversationID], message)
	conversation := f.conversations[conversationID]
	conversation.MessageCount = len(f.messages[conversationID])
	conversation.LastMessageAt = &message.CreatedAt
	conversation.UpdatedAt = message.CreatedAt
	f.conversations[conversationID] = conversation
	return nil, true, nil
}

func (f *fakeConversationRepo) AppendAssistantMessage(_ context.Context, conversationID int64, message version.Message) error {
	if _, ok := f.conversations[conversationID]; !ok {
		return version.ErrConversationNotFound
	}
	f.messages[conversationID] = append(f.messages[conversationID], message)
	conversation := f.conversations[conversationID]
	conversation.MessageCount = len(f.messages[conversationID])
	conversation.LastMessageAt = &message.CreatedAt
	conversation.UpdatedAt = message.CreatedAt
	f.conversations[conversationID] = conversation
	return nil
}

func (f *fakeConversationRepo) Summary(_ context.Context, conversationID int64) (*version.ConversationSummary, error) {
	return f.latestSummary(conversationID), nil
}

func (f *fakeConversationRepo) SaveSummary(_ context.Context, summary version.ConversationSummary) (version.ConversationSummary, error) {
	summary.Version = len(f.summaries[summary.ConversationID]) + 1
	summary.ID = 10000 + int64(summary.Version)
	f.summaries[summary.ConversationID] = append(f.summaries[summary.ConversationID], summary)
	return summary, nil
}

func (f *fakeConversationRepo) latestSummary(conversationID int64) *version.ConversationSummary {
	items := f.summaries[conversationID]
	if len(items) == 0 {
		return nil
	}
	latest := items[len(items)-1]
	return &latest
}

// ---------- fake queue / registrar ----------

type fakeQueue struct{ tasks []versionapp.TaskInput }

func (f *fakeQueue) Ensure(_ context.Context, task versionapp.TaskInput) error {
	f.tasks = append(f.tasks, task)
	return nil
}

func (f *fakeQueue) find(taskType, dedupeKey string) (versionapp.TaskInput, bool) {
	for _, task := range f.tasks {
		if task.Type == taskType && task.DedupeKey == dedupeKey {
			return task, true
		}
	}
	return versionapp.TaskInput{}, false
}

type registrarCall struct {
	versionID int64
	messageID int64
	part      version.MessagePart
	actorID   int64
}

type fakeRegistrar struct {
	calls []registrarCall
	err   error
	id    int64
}

func (f *fakeRegistrar) RegisterFromMessage(_ context.Context, versionID, messageID int64, part version.MessagePart, actorID int64) (version.Attachment, error) {
	f.calls = append(f.calls, registrarCall{versionID: versionID, messageID: messageID, part: part, actorID: actorID})
	if f.err != nil {
		return version.Attachment{}, f.err
	}
	f.id++
	return version.Attachment{
		ID: f.id, VersionID: versionID, ConversationMessageID: &messageID,
		Name: part.Name, MIME: part.MIME, Size: part.Size, ExtractionStatus: version.AttachmentPending,
		CreatedAt: testTime,
	}, nil
}

// ---------- conversation test environment ----------

type conversationEnv struct {
	svc        *versionapp.ConversationService
	repo       *fakeConversationRepo
	queue      *fakeQueue
	registrar  *fakeRegistrar
	versionEnv *testEnv
	versionID  int64
	events     *fakePublisher
}

func newConversationEnv(t *testing.T) *conversationEnv {
	t.Helper()
	base := newTestEnv(t)
	created := createVersion(t, base, "Conversation version")
	repo := newFakeConversationRepo()
	queue := &fakeQueue{}
	registrar := &fakeRegistrar{}
	events := &fakePublisher{}
	svc := versionapp.NewConversationService(
		repo, queue, registrar, base.svc, fixedClock{at: testTime}, &seqIDs{}, events,
	)
	return &conversationEnv{
		svc: svc, repo: repo, queue: queue, registrar: registrar,
		versionEnv: base, versionID: created.ID, events: events,
	}
}

func postUserMessage(t *testing.T, env *conversationEnv, conversationID int64, parts []version.MessagePart, clientID string) versionapp.MessageView {
	t.Helper()
	view, err := env.svc.PostUserMessage(requestContext(), env.versionID, conversationID, parts, clientID, 42)
	if err != nil {
		t.Fatalf("PostUserMessage: %v", err)
	}
	return view
}

func TestCreateConversationDefaultsTitleAndEnsuresOpeningTask(t *testing.T) {
	env := newConversationEnv(t)

	view, err := env.svc.Create(requestContext(), env.versionID, "clarification", "", 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if view.Title != "需求澄清 2026-10-31 12:00" {
		t.Fatalf("title = %q", view.Title)
	}
	if view.Kind != "clarification" || view.Status != "active" || view.MessageCount != 0 || view.LastMessageAt != nil {
		t.Fatalf("view = %#v", view)
	}
	if view.CreatedBy == nil || *view.CreatedBy != 42 || !view.CreatedAt.Equal(testTime) {
		t.Fatalf("view = %#v", view)
	}
	if view.Summary != nil {
		t.Fatalf("summary = %#v", view.Summary)
	}

	task, ok := env.queue.find("chat.turn", "opening:"+idString(view.ID))
	if !ok {
		t.Fatalf("opening task missing: %#v", env.queue.tasks)
	}
	if task.Team != "clarification" || task.VersionID != env.versionID || task.Payload["conversation_id"] != idString(view.ID) {
		t.Fatalf("task = %#v", task)
	}

	if len(env.events.events) != 1 || env.events.events[0].Type != "conversation.created" {
		t.Fatalf("events = %#v", env.events.events)
	}
	event := env.events.events[0]
	if event.Channel != "version:"+idString(env.versionID) || event.Payload["conversation_id"] != idString(view.ID) ||
		event.Payload["title"] != view.Title || event.Payload["kind"] != "clarification" {
		t.Fatalf("event = %#v", event)
	}

	versionView, err := env.versionEnv.svc.Get(requestContext(), env.versionID)
	if err != nil {
		t.Fatalf("version Get: %v", err)
	}
	if versionView.Status != version.StatusInitial {
		t.Fatalf("empty conversation projected version to %q", versionView.Status)
	}

	explicit, err := env.svc.Create(requestContext(), env.versionID, "", "  Coupon rules  ", 42)
	if err != nil {
		t.Fatalf("Create explicit title: %v", err)
	}
	if explicit.Title != "Coupon rules" {
		t.Fatalf("explicit title = %q", explicit.Title)
	}

	if _, err := env.svc.Create(requestContext(), env.versionID, "design", "", 42); err == nil {
		t.Fatal("design conversation accepted by the P1 API")
	} else if field := fieldName(t, err); field != "kind" {
		t.Fatalf("field = %q", field)
	}
	if _, err := env.svc.Create(requestContext(), 9999, "clarification", "", 42); err == nil {
		t.Fatal("missing version accepted")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("status = %d", apiError(t, err).Status)
	}
	if _, err := env.svc.Create(requestContext(), env.versionID, "clarification", strings.Repeat("a", 121), 42); err == nil {
		t.Fatal("oversized title accepted")
	}
}

func TestConversationListAndGet(t *testing.T) {
	env := newConversationEnv(t)
	first, err := env.svc.Create(requestContext(), env.versionID, "clarification", "First", 42)
	if err != nil {
		t.Fatalf("Create first: %v", err)
	}
	second, err := env.svc.Create(requestContext(), env.versionID, "clarification", "Second", 42)
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}
	// A design conversation may exist from another phase; the list filter must
	// still handle it.
	designID := int64(7001)
	env.repo.conversations[designID] = version.Conversation{
		ID: designID, VersionID: env.versionID, Kind: "design", Title: "Design", Status: "active",
		CreatedAt: testTime, UpdatedAt: testTime,
	}

	items, hasMore, err := env.svc.List(requestContext(), env.versionID, "", 10, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if hasMore || len(items) != 3 || items[0].ID != designID || items[2].ID != first.ID {
		t.Fatalf("list = %#v, hasMore = %v", items, hasMore)
	}

	clarification, _, err := env.svc.List(requestContext(), env.versionID, "clarification", 1, nil)
	if err != nil || len(clarification) != 1 || clarification[0].ID != second.ID {
		t.Fatalf("clarification = %#v, %v", clarification, err)
	}
	next, hasMore, err := env.svc.List(requestContext(), env.versionID, "clarification", 1, &second.ID)
	if err != nil || hasMore || len(next) != 1 || next[0].ID != first.ID {
		t.Fatalf("next = %#v, hasMore = %v, %v", next, hasMore, err)
	}
	if _, _, err := env.svc.List(requestContext(), env.versionID, "bogus", 10, nil); err == nil {
		t.Fatal("invalid kind filter accepted")
	}

	detail, err := env.svc.Get(requestContext(), env.versionID, second.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if detail.ID != second.ID || detail.Title != "Second" {
		t.Fatalf("detail = %#v", detail)
	}
	if _, err := env.svc.Get(requestContext(), env.versionID+1, second.ID); err == nil {
		t.Fatal("cross-version conversation returned")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("status = %d", apiError(t, err).Status)
	}
}

func TestPostUserMessageRegistersAttachmentsAndEnqueuesTasks(t *testing.T) {
	env := newConversationEnv(t)
	conversation, err := env.svc.Create(requestContext(), env.versionID, "clarification", "Chat", 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	parts := []version.MessagePart{
		{Type: "text", Text: "Please check the spec"},
		{Type: "attachment", SHA256: strings.Repeat("c", 64), Name: "spec.md", MIME: "text/markdown", Size: 128},
	}
	message := postUserMessage(t, env, conversation.ID, parts, "client-1")
	if message.ID <= 0 || message.ConversationID != conversation.ID || message.Role != "user" {
		t.Fatalf("message = %#v", message)
	}
	if len(message.Parts) != 2 || message.Parts[1].SHA256 != strings.Repeat("c", 64) {
		t.Fatalf("parts = %#v", message.Parts)
	}
	storedMessage := env.repo.messages[conversation.ID][0]
	if storedMessage.ClientMessageID == nil || *storedMessage.ClientMessageID != "client-1" {
		t.Fatalf("client id = %#v", storedMessage.ClientMessageID)
	}

	if len(env.registrar.calls) != 1 {
		t.Fatalf("registrar calls = %#v", env.registrar.calls)
	}
	call := env.registrar.calls[0]
	if call.versionID != env.versionID || call.messageID != message.ID || call.actorID != 42 || call.part.Name != "spec.md" {
		t.Fatalf("registrar call = %#v", call)
	}

	replyKey := "reply:" + idString(message.ID)
	if _, ok := env.queue.find("chat.turn", replyKey); !ok {
		t.Fatalf("reply task missing: %#v", env.queue.tasks)
	}
	curateKey := "curate:" + idString(message.ID)
	if _, ok := env.queue.find("requirement.curate", curateKey); !ok {
		t.Fatalf("curate task missing: %#v", env.queue.tasks)
	}

	if len(env.events.events) != 2 {
		t.Fatalf("events = %#v", env.events.events)
	}
	messageEvent := env.events.events[1]
	if messageEvent.Type != "conversation.message" {
		t.Fatalf("event = %#v", messageEvent)
	}
	if messageEvent.Payload["id"] != idString(message.ID) || messageEvent.Payload["role"] != "user" {
		t.Fatalf("payload = %#v", messageEvent.Payload)
	}
	if messageEvent.Scope["conversation_id"] != idString(conversation.ID) {
		t.Fatalf("scope = %#v", messageEvent.Scope)
	}

	versionView, err := env.versionEnv.svc.Get(requestContext(), env.versionID)
	if err != nil || versionView.Status != version.StatusRunning {
		t.Fatalf("version status = %q, %v", versionView.Status, err)
	}

	stored, err := env.svc.Get(requestContext(), env.versionID, conversation.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.MessageCount != 1 || stored.LastMessageAt == nil {
		t.Fatalf("conversation = %#v", stored)
	}
}

func TestPostUserMessageIsIdempotent(t *testing.T) {
	env := newConversationEnv(t)
	conversation, err := env.svc.Create(requestContext(), env.versionID, "clarification", "Chat", 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	parts := []version.MessagePart{{Type: "text", Text: "hello"}}
	first := postUserMessage(t, env, conversation.ID, parts, "same-id")
	eventsAfterFirst := len(env.events.events)
	registrarAfterFirst := len(env.registrar.calls)

	second, err := env.svc.PostUserMessage(requestContext(), env.versionID, conversation.ID, parts, "same-id", 42)
	if err != nil {
		t.Fatalf("second PostUserMessage: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("idempotent post created a new message: %d vs %d", second.ID, first.ID)
	}
	if len(env.repo.messages[conversation.ID]) != 1 {
		t.Fatalf("messages = %d", len(env.repo.messages[conversation.ID]))
	}
	if len(env.events.events) != eventsAfterFirst {
		t.Fatal("idempotent post emitted an event")
	}
	if len(env.registrar.calls) != registrarAfterFirst {
		t.Fatal("idempotent post registered attachments again")
	}

	// A race where the insert loses the unique constraint returns the winner.
	storedWinner := env.repo.messages[conversation.ID][0]
	env.repo.forceExisting = &storedWinner
	third, err := env.svc.PostUserMessage(requestContext(), env.versionID, conversation.ID, parts, "other-id", 42)
	if err != nil {
		t.Fatalf("racing PostUserMessage: %v", err)
	}
	if third.ID != first.ID {
		t.Fatalf("race winner = %d", third.ID)
	}
	env.repo.forceExisting = nil

	if _, err := env.svc.PostUserMessage(requestContext(), env.versionID, conversation.ID, parts, strings.Repeat("x", 65), 42); err == nil {
		t.Fatal("oversized client_message_id accepted")
	}
	if _, err := env.svc.PostUserMessage(requestContext(), env.versionID, conversation.ID, []version.MessagePart{{Type: "text", Text: "  "}}, "", 42); err == nil {
		t.Fatal("blank user message accepted")
	}
	if _, err := env.svc.PostUserMessage(requestContext(), env.versionID+1, conversation.ID, parts, "", 42); err == nil {
		t.Fatal("cross-version message accepted")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("status = %d", apiError(t, err).Status)
	}
}

func TestAppendAssistantMessagePublishesAndProjects(t *testing.T) {
	env := newConversationEnv(t)
	conversation, err := env.svc.Create(requestContext(), env.versionID, "clarification", "Chat", 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	parts := []version.MessagePart{
		{Type: "text", Text: "I drafted a requirement."},
		{Type: "requirement_card", RequirementID: 3001, Title: "Coupons", Status: "draft", Revision: 1},
		{Type: "rollback_proposal", RequirementID: 3001, Reason: "scope changed"},
	}
	message, err := env.svc.AppendAssistantMessage(requestContext(), conversation.ID, parts, 555)
	if err != nil {
		t.Fatalf("AppendAssistantMessage: %v", err)
	}
	if message.Role != "assistant" {
		t.Fatalf("message = %#v", message)
	}
	storedAssistant := env.repo.messages[conversation.ID][0]
	if storedAssistant.AgentRunID == nil || *storedAssistant.AgentRunID != 555 {
		t.Fatalf("agent run id = %#v", storedAssistant.AgentRunID)
	}
	if len(message.Parts) != 3 {
		t.Fatalf("parts = %#v", message.Parts)
	}
	if len(env.events.events) != 2 || env.events.events[1].Type != "conversation.message" {
		t.Fatalf("events = %#v", env.events.events)
	}
	if len(env.queue.tasks) != 1 {
		t.Fatalf("assistant message must not enqueue chat tasks: %#v", env.queue.tasks)
	}
	versionView, err := env.versionEnv.svc.Get(requestContext(), env.versionID)
	if err != nil || versionView.Status != version.StatusRunning {
		t.Fatalf("version status = %q, %v", versionView.Status, err)
	}

	if _, err := env.svc.AppendAssistantMessage(requestContext(), conversation.ID, []version.MessagePart{
		{Type: "requirement_card", RequirementID: 0, Title: "T", Status: "draft", Revision: 1},
	}, 1); err == nil {
		t.Fatal("invalid requirement card accepted")
	}
	if _, err := env.svc.AppendAssistantMessage(requestContext(), 9999, parts, 1); err == nil {
		t.Fatal("missing conversation accepted")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("status = %d", apiError(t, err).Status)
	}
}

func TestConversationMessagesPagination(t *testing.T) {
	env := newConversationEnv(t)
	conversation, err := env.svc.Create(requestContext(), env.versionID, "clarification", "Chat", 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ids := make([]int64, 0, 5)
	for i := 0; i < 5; i++ {
		message := postUserMessage(t, env, conversation.ID, []version.MessagePart{{Type: "text", Text: "m"}}, "")
		ids = append(ids, message.ID)
	}

	messages, hasMore, err := env.svc.Messages(requestContext(), env.versionID, conversation.ID, 0, 2)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if !hasMore || len(messages) != 2 || messages[0].ID != ids[0] || messages[1].ID != ids[1] {
		t.Fatalf("messages = %#v, hasMore = %v", messages, hasMore)
	}
	next, hasMore, err := env.svc.Messages(requestContext(), env.versionID, conversation.ID, messages[1].ID, 10)
	if err != nil || hasMore || len(next) != 3 || next[2].ID != ids[4] {
		t.Fatalf("next = %#v, hasMore = %v, %v", next, hasMore, err)
	}

	defaulted, _, err := env.svc.Messages(requestContext(), env.versionID, conversation.ID, 0, 0)
	if err != nil || len(defaulted) != 5 {
		t.Fatalf("default limit = %d, %v", len(defaulted), err)
	}
	if _, _, err := env.svc.Messages(requestContext(), env.versionID+1, conversation.ID, 0, 10); err == nil {
		t.Fatal("cross-version messages returned")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("status = %d", apiError(t, err).Status)
	}
}

func TestConversationSummaryVersioningAndPinnedFacts(t *testing.T) {
	env := newConversationEnv(t)
	conversation, err := env.svc.Create(requestContext(), env.versionID, "clarification", "Chat", 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := env.svc.UpdateSummary(requestContext(), conversation.ID, "First summary", []string{"fact one", "fact two"}); err != nil {
		t.Fatalf("UpdateSummary: %v", err)
	}
	summary, err := env.svc.Summary(requestContext(), conversation.ID)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.Version != 1 || summary.Summary != "First summary" || len(summary.PinnedFacts) != 2 {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.TokenEstimate <= 0 {
		t.Fatalf("token estimate = %d", summary.TokenEstimate)
	}

	if err := env.svc.UpdateSummary(requestContext(), conversation.ID, "Second summary", []string{"fact one"}); err != nil {
		t.Fatalf("second UpdateSummary: %v", err)
	}
	summary, err = env.svc.Summary(requestContext(), conversation.ID)
	if err != nil || summary.Version != 2 || summary.Summary != "Second summary" {
		t.Fatalf("summary = %#v, %v", summary, err)
	}

	summaryEvents := make([]int, 0, 2)
	for _, event := range env.events.events {
		if event.Type == "conversation.summary_updated" {
			if event.Payload["conversation_id"] != idString(conversation.ID) {
				t.Fatalf("summary event = %#v", event)
			}
			summaryEvents = append(summaryEvents, event.Payload["version"].(int))
		}
	}
	if len(summaryEvents) != 2 || summaryEvents[0] != 1 || summaryEvents[1] != 2 {
		t.Fatalf("summary events = %#v", summaryEvents)
	}

	if err := env.svc.UpdateSummary(requestContext(), conversation.ID, "summary", []string{strings.Repeat("f", 501)}); err == nil {
		t.Fatal("oversized pinned fact accepted")
	}
	tooMany := make([]string, 21)
	for i := range tooMany {
		tooMany[i] = "fact"
	}
	if err := env.svc.UpdateSummary(requestContext(), conversation.ID, "summary", tooMany); err == nil {
		t.Fatal("too many pinned facts accepted")
	}
	if err := env.svc.UpdateSummary(requestContext(), 9999, "summary", nil); err == nil {
		t.Fatal("missing conversation summary accepted")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("status = %d", apiError(t, err).Status)
	}
}

func TestAppendAssistantMessageWithReply(t *testing.T) {
	env := newConversationEnv(t)
	conversation, err := env.svc.Create(requestContext(), env.versionID, "clarification", "Chat", 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	userMessage := postUserMessage(t, env, conversation.ID, []version.MessagePart{{Type: "text", Text: "question"}}, "")
	replyTo := userMessage.ID

	message, err := env.svc.AppendAssistantMessageWithReply(requestContext(), conversation.ID,
		[]version.MessagePart{{Type: "text", Text: "answer"}}, 555, &replyTo)
	if err != nil {
		t.Fatalf("AppendAssistantMessageWithReply: %v", err)
	}
	if message.Role != "assistant" {
		t.Fatalf("message = %#v", message)
	}
	stored := env.repo.messages[conversation.ID]
	last := stored[len(stored)-1]
	if last.ReplyToMessageID == nil || *last.ReplyToMessageID != userMessage.ID {
		t.Fatalf("reply_to_message_id = %#v", last.ReplyToMessageID)
	}

	invalid := int64(-1)
	if _, err := env.svc.AppendAssistantMessageWithReply(requestContext(), conversation.ID,
		[]version.MessagePart{{Type: "text", Text: "answer"}}, 1, &invalid); err == nil {
		t.Fatal("negative reply_to_message_id accepted")
	}
}

// keep unused imports stable while iterating
var _ = errors.Is
var _ = platform.Invalid
var _ = time.Time{}

func TestListReconcilableClampsLimit(t *testing.T) {
	env := newConversationEnv(t)
	env.repo.reconcilable = []version.ReconcilerConversation{{
		ID: 8001, VersionID: 101,
		Messages: []version.ReconcilerMessage{{ID: 1, Role: "assistant", AgentProfileKey: "clarification_host"}},
	}}

	items, err := env.svc.ListReconcilable(requestContext(), 500)
	if err != nil {
		t.Fatalf("ListReconcilable: %v", err)
	}
	if len(items) != 1 || items[0].ID != 8001 || len(items[0].Messages) != 1 || items[0].Messages[0].AgentProfileKey != "clarification_host" {
		t.Fatalf("items = %#v", items)
	}
	if env.repo.lastReconcilerLimit != 100 {
		t.Fatalf("limit = %d, want clamped 100", env.repo.lastReconcilerLimit)
	}
	if _, err := env.svc.ListReconcilable(requestContext(), -3); err != nil {
		t.Fatalf("negative limit: %v", err)
	}
	if env.repo.lastReconcilerLimit != 1 {
		t.Fatalf("negative limit = %d, want clamped 1", env.repo.lastReconcilerLimit)
	}
}
