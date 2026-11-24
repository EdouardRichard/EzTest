package agentapp_test

// v0.2.4-D T10 (SC-D5, Q04=A-lite): tool calls and compaction boundaries become
// idempotent agent_activity FACTS, written to the store BEFORE the event that is
// derived from them (constitution 五.7 "facts exist independently of events",
// 五.9 "a fact log is not current state", 七.6 "no silent degradation").
//
// Expectations come from the SC-D5 contract — one row per tool call keyed
// (run_id, seq), type tool_call/compaction, payload carries the coarse facts,
// error carries the failure reason — not from observed runner output.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// twoToolCallTurn is one model turn requesting two allowed Host tool calls, so
// "one fact row per call, monotonic per-run seq" is observable in a single run.
func twoToolCallTurn() llmapp.ChatResponse {
	return llmapp.ChatResponse{
		ToolCalls: []llmapp.ToolCall{
			{ID: "call_a", Name: tools.ToolRequirementRead, Arguments: `{"requirement_id":"501"}`},
			{ID: "call_b", Name: tools.ToolRequirementRead, Arguments: `{"requirement_id":"501"}`},
		},
		FinishReason: "tool_calls",
	}
}

func activityRowsOf(t *testing.T, fixture *runnerFixture, kind agent.ActivityType) []agent.Activity {
	t.Helper()
	rows := make([]agent.Activity, 0)
	for _, row := range fixture.repo.activityRows() {
		if row.Type == kind {
			rows = append(rows, row)
		}
	}
	return rows
}

// TestToolCallWritesOneActivityFactPerCall pins the core Q04 write: each tool
// call owns ONE (run_id, seq) row whose start and end are both recorded, and the
// seq is monotonic per run.
func TestToolCallWritesOneActivityFactPerCall(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{twoToolCallTurn(), {Content: "已完成。"}}

	result, err := mustRun(t, fixture, hostItem())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	rows := activityRowsOf(t, fixture, agent.ActivityTypeToolCall)
	if len(rows) != 2 {
		t.Fatalf("tool_call facts = %d, want one row per call (2): %#v", len(rows), fixture.repo.activityRows())
	}
	for index, row := range rows {
		if row.Seq != index+1 {
			t.Fatalf("fact %d seq = %d, want the monotonic per-run ordinal %d", index, row.Seq, index+1)
		}
		if row.RunID != result.AgentRunID {
			t.Fatalf("fact run_id = %d, want the run %d", row.RunID, result.AgentRunID)
		}
		if row.Title != tools.ToolRequirementRead {
			t.Fatalf("fact title = %q, want the tool name %q", row.Title, tools.ToolRequirementRead)
		}
		if row.StartedAt.IsZero() {
			t.Fatalf("fact %d has no started_at", index)
		}
		if row.EndedAt == nil {
			t.Fatalf("fact %d has no ended_at: the call finished, the row must record it", index)
		}
		if row.EndedAt.Before(row.StartedAt) {
			t.Fatalf("fact %d ended before it started", index)
		}
		if row.Error != "" {
			t.Fatalf("fact %d error = %q, want empty for a successful call", index, row.Error)
		}
		if name, _ := row.Payload["tool"].(string); name != tools.ToolRequirementRead {
			t.Fatalf("fact %d payload.tool = %#v, want the tool name", index, row.Payload["tool"])
		}
	}
	if rows[0].Payload["tool_call_id"] != "call_a" || rows[1].Payload["tool_call_id"] != "call_b" {
		t.Fatalf("payload tool_call_id = %#v / %#v, want call_a / call_b",
			rows[0].Payload["tool_call_id"], rows[1].Payload["tool_call_id"])
	}
	// Start and end are two writes of the SAME row, never two rows.
	if len(fixture.repo.activityWrites) != 4 {
		t.Fatalf("activity writes = %d, want start+end per call (4)", len(fixture.repo.activityWrites))
	}
	for index, write := range fixture.repo.activityWrites {
		if want := index/2 + 1; write.Seq != want {
			t.Fatalf("write %d seq = %d, want %d (the end reuses the start's seq)", index, write.Seq, want)
		}
	}
}

// TestToolCallFactRecordsTheFailureReason pins the error column: a rejected call
// is a recorded fact with its reason, not a silently missing row.
func TestToolCallFactRecordsTheFailureReason(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{
		toolCall("no.such.tool", `{}`),
		{Content: "改用别的方式。"},
	}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("run: %v", err)
	}
	rows := activityRowsOf(t, fixture, agent.ActivityTypeToolCall)
	if len(rows) != 1 {
		t.Fatalf("tool_call facts = %d, want the rejected call recorded once", len(rows))
	}
	if rows[0].Error == "" {
		t.Fatalf("a rejected call recorded no error: %#v", rows[0])
	}
	if !strings.Contains(rows[0].Error, "no.such.tool") {
		t.Fatalf("error = %q, want the rejected tool named", rows[0].Error)
	}
	if rows[0].EndedAt == nil {
		t.Fatalf("a rejected call has no ended_at: %#v", rows[0])
	}
}

// TestActivityFactIsWrittenBeforeItsEvent is the ordering assertion for EVERY
// fact write, not just the first: at the moment a call's start fact reaches the
// store, the agent.activity event derived from it has NOT been published yet.
// Writes alternate start/end, so write i must observe exactly (i+1)/2 tool
// events — a regression that emits the event before a later call's fact would
// break its own index (T10 review #4).
func TestActivityFactIsWrittenBeforeItsEvent(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.repo.events = fixture.events
	fixture.chat.responses = []llmapp.ChatResponse{twoToolCallTurn(), {Content: "已完成。"}}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("run: %v", err)
	}
	writes := fixture.repo.activityWrites
	if len(writes) != 4 || len(fixture.repo.activityEventCounts) != len(writes) {
		t.Fatalf("observed writes = %d / counts = %d, want start+end per call (4)",
			len(writes), len(fixture.repo.activityEventCounts))
	}
	for index, got := range fixture.repo.activityEventCounts {
		if want := (index + 1) / 2; got != want {
			t.Fatalf("tool agent.activity events at fact write %d (seq %d) = %d, want %d (fact before its event)",
				index, writes[index].Seq, got, want)
		}
	}
	// Every derived event carries the ledger key it came from (七.7): an operator
	// reading the stream can locate the exact (run_id, seq) row.
	rows := activityRowsOf(t, fixture, agent.ActivityTypeToolCall)
	seen := 0
	for _, event := range fixture.events.events {
		if event.Type != "agent.activity" || event.Payload["status"] != "tool" {
			continue
		}
		if seen >= len(rows) {
			t.Fatalf("more tool activity events than facts: %#v", event.Payload)
		}
		if seq, ok := event.Payload["activity_seq"].(int); !ok || seq != rows[seen].Seq {
			t.Fatalf("event activity_seq = %#v, want the fact seq %d", event.Payload["activity_seq"], rows[seen].Seq)
		}
		if event.Payload["run_id"] != idTextForTest(rows[seen].RunID) {
			t.Fatalf("event run_id = %#v, want the fact run %d", event.Payload["run_id"], rows[seen].RunID)
		}
		seen++
	}
	if seen != 2 {
		t.Fatalf("tool agent.activity events = %d, want one per call", seen)
	}
}

func idTextForTest(id int64) string { return strconv.FormatInt(id, 10) }

// TestActivityEventIsSkippedWhenTheFactWriteFails pins 五.7/七.6: the event is a
// projection of the fact, so a failed fact write must NOT produce an event that
// claims an activity the ledger does not hold. The run itself still finishes —
// observability never gates the model's work.
func TestActivityEventIsSkippedWhenTheFactWriteFails(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.repo.activityErr = errors.New("activity store is unavailable")
	fixture.chat.responses = []llmapp.ChatResponse{
		toolCall(tools.ToolRequirementRead, `{"requirement_id":"501"}`),
		{Content: "已完成。"},
	}

	result, err := mustRun(t, fixture, hostItem())
	if err != nil {
		t.Fatalf("run: %v (a fact write failure must not fail the run)", err)
	}
	if result.Status != agent.AgentRunDone {
		t.Fatalf("run status = %q, want done", result.Status)
	}
	for _, event := range fixture.events.events {
		if event.Type == "agent.activity" && event.Payload["status"] == "tool" {
			t.Fatalf("an activity event was derived from a fact that was never written: %#v", event.Payload)
		}
	}
	if len(fixture.repo.activityRows()) != 0 {
		t.Fatalf("facts = %#v, want none when every write failed", fixture.repo.activityRows())
	}
}

// TestActivityFactSurvivesEventFailure is the other half of 五.7: losing the
// event transport must not lose the fact.
func TestActivityFactSurvivesEventFailure(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.events.err = errors.New("event transport is down")
	fixture.chat.responses = []llmapp.ChatResponse{
		toolCall(tools.ToolRequirementRead, `{"requirement_id":"501"}`),
		{Content: "已完成。"},
	}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("run: %v", err)
	}
	rows := activityRowsOf(t, fixture, agent.ActivityTypeToolCall)
	if len(rows) != 1 || rows[0].EndedAt == nil {
		t.Fatalf("facts = %#v, want the completed call fact despite the event failure", rows)
	}
}

// TestThinkingEmitsNoActivityFact pins the Q04 scope: the ledger holds tool and
// compaction boundaries only. The per-turn "thinking" heartbeat stays an
// immediate projection (constitution 五.11) — it decides nothing and is not a
// fact of record.
func TestThinkingEmitsNoActivityFact(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "请补充验收标准？"}}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if rows := fixture.repo.activityRows(); len(rows) != 0 {
		t.Fatalf("facts = %#v, want none for a tool-less, compaction-free run", rows)
	}
	if !containsEvent(fixture.events, "agent.activity") {
		t.Fatalf("the thinking heartbeat event disappeared: %v", eventTypes(fixture.events))
	}
}

// TestPressureCompactionWritesAnActivityFact drives the REAL pressure producer
// (per-round policy decision → compactContext) and asserts the compaction fact
// carries the before/after token counts of the boundary.
func TestPressureCompactionWritesAnActivityFact(t *testing.T) {
	fixture := newBriefingRunnerFixture(t)
	pressureCompactionSettings(fixture)
	advanceClockOnModelCall(fixture)
	fixture.conv.snapshot.Summary = ""
	fixture.conv.snapshot.PinnedFacts = nil
	fixture.conv.snapshot.Messages = pressureHistory(811)
	fixture.chat.responses = []llmapp.ChatResponse{
		pressureToolCallResponse(),
		{Content: "压力压缩摘要"},
		{Content: "已完成。"},
	}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("run: %v", err)
	}
	rows := activityRowsOf(t, fixture, agent.ActivityTypeCompaction)
	if len(rows) != 1 {
		t.Fatalf("compaction facts = %d, want one for the pressure boundary: %#v",
			len(rows), fixture.repo.activityRows())
	}
	assertCompactionFact(t, rows[0], agentcontext.Pressure, compactedEventPayload(t, fixture.events))
}

// TestOverflowCompactionWritesAnActivityFact drives the REAL overflow producer
// (provider context-overflow error → handleOverflow).
func TestOverflowCompactionWritesAnActivityFact(t *testing.T) {
	fixture := newBriefingRunnerFixture(t)
	fixture.settings.ints["context.keep_recent_messages"] = 1
	advanceClockOnModelCall(fixture)
	fixture.conv.snapshot.Summary = ""
	fixture.conv.snapshot.PinnedFacts = nil
	fixture.conv.snapshot.Messages = []agentapp.ConversationMessage{
		{ID: 641, Role: "user", Text: "消息一：需求背景"},
		{ID: 642, Role: "user", Text: "消息二：最新补充"},
	}
	fixture.chat.errors = []error{fmt.Errorf("upstream: %w", llmapp.ErrContextOverflow)}
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "恢复后的回复"}}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("run: %v", err)
	}
	rows := activityRowsOf(t, fixture, agent.ActivityTypeCompaction)
	if len(rows) != 1 {
		t.Fatalf("compaction facts = %d, want one for the overflow boundary: %#v",
			len(rows), fixture.repo.activityRows())
	}
	assertCompactionFact(t, rows[0], agentcontext.Overflow, compactedEventPayload(t, fixture.events))
}

// advanceClockOnModelCall makes every model call take real run-clock time, so
// the compaction boundary's measured duration is exercised through its REAL
// producer (the summarizer call inside the transaction) instead of a frozen
// clock that would make a permanent zero look correct (T10 review #1).
func advanceClockOnModelCall(fixture *runnerFixture) {
	fixture.chat.onChat = func(_ int, _ llmapp.ChatRequest) {
		fixture.clock.Advance(1500 * time.Millisecond)
	}
}

// TestNoOpCompactionWritesAnAttemptFact covers the second event-less outcome:
// the pressure trigger fired, the transaction ran, and nothing was folded. The
// existing contract keeps the summary ledger and the event stream untouched —
// but the attempt is still a fact of record, with its measured (equal) token
// counts (T10 review #1, defect class "a trigger with no row").
func TestNoOpCompactionWritesAnAttemptFact(t *testing.T) {
	fixture := newBriefingRunnerFixture(t)
	pressureCompactionSettings(fixture)
	advanceClockOnModelCall(fixture)
	// The recent window keeps the whole conversation, so the rebuilt packet has
	// nothing to fold.
	fixture.settings.ints["context.keep_recent_messages"] = 12
	fixture.conv.snapshot.Summary = "此前持久化摘要"
	fixture.conv.snapshot.PinnedFacts = nil
	fixture.conv.snapshot.Messages = []agentapp.ConversationMessage{
		{ID: 831, Role: "user", Text: "最新需求"},
	}
	fixture.chat.responses = []llmapp.ChatResponse{
		pressureToolCallResponse(),
		{Content: "已完成。"},
	}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if payloads := compactedEventPayloads(t, fixture.events); len(payloads) != 0 {
		t.Fatalf("context.compacted events = %d, want none for a no-op", len(payloads))
	}
	rows := activityRowsOf(t, fixture, agent.ActivityTypeCompaction)
	if len(rows) != 1 {
		t.Fatalf("compaction facts = %d, want the no-op attempt recorded once: %#v",
			len(rows), fixture.repo.activityRows())
	}
	row := rows[0]
	if got, _ := row.Payload["trigger"].(string); got != string(agentcontext.Pressure) {
		t.Fatalf("no-op fact trigger = %#v, want pressure", row.Payload["trigger"])
	}
	before, beforeOK := row.Payload["before_tokens"].(int)
	after, afterOK := row.Payload["after_tokens"].(int)
	if !beforeOK || !afterOK || before <= 0 || before != after {
		t.Fatalf("no-op fact tokens = %#v / %#v, want the measured equal counts",
			row.Payload["before_tokens"], row.Payload["after_tokens"])
	}
	if row.Error != "" {
		t.Fatalf("no-op fact error = %q, want empty (the attempt did not fail)", row.Error)
	}
	// A no-op rebuild folds nothing and calls no summarizer, so on the run clock
	// it legitimately takes no time. What must hold is that the duration is the
	// MEASURED one and the start is derived from it — not a hardcoded zero.
	assertDurationDerivation(t, row)
}

// assertDurationDerivation pins the duration/start/end relation without
// demanding a positive elapsed time (an attempt that does no model work really
// is instantaneous on the run clock).
func assertDurationDerivation(t *testing.T, row agent.Activity) {
	t.Helper()
	duration, ok := row.Payload["duration_ms"].(int64)
	if !ok || duration < 0 {
		t.Fatalf("fact duration_ms = %#v, want a measured non-negative duration", row.Payload["duration_ms"])
	}
	if row.EndedAt == nil {
		t.Fatalf("fact has no ended_at: %#v", row)
	}
	if want := row.EndedAt.Add(-time.Duration(duration) * time.Millisecond); !row.StartedAt.Equal(want) {
		t.Fatalf("fact started_at = %v, want ended_at - duration = %v", row.StartedAt, want)
	}
}

// assertMeasuredDuration pins that the ledger records a REAL elapsed boundary,
// not a permanent zero: duration_ms > 0 and started_at is exactly ended_at minus
// that duration (T10 review #1).
func assertMeasuredDuration(t *testing.T, row agent.Activity) {
	t.Helper()
	duration, ok := row.Payload["duration_ms"].(int64)
	if !ok || duration <= 0 {
		t.Fatalf("fact duration_ms = %#v, want the measured positive duration", row.Payload["duration_ms"])
	}
	if row.EndedAt == nil {
		t.Fatalf("fact has no ended_at: %#v", row)
	}
	if want := row.EndedAt.Add(-time.Duration(duration) * time.Millisecond); !row.StartedAt.Equal(want) {
		t.Fatalf("fact started_at = %v, want ended_at - duration = %v", row.StartedAt, want)
	}
}

// assertCompactionFact checks the SC-D5 compaction payload against the SAME
// authority the context.compacted event uses (agentcontext.CompactionEvent), so
// the fact and its derived event can never drift apart. The expected numbers are
// the boundary's own event payload — the fact must record what the producer
// measured, not a number this test invented.
func assertCompactionFact(t *testing.T, row agent.Activity, trigger agentcontext.CompactionTrigger, eventPayload map[string]any) {
	t.Helper()
	if got, _ := row.Payload["trigger"].(string); got != string(trigger) {
		t.Fatalf("compaction fact trigger = %#v, want %q", row.Payload["trigger"], trigger)
	}
	before, beforeOK := row.Payload["before_tokens"].(int)
	after, afterOK := row.Payload["after_tokens"].(int)
	if !beforeOK || !afterOK || before <= 0 || after <= 0 {
		t.Fatalf("compaction fact tokens = %#v / %#v, want positive before/after counts",
			row.Payload["before_tokens"], row.Payload["after_tokens"])
	}
	for _, key := range []string{
		"trigger", "before_tokens", "after_tokens", "retain_tokens",
		"summary_version", "covered_message_id", "cached_tokens", "duration_ms",
	} {
		value, ok := row.Payload[key]
		if !ok {
			t.Fatalf("compaction fact payload missing %q: %#v", key, row.Payload)
		}
		if value != eventPayload[key] {
			t.Fatalf("compaction fact %s = %#v, want the boundary's own event value %#v (single source)",
				key, value, eventPayload[key])
		}
	}
	if row.EndedAt == nil || row.StartedAt.IsZero() {
		t.Fatalf("compaction fact has no start/end: %#v", row)
	}
	assertMeasuredDuration(t, row)
}
