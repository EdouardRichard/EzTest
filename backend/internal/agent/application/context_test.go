package agentapp_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

type fakeSummarizer struct {
	summary string
	err     error
	calls   int
	inputs  [][]agentapp.Message
}

func (s *fakeSummarizer) Summarize(_ context.Context, messages []agentapp.Message) (string, error) {
	s.calls++
	s.inputs = append(s.inputs, messages)
	if s.err != nil {
		return "", s.err
	}
	return s.summary, nil
}

func contextSettings() *fakeSettings {
	return &fakeSettings{
		ints: map[string]int64{
			"context.window_tokens":         64000,
			"context.output_reserve_tokens": 8000,
			"context.keep_recent_messages":  3,
		},
		floats: map[string]float64{
			"context.compact_threshold": 0.7,
		},
	}
}

func sampleProfile() agent.AgentProfile {
	return agent.AgentProfile{
		Key:          "clarification_host",
		Name:         "澄清 Host",
		SystemPrompt: "[profile:clarification_host] 你是澄清 Host，只提问、解释与复述，不确认需求。",
		Budgets:      agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 300, HardSeconds: 600},
		Enabled:      true,
	}
}

func sampleInput() agentapp.ContextInput {
	return agentapp.ContextInput{
		Profile: sampleProfile(),
		Task: agentapp.TaskContext{
			Type:               "chat.turn",
			Goal:               "回答用户关于登录流程的问题",
			Constraints:        []string{"不能替用户确认需求", "不删除任何需求"},
			AcceptanceCriteria: []string{"给出至少一个澄清问题"},
			Scope:              []string{"version_id=10", "conversation_id=20"},
		},
		VersionPack: agentapp.VersionPack{
			VersionID:    10,
			Title:        "订单系统 Q4",
			Status:       "running",
			SUTs:         []string{"订单中心 (prod)"},
			Pages:        []string{"/orders"},
			APIs:         []string{"GET /api/orders"},
			Requirements: []string{"#501 draft 登录需求"},
			Attachments:  []string{"login.md (md, 1200 bytes)"},
		},
		Conversation: agentapp.ConversationContext{
			Summary:     "此前讨论了登录方式。",
			PinnedFacts: []string{"用户要求支持手机号登录"},
			RecentMessages: []agentapp.Message{
				{ID: 1, Role: "user", Content: "登录方式有哪些？"},
				{ID: 2, Role: "assistant", Content: "可以密码、手机号或扫码登录。"},
			},
		},
		TeamRoom: agentapp.TeamRoomContext{
			Team: "clarification",
			Digest: &agent.TeamRoomDigest{
				VersionID: 10, Team: agent.TeamClarification,
				Summary: "团队决定：优先手机号登录", TokenEstimate: 30, Version: 1,
			},
			Posts: []agent.TeamRoomPost{
				{ID: 301, VersionID: 10, Team: agent.TeamClarification, Kind: agent.PostKindDecision, Content: "决定支持手机号登录"},
			},
		},
		Artifacts: []agentapp.ArtifactRef{{Kind: "requirement", ID: "501"}},
	}
}

func buildContext(t *testing.T, settings *fakeSettings, summarizer agentapp.Summarizer, in agentapp.ContextInput) (agentapp.ContextPacket, agentapp.CompactInfo) {
	t.Helper()
	builder := agentapp.NewContextBuilder(settings, summarizer)
	packet, info, err := builder.Build(context.Background(), in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return packet, info
}

func sectionByName(t *testing.T, packet agentapp.ContextPacket, name string) agentapp.Section {
	t.Helper()
	for _, section := range packet.Sections {
		if section.Name == name {
			return section
		}
	}
	t.Fatalf("section %q not found in %#v", name, packet.Sections)
	return agentapp.Section{}
}

func TestContextBuildBudgetUsesModelMetadata(t *testing.T) {
	cases := []struct {
		name     string
		budget   agentapp.ModelBudget
		settings *fakeSettings
		want     int
	}{
		{
			name:     "model window wins",
			budget:   agentapp.ModelBudget{ContextWindow: 32000, MaxOutput: 4000},
			settings: contextSettings(),
			want:     32000 - 4000 - agentapp.ToolReserveTokens,
		},
		{
			name:     "settings fallback",
			budget:   agentapp.ModelBudget{},
			settings: contextSettings(),
			want:     64000 - 8000 - agentapp.ToolReserveTokens,
		},
		{
			name:     "output reserve uses the smaller model max output",
			budget:   agentapp.ModelBudget{ContextWindow: 32000, MaxOutput: 1000},
			settings: contextSettings(),
			want:     32000 - 1000 - agentapp.ToolReserveTokens,
		},
		{
			name:     "model max output above settings reserve keeps the reserve",
			budget:   agentapp.ModelBudget{ContextWindow: 32000, MaxOutput: 99000},
			settings: contextSettings(),
			want:     32000 - 8000 - agentapp.ToolReserveTokens,
		},
		{
			name:     "tiny window clamps to minimum usable budget",
			budget:   agentapp.ModelBudget{ContextWindow: 100},
			settings: contextSettings(),
			want:     agentapp.MinUsableBudget,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := sampleInput()
			in.ModelBudget = tc.budget
			_, info := buildContext(t, tc.settings, nil, in)
			if info.Budget != tc.want {
				t.Fatalf("budget = %d, want %d", info.Budget, tc.want)
			}
		})
	}

	t.Run("missing settings fall back to design defaults", func(t *testing.T) {
		in := sampleInput()
		_, info := buildContext(t, &fakeSettings{}, nil, in)
		if info.Budget != agentapp.DefaultWindowTokens-agentapp.DefaultOutputReserveTokens-agentapp.ToolReserveTokens {
			t.Fatalf("budget = %d", info.Budget)
		}
	})
}

func TestContextBuildTrimsRecentWindowBelowThreshold(t *testing.T) {
	in := sampleInput()
	in.Conversation.RecentMessages = []agentapp.Message{
		{ID: 1, Role: "user", Content: "第00条消息"},
		{ID: 2, Role: "assistant", Content: "第01条消息"},
		{ID: 3, Role: "user", Content: "第02条消息"},
		{ID: 4, Role: "assistant", Content: "第03条消息"},
		{ID: 5, Role: "user", Content: "第04条消息"},
	}
	packet, info := buildContext(t, contextSettings(), nil, in)
	if !info.Compacted || info.Summary == "" {
		t.Fatalf("compaction info = %#v", info)
	}
	if !strings.Contains(info.Summary, "第00条消息") {
		t.Fatalf("summary = %q", info.Summary)
	}
	conversation := ""
	for _, section := range packet.Sections {
		if section.Name == "conversation" {
			conversation = section.Content
		}
	}
	if !strings.Contains(conversation, "摘要:") || !strings.Contains(conversation, "第04条消息") {
		t.Fatalf("conversation section = %q", conversation)
	}
}

func TestContextBuildSmallInputKeepsAllSections(t *testing.T) {
	in := sampleInput()
	packet, info := buildContext(t, contextSettings(), nil, in)
	if info.Compacted {
		t.Fatalf("small input was compacted: %#v", info)
	}
	if packet.VersionID != 10 {
		t.Fatalf("version id = %d", packet.VersionID)
	}
	wantNames := []string{"system", "version_pack", "conversation", "team_room", "artifacts"}
	if len(packet.Sections) != len(wantNames) {
		t.Fatalf("sections = %#v", packet.Sections)
	}
	sum := 0
	for index, name := range wantNames {
		section := packet.Sections[index]
		if section.Name != name {
			t.Fatalf("section %d = %q, want %q", index, section.Name, name)
		}
		if packet.TokenCounts[name] != agentapp.EstimateTokens(section.Content) {
			t.Fatalf("token count for %q = %d", name, packet.TokenCounts[name])
		}
		if packet.TokenCounts[name] <= 0 {
			t.Fatalf("section %q has no token count", name)
		}
		sum += packet.TokenCounts[name]
	}
	if packet.TokenCounts["__total__"] != sum {
		t.Fatalf("total = %d, sum = %d", packet.TokenCounts["__total__"], sum)
	}

	system := sectionByName(t, packet, "system").Content
	for _, want := range []string{"[profile:clarification_host]", "回答用户关于登录流程的问题", "不能替用户确认需求", "version_id=10"} {
		if !strings.Contains(system, want) {
			t.Fatalf("system section misses %q: %s", want, system)
		}
	}
	versionPack := sectionByName(t, packet, "version_pack").Content
	for _, want := range []string{"订单系统 Q4", "running", "订单中心", "#501"} {
		if !strings.Contains(versionPack, want) {
			t.Fatalf("version_pack misses %q: %s", want, versionPack)
		}
	}
	conversation := sectionByName(t, packet, "conversation").Content
	for _, want := range []string{"此前讨论了登录方式", "用户要求支持手机号登录", "登录方式有哪些"} {
		if !strings.Contains(conversation, want) {
			t.Fatalf("conversation misses %q: %s", want, conversation)
		}
	}
	teamRoom := sectionByName(t, packet, "team_room").Content
	if !strings.Contains(teamRoom, "优先手机号登录") || !strings.Contains(teamRoom, "决定支持手机号登录") {
		t.Fatalf("team_room = %s", teamRoom)
	}
	artifacts := sectionByName(t, packet, "artifacts").Content
	if !strings.Contains(artifacts, "requirement:501") {
		t.Fatalf("artifacts = %s", artifacts)
	}

	if sectionByName(t, packet, "system").Optional || sectionByName(t, packet, "conversation").Optional {
		t.Fatal("required sections marked optional")
	}
	for _, name := range []string{"version_pack", "team_room", "artifacts"} {
		if !sectionByName(t, packet, name).Optional {
			t.Fatalf("section %q should be optional", name)
		}
	}

	if packet.Hash == "" || len(packet.Hash) != 64 {
		t.Fatalf("hash = %q", packet.Hash)
	}
	repeated, repeatedInfo := buildContext(t, contextSettings(), nil, in)
	if repeated.Hash != packet.Hash {
		t.Fatalf("hash changed between identical builds: %s vs %s", packet.Hash, repeated.Hash)
	}
	if repeatedInfo.Budget != info.Budget || repeatedInfo.Compacted != info.Compacted {
		t.Fatalf("info changed between identical builds: %#v vs %#v", repeatedInfo, info)
	}
}

func longConversation(count int) []agentapp.Message {
	messages := make([]agentapp.Message, 0, count)
	for index := 0; index < count; index++ {
		role := "user"
		if index%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, agentapp.Message{
			ID:      int64(index + 1),
			Role:    role,
			Content: fmt.Sprintf("第%02d条消息：%s", index, strings.Repeat("澄清需求细节", 20)),
		})
	}
	return messages
}

func TestContextBuildCompactsWithSummarizerKeepingPinnedAndRecent(t *testing.T) {
	in := sampleInput()
	in.ModelBudget = agentapp.ModelBudget{ContextWindow: 4000, MaxOutput: 1000}
	in.Conversation.Summary = "旧摘要"
	in.Conversation.RecentMessages = longConversation(20)
	in.VersionPack.Attachments = []string{strings.Repeat("附件说明", 100)}

	summarizer := &fakeSummarizer{summary: "LLM 滚动摘要：用户确认手机号登录。"}
	packet, info := buildContext(t, contextSettings(), summarizer, in)
	if !info.Compacted {
		t.Fatal("large input was not compacted")
	}
	if info.Overflow {
		t.Fatalf("unexpected minimal context: %#v", info)
	}
	if summarizer.calls != 1 {
		t.Fatalf("summarizer calls = %d", summarizer.calls)
	}
	if len(summarizer.inputs[0]) != 17 {
		t.Fatalf("summarizer input messages = %d, want 17", len(summarizer.inputs[0]))
	}
	if info.Summary != "LLM 滚动摘要：用户确认手机号登录。" {
		t.Fatalf("summary = %q", info.Summary)
	}
	if info.AfterTokens > info.Budget {
		t.Fatalf("after tokens = %d > budget %d", info.AfterTokens, info.Budget)
	}

	conversation := sectionByName(t, packet, "conversation").Content
	if !strings.Contains(conversation, "用户确认手机号登录") {
		t.Fatalf("conversation misses LLM summary: %s", conversation)
	}
	if !strings.Contains(conversation, "用户要求支持手机号登录") {
		t.Fatalf("pinned facts lost: %s", conversation)
	}
	if !strings.Contains(conversation, "第19条消息") {
		t.Fatalf("recent window lost: %s", conversation)
	}
	if strings.Contains(conversation, "第00条消息") {
		t.Fatalf("old message was kept verbatim: %s", conversation)
	}
	system := sectionByName(t, packet, "system").Content
	if !strings.Contains(system, "不能替用户确认需求") || !strings.Contains(system, "不删除任何需求") {
		t.Fatalf("task constraints lost: %s", system)
	}
}

func TestContextBuildSummarizerFailureFallsBackDeterministically(t *testing.T) {
	in := sampleInput()
	in.ModelBudget = agentapp.ModelBudget{ContextWindow: 4000, MaxOutput: 1000}
	in.Conversation.RecentMessages = longConversation(20)

	settings := contextSettings()
	firstSummarizer := &fakeSummarizer{err: errors.New("model unavailable")}
	first, firstInfo := buildContext(t, settings, firstSummarizer, in)
	if !firstInfo.Compacted || !firstInfo.UsedFallback {
		t.Fatalf("info = %#v", firstInfo)
	}
	if firstInfo.Summary == "" || !strings.Contains(firstInfo.Summary, "第00条消息") {
		t.Fatalf("fallback summary = %q", firstInfo.Summary)
	}
	if firstInfo.AfterTokens > firstInfo.Budget {
		t.Fatalf("after = %d > budget %d", firstInfo.AfterTokens, firstInfo.Budget)
	}

	secondSummarizer := &fakeSummarizer{err: errors.New("model unavailable")}
	second, secondInfo := buildContext(t, contextSettings(), secondSummarizer, in)
	if second.Hash != first.Hash || secondInfo.Summary != firstInfo.Summary {
		t.Fatalf("fallback is not deterministic")
	}
	if !strings.Contains(sectionByName(t, second, "conversation").Content, "用户要求支持手机号登录") {
		t.Fatal("pinned facts lost in fallback")
	}
}

func TestContextBuildExtremeOverflowStillFitsBudget(t *testing.T) {
	settings := contextSettings()
	in := sampleInput()
	in.ModelBudget = agentapp.ModelBudget{ContextWindow: 5000, MaxOutput: 1000}
	in.Profile.SystemPrompt = "[profile:clarification_host] " + strings.Repeat("角色说明。", 2000)
	in.Task.Constraints = []string{"不能替用户确认需求", "不删除任何需求"}
	in.Conversation.PinnedFacts = []string{"用户要求支持手机号登录"}
	in.Conversation.RecentMessages = longConversation(20)
	in.VersionPack.Attachments = []string{strings.Repeat("附件说明", 100)}

	packet, info := buildContext(t, settings, nil, in)
	if !info.Overflow {
		t.Fatalf("expected minimal context: %#v", info)
	}
	if info.AfterTokens > info.Budget {
		t.Fatalf("after = %d > budget %d", info.AfterTokens, info.Budget)
	}
	if len(packet.Sections) != 2 {
		t.Fatalf("minimal sections = %#v", packet.Sections)
	}
	if packet.Sections[0].Name != "system" || packet.Sections[1].Name != "conversation" {
		t.Fatalf("minimal sections = %#v", packet.Sections)
	}
	system := packet.Sections[0].Content
	if !strings.Contains(system, "不能替用户确认需求") {
		t.Fatalf("task constraints lost in minimal context: %s", system)
	}
	conversation := packet.Sections[1].Content
	if !strings.Contains(conversation, "用户要求支持手机号登录") {
		t.Fatalf("pinned facts lost in minimal context: %s", conversation)
	}
	if !strings.Contains(conversation, "第19条消息") {
		t.Fatalf("recent window lost in minimal context: %s", conversation)
	}
	if len(info.DroppedSections) == 0 {
		t.Fatal("dropped sections not reported")
	}
	if packet.TokenCounts["__total__"] != info.AfterTokens {
		t.Fatalf("token total mismatch: %d vs %d", packet.TokenCounts["__total__"], info.AfterTokens)
	}
}

func TestContextBuildTinyWindowStillOutputsMinimalContext(t *testing.T) {
	in := sampleInput()
	in.ModelBudget = agentapp.ModelBudget{ContextWindow: 100}
	in.Profile.SystemPrompt = strings.Repeat("很长的系统提示。", 500)
	in.Conversation.RecentMessages = longConversation(10)

	packet, info := buildContext(t, contextSettings(), nil, in)
	if info.Budget != agentapp.MinUsableBudget {
		t.Fatalf("budget = %d", info.Budget)
	}
	if info.AfterTokens > info.Budget {
		t.Fatalf("after = %d > budget %d", info.AfterTokens, info.Budget)
	}
	if !info.Overflow {
		t.Fatalf("expected overflow minimal context: %#v", info)
	}
	if len(packet.Sections) == 0 || len(info.DroppedSections) == 0 {
		t.Fatalf("packet/info = %#v / %#v", packet, info)
	}
}

func TestCompactTrimsOlderMessagesDeterministically(t *testing.T) {
	in := sampleInput()
	in.Conversation.RecentMessages = longConversation(20)

	compacted, info := agentapp.Compact(in, 1_000_000)
	if !info.Compacted {
		t.Fatal("Compact did not trim")
	}
	if len(compacted.Conversation.RecentMessages) != agentapp.DefaultKeepRecentMessages {
		t.Fatalf("recent messages = %d", len(compacted.Conversation.RecentMessages))
	}
	if !strings.Contains(compacted.Conversation.Summary, "第00条消息") {
		t.Fatalf("summary = %q", compacted.Conversation.Summary)
	}
	if strings.Contains(compacted.Conversation.Summary, "第19条消息") {
		t.Fatalf("summary contains recent message: %q", compacted.Conversation.Summary)
	}
	if compacted.Conversation.PinnedFacts[0] != "用户要求支持手机号登录" {
		t.Fatalf("pinned facts = %#v", compacted.Conversation.PinnedFacts)
	}

	small := sampleInput()
	compactedSmall, smallInfo := agentapp.Compact(small, 1_000_000)
	if smallInfo.Compacted {
		t.Fatalf("small input compacted: %#v", smallInfo)
	}
	if len(compactedSmall.Conversation.RecentMessages) != len(small.Conversation.RecentMessages) {
		t.Fatalf("small input messages changed: %#v", compactedSmall.Conversation.RecentMessages)
	}
}

func TestCompactDropsOptionalSectionsOverBudget(t *testing.T) {
	in := sampleInput()
	in.Conversation.RecentMessages = longConversation(20)
	compacted, info := agentapp.Compact(in, 300)
	if !info.Compacted {
		t.Fatal("Compact did not compact")
	}
	if len(info.DroppedSections) == 0 {
		t.Fatalf("dropped sections = %#v", info.DroppedSections)
	}
	if len(compacted.Artifacts) != 0 {
		t.Fatalf("artifacts not dropped: %#v", compacted.Artifacts)
	}
	if len(compacted.Conversation.PinnedFacts) != 1 {
		t.Fatalf("pinned facts = %#v", compacted.Conversation.PinnedFacts)
	}
}

func TestContextBuildValidatesPacket(t *testing.T) {
	in := sampleInput()
	packet, _ := buildContext(t, contextSettings(), nil, in)
	packet.RunID = 1
	packet.CreatedAt = time.Now().UTC()
	if err := packet.Validate(); err != nil {
		t.Fatalf("packet invalid: %v", err)
	}
}
