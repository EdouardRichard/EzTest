package agentapp_test

// H-E11 / R-H7 (task T28c): the streaming transcript row keeps publishing one
// agent.transcript.delta per model delta — offset/increment semantics are
// unchanged — but its DB rewrite is COALESCED on a bounded cadence. T28b measured
// a real 1437-delta turn rewriting the full row on every delta (~130-142
// rewrites/s, quadratic under concurrent runs).
//
// The expectations come from the T28c contract:
//  1. N deltas cost far fewer than N row updates;
//  2. no content is lost — the sealed row equals the concatenated reply and every
//     stored snapshot is an already-published prefix;
//  3. every seal/terminal boundary writes the final text first;
//  4. the event stream is byte-identical to the per-delta implementation;
//  5. concurrent runs keep their own coalescing state;
//  6. (mutations) a zero cadence or a removed flush turns 1 / 3 red.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// transcriptUpdateText is the text a stored transcript snapshot carries.
func transcriptUpdateText(update agent.AgentMessage) string {
	if len(update.Parts) == 0 {
		return ""
	}
	return update.Parts[0].Text
}

// contentDeltas builds one stream of content pieces plus the terminal delta and
// returns the concatenated reply.
func contentDeltas(pieces ...string) ([]llmapp.Delta, string) {
	deltas := make([]llmapp.Delta, 0, len(pieces)+1)
	var text strings.Builder
	for _, piece := range pieces {
		deltas = append(deltas, llmapp.Delta{Content: piece})
		text.WriteString(piece)
	}
	deltas = append(deltas, llmapp.Delta{FinishReason: "stop"})
	return deltas, text.String()
}

// TestTranscriptPersistenceIsCoalesced pins assertion 1: a 500-delta reply
// rewrites the row far fewer than 500 times.
func TestTranscriptPersistenceIsCoalesced(t *testing.T) {
	const deltaCount = 500
	deltas := make([]llmapp.Delta, 0, deltaCount+1)
	var reply strings.Builder
	for i := 0; i < deltaCount; i++ {
		piece := string(rune('a' + i%26))
		deltas = append(deltas, llmapp.Delta{Content: piece})
		reply.WriteString(piece)
	}
	deltas = append(deltas, llmapp.Delta{FinishReason: "stop"})

	fixture := newWiringFixture(t)
	fixture.chat.streams = [][]llmapp.Delta{deltas}

	result, err := fixture.runner.Run(context.Background(), designRequirementItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := assistantRows(fixture, result.AgentRunID)
	if len(rows) != 1 || rows[0].Parts[0].Text != reply.String() {
		t.Fatalf("sealed row = %#v, want the whole reply", rows)
	}

	updates := fixture.repo.transcriptUpdates()
	t.Logf("%d deltas -> %d in-place updates", deltaCount, len(updates))
	// The frozen test clock never trips the 250ms cadence, so only the 32-delta
	// cap and the seal boundaries write: ~1 + 500/32 + 2. A bound of less than a
	// quarter of the deltas is the discriminating assertion (the per-delta
	// implementation writes 501).
	if len(updates) >= deltaCount/4 {
		t.Fatalf("in-place updates = %d for %d deltas, want a coalesced cadence well below %d",
			len(updates), deltaCount, deltaCount/4)
	}
	if len(updates) < 2 {
		t.Fatalf("in-place updates = %d, want at least the opening flush and the seal", len(updates))
	}
}

// TestTranscriptCoalescingLosesNoContent pins assertion 2: the sealed row is the
// concatenated reply and every stored snapshot is an already-published prefix.
// The stored texts are checked against the texts the event stream actually
// published, not against the final reply alone.
func TestTranscriptCoalescingLosesNoContent(t *testing.T) {
	pieces := make([]string, 0, 120)
	for i := 0; i < 120; i++ {
		pieces = append(pieces, string(rune('A'+i%26)), "·")
	}
	deltas, reply := contentDeltas(pieces...)

	fixture := newWiringFixture(t)
	fixture.chat.streams = [][]llmapp.Delta{deltas}

	result, err := fixture.runner.Run(context.Background(), designRequirementItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := assistantRows(fixture, result.AgentRunID)
	if len(rows) != 1 {
		t.Fatalf("assistant rows = %d, want 1", len(rows))
	}
	if got := transcriptUpdateText(rows[0]); got != reply {
		t.Fatalf("sealed text = %q, want the concatenated reply (%d chars)", got, len(reply))
	}

	// Rebuild every text the event stream published, then require each stored
	// snapshot to be one of them.
	published := map[string]bool{}
	text := ""
	for _, delta := range transcriptDeltasFor(t, fixture, result.AgentRunID, rows[0].ID) {
		switch {
		case delta.Increment != "":
			text += delta.Increment
		case delta.Message != nil:
			text = ""
			if len(delta.Message.Parts) > 0 {
				text = delta.Message.Parts[0].Text
			}
		}
		published[text] = true
	}
	if !published[reply] {
		t.Fatalf("the event stream never published the final reply %q", reply)
	}
	previous := ""
	for index, update := range fixture.repo.transcriptUpdates() {
		stored := transcriptUpdateText(update)
		if !published[stored] {
			t.Fatalf("stored snapshot #%d %q is not an already-published prefix", index, stored)
		}
		if !strings.HasPrefix(stored, previous) {
			t.Fatalf("stored snapshot #%d %q does not extend the previous %q", index, stored, previous)
		}
		previous = stored
	}
	if previous != reply {
		t.Fatalf("last stored snapshot = %q, want the whole reply", previous)
	}
}

// TestTranscriptFlushBeforeSealCarriesFinalText pins assertion 3 for both turn
// ends: a normal seal and an interrupted (post-first-delta failure) turn must
// write the final text BEFORE the terminal seal. With coalescing the last few
// deltas are normally unwritten, so this is the flush the cadence must not skip.
func TestTranscriptFlushBeforeSealCarriesFinalText(t *testing.T) {
	pieces := []string{"一", "二", "三", "四", "五", "六", "七", "八"}
	final := strings.Join(pieces, "")

	t.Run("complete seal", func(t *testing.T) {
		deltas, _ := contentDeltas(pieces...)
		fixture := newWiringFixture(t)
		fixture.chat.streams = [][]llmapp.Delta{deltas}

		result, err := fixture.runner.Run(context.Background(), designRequirementItem())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		rows := assistantRows(fixture, result.AgentRunID)
		if len(rows) != 1 || rows[0].StreamState != agent.MessageStreamStateComplete {
			t.Fatalf("rows = %#v, want one complete row", rows)
		}
		assertFlushedBeforeTerminalWrite(t, fixture.repo.transcriptUpdates(), final)
	})

	t.Run("interrupted turn", func(t *testing.T) {
		deltas := make([]llmapp.Delta, 0, len(pieces))
		for _, piece := range pieces {
			deltas = append(deltas, llmapp.Delta{Content: piece})
		}
		fixture := newWiringFixture(t)
		fixture.chat.streams = [][]llmapp.Delta{deltas}
		fixture.chat.errors = []error{errors.New("upstream closed")}

		result, err := fixture.runner.Run(context.Background(), designRequirementItem())
		if err == nil {
			t.Fatal("a post-first-delta stream failure must fail the run")
		}
		rows := assistantRows(fixture, result.AgentRunID)
		if len(rows) != 1 || rows[0].StreamState != agent.MessageStreamStateInterrupted {
			t.Fatalf("rows = %#v, want one interrupted row", rows)
		}
		if got := transcriptUpdateText(rows[0]); got != final {
			t.Fatalf("interrupted text = %q, want %q", got, final)
		}
		assertFlushedBeforeTerminalWrite(t, fixture.repo.transcriptUpdates(), final)
	})
}

// assertFlushedBeforeTerminalWrite requires a streaming-state snapshot carrying
// the final text before the first terminal write of the row.
func assertFlushedBeforeTerminalWrite(t *testing.T, updates []agent.AgentMessage, final string) {
	t.Helper()
	sawStreamingFinal := false
	for _, update := range updates {
		if update.StreamState == agent.MessageStreamStateStreaming {
			if transcriptUpdateText(update) == final {
				sawStreamingFinal = true
			}
			continue
		}
		if !sawStreamingFinal {
			t.Fatalf("terminal write (%s, %q) arrived before a flush of the final text %q",
				update.StreamState, transcriptUpdateText(update), final)
		}
	}
	if !sawStreamingFinal {
		t.Fatalf("no streaming snapshot carried the final text %q; the seal skipped the flush", final)
	}
}

// TestTranscriptTerminalSealCarriesUnflushedText pins assertion 3's backstop: a
// run whose turn seal is lost must still terminate with the latest published
// text (which coalescing may not have written yet).
func TestTranscriptTerminalSealCarriesUnflushedText(t *testing.T) {
	pieces := []string{"已写", "但未", "落库", "的最后一段"}
	final := strings.Join(pieces, "")
	deltas, _ := contentDeltas(pieces...)

	fixture := newWiringFixture(t)
	fixture.chat.streams = [][]llmapp.Delta{deltas}
	fixture.repo.failNextSeals(1)

	result, err := fixture.runner.Run(context.Background(), designRequirementItem())
	if err == nil {
		t.Fatal("a lost seal write must fail the run")
	}
	rows := assistantRows(fixture, result.AgentRunID)
	if len(rows) != 1 || rows[0].StreamState != agent.MessageStreamStateInterrupted {
		t.Fatalf("rows = %#v, want one interrupted row", rows)
	}
	if got := transcriptUpdateText(rows[0]); got != final {
		t.Fatalf("terminal row text = %q, want the latest published text %q", got, final)
	}
	for _, update := range fixture.repo.transcriptUpdates() {
		if update.StreamState == agent.MessageStreamStateComplete {
			t.Fatal("the turn's complete seal landed; this test no longer drives the terminal backstop")
		}
	}
}

// TestTranscriptEventsUnchangedByCoalescedPersistence pins assertion 4: the
// exact per-delta frame sequence (kind, offset, increment, order) is the T8
// contract and coalescing only changes DB writes.
func TestTranscriptEventsUnchangedByCoalescedPersistence(t *testing.T) {
	deltas := []llmapp.Delta{
		{Content: "a"},
		{Content: "b"},
		{Content: "😀"},
		{Content: "c"},
		{FinishReason: "stop"},
	}
	fixture := newWiringFixture(t)
	fixture.chat.streams = [][]llmapp.Delta{deltas}

	result, err := fixture.runner.Run(context.Background(), designRequirementItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := assistantRows(fixture, result.AgentRunID)
	if len(rows) != 1 {
		t.Fatalf("assistant rows = %d, want 1", len(rows))
	}
	frames := transcriptDeltasFor(t, fixture, result.AgentRunID, rows[0].ID)
	if len(frames) != len(deltas)+1 {
		t.Fatalf("frames = %d, want the placeholder + one per content delta + the seal (%d)",
			len(frames), len(deltas)+1)
	}
	if frames[0].Message == nil || frames[0].StreamState != agent.MessageStreamStateStreaming {
		t.Fatalf("first frame = %#v, want the streaming placeholder snapshot", frames[0])
	}
	wantIncrements := []struct {
		offset int
		text   string
	}{
		{0, "a"}, {1, "b"}, {2, "😀"}, {4, "c"},
	}
	for index, want := range wantIncrements {
		frame := frames[index+1]
		if frame.Message != nil {
			t.Fatalf("frame %d = %#v, want an increment, not a snapshot", index+1, frame)
		}
		if frame.Seq != 1 || frame.PartType != "text" {
			t.Fatalf("frame %d seq/part_type = %d/%q, want 1/text", index+1, frame.Seq, frame.PartType)
		}
		if frame.Offset != want.offset || frame.Increment != want.text {
			t.Fatalf("frame %d offset/increment = %d/%q, want %d/%q",
				index+1, frame.Offset, frame.Increment, want.offset, want.text)
		}
		if frame.StreamState != agent.MessageStreamStateStreaming {
			t.Fatalf("frame %d stream_state = %q, want streaming", index+1, frame.StreamState)
		}
	}
	last := frames[len(frames)-1]
	if last.Message == nil || last.StreamState != agent.MessageStreamStateComplete {
		t.Fatalf("terminal frame = %#v, want a complete snapshot", last)
	}
	if len(last.Message.Parts) != 1 || last.Message.Parts[0].Text != "ab😀c" {
		t.Fatalf("terminal frame parts = %#v, want the whole reply", last.Message.Parts)
	}
}

// lockedPublisher is a goroutine-safe eventing publisher for the concurrent-run
// test (the shared fakePublisher is not synchronized).
type lockedPublisher struct {
	mu     sync.Mutex
	events []eventingapp.Event
}

func (p *lockedPublisher) Publish(_ context.Context, event eventingapp.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
	return nil
}

// TestTranscriptFlushIntervalBoundsReloadLag pins the interval half of the
// cadence: when the stream is slow (more than one flush interval between
// deltas), every delta is persisted, so a reload mid-reply lags the published
// stream by at most one interval — never by the whole turn.
func TestTranscriptFlushIntervalBoundsReloadLag(t *testing.T) {
	pieces := []string{"a", "b", "c", "d", "e"}
	deltas, reply := contentDeltas(pieces...)

	fixture := newWiringFixture(t)
	fixture.chat.streams = [][]llmapp.Delta{deltas}
	// Advance the injectable clock past the flush interval between deltas.
	fixture.chat.onDelta = func(int, int) { fixture.clock.Advance(300 * time.Millisecond) }

	result, err := fixture.runner.Run(context.Background(), designRequirementItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := assistantRows(fixture, result.AgentRunID)
	if len(rows) != 1 || transcriptUpdateText(rows[0]) != reply {
		t.Fatalf("sealed row = %#v, want the whole reply", rows)
	}
	snapshots := 0
	for _, update := range fixture.repo.transcriptUpdates() {
		if update.StreamState == agent.MessageStreamStateStreaming {
			snapshots++
		}
	}
	if snapshots < len(pieces) {
		t.Fatalf("streaming snapshots = %d for %d deltas spaced beyond the flush interval, want one per delta",
			snapshots, len(pieces))
	}
}

// TestTranscriptCoalescingIsPerRunUnderConcurrency pins assertion 5: concurrent
// runs sharing one repository keep independent coalescing state and never write
// another run's row.
func TestTranscriptCoalescingIsPerRunUnderConcurrency(t *testing.T) {
	const (
		runCount   = 4
		deltaCount = 200
	)
	sharedRepo := newFakeRuntimeRepo()
	if _, err := agentapp.EnsureProfiles(context.Background(), sharedRepo); err != nil {
		t.Fatalf("seed shared profiles: %v", err)
	}
	sharedIDs := &seqIDGenerator{}
	publisher := &lockedPublisher{}

	type harness struct {
		fixture *wiringFixture
		text    string
	}
	harnesses := make([]*harness, runCount)
	for index := range harnesses {
		deltas := make([]llmapp.Delta, 0, deltaCount+1)
		var text strings.Builder
		marker := string(rune('A' + index))
		for i := 0; i < deltaCount; i++ {
			deltas = append(deltas, llmapp.Delta{Content: marker})
			text.WriteString(marker)
		}
		deltas = append(deltas, llmapp.Delta{FinishReason: "stop"})
		fixture := newWiringFixture(t, func(deps *agentapp.RunnerDeps) {
			deps.Runtime = sharedRepo
			deps.IDs = sharedIDs
			deps.Events = publisher
		})
		fixture.chat.streams = [][]llmapp.Delta{deltas}
		harnesses[index] = &harness{fixture: fixture, text: text.String()}
	}

	results := make([]int64, runCount)
	errs := make([]error, runCount)
	var wait sync.WaitGroup
	for index := range harnesses {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			result, err := harnesses[index].fixture.runner.Run(context.Background(), designRequirementItem())
			results[index] = result.AgentRunID
			errs[index] = err
		}(index)
	}
	wait.Wait()

	rows := sharedRepo.transcript()
	runOfMessage := map[int64]int64{}
	for _, row := range rows {
		if row.RunID != nil {
			runOfMessage[row.ID] = *row.RunID
		}
	}
	updatesByRun := map[int64]int{}
	for _, update := range sharedRepo.transcriptUpdates() {
		updatesByRun[runOfMessage[update.ID]]++
	}
	for index := range harnesses {
		if errs[index] != nil {
			t.Fatalf("run %d failed: %v", index, errs[index])
		}
		runID := results[index]
		var assistant []agent.AgentMessage
		for _, row := range rows {
			if row.RunID != nil && *row.RunID == runID && row.Role == agent.MessageRoleAssistant {
				assistant = append(assistant, row)
			}
		}
		if len(assistant) != 1 {
			t.Fatalf("run %d assistant rows = %d, want 1", index, len(assistant))
		}
		if got := transcriptUpdateText(assistant[0]); got != harnesses[index].text {
			t.Fatalf("run %d sealed text = %q, want its own %q", index, got, harnesses[index].text)
		}
		if assistant[0].StreamState != agent.MessageStreamStateComplete {
			t.Fatalf("run %d stream_state = %q, want complete", index, assistant[0].StreamState)
		}
		count := updatesByRun[runID]
		t.Logf("run %d: %d deltas -> %d in-place updates", index, deltaCount, count)
		if count >= deltaCount/4 {
			t.Fatalf("run %d in-place updates = %d for %d deltas, want an independent coalesced cadence",
				index, count, deltaCount)
		}
	}
}
