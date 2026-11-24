package agentapp_test

import (
	"encoding/json"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// seedJudgeIndependenceFixture models the leak I-B4 forbids: the shared design
// conversation carries a background author's own summary projection alongside
// the human instructions, the design team room carries the critic's finding
// posts, and the version briefing source carries the clarification→design
// handoff document. Every one of these is a real channel a design.judge run
// could read the author's reasoning through if prepare()/the briefing builder
// did not force it empty — none of them is a synthetic strawman.
func seedJudgeIndependenceFixture(t *testing.T) *runnerFixture {
	t.Helper()
	fixture := newRunnerFixture(t)
	fixture.conv.kind = "design"
	fixture.conv.snapshot = agentapp.ConversationSnapshot{
		Summary:     "作者已提交登录用例草案，等待审查。",
		PinnedFacts: []string{"作者私有结论标记：登录用例已覆盖主流程"},
		Messages: []agentapp.ConversationMessage{
			{ID: 1, Role: "user", Text: "人工指令：请设计登录用例。"},
			{ID: 2, Role: "assistant", Text: "作者投影：我已提交登录场景与 3 条用例，覆盖手机号与邮箱登录。"},
			{ID: 3, Role: "user", Text: "人工指令：请补充异常路径。"},
			{ID: 4, Role: "assistant", Text: "作者投影：已补充密码错误与账号锁定用例。"},
		},
	}
	fixture.team.context = agentapp.TeamRoomContext{
		Team: "design",
		Digest: &agent.TeamRoomDigest{
			Summary: "团队摘要：作者认为登录用例已足够完整。",
		},
		Posts: []agent.TeamRoomPost{
			{ID: 9002, Kind: agent.PostKindFinding, Content: "critic finding：登录用例缺少锁定阈值断言。"},
			{ID: 9001, Kind: agent.PostKindStatus, Content: "作者状态：登录场景设计完成。"},
		},
	}
	fixture.briefing.source.Handoff = &agentcontext.HandoffSnapshot{
		Revision: 3,
		Document: "澄清交接包正文：作者与澄清 Host 讨论后决定优先覆盖手机号登录，邮箱登录延后。",
	}
	// The briefing findings group renders EVERY current version_fact, and
	// task.complete writes kind=progress whose payload.summary IS the author's
	// own conclusion. That is a real leak channel independent of the handoff
	// (whole-branch review Important #3). An objective approved-page fact rides
	// alongside it so the filter cannot pass by starving the whole group.
	fixture.briefing.source.Findings = []agentcontext.FactSnapshot{
		{
			ID: 9101, Kind: version.FactKindProgress, Key: "task.complete:run:9001",
			Summary: "作者完成结论：登录用例已覆盖全部验收标准，质量达标。",
		},
		{
			ID: 9102, Kind: "page", Key: "page:5001",
			Summary: "已批准页面：/login",
		},
	}
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "已完成独立审查，逐用例提交 verdict。"}}
	return fixture
}

// judgeWorkItemWithCases builds a design.judge work item carrying the T8
// scheduling payload (cases[] + draft_case_version_ids) directly as raw JSON,
// bypassing fakeWorkItem (which only knows the pre-T14 scalar fields). It
// deliberately ALSO carries a conversation_id: the independence guarantee
// must hold even if a future scheduling bug attaches one, not only because
// today's scheduler happens to omit it.
func judgeWorkItemWithCases(conversationID int64, cases []map[string]any, anchor string) agent.WorkItem {
	payload := map[string]any{
		"requirement_id":         "501",
		"requirement_version_id": "700",
	}
	if conversationID > 0 {
		payload["conversation_id"] = conversationID
	}
	if cases != nil {
		payload["cases"] = cases
	}
	if anchor != "" {
		payload["draft_case_version_ids"] = anchor
	}
	raw, _ := json.Marshal(payload)
	item := fakeWorkItem(agentapp.WorkTypeDesignJudge, agentapp.WorkItemPayload{})
	item.Payload = raw
	item.Team = agent.TeamDesign
	return item
}

// TestJudgeRunContextExcludesAuthorContent is the T14 acceptance of I-B4
// (spec SC-E3): the independence test drives the REAL Runner.Run()/prepare()
// path (not a taskContext-only unit call) with a design conversation, team
// room and briefing handoff all seeded with author/critic content, and
// asserts none of it reaches the judge's assembled context packet or prompt.
func TestJudgeRunContextExcludesAuthorContent(t *testing.T) {
	fixture := seedJudgeIndependenceFixture(t)
	item := judgeWorkItemWithCases(20, nil, "")

	result, err := mustRun(t, fixture, item)
	if err != nil {
		t.Fatalf("judge run: %v", err)
	}
	packet := decodeStoredPacket(t, fixture.repo.packets[0])
	prompt := requestPrompt(fixture.chat.request(0))

	if history := historySegments(packet); len(history) != 0 {
		t.Fatalf("judge context replayed %d conversation history segments, want 0 (I-B4)", len(history))
	}
	for _, leaked := range judgeLeakMarkers() {
		if strings.Contains(prompt, leaked) {
			t.Fatalf("judge prompt leaked author/critic/handoff content %q:\n%s", leaked, prompt)
		}
	}
	if len(fixture.conv.appended) != 0 {
		t.Fatalf("judge run appended %d assistant messages into the design conversation, want 0", len(fixture.conv.appended))
	}
	if len(fixture.team.posts) != 0 {
		t.Fatalf("judge run posted %d team-room messages, want 0", len(fixture.team.posts))
	}
	run := fixture.repo.runs[result.AgentRunID]
	if run.ConversationID != nil {
		t.Fatalf("judge agent_run.ConversationID = %v, want nil (no conversation binding even with a payload conversation_id)", *run.ConversationID)
	}
	for _, event := range fixture.events.events {
		if _, ok := event.Scope["conversation_id"]; ok {
			t.Fatalf("judge event %q leaked conversation_id in its scope: %#v", event.Type, event.Scope)
		}
	}
}

// TestJudgeRunContextIndependenceMutation proves the independence test
// discriminates: it drives the SAME seeded fixture through a design.critic
// run (a real profile that legitimately reads the design conversation and
// team room) and asserts the author/critic content DOES reach its prompt.
// If a future change made the judge assertion pass by trivially starving
// every design-team profile of context, this control would stop leaking too
// — the pair together proves the judge-specific exclusion is what removes
// the content, not a broken fixture.
func TestJudgeRunContextIndependenceMutation(t *testing.T) {
	fixture := seedJudgeIndependenceFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "已提交 finding。"}}
	item := fakeWorkItem(agentapp.WorkTypeDesignCritic, agentapp.WorkItemPayload{
		ConversationID: 20, RequirementID: 501, RequirementVersionID: 700,
	})
	item.Team = agent.TeamDesign

	if _, err := mustRun(t, fixture, item); err != nil {
		t.Fatalf("critic control run: %v", err)
	}
	prompt := requestPrompt(fixture.chat.request(0))
	leaked := false
	for _, marker := range []string{"作者投影", "人工指令", "critic finding", "作者状态"} {
		if strings.Contains(prompt, marker) {
			leaked = true
			break
		}
	}
	if !leaked {
		t.Fatalf("control design.critic run did NOT replay the seeded author/critic content; the independence fixture is not discriminating:\n%s", prompt)
	}
}

// judgeLeakMarkers is the SINGLE marker set the independence acceptance uses.
// H-F10b #33: the direct-injection mutation below evaluates the same set, so it
// proves the ASSERTION discriminates, not merely that the history helper sees a
// spliced segment.
func judgeLeakMarkers() []string {
	return []string{
		"作者投影", "作者私有结论标记", "作者已提交", "人工指令",
		"critic finding", "作者状态", "团队摘要",
		"澄清交接包正文",
		"作者完成结论", "task.complete:run:",
	}
}

// judgeLeakIn reports the first independence marker present in text, or "".
func judgeLeakIn(text string) string {
	for _, marker := range judgeLeakMarkers() {
		if strings.Contains(text, marker) {
			return marker
		}
	}
	return ""
}

// packetPrompt renders a context packet the way requestPrompt renders the chat
// request the assembler produces from it: the concatenation of every segment's
// content. It lets a packet-level mutation be judged by the SAME prompt-level
// predicate the acceptance test applies.
func packetPrompt(packet agentcontext.ContextPacket) string {
	var builder strings.Builder
	for _, segment := range packet.Segments {
		builder.WriteString(segment.Content)
		builder.WriteString("\n")
	}
	return builder.String()
}

// TestJudgeRunContextIndependenceDirectInjection is the second, code-level
// mutation the brief asks for: take the judge run's own assembled packet and
// inject an author message into its history the way a regression in prepare()
// would. H-F10b #33 upgraded it from a helper-level check ("historySegments sees
// the splice") to the ACCEPTANCE assertion itself: the mutated packet's rendered
// prompt must trip the same leak predicate the acceptance test uses, while the
// unmutated packet must not. That proves the assertion — not just the fixture or
// the helper — catches a leak.
func TestJudgeRunContextIndependenceDirectInjection(t *testing.T) {
	fixture := seedJudgeIndependenceFixture(t)
	item := judgeWorkItemWithCases(20, nil, "")
	if _, err := mustRun(t, fixture, item); err != nil {
		t.Fatalf("judge run: %v", err)
	}
	packet := decodeStoredPacket(t, fixture.repo.packets[0])
	if history := historySegments(packet); len(history) != 0 {
		t.Fatalf("baseline judge packet already has history: %d", len(history))
	}
	if leaked := judgeLeakIn(packetPrompt(packet)); leaked != "" {
		t.Fatalf("baseline judge packet already trips the acceptance predicate (%q)", leaked)
	}

	// Mutate: splice an author message into the packet's history segment,
	// exactly as a prepare() regression that stopped forcing Conversation
	// empty would produce.
	mutated := packet
	mutated.Segments = append(mutated.Segments, agentcontext.Segment{
		Name: agentcontext.SegmentHistory, Role: "assistant", Content: "作者投影：注入的泄漏消息。",
	})
	if history := historySegments(mutated); len(history) == 0 {
		t.Fatal("mutation did not register as history; the independence assertion would not discriminate")
	}
	if leaked := judgeLeakIn(packetPrompt(mutated)); leaked == "" {
		t.Fatal("the acceptance leak predicate did NOT flag the injected author message; " +
			"the independence assertion itself is not discriminating (H-F10b #33)")
	}
}

// TestJudgeRunContextExcludesAuthorCompletionFacts is Important #3 at the real
// prepare() path: the author's task.complete conclusion rides the briefing
// FINDINGS group (kind=progress), not the handoff. The judge must not read it,
// while the objective approved-page fact of the same group stays.
func TestJudgeRunContextExcludesAuthorCompletionFacts(t *testing.T) {
	fixture := seedJudgeIndependenceFixture(t)
	if _, err := mustRun(t, fixture, judgeWorkItemWithCases(20, nil, "")); err != nil {
		t.Fatalf("judge run: %v", err)
	}
	prompt := requestPrompt(fixture.chat.request(0))
	for _, leaked := range []string{"作者完成结论", "task.complete:run:", "[" + version.FactKindProgress + "]"} {
		if strings.Contains(prompt, leaked) {
			t.Fatalf("judge prompt leaked the author completion fact %q:\n%s", leaked, prompt)
		}
	}
	if !strings.Contains(prompt, "已批准页面") {
		t.Fatalf("judge prompt dropped the objective version fact of the same group:\n%s", prompt)
	}
}

// TestJudgeRunContextCompletionFactControlLeaks is the discriminating control for
// the test above: the SAME seeded progress fact DOES reach a design.critic run
// (which legitimately reads the version briefing). Without it, a change that
// starved every design-team briefing would look like a passing independence fix.
func TestJudgeRunContextCompletionFactControlLeaks(t *testing.T) {
	fixture := seedJudgeIndependenceFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "已提交 finding。"}}
	item := fakeWorkItem(agentapp.WorkTypeDesignCritic, agentapp.WorkItemPayload{
		ConversationID: 20, RequirementID: 501, RequirementVersionID: 700,
	})
	item.Team = agent.TeamDesign
	if _, err := mustRun(t, fixture, item); err != nil {
		t.Fatalf("critic control run: %v", err)
	}
	prompt := requestPrompt(fixture.chat.request(0))
	if !strings.Contains(prompt, "作者完成结论") {
		t.Fatalf("control design.critic run did NOT read the seeded author completion fact; the fixture is not discriminating:\n%s", prompt)
	}
}

// TestJudgeTaskContextRendersTargetCases pins the T14 cases[] consumption:
// the judge's task context names the three-dimension rubric, the read-only
// boundary, and the EXACT pending draft set (case_id/case_version_id/
// content_hash) it must review — not the full-draft-set generation anchor.
func TestJudgeTaskContextRendersTargetCases(t *testing.T) {
	fixture := seedJudgeIndependenceFixture(t)
	cases := []map[string]any{
		{"case_id": "7001", "case_version_id": "8001", "content_hash": strings.Repeat("a", 64)},
		{"case_id": "7002", "case_version_id": "8002", "content_hash": strings.Repeat("b", 64)},
	}
	item := judgeWorkItemWithCases(0, cases, "8001,8002,8003")

	if _, err := mustRun(t, fixture, item); err != nil {
		t.Fatalf("judge run: %v", err)
	}
	prompt := requestPrompt(fixture.chat.request(0))

	for _, dim := range []string{"覆盖率", "可执行标准", "关联系统"} {
		if !strings.Contains(prompt, dim) {
			t.Fatalf("judge task context missing rubric dimension %q:\n%s", dim, prompt)
		}
	}
	for _, boundary := range []string{"只读", "不确认"} {
		if !strings.Contains(prompt, boundary) {
			t.Fatalf("judge task context missing read-only boundary %q:\n%s", boundary, prompt)
		}
	}
	for _, target := range []string{"7001", "8001", "7002", "8002"} {
		if !strings.Contains(prompt, target) {
			t.Fatalf("judge task context does not name target case/case_version %q:\n%s", target, prompt)
		}
	}
	// 8003 is only in the full-draft-set anchor, not a pending review target:
	// it must not be presented as a case to review (it may already be judged).
	// The substring matches the ACTUAL judge_case scope format (colon-separated,
	// see taskContext), not a hypothetical "=" format that would never match.
	if strings.Contains(prompt, "8003") {
		t.Fatalf("judge task context leaked the non-pending anchor case 8003 as a review target:\n%s", prompt)
	}
}
