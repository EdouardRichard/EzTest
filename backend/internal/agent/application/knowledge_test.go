package agentapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// findSection returns one rendered section and whether it exists. Unlike
// sectionByName it tolerates the knowledge section being dropped when empty.
func findSection(packet agentapp.ContextPacket, name string) (agentapp.Section, bool) {
	for _, section := range packet.Sections {
		if section.Name == name {
			return section, true
		}
	}
	return agentapp.Section{}, false
}

func knowledgeRefs() []agentapp.KnowledgeRef {
	return []agentapp.KnowledgeRef{
		{ID: 7001, Title: "支付回调重试约定", Snippet: "回调必须在 5 分钟内完成幂等处理", Scope: "global", Tokens: 123},
		{ID: 7002, Title: "登录超时", Snippet: "会话 30 分钟后过期", Scope: "system", Tokens: 40},
	}
}

func sectionNames(packet agentapp.ContextPacket) []string {
	names := make([]string, 0, len(packet.Sections))
	for _, section := range packet.Sections {
		names = append(names, section.Name)
	}
	return names
}

func TestContextBuildRendersKnowledgeSection(t *testing.T) {
	in := sampleInput()
	in.VersionPack.Knowledge = knowledgeRefs()

	packet, info := buildContext(t, contextSettings(), nil, in)
	if info.Compacted {
		t.Fatalf("small input was compacted: %#v", info)
	}

	section, ok := findSection(packet, "knowledge")
	if !ok {
		t.Fatalf("knowledge section missing: %v", sectionNames(packet))
	}
	if !section.Optional {
		t.Fatal("knowledge section must be optional")
	}
	for _, want := range []string{
		"知识",
		"[K-7001] 支付回调重试约定（global）：回调必须在 5 分钟内完成幂等处理",
		"[K-7002] 登录超时（system）：会话 30 分钟后过期",
	} {
		if !strings.Contains(section.Content, want) {
			t.Fatalf("knowledge section misses %q: %s", want, section.Content)
		}
	}
	if strings.Contains(section.Content, "回调必须在 5 分钟内完成幂等处理\n\n") {
		t.Fatalf("knowledge section has a trailing blank line: %q", section.Content)
	}

	wantCount := agentapp.EstimateTokens(section.Content)
	if packet.TokenCounts["knowledge"] != wantCount || wantCount <= 0 {
		t.Fatalf("knowledge token count = %d, want %d", packet.TokenCounts["knowledge"], wantCount)
	}

	// The knowledge section sits right after the version pack and before the
	// conversation so the model reads version facts before reference knowledge.
	names := strings.Join(sectionNames(packet), ",")
	want := "system,version_pack,knowledge,conversation,team_room,artifacts"
	if names != want {
		t.Fatalf("section order = %s, want %s", names, want)
	}
}

func TestContextBuildTruncatesKnowledgeToInjectionBudget(t *testing.T) {
	refs := []agentapp.KnowledgeRef{
		{ID: 7001, Title: "第一条", Snippet: "甲", Scope: "global", Tokens: 30},
		{ID: 7002, Title: "第二条", Snippet: "乙", Scope: "global", Tokens: 30},
		{ID: 7003, Title: "第三条", Snippet: "丙", Scope: "global", Tokens: 10},
	}

	t.Run("first reference over budget truncates the rest", func(t *testing.T) {
		in := sampleInput()
		in.VersionPack.Knowledge = refs
		settings := contextSettings()
		settings.ints["knowledge.injection_budget_tokens"] = 50

		packet, _ := buildContext(t, settings, nil, in)
		section, ok := findSection(packet, "knowledge")
		if !ok {
			t.Fatalf("knowledge section missing: %v", sectionNames(packet))
		}
		if !strings.Contains(section.Content, "[K-7001]") {
			t.Fatalf("first reference missing: %s", section.Content)
		}
		for _, dropped := range []string{"[K-7002]", "[K-7003]"} {
			if strings.Contains(section.Content, dropped) {
				t.Fatalf("reference %s over budget was injected: %s", dropped, section.Content)
			}
		}
	})

	t.Run("references fitting the budget are all injected", func(t *testing.T) {
		in := sampleInput()
		in.VersionPack.Knowledge = refs
		settings := contextSettings()
		settings.ints["knowledge.injection_budget_tokens"] = 70

		packet, _ := buildContext(t, settings, nil, in)
		section, ok := findSection(packet, "knowledge")
		if !ok {
			t.Fatalf("knowledge section missing: %v", sectionNames(packet))
		}
		for _, want := range []string{"[K-7001]", "[K-7002]", "[K-7003]"} {
			if !strings.Contains(section.Content, want) {
				t.Fatalf("reference %s missing: %s", want, section.Content)
			}
		}
	})

	t.Run("zero budget disables injection", func(t *testing.T) {
		in := sampleInput()
		in.VersionPack.Knowledge = refs
		settings := contextSettings()
		settings.ints["knowledge.injection_budget_tokens"] = 0

		packet, _ := buildContext(t, settings, nil, in)
		if _, ok := findSection(packet, "knowledge"); ok {
			t.Fatalf("knowledge section rendered with a zero budget: %v", sectionNames(packet))
		}
	})
}

func TestContextBuildOmitsKnowledgeSectionWhenEmpty(t *testing.T) {
	in := sampleInput()
	if len(in.VersionPack.Knowledge) != 0 {
		t.Fatal("sample input unexpectedly carries knowledge")
	}
	packet, _ := buildContext(t, contextSettings(), nil, in)
	if _, ok := findSection(packet, "knowledge"); ok {
		t.Fatalf("empty knowledge section was rendered: %v", sectionNames(packet))
	}
	if _, ok := packet.TokenCounts["knowledge"]; ok {
		t.Fatalf("token counts carry an empty knowledge section: %#v", packet.TokenCounts)
	}
	if _, ok := findSection(packet, "version_pack"); !ok {
		t.Fatal("dropping the knowledge section must not drop the version pack")
	}
}

func TestCompactDropsKnowledgeSectionOverBudget(t *testing.T) {
	in := sampleInput()
	in.VersionPack.Knowledge = []agentapp.KnowledgeRef{
		{ID: 7001, Title: "超长知识", Snippet: strings.Repeat("参考内容", 2000), Scope: "global", Tokens: 12000},
	}

	compacted, info := agentapp.Compact(in, 300)
	if len(compacted.VersionPack.Knowledge) != 0 {
		t.Fatalf("knowledge refs survived compaction: %#v", compacted.VersionPack.Knowledge)
	}
	found := false
	for _, dropped := range info.DroppedSections {
		if dropped == "knowledge" {
			found = true
		}
	}
	if !found {
		t.Fatalf("knowledge not reported as dropped: %#v", info.DroppedSections)
	}
}

/* -------------------------- runner injection ------------------------------- */

// fakeKnowledgeReader implements agentapp.KnowledgeReader.
type fakeKnowledgeReader struct {
	mu         sync.Mutex
	refs       []agentapp.KnowledgeRef
	tokens     int
	err        error
	calls      int
	versionID  int64
	systemKeys []string
	query      string
}

func (f *fakeKnowledgeReader) RetrieveForVersion(_ context.Context, versionID int64, systemKeys []string, query string) ([]agentapp.KnowledgeRef, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.versionID = versionID
	f.systemKeys = append([]string(nil), systemKeys...)
	f.query = query
	if f.err != nil {
		return nil, 0, f.err
	}
	return append([]agentapp.KnowledgeRef(nil), f.refs...), f.tokens, nil
}

// fakeKnowledgeUsageRecorder implements agentapp.KnowledgeUsageRecorder.
type fakeKnowledgeUsageRecorder struct {
	mu         sync.Mutex
	calls      int
	agentRunID int64
	workItemID int64
	refs       []agentapp.KnowledgeRef
	err        error
}

func (f *fakeKnowledgeUsageRecorder) RecordInjection(_ context.Context, agentRunID, workItemID int64, refs []agentapp.KnowledgeRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.agentRunID = agentRunID
	f.workItemID = workItemID
	f.refs = append([]agentapp.KnowledgeRef(nil), refs...)
	return f.err
}

func TestRunnerInjectsKnowledgeAndRecordsUsageOnce(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.knowledge.refs = knowledgeRefs()
	fixture.knowledge.tokens = 163
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "请补充登录方式与验收标准？"}}

	result, err := mustRun(t, fixture, hostItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != agent.AgentRunDone {
		t.Fatalf("result = %#v", result)
	}

	reader := fixture.knowledge
	if reader.calls != 1 || reader.versionID != 10 {
		t.Fatalf("knowledge reader calls = %d version = %d", reader.calls, reader.versionID)
	}
	if len(reader.systemKeys) != 0 {
		t.Fatalf("runner must let the adapter derive system keys, got %v", reader.systemKeys)
	}
	if !strings.Contains(reader.query, "订单系统") || !strings.Contains(reader.query, "登录") {
		t.Fatalf("runner did not derive the FTS query from the version pack: %q", reader.query)
	}
	if len(fixture.repo.packets) != 1 {
		t.Fatalf("context packets = %d", len(fixture.repo.packets))
	}
	packet := fixture.repo.packets[0]
	if !strings.Contains(sectionByName(t, packet, "knowledge").Content, "[K-7001]") {
		t.Fatalf("persisted packet misses injected knowledge: %#v", packet.Sections)
	}

	usage := fixture.knowledgeUsage
	if usage.calls != 1 {
		t.Fatalf("knowledge usage calls = %d, want 1", usage.calls)
	}
	if usage.agentRunID != result.AgentRunID || usage.workItemID != 1001 {
		t.Fatalf("usage run/work item = %d/%d, want %d/1001", usage.agentRunID, usage.workItemID, result.AgentRunID)
	}
	if len(usage.refs) != 2 || usage.refs[0].ID != 7001 || usage.refs[0].Tokens != 123 {
		t.Fatalf("usage refs = %#v", usage.refs)
	}
}

func TestRunnerTreatsKnowledgeUsageFailureAsLogOnly(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.knowledge.refs = knowledgeRefs()
	fixture.knowledgeUsage.err = errors.New("knowledge_usage write failed")
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "请补充登录方式与验收标准？"}}

	result, err := mustRun(t, fixture, hostItem())
	if err != nil {
		t.Fatalf("usage failure failed the run: %v", err)
	}
	if result.Status != agent.AgentRunDone {
		t.Fatalf("result = %#v", result)
	}
	if fixture.knowledgeUsage.calls != 1 {
		t.Fatalf("knowledge usage calls = %d, want 1", fixture.knowledgeUsage.calls)
	}
	if !strings.Contains(sectionByName(t, fixture.repo.packets[0], "knowledge").Content, "[K-7001]") {
		t.Fatal("knowledge section lost after a usage failure")
	}
}

func TestRunnerContinuesWhenKnowledgeRetrievalFails(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.knowledge.err = errors.New("knowledge repository unavailable")
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "请补充登录方式与验收标准？"}}

	result, err := mustRun(t, fixture, hostItem())
	if err != nil {
		t.Fatalf("retrieval failure failed the run: %v", err)
	}
	if result.Status != agent.AgentRunDone {
		t.Fatalf("result = %#v", result)
	}
	if _, ok := findSection(fixture.repo.packets[0], "knowledge"); ok {
		t.Fatal("knowledge section rendered after a retrieval failure")
	}
	if fixture.knowledgeUsage.calls != 0 {
		t.Fatalf("usage recorded without injected refs: %d", fixture.knowledgeUsage.calls)
	}
}

func TestRunnerVersionPackToolExposesKnowledgeRefs(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.knowledge.refs = knowledgeRefs()
	fixture.chat.responses = []llmapp.ChatResponse{
		toolCall(tools.ToolContextVersionPack, `{}`),
		{Content: "已读取版本包。"},
	}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var toolContent string
	for _, message := range fixture.repo.messages {
		if message.Role != agent.MessageRoleTool || len(message.Parts) == 0 {
			continue
		}
		if strings.Contains(message.Parts[0].Text, "knowledge") {
			toolContent = message.Parts[0].Text
		}
	}
	if toolContent == "" {
		t.Fatalf("context.version_pack tool result misses knowledge: %#v", fixture.repo.messages)
	}
	var decoded struct {
		Knowledge []struct {
			ID      string `json:"id"`
			Title   string `json:"title"`
			Snippet string `json:"snippet"`
			Scope   string `json:"scope"`
			Tokens  int    `json:"tokens"`
		} `json:"knowledge"`
	}
	if err := json.Unmarshal([]byte(toolContent), &decoded); err != nil {
		t.Fatalf("tool result is not JSON: %v: %s", err, toolContent)
	}
	if len(decoded.Knowledge) != 2 {
		t.Fatalf("tool result knowledge = %#v", decoded.Knowledge)
	}
	first := decoded.Knowledge[0]
	if first.ID != "7001" || first.Title != "支付回调重试约定" || first.Scope != "global" ||
		first.Snippet != "回调必须在 5 分钟内完成幂等处理" || first.Tokens != 123 {
		t.Fatalf("first knowledge ref = %#v", first)
	}
	if strings.Contains(toolContent, "完整正文") {
		t.Fatal("version pack tool result leaked full knowledge content")
	}
}

// fakeKnowledgeWriter implements tools.KnowledgeProposePort for the runner
// composition test: the tool is only reachable through the P7 registry and the
// profile allowlist.
type fakeKnowledgeWriter struct {
	mu         sync.Mutex
	calls      int
	versionID  int64
	agentRunID int64
	input      tools.KnowledgeProposal
	err        error
}

func (f *fakeKnowledgeWriter) ProposeKnowledge(_ context.Context, versionID, agentRunID int64, in tools.KnowledgeProposal) (tools.KnowledgeProposalResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.versionID = versionID
	f.agentRunID = agentRunID
	f.input = in
	if f.err != nil {
		return tools.KnowledgeProposalResult{}, f.err
	}
	return tools.KnowledgeProposalResult{ID: 7005, Status: "draft", Scope: in.Scope, Revision: 1}, nil
}

func TestRunnerHostCanCallKnowledgePropose(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{
		toolCall(tools.ToolKnowledgePropose, `{"title":"支付回调重试约定","content":"回调必须幂等","tags":["支付"],"system_key":"payment"}`),
		{Content: "已提议一条知识草案，等待管理员审核。"},
	}

	result, err := mustRun(t, fixture, hostItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	writer := fixture.knowledgeWriter
	if writer.calls != 1 {
		t.Fatalf("knowledge writer calls = %d, want 1", writer.calls)
	}
	if writer.versionID != 10 || writer.agentRunID != result.AgentRunID {
		t.Fatalf("writer scope = version %d run %d, want 10/%d", writer.versionID, writer.agentRunID, result.AgentRunID)
	}
	if writer.input.Title != "支付回调重试约定" || writer.input.Scope != "system" ||
		writer.input.SystemKey == nil || *writer.input.SystemKey != "payment" {
		t.Fatalf("writer input = %#v", writer.input)
	}
	var toolContent string
	for _, message := range fixture.repo.messages {
		if message.Role == agent.MessageRoleTool && len(message.Parts) > 0 && strings.Contains(message.Parts[0].Text, "knowledge_id") {
			toolContent = message.Parts[0].Text
		}
	}
	if !strings.Contains(toolContent, `"knowledge_id":"7005"`) || !strings.Contains(toolContent, `"status":"draft"`) {
		t.Fatalf("tool result = %q", toolContent)
	}
}

func TestRunnerRejectsKnowledgeProposeOutsideWhitelist(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{
		toolCall(tools.ToolKnowledgePropose, `{"title":"标题","content":"内容"}`),
		{Content: "已创建需求草案，请确认。"},
	}

	if _, err := mustRun(t, fixture, curatorItem()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fixture.knowledgeWriter.calls != 0 {
		t.Fatalf("knowledge writer calls = %d, want 0 for a non-P7 profile", fixture.knowledgeWriter.calls)
	}
	var toolContent string
	for _, message := range fixture.repo.messages {
		if message.Role == agent.MessageRoleTool && len(message.Parts) > 0 {
			toolContent = message.Parts[0].Text
		}
	}
	if !strings.Contains(toolContent, "not allowed") {
		t.Fatalf("tool result = %q", toolContent)
	}
}

func TestKnowledgeQueryUsesTitleAndRequirements(t *testing.T) {
	query := agentapp.KnowledgeQuery(agentapp.VersionPack{
		Title: "  订单系统   Q4 ",
		Requirements: []string{
			"#501 draft 登录",
			"#501 draft   登录",
			"退款时效",
		},
	})
	if query != "订单系统 Q4 #501 draft 登录 退款时效" {
		t.Fatalf("query = %q", query)
	}
}

func TestKnowledgeQueryEmptyPack(t *testing.T) {
	if query := agentapp.KnowledgeQuery(agentapp.VersionPack{}); query != "" {
		t.Fatalf("empty query = %q", query)
	}
}

func TestKnowledgeQueryIsBounded(t *testing.T) {
	pack := agentapp.VersionPack{Title: strings.Repeat("长", 2000)}
	query := agentapp.KnowledgeQuery(pack)
	if len([]rune(query)) > agentapp.KnowledgeQueryMaxChars {
		t.Fatalf("query runes = %d, want <= %d", len([]rune(query)), agentapp.KnowledgeQueryMaxChars)
	}
}
