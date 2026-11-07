package agentapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

const (
	summaryPrompt = `你是 EzTest 的会话摘要器。把给定的历史对话压缩成简短中文摘要：
- 保留用户目标、已确认事实、未决问题与任务约束；
- 不编造未出现的信息，不使用 Markdown 标题；
- 最多 800 字。`
	// summaryCompactionSystemPrompt is the R-A5-7 system message used when the
	// caller has no current conversation prefix to reuse (R-A5-8). It restates
	// the nine-section contract so the fallback never conflicts with it.
	summaryCompactionSystemPrompt = `你是 EzTest 的会话压缩器。把给定的历史对话压缩成结构化中文摘要：
- 必须依次输出规定的九个二级标题，空段写 (none)，不得删段；
- 保留精确 ID、路径、命令、错误串、数值与函数签名，不编造未出现的信息；
- “待办作业 / 当前进展 / 下一步”必须引用 DB 快照中的 plan item id/status 与 fact key；
- 只输出摘要文本，不调用任何工具。`
	maxSummaryInputMessages = 40
	// maxSummarizerOutputTokens bounds the summarizer completion.
	maxSummarizerOutputTokens = 1024
)

// LLMSummarizer adapts the P1 chat gateway to the ContextBuilder Summarizer
// port. Summarization is best effort: on any failure the builder falls back to
// its deterministic summary.
type LLMSummarizer struct {
	chat llmapp.ChatGateway
}

// The context-package compaction port is narrow (no agentapp types), and
// agentapp.Message is an alias of agentcontext.Message, so the LLM summarizer
// satisfies it structurally. Boundary compaction can therefore inject this
// type without agentcontext importing agentapp (which would cycle).
var _ agentcontext.Summarizer = (*LLMSummarizer)(nil)

// Summarize compresses the oldest messages into a rolling summary.
func (s *LLMSummarizer) Summarize(ctx context.Context, messages []Message) (string, error) {
	if s == nil || s.chat == nil {
		return "", errors.New("llm summarizer is not configured")
	}
	if len(messages) == 0 {
		return "", nil
	}
	start := 0
	if len(messages) > maxSummaryInputMessages {
		start = len(messages) - maxSummaryInputMessages
	}
	var transcript strings.Builder
	for _, message := range messages[start:] {
		content := truncateRunes(strings.TrimSpace(message.Content), 300)
		if content == "" {
			continue
		}
		transcript.WriteString("[")
		transcript.WriteString(message.Role)
		transcript.WriteString("] ")
		transcript.WriteString(content)
		transcript.WriteString("\n")
	}
	if strings.TrimSpace(transcript.String()) == "" {
		return "", nil
	}

	response, err := s.chat.Chat(ctx, llmapp.ChatRequest{
		Messages: []llmapp.ChatMessage{
			{Role: "system", Content: summaryPrompt},
			{Role: "user", Content: transcript.String()},
		},
		MaxOutputTokens: maxSummarizerOutputTokens,
	})
	if err != nil {
		return "", err
	}
	summary := truncateRunes(strings.TrimSpace(response.Content), maxSummaryRunes)
	if summary == "" {
		return "", errors.New("llm summarizer returned an empty summary")
	}
	return summary, nil
}

// The R-A5-7 summary contract. The nine headings are the stable machine identity
// of the sections and are rendered verbatim in this order; an empty section
// renders the literal (none) placeholder and is never dropped.
const (
	// SummarySectionGoal is the user goal/intent section.
	SummarySectionGoal = "用户目标与意图"
	// SummarySectionConcepts is the concepts/constraints section.
	SummarySectionConcepts = "关键概念与约束"
	// SummarySectionRequirements is the requirements/cases/artifacts section.
	SummarySectionRequirements = "需求 / 用例 / 产物"
	// SummarySectionFiles is the files/code section.
	SummarySectionFiles = "文件与代码"
	// SummarySectionErrors is the errors/fixes section.
	SummarySectionErrors = "错误与修复"
	// SummarySectionTodo is the pending-work section (plan ids/statuses).
	SummarySectionTodo = "待办作业"
	// SummarySectionProgress is the current-progress section (plan/facts).
	SummarySectionProgress = "当前进展"
	// SummarySectionNext is the next-step section (plan id/status).
	SummarySectionNext = "下一步"
	// SummarySectionContext is the key-context section (decisions, open
	// questions, exact ids/numbers/signatures).
	SummarySectionContext = "关键上下文"

	// summaryNoneText is the R-A5-7 empty-section placeholder.
	summaryNoneText = "(none)"
	// SummaryImageTokens is the R-A5-5 token charge of one skipped image. It is
	// wired into EstimateSummaryRequestTokens (and the result/commit bookkeeping)
	// so the Task 36 budget accounting charges every skipped image exactly once.
	SummaryImageTokens = 1500
	// maxSummaryContextItems bounds the extracted exact identifiers of the
	// key-context section so one noisy transcript cannot flood the summary.
	maxSummaryContextItems = 20
	// maxSummaryPlanItems bounds one rendered plan/fact group.
	maxSummaryPlanItems = 10
	// maxSummaryFactItems bounds one rendered fact group.
	maxSummaryFactItems = 10
	// maxSummaryGoalItems bounds the older-user request bullets of the goal
	// section derived from the folded transcript.
	maxSummaryGoalItems = 5
	// maxSummaryConstraintItems bounds the derived constraint bullets.
	maxSummaryConstraintItems = 10
	// maxSummaryRequirementItems bounds the derived requirement/case/artifact
	// bullets.
	maxSummaryRequirementItems = 10
	// maxSummaryBareNumbers bounds the bare-number extraction so a numeric
	// transcript cannot flood the key-context section.
	maxSummaryBareNumbers = 12
)

// summarySectionHeadings is the ordered R-A5-7 section list.
var summarySectionHeadings = []string{
	SummarySectionGoal,
	SummarySectionConcepts,
	SummarySectionRequirements,
	SummarySectionFiles,
	SummarySectionErrors,
	SummarySectionTodo,
	SummarySectionProgress,
	SummarySectionNext,
	SummarySectionContext,
}

// SummarySectionHeadings returns a copy of the nine R-A5-7 headings in render
// order.
func SummarySectionHeadings() []string {
	return append([]string(nil), summarySectionHeadings...)
}

// summaryHeadingIndexes maps a normalized heading to its canonical position.
var summaryHeadingIndexes = func() map[string]int {
	indexes := make(map[string]int, len(summarySectionHeadings))
	for index, heading := range summarySectionHeadings {
		indexes[summaryHeadingKey(heading)] = index
	}
	return indexes
}()

// summaryHeadingKey normalizes a markdown heading for tolerant parsing: it
// accepts "## heading", "## **heading**" and a trailing colon, but never treats
// a different heading as one of the nine.
func summaryHeadingKey(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "##")
	text = strings.TrimSpace(text)
	text = strings.Trim(text, "*")
	text = strings.TrimSpace(text)
	text = strings.TrimSuffix(text, ":")
	text = strings.TrimSuffix(text, "：")
	text = strings.ReplaceAll(text, " ", "")
	return text
}

// SummarySection is one rendered R-A5-7 section. Body is the generated content;
// an empty Body renders the (none) placeholder.
type SummarySection struct {
	Heading string
	Body    string
}

// SummaryReplayMessage is one message replayed verbatim in the R-A5-8 summary
// request. It preserves the provider-relevant fields of the live transcript
// (tool calls, tool-call identity, reasoning content and images) that the
// narrower context.Message cannot carry, so the summary call shares the longest
// possible prefix with the conversation's next provider request. The summary
// text itself is still derived from Content only.
type SummaryReplayMessage struct {
	ID               int64
	Role             string
	Content          string
	Images           []llmapp.ChatImage
	ToolCalls        []llmapp.ToolCall
	ToolCallID       string
	ReasoningContent string
}

// SummaryInput is the complete material of one compaction summary (R-A5-4,
// R-A5-7, R-A5-8, R-A5-13). It carries the pinned task context and plan/fact
// snapshots that are never folded, the transcript range to fold, the stable
// prefix/tools to reuse for the provider prefix cache, the prior summary to
// merge and the policy that bounds the output.
type SummaryInput struct {
	// ConversationID and VersionID identify the conversation the summary
	// revision is persisted for (R-A5-12).
	ConversationID int64
	VersionID      int64
	// CompactionID is the opaque R-A5-10 compaction transaction token stored
	// next to the summary.
	CompactionID string
	// Trigger is the compaction trigger that produced this summary.
	Trigger agentcontext.CompactionTrigger
	// PreviousCoveredMessageID is the watermark of the last persisted summary;
	// only messages after it are folded (R-A5-12).
	PreviousCoveredMessageID int64
	// CoveredMessageID is the last message this summary covers. Persistence
	// keeps it monotonic.
	CoveredMessageID int64

	// StablePrefix and Tools are the current conversation's system prompt and
	// tool definitions. BuildSummaryRequest reuses them byte for byte so the
	// summary call hits the provider prefix cache (R-A5-8).
	StablePrefix string
	Tools        []llmapp.ToolDefinition
	// LiveMessages are the current conversation's messages between the stable
	// system prefix and the folded range: the rendered version briefing, the
	// rendered version facts and the prior summary. BuildSummaryRequest replays
	// them verbatim before the folded range so the request is a prefix of the
	// live provider transcript (R-A5-8). The summary text never derives from
	// them.
	LiveMessages []SummaryReplayMessage
	// Messages is the summarised range, oldest first. Callers may pass the
	// unfiltered range: BuildSummaryResult and BuildSummaryRequest apply
	// SelectSummaryMessages exactly once with PreviousCoveredMessageID, so an
	// already-covered message is never folded twice (R-A5-12). Image content
	// never reaches the summary text: SummaryMessages replaces it with
	// attachment markers.
	Messages []SummaryReplayMessage
	// ImageCount is the number of images skipped by the folded range (R-A5-5).
	// Each image is charged SummaryImageTokens in the request estimate.
	ImageCount int

	// Task is the pinned per-work-item task context (R-A5-4).
	Task           TaskContext
	WorkItemID     int64
	RequirementIDs []int64
	CaseIDs        []int64
	// Plan and Facts are the DB truth snapshots (R-A5-13): the todo, progress
	// and next sections only reference these ids/keys, never invented ones.
	Plan  []agentcontext.PlanItemSnapshot
	Facts []agentcontext.FactSnapshot
	// PinnedFacts are the pinned session facts (≤20 rendered).
	PinnedFacts []string
	// PriorSummary is merged: still-true facts are kept, stale plan references
	// are dropped and no line is duplicated verbatim.
	PriorSummary string
	// Draft is an optional model-generated summary. BuildSummary normalises it
	// into the nine sections; the plan/progress/next sections stay derived from
	// the DB snapshots.
	Draft string

	// Policy is the resolved R-A5-1 compaction policy.
	Policy agentcontext.CompactionPolicy
	// SummaryMax overrides Policy.SummaryMax when positive.
	SummaryMax int
	// MaxOutputTokens overrides the derived completion bound when positive.
	MaxOutputTokens int
}

// summaryMax resolves the R-A5-7 output cap: the explicit override wins, then
// the compaction policy. An unset policy falls back to the spec's 8192-token
// summary cap, so a summary is never unbounded.
func (in SummaryInput) summaryMax() int {
	if in.SummaryMax > 0 {
		return in.SummaryMax
	}
	if in.Policy.SummaryMax > 0 {
		return in.Policy.SummaryMax
	}
	return agentcontext.MaxCompactionSummaryTokens
}

// SummaryResult is one generated summary plus the bookkeeping the runner and
// the persistence hook need.
type SummaryResult struct {
	Text             string
	Truncated        bool
	Tokens           int
	InputTokens      int
	CachedTokens     int
	CacheHit         bool
	UsedFallback     bool
	FallbackReason   string
	CoveredMessageID int64
	CompactionID     string
	// PinnedLeakFiltered reports that the model draft reproduced explicitly
	// pinned material (session pinned facts or the pinned task context) and the
	// H5 filter removed those lines from the persisted summary.
	PinnedLeakFiltered bool
}

// EstimateSummaryTokens is the R-A5-14 conservative estimator for summary
// material: CJK characters cost 1 token, other bytes a quarter and a fixed
// 16-token overhead. It deliberately over-estimates (rounds the non-CJK part
// up) so a truncated summary still fits the budget the provider sees.
func EstimateSummaryTokens(text string) int {
	return summaryTextTokens(text) + tokenOverhead
}

// summaryTextTokens is the overflow-free part of EstimateSummaryTokens; the
// aggregate request estimate adds the per-prompt overhead once instead of once
// per message.
func summaryTextTokens(text string) int {
	cjk, other := 0, 0
	for _, r := range text {
		if summaryCJK(r) {
			cjk++
			continue
		}
		other++
	}
	return cjk + (other+3)/4
}

// EstimateSummaryRequestTokens is the R-A5-14/R-A5-5 conservative estimate of
// one summary request input: the reused stable prefix, the live prefix
// messages, the watermark-filtered folded range and the compaction instruction,
// plus SummaryImageTokens for every skipped image. Task 36 charges this against
// the compaction trigger budget.
func EstimateSummaryRequestTokens(in SummaryInput) int {
	in = selectSummaryInput(in)
	return estimateSummaryRequestTokens(in)
}

// estimateSummaryRequestTokens is the already-selected form used by the builder
// so the watermark is applied exactly once per summary transaction.
func estimateSummaryRequestTokens(in SummaryInput) int {
	total := summaryTextTokens(in.StablePrefix)
	for _, message := range in.LiveMessages {
		total += summaryReplayMessageTokens(message)
	}
	for _, message := range in.Messages {
		total += summaryReplayMessageTokens(message)
	}
	total += summaryTextTokens(summaryCompactionInstruction(in))
	if in.ImageCount > 0 {
		total += in.ImageCount * SummaryImageTokens
	}
	return total + tokenOverhead
}

// summaryReplayMessageTokens estimates one replayed message: its text plus the
// reasoning/tool-call payload it carries.
func summaryReplayMessageTokens(message SummaryReplayMessage) int {
	total := summaryTextTokens(message.Content)
	total += summaryTextTokens(message.ReasoningContent)
	for _, call := range message.ToolCalls {
		total += summaryTextTokens(call.Name + call.Arguments)
	}
	return total
}

// summaryCJK reports whether a rune is rendered at roughly one token per rune
// (Han characters and CJK/full-width punctuation).
func summaryCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF:
		return true
	case r >= 0x3400 && r <= 0x4DBF:
		return true
	case r >= 0x3000 && r <= 0x303F:
		return true
	case r >= 0xFF00 && r <= 0xFFEF:
		return true
	default:
		return false
	}
}

// selectSummaryInput applies the R-A5-12 watermark exactly once per summary
// transaction. Messages may therefore be passed unfiltered: the internal
// builders assume an already-selected input and never filter again, so the
// deterministic summary and the prefix request fold exactly the same slice.
func selectSummaryInput(in SummaryInput) SummaryInput {
	in.Messages = SelectSummaryMessages(in.Messages, in.PreviousCoveredMessageID)
	return in
}

// BuildSummary renders the R-A5-7 nine-section summary text. It is deterministic
// and never invents material: the plan/progress/next sections are rebuilt from
// the DB snapshots, exact ids/paths/commands/errors/numbers/signatures are
// preserved, a prior summary is merged without verbatim duplication and every
// empty section renders (none). Pinned R-A5-4 material (task context, pinned
// facts, latest user message) is never rendered.
func BuildSummary(in SummaryInput) string {
	return BuildSummaryResult(in).Text
}

// BuildSummaryResult is BuildSummary plus the truncation/token bookkeeping used
// by the summary_truncated flag and event (R-A5-7). The watermark is applied
// here, and Summarize reuses the selected input for both the request and the
// result so one transaction folds exactly one filtered slice (R-A5-12).
func BuildSummaryResult(in SummaryInput) SummaryResult {
	return buildSummaryResult(selectSummaryInput(in))
}

// buildSummaryResult is the already-selected builder. It never filters again.
func buildSummaryResult(in SummaryInput) SummaryResult {
	sections := buildSummarySections(in)
	text, truncated := renderSummaryWithinBudget(sections, in.summaryMax())
	return SummaryResult{
		Text:             text,
		Truncated:        truncated,
		Tokens:           EstimateSummaryTokens(text),
		InputTokens:      estimateSummaryRequestTokens(in),
		CoveredMessageID: in.CoveredMessageID,
		CompactionID:     in.CompactionID,
	}
}

// NormalizeSummary canonicalizes a model-generated summary: content before the
// first recognized heading is dropped, missing headings are restored and empty
// sections render (none). It returns "" when the text has no recognized heading.
func NormalizeSummary(text string) string {
	bodies, ok := parseSummarySections(text)
	if !ok {
		return ""
	}
	sections := make([]SummarySection, len(summarySectionHeadings))
	for index, heading := range summarySectionHeadings {
		sections[index] = SummarySection{Heading: heading, Body: strings.TrimSpace(bodies[index])}
	}
	return renderSummarySections(sections)
}

// buildSummarySections derives the canonical sections and merges the optional
// model draft and prior summary. The DB-truth sections (todo/progress/next) keep
// their derived content: a model draft or prior summary must not be able to
// invent a plan id or status (R-A5-13).
func buildSummarySections(in SummaryInput) []SummarySection {
	sections := deriveSummarySections(in)
	if draft, ok := parseSummarySections(in.Draft); ok {
		for index := range sections {
			if summaryDBTruthSection(index) {
				continue
			}
			body := strings.TrimSpace(draft[index])
			if body == "" || body == summaryNoneText {
				continue
			}
			sections[index].Body = mergeSummaryBodies(sections[index].Body, body)
		}
	}
	if prior, ok := parseSummarySections(in.PriorSummary); ok {
		for index := range sections {
			if summaryDBTruthSection(index) {
				// A prior plan/progress/next line is stale by definition: the
				// current DB snapshot is the only truth for those sections.
				continue
			}
			body := filterStalePriorSummaryLines(strings.TrimSpace(prior[index]), in)
			if body == "" {
				continue
			}
			sections[index].Body = mergeSummaryBodies(sections[index].Body, body)
		}
	}
	return sections
}

// deriveSummarySections renders the deterministic sections from the input
// material. No section invents content. R-A5-4: the goal/concepts/requirements/
// files/errors/context sections are derived from the folded transcript only
// (older user messages and tool outputs). Pinned task context, pinned facts,
// work item/requirement/case ids and the latest user message never enter the
// summary; the todo/progress/next sections only reference the DB truth
// snapshots (R-A5-13).
func deriveSummarySections(in SummaryInput) []SummarySection {
	sections := make([]SummarySection, len(summarySectionHeadings))
	for index, heading := range summarySectionHeadings {
		sections[index].Heading = heading
	}
	material := summaryMaterialMessages(in.Messages)
	extracted := summaryExtract(material)
	sections[0].Body = summaryGoalBody(material)
	sections[1].Body = summaryConceptsBody(material)
	sections[2].Body = summaryRequirementsBody(material, extracted)
	sections[3].Body = summaryFilesBody(extracted)
	sections[4].Body = summaryErrorsBody(extracted)
	sections[5].Body = summaryTodoBody(in.Plan)
	sections[6].Body = summaryProgressBody(in)
	sections[7].Body = summaryNextBody(in.Plan)
	sections[8].Body = summaryContextBody(extracted)
	return sections
}

// summaryDBTruthSection reports whether a section is rebuilt from the DB
// snapshots only (todo/progress/next).
func summaryDBTruthSection(index int) bool {
	return index >= 5 && index <= 7
}

// summaryMaterialMessages drops the pinned latest user message from the summary
// material (R-A5-4). The latest user message is the current/triggering turn: it
// is retained verbatim by the live transcript and must never be folded into the
// summary. Only that one message is excluded; earlier user turns and all
// assistant/tool material stay.
func summaryMaterialMessages(messages []SummaryReplayMessage) []SummaryReplayMessage {
	latest := -1
	for index := len(messages) - 1; index >= 0; index-- {
		if strings.EqualFold(strings.TrimSpace(messages[index].Role), "user") {
			latest = index
			break
		}
	}
	if latest < 0 {
		return messages
	}
	material := make([]SummaryReplayMessage, 0, len(messages)-1)
	material = append(material, messages[:latest]...)
	material = append(material, messages[latest+1:]...)
	return material
}

// summaryGoalBody lists the older user requests folded into the summary. It is
// deliberately derived from the folded transcript, never from the pinned task
// goal or the latest user message.
func summaryGoalBody(messages []SummaryReplayMessage) string {
	lines := make([]string, 0, maxSummaryGoalItems)
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "user") {
			continue
		}
		content := summaryOneLine(message.Content)
		if content == "" {
			continue
		}
		lines = append(lines, "- 历史用户: "+truncateRunes(content, 200))
		if len(lines) == maxSummaryGoalItems {
			break
		}
	}
	return summaryBullets(lines)
}

// summaryConstraintLinePattern recognizes explicit constraint/requirement
// statements in the folded transcript.
var summaryConstraintLinePattern = regexp.MustCompile(`(?i)(约束|限制|必须|不得|禁止|不能|只允许|要求|规则|constraint|must|shall|required)`)

// summaryConceptsBody extracts constraint-like statements from the folded
// transcript (older user messages and tool outputs). Pinned task constraints are
// never read.
func summaryConceptsBody(messages []SummaryReplayMessage) string {
	lines := make([]string, 0, maxSummaryConstraintItems)
	for _, message := range messages {
		for _, rawLine := range strings.Split(message.Content, "\n") {
			line := summaryOneLine(rawLine)
			if line == "" || !summaryConstraintLinePattern.MatchString(line) {
				continue
			}
			lines = append(lines, "- 约束: "+truncateRunes(line, 200))
			if len(lines) == maxSummaryConstraintItems {
				return summaryBullets(lines)
			}
		}
	}
	return summaryBullets(lines)
}

// summaryRequirementIDPattern recognizes requirement/case/artifact identifiers
// in the folded transcript.
var summaryRequirementIDPattern = regexp.MustCompile(`(?i)(?:requirement_id|case_id|fr|req|case)[-_=:]?\d+`)

// summaryImageMarkerPattern recognizes the R-A5-5 image placeholder.
var summaryImageMarkerPattern = regexp.MustCompile(`\[image skipped: attachment_id=[^\]]+\]`)

// summaryRequirementsBody renders the requirements/cases/artifacts observed in
// the folded transcript: requirement/case identifiers and image attachment
// markers. Pinned requirement/case ids are never read.
func summaryRequirementsBody(messages []SummaryReplayMessage, extracted summaryExtracted) string {
	lines := make([]string, 0, maxSummaryRequirementItems)
	for _, item := range extracted.ids {
		if !summaryRequirementIDPattern.MatchString(item) {
			continue
		}
		lines = append(lines, "- "+item)
		if len(lines) == maxSummaryRequirementItems {
			return summaryBullets(lines)
		}
	}
	for _, message := range messages {
		for _, marker := range summaryImageMarkerPattern.FindAllString(message.Content, -1) {
			lines = append(lines, "- artifact: "+marker)
			if len(lines) == maxSummaryRequirementItems {
				return summaryBullets(lines)
			}
		}
	}
	return summaryBullets(lines)
}

// summaryFilesBody renders the paths/commands/signatures extracted from the
// folded transcript.
func summaryFilesBody(extracted summaryExtracted) string {
	lines := make([]string, 0, len(extracted.paths)+len(extracted.commands)+len(extracted.signatures))
	lines = append(lines, extracted.paths...)
	lines = append(lines, extracted.commands...)
	lines = append(lines, extracted.signatures...)
	return summaryBullets(lines)
}

// summaryErrorsBody renders the error strings extracted from the folded
// transcript.
func summaryErrorsBody(extracted summaryExtracted) string {
	return summaryBullets(extracted.errors)
}

// summaryTodoBody renders the open plan items: every line carries the real plan
// item id and status (R-A5-13).
func summaryTodoBody(plan []agentcontext.PlanItemSnapshot) string {
	lines := make([]string, 0, maxSummaryPlanItems)
	for _, item := range plan {
		if summaryPlanTerminal(item.Status) {
			continue
		}
		lines = append(lines, summaryPlanItemLine(item))
		if len(lines) == maxSummaryPlanItems {
			break
		}
	}
	return strings.Join(lines, "\n")
}

// summaryProgressBody renders the finished plan items and the current facts. The
// fact lines keep the snapshot's kind/key/confidence verbatim.
func summaryProgressBody(in SummaryInput) string {
	lines := make([]string, 0, maxSummaryPlanItems+maxSummaryFactItems)
	for _, item := range in.Plan {
		if !summaryPlanTerminal(item.Status) {
			continue
		}
		lines = append(lines, summaryPlanItemLine(item))
		if len(lines) == maxSummaryPlanItems {
			break
		}
	}
	for index, fact := range in.Facts {
		if index == maxSummaryFactItems {
			break
		}
		lines = append(lines, summaryFactLine(fact))
	}
	return strings.Join(lines, "\n")
}

// summaryNextBody renders the next non-terminal plan item after the current one,
// falling back to the current item when it is the only open item. An empty plan
// renders (none) downstream: no next step is invented.
func summaryNextBody(plan []agentcontext.PlanItemSnapshot) string {
	open := make([]agentcontext.PlanItemSnapshot, 0, len(plan))
	for _, item := range plan {
		if !summaryPlanTerminal(item.Status) {
			open = append(open, item)
		}
	}
	if len(open) == 0 {
		return ""
	}
	next := open[0]
	if len(open) > 1 {
		next = open[1]
	}
	return summaryPlanItemLine(next)
}

// summaryContextBody renders the exact identifiers, paths, commands, errors,
// numbers and signatures preserved from the folded transcript. Pinned facts and
// the work item id never enter the summary (R-A5-4).
func summaryContextBody(extracted summaryExtracted) string {
	lines := make([]string, 0, maxSummaryContextItems)
	add := func(prefix string, values []string) {
		for _, item := range values {
			if len(lines) >= maxSummaryContextItems {
				return
			}
			lines = append(lines, "- "+prefix+item)
		}
	}
	add("", extracted.ids)
	add("error: ", extracted.errors)
	add("", extracted.numbers)
	return strings.Join(lines, "\n")
}

// summaryPlanItemLine renders one real plan item with its id/status (and its
// dependencies when present).
func summaryPlanItemLine(item agentcontext.PlanItemSnapshot) string {
	line := fmt.Sprintf("- plan_item id=%d status=%s title=%s",
		item.ID, summaryPlanStatus(item.Status), summaryOneLine(item.Title))
	if dependencies := summaryDependencyList(item.DependsOn); dependencies != "" {
		line += " depends_on=" + dependencies
	}
	return line
}

// summaryFactLine renders one real fact snapshot with kind/key/confidence.
func summaryFactLine(fact agentcontext.FactSnapshot) string {
	line := fmt.Sprintf("- fact kind=%s key=%s confidence=%s",
		summaryFactPart(fact.Kind), summaryFactPart(fact.Key), summaryConfidence(fact.Confidence))
	if summary := summaryOneLine(fact.Summary); summary != "" {
		line += " " + truncateRunes(summary, 200)
	}
	return line
}

func summaryFactPart(value string) string {
	if value = summaryOneLine(value); value == "" {
		return "unknown"
	}
	return value
}

func summaryConfidence(confidence *float64) string {
	if confidence == nil {
		return "unknown"
	}
	return strconv.FormatFloat(*confidence, 'f', 2, 64)
}

// summaryTerminalStatuses mirrors the briefing plan-terminal set: an unknown or
// empty status is open (never silently hidden).
var summaryTerminalStatuses = map[string]bool{
	"done":      true,
	"completed": true,
	"skipped":   true,
	"cancelled": true,
	"canceled":  true,
	"failed":    true,
	"closed":    true,
}

func summaryPlanTerminal(status string) bool {
	return summaryTerminalStatuses[strings.ToLower(strings.TrimSpace(status))]
}

func summaryPlanStatus(status string) string {
	if status = summaryOneLine(status); status == "" {
		return "unknown"
	}
	return status
}

func summaryDependencyList(dependencies []string) string {
	parts := make([]string, 0, len(dependencies))
	for _, dependency := range dependencies {
		if line := summaryOneLine(dependency); line != "" {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, ",")
}

// summaryExtracted is the exact-identifier material preserved from the folded
// transcript.
type summaryExtracted struct {
	paths      []string
	commands   []string
	ids        []string
	errors     []string
	numbers    []string
	signatures []string
}

var (
	summaryIDTokenPattern   = regexp.MustCompile(`\b[A-Z][A-Z0-9]{1,9}-\d+\b`)
	summaryFieldIDPattern   = regexp.MustCompile(`\b[a-z][a-z0-9]*_id\s*[=:]\s*"?\d+`)
	summaryPathPattern      = regexp.MustCompile(`(?:[A-Za-z]:\\[^\s，。；;：:、"'()（）\[\]]+|(?:\.{0,2}/)[^\s，。；;：:、"'()（）\[\]]+|(?:[\w.-]+/)+[\w.-]+)`)
	summaryCommandPattern   = regexp.MustCompile(`\b(?:go (?:test|build|vet|run|list)|npm|pnpm|yarn|git|docker|psql|make|curl)\b[^\n]*`)
	summaryErrorLinePattern = regexp.MustCompile(`(?i)(?:error|failed|panic|exception|失败|修复|错误[:：])`)
	summaryNumberPattern    = regexp.MustCompile(`\b(?:v\d+(?:\.\d+)+|\d+(?:\.\d+)?\s?(?:ms|s|%|KB|MB|GB|tokens?|chars?|条|个|分钟|秒))`)
	summarySignaturePattern = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?\([^()\n]{0,80}\)`)
	// summaryBareNumberPattern matches a bounded bare number (ids/counts/HTTP
	// statuses) that the unit/version pattern above does not cover.
	summaryBareNumberPattern = regexp.MustCompile(`\b\d{2,6}\b`)
)

// summaryExtract scans the folded transcript for the exact material the summary
// must preserve: ids, file paths, commands, error strings, numbers/signatures.
// Deduplication is order-preserving so the render is deterministic.
func summaryExtract(messages []SummaryReplayMessage) summaryExtracted {
	extracted := summaryExtracted{}
	seen := map[string]map[string]struct{}{
		"path":      {},
		"command":   {},
		"id":        {},
		"error":     {},
		"number":    {},
		"signature": {},
	}
	add := func(kind string, values []string, drop func(string) string) {
		for _, value := range values {
			if drop != nil {
				value = drop(value)
			}
			value = summaryOneLine(value)
			if value == "" {
				continue
			}
			if _, duplicate := seen[kind][value]; duplicate {
				continue
			}
			seen[kind][value] = struct{}{}
			switch kind {
			case "path":
				extracted.paths = append(extracted.paths, value)
			case "command":
				extracted.commands = append(extracted.commands, value)
			case "id":
				extracted.ids = append(extracted.ids, value)
			case "error":
				extracted.errors = append(extracted.errors, value)
			case "number":
				if len(extracted.numbers) >= maxSummaryBareNumbers {
					continue
				}
				extracted.numbers = append(extracted.numbers, value)
			case "signature":
				extracted.signatures = append(extracted.signatures, value)
			}
		}
	}
	for _, message := range messages {
		for _, rawLine := range strings.Split(message.Content, "\n") {
			line := strings.TrimSpace(rawLine)
			if line == "" {
				continue
			}
			add("id", summaryIDTokenPattern.FindAllString(line, -1), nil)
			add("id", summaryFieldIDPattern.FindAllString(line, -1), nil)
			add("path", summaryPathPattern.FindAllString(line, -1), nil)
			add("command", summaryCommandPattern.FindAllString(line, -1), func(value string) string {
				return strings.TrimRight(value, "。，；;.,")
			})
			add("number", summaryNumberPattern.FindAllString(line, -1), nil)
			add("number", summaryBareNumbers(line), nil)
			add("signature", summarySignaturePattern.FindAllString(line, -1), nil)
			if summaryErrorLinePattern.MatchString(line) {
				add("error", []string{truncateRunes(line, 200)}, nil)
			}
		}
	}
	return extracted
}

// summaryBareNumbers extracts bounded bare numbers (HTTP statuses, counts, ids)
// with a noise guard: numbers embedded in paths, versions, hyphenated ids or
// id fields are already preserved elsewhere and are not duplicated here.
func summaryBareNumbers(line string) []string {
	matches := summaryBareNumberPattern.FindAllStringIndex(line, -1)
	if len(matches) == 0 {
		return nil
	}
	values := make([]string, 0, len(matches))
	for _, match := range matches {
		if !summaryBareNumberBoundary(line, match[0], match[1]) {
			continue
		}
		if summaryIDFieldNumber(line, match[0]) {
			continue
		}
		value := line[match[0]:match[1]]
		if summaryYearNumber(value) {
			continue
		}
		values = append(values, value)
		if len(values) == maxSummaryBareNumbers {
			break
		}
	}
	return values
}

// summaryBareNumberBoundary rejects numbers that are part of a larger token
// (path segment, version, hyphenated id, decimal or unit suffix).
func summaryBareNumberBoundary(line string, start, end int) bool {
	if start > 0 {
		switch line[start-1] {
		case '.', '/', '\\', '-', '_', ':', '=':
			return false
		}
	}
	if end < len(line) {
		switch line[end] {
		case '.', '/', '\\', '-', '_', ':', '%':
			return false
		}
	}
	return true
}

// summaryIDFieldNumber reports whether the number is the value of an id field
// (for example attachment_id=9001): the id extractor already preserves that
// field verbatim, so a bare duplicate would only add noise.
func summaryIDFieldNumber(line string, start int) bool {
	prefix := strings.TrimSpace(line[:start])
	for _, separator := range []string{"=", ":", "："} {
		if !strings.HasSuffix(prefix, separator) {
			continue
		}
		field := strings.TrimSpace(strings.TrimSuffix(prefix, separator))
		if index := strings.LastIndexAny(field, " \t"); index >= 0 {
			field = field[index+1:]
		}
		return field == "id" || strings.HasSuffix(field, "_id")
	}
	return false
}

// summaryYearNumber filters 4-digit calendar years so timestamps do not flood
// the bare-number list.
func summaryYearNumber(value string) bool {
	if len(value) != 4 {
		return false
	}
	return strings.HasPrefix(value, "19") || strings.HasPrefix(value, "20")
}

// summaryBullets renders deduplicated bullet lines, preserving order.
func summaryBullets(lines []string) string {
	seen := make(map[string]struct{}, len(lines))
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = summaryOneLine(line)
		if line == "" || strings.HasPrefix(line, "- ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
		}
		if line == "" {
			continue
		}
		if _, duplicate := seen[line]; duplicate {
			continue
		}
		seen[line] = struct{}{}
		out = append(out, "- "+line)
	}
	return strings.Join(out, "\n")
}

// capPinnedFacts keeps the newest MaxPinnedFacts pinned facts (R-A5-4: ≤20).
// The input slice is never mutated.
func capPinnedFacts(facts []string) []string {
	if len(facts) <= MaxPinnedFacts {
		return facts
	}
	return facts[len(facts)-MaxPinnedFacts:]
}

// summaryOneLine trims and collapses whitespace so a database value cannot break
// the single-line summary layout.
func summaryOneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// mergeSummaryBodies concatenates bodies and drops verbatim (whitespace-
// normalized) duplicate lines. The first occurrence wins, so derived/DB material
// keeps precedence over a merged prior summary.
func mergeSummaryBodies(bodies ...string) string {
	seen := make(map[string]struct{})
	lines := make([]string, 0)
	for _, body := range bodies {
		for _, rawLine := range strings.Split(body, "\n") {
			line := strings.TrimSpace(rawLine)
			if line == "" || line == summaryNoneText {
				continue
			}
			key := strings.Join(strings.Fields(line), " ")
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// filterStalePriorSummaryLines keeps only the prior lines that are still true
// under the current DB snapshots and that do not re-introduce pinned R-A5-4
// material. A prior plan line is dropped when its item is gone or its status
// changed; a prior fact line is dropped when its kind/key is no longer current;
// a pinned_fact line is always dropped because pinned facts never enter the
// summary; any line echoing the pinned task context or the latest user message
// is dropped.
func filterStalePriorSummaryLines(body string, in SummaryInput) string {
	if body == "" {
		return body
	}
	statuses := make(map[int64]string, len(in.Plan))
	for _, item := range in.Plan {
		statuses[item.ID] = summaryPlanStatus(item.Status)
	}
	facts := make(map[string]struct{}, len(in.Facts))
	for _, fact := range in.Facts {
		facts[summaryFactKey(fact.Kind, fact.Key)] = struct{}{}
	}
	needles := summaryPinnedNeedles(in)
	lines := make([]string, 0)
	for _, rawLine := range strings.Split(body, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if summaryPriorPinnedFactPattern.MatchString(line) {
			continue
		}
		if match := summaryPriorPlanPattern.FindStringSubmatch(line); match != nil {
			id, err := strconv.ParseInt(match[1], 10, 64)
			if err != nil {
				continue
			}
			status, ok := statuses[id]
			if !ok {
				continue
			}
			if len(match) > 2 && match[2] != "" && !strings.EqualFold(match[2], status) {
				continue
			}
		}
		if len(facts) > 0 {
			if match := summaryPriorFactPattern.FindStringSubmatch(line); match != nil {
				if _, ok := facts[summaryFactKey(match[1], match[2])]; !ok {
					continue
				}
			}
		}
		if summaryLineHasNeedle(line, needles) {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// summaryFactKey normalizes the DB fact identity used to detect stale prior
// fact lines.
func summaryFactKey(kind, key string) string {
	return strings.ToLower(summaryFactPart(kind)) + "\x00" + strings.TrimSpace(key)
}

// summaryPinnedNeedles is the R-A5-4 pinned material that must never reappear
// through a prior summary: the task context values (goal/constraints/acceptance/
// scope/type), the pinned facts and the latest user message. Short values are
// skipped so a single character cannot filter unrelated lines.
func summaryPinnedNeedles(in SummaryInput) []string {
	values := make([]string, 0, 8+len(in.Task.Constraints)+len(in.Task.AcceptanceCriteria)+len(in.Task.Scope)+len(in.PinnedFacts))
	values = append(values, in.Task.Type, in.Task.Goal)
	values = append(values, in.Task.Constraints...)
	values = append(values, in.Task.AcceptanceCriteria...)
	values = append(values, in.Task.Scope...)
	values = append(values, in.PinnedFacts...)
	values = append(values, summaryLatestUserText(in.Messages))
	needles := make([]string, 0, len(values))
	for _, value := range values {
		value = summaryOneLine(value)
		if len([]rune(value)) < 6 {
			continue
		}
		needles = append(needles, value)
	}
	return needles
}

// summaryLatestUserText returns the latest user message of the folded range (the
// R-A5-4 pinned latest user message) when one is present.
func summaryLatestUserText(messages []SummaryReplayMessage) string {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if !strings.EqualFold(strings.TrimSpace(message.Role), "user") {
			continue
		}
		return summaryOneLine(message.Content)
	}
	return ""
}

// summaryLineHasNeedle reports whether a whitespace-normalized line contains any
// pinned needle.
func summaryLineHasNeedle(line string, needles []string) bool {
	line = summaryOneLine(line)
	for _, needle := range needles {
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

var (
	summaryPriorPlanPattern       = regexp.MustCompile(`plan_item id=(\d+)(?:\s+status=([^\s]+))?`)
	summaryPriorFactPattern       = regexp.MustCompile(`fact\s+kind=(\S+)\s+key=(\S+)`)
	summaryPriorPinnedFactPattern = regexp.MustCompile(`(?i)pinned_fact\s*:`)
)

// parseSummarySections parses markdown summary text into the canonical section
// bodies. ok is false when the text carries none of the nine headings; content
// before the first recognized heading (a model preamble) is dropped.
func parseSummarySections(text string) ([]string, bool) {
	bodies := make([]string, len(summarySectionHeadings))
	if strings.TrimSpace(text) == "" {
		return bodies, false
	}
	current := -1
	found := false
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "##") && !strings.HasPrefix(line, "###") {
			heading := summaryHeadingKey(line)
			if index, ok := summaryHeadingIndexes[heading]; ok {
				current = index
				found = true
				continue
			}
			current = -1
			continue
		}
		if current < 0 {
			continue
		}
		bodies[current] = strings.TrimSpace(bodies[current] + "\n" + line)
	}
	if !found {
		return bodies, false
	}
	return bodies, true
}

// renderSummarySections renders the canonical layout: every heading is always
// emitted and an empty body renders (none).
func renderSummarySections(sections []SummarySection) string {
	parts := make([]string, 0, len(sections))
	for _, section := range sections {
		body := strings.TrimSpace(section.Body)
		if body == "" {
			body = summaryNoneText
		}
		parts = append(parts, "## "+section.Heading+"\n"+body)
	}
	return strings.Join(parts, "\n\n")
}

// renderSummarySectionsWith renders the canonical layout with per-section body
// overrides used by the truncation pass.
func renderSummarySectionsWith(sections []SummarySection, bodies []string) string {
	parts := make([]string, 0, len(sections))
	for index, section := range sections {
		body := section.Body
		if index < len(bodies) {
			body = bodies[index]
		}
		body = strings.TrimSpace(body)
		if body == "" {
			body = summaryNoneText
		}
		parts = append(parts, "## "+section.Heading+"\n"+body)
	}
	return strings.Join(parts, "\n\n")
}

// renderSummaryWithinBudget enforces summary_max while keeping the nine
// headings. Content that does not fit is dropped from the section tails; a
// section that cannot fit at all renders (none). Only the documented floor case
// (the empty headings layout alone exceeds the cap) returns a render above the
// cap, because dropping a section is forbidden by R-A5-7.
func renderSummaryWithinBudget(sections []SummarySection, maxTokens int) (string, bool) {
	full := renderSummarySections(sections)
	if maxTokens <= 0 || EstimateSummaryTokens(full) <= maxTokens {
		return full, false
	}
	bodies := make([]string, len(sections))
	for index := range sections {
		body := strings.TrimSpace(sections[index].Body)
		if body == "" {
			continue
		}
		lines := nonEmptySummaryLines(body)
		kept := make([]string, 0, len(lines))
		for _, line := range lines {
			trial := append(append([]string(nil), kept...), line)
			bodies[index] = strings.Join(trial, "\n")
			if EstimateSummaryTokens(renderSummarySectionsWith(sections, bodies)) <= maxTokens {
				kept = trial
				continue
			}
			fitted := fitSummaryLine(line, func(candidate string) bool {
				bodies[index] = strings.Join(append(append([]string(nil), kept...), candidate), "\n")
				return EstimateSummaryTokens(renderSummarySectionsWith(sections, bodies)) <= maxTokens
			})
			if fitted != "" {
				kept = append(kept, fitted)
			}
			bodies[index] = strings.Join(kept, "\n")
			break
		}
		bodies[index] = strings.Join(kept, "\n")
	}
	text := renderSummarySectionsWith(sections, bodies)
	if EstimateSummaryTokens(text) > maxTokens {
		// Heading floor: the nine empty headings alone exceed the cap. Keep
		// every section (R-A5-7 forbids dropping one) and report truncation.
		emptySections := make([]SummarySection, len(sections))
		for index, section := range sections {
			emptySections[index] = SummarySection{Heading: section.Heading}
		}
		return renderSummarySections(emptySections), true
	}
	truncated := false
	for index := range sections {
		if strings.TrimSpace(sections[index].Body) != strings.TrimSpace(bodies[index]) {
			truncated = true
			break
		}
	}
	return text, truncated
}

func nonEmptySummaryLines(body string) []string {
	lines := make([]string, 0)
	for _, rawLine := range strings.Split(body, "\n") {
		if line := strings.TrimSpace(rawLine); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// fitSummaryLine returns the longest truncation of line (with the ellipsis
// inside the cap) accepted by fits, or "" when nothing fits.
func fitSummaryLine(line string, fits func(string) bool) string {
	if fits(line) {
		return line
	}
	runes := []rune(line)
	if len(runes) == 0 {
		return ""
	}
	best := ""
	low, high := 1, len(runes)
	for low <= high {
		middle := (low + high) / 2
		candidate := string(runes[:middle]) + truncationEllipsis
		if fits(candidate) {
			best = candidate
			low = middle + 1
			continue
		}
		high = middle - 1
	}
	return best
}

const truncationEllipsis = "…"

// summaryInstructionMarker labels the compaction instruction appended as the
// last user message of the prefix-reusing summary request (R-A5-8).
const summaryInstructionMarker = "压缩指令"

// BuildSummaryRequest builds the R-A5-8 summary request as a true prefix of the
// live provider transcript: the current conversation's stable prefix is reused
// byte for byte (never trimmed), then the live prefix messages (briefing/facts/
// prior summary), then the watermark-filtered folded range, and finally the
// compaction instruction as the last user message. Replaying the live messages
// before the folded range is what lets the provider reuse the mid-transcript
// prefix cache, not just the system prompt. Tool calls, tool-call identities,
// reasoning content and images are preserved on replay. The watermark is applied
// here; Summarize reuses the selected input for the result as well.
func BuildSummaryRequest(in SummaryInput) llmapp.ChatRequest {
	return buildSummaryRequest(selectSummaryInput(in))
}

// buildSummaryRequest is the already-selected request builder. It never filters
// again.
func buildSummaryRequest(in SummaryInput) llmapp.ChatRequest {
	system := in.StablePrefix
	if system == "" {
		system = summaryCompactionSystemPrompt
	}
	messages := make([]llmapp.ChatMessage, 0, 1+len(in.LiveMessages)+len(in.Messages)+1)
	messages = append(messages, llmapp.ChatMessage{Role: "system", Content: system})
	for _, message := range in.LiveMessages {
		messages = append(messages, summaryReplayChatMessage(message))
	}
	for _, message := range in.Messages {
		messages = append(messages, summaryReplayChatMessage(message))
	}
	messages = append(messages, llmapp.ChatMessage{
		Role:    "user",
		Content: summaryCompactionInstruction(in),
	})
	return llmapp.ChatRequest{
		Messages:        messages,
		Tools:           append([]llmapp.ToolDefinition(nil), in.Tools...),
		MaxOutputTokens: summaryOutputTokens(in),
	}
}

// summaryReplayChatMessage converts one replay message to the provider shape
// without dropping any prefix-relevant field.
func summaryReplayChatMessage(message SummaryReplayMessage) llmapp.ChatMessage {
	return llmapp.ChatMessage{
		Role:             message.Role,
		Content:          message.Content,
		Images:           append([]llmapp.ChatImage(nil), message.Images...),
		ToolCalls:        append([]llmapp.ToolCall(nil), message.ToolCalls...),
		ToolCallID:       message.ToolCallID,
		ReasoningContent: message.ReasoningContent,
	}
}

// SelectSummaryMessages returns the messages newer than the covered watermark
// (R-A5-12). IDs are monotonic in a conversation, so a summary only folds the
// messages the previous revision did not cover. Summarize applies it once per
// transaction and hands the same selected input to the request and result
// builders; the exported BuildSummaryResult/BuildSummaryRequest apply it once
// for callers that start from unfiltered Messages.
func SelectSummaryMessages(messages []SummaryReplayMessage, coveredMessageID int64) []SummaryReplayMessage {
	if coveredMessageID <= 0 {
		return messages
	}
	selected := make([]SummaryReplayMessage, 0, len(messages))
	for _, message := range messages {
		if message.ID > coveredMessageID {
			selected = append(selected, message)
		}
	}
	return selected
}

// ErrSummaryWatermarkStale reports a covered_message_id that would move
// backwards. R-A5-12 requires the watermark to be monotonic.
var ErrSummaryWatermarkStale = errors.New("agentapp: summary covered_message_id would move backwards")

// summaryOutputTokens derives the summary completion bound from summary_max.
func summaryOutputTokens(in SummaryInput) int {
	if in.MaxOutputTokens > 0 {
		return in.MaxOutputTokens
	}
	maxTokens := in.summaryMax()
	if maxTokens <= 0 {
		return maxSummarizerOutputTokens
	}
	if maxTokens < 256 {
		maxTokens = 256
	}
	if maxTokens > agentcontext.MaxCompactionSummaryTokens {
		maxTokens = agentcontext.MaxCompactionSummaryTokens
	}
	return maxTokens
}

// summaryCompactionInstruction renders the last user message of the summary
// request. It restates the nine-section contract, the DB truth (plan/facts), the
// prior summary and the pinned task context.
func summaryCompactionInstruction(in SummaryInput) string {
	var builder strings.Builder
	builder.WriteString("# ")
	builder.WriteString(summaryInstructionMarker)
	builder.WriteString("\n把上面的历史对话压缩成结构化中文摘要，只输出摘要文本，不调用任何工具。\n")
	builder.WriteString("必须依次使用以下九个二级标题；空段写 ")
	builder.WriteString(summaryNoneText)
	builder.WriteString("，不得删段：\n")
	for _, heading := range summarySectionHeadings {
		builder.WriteString("## ")
		builder.WriteString(heading)
		builder.WriteString("\n")
	}
	builder.WriteString("保留精确 ID、路径、命令、错误串、数值与函数签名；不编造未出现的信息；")
	builder.WriteString("“待办作业 / 当前进展 / 下一步”必须引用下方 DB 快照中的 plan item id/status 与 fact key；")
	builder.WriteString("摘要与 DB 快照冲突时以 DB 为准。图片只保留 [image skipped: ...] 占位，不写图片内容。\n")

	builder.WriteString("\n## DB 真相（plan 快照：id/title/status/depends_on）\n")
	if len(in.Plan) == 0 {
		builder.WriteString(summaryNoneText)
		builder.WriteString("\n")
	}
	for index, item := range in.Plan {
		if index == maxSummaryPlanItems {
			break
		}
		builder.WriteString(summaryPlanItemLine(item))
		builder.WriteString("\n")
	}
	builder.WriteString("\n## DB 真相（facts 快照：kind/key/confidence）\n")
	if len(in.Facts) == 0 {
		builder.WriteString(summaryNoneText)
		builder.WriteString("\n")
	}
	for index, fact := range in.Facts {
		if index == maxSummaryFactItems {
			break
		}
		builder.WriteString(summaryFactLine(fact))
		builder.WriteString("\n")
	}
	if prior := strings.TrimSpace(in.PriorSummary); prior != "" {
		builder.WriteString("\n## 已有摘要（保留仍成立的事实、丢弃过时项、不得逐字复制）\n")
		builder.WriteString(prior)
		builder.WriteString("\n")
	}
	builder.WriteString("\n## 钉住任务上下文（永不进入摘要，只作背景）\n")
	builder.WriteString(strings.Join(summaryTaskContextLines(in.Task, in.WorkItemID, in.RequirementIDs, in.CaseIDs, capPinnedFacts(in.PinnedFacts)), "\n"))
	return builder.String()
}

// summaryTaskContextLines renders the pinned R-A5-4 task context: work item,
// task type, goal, constraints, acceptance criteria, scope and the
// requirement/case ids. It is the single renderer shared by the compaction
// instruction and the post-compaction reinjection, and it never reads the
// rolling summary or the folded transcript.
func summaryTaskContextLines(task TaskContext, workItemID int64, requirementIDs, caseIDs []int64, pinnedFacts []string) []string {
	lines := make([]string, 0, 16)
	lines = append(lines, fmt.Sprintf("work_item: %d", workItemID))
	if taskType := summaryOneLine(task.Type); taskType != "" {
		lines = append(lines, "task_type: "+taskType)
	}
	lines = append(lines, "goal: "+summaryOrNone(task.Goal))
	lines = append(lines, "constraints:")
	lines = append(lines, summaryTaskContextList(task.Constraints)...)
	lines = append(lines, "acceptance:")
	lines = append(lines, summaryTaskContextList(task.AcceptanceCriteria)...)
	lines = append(lines, "scope:")
	lines = append(lines, summaryTaskContextList(task.Scope)...)

	requirements := summaryScopedIDs(requirementIDs, task.Scope, "requirement_id")
	lines = append(lines, "requirements:")
	if len(requirements) == 0 {
		lines = append(lines, "- "+summaryNoneText)
	}
	for _, id := range requirements {
		lines = append(lines, fmt.Sprintf("- requirement_id=%d", id))
	}
	cases := summaryScopedIDs(caseIDs, task.Scope, "case_id")
	lines = append(lines, "cases:")
	if len(cases) == 0 {
		lines = append(lines, "- "+summaryNoneText)
	}
	for _, id := range cases {
		lines = append(lines, fmt.Sprintf("- case_id=%d", id))
	}
	if len(pinnedFacts) > 0 {
		lines = append(lines, fmt.Sprintf("pinned_facts (%d/%d):", len(pinnedFacts), MaxPinnedFacts))
		for _, fact := range pinnedFacts {
			if line := summaryOneLine(fact); line != "" {
				lines = append(lines, "- "+line)
			}
		}
	}
	return lines
}

func summaryOrNone(text string) string {
	if line := summaryOneLine(text); line != "" {
		return line
	}
	return summaryNoneText
}

func summaryTaskContextList(values []string) []string {
	if len(values) == 0 {
		return []string{"- " + summaryNoneText}
	}
	lines := make([]string, 0, len(values))
	for _, value := range values {
		if line := summaryOneLine(value); line != "" {
			lines = append(lines, "- "+line)
		}
	}
	if len(lines) == 0 {
		return []string{"- " + summaryNoneText}
	}
	return lines
}

// summaryScopedIDs merges explicit ids with the ids carried by the task scope
// lines (for example "requirement_id=501").
func summaryScopedIDs(ids []int64, scope []string, field string) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	merged := make([]int64, 0, len(ids))
	appendID := func(id int64) {
		if id <= 0 {
			return
		}
		if _, duplicate := seen[id]; duplicate {
			return
		}
		seen[id] = struct{}{}
		merged = append(merged, id)
	}
	for _, id := range ids {
		appendID(id)
	}
	prefix := field + "="
	for _, line := range scope {
		line = strings.TrimSpace(line)
		index := strings.Index(line, prefix)
		if index < 0 {
			continue
		}
		value := strings.TrimSpace(line[index+len(prefix):])
		end := 0
		for end < len(value) && value[end] >= '0' && value[end] <= '9' {
			end++
		}
		if end == 0 {
			continue
		}
		parsed, err := strconv.ParseInt(value[:end], 10, 64)
		if err != nil {
			continue
		}
		appendID(parsed)
	}
	return merged
}

// SummaryMessages converts conversation-thread messages into the folded summary
// range. The transcript contract is:
//   - the message Text and every text part are replayed;
//   - an image part is replaced by the R-A5-5 attachment marker and its bytes,
//     data_refs and other payload fields never enter the summary;
//   - any other structured part (requirement_card/rollback_proposal/...) is
//     rendered as bounded "[type] {json}" text instead of being dropped;
//   - the UI thread shape carries no tool-call identity, so ToolCallID is empty;
//     callers with the live transcript pass SummaryReplayMessage values with the
//     full tool-call/reasoning fields instead.
func SummaryMessages(messages []ConversationMessage) []SummaryReplayMessage {
	converted := make([]SummaryReplayMessage, 0, len(messages))
	for _, message := range messages {
		parts := make([]string, 0, len(message.Parts)+1)
		if text := strings.TrimSpace(message.Text); text != "" {
			parts = append(parts, text)
		}
		for _, part := range message.Parts {
			switch part.Type {
			case "image":
				parts = append(parts, tools.ImageSkippedMarker(conversationImageAttachmentID(part)))
			case "text":
				if text := strings.TrimSpace(part.Text); text != "" && text != strings.TrimSpace(message.Text) {
					parts = append(parts, text)
				}
			default:
				if structured := summaryStructuredPart(part); structured != "" {
					parts = append(parts, structured)
				}
			}
		}
		content := strings.TrimSpace(strings.Join(parts, "\n"))
		if content == "" {
			continue
		}
		converted = append(converted, SummaryReplayMessage{
			ID:      message.ID,
			Role:    message.Role,
			Content: content,
		})
	}
	return converted
}

// summaryStructuredPart renders one non-text/non-image conversation part as
// bounded structured text. The payload is serialized deterministically (JSON
// object keys are sorted) and capped so one card cannot dominate the summary.
func summaryStructuredPart(part ConversationPart) string {
	if strings.TrimSpace(part.Type) == "" {
		return ""
	}
	payload := ""
	if len(part.Data) > 0 {
		encoded, err := json.Marshal(part.Data)
		if err == nil {
			payload = truncateRunes(string(encoded), 500)
		}
	}
	if payload == "" {
		return "[" + part.Type + "]"
	}
	return "[" + part.Type + "] " + payload
}

// conversationImageAttachmentID extracts the decimal attachment id of an image
// conversation part. Image parts carry no attachment id in older threads; the
// marker then says "unknown" instead of fabricating one.
func conversationImageAttachmentID(part ConversationPart) int64 {
	switch value := part.Data["attachment_id"].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

// EventSummaryTruncated is the R-A5-7 event published when summary_max cut the
// generated summary.
const EventSummaryTruncated = "summary_truncated"

// SummaryEvent is one summary lifecycle event.
type SummaryEvent struct {
	Type    string
	Payload map[string]any
}

// SummaryEventPublisher is the narrow outbound port of the summary truncation
// event. Emission is best effort and never fails the compaction.
type SummaryEventPublisher interface {
	PublishSummaryEvent(ctx context.Context, event SummaryEvent) error
}

// CompactionSummarizerOption customizes a CompactionSummarizer.
type CompactionSummarizerOption func(*CompactionSummarizer)

// WithSummaryEventPublisher wires the summary_truncated event port.
func WithSummaryEventPublisher(publisher SummaryEventPublisher) CompactionSummarizerOption {
	return func(summarizer *CompactionSummarizer) {
		if summarizer == nil || publisher == nil {
			return
		}
		summarizer.publisher = publisher
	}
}

// WithSummaryLogger sets the logger used for fallback degradation warnings.
func WithSummaryLogger(logger *slog.Logger) CompactionSummarizerOption {
	return func(summarizer *CompactionSummarizer) {
		if summarizer == nil || logger == nil {
			return
		}
		summarizer.logger = logger
	}
}

// CompactionSummarizer generates the R-A5-7 summary of one compaction
// transaction through the R-A5-8 prefix-reusing request. Task 36 wires it into
// the runner's compaction transaction; the type is complete here so the request
// shape, the nine-section finalisation and the cached_tokens bookkeeping are
// tested without a live provider.
type CompactionSummarizer struct {
	chat      llmapp.ChatGateway
	publisher SummaryEventPublisher
	logger    *slog.Logger
}

// NewCompactionSummarizer wires the summary chat gateway. A nil gateway is
// allowed: Summarize then returns the deterministic nine-section fallback.
func NewCompactionSummarizer(chat llmapp.ChatGateway, options ...CompactionSummarizerOption) *CompactionSummarizer {
	summarizer := &CompactionSummarizer{chat: chat}
	for _, option := range options {
		if option != nil {
			option(summarizer)
		}
	}
	return summarizer
}

// Summarize requests one summary with the prefix-reusing request, finalises the
// model output into the nine canonical sections, enforces summary_max, records
// the provider cached_tokens and publishes summary_truncated. A provider failure
// or a missing gateway degrades to the deterministic summary instead of failing
// the compaction (R-A5-10).
//
// R-A5-12: the watermark is applied exactly once per transaction and the same
// selected input feeds the request, the deterministic fallback and the result.
func (s *CompactionSummarizer) Summarize(ctx context.Context, in SummaryInput) SummaryResult {
	selected := selectSummaryInput(in)
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return s.fallback(ctx, selected, err)
		}
	}
	if s == nil || s.chat == nil {
		return s.fallback(ctx, selected, errors.New("compaction summarizer is not configured"))
	}
	response, err := s.chat.Chat(ctx, buildSummaryRequest(selected))
	if err != nil {
		return s.fallback(ctx, selected, err)
	}
	final := selected
	final.Draft = strings.TrimSpace(response.Content)
	result := buildSummaryResult(final)
	if strings.TrimSpace(final.Draft) == "" {
		result.UsedFallback = true
		result.FallbackReason = "summary model returned an empty response"
	} else if _, ok := parseSummarySections(final.Draft); !ok {
		// The provider answered but not in the nine-section contract: the
		// deterministic builder authored the result, so record the fallback.
		result.UsedFallback = true
		result.FallbackReason = "summary model output has no section headings"
	}
	if cachedTokens, cacheHit, ok := providerCacheStats(response.Usage); ok {
		result.CachedTokens = cachedTokens
		result.CacheHit = cacheHit
	}
	result = filterSummaryPinnedLeak(result, selected)
	if result.Truncated {
		s.publishTruncated(ctx, selected, result)
	}
	return result
}

// FilterPinnedLeak is the H5 guard (v0.22 fix round 1): pinned session facts
// and the pinned per-work-item task context must never be folded into the
// rolling summary. The deterministic builder already excludes them, but a model
// draft can reproduce them verbatim; any summary line containing an exact pinned
// fact/constraint/acceptance/scope/goal string is dropped before persistence.
// Strings shorter than 8 runes are ignored so generic words cannot blank the
// summary.
func FilterPinnedLeak(text string, in SummaryInput) (string, bool) {
	needles := make([]string, 0, len(in.PinnedFacts)+5+len(in.Task.Constraints)+len(in.Task.AcceptanceCriteria)+len(in.Task.Scope))
	appendNeedle := func(value string) {
		value = strings.TrimSpace(value)
		if len([]rune(value)) < 8 {
			return
		}
		needles = append(needles, value)
	}
	for _, fact := range capPinnedFacts(in.PinnedFacts) {
		appendNeedle(fact)
	}
	appendNeedle(in.Task.Goal)
	for _, value := range in.Task.Constraints {
		appendNeedle(value)
	}
	for _, value := range in.Task.AcceptanceCriteria {
		appendNeedle(value)
	}
	for _, value := range in.Task.Scope {
		appendNeedle(value)
	}
	if len(needles) == 0 || strings.TrimSpace(text) == "" {
		return text, false
	}
	leaked := false
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	for _, line := range lines {
		drop := false
		for _, needle := range needles {
			if strings.Contains(line, needle) {
				drop = true
				break
			}
		}
		if drop {
			leaked = true
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n"), leaked
}

// filterSummaryPinnedLeak applies the H5 filter and refreshes the token
// bookkeeping when lines were removed.
func filterSummaryPinnedLeak(result SummaryResult, in SummaryInput) SummaryResult {
	filtered, leaked := FilterPinnedLeak(result.Text, in)
	if !leaked {
		return result
	}
	result.Text = filtered
	result.Tokens = EstimateSummaryTokens(filtered)
	result.PinnedLeakFiltered = true
	return result
}

// fallback returns the deterministic nine-section summary and records why the
// model path was skipped. in is already watermark-selected. A truncated fallback
// still publishes summary_truncated (R-A5-7): the event must not depend on which
// path authored the summary.
func (s *CompactionSummarizer) fallback(ctx context.Context, in SummaryInput, cause error) SummaryResult {
	result := buildSummaryResult(in)
	result.UsedFallback = true
	if cause != nil {
		result.FallbackReason = cause.Error()
		s.logWarn(ctx, "context compaction: summary generation failed; using deterministic summary", "error", cause)
	}
	result = filterSummaryPinnedLeak(result, in)
	if result.Truncated {
		s.publishTruncated(ctx, in, result)
	}
	return result
}

// publishTruncated emits the R-A5-7 summary_truncated flag/event. A transport
// failure is logged and never fails the compaction.
func (s *CompactionSummarizer) publishTruncated(ctx context.Context, in SummaryInput, result SummaryResult) {
	if s == nil || s.publisher == nil {
		return
	}
	payload := map[string]any{
		"conversation_id":    strconv.FormatInt(in.ConversationID, 10),
		"compaction_id":      in.CompactionID,
		"trigger":            string(in.Trigger),
		"summary_max":        in.summaryMax(),
		"summary_tokens":     result.Tokens,
		"input_tokens":       result.InputTokens,
		"image_tokens":       in.ImageCount * SummaryImageTokens,
		"covered_message_id": result.CoveredMessageID,
	}
	publishContext := context.Background()
	if ctx != nil {
		publishContext = context.WithoutCancel(ctx)
	}
	if err := s.publisher.PublishSummaryEvent(publishContext, SummaryEvent{
		Type:    EventSummaryTruncated,
		Payload: payload,
	}); err != nil {
		s.logWarn(ctx, "context compaction: publish summary_truncated failed", "error", err)
	}
}

func (s *CompactionSummarizer) logWarn(ctx context.Context, message string, args ...any) {
	logger := (*slog.Logger)(nil)
	if s != nil {
		logger = s.logger
	}
	if logger == nil {
		logger = slog.Default()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	logger.WarnContext(ctx, message, args...)
}

// SummaryCommit is the R-A5-12 persistence hook of one compaction summary
// revision. The store (Task 36 adapter) assigns the version, enforces the
// monotonic covered_message_id and stores compaction_id/summary_meta.
type SummaryCommit struct {
	VersionID        int64
	ConversationID   int64
	CompactionID     string
	Trigger          agentcontext.CompactionTrigger
	Summary          string
	PinnedFacts      []string
	CoveredMessageID int64
	SummaryMeta      map[string]any
	TokenEstimate    int
}

// SummaryRecord is one persisted conversation_summary revision.
type SummaryRecord struct {
	Version   int
	UpdatedAt time.Time
	SummaryCommit
}

// SummaryStore is the R-A5-12 persistence port. Implementations MUST:
//   - assign version = latest version + 1 (per conversation),
//   - reject a covered_message_id lower than the latest one with
//     ErrSummaryWatermarkStale,
//   - store summary_meta as an object and keep compaction_id.
type SummaryStore interface {
	AppendSummary(ctx context.Context, commit SummaryCommit) (SummaryRecord, error)
}

// SummaryLedger is the in-process reference implementation of SummaryStore. The
// DB adapter (Task 36) must implement the same contract transactionally.
type SummaryLedger struct {
	mu     sync.Mutex
	now    func() time.Time
	latest map[int64]SummaryRecord
}

// AppendSummary stores one summary revision under the R-A5-12 contract.
func (l *SummaryLedger) AppendSummary(ctx context.Context, commit SummaryCommit) (SummaryRecord, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return SummaryRecord{}, err
		}
	}
	if commit.ConversationID <= 0 {
		return SummaryRecord{}, errors.New("agentapp: summary conversation_id must be positive")
	}
	if strings.TrimSpace(commit.Summary) == "" {
		return SummaryRecord{}, errors.New("agentapp: summary text must not be empty")
	}
	if l == nil {
		return SummaryRecord{}, errors.New("agentapp: summary ledger is not configured")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.latest == nil {
		l.latest = make(map[int64]SummaryRecord)
	}
	previous, ok := l.latest[commit.ConversationID]
	if ok && commit.CoveredMessageID < previous.CoveredMessageID {
		return SummaryRecord{}, fmt.Errorf("%w: previous=%d candidate=%d",
			ErrSummaryWatermarkStale, previous.CoveredMessageID, commit.CoveredMessageID)
	}
	version := 1
	if ok {
		version = previous.Version + 1
	}
	commit.PinnedFacts = append([]string(nil), commit.PinnedFacts...)
	if commit.SummaryMeta == nil {
		commit.SummaryMeta = map[string]any{}
	} else {
		meta := make(map[string]any, len(commit.SummaryMeta))
		for key, value := range commit.SummaryMeta {
			meta[key] = value
		}
		commit.SummaryMeta = meta
	}
	record := SummaryRecord{Version: version, UpdatedAt: l.currentTime(), SummaryCommit: commit}
	l.latest[commit.ConversationID] = record
	return record, nil
}

// LatestSummary returns the newest revision of one conversation.
func (l *SummaryLedger) LatestSummary(conversationID int64) (SummaryRecord, bool) {
	if l == nil {
		return SummaryRecord{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	record, ok := l.latest[conversationID]
	return record, ok
}

func (l *SummaryLedger) currentTime() time.Time {
	if l == nil || l.now == nil {
		return time.Now()
	}
	return l.now()
}

// Commit renders the persistence commit of one summary result. The pinned-facts
// payload honors the R-A5-4 ≤20 cap (newest window), so a persistence adapter
// never stores a different fact window than the prompt used.
func (r SummaryResult) Commit(in SummaryInput) SummaryCommit {
	meta := map[string]any{
		"trigger":              string(in.Trigger),
		"summary_tokens":       r.Tokens,
		"input_tokens":         r.InputTokens,
		"image_tokens":         in.ImageCount * SummaryImageTokens,
		"summary_max":          in.summaryMax(),
		"truncated":            r.Truncated,
		"cached_tokens":        r.CachedTokens,
		"used_fallback":        r.UsedFallback,
		"pinned_leak_filtered": r.PinnedLeakFiltered,
	}
	if in.ConversationID > 0 {
		meta["conversation_id"] = strconv.FormatInt(in.ConversationID, 10)
	}
	return SummaryCommit{
		VersionID:        in.VersionID,
		ConversationID:   in.ConversationID,
		CompactionID:     r.CompactionID,
		Trigger:          in.Trigger,
		Summary:          r.Text,
		PinnedFacts:      append([]string(nil), capPinnedFacts(in.PinnedFacts)...),
		CoveredMessageID: r.CoveredMessageID,
		SummaryMeta:      meta,
		TokenEstimate:    r.Tokens,
	}
}
