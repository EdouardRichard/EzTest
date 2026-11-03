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
// 67-requirement authoring wave: the design Host thread accumulated 120 author
// summaries and the design team room 10 multi-KB posts. The live acceptance
// model has a 1,048,576-token window, so the cache-first assembler never
// reaches its window_full boundary and never compacts the replay; model the
// same window here instead of the fixture's 64k fallback, otherwise the legacy
// threshold would hide the flood.
func newDesignFloodFixture(t *testing.T) *runnerFixture {
	t.Helper()
	fixture := newBriefingRunnerFixture(t)
	fixture.chat.models = []llmapp.ModelInfo{{
		ConfigID: 1, Model: "fake-llm", ContextWindow: 1048576, MaxOutput: 393216,
	}}
	fixture.conv.kind = "design"
	fixture.conv.snapshot.Summary = ""
	fixture.conv.snapshot.PinnedFacts = nil

	history := make([]agentapp.ConversationMessage, 0, 121)
	history = append(history, agentapp.ConversationMessage{
		ID: 1, Role: "user", Text: "请确保用例覆盖移动端。",
	})
	for index := 0; index < 120; index++ {
		history = append(history, agentapp.ConversationMessage{
			ID:   int64(100 + index),
			Role: "assistant",
			Text: fmt.Sprintf("历史作者摘要%03d %s", index, strings.Repeat("历史投影", 1000)),
		})
	}
	fixture.conv.snapshot.Messages = history

	posts := make([]agent.TeamRoomPost, 0, 10)
	for index := 0; index < 10; index++ {
		posts = append(posts, agent.TeamRoomPost{
			ID:      int64(7000 + index),
			Kind:    agent.PostKindStatus,
			Content: fmt.Sprintf("团队历史帖子%02d %s END%02d", index, strings.Repeat("团队投影", 1500), index),
		})
	}
	fixture.team.context = agentapp.TeamRoomContext{Team: "design", Posts: posts}
	return fixture
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

	if contents := segmentContents(packet, agentcontext.SegmentHistory); len(contents) != 1 {
		t.Fatalf("background author replayed %d history messages, want only the human instruction", len(contents))
	}
	if strings.Contains(prompt, "历史作者摘要000") {
		t.Fatal("previous author projections are replayed into a background author prompt")
	}
	if !strings.Contains(prompt, "请确保用例覆盖移动端。") {
		t.Fatal("the latest human instruction was dropped from the background author prompt")
	}
	if strings.Contains(prompt, "团队历史帖子02") {
		t.Fatal("background author replayed more team-room posts than the bounded tail")
	}
	if strings.Contains(prompt, "END00") {
		t.Fatal("background author replayed a team-room post without the content clip")
	}
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

	history := segmentContents(packet, agentcontext.SegmentHistory)
	if len(history) > 6 {
		t.Fatalf("design host replayed %d history messages, want at most 6 (3 human + 3 assistant)", len(history))
	}
	if strings.Contains(prompt, "历史作者摘要000") {
		t.Fatal("design host replayed the whole author-summary thread")
	}
	if !strings.Contains(prompt, "请确保用例覆盖移动端。") {
		t.Fatal("the latest human instruction was dropped from the design host prompt")
	}
	if strings.Contains(prompt, "团队历史帖子02") {
		t.Fatal("design host replayed more team-room posts than the bounded tail")
	}
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
