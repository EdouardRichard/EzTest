package agentapp_test

// v0.2.4-D T8 (SC-D3/R-D5): the agent transcript (agent_message) streams for
// EVERY profile, not only the Host conversations. One assistant row per model
// turn is opened at the first delta, rewritten in place while the model streams,
// and sealed at the turn's end; each write publishes one agent.transcript.delta
// on the run:{id} channel.
//
// The expectations below come from the T8 contract (R-D2 increment granularity,
// the shared stream-state authority, terminal freeze) — never from observed
// runner output.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// transcriptDeltas returns the run-channel transcript events of one run.
func transcriptDeltas(t *testing.T, fixture *wiringFixture, runID int64) []agentapp.TranscriptDelta {
	t.Helper()
	wantChannel := "run:" + fmtInt(runID)
	var deltas []agentapp.TranscriptDelta
	for _, event := range fixture.events.events {
		if event.Type != agentapp.TranscriptDeltaEventType {
			continue
		}
		if event.Channel != wantChannel {
			t.Fatalf("transcript delta channel = %q, want %q", event.Channel, wantChannel)
		}
		raw, err := json.Marshal(event.Payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		var delta agentapp.TranscriptDelta
		if err := json.Unmarshal(raw, &delta); err != nil {
			t.Fatalf("decode payload %s: %v", raw, err)
		}
		deltas = append(deltas, delta)
	}
	return deltas
}

func fmtInt(value int64) string { return strconv.FormatInt(value, 10) }

// transcriptDeltasFor narrows the run's frames to one transcript row: the same
// channel also carries the run's system/user/tool rows.
func transcriptDeltasFor(t *testing.T, fixture *wiringFixture, runID, messageID int64) []agentapp.TranscriptDelta {
	t.Helper()
	var frames []agentapp.TranscriptDelta
	for _, delta := range transcriptDeltas(t, fixture, runID) {
		if delta.MessageID == fmtInt(messageID) {
			frames = append(frames, delta)
		}
	}
	return frames
}

// assistantRows returns the persisted assistant transcript rows of one run.
func assistantRows(fixture *wiringFixture, runID int64) []agent.AgentMessage {
	var rows []agent.AgentMessage
	for _, message := range fixture.repo.transcript() {
		if message.RunID != nil && *message.RunID == runID && message.Role == agent.MessageRoleAssistant {
			rows = append(rows, message)
		}
	}
	return rows
}

// replayTranscript applies the delta stream the way the frontend must: append
// mode extends the part's text at offset, snapshot mode replaces the whole row.
// It returns the text of the row's first part after every frame was applied.
func replayTranscript(t *testing.T, deltas []agentapp.TranscriptDelta) (string, string) {
	t.Helper()
	text := ""
	state := ""
	for _, delta := range deltas {
		state = delta.StreamState
		switch {
		case delta.Increment != "":
			if delta.Seq != 1 {
				t.Fatalf("append delta seq = %d, want the first part (1)", delta.Seq)
			}
			if delta.Message != nil {
				t.Fatal("a delta carries both an increment and a full snapshot")
			}
			if got := len(utf16.Encode([]rune(text))); got != delta.Offset {
				t.Fatalf("append offset = %d, want the UTF-16 length %d of %q", delta.Offset, got, text)
			}
			text += delta.Increment
		case delta.Message != nil:
			if delta.Increment != "" || delta.Seq != 0 {
				t.Fatalf("snapshot delta also carries append fields: %#v", delta)
			}
			text = ""
			if len(delta.Message.Parts) > 0 {
				text = delta.Message.Parts[0].Text
			}
		default:
			t.Fatalf("delta carries neither an increment nor a snapshot: %#v", delta)
		}
	}
	return text, state
}

// TestTranscriptStreamsInPlaceForNonHostProfile is the core T8 assertion: a
// non-Host profile (design_author) writes ONE assistant row per turn, updated in
// place, and seals it complete — no per-delta row growth, no batch duplicate.
func TestTranscriptStreamsInPlaceForNonHostProfile(t *testing.T) {
	fixture := newWiringFixture(t)
	fixture.chat.streams = [][]llmapp.Delta{{
		{Content: "本轮"},
		{Content: "设计"},
		{Content: "说明"},
		{FinishReason: "stop", Usage: map[string]int{"output_tokens": 9}},
	}}

	result, err := fixture.runner.Run(context.Background(), designRequirementItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := assistantRows(fixture, result.AgentRunID)
	if len(rows) != 1 {
		t.Fatalf("assistant rows = %d, want exactly 1 per model turn", len(rows))
	}
	row := rows[0]
	if len(row.Parts) != 1 || row.Parts[0].Type != "text" || row.Parts[0].Text != "本轮设计说明" {
		t.Fatalf("final parts = %#v, want the whole reply in one text part", row.Parts)
	}
	if row.StreamState != agent.MessageStreamStateComplete {
		t.Fatalf("stream_state = %q, want complete after the turn", row.StreamState)
	}
	if row.Usage["output_tokens"] != 9 {
		t.Fatalf("usage = %#v, want the provider snapshot on the same row", row.Usage)
	}

	updates := fixture.repo.transcriptUpdates()
	if len(updates) < 3 {
		t.Fatalf("in-place updates = %d, want one per growing delta", len(updates))
	}
	previous := ""
	for _, update := range updates {
		if update.ID != row.ID {
			t.Fatalf("update targets row %d, want the streaming head %d", update.ID, row.ID)
		}
		if len(update.Parts) == 0 {
			continue
		}
		text := update.Parts[0].Text
		if !strings.HasPrefix(text, previous) {
			t.Fatalf("snapshot %q does not extend %q (updates carry the full snapshot)", text, previous)
		}
		previous = text
	}
	if previous != "本轮设计说明" {
		t.Fatalf("last stored snapshot = %q, want the whole reply", previous)
	}
}

// TestTranscriptDeltaIsIncrementalAndReplaysToTheStoredRow pins R-D2: the event
// body carries only the increment while the DB holds the full snapshot, and
// replaying the frames reproduces the stored row exactly.
func TestTranscriptDeltaIsIncrementalAndReplaysToTheStoredRow(t *testing.T) {
	fixture := newWiringFixture(t)
	fixture.chat.streams = [][]llmapp.Delta{{
		{Content: "你好"},
		{Content: "，世界"},
		{Content: "🙂"},
		{FinishReason: "stop"},
	}}

	result, err := fixture.runner.Run(context.Background(), designRequirementItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := assistantRows(fixture, result.AgentRunID)
	if len(rows) != 1 {
		t.Fatalf("assistant rows = %d, want 1", len(rows))
	}
	deltas := transcriptDeltasFor(t, fixture, result.AgentRunID, rows[0].ID)
	if len(deltas) < 4 {
		t.Fatalf("deltas = %d, want the placeholder + one per content delta + the seal", len(deltas))
	}
	if deltas[0].Message == nil || deltas[0].StreamState != agent.MessageStreamStateStreaming {
		t.Fatalf("first frame = %#v, want a streaming placeholder snapshot", deltas[0])
	}
	if deltas[0].RunID != fmtInt(result.AgentRunID) {
		t.Fatalf("delta run_id = %q, want %d", deltas[0].RunID, result.AgentRunID)
	}
	appends := 0
	for _, delta := range deltas[1 : len(deltas)-1] {
		if delta.Increment == "" {
			t.Fatalf("mid-stream frame is not an increment: %#v", delta)
		}
		if delta.PartType != "text" {
			t.Fatalf("increment part_type = %q, want text", delta.PartType)
		}
		if delta.StreamState != agent.MessageStreamStateStreaming {
			t.Fatalf("mid-stream stream_state = %q, want streaming", delta.StreamState)
		}
		appends++
	}
	if appends != 3 {
		t.Fatalf("increment frames = %d, want 3 (one per content delta)", appends)
	}
	last := deltas[len(deltas)-1]
	if last.Message == nil || last.StreamState != agent.MessageStreamStateComplete {
		t.Fatalf("terminal frame = %#v, want a complete full snapshot", last)
	}

	text, state := replayTranscript(t, deltas)
	if text != "你好，世界🙂" || state != agent.MessageStreamStateComplete {
		t.Fatalf("replay = %q/%q, want the stored reply/complete", text, state)
	}
	if rows[0].Parts[0].Text != text {
		t.Fatalf("stored row %#v disagrees with the replayed stream %q", rows, text)
	}
	if last.Message.ID != fmtInt(rows[0].ID) {
		t.Fatalf("terminal frame message id = %q, want the stored row %d", last.Message.ID, rows[0].ID)
	}
}

// TestTranscriptStreamOffsetAstralPlane is the T14 astral discriminator: an astral
// rune in the prefix contributes 2 UTF-16 code units to the continuation's append
// offset — never the rune count (1) nor the byte count (4). It pins the offset
// SEMANTICS through the real call site (textunit.UTF16Length), so it stays green
// under an m1-type duplicate regression and only reds if the meaning drifts.
func TestTranscriptStreamOffsetAstralPlane(t *testing.T) {
	fixture := newWiringFixture(t)
	fixture.chat.streams = [][]llmapp.Delta{{
		{Content: "😀"},
		{Content: "尾巴"},
		{FinishReason: "stop"},
	}}

	result, err := fixture.runner.Run(context.Background(), designRequirementItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := assistantRows(fixture, result.AgentRunID)
	if len(rows) != 1 {
		t.Fatalf("assistant rows = %d, want 1", len(rows))
	}
	deltas := transcriptDeltasFor(t, fixture, result.AgentRunID, rows[0].ID)

	// The continuation after the astral prefix must carry offset 2 (UTF-16 units
	// of "😀"), not 1 (rune count) and not 4 (byte count).
	var continuation *agentapp.TranscriptDelta
	for i := range deltas {
		if deltas[i].Increment == "尾巴" {
			continuation = &deltas[i]
		}
	}
	if continuation == nil {
		t.Fatalf("no append increment for the astral-prefix continuation: %#v", deltas)
	}
	if continuation.Offset != 2 {
		t.Fatalf("append offset after astral prefix = %d, want 2 UTF-16 units (not 1 rune, not 4 bytes)", continuation.Offset)
	}

	// replayTranscript independently re-checks every append offset against
	// len(utf16.Encode([]rune(text))) and reproduces the stored row.
	text, state := replayTranscript(t, deltas)
	if text != "😀尾巴" || state != agent.MessageStreamStateComplete {
		t.Fatalf("replay = %q/%q, want 😀尾巴/complete", text, state)
	}
	if rows[0].Parts[0].Text != text {
		t.Fatalf("stored row %#v disagrees with the replayed stream %q", rows, text)
	}
}

// TestTranscriptStreamsForCaseExecutor pins "all profiles": the execution
// profile, which has no conversation at all, streams its transcript too.
func TestTranscriptStreamsForCaseExecutor(t *testing.T) {
	provider := &fakeTaskProvider{task: executionTaskFixture()}
	fixture := newWiringFixture(t, func(deps *agentapp.RunnerDeps) {
		deps.TaskProvider = provider
	})
	fixture.chat.streams = [][]llmapp.Delta{{
		{Content: "用例"},
		{Content: "执行完成"},
		{FinishReason: "stop"},
	}}

	result, err := fixture.runner.Run(context.Background(), executionCaseItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := assistantRows(fixture, result.AgentRunID)
	if len(rows) != 1 || rows[0].Parts[0].Text != "用例执行完成" {
		t.Fatalf("case executor transcript = %#v, want one complete row", rows)
	}
	if rows[0].StreamState != agent.MessageStreamStateComplete {
		t.Fatalf("stream_state = %q, want complete", rows[0].StreamState)
	}
	if len(fixture.repo.transcriptUpdates()) == 0 {
		t.Fatal("the case executor transcript was written as one batch row, not streamed")
	}
	if len(transcriptDeltas(t, fixture, result.AgentRunID)) == 0 {
		t.Fatal("no run:{id} transcript delta was published for the case executor")
	}
}

// TestTranscriptSealsInterruptedAfterFirstDeltaError pins R-D7/SC-D4 for the
// agent transcript: content already produced is preserved and the row is sealed
// interrupted, never complete and never deleted.
func TestTranscriptSealsInterruptedAfterFirstDeltaError(t *testing.T) {
	fixture := newWiringFixture(t)
	fixture.chat.streams = [][]llmapp.Delta{{
		{Content: "已经生成的一半"},
	}}
	fixture.chat.errors = []error{errors.New("upstream closed")}

	result, err := fixture.runner.Run(context.Background(), designRequirementItem())
	if err == nil {
		t.Fatal("a post-first-delta stream failure must fail the run")
	}
	if result.Status == agent.AgentRunDone {
		t.Fatalf("result = %#v, want a failed run", result)
	}
	rows := assistantRows(fixture, result.AgentRunID)
	if len(rows) != 1 {
		t.Fatalf("assistant rows = %d, want the preserved partial row", len(rows))
	}
	if rows[0].Parts[0].Text != "已经生成的一半" {
		t.Fatalf("partial content = %#v, want it preserved", rows[0].Parts)
	}
	if rows[0].StreamState != agent.MessageStreamStateInterrupted {
		t.Fatalf("stream_state = %q, want interrupted", rows[0].StreamState)
	}
	deltas := transcriptDeltas(t, fixture, result.AgentRunID)
	if len(deltas) == 0 {
		t.Fatal("no transcript delta was published")
	}
	last := deltas[len(deltas)-1]
	if last.StreamState != agent.MessageStreamStateInterrupted || last.Message == nil {
		t.Fatalf("terminal frame = %#v, want an interrupted full snapshot", last)
	}
}

// TestTranscriptIsFrozenAfterTheRunEnds pins the terminal freeze from the
// caller's side: once the run finished, its transcript rows refuse further
// in-place writes.
func TestTranscriptIsFrozenAfterTheRunEnds(t *testing.T) {
	fixture := newWiringFixture(t)
	fixture.chat.streams = [][]llmapp.Delta{{
		{Content: "终态回复"},
		{FinishReason: "stop"},
	}}
	result, err := fixture.runner.Run(context.Background(), designRequirementItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := assistantRows(fixture, result.AgentRunID)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	late := rows[0]
	late.Parts = []agent.MessagePart{{Type: "text", Text: "late rewrite"}}
	if err := fixture.repo.UpdateMessage(context.Background(), late); !errors.Is(err, agent.ErrMessageStreamClosed) {
		t.Fatalf("late update err = %v, want ErrMessageStreamClosed", err)
	}
}

// TestTranscriptToolCallsShareTheStreamingRow pins the tool-call round: the
// assembled calls land on the SAME assistant row (never a second row), and the
// tool result stays its own complete row.
func TestTranscriptToolCallsShareTheStreamingRow(t *testing.T) {
	fixture := newWiringFixture(t)
	fixture.chat.streams = [][]llmapp.Delta{
		{
			{ToolCall: &llmapp.ToolCallDelta{Index: 0, ID: "call_1", Name: "requirement.read", Arguments: `{"id":`}},
			{ToolCall: &llmapp.ToolCallDelta{Index: 0, Arguments: `"501"}`}},
			{FinishReason: "tool_calls"},
		},
		{
			{Content: "读完了"},
			{FinishReason: "stop"},
		},
	}

	result, err := fixture.runner.Run(context.Background(), designRequirementItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := assistantRows(fixture, result.AgentRunID)
	if len(rows) != 2 {
		t.Fatalf("assistant rows = %d, want one per model turn", len(rows))
	}
	if len(rows[0].ToolCalls) != 1 || rows[0].ToolCalls[0].ID != "call_1" ||
		rows[0].ToolCalls[0].Arguments != `{"id":"501"}` {
		t.Fatalf("tool calls = %#v, want the assembled call on the first row", rows[0].ToolCalls)
	}
	for _, row := range rows {
		if row.StreamState != agent.MessageStreamStateComplete {
			t.Fatalf("row %d stream_state = %q, want complete", row.ID, row.StreamState)
		}
	}
	toolRows := 0
	for _, message := range fixture.repo.transcript() {
		if message.Role == agent.MessageRoleTool {
			toolRows++
			if message.StreamState != agent.MessageStreamStateComplete {
				t.Fatalf("tool row stream_state = %q, want the complete default", message.StreamState)
			}
		}
	}
	if toolRows != 1 {
		t.Fatalf("tool result rows = %d, want 1", toolRows)
	}
}

// TestHostProfileStreamsBothSurfaces pins the T6/T8 separation: a Host run keeps
// its incremental conversation head AND streams its agent transcript; the two
// write paths never substitute for each other.
func TestHostProfileStreamsBothSurfaces(t *testing.T) {
	stream := newHostStreamPort()
	fixture := newWiringFixture(t, func(deps *agentapp.RunnerDeps) { deps.Conversation = stream })
	fixture.chat.streams = [][]llmapp.Delta{{
		{Content: "你好"},
		{Content: "，世界"},
		{FinishReason: "stop"},
	}}

	result, err := fixture.runner.Run(context.Background(), hostItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	_, begins, updates, finishes, _ := stream.snapshot()
	if len(begins) != 1 || len(updates) == 0 || len(finishes) != 1 {
		t.Fatalf("Host conversation stream = begins %d / updates %d / finish %d, want the T6 lifecycle",
			len(begins), len(updates), len(finishes))
	}
	rows := assistantRows(fixture, result.AgentRunID)
	if len(rows) != 1 || rows[0].StreamState != agent.MessageStreamStateComplete {
		t.Fatalf("Host agent transcript = %#v, want one complete streamed row", rows)
	}
	if len(transcriptDeltas(t, fixture, result.AgentRunID)) == 0 {
		t.Fatal("a Host run published no run:{id} transcript delta")
	}
}

// TestTranscriptUpdateFailureIsLoggedAndTheSealReconciles pins constitution
// VII.6: a failed in-place write is logged and never silently drops the reply —
// the terminal seal still carries the full snapshot.
func TestTranscriptUpdateFailureIsLoggedAndTheSealReconciles(t *testing.T) {
	logs := &lockedBuffer{}
	fixture := newWiringFixture(t, func(deps *agentapp.RunnerDeps) {
		deps.Logger = slog.New(slog.NewTextHandler(logs, nil))
	})
	fixture.chat.streams = [][]llmapp.Delta{{
		{Content: "第一段"},
		{Content: "第二段"},
		{FinishReason: "stop"},
	}}
	fixture.repo.failNextUpdates(1)

	result, err := fixture.runner.Run(context.Background(), designRequirementItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := assistantRows(fixture, result.AgentRunID)
	if len(rows) != 1 || rows[0].Parts[0].Text != "第一段第二段" {
		t.Fatalf("row after a failed update = %#v, want the full reply", rows)
	}
	if rows[0].StreamState != agent.MessageStreamStateComplete {
		t.Fatalf("stream_state = %q, want the seal to reconcile", rows[0].StreamState)
	}
	if !strings.Contains(logs.String(), "transcript") {
		t.Fatalf("the failed in-place write was not logged:\n%s", logs.String())
	}
}

// TestEveryTranscriptRowReachesTheRunChannel pins the drawer's completeness:
// the live view is not assistant-only. The system/user prompt rows and the tool
// result rows are published as full snapshots too, so an admin who opens the
// drawer mid-run and then only listens to deltas sees the whole transcript.
func TestEveryTranscriptRowReachesTheRunChannel(t *testing.T) {
	fixture := newWiringFixture(t)
	fixture.chat.streams = [][]llmapp.Delta{
		{
			{ToolCall: &llmapp.ToolCallDelta{Index: 0, ID: "call_1", Name: "requirement.read", Arguments: `{"id":"501"}`}},
			{FinishReason: "tool_calls"},
		},
		{
			{Content: "读完了"},
			{FinishReason: "stop"},
		},
	}
	result, err := fixture.runner.Run(context.Background(), designRequirementItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	published := map[string]bool{}
	for _, delta := range transcriptDeltas(t, fixture, result.AgentRunID) {
		published[delta.MessageID] = true
	}
	rows := fixture.repo.transcript()
	if len(rows) < 4 {
		t.Fatalf("transcript rows = %d, want system/user/assistant/tool/assistant", len(rows))
	}
	for _, row := range rows {
		if !published[fmtInt(row.ID)] {
			t.Fatalf("%s row %d never reached the run:{id} channel", row.Role, row.ID)
		}
	}
}

// TestTranscriptStreamStatesComeFromTheSharedAuthority is the single-source pin
// at the runner boundary: every published state is a member of the ONE
// authority, never a runner-local literal.
func TestTranscriptStreamStatesComeFromTheSharedAuthority(t *testing.T) {
	fixture := newWiringFixture(t)
	fixture.chat.streams = [][]llmapp.Delta{{
		{Content: "x"},
		{FinishReason: "stop"},
	}}
	result, err := fixture.runner.Run(context.Background(), designRequirementItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	allowed := map[string]bool{}
	for _, state := range version.MessageStreamStates() {
		allowed[state] = true
	}
	for _, delta := range transcriptDeltas(t, fixture, result.AgentRunID) {
		if !allowed[delta.StreamState] {
			t.Fatalf("published stream_state %q is outside the authority %v", delta.StreamState, version.MessageStreamStates())
		}
	}
}
