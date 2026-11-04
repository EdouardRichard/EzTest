package agentapp_test

import (
	"fmt"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// newDesignFloodFixture models the live S3 design conversation after a
// 67-requirement authoring wave: the design Host thread accumulated five human
// instructions interleaved with 120 author summaries, and the design team room
// 10 multi-KB posts. The live acceptance model has a 1,048,576-token window, so
// the cache-first assembler never reaches its window_full boundary and never
// compacts the replay; model the same window here instead of the fixture's 64k
// fallback, otherwise the legacy threshold would hide the flood.
func newDesignFloodFixture(t *testing.T) *runnerFixture {
	t.Helper()
	fixture := newBriefingRunnerFixture(t)
	fixture.chat.models = []llmapp.ModelInfo{{
		ConfigID: 1, Model: "fake-llm", ContextWindow: 1048576, MaxOutput: 393216,
	}}
	fixture.conv.kind = "design"
	fixture.conv.snapshot.Summary = ""
	fixture.conv.snapshot.PinnedFacts = nil

	// The oldest two human instructions must fall outside every bounded window;
	// 三/四/五 are the newest three human turns. A single-human fixture cannot
	// catch a regression that stops dropping the oldest turn.
	humanTurns := []string{
		"人工指令一：请确保用例覆盖移动端。",
		"人工指令二：请补充异常路径。",
		"人工指令三：请保留草稿状态。",
		"人工指令四：请覆盖分页边界。",
		"人工指令五：这是最新的人工指令。",
	}
	history := make([]agentapp.ConversationMessage, 0, 125)
	nextHuman := 0
	appendHuman := func() {
		history = append(history, agentapp.ConversationMessage{
			ID: int64(1 + nextHuman), Role: "user", Text: humanTurns[nextHuman],
		})
		nextHuman++
	}
	appendHuman()
	for index := 0; index < 120; index++ {
		history = append(history, agentapp.ConversationMessage{
			ID:   int64(100 + index),
			Role: "assistant",
			Text: fmt.Sprintf("历史作者摘要%03d %s", index, strings.Repeat("历史投影", 1000)),
		})
		switch index {
		case 29, 59, 89, 119:
			appendHuman()
		}
	}
	fixture.conv.snapshot.Messages = history

	// TeamRoomRepository.ListPosts returns newest first (ORDER BY id DESC) and
	// boundDesignAuthoringTeamRoom keeps Posts[:2]: build the fixture in that
	// same order, otherwise the test would model the opposite tail from
	// production.
	posts := make([]agent.TeamRoomPost, 0, 10)
	for index := 9; index >= 0; index-- {
		posts = append(posts, agent.TeamRoomPost{
			ID:      int64(7000 + index),
			Kind:    agent.PostKindStatus,
			Content: fmt.Sprintf("团队历史帖子%02d %s END%02d", index, strings.Repeat("团队投影", 1500), index),
		})
	}
	fixture.team.context = agentapp.TeamRoomContext{Team: "design", Posts: posts}
	return fixture
}

// assertNewestTeamPostsKept pins the production team-room order: ListPosts
// returns newest first and boundDesignAuthoringTeamRoom keeps Posts[:2], so the
// two highest-id posts (09, 08) must survive the tail bound, each clipped to
// 400 runes. An older-first fixture would keep the oldest posts instead and
// hide that production keeps the newest.
func assertNewestTeamPostsKept(t *testing.T, prompt string) {
	t.Helper()
	for _, kept := range []string{"团队历史帖子09", "团队历史帖子08"} {
		if !strings.Contains(prompt, kept) {
			t.Fatalf("%q (a newest team-room post) was dropped from the prompt", kept)
		}
	}
	for _, dropped := range []string{"团队历史帖子07", "团队历史帖子00"} {
		if strings.Contains(prompt, dropped) {
			t.Fatalf("%q (an older team-room post) was replayed into the prompt", dropped)
		}
	}
	if strings.Contains(prompt, "END09") {
		t.Fatal("the newest team-room post was replayed without the content clip")
	}
}

// historySegments returns the history segments of a packet in original order.
// Their roles distinguish the human-instruction window from the assistant
// window.
func historySegments(packet agentcontext.ContextPacket) []agentcontext.Segment {
	segments := make([]agentcontext.Segment, 0)
	for _, segment := range packet.Segments {
		if segment.Name == agentcontext.SegmentHistory {
			segments = append(segments, segment)
		}
	}
	return segments
}

// TestDesignAuthorContextIsBounded pins the context-scaling half of the
// 67-requirement flood. Every design.requirement run carries the same design
// conversation id, and the case author posts its final summary both to the
// conversation and to the design team room. Before the fix each background
// author replayed the whole growing thread (170 author summaries in the live
// S3 version) plus the 10 newest team-room posts, so the prompt reached
// 300k-670k conservative tokens; the 20k/200k token budget was consumed by the
// read-only turns and case.graph_propose never ran.
//
// A background author works from its requirement scope, requirement.read and
// design.graph_read. It must keep the human turns (the latest instruction) and
// at most the newest team-room posts, clipped; previous authors' projections
// must not be replayed. This keeps three author turns inside the design token
// budget.
func TestDesignAuthorContextIsBounded(t *testing.T) {
	fixture := newDesignFloodFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "已提交"}}

	if _, err := mustRun(t, fixture, designRequirementWorkItem(1001, 501, 700)); err != nil {
		t.Fatalf("run: %v", err)
	}
	prompt := requestPrompt(fixture.chat.request(0))
	packet := decodeStoredPacket(t, fixture.repo.packets[0])

	history := historySegments(packet)
	if len(history) != 3 {
		t.Fatalf("background author replayed %d history messages, want the newest 3 human instructions", len(history))
	}
	for _, segment := range history {
		if segment.Role != "user" {
			t.Fatalf("background author history contains a %q message, want only human instructions", segment.Role)
		}
	}
	if strings.Contains(prompt, "历史作者摘要") {
		t.Fatal("previous author projections are replayed into a background author prompt")
	}
	for _, kept := range []string{"人工指令三", "人工指令四", "人工指令五"} {
		if !strings.Contains(prompt, kept) {
			t.Fatalf("%q (a retained human instruction) was dropped from the background author prompt", kept)
		}
	}
	for _, dropped := range []string{"人工指令一", "人工指令二"} {
		if strings.Contains(prompt, dropped) {
			t.Fatalf("%q (an oldest human instruction) was replayed into the background author prompt", dropped)
		}
	}
	assertNewestTeamPostsKept(t, prompt)
	if !strings.Contains(prompt, "requirement_id=501") {
		t.Fatalf("the requirement scope was lost from the author prompt:\n%s", prompt)
	}
	if len(prompt) > 40000 {
		t.Fatalf("author prompt = %d chars, want the bounded context (<= 40000)", len(prompt))
	}
	totalTokens := 0
	for _, segment := range packet.Segments {
		totalTokens += segment.Tokens
	}
	if totalTokens > 32000 {
		t.Fatalf("author context = %d conservative tokens, want <= 32000 (three turns must fit %d)",
			totalTokens, agentapp.DesignRunMaxTokens)
	}
}

// TestDesignHostContextIsBounded covers the other consumer of the same design
// thread: the design Host chat.turn that S4 uses to modify an existing case. It
// needs the human instructions and its own recent replies, not every
// background author's UI projection, and it needs at most the newest clipped
// team-room posts. Without the bound the S4 modification run would replay the
// same 300k-670k-token thread and settle before case.graph_propose.
func TestDesignHostContextIsBounded(t *testing.T) {
	fixture := newDesignFloodFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "我已读取用例，将按你的修改要求提交 draft。"}}

	item := fakeWorkItem(agentapp.WorkTypeChatTurn, agentapp.WorkItemPayload{
		ConversationID: 20, MessageID: 501, Trigger: "user_message",
	})
	if _, err := mustRun(t, fixture, item); err != nil {
		t.Fatalf("design host run: %v", err)
	}
	prompt := requestPrompt(fixture.chat.request(0))
	packet := decodeStoredPacket(t, fixture.repo.packets[0])

	history := historySegments(packet)
	if len(history) != 6 {
		t.Fatalf("design host replayed %d history messages, want 6 (3 human + 3 assistant)", len(history))
	}
	humans, assistants := 0, 0
	for _, segment := range history {
		switch segment.Role {
		case "user":
			humans++
		case "assistant":
			assistants++
		}
	}
	if humans != 3 || assistants != 3 {
		t.Fatalf("design host history roles = %d human + %d assistant, want 3 + 3", humans, assistants)
	}
	for _, kept := range []string{"人工指令三", "人工指令四", "人工指令五", "历史作者摘要119"} {
		if !strings.Contains(prompt, kept) {
			t.Fatalf("%q (a retained recent message) was dropped from the design host prompt", kept)
		}
	}
	for _, dropped := range []string{"人工指令一", "人工指令二", "历史作者摘要116", "历史作者摘要000"} {
		if strings.Contains(prompt, dropped) {
			t.Fatalf("%q (an older message) was replayed into the design host prompt", dropped)
		}
	}
	assertNewestTeamPostsKept(t, prompt)
	if len(prompt) > 48000 {
		t.Fatalf("design host prompt = %d chars, want the bounded context (<= 48000)", len(prompt))
	}
	totalTokens := 0
	for _, segment := range packet.Segments {
		totalTokens += segment.Tokens
	}
	if totalTokens > 32000 {
		t.Fatalf("design host context = %d conservative tokens, want <= 32000 (three turns must fit %d)",
			totalTokens, agentapp.DesignRunMaxTokens)
	}
}
