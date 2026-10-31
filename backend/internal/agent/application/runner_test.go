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
}

func newRunnerFixture(t *testing.T) *runnerFixture {
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
			Name: "需求.md", MIME: "text/markdown", Status: "ready", Text: "登录需求正文", TotalChars: 6,
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
		ToolDeps: tools.Deps{
			Requirement: fixture.requirement,
			Attachment:  &fakeAttachmentPort{},
			Resource:    &fakeResourcePort{},
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

	var roles []string
	for _, message := range fixture.repo.messages {
		roles = append(roles, string(message.Role))
	}
	wantRoles := []string{"system", "user", "assistant"}
	if strings.Join(roles, ",") != strings.Join(wantRoles, ",") {
		t.Fatalf("agent transcript roles = %v", roles)
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
	fixture.chat.errors = []error{fmt.Errorf("upstream: %w", llmapp.ErrContextOverflow)}
	fixture.chat.responses = []llmapp.ChatResponse{
		{Content: "recovered"},
		{Content: "恢复后的回复"},
	}

	result, err := mustRun(t, fixture, hostItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != agent.AgentRunDone {
		t.Fatalf("result = %#v", result)
	}
	if fixture.chat.requestCount() != 2 {
		t.Fatalf("chat calls = %d, want overflow then retry", fixture.chat.requestCount())
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
	item := fakeWorkItem("design.round", agentapp.WorkItemPayload{ConversationID: 20})

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
	if _, ok := agentapp.ProfileKeyForWorkType("design.round"); ok {
		t.Fatal("P2+ work type must not map to a P1 profile")
	}
}
