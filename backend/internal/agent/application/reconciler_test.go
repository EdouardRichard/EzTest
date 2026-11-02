package agentapp_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
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

type fakeDesignReconciler struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (r *fakeDesignReconciler) Reconcile(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.err
}

func (r *fakeDesignReconciler) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// fakeP7Ensurer records P7 reconcile passes and can fail, so a test can prove
// the P7 hook runs every pass without swallowing other rule failures.
type fakeP7Ensurer struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (e *fakeP7Ensurer) ReconcileP7(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	return e.err
}

func (e *fakeP7Ensurer) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// orderTracker records the P4/P7 execution order in one shared slice.
type orderTracker struct {
	mu    sync.Mutex
	steps []string
}

func (o *orderTracker) add(step string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.steps = append(o.steps, step)
}

func (o *orderTracker) recorded() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.steps...)
}

// trackedP4Ensurer and trackedP7Ensurer share a tracker and record the order in
// which the reconciler invokes the P4 rules and the P7 hook.
type trackedP4Ensurer struct{ tracker *orderTracker }

func (e *trackedP4Ensurer) ScanFailures(context.Context, int64) error {
	e.tracker.add("p4.scan_failures")
	return nil
}

func (e *trackedP4Ensurer) EnsureReports(context.Context, int64) error {
	e.tracker.add("p4.ensure_reports")
	return nil
}

func (e *trackedP4Ensurer) EnsureClosures(context.Context, int64) error {
	e.tracker.add("p4.ensure_closures")
	return nil
}

func (e *trackedP4Ensurer) EnsureVersionClose(context.Context, int64) error {
	e.tracker.add("p4.ensure_version_close")
	return nil
}

type trackedP7Ensurer struct{ tracker *orderTracker }

func (e *trackedP7Ensurer) ReconcileP7(context.Context) error {
	e.tracker.add("p7")
	return nil
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
	design        *fakeDesignReconciler
	queue         *fakeEnsurer
	events        *fakePublisher
}

func newReconcilerFixture(t *testing.T) *reconcilerFixture {
	t.Helper()
	fixture := &reconcilerFixture{
		projector:     &fakeProjector{},
		conversations: &fakeConversationReader{},
		attachments:   &fakePendingAttachments{},
		design:        &fakeDesignReconciler{},
		queue:         &fakeEnsurer{},
		events:        &fakePublisher{},
	}
	fixture.reconciler = agentapp.NewReconciler(agentapp.ReconcilerDeps{
		Versions:      fixture.projector,
		Conversations: fixture.conversations,
		Attachments:   fixture.attachments,
		Design:        fixture.design,
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

func TestReconcilerCallsDesignReconcilerEveryPass(t *testing.T) {
	fixture := newReconcilerFixture(t)
	for i := 0; i < 2; i++ {
		if err := fixture.reconciler.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile %d: %v", i, err)
		}
	}
	if calls := fixture.design.callCount(); calls != 2 {
		t.Fatalf("design reconciler calls = %d, want one per pass", calls)
	}
}

func TestReconcilerJoinsDesignReconcilerErrorAndKeepsOtherRules(t *testing.T) {
	fixture := newReconcilerFixture(t)
	designErr := errors.New("design store down")
	fixture.design.err = designErr
	fixture.projector.initial = []int64{10}

	err := fixture.reconciler.Reconcile(context.Background())
	if err == nil {
		t.Fatal("design reconciler error was swallowed")
	}
	if !errors.Is(err, designErr) {
		t.Fatalf("joined error = %v, want design error", err)
	}
	if calls := fixture.design.callCount(); calls != 1 {
		t.Fatalf("design reconciler calls = %d", calls)
	}
	// Earlier rules still ran in the same pass (errors are aggregated).
	if len(fixture.projector.marked) != 1 || fixture.projector.marked[0] != 10 {
		t.Fatalf("version projection = %v", fixture.projector.marked)
	}
}

func TestReconcilerCallsP7EnsurerEveryPass(t *testing.T) {
	design := &fakeDesignReconciler{}
	p7 := &fakeP7Ensurer{}
	reconciler := agentapp.NewReconciler(agentapp.ReconcilerDeps{
		Versions: &fakeProjector{},
		Design:   design,
		P7:       p7,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	for i := 0; i < 2; i++ {
		if err := reconciler.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile %d: %v", i, err)
		}
	}
	if calls := p7.callCount(); calls != 2 {
		t.Fatalf("P7 ensurer calls = %d, want one per pass", calls)
	}
	if calls := design.callCount(); calls != 2 {
		t.Fatalf("design reconciler calls = %d, want the existing rules to keep running", calls)
	}
}

func TestReconcilerJoinsP7ErrorAndKeepsOtherRules(t *testing.T) {
	p7Err := errors.New("p7 ensure down")
	p7 := &fakeP7Ensurer{err: p7Err}
	projector := &fakeProjector{initial: []int64{10}}
	design := &fakeDesignReconciler{}
	p4 := &fakeP4Ensurer{failRule: map[string]error{}}
	versions := &fakeRunningVersions{ids: []int64{10}}
	reconciler := agentapp.NewReconciler(agentapp.ReconcilerDeps{
		Versions:        projector,
		Design:          design,
		P4:              p4,
		RunningVersions: versions,
		P7:              p7,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	err := reconciler.Reconcile(context.Background())
	if err == nil {
		t.Fatal("P7 ensurer error was swallowed")
	}
	if !errors.Is(err, p7Err) {
		t.Fatalf("joined error = %v, want P7 error", err)
	}
	if len(projector.marked) != 1 || projector.marked[0] != 10 {
		t.Fatalf("version projection = %v, want the earlier rules to run", projector.marked)
	}
	if calls := design.callCount(); calls != 1 {
		t.Fatalf("design reconciler calls = %d", calls)
	}
	if calls := len(p4.recorded()); calls != 4 {
		t.Fatalf("P4 rule calls = %d, want the P4 rules to run despite the P7 error", calls)
	}
	if calls := p7.callCount(); calls != 1 {
		t.Fatalf("P7 ensurer calls = %d", calls)
	}
}

func TestReconcilerRunsP7AfterP4(t *testing.T) {
	tracker := &orderTracker{}
	reconciler := agentapp.NewReconciler(agentapp.ReconcilerDeps{
		Versions:        &fakeProjector{},
		P4:              &trackedP4Ensurer{tracker: tracker},
		RunningVersions: &fakeRunningVersions{ids: []int64{10}},
		P7:              &trackedP7Ensurer{tracker: tracker},
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	want := []string{
		"p4.scan_failures", "p4.ensure_reports", "p4.ensure_closures",
		"p4.ensure_version_close", "p7",
	}
	if got := tracker.recorded(); !slices.Equal(got, want) {
		t.Fatalf("P4/P7 order = %v, want %v", got, want)
	}
}

func TestReconcilerIsNilSafeWithoutDesignReconciler(t *testing.T) {
	reconciler := agentapp.NewReconciler(agentapp.ReconcilerDeps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile without a design reconciler: %v", err)
	}
}

// fakeStageReader is the version-stage read the R49 stage-job recovery scans.
type fakeStageReader struct {
	mu     sync.Mutex
	states map[int64]version.StageState
	err    error
}

func (r *fakeStageReader) GetStage(_ context.Context, versionID int64) (version.StageState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return version.StageState{}, r.err
	}
	state, ok := r.states[versionID]
	if !ok {
		return version.StageState{}, version.ErrNotFound
	}
	return state, nil
}

// fakeStageJobs records R49 re-enqueues and mirrors the dispatcher's idempotent
// creation: the repaired stage-entry item appears in the work queue, so a later
// scan must not enqueue it again.
type fakeStageJobs struct {
	mu    sync.Mutex
	jobs  []versionapp.StageJob
	queue *fakeEnsurer
	err   error
}

func (f *fakeStageJobs) EnqueueStageJob(_ context.Context, job versionapp.StageJob) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.jobs = append(f.jobs, job)
	if f.queue != nil {
		f.queue.addItem(job.VersionID, job.Kind, fmt.Sprintf("%d:%s:test", job.VersionID, job.Kind), agent.WorkItemOpen)
	}
	return nil
}

func (f *fakeStageJobs) recorded() []versionapp.StageJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]versionapp.StageJob(nil), f.jobs...)
}

func (e *fakeEnsurer) itemsOfType(versionID int64, workType string) []agent.WorkItem {
	e.mu.Lock()
	defer e.mu.Unlock()
	items := make([]agent.WorkItem, 0)
	for _, item := range e.items {
		if item.VersionID == versionID && item.Type == workType {
			items = append(items, item)
		}
	}
	return items
}

// TestNotifyDebounces proves the event-driven wake coalesces a burst: ten
// Notify calls inside 100ms schedule exactly one reconciliation pass, and
// cancellation stops the loop. This is what lets the 3s poll be removed
// without losing wakeups (spec §5.7).
func TestNotifyDebounces(t *testing.T) {
	fixture := newReconcilerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- fixture.reconciler.Run(ctx, nil) }()

	for i := 0; i < 10; i++ {
		fixture.reconciler.Notify("test.burst", 10)
		time.Sleep(10 * time.Millisecond)
	}
	// The default debounce is 300ms; wait for the coalesced pass, then one full
	// window more to prove no second pass was scheduled.
	deadline := time.Now().Add(2 * time.Second)
	for fixture.design.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if calls := fixture.design.callCount(); calls != 1 {
		t.Fatalf("reconcile passes = %d after a 10-notify burst, want 1", calls)
	}
	time.Sleep(500 * time.Millisecond)
	if calls := fixture.design.callCount(); calls != 1 {
		t.Fatalf("reconcile passes = %d, want exactly one coalesced pass", calls)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v on cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop on context cancellation")
	}
}

// TestReconcilerRepairsMissingStageJob pins R49: a running version in a stage
// whose entry job is missing gets exactly one re-enqueue through the stage-job
// dispatcher (same idempotency key path), and a second scan does not duplicate
// it. The catalog mapping mirrors the stage machine: designing owns design.cases,
// executing owns execute.batch, reporting owns report.version.
func TestReconcilerRepairsMissingStageJob(t *testing.T) {
	cases := []struct {
		name     string
		stage    version.Stage
		wantKind string
	}{
		{"designing", version.StageDesigning, versionapp.StageJobDesignCases},
		{"executing", version.StageExecuting, versionapp.StageJobExecuteBatch},
		{"reporting", version.StageReporting, versionapp.StageJobReportVersion},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			queue := &fakeEnsurer{}
			jobs := &fakeStageJobs{queue: queue}
			reconciler := agentapp.NewReconciler(agentapp.ReconcilerDeps{
				Versions:        &fakeProjector{},
				Queue:           queue,
				RunningVersions: &fakeRunningVersions{ids: []int64{10}},
				Stages: &fakeStageReader{states: map[int64]version.StageState{
					10: {VersionID: 10, Stage: testCase.stage},
				}},
				StageJobs: jobs,
				Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
			})
			for pass := 0; pass < 2; pass++ {
				if err := reconciler.Reconcile(context.Background()); err != nil {
					t.Fatalf("Reconcile pass %d: %v", pass, err)
				}
			}
			recorded := jobs.recorded()
			if len(recorded) != 1 {
				t.Fatalf("stage jobs = %#v, want one re-enqueue (idempotent)", recorded)
			}
			if recorded[0].VersionID != 10 || recorded[0].Kind != testCase.wantKind {
				t.Fatalf("re-enqueued job = %#v, want version 10 %s", recorded[0], testCase.wantKind)
			}
			if items := queue.itemsOfType(10, testCase.wantKind); len(items) != 1 {
				t.Fatalf("%s items = %#v, want one", testCase.wantKind, items)
			}
		})
	}

	t.Run("case_review has no dispatchable entry job", func(t *testing.T) {
		// The design.cases job can only be dispatched while the version is
		// designing; the dispatcher rejects it at case_review (stage ownership),
		// so recovery must not emit a guaranteed 409 on every pass. case_review
		// itself has no entry job (Task 4/7).
		queue := &fakeEnsurer{}
		jobs := &fakeStageJobs{queue: queue}
		reconciler := agentapp.NewReconciler(agentapp.ReconcilerDeps{
			Versions:        &fakeProjector{},
			Queue:           queue,
			RunningVersions: &fakeRunningVersions{ids: []int64{10}},
			Stages: &fakeStageReader{states: map[int64]version.StageState{
				10: {VersionID: 10, Stage: version.StageCaseReview},
			}},
			StageJobs: jobs,
			Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		if err := reconciler.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if recorded := jobs.recorded(); len(recorded) != 0 {
			t.Fatalf("stage jobs = %#v, want none at case_review", recorded)
		}
	})

	t.Run("existing entry job is not re-enqueued", func(t *testing.T) {
		queue := &fakeEnsurer{}
		queue.addItem(10, versionapp.StageJobDesignCases, "10:design.cases:existing", agent.WorkItemOpen)
		jobs := &fakeStageJobs{queue: queue}
		reconciler := agentapp.NewReconciler(agentapp.ReconcilerDeps{
			Versions:        &fakeProjector{},
			Queue:           queue,
			RunningVersions: &fakeRunningVersions{ids: []int64{10}},
			Stages: &fakeStageReader{states: map[int64]version.StageState{
				10: {VersionID: 10, Stage: version.StageDesigning},
			}},
			StageJobs: jobs,
			Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		if err := reconciler.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if recorded := jobs.recorded(); len(recorded) != 0 {
			t.Fatalf("stage jobs = %#v, want none while the entry job exists", recorded)
		}
		if items := queue.itemsOfType(10, versionapp.StageJobDesignCases); len(items) != 1 {
			t.Fatalf("design.cases items = %#v, want the existing one", items)
		}
	})

	t.Run("stages without an entry job are skipped", func(t *testing.T) {
		queue := &fakeEnsurer{}
		jobs := &fakeStageJobs{queue: queue}
		reconciler := agentapp.NewReconciler(agentapp.ReconcilerDeps{
			Versions:        &fakeProjector{},
			Queue:           queue,
			RunningVersions: &fakeRunningVersions{ids: []int64{10}},
			Stages: &fakeStageReader{states: map[int64]version.StageState{
				10: {VersionID: 10, Stage: version.StageClarifying},
			}},
			StageJobs: jobs,
			Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		if err := reconciler.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if recorded := jobs.recorded(); len(recorded) != 0 {
			t.Fatalf("stage jobs = %#v, want none for a stage without an entry job", recorded)
		}
	})
}

var _ agentapp.VersionStageReader = (*fakeStageReader)(nil)
var _ versionapp.StageJobEnqueuer = (*fakeStageJobs)(nil)

var _ agentapp.VersionProjector = (*fakeProjector)(nil)
var _ agentapp.ConversationReader = (*fakeConversationReader)(nil)
var _ agentapp.PendingAttachmentReader = (*fakePendingAttachments)(nil)
var _ agentapp.DesignReconciler = (*fakeDesignReconciler)(nil)
var _ agentapp.WorkQueueEnsurer = (*fakeEnsurer)(nil)
var _ = time.Second
