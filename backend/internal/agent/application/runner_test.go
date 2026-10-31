package agentapp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

var runnerClockStart = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

type runnerFixture struct {
	runner      *agentapp.Runner
	repo        *fakeRuntimeRepo
	chat        *scriptedChat
	conv        *fakeConversationPort
	versions    *fakeVersionReader
	attachments *fakeAttachmentReader
	team        *fakeTeamRoomPort
	content     *fakeContentWriter
	settings    *fakeSettings
	clock       *manualClock
	events      *fakePublisher
	audit       *fakeAuditRecorder
	requirement *fakeRequirementPort
	design      *fakeDesignPort
}

func newRunnerFixture(t *testing.T) *runnerFixture {
	t.Helper()
	return newRunnerFixtureWithTaskProvider(t, nil)
}

func newRunnerFixtureWithTaskProvider(t *testing.T, taskProvider agentapp.TaskContextProvider) *runnerFixture {
	t.Helper()
	repo := newFakeRuntimeRepo()
	if _, err := agentapp.EnsureProfiles(context.Background(), repo); err != nil {
		t.Fatalf("seed profiles: %v", err)
	}
	fixture := &runnerFixture{
		repo: repo,
		chat: &scriptedChat{},
		conv: &fakeConversationPort{
			messages: map[int64]agentapp.ConversationMessage{
				501: {ID: 501, Role: "user", Text: "我想做一个登录功能"},
			},
			snapshot: agentapp.ConversationSnapshot{
				Summary:     "此前讨论了登录。",
				PinnedFacts: []string{"支持手机号登录"},
				Messages:    []agentapp.ConversationMessage{{ID: 501, Role: "user", Text: "我想做一个登录功能"}},
			},
		},
		versions: &fakeVersionReader{pack: agentapp.VersionPack{
			Title: "订单系统", Status: "initial", Requirements: []string{"#501 draft 登录"},
		}},
		attachments: &fakeAttachmentReader{info: agentapp.AttachmentInfo{
			Name: "需求.md", MIME: "text/markdown", Status: "done", Text: "登录需求正文", TotalChars: 6,
		}},
		team:    &fakeTeamRoomPort{},
		content: &fakeContentWriter{},
		settings: &fakeSettings{ints: map[string]int64{
			"agent.max_turns":             12,
			"agent.tool_result_max_bytes": 32768,
		}},
		clock:  &manualClock{now: runnerClockStart},
		events: &fakePublisher{},
		audit:  &fakeAuditRecorder{},
		requirement: &fakeRequirementPort{
			created: tools.RequirementSummary{ID: 501, CurrentVersionID: 700, Revision: 1, Status: "draft", Title: "登录功能"},
			updated: tools.RequirementSummary{ID: 501, CurrentVersionID: 701, Revision: 2, Status: "draft", Title: "登录功能"},
			detail:  tools.RequirementDetail{RequirementSummary: tools.RequirementSummary{ID: 501, Status: "draft"}},
		},
		design: &fakeDesignPort{},
	}
	fixture.runner = agentapp.NewRunner(agentapp.RunnerDeps{
		Runtime:      repo,
		Chat:         fixture.chat,
		Settings:     fixture.settings,
		Clock:        fixture.clock,
		IDs:          &seqIDGenerator{},
		Events:       fixture.events,
		Audit:        fixture.audit,
		Conversation: fixture.conv,
		Versions:     fixture.versions,
		Attachments:  fixture.attachments,
		TeamRoom:     fixture.team,
		Content:      fixture.content,
		TaskProvider: taskProvider,
		ToolDeps: tools.Deps{
			Requirement: fixture.requirement,
			Attachment:  &fakeAttachmentPort{},
			Resource:    &fakeResourcePort{},
			Design:      fixture.design,
		},
		InstanceID: "instance-1",
	})
	return fixture
}

func hostItem() agent.WorkItem {
	return fakeWorkItem(agentapp.WorkTypeChatTurn, agentapp.WorkItemPayload{
		ConversationID: 20, MessageID: 501, Trigger: "user_message",
	})
}

func curatorItem() agent.WorkItem {
	return fakeWorkItem(agentapp.WorkTypeRequirementCurate, agentapp.WorkItemPayload{
		ConversationID: 20, MessageID: 501,
	})
}

func scoutItem() agent.WorkItem {
	return fakeWorkItem(agentapp.WorkTypeAttachmentIngest, agentapp.WorkItemPayload{
		ConversationID: 20, AttachmentID: 9001, MessageID: 501,
	})
}

func mustRun(t *testing.T, fixture *runnerFixture, item agent.WorkItem) (agentapp.RunResult, error) {
	t.Helper()
	return fixture.runner.Run(context.Background(), item)
}

func eventTypes(publisher *fakePublisher) []string {
	types := make([]string, 0, len(publisher.events))
	for _, event := range publisher.events {
		types = append(types, event.Type)
	}
	return types
}

func containsEvent(publisher *fakePublisher, eventType string) bool {
	for _, event := range publisher.events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func requestPrompt(request llmapp.ChatRequest) string {
	var builder strings.Builder
	for _, message := range request.Messages {
		builder.WriteString(message.Content)
		builder.WriteString("\n")
	}
	return builder.String()
}

func toolCall(name, arguments string) llmapp.ChatResponse {
	return llmapp.ChatResponse{
		ToolCalls:    []llmapp.ToolCall{{ID: "call_1", Name: name, Arguments: arguments}},
		FinishReason: "tool_calls",
	}
}

func TestRunnerHostWritesFinalConversationMessage(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "请补充登录方式与验收标准？"}}

	result, err := mustRun(t, fixture, hostItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != agent.AgentRunDone || result.Turns != 1 || result.AgentRunID == 0 {
		t.Fatalf("result = %#v", result)
	}
	if len(fixture.conv.appended) != 1 {
		t.Fatalf("conversation messages = %d", len(fixture.conv.appended))
	}
	message := fixture.conv.appended[0]
	if len(message.Parts) == 0 || message.Parts[0].Type != "text" || !strings.Contains(message.Parts[0].Text, "登录方式") {
		t.Fatalf("parts = %#v", message.Parts)
	}
	if message.ReplyToMessageID == nil || *message.ReplyToMessageID != 501 {
		t.Fatalf("reply_to = %v", message.ReplyToMessageID)
	}
	if message.AgentRunID != result.AgentRunID {
		t.Fatalf("agent run id = %d, want %d", message.AgentRunID, result.AgentRunID)
	}
	if !containsEvent(fixture.events, "agent.run.finished") {
		t.Fatalf("events = %v", eventTypes(fixture.events))
	}
	if len(fixture.repo.packets) != 1 {
		t.Fatalf("context packets = %d", len(fixture.repo.packets))
	}
	if len(fixture.chat.requests) == 0 || len(fixture.chat.requests[0].Messages) < 2 {
		t.Fatalf("chat request missing messages: %#v", fixture.chat.requests)
	}
	prompt := ""
	for _, message := range fixture.chat.requests[0].Messages {
		prompt += message.Content + "\n"
	}
	for _, want := range []string{"订单系统", "此前讨论了登录", "支持手机号登录", "我想做一个登录功能"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("model prompt missing %q: %s", want, prompt)
		}
	}

	var roles []string
	for _, message := range fixture.repo.messages {
		roles = append(roles, string(message.Role))
	}
	wantRoles := []string{"system", "user", "assistant"}
	if strings.Join(roles, ",") != strings.Join(wantRoles, ",") {
		t.Fatalf("agent transcript roles = %v", roles)
	}
}

func TestRunnerRejectsToolsOutsideProfileAllowlist(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{
		toolCall(tools.ToolRequirementDraftCreate, `{"title":"越权","description":"x","acceptance_criteria":"x","priority":"P1"}`),
		{Content: "工具被拒绝，继续澄清。"},
	}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fixture.requirement.createDraft.Title != "" {
		t.Fatalf("disallowed tool executed: %#v", fixture.requirement.createDraft)
	}
	var toolContent string
	for _, message := range fixture.chat.requests[1].Messages {
		if message.Role == "tool" {
			toolContent = message.Content
		}
	}
	if !strings.Contains(toolContent, "not allowed") {
		t.Fatalf("tool result = %q", toolContent)
	}
}

func TestRunnerCuratorToolCallWritesRequirementCard(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{
		toolCall(tools.ToolRequirementDraftCreate, `{"title":"登录功能","description":"支持手机号登录","acceptance_criteria":"可登录","priority":"P1"}`),
		{Content: "已创建需求草案，请确认。"},
	}

	result, err := mustRun(t, fixture, curatorItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != agent.AgentRunDone || result.Turns != 2 {
		t.Fatalf("result = %#v", result)
	}
	if fixture.requirement.createDraft.Title != "登录功能" {
		t.Fatalf("draft = %#v", fixture.requirement.createDraft)
	}
	if len(fixture.conv.appended) != 1 {
		t.Fatalf("conversation messages = %d", len(fixture.conv.appended))
	}
	parts := fixture.conv.appended[0].Parts
	if len(parts) != 2 || parts[0].Type != "text" || parts[1].Type != "requirement_card" {
		t.Fatalf("parts = %#v", parts)
	}
	if parts[1].Data["requirement_id"] != "501" || parts[1].Data["revision"] != 1 {
		t.Fatalf("card = %#v", parts[1].Data)
	}

	toolMessages := 0
	for _, message := range fixture.repo.messages {
		if message.Role == agent.MessageRoleTool {
			toolMessages++
			if message.ToolCallID == nil || *message.ToolCallID != "call_1" {
				t.Fatalf("tool message = %#v", message)
			}
		}
	}
	if toolMessages != 1 {
		t.Fatalf("tool transcript messages = %d", toolMessages)
	}
}

func TestRunnerScoutWritesTeamRoomFinding(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "已读取附件：需求.md，共 6 字符。关键约束：手机号登录。"}}

	result, err := mustRun(t, fixture, scoutItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != agent.AgentRunDone {
		t.Fatalf("result = %#v", result)
	}
	if len(fixture.team.posts) != 1 {
		t.Fatalf("team posts = %d", len(fixture.team.posts))
	}
	if len(fixture.chat.requests) == 0 || len(fixture.chat.requests[0].Messages) < 2 {
		t.Fatalf("chat request missing user attachment context: %#v", fixture.chat.requests)
	}
	userContent := fixture.chat.requests[0].Messages[1].Content
	if !strings.Contains(userContent, "请阅读附件《需求.md》") || !strings.Contains(userContent, "登录需求正文") {
		t.Fatalf("scout user content = %q", userContent)
	}
	post := fixture.team.posts[0]
	if post.Team != agent.TeamClarification || post.Kind != agent.PostKindFinding {
		t.Fatalf("post = %#v", post)
	}
	if len(post.Refs) != 1 || post.Refs[0].Kind != "attachment" || post.Refs[0].ID != "9001" {
		t.Fatalf("refs = %#v", post.Refs)
	}
	if len(fixture.conv.appended) != 0 {
		t.Fatalf("scout wrote a conversation message: %#v", fixture.conv.appended)
	}
}

func TestRunnerStopsAtMaxTurnsBudget(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.settings.ints["agent.max_turns"] = 2
	fixture.chat.responses = []llmapp.ChatResponse{
		toolCall(tools.ToolContextVersionPack, `{}`),
		toolCall(tools.ToolContextTaskPack, `{}`),
	}

	result, err := mustRun(t, fixture, hostItem())
	if err == nil {
		t.Fatal("expected a budget failure")
	}
	if result.Status != agent.AgentRunFailed || result.TerminalReason != "budget" {
		t.Fatalf("result = %#v", result)
	}
	if result.Turns != 2 {
		t.Fatalf("turns = %d, want 2", result.Turns)
	}
	if agentapp.IsRetryable(err) {
		t.Fatalf("budget failure must not be retryable: %v", err)
	}
	finished := fixture.repo.lastFinished(t)
	if finished.Status != agent.AgentRunFailed || finished.TerminalReason == nil || *finished.TerminalReason != "budget" {
		t.Fatalf("finished run = %#v", finished)
	}
	if !containsEvent(fixture.events, "agent.run.failed") {
		t.Fatalf("events = %v", eventTypes(fixture.events))
	}
}

func TestRunnerSoftTimeoutDropsTools(t *testing.T) {
	fixture := newRunnerFixture(t)
	profile := fixture.repo.profiles[agentapp.ProfileKeyClarificationHost]
	profile.Budgets.SoftSeconds = 300
	fixture.repo.profiles[agentapp.ProfileKeyClarificationHost] = profile

	fixture.chat.onChat = func(index int, _ llmapp.ChatRequest) {
		if index == 0 {
			fixture.clock.Advance(301 * time.Second)
		}
	}
	fixture.chat.responses = []llmapp.ChatResponse{
		toolCall(tools.ToolContextVersionPack, `{}`),
		{Content: "收尾回复"},
	}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fixture.chat.requestCount() != 2 {
		t.Fatalf("chat calls = %d", fixture.chat.requestCount())
	}
	if len(fixture.chat.request(0).Tools) == 0 {
		t.Fatal("first call should expose tools")
	}
	if len(fixture.chat.request(1).Tools) != 0 {
		t.Fatalf("soft-timeout call still exposes tools: %d", len(fixture.chat.request(1).Tools))
	}
}

func TestRunnerCompactsAndRetriesOnContextOverflow(t *testing.T) {
	fixture := newRunnerFixture(t)
	// First model call asks for a tool, the second rejects the accumulated
	// tool result as context overflow, and the retry must drop that history.
	fixture.chat.errors = []error{nil, fmt.Errorf("upstream: %w", llmapp.ErrContextOverflow)}
	fixture.chat.responses = []llmapp.ChatResponse{
		toolCall(tools.ToolContextVersionPack, `{}`),
		{Content: "恢复后的回复"},
	}

	result, err := mustRun(t, fixture, hostItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != agent.AgentRunDone {
		t.Fatalf("result = %#v", result)
	}
	if fixture.chat.requestCount() != 3 {
		t.Fatalf("chat calls = %d, want tool, overflow, retry", fixture.chat.requestCount())
	}
	requests := fixture.chat.requests
	before := requests[1]
	after := requests[2]
	if len(after.Messages) >= len(before.Messages) {
		t.Fatalf("overflow retry did not shrink messages: before=%d after=%d", len(before.Messages), len(after.Messages))
	}
	for _, message := range after.Messages {
		if message.Role == "tool" {
			t.Fatalf("overflow retry kept a tool message: %#v", after.Messages)
		}
	}
	if !containsEvent(fixture.events, "context.compacted") {
		t.Fatalf("events = %v", eventTypes(fixture.events))
	}
}

func TestRunnerSpillsOversizedToolResult(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.settings.ints["agent.tool_result_max_bytes"] = 64
	fixture.versions.pack.Requirements = []string{strings.Repeat("长", 300)}
	fixture.chat.responses = []llmapp.ChatResponse{
		toolCall(tools.ToolContextVersionPack, `{}`),
		{Content: "已读取上下文"},
	}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fixture.content.puts) != 1 {
		t.Fatalf("spilled results = %d", len(fixture.content.puts))
	}
	var toolMessage *agent.AgentMessage
	for i := range fixture.repo.messages {
		if fixture.repo.messages[i].Role == agent.MessageRoleTool {
			toolMessage = &fixture.repo.messages[i]
		}
	}
	if toolMessage == nil {
		t.Fatal("no tool message recorded")
	}
	content := ""
	if len(toolMessage.Parts) > 0 {
		content = toolMessage.Parts[0].Text
	}
	if !strings.Contains(content, `"truncated":true`) || !strings.Contains(content, "content_ref") {
		t.Fatalf("tool message content = %q", content)
	}
	if !strings.Contains(content, `"sha256"`) {
		t.Fatalf("tool message content = %q", content)
	}
}

func TestRunnerLeaseLostOnFinishIsNotRetried(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.repo.finishRunErr = agent.ErrLeaseLost
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "回复"}}

	_, err := mustRun(t, fixture, hostItem())
	if err == nil {
		t.Fatal("expected a lease_lost error")
	}
	if !agentapp.IsLeaseLost(err) {
		t.Fatalf("IsLeaseLost = false for %v", err)
	}
	if agentapp.IsRetryable(err) {
		t.Fatalf("lease_lost must not be retryable: %v", err)
	}
}

func TestRunnerRejectsUnknownWorkType(t *testing.T) {
	fixture := newRunnerFixture(t)
	item := fakeWorkItem("execute.case", agentapp.WorkItemPayload{ConversationID: 20})

	_, err := mustRun(t, fixture, item)
	if err == nil {
		t.Fatal("expected an error")
	}
	if agentapp.IsRetryable(err) {
		t.Fatalf("unknown work type must not be retryable: %v", err)
	}
	if fixture.chat.requestCount() != 0 {
		t.Fatalf("chat calls = %d", fixture.chat.requestCount())
	}
}

func TestRunnerRejectsModelSuppliedVersionID(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{
		toolCall(tools.ToolRequirementRead, `{"version_id":"999"}`),
		{Content: "参数有误，我会重试。"},
	}

	if _, err := mustRun(t, fixture, curatorItem()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var toolMessage *agent.AgentMessage
	for i := range fixture.repo.messages {
		if fixture.repo.messages[i].Role == agent.MessageRoleTool {
			toolMessage = &fixture.repo.messages[i]
		}
	}
	if toolMessage == nil || len(toolMessage.Parts) == 0 {
		t.Fatal("no tool message recorded")
	}
	if !strings.Contains(toolMessage.Parts[0].Text, "invalid_argument") {
		t.Fatalf("tool error = %q", toolMessage.Parts[0].Text)
	}
}

func TestWorkItemPayloadParsesDecimalStringIDs(t *testing.T) {
	item := fakeWorkItem(agentapp.WorkTypeRequirementCurate, agentapp.WorkItemPayload{
		ConversationID: 20, MessageID: 501,
	})
	payload, err := agentapp.ParseWorkItemPayload(item.Payload)
	if err != nil {
		t.Fatalf("ParseWorkItemPayload: %v", err)
	}
	if payload.ConversationID != 20 || payload.MessageID != 501 {
		t.Fatalf("payload = %#v", payload)
	}

	raw, _ := json.Marshal(map[string]any{"conversation_id": 20, "message_id": 501})
	payload, err = agentapp.ParseWorkItemPayload(raw)
	if err != nil {
		t.Fatalf("numeric payload: %v", err)
	}
	if payload.ConversationID != 20 || payload.MessageID != 501 {
		t.Fatalf("numeric payload = %#v", payload)
	}
}

const designProposalJSON = `{"requirement_id":"501","requirement_version_id":"700","scenarios":[{"ref":"s1","name":"正常登录","version_sut_ids":["9001"]}],"cases":[{"ref":"c1","scenario_ref":"s1","title":"账号密码登录成功","priority":"P1","default_version_sut_id":"9001","version_sut_ids":["9001"],"steps":[{"seq":1,"action":"打开登录页","expected":"显示表单"}],"assertions":[{"step_seq":1,"kind":"ui","target":"表单","expected":"可见"}],"sources":[{"kind":"requirement","ref_id":"700"}]}]}`

func designRequirementItem() agent.WorkItem {
	return fakeWorkItem(agentapp.WorkTypeDesignRequirement, agentapp.WorkItemPayload{
		ConversationID: 20, RequirementID: 501, RequirementVersionID: 700, Trigger: "manual",
	})
}

func TestRunnerDesignHostUsesDesignProfileAndTeamRoom(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.conv.kind = "design"
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "我已读取脑图，请告诉我要新增或修改哪条用例。"}}

	item := fakeWorkItem(agentapp.WorkTypeChatTurn, agentapp.WorkItemPayload{
		ConversationID: 20, Trigger: "opening",
	})
	result, err := mustRun(t, fixture, item)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != agent.AgentRunDone {
		t.Fatalf("result = %#v", result)
	}
	if len(fixture.conv.appended) != 1 {
		t.Fatalf("design host assistant messages = %d", len(fixture.conv.appended))
	}
	finished := fixture.repo.lastFinished(t)
	if finished.ProfileKey != agentapp.ProfileKeyDesignHost {
		t.Fatalf("profile = %q, want design_host", finished.ProfileKey)
	}
	if len(fixture.team.contextTeams) == 0 || fixture.team.contextTeams[0] != string(agent.TeamDesign) {
		t.Fatalf("team room context teams = %v", fixture.team.contextTeams)
	}
	prompt := requestPrompt(fixture.chat.request(0))
	if !strings.Contains(prompt, "设计会话") || strings.Contains(prompt, "澄清会话的开场") {
		t.Fatalf("design opening prompt = %q", prompt)
	}
	for _, definition := range fixture.chat.request(0).Tools {
		if definition.Name == "case.confirm" {
			t.Fatal("case.confirm must never be exposed to the model")
		}
	}
}

func TestRunnerDesignAuthorCallsGraphToolsWithScopeAndPostsTeamUpdate(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.design.readResult = map[string]any{
		"requirements": []any{map[string]any{"requirement_id": "501"}},
	}
	fixture.design.proposeResult = map[string]any{"graph_hash": "after", "case_ids": []string{"7001"}}
	fixture.chat.responses = []llmapp.ChatResponse{
		toolCall(tools.ToolDesignGraphRead, `{}`),
		toolCall(tools.ToolCaseGraphPropose, designProposalJSON),
		{Content: "已为需求 501 生成 1 个场景与 1 条用例 draft。"},
	}

	result, err := mustRun(t, fixture, designRequirementItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != agent.AgentRunDone || result.Turns != 3 {
		t.Fatalf("result = %#v", result)
	}
	if finished := fixture.repo.lastFinished(t); finished.ProfileKey != agentapp.ProfileKeyCaseAuthor {
		t.Fatalf("profile = %q", finished.ProfileKey)
	}
	if fixture.design.readCalls != 1 || fixture.design.readVersionID != 10 || fixture.design.readRequirementID != 501 {
		t.Fatalf("graph read = %#v", fixture.design)
	}
	if fixture.design.proposeCalls != 1 || fixture.design.proposeVersionID != 10 {
		t.Fatalf("propose call = %#v", fixture.design)
	}
	if fixture.design.proposeInput.RequirementID != 501 || fixture.design.proposeInput.RequirementVersionID != 700 {
		t.Fatalf("proposal = %#v", fixture.design.proposeInput)
	}
	if fixture.design.proposeRunID != result.AgentRunID {
		t.Fatalf("propose run id = %d, want %d", fixture.design.proposeRunID, result.AgentRunID)
	}
	if len(fixture.team.posts) != 1 {
		t.Fatalf("team posts = %#v", fixture.team.posts)
	}
	if post := fixture.team.posts[0]; post.Team != agent.TeamDesign || post.Kind != agent.PostKindStatus ||
		!strings.Contains(post.Content, "场景") {
		t.Fatalf("post = %#v", post)
	}
	if len(fixture.conv.appended) != 0 {
		t.Fatalf("author wrote a conversation message: %#v", fixture.conv.appended)
	}
	prompt := requestPrompt(fixture.chat.request(0))
	for _, want := range []string{"requirement_id=501", "requirement_version_id=700", "design.requirement"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("design task prompt missing %q: %s", want, prompt)
		}
	}
	if len(fixture.team.contextTeams) == 0 || fixture.team.contextTeams[0] != string(agent.TeamDesign) {
		t.Fatalf("team room context teams = %v", fixture.team.contextTeams)
	}
}

func TestRunnerDesignPlannerWritesAssistantMessageAndTeamPost(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.conv.kind = "design"
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "本轮设计范围：1 个待设计需求，预计生成 1 个场景与 2 条用例。"}}
	item := fakeWorkItem(agentapp.WorkTypeDesignRound, agentapp.WorkItemPayload{
		ConversationID: 20, RoundID: 9001,
	})

	if _, err := mustRun(t, fixture, item); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fixture.conv.appended) != 1 || len(fixture.team.posts) != 1 {
		t.Fatalf("messages/posts = %d/%d", len(fixture.conv.appended), len(fixture.team.posts))
	}
	if post := fixture.team.posts[0]; post.Team != agent.TeamDesign || !strings.Contains(post.Content, "范围") {
		t.Fatalf("post = %#v", post)
	}
	prompt := requestPrompt(fixture.chat.request(0))
	for _, want := range []string{"design.round", "round_id=9001"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("planner prompt missing %q: %s", want, prompt)
		}
	}
}

func TestRunnerDesignCriticAndDataPlannerSubmitViaScopedTools(t *testing.T) {
	critic := newRunnerFixture(t)
	critic.chat.responses = []llmapp.ChatResponse{
		toolCall(tools.ToolDesignReviewSubmit, `{"summary":"整体可用","findings":[{"severity":"minor","message":"补充异常步骤断言"}]}`),
		{Content: "已提交 1 条 minor finding。"},
	}
	criticItem := fakeWorkItem(agentapp.WorkTypeDesignCritic, agentapp.WorkItemPayload{
		ConversationID: 20, RequirementID: 501, RequirementVersionID: 700,
	})
	if _, err := mustRun(t, critic, criticItem); err != nil {
		t.Fatalf("critic run: %v", err)
	}
	if critic.design.reviewCalls != 1 || critic.design.reviewRequirementID != 501 {
		t.Fatalf("review call = %#v", critic.design)
	}
	if len(critic.team.posts) != 1 || critic.team.posts[0].Kind != agent.PostKindFinding {
		t.Fatalf("critic posts = %#v", critic.team.posts)
	}
	if len(critic.design.reviewInput.Findings) != 1 || critic.design.reviewInput.Findings[0].Message == "" {
		t.Fatalf("review input = %#v", critic.design.reviewInput)
	}

	dataPlanner := newRunnerFixture(t)
	dataPlanner.design.readResult = map[string]any{
		"cases": []any{map[string]any{"case_id": "7001", "current_version_id": "8001", "status": "draft"}},
	}
	dataPlanner.chat.responses = []llmapp.ChatResponse{
		toolCall(tools.ToolDesignGraphRead, `{}`),
		toolCall(tools.ToolDesignDataPlanSubmit, `{"case_version_id":"8001","preconditions":"已登录","test_data":{"account":"admin"}}`),
		{Content: "已为用例补充前置条件与测试数据。"},
	}
	dataItem := fakeWorkItem(agentapp.WorkTypeDesignDataPlan, agentapp.WorkItemPayload{
		ConversationID: 20, RequirementID: 501, RequirementVersionID: 700,
	})
	if _, err := mustRun(t, dataPlanner, dataItem); err != nil {
		t.Fatalf("data planner run: %v", err)
	}
	if dataPlanner.design.dataPlanCalls != 1 || dataPlanner.design.dataPlanCaseVersionID != 8001 {
		t.Fatalf("data plan call = %#v", dataPlanner.design)
	}
	if len(dataPlanner.team.posts) != 1 || dataPlanner.team.posts[0].Team != agent.TeamDesign {
		t.Fatalf("data planner posts = %#v", dataPlanner.team.posts)
	}
}

func TestWorkItemPayloadParsesDesignIDs(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"conversation_id":        "20",
		"requirement_id":         "501",
		"requirement_version_id": "700",
		"case_id":                "8001",
		"case_version_id":        "8002",
		"round_id":               "9001",
	})
	payload, err := agentapp.ParseWorkItemPayload(raw)
	if err != nil {
		t.Fatalf("ParseWorkItemPayload: %v", err)
	}
	if payload.ConversationID != 20 || payload.RequirementID != 501 || payload.RequirementVersionID != 700 ||
		payload.CaseID != 8001 || payload.CaseVersionID != 8002 || payload.RoundID != 9001 {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestProfileKeyForWorkType(t *testing.T) {
	cases := map[string]string{
		agentapp.WorkTypeChatTurn:          agentapp.ProfileKeyClarificationHost,
		agentapp.WorkTypeRequirementCurate: agentapp.ProfileKeyRequirementCurator,
		agentapp.WorkTypeAttachmentIngest:  agentapp.ProfileKeyContextScout,
	}
	for workType, want := range cases {
		got, ok := agentapp.ProfileKeyForWorkType(workType)
		if !ok || got != want {
			t.Fatalf("ProfileKeyForWorkType(%q) = %q/%v, want %q", workType, got, ok, want)
		}
	}
	// P3 mappings live in the P3 contract test; an unregistered later work type
	// must still not map to a P1/P2 profile.
	if _, ok := agentapp.ProfileKeyForWorkType("report.wave"); ok {
		t.Fatal("P4+ work type must not map to a P1/P2 profile")
	}
}
