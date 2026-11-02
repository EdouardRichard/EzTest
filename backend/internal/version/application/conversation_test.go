package versionapp_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
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
	conversations       map[int64]version.Conversation
	messages            map[int64][]version.Message
	messageParts        map[int64][]version.ConversationMessagePart
	summaries           map[int64][]version.ConversationSummary
	forceExisting       *version.Message
	reconcilable        []version.ReconcilerConversation
	lastReconcilerLimit int
}

func newFakeConversationRepo() *fakeConversationRepo {
	return &fakeConversationRepo{
		conversations: map[int64]version.Conversation{},
		messages:      map[int64][]version.Message{},
		messageParts:  map[int64][]version.ConversationMessagePart{},
		summaries:     map[int64][]version.ConversationSummary{},
	}
}

func (f *fakeConversationRepo) Create(_ context.Context, conversation version.Conversation) error {
	f.conversations[conversation.ID] = conversation
	return nil
}

func (f *fakeConversationRepo) UpdateModelOverride(_ context.Context, versionID, conversationID int64, modelOverride *string, updatedAt time.Time) (version.Conversation, error) {
	conversation, ok := f.conversations[conversationID]
	if !ok || conversation.VersionID != versionID {
		return version.Conversation{}, version.ErrConversationNotFound
	}
	conversation.ModelOverride = modelOverride
	conversation.UpdatedAt = updatedAt
	f.conversations[conversationID] = conversation
	return conversation, nil
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

func (f *fakeConversationRepo) MessageParts(_ context.Context, versionID, conversationID, messageID int64) ([]version.ConversationMessagePart, error) {
	conversation, ok := f.conversations[conversationID]
	if !ok || conversation.VersionID != versionID {
		return nil, version.ErrConversationNotFound
	}
	found := false
	for _, message := range f.messages[conversationID] {
		if message.ID == messageID {
			found = true
			break
		}
	}
	if !found {
		return nil, version.ErrMessageNotFound
	}
	parts := append([]version.ConversationMessagePart(nil), f.messageParts[messageID]...)
	sort.Slice(parts, func(i, j int) bool { return parts[i].Seq < parts[j].Seq })
	return parts, nil
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

func (f *fakeConversationRepo) ListReconcilable(_ context.Context, limit int) ([]version.ReconcilerConversation, error) {
	f.lastReconcilerLimit = limit
	return f.reconcilable, nil
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
	versionID      int64
	conversationID int64
	messageID      int64
	parts          []version.MessagePart
	attachmentIDs  []int64
	actorID        int64
}

type fakeRegistrar struct {
	prepares []registrarCall
	links    []registrarCall
	err      error
	id       int64
}

// PrepareUserMessageParts is the conversion/pre-registration hook: the fake
// keeps the parts unchanged and hands back one attachment row for each
// attachment part so conversation tests stay focused on message persistence.
func (f *fakeRegistrar) PrepareUserMessageParts(_ context.Context, versionID, conversationID int64, parts []version.MessagePart, actorID int64) ([]version.MessagePart, []int64, error) {
	if f.err != nil {
		return nil, nil, f.err
	}
	ids := make([]int64, 0)
	for _, part := range parts {
		if part.Type != version.MessagePartAttachment {
			continue
		}
		f.id++
		ids = append(ids, f.id)
	}
	f.prepares = append(f.prepares, registrarCall{
		versionID: versionID, conversationID: conversationID, parts: parts, actorID: actorID,
	})
	return parts, ids, nil
}

func (f *fakeRegistrar) LinkFromMessage(_ context.Context, versionID, messageID int64, attachmentIDs []int64) error {
	f.links = append(f.links, registrarCall{
		versionID: versionID, messageID: messageID, attachmentIDs: attachmentIDs,
	})
	return nil
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

	design, err := env.svc.Create(requestContext(), env.versionID, "design", "", 42)
	if err != nil {
		t.Fatalf("Create design: %v", err)
	}
	if design.Kind != "design" || design.Title != "用例设计 2026-10-31 12:00" {
		t.Fatalf("design = %#v", design)
	}
	designTask, ok := env.queue.find("chat.turn", "opening:"+idString(design.ID))
	if !ok || designTask.Team != "design" {
		t.Fatalf("design opening task = %#v", designTask)
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

func TestCreateDesignConversationAllowsDesignAndEnqueuesDesignTeam(t *testing.T) {
	env := newConversationEnv(t)

	view, err := env.svc.Create(requestContext(), env.versionID, "design", "", 42)
	if err != nil {
		t.Fatalf("Create design: %v", err)
	}
	if view.Kind != version.ConversationDesign || view.Status != version.ConversationActive {
		t.Fatalf("view = %#v", view)
	}
	if view.Title != "用例设计 2026-10-31 12:00" {
		t.Fatalf("title = %q", view.Title)
	}
	task, ok := env.queue.find("chat.turn", "opening:"+idString(view.ID))
	if !ok {
		t.Fatalf("opening task missing: %#v", env.queue.tasks)
	}
	if task.Team != "design" || task.VersionID != env.versionID ||
		task.Payload["conversation_id"] != idString(view.ID) || task.Payload["trigger"] != "opening" {
		t.Fatalf("task = %#v", task)
	}
	if _, ok := env.queue.find("requirement.curate", "curate:"+idString(view.ID)); ok {
		t.Fatal("design conversation creation enqueued requirement.curate")
	}
	if len(env.events.events) != 1 || env.events.events[0].Payload["kind"] != "design" {
		t.Fatalf("events = %#v", env.events.events)
	}
	if versionView, err := env.versionEnv.svc.Get(requestContext(), env.versionID); err != nil ||
		versionView.Status != version.StatusInitial {
		t.Fatalf("empty design conversation projected version to %q (%v)", versionView.Status, err)
	}
}

func TestDesignConversationUserMessageSkipsCurateAndUsesDesignTeam(t *testing.T) {
	env := newConversationEnv(t)
	conversation, err := env.svc.Create(requestContext(), env.versionID, "design", "Design chat", 42)
	if err != nil {
		t.Fatalf("Create design: %v", err)
	}
	message := postUserMessage(t, env, conversation.ID, []version.MessagePart{{Type: "text", Text: "请新增异常登录用例"}}, "design-1")

	replyTask, ok := env.queue.find("chat.turn", "reply:"+idString(message.ID))
	if !ok || replyTask.Team != "design" {
		t.Fatalf("design reply task = %#v, %v", replyTask, ok)
	}
	if _, ok := env.queue.find("requirement.curate", "curate:"+idString(message.ID)); ok {
		t.Fatal("design conversation enqueued requirement.curate")
	}
	if versionView, err := env.versionEnv.svc.Get(requestContext(), env.versionID); err != nil ||
		versionView.Status != version.StatusRunning {
		t.Fatalf("design message did not project running: %q (%v)", versionView.Status, err)
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

	if len(env.registrar.prepares) != 1 || len(env.registrar.links) != 1 {
		t.Fatalf("registrar prepares = %#v, links = %#v", env.registrar.prepares, env.registrar.links)
	}
	prepare := env.registrar.prepares[0]
	if prepare.versionID != env.versionID || prepare.conversationID != conversation.ID || prepare.actorID != 42 ||
		len(prepare.parts) != 2 || prepare.parts[1].Name != "spec.md" {
		t.Fatalf("registrar prepare = %#v", prepare)
	}
	link := env.registrar.links[0]
	if link.versionID != env.versionID || link.messageID != message.ID || len(link.attachmentIDs) != 1 {
		t.Fatalf("registrar link = %#v", link)
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
	preparesAfterFirst := len(env.registrar.prepares)
	linksAfterFirst := len(env.registrar.links)

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
	if len(env.registrar.prepares) != preparesAfterFirst || len(env.registrar.links) != linksAfterFirst {
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

func TestConversationMessagePartsScopedAndOrdered(t *testing.T) {
	env := newConversationEnv(t)
	conversation, err := env.svc.Create(requestContext(), env.versionID, "clarification", "Chat", 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	message := postUserMessage(t, env, conversation.ID, []version.MessagePart{{Type: "text", Text: "hi"}}, "")
	env.repo.messageParts[message.ID] = []version.ConversationMessagePart{
		{Seq: 2, Type: "job_card", Payload: json.RawMessage(`{"job_id":"7"}`), CreatedAt: testTime},
		{Seq: 1, Type: "text", Payload: json.RawMessage(`{"text":"hi"}`), CreatedAt: testTime},
	}

	parts, err := env.svc.MessageParts(requestContext(), env.versionID, conversation.ID, message.ID)
	if err != nil {
		t.Fatalf("MessageParts: %v", err)
	}
	if len(parts) != 2 || parts[0].Seq != 1 || parts[1].Seq != 2 ||
		parts[0].Type != "text" || parts[1].Type != "job_card" {
		t.Fatalf("parts = %#v", parts)
	}
	if string(parts[1].Payload) != `{"job_id":"7"}` || !parts[0].CreatedAt.Equal(testTime) {
		t.Fatalf("part payload/time = %#v", parts[1])
	}

	// Cross-version and foreign-message reads are 404; a zero mid is a 400.
	notFound := []struct {
		name      string
		versionID int64
		mid       int64
	}{
		{"foreign version", env.versionID + 1, message.ID},
		{"unknown message", env.versionID, message.ID + 999},
	}
	for _, testCase := range notFound {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := env.svc.MessageParts(requestContext(), testCase.versionID, conversation.ID, testCase.mid); err == nil {
				t.Fatal("out-of-scope message parts returned")
			} else if apiError(t, err).Status != http.StatusNotFound {
				t.Fatalf("status = %d", apiError(t, err).Status)
			}
		})
	}
	if _, err := env.svc.MessageParts(requestContext(), env.versionID, conversation.ID, 0); err == nil {
		t.Fatal("zero message id accepted")
	} else if apiError(t, err).Status != http.StatusBadRequest {
		t.Fatalf("status = %d", apiError(t, err).Status)
	}

	// A scoped message with no appended parts is an empty list, not a 404.
	other := postUserMessage(t, env, conversation.ID, []version.MessagePart{{Type: "text", Text: "no parts"}}, "")
	empty, err := env.svc.MessageParts(requestContext(), env.versionID, conversation.ID, other.ID)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty parts = %#v, %v", empty, err)
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

// TestPostUserMessageStoresImagePart covers R5/§8.3: an image the client sends
// is persisted as an `image` part, not an attachment part or extracted text.
func TestPostUserMessageStoresImagePart(t *testing.T) {
	env := newConversationEnv(t)
	conversation, err := env.svc.Create(requestContext(), env.versionID, "clarification", "Chat", 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sha := strings.Repeat("e", 64)
	parts := []version.MessagePart{
		{Type: version.MessagePartText, Text: "这张截图说明了什么？"},
		{Type: version.MessagePartImage, MIME: "image/png", DataRef: sha, Width: 800, Height: 600},
	}
	message := postUserMessage(t, env, conversation.ID, parts, "image-1")
	if len(message.Parts) != 2 || message.Parts[1].Type != version.MessagePartImage ||
		message.Parts[1].DataRef != sha || message.Parts[1].Width != 800 || message.Parts[1].Height != 600 {
		t.Fatalf("parts = %#v", message.Parts)
	}
	if len(env.registrar.links) != 0 {
		t.Fatalf("image part registered as a text attachment: %#v", env.registrar.links)
	}
	for _, prepare := range env.registrar.prepares {
		for _, part := range prepare.parts {
			if part.Type == version.MessagePartAttachment {
				t.Fatalf("image part sent to attachment registration: %#v", part)
			}
		}
	}
	raw, err := json.Marshal(message.Parts[1])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["type"] != "image" || payload["data_ref"] != sha || payload["mime"] != "image/png" {
		t.Fatalf("payload = %#v", payload)
	}
	stored := env.repo.messages[conversation.ID][0]
	if stored.Parts[1].Type != version.MessagePartImage {
		t.Fatalf("stored = %#v", stored.Parts)
	}
}

// ---------- image end-to-end (real attachment service) ----------

// imageConversationEnv wires the conversation service to the real attachment
// service so attachment parts are converted/probed end to end.
type imageConversationEnv struct {
	svc          *versionapp.ConversationService
	repo         *fakeConversationRepo
	attachments  *attachmentEnv
	versionEnv   *testEnv
	versionID    int64
	conversation versionapp.ConversationView
}

func newImageConversationEnv(t *testing.T) *imageConversationEnv {
	t.Helper()
	base := newTestEnv(t)
	created := createVersion(t, base, "Image conversation version")
	attachEnv := newAttachmentEnv(t)
	attachEnv.versionID = created.ID
	repo := newFakeConversationRepo()
	svc := versionapp.NewConversationService(
		repo, &fakeQueue{}, attachEnv.svc, base.svc, fixedClock{at: testTime}, &seqIDs{}, &fakePublisher{})
	conversation, err := svc.Create(requestContext(), created.ID, "clarification", "Chat", 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return &imageConversationEnv{
		svc: svc, repo: repo, attachments: attachEnv, versionEnv: base,
		versionID: created.ID, conversation: conversation,
	}
}

// pngImageBytes builds the smallest header the image prober needs: a PNG
// signature plus an IHDR chunk carrying the pixel dimensions.
func pngImageBytes(width, height int) []byte {
	data := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	header := make([]byte, 25)
	binary.BigEndian.PutUint32(header[0:4], 13)
	copy(header[4:8], "IHDR")
	binary.BigEndian.PutUint32(header[8:12], uint32(width))
	binary.BigEndian.PutUint32(header[12:16], uint32(height))
	header[16] = 8 // bit depth
	header[17] = 2 // truecolor
	return append(data, header...)
}

// TestImageAttachmentBecomesImagePart covers the end-to-end server conversion:
// an uploaded image the Composer sends as an attachment part is stored as a
// vision-ready image part. The attachment row still exists (extraction skipped,
// no text) as the content-ledger anchor that keeps the bytes alive.
func TestImageAttachmentBecomesImagePart(t *testing.T) {
	env := newImageConversationEnv(t)
	data := pngImageBytes(800, 600)
	sha := strings.Repeat("e", 64)
	env.attachments.store.put(sha, data, "image/png")

	parts := []version.MessagePart{
		{Type: version.MessagePartText, Text: "这张截图说明了什么？"},
		{Type: version.MessagePartAttachment, SHA256: sha, Name: "shot.png", MIME: "image/png", Size: int64(len(data))},
	}
	message, err := env.svc.PostUserMessage(
		requestContext(), env.versionID, env.conversation.ID, parts, "image-e2e", 42)
	if err != nil {
		t.Fatalf("PostUserMessage: %v", err)
	}
	if len(message.Parts) != 2 {
		t.Fatalf("parts = %#v", message.Parts)
	}
	image := message.Parts[1]
	if image.Type != version.MessagePartImage || image.MIME != "image/png" ||
		image.DataRef != sha || image.Width != 800 || image.Height != 600 {
		t.Fatalf("image part = %#v", image)
	}
	stored := env.repo.messages[env.conversation.ID][0]
	if stored.Parts[1].Type != version.MessagePartImage || stored.Parts[1].DataRef != sha {
		t.Fatalf("stored parts = %#v", stored.Parts)
	}
	// The attachment row is the content-ledger anchor (it adds the content ref
	// the storage GC needs); images are extraction-skipped because there is no
	// text, while the message itself carries the image part.
	if len(env.attachments.repo.attachments) != 1 {
		t.Fatalf("attachment rows = %#v", env.attachments.repo.attachments)
	}
	for _, row := range env.attachments.repo.attachments {
		if row.ExtractionStatus != version.AttachmentSkipped || row.MIME != "image/png" {
			t.Fatalf("image attachment row = %#v", row)
		}
	}
	if len(env.attachments.store.refs) != 1 || env.attachments.store.refs[0].ObjectID != sha {
		t.Fatalf("image content refs = %#v", env.attachments.store.refs)
	}
	if len(env.repo.messages[env.conversation.ID]) != 1 {
		t.Fatalf("messages = %#v", env.repo.messages[env.conversation.ID])
	}
}

// TestPostUserMessageBlocksPendingAttachment covers the §8.5 guard: a message
// that references an attachment already registered in the conversation while
// its extraction is still running returns the typed 409 attachment_pending
// ("解析中，可重试") instead of being accepted.
func TestPostUserMessageBlocksPendingAttachment(t *testing.T) {
	env := newImageConversationEnv(t)
	env.attachments.repo.conversationMessages = map[int64]int64{7001: env.conversation.ID}
	env.attachments.repo.conversations = map[int64]int64{env.conversation.ID: env.versionID}
	data := []byte("# 需求")
	sha := strings.Repeat("a", 64)
	env.attachments.store.put(sha, data, "text/markdown")
	pending, err := env.attachments.svc.RegisterFromMessage(requestContext(), env.versionID,
		env.conversation.ID, 7001, attachmentPart(sha, "spec.md", "text/markdown", int64(len(data))), 42)
	if err != nil {
		t.Fatalf("RegisterFromMessage: %v", err)
	}
	if pending.ExtractionStatus != version.AttachmentPending {
		t.Fatalf("pending = %#v", pending)
	}

	referencing := []version.MessagePart{
		{Type: version.MessagePartText, Text: "请再看一下附件"},
		{Type: version.MessagePartAttachment, SHA256: sha, Name: "spec.md", MIME: "text/markdown", Size: int64(len(data))},
	}
	_, err = env.svc.PostUserMessage(requestContext(), env.versionID, env.conversation.ID, referencing, "", 42)
	if err == nil {
		t.Fatal("message referencing a pending attachment was accepted")
	}
	apiErr := apiError(t, err)
	if apiErr.Status != http.StatusConflict || apiErr.Code != "attachment_pending" {
		t.Fatalf("error = %#v, want 409 attachment_pending", apiErr)
	}
	if !apiErr.Retryable || !strings.Contains(apiErr.Message, "解析中") {
		t.Fatalf("error = %#v, want a retryable parsing message", apiErr)
	}
	if len(env.repo.messages[env.conversation.ID]) != 0 {
		t.Fatalf("blocked message was stored: %#v", env.repo.messages[env.conversation.ID])
	}

	// Once the extraction is done the same reference is accepted.
	env.attachments.extractor.text = "done text"
	if _, err := env.attachments.svc.Extract(requestContext(), pending.ID); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	message, err := env.svc.PostUserMessage(
		requestContext(), env.versionID, env.conversation.ID, referencing, "", 42)
	if err != nil {
		t.Fatalf("PostUserMessage after extraction: %v", err)
	}
	if len(message.Parts) != 2 || message.Parts[1].Type != version.MessagePartAttachment {
		t.Fatalf("parts = %#v", message.Parts)
	}
}

// TestPostUserMessageBlocksNewAttachmentUntilExtractionDone covers the normal
// Composer flow: the content is uploaded and posted immediately, so no
// attachment row exists before the message insert. The preflight must register
// the row first, observe that it is still parsing, return the typed 409 and
// leave the message uninserted. The retry after extraction must insert the
// message, link the same row and not extract/register it twice.
func TestPostUserMessageBlocksNewAttachmentUntilExtractionDone(t *testing.T) {
	env := newImageConversationEnv(t)
	data := []byte("# 需求")
	sha := strings.Repeat("d", 64)
	env.attachments.store.put(sha, data, "text/markdown")
	parts := []version.MessagePart{
		{Type: version.MessagePartText, Text: "请读附件"},
		{Type: version.MessagePartAttachment, SHA256: sha, Name: "spec.md", MIME: "text/markdown", Size: int64(len(data))},
	}

	_, err := env.svc.PostUserMessage(
		requestContext(), env.versionID, env.conversation.ID, parts, "new-attachment-1", 42)
	if err == nil {
		t.Fatal("message referencing a brand-new attachment was accepted while extraction is pending")
	}
	apiErr := apiError(t, err)
	if apiErr.Status != http.StatusConflict || apiErr.Code != "attachment_pending" || !apiErr.Retryable {
		t.Fatalf("error = %#v, want a retryable 409 attachment_pending", apiErr)
	}
	if !strings.Contains(apiErr.Message, "解析中") {
		t.Fatalf("error message = %q, want the parsing hint", apiErr.Message)
	}
	if len(env.repo.messages[env.conversation.ID]) != 0 {
		t.Fatalf("blocked message was stored: %#v", env.repo.messages[env.conversation.ID])
	}
	if len(env.attachments.repo.attachments) != 1 {
		t.Fatalf("pre-registered attachment rows = %#v", env.attachments.repo.attachments)
	}
	var pending version.Attachment
	for _, row := range env.attachments.repo.attachments {
		pending = row
	}
	if pending.ConversationMessageID != nil {
		t.Fatalf("attachment row was linked before the message insert: %#v", pending)
	}
	if pending.ExtractionStatus != version.AttachmentPending {
		t.Fatalf("status = %q, want pending", pending.ExtractionStatus)
	}
	if _, ok := env.attachments.queue.find("attachment.ingest", "attachment:"+idString(pending.ID)); !ok {
		t.Fatalf("pre-registered attachment did not enqueue extraction: %#v", env.attachments.queue.tasks)
	}

	// A retry while the row is still pending is rejected again, still leaving
	// the message uninserted and the row single.
	if _, err := env.svc.PostUserMessage(
		requestContext(), env.versionID, env.conversation.ID, parts, "new-attachment-1", 42); err == nil {
		t.Fatal("message accepted while the pre-registered attachment is still pending")
	}
	if len(env.repo.messages[env.conversation.ID]) != 0 || len(env.attachments.repo.attachments) != 1 {
		t.Fatalf("pending retry inserted rows: messages=%d rows=%d",
			len(env.repo.messages[env.conversation.ID]), len(env.attachments.repo.attachments))
	}

	env.attachments.extractor.text = "附件正文"
	if _, err := env.attachments.svc.Extract(requestContext(), pending.ID); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	message, err := env.svc.PostUserMessage(
		requestContext(), env.versionID, env.conversation.ID, parts, "new-attachment-2", 42)
	if err != nil {
		t.Fatalf("PostUserMessage after extraction: %v", err)
	}
	if len(message.Parts) != 2 || message.Parts[1].Type != version.MessagePartAttachment ||
		message.Parts[1].SHA256 != sha || message.Parts[1].Name != "spec.md" {
		t.Fatalf("parts = %#v", message.Parts)
	}
	if len(env.attachments.repo.attachments) != 1 {
		t.Fatalf("retry created duplicate attachment rows: %#v", env.attachments.repo.attachments)
	}
	linked := env.attachments.repo.attachments[pending.ID]
	if linked.ConversationMessageID == nil || *linked.ConversationMessageID != message.ID {
		t.Fatalf("existing row was not linked to the message: %#v (message %d)", linked, message.ID)
	}
	if linked.ExtractionStatus != version.AttachmentDone {
		t.Fatalf("linked row status = %q", linked.ExtractionStatus)
	}
	if env.attachments.extractor.calls != 1 || len(env.attachments.store.uploads) != 1 {
		t.Fatalf("retry extracted twice: calls=%d uploads=%d",
			env.attachments.extractor.calls, len(env.attachments.store.uploads))
	}
	if len(env.repo.messages[env.conversation.ID]) != 1 {
		t.Fatalf("messages = %d", len(env.repo.messages[env.conversation.ID]))
	}
}
