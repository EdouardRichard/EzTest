package agentapp_test

// v0.2.4-D T6: the runner's Host incremental write path (SC-D2/SC-D4).
//
// The Host profiles (clarification_host/design_host) write their reply as one
// incremental conversation message: a placeholder head at the FIRST delta,
// per-seq full-snapshot part updates while the model streams, and a terminal
// seal. The expectations below come from the T5 pipeline contract (1-based
// contiguous seq, full snapshot per update, Finish=complete /
// Interrupt=interrupted, accumulated content never deleted) and the R-D7
// first-delta retry boundary — not from observed runner output.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

type hostStreamBegin struct {
	ConversationID int64
	Agent          string
	TurnID         int64
	RunID          int64
}

type hostStreamUpdate struct {
	MessageID int64
	Seq       int
	Part      agentapp.ConversationPart
}

type hostStreamSeal struct {
	MessageID int64
	Parts     []agentapp.ConversationPart
}

// hostStreamPort is the writer-lease conversation port plus the T5 incremental
// stream surface. Every write honours ctx.Err() exactly like the real
// repository (a cancelled context cannot write), so a seal that must survive
// cancellation is really pinned instead of supplied by a permissive fake.
type hostStreamPort struct {
	*leaseConversationPort
	mu         sync.Mutex
	calls      []string
	begins     []hostStreamBegin
	updates    []hostStreamUpdate
	finishes   []hostStreamSeal
	interrupts []hostStreamSeal
	recoveries []agentapp.ConversationPart
	messageID  int64
	beginErr   error
	updateErr  error
	finishErr  error
	orphaned   int
	// sealAttribution records the run-context value each terminal seal carried,
	// so "the seal keeps the run attribution" is pinned instead of assumed.
	sealAttribution []any
}

// hostStreamAttributionKey is the run-context value the seal must preserve.
type hostStreamAttributionKey struct{}

// validateStreamParts mirrors the T5 contract bounds the runner can violate
// (non-empty parts, bounded text/thinking, message part cap). The numbers come
// from the version domain authority, never from a hand-copied literal.
func validateStreamParts(parts []agentapp.ConversationPart) error {
	if len(parts) == 0 {
		return errors.New("invalid_argument: parts must contain at least one part")
	}
	if len(parts) > version.MaxMessageStreamParts {
		return errors.New("invalid_argument: too many message parts")
	}
	for _, part := range parts {
		if part.Type != "text" && part.Type != "thinking" {
			continue
		}
		if len([]rune(part.Text)) < 1 || len([]rune(part.Text)) > version.MessageTextMax {
			return errors.New("invalid_argument: text is out of the contract bounds")
		}
	}
	return nil
}

func newHostStreamPort() *hostStreamPort {
	return &hostStreamPort{leaseConversationPort: newLeaseConversationPort(), messageID: 9101}
}

func (p *hostStreamPort) record(name string) {
	p.calls = append(p.calls, name)
}

func (p *hostStreamPort) BeginAssistantStream(ctx context.Context, conversationID int64, agentName string, turnID, runID int64) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.record("begin")
	p.begins = append(p.begins, hostStreamBegin{ConversationID: conversationID, Agent: agentName, TurnID: turnID, RunID: runID})
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if p.beginErr != nil {
		return 0, p.beginErr
	}
	return p.messageID, nil
}

func (p *hostStreamPort) UpdateStreamPart(ctx context.Context, _, messageID int64, seq int, part agentapp.ConversationPart) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.record("update")
	p.updates = append(p.updates, hostStreamUpdate{MessageID: messageID, Seq: seq, Part: part})
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.updateErr != nil {
		return p.updateErr
	}
	return validateStreamParts([]agentapp.ConversationPart{part})
}

func (p *hostStreamPort) FinishAssistantStream(ctx context.Context, _, messageID int64, parts []agentapp.ConversationPart) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.record("finish")
	p.finishes = append(p.finishes, hostStreamSeal{MessageID: messageID, Parts: append([]agentapp.ConversationPart(nil), parts...)})
	p.sealAttribution = append(p.sealAttribution, ctx.Value(hostStreamAttributionKey{}))
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.finishErr != nil {
		return p.finishErr
	}
	return validateStreamParts(parts)
}

func (p *hostStreamPort) InterruptAssistantStream(ctx context.Context, _, messageID int64, parts []agentapp.ConversationPart) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.record("interrupt")
	p.interrupts = append(p.interrupts, hostStreamSeal{MessageID: messageID, Parts: append([]agentapp.ConversationPart(nil), parts...)})
	p.sealAttribution = append(p.sealAttribution, ctx.Value(hostStreamAttributionKey{}))
	if err := ctx.Err(); err != nil {
		return err
	}
	return validateStreamParts(parts)
}

func (p *hostStreamPort) InterruptOrphanStreams(ctx context.Context, _ int64, _ string, _ int64, errorPart agentapp.ConversationPart) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.record("recover")
	p.recoveries = append(p.recoveries, errorPart)
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return p.orphaned, nil
}

func (p *hostStreamPort) snapshot() ([]string, []hostStreamBegin, []hostStreamUpdate, []hostStreamSeal, []hostStreamSeal) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...),
		append([]hostStreamBegin(nil), p.begins...),
		append([]hostStreamUpdate(nil), p.updates...),
		append([]hostStreamSeal(nil), p.finishes...),
		append([]hostStreamSeal(nil), p.interrupts...)
}

type hostStreamFixture struct {
	*wiringFixture
	stream *hostStreamPort
}

func newHostStreamFixture(t *testing.T, options ...func(*agentapp.RunnerDeps)) *hostStreamFixture {
	t.Helper()
	stream := newHostStreamPort()
	base := newWiringFixture(t, append([]func(*agentapp.RunnerDeps){
		func(deps *agentapp.RunnerDeps) { deps.Conversation = stream },
	}, options...)...)
	return &hostStreamFixture{wiringFixture: base, stream: stream}
}

// lockedBuffer collects the runner log across the run goroutine and the writer
// lease heartbeat goroutine.
type lockedBuffer struct {
	mu sync.Mutex
	strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Builder.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Builder.String()
}

// seqTexts returns the successive snapshots pushed for one seq, in call order.
func seqTexts(updates []hostStreamUpdate, seq int) []string {
	var texts []string
	for _, update := range updates {
		if update.Seq == seq {
			texts = append(texts, update.Part.Text)
		}
	}
	return texts
}

// assertGrowingPrefixChain pins the R-D2 snapshot contract: every update of one
// seq carries the FULL accumulated text, and each snapshot extends the previous
// one (never a fragment, never a shrink).
func assertGrowingPrefixChain(t *testing.T, label string, texts []string, want string) {
	t.Helper()
	if len(texts) == 0 {
		t.Fatalf("%s: no update was pushed", label)
	}
	previous := ""
	for _, text := range texts {
		if !strings.HasPrefix(text, previous) {
			t.Fatalf("%s: snapshot %q does not extend %q (updates must carry the full snapshot)", label, text, previous)
		}
		previous = text
	}
	if previous != want {
		t.Fatalf("%s: last snapshot = %q, want %q", label, previous, want)
	}
}

func assertContiguousSeqs(t *testing.T, updates []hostStreamUpdate, wantMax int) {
	t.Helper()
	seen := map[int]bool{}
	maxSeq := 0
	for _, update := range updates {
		if update.Seq < 1 {
			t.Fatalf("seq = %d, want 1-based", update.Seq)
		}
		seen[update.Seq] = true
		if update.Seq > maxSeq {
			maxSeq = update.Seq
		}
	}
	if maxSeq != wantMax {
		t.Fatalf("max seq = %d, want %d", maxSeq, wantMax)
	}
	for seq := 1; seq <= maxSeq; seq++ {
		if !seen[seq] {
			t.Fatalf("seq %d was never written; seqs must be contiguous from 1", seq)
		}
	}
}

func partByType(parts []agentapp.ConversationPart, partType string) *agentapp.ConversationPart {
	for index := range parts {
		if parts[index].Type == partType {
			return &parts[index]
		}
	}
	return nil
}

// TestHostStreamBeginsAtFirstDeltaAndSealsComplete pins the SC-D2 lifecycle:
// one placeholder head per run created at the first delta through the writer
// lease, per-seq full-snapshot updates while streaming, and exactly one
// complete seal carrying the final parts. The batch append paths are not used.
func TestHostStreamBeginsAtFirstDeltaAndSealsComplete(t *testing.T) {
	fixture := newHostStreamFixture(t)
	fixture.chat.streams = [][]llmapp.Delta{{
		{ReasoningContent: "先想"},
		{ReasoningContent: "清楚"},
		{Content: "你好"},
		{Content: "，世界"},
		{FinishReason: "stop"},
	}}

	result, err := fixture.runner.Run(context.Background(), hostItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != agent.AgentRunDone {
		t.Fatalf("result = %#v", result)
	}
	calls, begins, updates, finishes, interrupts := fixture.stream.snapshot()
	if len(begins) != 1 {
		t.Fatalf("placeholder heads = %d, want exactly 1 per run", len(begins))
	}
	if begins[0].ConversationID != 20 || begins[0].Agent != agentapp.ProfileKeyClarificationHost ||
		begins[0].TurnID != result.AgentRunID || begins[0].RunID != result.AgentRunID {
		t.Fatalf("begin = %#v, want conversation 20 / clarification_host / turn=run=%d", begins[0], result.AgentRunID)
	}
	if len(finishes) != 1 || len(interrupts) != 0 {
		t.Fatalf("seals: finish=%d interrupt=%d, want exactly one complete seal", len(finishes), len(interrupts))
	}
	if finishes[0].MessageID != fixture.stream.messageID {
		t.Fatalf("finish message = %d, want the head %d", finishes[0].MessageID, fixture.stream.messageID)
	}
	if len(finishes[0].Parts) != 2 ||
		finishes[0].Parts[0].Type != "thinking" || finishes[0].Parts[0].Text != "先想清楚" ||
		finishes[0].Parts[1].Type != "text" || finishes[0].Parts[1].Text != "你好，世界" {
		t.Fatalf("final parts = %#v, want [thinking 先想清楚, text 你好，世界]", finishes[0].Parts)
	}
	assertContiguousSeqs(t, updates, 2)
	assertGrowingPrefixChain(t, "thinking seq 1", seqTexts(updates, 1), "先想清楚")
	assertGrowingPrefixChain(t, "text seq 2", seqTexts(updates, 2), "你好，世界")
	for _, update := range updates {
		if update.MessageID != fixture.stream.messageID {
			t.Fatalf("update targets message %d, want the head %d", update.MessageID, fixture.stream.messageID)
		}
		wantType := "thinking"
		if update.Seq == 2 {
			wantType = "text"
		}
		if update.Part.Type != wantType {
			t.Fatalf("seq %d part type = %q, want %q", update.Seq, update.Part.Type, wantType)
		}
	}
	// The head is created before any part update, and sealed last.
	if calls[0] != "recover" || calls[1] != "begin" || calls[2] != "update" || calls[len(calls)-1] != "finish" {
		t.Fatalf("call order = %v, want recover -> begin -> update... -> finish", calls)
	}
	if len(fixture.stream.written) != 0 || len(fixture.stream.fakeConversationPort.appended) != 0 {
		t.Fatalf("batch write path was used alongside the stream: written=%#v appended=%#v",
			fixture.stream.written, fixture.stream.fakeConversationPort.appended)
	}
}

// TestHostStreamThinkingAndTextUseIndependentSeqs pins SC-D2's reasoning
// increment: the thinking part grows on its own seq with a server-computed
// duration, and the text part never shares that seq.
func TestHostStreamThinkingAndTextUseIndependentSeqs(t *testing.T) {
	fixture := newHostStreamFixture(t)
	fixture.chat.streams = [][]llmapp.Delta{{
		{Content: "先答"},
		{ReasoningContent: "再想"},
		{Content: "后半"},
		{ReasoningContent: "更细"},
		{FinishReason: "stop"},
	}}

	if _, err := fixture.runner.Run(context.Background(), hostItem()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_, _, updates, finishes, _ := fixture.stream.snapshot()
	assertContiguousSeqs(t, updates, 2)
	assertGrowingPrefixChain(t, "text seq 1", seqTexts(updates, 1), "先答后半")
	assertGrowingPrefixChain(t, "thinking seq 2", seqTexts(updates, 2), "再想更细")
	for _, update := range updates {
		if update.Seq != 2 {
			continue
		}
		duration, ok := update.Part.Data["duration_ms"].(int64)
		if !ok || duration < 0 {
			t.Fatalf("thinking duration_ms = %#v, want a non-negative server-computed int64", update.Part.Data["duration_ms"])
		}
	}
	if len(finishes) != 1 {
		t.Fatalf("complete seals = %d, want 1", len(finishes))
	}
	thinking := partByType(finishes[0].Parts, "thinking")
	if thinking == nil || thinking.Text != "再想更细" {
		t.Fatalf("final thinking part = %#v", thinking)
	}
}

// TestHostStreamToolCallAssemblesArgumentsBeforeFinalPart pins SC-D2's tool-call
// rule: argument fragments are assembled server-side, so no part ever carries a
// raw JSON fragment; the in-flight part is explicitly marked running and the
// final part carries the complete arguments.
func TestHostStreamToolCallAssemblesArgumentsBeforeFinalPart(t *testing.T) {
	fixture := newHostStreamFixture(t)
	fixture.chat.streams = [][]llmapp.Delta{
		{
			{ToolCall: &llmapp.ToolCallDelta{Index: 0, ID: "call_1", Name: tools.ToolContextVersionPack}},
			{ToolCall: &llmapp.ToolCallDelta{Index: 0, Arguments: "{"}},
			{ToolCall: &llmapp.ToolCallDelta{Index: 0, Arguments: "}"}},
			{FinishReason: "tool_calls"},
		},
		{
			{Content: "读完了"},
			{FinishReason: "stop"},
		},
	}

	result, err := fixture.runner.Run(context.Background(), hostItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Turns != 2 {
		t.Fatalf("turns = %d, want 2", result.Turns)
	}
	_, _, updates, finishes, _ := fixture.stream.snapshot()
	sawRunning := false
	for _, update := range updates {
		if update.Part.Type != "tool_call" {
			continue
		}
		if raw, ok := update.Part.Data["arguments"]; ok {
			encoded, err := json.Marshal(raw)
			if err != nil || !json.Valid(encoded) {
				t.Fatalf("tool_call arguments fragment reached the wire: %#v (%v)", raw, err)
			}
		}
		if update.Part.Data["status"] == "running" {
			sawRunning = true
		}
	}
	if !sawRunning {
		t.Fatalf("no explicitly marked in-flight tool_call part: %#v", updates)
	}
	if len(finishes) != 1 {
		t.Fatalf("complete seals = %d, want 1", len(finishes))
	}
	call := partByType(finishes[0].Parts, "tool_call")
	if call == nil {
		t.Fatalf("final parts lost the tool_call part: %#v", finishes[0].Parts)
	}
	if call.Data["status"] != "done" || call.Data["call_id"] != "call_1" || call.Data["name"] != tools.ToolContextVersionPack {
		t.Fatalf("final tool_call part = %#v, want done/call_1/%s", call.Data, tools.ToolContextVersionPack)
	}
	arguments, err := json.Marshal(call.Data["arguments"])
	if err != nil || string(arguments) != "{}" {
		t.Fatalf("final tool_call arguments = %s (%v), want the assembled {}", arguments, err)
	}
	if text := partByType(finishes[0].Parts, "text"); text == nil || text.Text != "读完了" {
		t.Fatalf("final text part = %#v", text)
	}
}

// TestHostStreamPostFirstDeltaErrorPreservesContentAndInterrupts pins SC-D4:
// after the first delta an error never retries, the accumulated parts are
// preserved (never deleted), an error part explains the interruption and the
// head is sealed INTERRUPTED (not complete).
func TestHostStreamPostFirstDeltaErrorPreservesContentAndInterrupts(t *testing.T) {
	fixture := newHostStreamFixture(t)
	fixture.chat.streams = [][]llmapp.Delta{{
		{ReasoningContent: "已经想过的"},
		{Content: "已经显示的"},
		{Content: "半句"},
	}}
	fixture.chat.errors = []error{fmt.Errorf("upstream: %w", llmapp.ErrTransient)}

	result, err := fixture.runner.Run(context.Background(), hostItem())
	if err == nil {
		t.Fatal("expected the run to fail after the mid-stream error")
	}
	if result.Status != agent.AgentRunFailed || result.TerminalReason != "model_error" {
		t.Fatalf("result = %#v, want failed/model_error (terminal taxonomy unchanged)", result)
	}
	if agentapp.IsRetryable(err) {
		t.Fatalf("post-first-delta failure is retryable: %v", err)
	}
	if fixture.chat.requestCount() != 1 {
		t.Fatalf("model calls = %d, want 1 (no retry after the first delta)", fixture.chat.requestCount())
	}
	_, _, _, finishes, interrupts := fixture.stream.snapshot()
	if len(finishes) != 0 {
		t.Fatalf("an interrupted reply was sealed complete: %#v", finishes)
	}
	if len(interrupts) != 1 {
		t.Fatalf("interrupt seals = %d, want exactly 1", len(interrupts))
	}
	parts := interrupts[0].Parts
	text := partByType(parts, "text")
	if text == nil || text.Text != "已经显示的半句" {
		t.Fatalf("accumulated text was not preserved: %#v", parts)
	}
	thinking := partByType(parts, "thinking")
	if thinking == nil || thinking.Text != "已经想过的" {
		t.Fatalf("accumulated thinking was not preserved: %#v", parts)
	}
	errorPart := partByType(parts, "error")
	if errorPart == nil {
		t.Fatalf("no error part explains the interruption: %#v", parts)
	}
	code, _ := errorPart.Data["code"].(string)
	message, _ := errorPart.Data["message"].(string)
	if strings.TrimSpace(code) == "" || strings.TrimSpace(message) == "" {
		t.Fatalf("error part = %#v, want a non-empty code and message", errorPart.Data)
	}
}

// TestHostStreamPreFirstDeltaErrorCreatesNoPlaceholder pins the other side of
// the R-D7 boundary: nothing was shown, so no placeholder head is created and
// the existing retry policy is untouched.
func TestHostStreamPreFirstDeltaErrorCreatesNoPlaceholder(t *testing.T) {
	fixture := newHostStreamFixture(t)
	fixture.chat.errors = []error{errors.New("stream rejected before any delta")}
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "重试也不会显示"}}

	if _, err := fixture.runner.Run(context.Background(), hostItem()); err == nil {
		t.Fatal("expected the pre-first-delta failure to fail the run")
	} else if !agentapp.IsRetryable(err) {
		t.Fatalf("pre-first-delta transient failure lost its retry policy: %v", err)
	}
	_, begins, updates, finishes, interrupts := fixture.stream.snapshot()
	if len(begins) != 0 || len(updates) != 0 || len(finishes) != 0 || len(interrupts) != 0 {
		t.Fatalf("a placeholder head was created before the first delta: begins=%d updates=%d finish=%d interrupt=%d",
			len(begins), len(updates), len(finishes), len(interrupts))
	}
}

// TestHostStreamPreFirstDeltaOverflowStillCompactsAndRetries pins that the
// compaction rescue survives T6: the retried turn opens exactly one head and
// streams the recovered reply into it.
func TestHostStreamPreFirstDeltaOverflowStillCompactsAndRetries(t *testing.T) {
	fixture := newHostStreamFixture(t)
	fixture.settings.ints["context.keep_recent_messages"] = 1
	snapshot := &fixture.stream.fakeConversationPort.snapshot
	snapshot.Summary = ""
	snapshot.PinnedFacts = nil
	snapshot.Messages = []agentapp.ConversationMessage{
		{ID: 661, Role: "user", Text: "较早需求一"},
		{ID: 662, Role: "user", Text: "较早需求二"},
		{ID: 663, Role: "user", Text: "较早需求三"},
		{ID: 664, Role: "user", Text: "最新需求"},
	}
	fixture.chat.errors = []error{fmt.Errorf("upstream: %w", llmapp.ErrContextOverflow)}
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "恢复后的回复"}}

	result, err := fixture.runner.Run(context.Background(), hostItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != agent.AgentRunDone {
		t.Fatalf("result = %#v", result)
	}
	_, begins, _, finishes, interrupts := fixture.stream.snapshot()
	if len(begins) != 1 || len(finishes) != 1 || len(interrupts) != 0 {
		t.Fatalf("begins=%d finish=%d interrupt=%d, want one head opened by the retried turn",
			len(begins), len(finishes), len(interrupts))
	}
	if text := partByType(finishes[0].Parts, "text"); text == nil || text.Text != "恢复后的回复" {
		t.Fatalf("recovered reply = %#v", finishes[0].Parts)
	}
	if !containsEvent(fixture.events, "context.compacted") {
		t.Fatalf("events = %v, want context.compacted", eventTypes(fixture.events))
	}
}

// fakeDeadlineContext is a deterministic stand-in for context.WithTimeout in the
// hard-deadline leg (H-E14). It reports context.DeadlineExceeded only after expire
// is called, so the deadline fires exactly when the test decides (after both deltas
// were emitted) instead of racing a real 30ms wall-clock timeout against a 120ms
// sleep — the flake this removes. Value delegates to the parent so the seal
// attribution survives; the parent here is a Background-derived value context that
// is never cancelled, so this type spawns no watcher goroutine and the
// goroutine-leak assertion stays meaningful.
type fakeDeadlineContext struct {
	parent  context.Context
	done    chan struct{}
	mu      sync.Mutex
	expired bool
}

func newFakeDeadlineContext(parent context.Context) *fakeDeadlineContext {
	return &fakeDeadlineContext{parent: parent, done: make(chan struct{})}
}

// expire deterministically trips the deadline (idempotent).
func (c *fakeDeadlineContext) expire() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.expired {
		c.expired = true
		close(c.done)
	}
}

func (c *fakeDeadlineContext) Deadline() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.expired {
		return time.Unix(0, 1), true
	}
	return time.Time{}, false
}

func (c *fakeDeadlineContext) Done() <-chan struct{} { return c.done }

func (c *fakeDeadlineContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.expired {
		return context.DeadlineExceeded
	}
	return c.parent.Err()
}

func (c *fakeDeadlineContext) Value(key any) any { return c.parent.Value(key) }

// TestHostStreamCancellationInterruptsHeadAndKeepsContent pins SC-D4's cancel
// propagation: cancelling the run aborts stream consumption, the accumulated
// content is written with a context that survives cancellation, the head is
// sealed interrupted and the writer-lease heartbeat goroutine is stopped.
func TestHostStreamCancellationInterruptsHeadAndKeepsContent(t *testing.T) {
	cases := []struct {
		name       string
		wantReason string
		wantStatus agent.AgentRunStatus
		deadline   bool
	}{
		// H-A12 (Q06 / C-4): reason=aborted lands agent_run.status=aborted, not
		// interrupted. The Host head still seals interrupted (stream_state), which
		// is independent of the run's terminal status.
		{name: "cancel", wantReason: "aborted", wantStatus: agent.AgentRunAborted},
		{name: "hard_deadline", wantReason: "interrupted", wantStatus: agent.AgentRunInterrupted, deadline: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newHostStreamFixture(t)
			parent := context.WithValue(context.Background(), hostStreamAttributionKey{}, "run-attribution")
			ctx, cancel := context.WithCancel(parent)
			defer cancel()
			// H-E14: the hard-deadline leg uses a FAKE clock — a context that reports
			// DeadlineExceeded only when the test expires it (after both deltas were
			// emitted), instead of a real 30ms timeout raced against a 120ms sleep.
			// The deadline fires exactly where a real SSE reader would notice it,
			// deterministically, so the leg never flakes on a slow machine.
			var runCtx context.Context
			fixture.chat.onStreamCtx = func(ctx context.Context) { runCtx = ctx }
			var expireDeadline func()
			if tc.deadline {
				fake := newFakeDeadlineContext(parent)
				ctx = fake
				expireDeadline = func() {
					fake.expire()
					// Deterministic barrier (no wall-clock sleep): the runner derives
					// its run context from the fake via context.WithTimeout, whose
					// cancellation propagates asynchronously. Wait for the deadline to
					// actually reach the run context — the same state a real SSE reader
					// observes before its next read — so the post-delta ctx.Err() check
					// is deterministic rather than racing the propagation goroutine.
					// Bounded (review Minor 2): a future wiring regression where the
					// runner no longer derives its run context from the passed ctx must
					// fail cleanly here, not spin to the package timeout. The bound is a
					// failure detector only — normal propagation is sub-millisecond, so
					// it never reintroduces timing flake.
					probeDeadline := time.Now().Add(5 * time.Second)
					for runCtx.Err() == nil {
						if time.Now().After(probeDeadline) {
							t.Fatal("fake deadline did not propagate to the run context within 5s (run-context wiring regression?)")
						}
						runtime.Gosched()
					}
				}
			}
			fixture.chat.streams = [][]llmapp.Delta{{
				{Content: "取消前已经显示的"},
				{Content: "内容"},
			}}
			// Both deltas reach the caller before the run is stopped, so the
			// interruption must preserve everything that was already shown.
			fixture.chat.onDelta = func(_ int, emitted int) {
				if emitted < 1 {
					return
				}
				if tc.deadline {
					expireDeadline()
					return
				}
				cancel()
			}

			before := runtime.NumGoroutine()
			result, err := fixture.runner.Run(ctx, hostItem())
			if err == nil {
				t.Fatal("expected the cancelled run to fail")
			}
			if result.Status != tc.wantStatus || result.TerminalReason != tc.wantReason {
				t.Fatalf("result = %#v, want %s/%s", result, tc.wantStatus, tc.wantReason)
			}
			// SC-D4 / R-D7: the reply is already visible, so the work item must
			// never be replayed — a retry would regenerate the whole reply into a
			// second head next to this interrupted one.
			if agentapp.IsRetryable(err) {
				t.Fatalf("a mid-stream %s is retryable at the work-item level: %v", tc.name, err)
			}
			_, _, _, finishes, interrupts := fixture.stream.snapshot()
			if len(finishes) != 0 || len(interrupts) != 1 {
				t.Fatalf("finish=%d interrupt=%d, want one interrupted seal and no complete seal", len(finishes), len(interrupts))
			}
			text := partByType(interrupts[0].Parts, "text")
			if text == nil || text.Text != "取消前已经显示的内容" {
				t.Fatalf("cancelled reply lost its accumulated content: %#v", interrupts[0].Parts)
			}
			errorPart := partByType(interrupts[0].Parts, "error")
			if errorPart == nil {
				t.Fatalf("cancelled reply has no error part: %#v", interrupts[0].Parts)
			}
			// The thread is user-facing: it must not leak the raw Go context
			// string the job card deliberately suppresses for an aborted run.
			message, _ := errorPart.Data["message"].(string)
			if strings.Contains(message, "context canceled") || strings.Contains(message, "context deadline exceeded") {
				t.Fatalf("interruption error part leaks a raw Go error string: %q", message)
			}
			if !strings.Contains(message, "已保留") {
				t.Fatalf("interruption error part is not a user-facing explanation: %q", message)
			}
			// The seal keeps the run attribution (WithoutCancel), not a bare
			// context.Background().
			if len(fixture.stream.sealAttribution) != 1 || fixture.stream.sealAttribution[0] != "run-attribution" {
				t.Fatalf("seal context attribution = %#v, want the run context value", fixture.stream.sealAttribution)
			}
			// Carried T3 review #4: the partial agent transcript must survive the
			// cancelled run context too (the fake repo honours ctx.Err()).
			var partial *agent.AgentMessage
			for index := range fixture.repo.messages {
				if fixture.repo.messages[index].Role == agent.MessageRoleAssistant {
					partial = &fixture.repo.messages[index]
				}
			}
			if partial == nil || len(partial.Parts) == 0 || partial.Parts[0].Text != "取消前已经显示的内容" {
				t.Fatalf("partial assistant transcript was not preserved under cancellation: %#v", fixture.repo.messages)
			}
			if fixture.stream.released != 1 {
				t.Fatalf("writer lease releases = %d, want 1 (heartbeat goroutine stopped)", fixture.stream.released)
			}
			deadline := time.Now().Add(3 * time.Second)
			for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if leaked := runtime.NumGoroutine() - before; leaked > 0 {
				t.Fatalf("goroutines leaked after the cancelled run: %d", leaked)
			}
		})
	}
}

// TestAbortedReasonWritesAbortedStatus pins the Q06 / C-4 wiring (H-A12): a run
// cancelled mid-stream classifies reason=aborted and the single terminal funnel
// (finish -> FinishRun) persists agent_run.status=aborted, NOT interrupted. The
// aborted enum member already has a read side (Agent Center AbortedCount filter)
// but had zero writers; this asserts the persisted row, not just the returned
// result, so reverting the mapping to interrupted fails here. reason=interrupted
// (hard deadline) keeps its own status and is covered by the cancellation table.
func TestAbortedReasonWritesAbortedStatus(t *testing.T) {
	fixture := newHostStreamFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fixture.chat.streams = [][]llmapp.Delta{{
		{Content: "取消前已经显示的"},
		{Content: "内容"},
	}}
	fixture.chat.onDelta = func(_ int, emitted int) {
		if emitted >= 1 {
			cancel()
		}
	}

	result, err := fixture.runner.Run(ctx, hostItem())
	if err == nil {
		t.Fatal("expected the cancelled run to fail")
	}
	if result.TerminalReason != "aborted" {
		t.Fatalf("terminal reason = %q, want aborted", result.TerminalReason)
	}
	if result.Status != agent.AgentRunAborted {
		t.Fatalf("result status = %q, want aborted", result.Status)
	}
	// The persisted agent_run row is the fact the Agent Center reads; assert it
	// carries aborted so the wiring is proven at the write path, not the return.
	persisted := fixture.repo.lastFinished(t)
	if persisted.Status != agent.AgentRunAborted {
		t.Fatalf("persisted agent_run.status = %q, want aborted (Q06 wiring)", persisted.Status)
	}
	if persisted.TerminalReason == nil || *persisted.TerminalReason != "aborted" {
		t.Fatalf("persisted terminal_reason = %v, want aborted", persisted.TerminalReason)
	}
	if !persisted.Status.Terminal() {
		t.Fatalf("aborted must be a terminal run status")
	}
}

// TestHostStreamRecoverySealsOrphanHeadsWithConnectionError pins SC-D4's crash
// path: acquiring the conversation writer lease takes over from a dead turn, so
// any head still streaming belongs to an orphan and is sealed interrupted with
// the "连接中断" error part before this run opens its own head.
func TestHostStreamRecoverySealsOrphanHeadsWithConnectionError(t *testing.T) {
	fixture := newHostStreamFixture(t)
	fixture.stream.orphaned = 2
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "新的一轮"}}

	if _, err := fixture.runner.Run(context.Background(), hostItem()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	calls, _, _, _, _ := fixture.stream.snapshot()
	if len(fixture.stream.recoveries) != 1 {
		t.Fatalf("orphan recovery calls = %d, want exactly 1 per Host lease acquisition", len(fixture.stream.recoveries))
	}
	if calls[0] != "recover" {
		t.Fatalf("call order = %v, want recovery before this run's head", calls)
	}
	errorPart := fixture.stream.recoveries[0]
	if errorPart.Type != "error" {
		t.Fatalf("recovery part type = %q, want error", errorPart.Type)
	}
	message, _ := errorPart.Data["message"].(string)
	if !strings.Contains(message, "连接中断") {
		t.Fatalf("recovery message = %q, want it to name 连接中断", message)
	}
	code, _ := errorPart.Data["code"].(string)
	if strings.TrimSpace(code) == "" {
		t.Fatalf("recovery error part has no stable code: %#v", errorPart.Data)
	}
}

// TestHostStreamUpdateFailureIsLoggedAndTerminalFrameReconciles pins
// constitution VII.6: a failed in-place update is logged (never silent) and the
// terminal full frame still carries every produced part, so no content is lost.
func TestHostStreamUpdateFailureIsLoggedAndTerminalFrameReconciles(t *testing.T) {
	logs := &lockedBuffer{}
	fixture := newHostStreamFixture(t, func(deps *agentapp.RunnerDeps) {
		deps.Logger = slog.New(slog.NewTextHandler(logs, nil))
	})
	fixture.stream.updateErr = errors.New("part row is temporarily unavailable")
	fixture.chat.streams = [][]llmapp.Delta{{
		{Content: "第一段"},
		{Content: "第二段"},
		{FinishReason: "stop"},
	}}

	result, err := fixture.runner.Run(context.Background(), hostItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != agent.AgentRunDone {
		t.Fatalf("result = %#v", result)
	}
	_, _, _, finishes, _ := fixture.stream.snapshot()
	if len(finishes) != 1 {
		t.Fatalf("complete seals = %d, want 1", len(finishes))
	}
	if text := partByType(finishes[0].Parts, "text"); text == nil || text.Text != "第一段第二段" {
		t.Fatalf("terminal frame lost content after a failed update: %#v", finishes[0].Parts)
	}
	if !strings.Contains(logs.String(), "stream part update failed") {
		t.Fatalf("the failed in-place update was silent: %s", logs.String())
	}
}

// TestHostStreamBeginFailureFallsBackLoudlyToBatch pins that an unavailable
// incremental head never silently swallows the reply: the fallback is logged
// and the batch writer still writes the full message.
func TestHostStreamBeginFailureFallsBackLoudlyToBatch(t *testing.T) {
	logs := &lockedBuffer{}
	fixture := newHostStreamFixture(t, func(deps *agentapp.RunnerDeps) {
		deps.Logger = slog.New(slog.NewTextHandler(logs, nil))
	})
	fixture.stream.beginErr = errors.New("conversation head is unavailable")
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "批量回退的回复"}}

	if _, err := fixture.runner.Run(context.Background(), hostItem()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(logs.String(), "incremental") {
		t.Fatalf("the incremental-write fallback was silent: %s", logs.String())
	}
	if len(fixture.stream.written) != 1 || fixture.stream.written[0].Text != "批量回退的回复" {
		t.Fatalf("batch fallback parts = %#v", fixture.stream.written)
	}
}

// TestNonHostProfileKeepsBatchWrite pins the T6 boundary: only the two Host
// conversation profiles write incrementally; the background design job keeps
// its team-room output and never opens a conversation head.
func TestNonHostProfileKeepsBatchWrite(t *testing.T) {
	fixture := newHostStreamFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "本轮设计说明"}}

	if _, err := fixture.runner.Run(context.Background(), designRequirementItem()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_, begins, updates, finishes, interrupts := fixture.stream.snapshot()
	if len(begins) != 0 || len(updates) != 0 || len(finishes) != 0 || len(interrupts) != 0 {
		t.Fatalf("a non-Host profile used the incremental conversation path: begins=%d updates=%d finish=%d interrupt=%d",
			len(begins), len(updates), len(finishes), len(interrupts))
	}
	if len(fixture.team.posts) != 1 {
		t.Fatalf("design author team-room posts = %d, want 1 (batch output unchanged)", len(fixture.team.posts))
	}
}

// TestHostStreamSoftBudgetSettlementSealsComplete pins the soft-timeout/turn
// winding path (SC-D4): a soft budget does not abort the stream, it opens the
// tool-less settlement turn, and the head is sealed COMPLETE with the wrap-up
// reply — an interrupted seal there would misreport a finished run.
func TestHostStreamSoftBudgetSettlementSealsComplete(t *testing.T) {
	fixture := newHostStreamFixture(t)
	fixture.settings.ints["agent.max_turns"] = 2
	fixture.chat.responses = []llmapp.ChatResponse{
		toolCall(tools.ToolContextVersionPack, `{}`),
		{Content: "预算收尾的回复"},
	}

	result, err := fixture.runner.Run(context.Background(), hostItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != agent.AgentRunDone || result.TerminalReason != agentapp.BudgetCauseTurns {
		t.Fatalf("result = %#v, want done/%s", result, agentapp.BudgetCauseTurns)
	}
	_, _, _, finishes, interrupts := fixture.stream.snapshot()
	if len(finishes) != 1 || len(interrupts) != 0 {
		t.Fatalf("finish=%d interrupt=%d, want the settlement reply sealed complete", len(finishes), len(interrupts))
	}
	if text := partByType(finishes[0].Parts, "text"); text == nil || text.Text != "预算收尾的回复" {
		t.Fatalf("settlement reply = %#v", finishes[0].Parts)
	}
	if partByType(finishes[0].Parts, "tool_call") == nil {
		t.Fatalf("the first turn's tool call was lost: %#v", finishes[0].Parts)
	}
}

// TestDesignHostStreamUsesSameLifecycle pins SC-D2's second interface: the
// design Host conversation streams through the same head/seq/seal lifecycle as
// the clarification Host, with its own lease identity.
func TestDesignHostStreamUsesSameLifecycle(t *testing.T) {
	fixture := newHostStreamFixture(t)
	fixture.stream.fakeConversationPort.kind = "design"
	fixture.chat.streams = [][]llmapp.Delta{{
		{Content: "设计对话的"},
		{Content: "增量回复"},
		{FinishReason: "stop"},
	}}
	item := fakeWorkItem(agentapp.WorkTypeChatTurn, agentapp.WorkItemPayload{ConversationID: 20, Trigger: "opening"})

	result, err := fixture.runner.Run(context.Background(), item)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	_, begins, updates, finishes, interrupts := fixture.stream.snapshot()
	if len(begins) != 1 || begins[0].Agent != agentapp.ProfileKeyDesignHost || begins[0].TurnID != result.AgentRunID {
		t.Fatalf("begin = %#v, want design_host with turn=run", begins)
	}
	assertContiguousSeqs(t, updates, 1)
	assertGrowingPrefixChain(t, "design host text", seqTexts(updates, 1), "设计对话的增量回复")
	if len(finishes) != 1 || len(interrupts) != 0 {
		t.Fatalf("finish=%d interrupt=%d, want one complete seal", len(finishes), len(interrupts))
	}
	if len(fixture.stream.written) != 0 {
		t.Fatalf("design host also used the batch writer: %#v", fixture.stream.written)
	}
}

// TestHostStreamSealFailureNeverReplaysTheReply pins review Important #1: once
// the head carries the reply, a failed terminal seal must NOT be retryable at
// the work-item level. A retry reopens the item, runs the model again and writes
// a SECOND head with the same reply next to the interrupted one (SC-D4/R-D7).
func TestHostStreamSealFailureNeverReplaysTheReply(t *testing.T) {
	fixture := newHostStreamFixture(t)
	fixture.stream.finishErr = errors.New("conversation head seal is temporarily unavailable")
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "已经显示给用户的回复"}}

	result, err := fixture.runner.Run(context.Background(), hostItem())
	if err == nil {
		t.Fatal("expected the run to fail when the terminal seal fails")
	}
	if agentapp.IsRetryable(err) {
		t.Fatalf("a failed seal after visible content is retryable (would duplicate the reply): %v", err)
	}
	if result.Status != agent.AgentRunFailed {
		t.Fatalf("result = %#v, want failed", result)
	}
	_, begins, _, _, interrupts := fixture.stream.snapshot()
	if len(begins) != 1 {
		t.Fatalf("heads opened = %d, want exactly 1 (no replay)", len(begins))
	}
	if len(interrupts) != 1 {
		t.Fatalf("interrupt seals = %d, want the terminal funnel to seal the head", len(interrupts))
	}
	if text := partByType(interrupts[0].Parts, "text"); text == nil || text.Text != "已经显示给用户的回复" {
		t.Fatalf("the visible reply was not preserved by the interrupted seal: %#v", interrupts[0].Parts)
	}
	if fixture.chat.requestCount() != 1 {
		t.Fatalf("model calls = %d, want 1 (no regeneration)", fixture.chat.requestCount())
	}
}

// TestHostStreamProducedPartCapStillSealsTheReply pins review Important #3: the
// produced-part cap bounds the TRACE, never the model reply (appendStreamPart's
// own contract). A Host run that fills the cap must still seal its reply text,
// and the truncation error part's "最终回复不受影响" must be true.
func TestHostStreamProducedPartCapStillSealsTheReply(t *testing.T) {
	fixture := newHostStreamFixture(t)
	// 100 distinct disallowed tools produce 100 tool_call parts + 100 distinct
	// error parts = the produced-part cap, without failing the run.
	calls := make([]llmapp.ToolCall, 0, 100)
	for index := 0; index < 100; index++ {
		calls = append(calls, llmapp.ToolCall{
			ID:        fmt.Sprintf("call_%d", index),
			Name:      fmt.Sprintf("unknown.tool_%d", index),
			Arguments: `{}`,
		})
	}
	fixture.chat.responses = []llmapp.ChatResponse{
		{ToolCalls: calls, FinishReason: "tool_calls"},
		{Content: "上限之后的最终回复", FinishReason: "stop"},
	}

	result, err := fixture.runner.Run(context.Background(), hostItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != agent.AgentRunDone {
		t.Fatalf("result = %#v", result)
	}
	_, _, _, finishes, interrupts := fixture.stream.snapshot()
	if len(finishes) != 1 || len(interrupts) != 0 {
		t.Fatalf("finish=%d interrupt=%d, want one complete seal", len(finishes), len(interrupts))
	}
	text := partByType(finishes[0].Parts, "text")
	if text == nil || text.Text != "上限之后的最终回复" {
		t.Fatalf("the produced-part cap dropped the model reply from the seal: %#v", finishes[0].Parts)
	}
	var truncation *agentapp.ConversationPart
	for index := range finishes[0].Parts {
		if finishes[0].Parts[index].Type == "error" &&
			finishes[0].Parts[index].Data["code"] == "stream_parts_truncated" {
			truncation = &finishes[0].Parts[index]
		}
	}
	if truncation == nil {
		t.Fatalf("the cap hit produced no explicit truncation signal: %#v", finishes[0].Parts)
	}
}

// TestHostStreamOversizedReplySealsTheStoredPrefix pins review Important #3's
// robustness clause: a reply that outgrows the message contract must not leave a
// frozen streaming head. When the full snapshot is refused, the seal falls back
// to exactly the prefix the head already stores plus an explicit error part —
// nothing the user already saw is deleted, and the head is never left streaming.
func TestHostStreamOversizedReplySealsTheStoredPrefix(t *testing.T) {
	fixture := newHostStreamFixture(t)
	oversized := strings.Repeat("超", version.MessageTextMax+1)
	fixture.chat.streams = [][]llmapp.Delta{{
		{Content: "已经保存的前缀"},
		{Content: oversized},
		{FinishReason: "stop"},
	}}

	result, err := fixture.runner.Run(context.Background(), hostItem())
	if err == nil {
		t.Fatal("expected the oversized reply to fail the run explicitly")
	}
	if agentapp.IsRetryable(err) {
		t.Fatalf("an oversized visible reply is retryable (would duplicate it): %v", err)
	}
	if result.Status != agent.AgentRunFailed {
		t.Fatalf("result = %#v, want failed", result)
	}
	_, _, _, _, interrupts := fixture.stream.snapshot()
	if len(interrupts) == 0 {
		t.Fatal("the oversized reply left the head streaming (no interrupted seal)")
	}
	sealed := interrupts[len(interrupts)-1]
	text := partByType(sealed.Parts, "text")
	if text == nil || text.Text != "已经保存的前缀" {
		t.Fatalf("salvage seal dropped the stored prefix: %#v", sealed.Parts)
	}
	if partByType(sealed.Parts, "error") == nil {
		t.Fatalf("salvage seal has no explicit error part: %#v", sealed.Parts)
	}
	if err := validateStreamParts(sealed.Parts); err != nil {
		t.Fatalf("salvage seal is itself an invalid payload: %v", err)
	}
}

// TestHostStreamCompleteSealKeepsRunAttribution pins the seal-context minor: a
// successful seal must carry the run context values (WithoutCancel), not a bare
// context.Background().
func TestHostStreamCompleteSealKeepsRunAttribution(t *testing.T) {
	fixture := newHostStreamFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "正常回复"}}
	ctx := context.WithValue(context.Background(), hostStreamAttributionKey{}, "run-attribution")

	if _, err := fixture.runner.Run(ctx, hostItem()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fixture.stream.sealAttribution) != 1 || fixture.stream.sealAttribution[0] != "run-attribution" {
		t.Fatalf("seal context attribution = %#v, want the run context value", fixture.stream.sealAttribution)
	}
}
