package agentapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// Context budget constants and P1 defaults. Model metadata from the LLM
// gateway takes precedence over the settings fallback (user decision
// 2026-10-31); the design's global context.window_tokens remains the fallback.
const (
	DefaultWindowTokens        = 64000
	DefaultOutputReserveTokens = 8000
	DefaultCompactThreshold    = 0.7
	DefaultKeepRecentMessages  = 12
	ToolReserveTokens          = 2048
	MinUsableBudget            = 256
	MinimalRecentMessages      = 4
	MaxPinnedFacts             = 20
	maxSummaryRunes            = 2000
	maxSummaryMessages         = 40
	maxSummaryMessageRunes     = 120
)

// ModelBudget is the model metadata resolved by the LLM gateway for the
// selected model. Zero values mean "unknown" and fall back to settings.
type ModelBudget struct {
	ContextWindow int
	MaxOutput     int
}

// TaskContext is the runtime-injected task objective. Scope entries are
// identifiers only; the task board payload never carries chat transcripts.
type TaskContext struct {
	Type               string
	Goal               string
	Constraints        []string
	AcceptanceCriteria []string
	Scope              []string
}

// VersionPack is the version/task package rendered into the context.
type VersionPack struct {
	VersionID    int64
	Title        string
	Status       string
	SUTs         []string
	Pages        []string
	APIs         []string
	Requirements []string
	Attachments  []string
}

// Message is one conversation message supplied to the context builder and the
// summarizer.
type Message struct {
	ID      int64
	Role    string
	Content string
}

// ConversationContext is the session memory: rolling summary, pinned facts and
// the recent window kept verbatim.
type ConversationContext struct {
	Summary        string
	PinnedFacts    []string
	RecentMessages []Message
}

// ArtifactRef is one structured artifact reference included in the context.
type ArtifactRef struct {
	Kind string
	ID   string
}

// ContextInput is everything a run needs to build its context packet.
type ContextInput struct {
	ModelBudget  ModelBudget
	Profile      agent.AgentProfile
	Task         TaskContext
	VersionPack  VersionPack
	Conversation ConversationContext
	TeamRoom     TeamRoomContext
	Artifacts    []ArtifactRef
	// Execution is the P3 case-executor task snapshot. It is part of the system
	// section because its steps/assertions/prior results are pinned facts that
	// compaction must never drop.
	Execution *ExecutionTask
	// P4Task is the registered P4 domain task payload (JSON-ready) rendered by
	// context.task_pack. Nil for P1/P2/P3 runs and for P4 types no domain claims.
	P4Task any
}

// Section and ContextPacket are aliases so the builder and the runtime
// repository share one persisted shape.
type Section = agent.ContextSection
type ContextPacket = agent.ContextPacket

// CompactInfo reports what the builder did to fit the context budget. The
// runner persists Summary/Compacted via conversation_summary and publishes
// context.compacted; compaction never fails the run.
type CompactInfo struct {
	Compacted       bool
	Reason          string
	Summary         string
	BeforeTokens    int
	AfterTokens     int
	Budget          int
	DroppedSections []string
	UsedFallback    bool
	Overflow        bool
}

// ContextBuilder renders the layered context packet with a conservative token
// budget and deterministic compaction.
type ContextBuilder struct {
	settings   SettingsReader
	summarizer Summarizer
}

// NewContextBuilder wires the context builder. summarizer may be nil; the
// deterministic fallback summary is then used when compaction is needed.
func NewContextBuilder(settings SettingsReader, summarizer Summarizer) *ContextBuilder {
	return &ContextBuilder{settings: settings, summarizer: summarizer}
}

// Build renders and, when over the compaction threshold, compacts the context.
// It returns a packet whose total token estimate never exceeds the resolved
// budget, plus the compaction metadata the runtime should persist.
func (b *ContextBuilder) Build(ctx context.Context, in ContextInput) (ContextPacket, CompactInfo, error) {
	budget, threshold, keepRecent := b.resolveBudget(ctx, in)
	info := CompactInfo{Budget: budget}

	// Keep the recent window bounded even when the whole context is below the
	// compaction threshold. Older messages are folded into the rolling summary
	// instead of being silently dropped by the adapter.
	if keepRecent >= 0 && len(in.Conversation.RecentMessages) > keepRecent {
		older := in.Conversation.RecentMessages[:len(in.Conversation.RecentMessages)-keepRecent]
		in.Conversation.RecentMessages = in.Conversation.RecentMessages[len(in.Conversation.RecentMessages)-keepRecent:]
		summary, usedFallback := b.summarize(ctx, older)
		in.Conversation.Summary = mergeSummaries(in.Conversation.Summary, summary)
		info.Compacted = true
		info.Reason = "window"
		info.Summary = summary
		info.UsedFallback = usedFallback
	}

	sections := renderSections(in)
	total := totalTokens(sections)
	info.BeforeTokens = total
	if float64(total) <= threshold*float64(budget) {
		info.AfterTokens = total
		return packetFor(in, sections), info, nil
	}

	info.Compacted = true
	info.Reason = "threshold"

	compacted, compactInfo := compactInput(in, budget, keepRecent)
	in = compacted
	mergeCompactInfo(&info, compactInfo)

	sections = renderSections(in)
	if total = totalTokens(sections); total > budget {
		info.Overflow = true
		info.Reason = "overflow"
		sections = fitMinimalSections(in, budget)
	}
	info.AfterTokens = totalTokens(sections)
	return packetFor(in, sections), info, nil
}

// Compact applies deterministic (no-LLM) compaction to an input: older messages
// fold into the rolling summary, pinned facts are bounded and optional sections
// are dropped while the rendered context exceeds budget.
func Compact(in ContextInput, budget int) (ContextInput, CompactInfo) {
	if budget <= 0 {
		budget = MinUsableBudget
	}
	compacted, info := compactInput(in, budget, DefaultKeepRecentMessages)
	info.BeforeTokens = totalTokens(renderSections(in))
	info.AfterTokens = totalTokens(renderSections(compacted))
	return compacted, info
}

func (b *ContextBuilder) summarize(ctx context.Context, messages []Message) (summary string, usedFallback bool) {
	if b.summarizer != nil {
		if generated, err := b.summarizer.Summarize(ctx, messages); err == nil {
			if generated = strings.TrimSpace(generated); generated != "" {
				return generated, false
			}
		}
	}
	return deterministicSummary(messages), true
}

func (b *ContextBuilder) resolveBudget(ctx context.Context, in ContextInput) (budget int, threshold float64, keepRecent int) {
	window := in.ModelBudget.ContextWindow
	if window <= 0 {
		window = int(b.settingInt(ctx, "context.window_tokens", DefaultWindowTokens))
	}
	reserve := int(b.settingInt(ctx, "context.output_reserve_tokens", DefaultOutputReserveTokens))
	if in.ModelBudget.MaxOutput > 0 && in.ModelBudget.MaxOutput < reserve {
		reserve = in.ModelBudget.MaxOutput
	}
	budget = window - reserve - ToolReserveTokens
	if budget < MinUsableBudget {
		budget = MinUsableBudget
	}
	threshold = b.settingFloat(ctx, "context.compact_threshold", DefaultCompactThreshold)
	if threshold <= 0 || threshold > 1 {
		threshold = DefaultCompactThreshold
	}
	keepRecent = int(b.settingInt(ctx, "context.keep_recent_messages", DefaultKeepRecentMessages))
	if keepRecent < 0 {
		keepRecent = DefaultKeepRecentMessages
	}
	return budget, threshold, keepRecent
}

func (b *ContextBuilder) settingInt(ctx context.Context, key string, fallback int64) int64 {
	if b.settings == nil {
		return fallback
	}
	value, err := b.settings.Int(ctx, key)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func (b *ContextBuilder) settingFloat(ctx context.Context, key string, fallback float64) float64 {
	if b.settings == nil {
		return fallback
	}
	value, err := b.settings.Float(ctx, key)
	if err != nil {
		return fallback
	}
	return value
}

// compactInput is the deterministic structural compaction shared by Build and
// Compact. It never truncates section text (that requires rendering); callers
// enforce the final budget with renderSections/fitMinimalSections.
func compactInput(in ContextInput, budget, keepRecent int) (ContextInput, CompactInfo) {
	out := in
	info := CompactInfo{}

	if len(out.Conversation.RecentMessages) > keepRecent {
		older := out.Conversation.RecentMessages[:len(out.Conversation.RecentMessages)-keepRecent]
		out.Conversation.RecentMessages = out.Conversation.RecentMessages[len(out.Conversation.RecentMessages)-keepRecent:]
		summary := deterministicSummary(older)
		out.Conversation.Summary = mergeSummaries(out.Conversation.Summary, summary)
		if info.Summary == "" {
			info.Summary = summary
		}
		info.Compacted = true
	}
	if len(out.Conversation.PinnedFacts) > MaxPinnedFacts {
		out.Conversation.PinnedFacts = out.Conversation.PinnedFacts[len(out.Conversation.PinnedFacts)-MaxPinnedFacts:]
	}

	for _, name := range []string{"artifacts", "team_room", "version_pack"} {
		if totalTokens(renderSections(out)) <= budget {
			break
		}
		switch name {
		case "artifacts":
			out.Artifacts = nil
		case "team_room":
			out.TeamRoom = TeamRoomContext{}
		case "version_pack":
			out.VersionPack = VersionPack{}
		}
		info.DroppedSections = append(info.DroppedSections, name)
		info.Compacted = true
	}
	return out, info
}

func mergeCompactInfo(target *CompactInfo, addition CompactInfo) {
	target.Compacted = target.Compacted || addition.Compacted
	target.UsedFallback = target.UsedFallback || addition.UsedFallback
	if addition.Summary != "" && target.Summary == "" {
		target.Summary = addition.Summary
	}
	for _, name := range addition.DroppedSections {
		duplicate := false
		for _, existing := range target.DroppedSections {
			if existing == name {
				duplicate = true
				break
			}
		}
		if !duplicate {
			target.DroppedSections = append(target.DroppedSections, name)
		}
	}
}

// renderSections builds the five ordered context sections (spec §5.3).
func renderSections(in ContextInput) []Section {
	return []Section{
		{Name: "system", Content: systemContent(in)},
		{Name: "version_pack", Content: versionPackContent(in.VersionPack), Optional: true},
		{Name: "conversation", Content: conversationContent(in.Conversation)},
		{Name: "team_room", Content: teamRoomContent(in.TeamRoom), Optional: true},
		{Name: "artifacts", Content: artifactsContent(in.Artifacts), Optional: true},
	}
}

func systemContent(in ContextInput) string {
	parts := make([]string, 0, 3)
	if prompt := strings.TrimSpace(in.Profile.SystemPrompt); prompt != "" {
		parts = append(parts, prompt)
	}
	parts = append(parts, taskBlock(in.Task))
	if in.Execution != nil {
		parts = append(parts, executionBlock(*in.Execution))
	}
	return strings.Join(parts, "\n\n")
}

// minimalSystemContent keeps the task block (goal/constraints first) ahead of
// the profile prompt so extreme truncation cannot lose task constraints.
func minimalSystemContent(in ContextInput) string {
	parts := make([]string, 0, 3)
	parts = append(parts, taskBlock(in.Task))
	if in.Execution != nil {
		parts = append(parts, executionBlock(*in.Execution))
	}
	if prompt := strings.TrimSpace(in.Profile.SystemPrompt); prompt != "" {
		parts = append(parts, prompt)
	}
	return strings.Join(parts, "\n\n")
}

func taskBlock(task TaskContext) string {
	var builder strings.Builder
	builder.WriteString("# 任务\n")
	if task.Type != "" {
		fmt.Fprintf(&builder, "类型: %s\n", task.Type)
	}
	if task.Goal != "" {
		fmt.Fprintf(&builder, "目标: %s\n", task.Goal)
	}
	if len(task.Constraints) > 0 {
		builder.WriteString("约束:\n")
		for _, constraint := range task.Constraints {
			fmt.Fprintf(&builder, "- %s\n", constraint)
		}
	}
	if len(task.AcceptanceCriteria) > 0 {
		builder.WriteString("验收:\n")
		for _, criterion := range task.AcceptanceCriteria {
			fmt.Fprintf(&builder, "- %s\n", criterion)
		}
	}
	if len(task.Scope) > 0 {
		builder.WriteString("作用域:\n")
		for _, scope := range task.Scope {
			fmt.Fprintf(&builder, "- %s\n", scope)
		}
	}
	return strings.TrimRight(builder.String(), "\n")
}

// executionBlock renders the P3 case-executor task: the pinned case, its step
// and assertion truth, the version-SUT bindings and the already-recorded results
// a resumed run continues from.
func executionBlock(task ExecutionTask) string {
	var builder strings.Builder
	builder.WriteString("# 执行任务\n")
	fmt.Fprintf(&builder, "用例: %s (case_version_id=%s, case_run_id=%s)\n",
		task.CaseTitle, idText(task.CaseVersionID), idText(task.CaseRunID))
	if task.Description != "" {
		fmt.Fprintf(&builder, "描述: %s\n", task.Description)
	}
	if task.Preconditions != "" {
		fmt.Fprintf(&builder, "前置条件: %s\n", task.Preconditions)
	}
	if task.Priority != "" {
		fmt.Fprintf(&builder, "优先级: %s\n", task.Priority)
	}
	if len(task.VersionSUTs) > 0 {
		builder.WriteString("SUT 绑定:\n")
		for _, sut := range task.VersionSUTs {
			fmt.Fprintf(&builder, "- version_sut=%s %s (%s)", idText(sut.VersionSUTID), sut.SUTName, sut.EnvironmentName)
			if sut.BaseURL != "" {
				fmt.Fprintf(&builder, " base_url=%s", sut.BaseURL)
			}
			if len(sut.PageURLs) > 0 {
				fmt.Fprintf(&builder, " pages=%s", strings.Join(sut.PageURLs, ", "))
			}
			if len(sut.APIEndpoints) > 0 {
				fmt.Fprintf(&builder, " apis=%s", strings.Join(sut.APIEndpoints, ", "))
			}
			if len(sut.AccountRoles) > 0 {
				fmt.Fprintf(&builder, " roles=%s", strings.Join(sut.AccountRoles, ", "))
			}
			builder.WriteString("\n")
		}
	}
	if len(task.Steps) > 0 {
		builder.WriteString("步骤:\n")
		for _, step := range task.Steps {
			fmt.Fprintf(&builder, "- step %d [version_sut=%s", step.Seq, idText(step.TargetVersionSUTID))
			if step.AccountRole != "" {
				fmt.Fprintf(&builder, ", role=%s", step.AccountRole)
			}
			builder.WriteString("]: ")
			builder.WriteString(step.Action)
			if step.Expected != "" {
				fmt.Fprintf(&builder, "；期望: %s", step.Expected)
			}
			builder.WriteString("\n")
		}
	}
	if len(task.Assertions) > 0 {
		builder.WriteString("断言:\n")
		for _, assertion := range task.Assertions {
			if assertion.Kind == "visual" {
				// P5: the model can only run a visual assertion when the task
				// context names the visual_assertion ID (spec §7.3). The
				// finalizer makes a missing run inconclusive, never passed.
				fmt.Fprintf(&builder, "- step %d assertion %d [kind=visual, version_sut=%s",
					assertion.StepSeq, assertion.AssertionSeq, idText(assertion.TargetVersionSUTID))
				if assertion.VisualAssertionID > 0 {
					fmt.Fprintf(&builder, ", visual_assertion_id=%s", idText(assertion.VisualAssertionID))
				}
				if assertion.CaseAssertionID > 0 {
					fmt.Fprintf(&builder, ", case_assertion_id=%s", idText(assertion.CaseAssertionID))
				}
				fmt.Fprintf(&builder, "]: %s\n", assertion.Expected)
				continue
			}
			fmt.Fprintf(&builder, "- step %d assertion %d [kind=%s, version_sut=%s]: %s\n",
				assertion.StepSeq, assertion.AssertionSeq, assertion.Kind,
				idText(assertion.TargetVersionSUTID), assertion.Expected)
		}
	}
	if len(task.PriorSteps) > 0 {
		builder.WriteString("已完成步骤（续跑，勿重复）：\n")
		for _, prior := range task.PriorSteps {
			fmt.Fprintf(&builder, "- step %d %s", prior.Seq, prior.Status)
			if prior.Observed != "" {
				fmt.Fprintf(&builder, ": %s", prior.Observed)
			}
			if prior.Error != "" {
				fmt.Fprintf(&builder, " error=%s", prior.Error)
			}
			builder.WriteString("\n")
		}
	}
	if len(task.PriorAssertions) > 0 {
		builder.WriteString("已完成断言（续跑，勿重复）：\n")
		for _, prior := range task.PriorAssertions {
			fmt.Fprintf(&builder, "- step %d assertion %d %s kind=%s expected=%q actual=%q",
				prior.StepSeq, prior.AssertionSeq, prior.Verdict, prior.Kind, prior.Expected, prior.Actual)
			if prior.Reason != "" {
				fmt.Fprintf(&builder, " reason=%s", prior.Reason)
			}
			builder.WriteString("\n")
		}
	}
	if task.HumanResponse != "" {
		fmt.Fprintf(&builder, "人工接管响应（一次性，不得写入证据）：%s\n", task.HumanResponse)
	}
	return strings.TrimRight(builder.String(), "\n")
}

func versionPackContent(pack VersionPack) string {
	if pack.VersionID == 0 && pack.Title == "" && pack.Status == "" &&
		len(pack.SUTs) == 0 && len(pack.Pages) == 0 && len(pack.APIs) == 0 &&
		len(pack.Requirements) == 0 && len(pack.Attachments) == 0 {
		return ""
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "版本: %s (%s)", pack.Title, pack.Status)
	if len(pack.SUTs) > 0 {
		fmt.Fprintf(&builder, "\nSUT: %s", strings.Join(pack.SUTs, ", "))
	}
	if len(pack.Pages) > 0 {
		fmt.Fprintf(&builder, "\n页面(%d): %s", len(pack.Pages), strings.Join(pack.Pages, ", "))
	}
	if len(pack.APIs) > 0 {
		fmt.Fprintf(&builder, "\nAPI(%d): %s", len(pack.APIs), strings.Join(pack.APIs, ", "))
	}
	if len(pack.Requirements) > 0 {
		builder.WriteString("\n需求:")
		for _, requirement := range pack.Requirements {
			fmt.Fprintf(&builder, "\n- %s", requirement)
		}
	}
	if len(pack.Attachments) > 0 {
		builder.WriteString("\n附件:")
		for _, attachment := range pack.Attachments {
			fmt.Fprintf(&builder, "\n- %s", attachment)
		}
	}
	return builder.String()
}

func conversationContent(conversation ConversationContext) string {
	return conversationText(conversation.Summary, conversation.PinnedFacts, conversation.RecentMessages)
}

func conversationText(summary string, pinnedFacts []string, messages []Message) string {
	var builder strings.Builder
	if summary = strings.TrimSpace(summary); summary != "" {
		fmt.Fprintf(&builder, "摘要: %s", summary)
	}
	if len(pinnedFacts) > 0 {
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString("钉住事实:")
		for _, fact := range pinnedFacts {
			fmt.Fprintf(&builder, "\n- %s", fact)
		}
	}
	if len(messages) > 0 {
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString("最近消息:")
		for _, message := range messages {
			fmt.Fprintf(&builder, "\n[%s] %s", message.Role, message.Content)
		}
	}
	return builder.String()
}

func teamRoomContent(context TeamRoomContext) string {
	if context.Digest == nil && len(context.Posts) == 0 {
		return ""
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "团队: %s", context.Team)
	if context.Digest != nil {
		fmt.Fprintf(&builder, "\n摘要: %s", context.Digest.Summary)
	}
	if len(context.Posts) > 0 {
		builder.WriteString("\n帖子:")
		for _, post := range context.Posts {
			fmt.Fprintf(&builder, "\n- [%s] %s", post.Kind, post.Content)
		}
	}
	return builder.String()
}

func artifactsContent(artifacts []ArtifactRef) string {
	if len(artifacts) == 0 {
		return ""
	}
	lines := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		lines = append(lines, fmt.Sprintf("- %s:%s", artifact.Kind, artifact.ID))
	}
	return strings.Join(lines, "\n")
}

// fitMinimalSections is the overflow-safe last resort: system + task +
// conversation memory (pinned facts and the most recent messages), truncated
// deterministically so the estimate never exceeds budget.
func fitMinimalSections(in ContextInput, budget int) []Section {
	system := minimalSystemContent(in)
	pinned := in.Conversation.PinnedFacts
	recent := lastMessages(in.Conversation.RecentMessages, MinimalRecentMessages)

	// Reserve at most half the budget for conversation memory, dropping the
	// oldest recent messages first so the newest window survives longest.
	maxConversationTokens := budget / 2
	for len(recent) > 0 {
		if EstimateTokens(conversationText("", pinned, recent)) <= maxConversationTokens {
			break
		}
		recent = recent[1:]
	}
	conversation := conversationText("", pinned, recent)
	if EstimateTokens(conversation) > maxConversationTokens {
		conversation = truncateToTokens(conversation, maxConversationTokens)
	}

	remaining := budget - EstimateTokens(conversation)
	if EstimateTokens(system) > remaining {
		system = truncateToTokens(system, remaining)
	}
	return minimalSections(system, conversation)
}

func minimalSections(system, conversation string) []Section {
	return []Section{
		{Name: "system", Content: system},
		{Name: "conversation", Content: conversation},
	}
}

func lastMessages(messages []Message, limit int) []Message {
	if limit <= 0 || len(messages) <= limit {
		return messages
	}
	return messages[len(messages)-limit:]
}

func deterministicSummary(messages []Message) string {
	if len(messages) == 0 {
		return ""
	}
	start := 0
	if len(messages) > maxSummaryMessages {
		start = len(messages) - maxSummaryMessages
	}
	var builder strings.Builder
	builder.WriteString("历史消息摘要:")
	for _, message := range messages[start:] {
		fmt.Fprintf(&builder, "\n- [%s] %s", message.Role,
			truncateRunes(normalizePostContent(message.Content), maxSummaryMessageRunes))
	}
	return truncateRunes(builder.String(), maxSummaryRunes)
}

func mergeSummaries(existing, addition string) string {
	existing = strings.TrimSpace(existing)
	addition = strings.TrimSpace(addition)
	switch {
	case addition == "":
		return existing
	case existing == "":
		return addition
	default:
		return truncateRunes(existing+"\n"+addition, maxSummaryRunes)
	}
}

func totalTokens(sections []Section) int {
	total := 0
	for _, section := range sections {
		total += EstimateTokens(section.Content)
	}
	return total
}

func packetFor(in ContextInput, sections []Section) ContextPacket {
	counts := make(map[string]int, len(sections)+1)
	total := 0
	for _, section := range sections {
		count := EstimateTokens(section.Content)
		counts[section.Name] = count
		total += count
	}
	counts["__total__"] = total
	return ContextPacket{
		VersionID:   in.VersionPack.VersionID,
		Sections:    sections,
		TokenCounts: counts,
		Hash:        hashSections(in.VersionPack.VersionID, sections),
	}
}

func hashSections(versionID int64, sections []Section) string {
	payload := struct {
		VersionID int64     `json:"version_id"`
		Sections  []Section `json:"sections"`
	}{VersionID: versionID, Sections: sections}
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte(fmt.Sprintf("marshal-error:%v", err))
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
