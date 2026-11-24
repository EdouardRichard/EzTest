package agentapp_test

// v0.2.4-H T12 (H-E3): the stage_boundary compaction trigger is WIRED.
//
// CompactionTriggers() has always declared stage_boundary and
// CompactionPolicy.StageBoundaryDecision() has always produced a forced decision
// for it ("produced even when the prompt is below the pressure threshold; the
// runner wiring that acts on it is Task 36"). Nothing produced it: the run's
// first assembly passed InvalidationNone and the only other assembly is the
// overflow rescue, so the vocabulary member had no producer and the fact/event
// it names could never be observed.
//
// The trigger is the version stage transition across the version's prompt chain:
// stage is part of the byte-stable prefix, so when the stage recorded on the
// version's last persisted packet differs from the stage this run assembles with,
// the round is a declared boundary — the prefix is rebuilt and the transcript is
// folded once, independently of the pressure threshold.
//
// The assertions read the trigger vocabulary from agentcontext (never a restated
// string) and require the FACT and its derived event to agree, exactly like the
// pressure/overflow boundaries.

import (
	"errors"
	"strings"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

var errStageBoundaryRead = errors.New("context packet store unavailable")

// stageBoundaryHistory is long enough that a forced boundary folds transcript
// while the prompt stays far below the pressure threshold.
func stageBoundaryHistory() []agentapp.ConversationMessage {
	return []agentapp.ConversationMessage{
		{ID: 701, Role: "user", Text: "消息一：需求背景"},
		{ID: 702, Role: "assistant", Text: "消息二：澄清回复"},
		{ID: 703, Role: "user", Text: "消息三：补充需求"},
		{ID: 704, Role: "assistant", Text: "消息四：继续澄清"},
		{ID: 705, Role: "user", Text: "消息五：确认范围"},
		{ID: 706, Role: "assistant", Text: "消息六：最新回复"},
	}
}

// seedPriorStage stores a previous packet for the version whose recorded stage is
// prior. The packet is produced by the real encoder so the test reads the same
// persisted shape the production reader decodes.
func seedPriorStage(t *testing.T, fixture *runnerFixture, prior string) {
	t.Helper()
	packet := agentcontext.ContextPacket{Stage: prior, PrefixHash: strings64Hex('a')}
	stored := agentapp.EncodeContextPacket(1, 700, packet)
	fixture.repo.priorPacket = &stored
}

func strings64Hex(char byte) string {
	buf := make([]byte, 64)
	for index := range buf {
		buf[index] = char
	}
	return string(buf)
}

func TestRunnerCompactsAtStageBoundary(t *testing.T) {
	fixture := newBriefingRunnerFixture(t)
	// The boundary runs inside prepare, between two model calls: a stepping clock
	// gives it a measured duration instead of the frozen clock's zero.
	fixture.clock.step = 250 * time.Millisecond
	fixture.settings.ints["context.keep_recent_messages"] = 2
	fixture.conv.snapshot.Summary = "更早的滚动摘要"
	fixture.conv.snapshot.PinnedFacts = nil
	fixture.conv.snapshot.Messages = stageBoundaryHistory()
	// defaultAssemblyBriefing assembles at stage "clarifying"; the version's last
	// packet was assembled at a different stage, so this run crosses the boundary.
	seedPriorStage(t, fixture, "designing")
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "已回复"}}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("run: %v", err)
	}
	prepared := decodeStoredPacket(t, fixture.repo.packets[0])
	if prepared.InvalidationReason != string(agentcontext.InvalidationStageBoundary) {
		t.Fatalf("invalidation reason = %q, want %q", prepared.InvalidationReason, agentcontext.InvalidationStageBoundary)
	}
	if prepared.Stage != "clarifying" {
		t.Fatalf("persisted packet stage = %q, want the stage this run assembled with", prepared.Stage)
	}
	rows := activityRowsOf(t, fixture, agent.ActivityTypeCompaction)
	if len(rows) != 1 {
		t.Fatalf("compaction facts = %d, want one for the stage boundary: %#v",
			len(rows), fixture.repo.activityRows())
	}
	assertCompactionFact(t, rows[0], agentcontext.StageBoundary, compactedEventPayload(t, fixture.events))
	history := segmentContents(prepared, agentcontext.SegmentHistory)
	if len(history) != 2 || history[0] != "消息五：确认范围" || history[1] != "消息六：最新回复" {
		t.Fatalf("recent history = %#v, want the 2 newest messages verbatim after the fold", history)
	}
	if len(fixture.repo.priorPacketReads) != 1 {
		t.Fatalf("prior packet reads = %#v, want exactly one per run", fixture.repo.priorPacketReads)
	}
}

// TestRunnerDoesNotDeclareStageBoundaryWithoutATransition is the negative control:
// the same long history with the SAME prior stage (and with no prior packet at
// all) stays an append-only round — no boundary, no compaction fact.
func TestRunnerDoesNotDeclareStageBoundaryWithoutATransition(t *testing.T) {
	for _, prior := range []string{"clarifying", ""} {
		fixture := newBriefingRunnerFixture(t)
		fixture.settings.ints["context.keep_recent_messages"] = 2
		fixture.conv.snapshot.Summary = "更早的滚动摘要"
		fixture.conv.snapshot.PinnedFacts = nil
		fixture.conv.snapshot.Messages = stageBoundaryHistory()
		if prior != "" {
			seedPriorStage(t, fixture, prior)
		}
		fixture.chat.responses = []llmapp.ChatResponse{{Content: "已回复"}}

		if _, err := mustRun(t, fixture, hostItem()); err != nil {
			t.Fatalf("prior=%q run: %v", prior, err)
		}
		prepared := decodeStoredPacket(t, fixture.repo.packets[0])
		if prepared.InvalidationReason != "" {
			t.Fatalf("prior=%q invalidation reason = %q, want an append-only round", prior, prepared.InvalidationReason)
		}
		if rows := activityRowsOf(t, fixture, agent.ActivityTypeCompaction); len(rows) != 0 {
			t.Fatalf("prior=%q compaction facts = %#v, want none without a transition", prior, rows)
		}
		if len(segmentContents(prepared, agentcontext.SegmentHistory)) != len(stageBoundaryHistory()) {
			t.Fatalf("prior=%q history was folded without a boundary", prior)
		}
	}
}

// TestRunnerStageBoundaryReadFailureIsLoggedNotSilent pins that an unreadable
// prior packet leaves the round append-only (no fabricated boundary) and does not
// fail the run: the trigger's input is supplemental, and the fallback is logged.
func TestRunnerStageBoundaryReadFailureIsLoggedNotSilent(t *testing.T) {
	fixture := newBriefingRunnerFixture(t)
	fixture.settings.ints["context.keep_recent_messages"] = 2
	fixture.conv.snapshot.Messages = stageBoundaryHistory()
	fixture.repo.priorPacketErr = errStageBoundaryRead
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "已回复"}}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("run: %v", err)
	}
	prepared := decodeStoredPacket(t, fixture.repo.packets[0])
	if prepared.InvalidationReason != "" {
		t.Fatalf("invalidation reason = %q, want no fabricated boundary", prepared.InvalidationReason)
	}
	if !strings.Contains(fixture.logOutput(), "previous context packet read failed") {
		t.Fatalf("the prior-packet read failure was swallowed; a degradation must be logged: %s", fixture.logOutput())
	}
}
