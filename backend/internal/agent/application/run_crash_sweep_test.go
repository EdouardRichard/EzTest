package agentapp_test

// v0.2.4-H T12 (H-E2): the run-level crash sweep.
//
// agent_run has no crash recovery of its own: RecoverExpired settles the WORK
// ITEM (open when attempts remain, dead/failed when exhausted) and never touches
// the run row or its transcript. A process that dies mid-run therefore leaves
// agent_run.status='running' forever and its agent_message head 'streaming', so
// the admin transcript renders an unfinished bubble that no later turn will ever
// seal. The Host conversation head has the same problem when the conversation
// gets no further turn (no lease holder exists to run the orphan sweep).
//
// The sweeper closes both, and it does so through the EXISTING server-side
// terminal entries only: RuntimeRepository.UpdateMessage seals a transcript row,
// RuntimeRepository.FinishRun writes the run terminal (constitution 三.6 — no
// second terminal write path), and the conversation port seals the run's Host
// heads. These tests pin the orchestration; the real SQL of the orphan selector
// and of the seals is pinned by the integration tests.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
)

type crashSweepClock struct{ now time.Time }

func (c crashSweepClock) Now() time.Time { return c.now }

type crashSweepEvents struct{ events []eventingapp.Event }

func (p *crashSweepEvents) Publish(_ context.Context, event eventingapp.Event) error {
	p.events = append(p.events, event)
	return nil
}

// crashSweepStore is the narrow read/write seam the sweeper needs. It records
// every seal and every terminal write so "sealed through the existing entry"
// is observable, and it can fail individual writes to prove per-run tolerance.
type crashSweepStore struct {
	crashed        []agentapp.CrashedRun
	listErr        error
	messages       map[int64][]agent.AgentMessage
	streamingReads []int64
	updates        []agent.AgentMessage
	updateErr      map[int64]error
	finished       []agent.AgentRun
	finishErr      map[int64]error
	listLimits     []int
	// leakTerminalRows makes the read return terminal rows too, so the sweeper's
	// own "never rewrite a frozen row" guard is under test independently of the
	// store's SQL filter.
	leakTerminalRows bool
}

func (s *crashSweepStore) ListCrashedRuns(_ context.Context, limit int) ([]agentapp.CrashedRun, error) {
	s.listLimits = append(s.listLimits, limit)
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.crashed, nil
}

func (s *crashSweepStore) ListStreamingRunMessages(_ context.Context, runID int64) ([]agent.AgentMessage, error) {
	rows := make([]agent.AgentMessage, 0)
	for _, message := range s.messages[runID] {
		if s.leakTerminalRows || message.StreamState == agent.MessageStreamStateStreaming {
			rows = append(rows, message)
		}
	}
	s.streamingReads = append(s.streamingReads, runID)
	return rows, nil
}

func (s *crashSweepStore) UpdateMessage(_ context.Context, message agent.AgentMessage) error {
	if err := s.updateErr[message.ID]; err != nil {
		return err
	}
	s.updates = append(s.updates, message)
	return nil
}

func (s *crashSweepStore) FinishRun(_ context.Context, run agent.AgentRun) error {
	if err := s.finishErr[run.ID]; err != nil {
		return err
	}
	s.finished = append(s.finished, run)
	return nil
}

type crashSweepConversations struct {
	calls []struct {
		conversationID int64
		runID          int64
		part           agentapp.ConversationPart
	}
	err error
}

func (c *crashSweepConversations) InterruptRunOrphanStreams(_ context.Context, conversationID, runID int64, part agentapp.ConversationPart) (int, error) {
	c.calls = append(c.calls, struct {
		conversationID int64
		runID          int64
		part           agentapp.ConversationPart
	}{conversationID, runID, part})
	if c.err != nil {
		return 0, c.err
	}
	return 1, nil
}

func crashSweepFixture() (*crashSweepStore, *crashSweepConversations, *crashSweepEvents, time.Time) {
	now := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	store := &crashSweepStore{
		crashed: []agentapp.CrashedRun{{
			ID: 900, LeaseEpoch: 3, WorkItemID: 700, VersionID: 700, ConversationID: 20,
			ProfileKey: "clarification_host", Turns: 2,
			Usage: map[string]int{"prompt_tokens": 120},
		}},
		messages: map[int64][]agent.AgentMessage{
			900: {
				{ID: 901, RunID: crashSweepPtr(int64(900)), Role: agent.MessageRoleSystem, StreamState: agent.MessageStreamStateComplete},
				{ID: 902, RunID: crashSweepPtr(int64(900)), Role: agent.MessageRoleAssistant, StreamState: agent.MessageStreamStateStreaming,
					Parts: []agent.MessagePart{{Type: "text", Text: "崩溃前已生成的内容"}}},
				{ID: 903, RunID: crashSweepPtr(int64(900)), Role: agent.MessageRoleAssistant, StreamState: agent.MessageStreamStateInterrupted},
			},
		},
	}
	return store, &crashSweepConversations{}, &crashSweepEvents{}, now
}

func crashSweepPtr[T any](value T) *T { return &value }

func newCrashSweeper(store *crashSweepStore, conversations *crashSweepConversations, events *crashSweepEvents, now time.Time) *agentapp.RunCrashSweeper {
	return agentapp.NewRunCrashSweeper(store, conversations, events,
		slog.New(slog.NewTextHandler(io.Discard, nil)), crashSweepClock{now: now})
}

// TestCrashSweepSealsDeadItemRunAndTranscript is the core H-E2 assertion: an
// exhausted-attempts (dead) item's run reaches a terminal status and its
// streaming transcript head is sealed, both through the existing entries.
func TestCrashSweepSealsDeadItemRunAndTranscript(t *testing.T) {
	store, conversations, events, now := crashSweepFixture()
	sweeper := newCrashSweeper(store, conversations, events, now)

	report, err := sweeper.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if report.Runs != 1 || report.TranscriptHeads != 1 || report.ConversationHeads != 1 || report.Failed != 0 {
		t.Fatalf("report = %#v, want 1 run / 1 transcript head / 1 conversation head / 0 failures", report)
	}
	if len(store.updates) != 1 || store.updates[0].ID != 902 {
		t.Fatalf("sealed rows = %#v, want only the streaming head 902", store.updates)
	}
	if store.updates[0].StreamState != agent.MessageStreamStateInterrupted {
		t.Fatalf("sealed stream_state = %q, want interrupted", store.updates[0].StreamState)
	}
	if len(store.updates[0].Parts) != 1 || store.updates[0].Parts[0].Text != "崩溃前已生成的内容" {
		t.Fatalf("sealed parts = %#v, want the accumulated content preserved", store.updates[0].Parts)
	}
	if len(store.finished) != 1 {
		t.Fatalf("terminal writes = %#v, want exactly one", store.finished)
	}
	finished := store.finished[0]
	if finished.ID != 900 || finished.LeaseEpoch != 3 {
		t.Fatalf("terminal write identity = (%d,%d), want the stored run id and lease epoch (fenced)",
			finished.ID, finished.LeaseEpoch)
	}
	if finished.Status != agent.AgentRunInterrupted {
		t.Fatalf("terminal status = %q, want interrupted (a crash is an interruption, never done)", finished.Status)
	}
	if finished.TerminalReason == nil || *finished.TerminalReason != "interrupted" {
		t.Fatalf("terminal_reason = %#v, want the interrupted taxonomy reason", finished.TerminalReason)
	}
	if finished.FinishedAt == nil || !finished.FinishedAt.Equal(now) {
		t.Fatalf("finished_at = %#v, want the sweep clock's now", finished.FinishedAt)
	}
	if finished.Turns != 2 || finished.Usage["prompt_tokens"] != 120 {
		t.Fatalf("terminal counters = %d/%#v, want the run's own recorded counters preserved",
			finished.Turns, finished.Usage)
	}
	if len(conversations.calls) != 1 || conversations.calls[0].conversationID != 20 || conversations.calls[0].runID != 900 {
		t.Fatalf("conversation recovery calls = %#v, want one for (20,900)", conversations.calls)
	}
	if conversations.calls[0].part.Type != "error" ||
		conversations.calls[0].part.Data["code"] != "stream_interrupted" {
		t.Fatalf("recovery part = %#v, want the shared stream_interrupted error part", conversations.calls[0].part)
	}
	// The terminal transcript frame is republished on the run's own channel so a
	// live viewer stops rendering an unfinished row.
	if len(events.events) != 1 {
		t.Fatalf("published events = %#v, want one terminal transcript frame", events.events)
	}
	frame := events.events[0]
	if frame.Channel != eventingapp.RunChannel(900) || frame.Type != agentapp.TranscriptDeltaEventType {
		t.Fatalf("frame channel/type = %q/%q, want the run transcript channel and event", frame.Channel, frame.Type)
	}
	if frame.Payload["stream_state"] != agent.MessageStreamStateInterrupted || frame.Payload["message"] == nil {
		t.Fatalf("frame payload = %#v, want a full interrupted snapshot", frame.Payload)
	}
}

// TestCrashSweepIsNoOpWithoutCrashedRuns pins that a healthy queue is untouched.
func TestCrashSweepIsNoOpWithoutCrashedRuns(t *testing.T) {
	store, conversations, events, now := crashSweepFixture()
	store.crashed = nil
	report, err := newCrashSweeper(store, conversations, events, now).Sweep(context.Background())
	if err != nil || report.Runs != 0 || report.Failed != 0 {
		t.Fatalf("empty sweep = %#v (%v), want a zero report", report, err)
	}
	if len(store.updates) != 0 || len(store.finished) != 0 || len(conversations.calls) != 0 || len(events.events) != 0 {
		t.Fatal("an empty sweep wrote something")
	}
	if len(store.listLimits) != 1 || store.listLimits[0] <= 0 {
		t.Fatalf("list limits = %#v, want one bounded read", store.listLimits)
	}
}

// TestCrashSweepToleratesPerRunFailures pins that one unsealable/unwritable run
// never blocks the recovery of the others, and that the failure is counted.
func TestCrashSweepToleratesPerRunFailures(t *testing.T) {
	store, conversations, events, now := crashSweepFixture()
	store.crashed = append(store.crashed, agentapp.CrashedRun{ID: 910, LeaseEpoch: 1, WorkItemID: 710})
	store.messages[910] = []agent.AgentMessage{
		{ID: 911, RunID: crashSweepPtr(int64(910)), Role: agent.MessageRoleAssistant, StreamState: agent.MessageStreamStateStreaming},
	}
	store.finishErr = map[int64]error{900: errors.New("store unavailable")}

	report, err := newCrashSweeper(store, conversations, events, now).Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if report.Runs != 1 || report.Failed != 1 {
		t.Fatalf("report = %#v, want the second run recovered and the first counted as failed", report)
	}
	if len(store.finished) != 1 || store.finished[0].ID != 910 {
		t.Fatalf("terminal writes = %#v, want the healthy run 910 finished", store.finished)
	}
	// A run without a conversation binding must not call the conversation port.
	for _, call := range conversations.calls {
		if call.runID == 910 {
			t.Fatalf("unbound run reached the conversation port: %#v", call)
		}
	}
}

// TestCrashSweepFinishesRunEvenWhenASealFails mirrors the runner's own terminal
// funnel: a lost head seal is logged, never a reason to leave the run running.
func TestCrashSweepFinishesRunEvenWhenASealFails(t *testing.T) {
	store, conversations, events, now := crashSweepFixture()
	store.updateErr = map[int64]error{902: errors.New("row locked")}
	store.messages[900] = append(store.messages[900], agent.AgentMessage{
		ID: 904, RunID: crashSweepPtr(int64(900)), Role: agent.MessageRoleAssistant, StreamState: agent.MessageStreamStateStreaming,
	})

	report, err := newCrashSweeper(store, conversations, events, now).Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if report.Runs != 1 || report.TranscriptHeads != 1 {
		t.Fatalf("report = %#v, want the run recovered with the one head that could be sealed", report)
	}
	if len(store.updates) != 1 || store.updates[0].ID != 904 {
		t.Fatalf("sealed rows = %#v, want the second head sealed despite the first failure", store.updates)
	}
	if len(store.finished) != 1 || store.finished[0].Status != agent.AgentRunInterrupted {
		t.Fatalf("terminal writes = %#v, want the run finished even though a seal failed", store.finished)
	}
}

// TestCrashSweepReportsConversationRecoveryFailure pins that a Host-head recovery
// failure is surfaced (never silent) while the run still reaches its terminal.
func TestCrashSweepReportsConversationRecoveryFailure(t *testing.T) {
	store, conversations, events, now := crashSweepFixture()
	conversations.err = errors.New("conversation unavailable")

	report, err := newCrashSweeper(store, conversations, events, now).Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if report.ConversationHeads != 0 || report.Runs != 1 {
		t.Fatalf("report = %#v, want no conversation head sealed and the run still recovered", report)
	}
	if len(store.finished) != 1 {
		t.Fatalf("terminal writes = %#v, want the run finished", store.finished)
	}
}

// TestCrashSweepWithoutConversationPort pins the half-assembled deployment: the
// transcript and the run terminal are still recovered, and nothing panics.
func TestCrashSweepWithoutConversationPort(t *testing.T) {
	store, _, events, now := crashSweepFixture()
	report, err := agentapp.NewRunCrashSweeper(store, nil, events,
		slog.New(slog.NewTextHandler(io.Discard, nil)), crashSweepClock{now: now}).Sweep(context.Background())
	if err != nil || report.Runs != 1 || report.TranscriptHeads != 1 || report.ConversationHeads != 0 {
		t.Fatalf("portless sweep = %#v (%v), want the transcript and run recovered", report, err)
	}
}

// TestCrashSweepNeverRewritesAFrozenTranscriptRow pins the sweeper's own guard: a
// terminal row is immutable (the repository refuses the write), so the sweeper
// must not even attempt it, whatever the read handed back.
func TestCrashSweepNeverRewritesAFrozenTranscriptRow(t *testing.T) {
	store, conversations, events, now := crashSweepFixture()
	store.leakTerminalRows = true
	report, err := newCrashSweeper(store, conversations, events, now).Sweep(context.Background())
	if err != nil || report.TranscriptHeads != 1 {
		t.Fatalf("sweep = %#v (%v), want only the streaming head sealed", report, err)
	}
	for _, update := range store.updates {
		if update.ID != 902 {
			t.Fatalf("the sweeper rewrote frozen row %d", update.ID)
		}
	}
}
