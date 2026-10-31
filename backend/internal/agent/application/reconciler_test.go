package agentapp_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

type fakeProjector struct {
	mu      sync.Mutex
	initial []int64
	marked  []int64
	err     error
}

func (p *fakeProjector) ListInitialVersionsWithContent(context.Context, int) ([]int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	return append([]int64(nil), p.initial...), nil
}

func (p *fakeProjector) MarkRunning(_ context.Context, versionID int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, existing := range p.initial {
		if existing == versionID {
			p.initial = append(p.initial[:i], p.initial[i+1:]...)
			p.marked = append(p.marked, versionID)
			return nil
		}
	}
	return nil
}

type fakeConversationReader struct {
	conversations []agentapp.ReconcilerConversation
	err           error
}

func (r *fakeConversationReader) ListConversations(context.Context, int) ([]agentapp.ReconcilerConversation, error) {
	return r.conversations, r.err
}

type fakePendingAttachments struct {
	attachments []agentapp.PendingAttachment
	err         error
}

func (r *fakePendingAttachments) ListPendingAttachments(context.Context, int) ([]agentapp.PendingAttachment, error) {
	return r.attachments, r.err
}

type fakeEnsurer struct {
	mu          sync.Mutex
	items       []agent.WorkItem
	ensureCalls []agent.NewWorkItem
	nextID      int64
	err         error
}

func (e *fakeEnsurer) Ensure(_ context.Context, in agent.NewWorkItem) (agent.WorkItem, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return agent.WorkItem{}, false, e.err
	}
	e.ensureCalls = append(e.ensureCalls, in)
	for _, existing := range e.items {
		if existing.VersionID == in.VersionID && existing.Type == in.Type && existing.DedupeKey == in.DedupeKey {
			return existing, false, nil
		}
	}
	e.nextID++
	payload, _ := in.EncodePayload()
	item := agent.WorkItem{
		ID:          e.nextID,
		VersionID:   in.VersionID,
		Team:        agent.Team(in.Team),
		Type:        in.Type,
		Status:      agent.WorkItemOpen,
		Priority:    in.Priority,
		DedupeKey:   in.DedupeKey,
		Payload:     payload,
		Attempts:    0,
		MaxAttempts: agent.DefaultMaxAttempts,
	}
	e.items = append(e.items, item)
	return item, true, nil
}

func (e *fakeEnsurer) ListByVersion(_ context.Context, versionID int64, _ int) ([]agent.WorkItem, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	list := make([]agent.WorkItem, 0)
	for _, item := range e.items {
		if item.VersionID == versionID {
			list = append(list, item)
		}
	}
	return list, nil
}

func (e *fakeEnsurer) ensured() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := make([]string, 0, len(e.ensureCalls))
	for _, call := range e.ensureCalls {
		result = append(result, call.Type+":"+call.DedupeKey)
	}
	return result
}

func (e *fakeEnsurer) addItem(versionID int64, workType, dedupeKey string, status agent.WorkItemStatus) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.nextID++
	e.items = append(e.items, agent.WorkItem{
		ID: e.nextID, VersionID: versionID, Team: agent.TeamClarification,
		Type: workType, Status: status, DedupeKey: dedupeKey,
	})
}

type reconcilerFixture struct {
	reconciler    *agentapp.Reconciler
	projector     *fakeProjector
	conversations *fakeConversationReader
	attachments   *fakePendingAttachments
	queue         *fakeEnsurer
	events        *fakePublisher
}

func newReconcilerFixture(t *testing.T) *reconcilerFixture {
	t.Helper()
	fixture := &reconcilerFixture{
		projector:     &fakeProjector{},
		conversations: &fakeConversationReader{},
		attachments:   &fakePendingAttachments{},
		queue:         &fakeEnsurer{},
		events:        &fakePublisher{},
	}
	fixture.reconciler = agentapp.NewReconciler(agentapp.ReconcilerDeps{
		Versions:      fixture.projector,
		Conversations: fixture.conversations,
		Attachments:   fixture.attachments,
		Queue:         fixture.queue,
		Events:        fixture.events,
		Clock:         &manualClock{now: runnerClockStart},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return fixture
}

func expectedKeys(keys ...string) map[string]bool {
	result := make(map[string]bool, len(keys))
	for _, key := range keys {
		result[key] = true
	}
	return result
}

func assertEnsured(t *testing.T, queue *fakeEnsurer, want ...string) {
	t.Helper()
	got := queue.ensured()
	gotSet := expectedKeys(got...)
	if len(gotSet) != len(want) {
		t.Fatalf("ensured = %v, want %v", got, want)
	}
	for _, key := range want {
		if !gotSet[key] {
			t.Fatalf("ensured = %v, missing %q", got, key)
		}
	}
}

func TestReconcilerProjectsInitialVersionOnce(t *testing.T) {
	fixture := newReconcilerFixture(t)
	fixture.projector.initial = []int64{10}

	if err := fixture.reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fixture.projector.marked) != 1 || fixture.projector.marked[0] != 10 {
		t.Fatalf("marked = %v", fixture.projector.marked)
	}

	if err := fixture.reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if len(fixture.projector.marked) != 1 {
		t.Fatalf("version projected twice: %v", fixture.projector.marked)
	}
}

func TestReconcilerEnsuresOpeningOnce(t *testing.T) {
	fixture := newReconcilerFixture(t)
	fixture.conversations.conversations = []agentapp.ReconcilerConversation{
		{ID: 20, VersionID: 10},
	}

	if err := fixture.reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	assertEnsured(t, fixture.queue, agentapp.WorkTypeChatTurn+":opening:20")

	if err := fixture.reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	assertEnsured(t, fixture.queue, agentapp.WorkTypeChatTurn+":opening:20")
}

func TestReconcilerEnsuresReplyAndCuratePerUserMessage(t *testing.T) {
	fixture := newReconcilerFixture(t)
	fixture.conversations.conversations = []agentapp.ReconcilerConversation{{
		ID:        20,
		VersionID: 10,
		Messages: []agentapp.ReconcilerMessage{
			{ID: 501, Role: "user"},
			{ID: 502, Role: "assistant", AgentProfileKey: agentapp.ProfileKeyClarificationHost},
			{ID: 503, Role: "user"},
			{ID: 504, Role: "assistant", AgentProfileKey: agentapp.ProfileKeyRequirementCurator},
			{ID: 505, Role: "user"},
		},
	}}

	if err := fixture.reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	assertEnsured(t, fixture.queue,
		agentapp.WorkTypeChatTurn+":reply:505",
		agentapp.WorkTypeRequirementCurate+":curate:505",
	)
}

func TestReconcilerSkipsMessagesWithExistingTasks(t *testing.T) {
	fixture := newReconcilerFixture(t)
	fixture.conversations.conversations = []agentapp.ReconcilerConversation{{
		ID: 20, VersionID: 10,
		Messages: []agentapp.ReconcilerMessage{{ID: 501, Role: "user"}},
	}}
	fixture.queue.addItem(10, agentapp.WorkTypeChatTurn, "reply:501", agent.WorkItemRunning)
	fixture.queue.addItem(10, agentapp.WorkTypeRequirementCurate, "curate:501", agent.WorkItemDone)

	if err := fixture.reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if ensured := fixture.queue.ensured(); len(ensured) != 0 {
		t.Fatalf("ensured = %v, want none while tasks are active/done", ensured)
	}
}

func TestReconcilerEnsuresOpeningWhenPreviousAttemptFailed(t *testing.T) {
	fixture := newReconcilerFixture(t)
	fixture.conversations.conversations = []agentapp.ReconcilerConversation{{ID: 20, VersionID: 10}}
	fixture.queue.addItem(10, agentapp.WorkTypeChatTurn, "opening:20", agent.WorkItemFailed)

	if err := fixture.reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// The dedupe unique key still makes Ensure idempotent; the reconciler asks
	// again because failed is not one of the blocking statuses.
	assertEnsured(t, fixture.queue, agentapp.WorkTypeChatTurn+":opening:20")
}

func TestReconcilerEnsuresPendingAttachmentsOnce(t *testing.T) {
	fixture := newReconcilerFixture(t)
	fixture.attachments.attachments = []agentapp.PendingAttachment{{
		ID: 9001, VersionID: 10, ConversationID: 20, MessageID: 501,
	}}

	if err := fixture.reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	assertEnsured(t, fixture.queue, agentapp.WorkTypeAttachmentIngest+":attachment:9001")

	if err := fixture.reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	assertEnsured(t, fixture.queue, agentapp.WorkTypeAttachmentIngest+":attachment:9001")
}

func TestReconcilerIsIdempotentAcrossRules(t *testing.T) {
	fixture := newReconcilerFixture(t)
	fixture.projector.initial = []int64{10}
	fixture.conversations.conversations = []agentapp.ReconcilerConversation{{
		ID: 20, VersionID: 10,
		Messages: []agentapp.ReconcilerMessage{{ID: 501, Role: "user"}},
	}}
	fixture.attachments.attachments = []agentapp.PendingAttachment{{
		ID: 9001, VersionID: 10, ConversationID: 20, MessageID: 501,
	}}

	if err := fixture.reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if calls := len(fixture.queue.ensured()); calls != 3 {
		t.Fatalf("first reconcile ensured %d tasks: %v", calls, fixture.queue.ensured())
	}
	if err := fixture.reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if calls := len(fixture.queue.ensured()); calls != 3 {
		t.Fatalf("second reconcile added tasks: %v", fixture.queue.ensured())
	}
}

func TestReconcilerEnsurePayloads(t *testing.T) {
	fixture := newReconcilerFixture(t)
	fixture.conversations.conversations = []agentapp.ReconcilerConversation{{
		ID: 20, VersionID: 10,
		Messages: []agentapp.ReconcilerMessage{{ID: 501, Role: "user"}},
	}}

	if err := fixture.reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	fixture.queue.mu.Lock()
	defer fixture.queue.mu.Unlock()
	for _, call := range fixture.queue.ensureCalls {
		if call.Team != string(agent.TeamClarification) {
			t.Fatalf("team = %q", call.Team)
		}
		if call.VersionID != 10 {
			t.Fatalf("version = %d", call.VersionID)
		}
		if call.Payload == nil {
			t.Fatalf("payload missing for %s %s", call.Type, call.DedupeKey)
		}
	}
}

func TestReconcilerDesignConversationsSkipCurateAndUseDesignTeam(t *testing.T) {
	fixture := newReconcilerFixture(t)
	fixture.conversations.conversations = []agentapp.ReconcilerConversation{
		{
			ID: 30, VersionID: 10, Kind: "design",
			Messages: []agentapp.ReconcilerMessage{{ID: 501, Role: "user"}},
		},
		{ID: 31, VersionID: 10, Kind: "design"},
		{
			ID: 20, VersionID: 10, Kind: "clarification",
			Messages: []agentapp.ReconcilerMessage{{ID: 601, Role: "user"}},
		},
	}

	if err := fixture.reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	fixture.queue.mu.Lock()
	defer fixture.queue.mu.Unlock()
	byKey := map[string]agent.NewWorkItem{}
	for _, call := range fixture.queue.ensureCalls {
		byKey[call.Type+":"+call.DedupeKey] = call
	}
	for _, key := range []string{
		agentapp.WorkTypeChatTurn + ":reply:601",
		agentapp.WorkTypeRequirementCurate + ":curate:601",
		agentapp.WorkTypeChatTurn + ":opening:31",
	} {
		if _, ok := byKey[key]; !ok {
			t.Fatalf("missing task %q: %v", key, byKey)
		}
	}
	// The design conversation had a user message; it gets a reply Host task but
	// no requirement.curate rule.
	if _, ok := byKey[agentapp.WorkTypeRequirementCurate+":curate:501"]; ok {
		t.Fatalf("design conversation created requirement.curate: %v", byKey)
	}
	if byKey[agentapp.WorkTypeChatTurn+":reply:501"].Team != string(agent.TeamDesign) {
		t.Fatalf("design reply team = %q", byKey[agentapp.WorkTypeChatTurn+":reply:501"].Team)
	}
	if byKey[agentapp.WorkTypeChatTurn+":opening:31"].Team != string(agent.TeamDesign) {
		t.Fatalf("design opening team = %q", byKey[agentapp.WorkTypeChatTurn+":opening:31"].Team)
	}
	if byKey[agentapp.WorkTypeChatTurn+":reply:601"].Team != string(agent.TeamClarification) {
		t.Fatalf("clarification reply team = %q", byKey[agentapp.WorkTypeChatTurn+":reply:601"].Team)
	}
}

var _ agentapp.VersionProjector = (*fakeProjector)(nil)
var _ agentapp.ConversationReader = (*fakeConversationReader)(nil)
var _ agentapp.PendingAttachmentReader = (*fakePendingAttachments)(nil)
var _ agentapp.WorkQueueEnsurer = (*fakeEnsurer)(nil)
var _ = time.Second
