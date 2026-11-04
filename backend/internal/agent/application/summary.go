package agentapp

import (
	"context"
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

// NewLLMSummarizer wires the LLM-backed summarizer. A nil gateway is allowed;
// Summarize then returns an error so the caller falls back deterministically.
func NewLLMSummarizer(chat llmapp.ChatGateway) *LLMSummarizer {
	return &LLMSummarizer{chat: chat}
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
	// SummaryImageTokens is the R-A5-5 token charge of one skipped image.
	SummaryImageTokens = 1500
	// maxSummaryContextItems bounds the extracted exact identifiers of the
	// key-context section so one noisy transcript cannot flood the summary.
	maxSummaryContextItems = 20
	// maxSummaryPlanItems bounds one rendered plan/fact group.
	maxSummaryPlanItems = 10
	// maxSummaryFactItems bounds one rendered fact group.
	maxSummaryFactItems = 10
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
	// Messages is the summarised range, oldest first. Image content never
	// reaches this slice: SummaryMessages replaces it with attachment markers.
	Messages []Message

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
	CachedTokens     int
	CacheHit         bool
	UsedFallback     bool
	FallbackReason   string
	CoveredMessageID int64
	CompactionID     string
}

// EstimateSummaryTokens is the R-A5-14 conservative estimator for summary
// material: CJK characters cost 1 token, other bytes a quarter and a fixed
// 16-token overhead. It deliberately over-estimates (rounds the non-CJK part
// up) so a truncated summary still fits the budget the provider sees.
func EstimateSummaryTokens(text string) int {
	cjk, other := 0, 0
	for _, r := range text {
		if summaryCJK(r) {
			cjk++
			continue
		}
		other++
	}
	return cjk + (other+3)/4 + tokenOverhead
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

// BuildSummary renders the R-A5-7 nine-section summary text. It is deterministic
// and never invents material: the plan/progress/next sections are rebuilt from
// the DB snapshots, exact ids/paths/commands/errors/numbers/signatures are
// preserved, a prior summary is merged without verbatim duplication and every
// empty section renders (none).
func BuildSummary(in SummaryInput) string {
	return BuildSummaryResult(in).Text
}

// BuildSummaryResult is BuildSummary plus the truncation/token bookkeeping used
// by the summary_truncated flag and event (R-A5-7).
func BuildSummaryResult(in SummaryInput) SummaryResult {
	sections := buildSummarySections(in)
	text, truncated := renderSummaryWithinBudget(sections, in.summaryMax())
	return SummaryResult{
		Text:             text,
		Truncated:        truncated,
		Tokens:           EstimateSummaryTokens(text),
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
			body := filterStalePriorSummaryLines(strings.TrimSpace(prior[index]), in.Plan)
			if body == "" {
				continue
			}
			sections[index].Body = mergeSummaryBodies(sections[index].Body, body)
		}
	}
	return sections
}

// deriveSummarySections renders the deterministic sections from the input
// material. No section invents content.
func deriveSummarySections(in SummaryInput) []SummarySection {
	sections := make([]SummarySection, len(summarySectionHeadings))
	for index, heading := range summarySectionHeadings {
		sections[index].Heading = heading
	}
	sections[0].Body = summaryGoalBody(in)
	sections[1].Body = summaryConceptsBody(in)
	sections[2].Body = summaryRequirementsBody(in)
	sections[3].Body = summaryFilesBody(in)
	sections[4].Body = summaryErrorsBody(in)
	sections[5].Body = summaryTodoBody(in.Plan)
	sections[6].Body = summaryProgressBody(in)
	sections[7].Body = summaryNextBody(in.Plan)
	sections[8].Body = summaryContextBody(in)
	return sections
}

// summaryDBTruthSection reports whether a section is rebuilt from the DB
// snapshots only (todo/progress/next).
func summaryDBTruthSection(index int) bool {
	return index >= 5 && index <= 7
}

func summaryGoalBody(in SummaryInput) string {
	lines := make([]string, 0, 3)
	if taskType := summaryOneLine(in.Task.Type); taskType != "" {
		lines = append(lines, "task_type: "+taskType)
	}
	if goal := summaryOneLine(in.Task.Goal); goal != "" {
		lines = append(lines, "goal: "+goal)
	}
	if latest := summaryLatestUserIntent(in.Messages); latest != "" {
		lines = append(lines, "latest_user: "+latest)
	}
	return strings.Join(lines, "\n")
}

// summaryLatestUserIntent returns the newest user message, collapsed to one line
// and bounded so a huge instruction cannot dominate the summary.
func summaryLatestUserIntent(messages []Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if !strings.EqualFold(strings.TrimSpace(messages[index].Role), "user") {
			continue
		}
		if content := summaryOneLine(messages[index].Content); content != "" {
			return truncateRunes(content, 200)
		}
	}
	return ""
}

func summaryConceptsBody(in SummaryInput) string {
	lines := make([]string, 0, len(in.Task.Constraints))
	for _, constraint := range in.Task.Constraints {
		if line := summaryOneLine(constraint); line != "" {
			lines = append(lines, "- 约束: "+line)
		}
	}
	return strings.Join(lines, "\n")
}

func summaryRequirementsBody(in SummaryInput) string {
	lines := make([]string, 0, len(in.RequirementIDs)+len(in.CaseIDs)+len(in.Task.AcceptanceCriteria)+len(in.Task.Scope))
	for _, requirementID := range uniqueSummaryIDs(in.RequirementIDs) {
		lines = append(lines, fmt.Sprintf("requirement_id=%d", requirementID))
	}
	for _, caseID := range uniqueSummaryIDs(in.CaseIDs) {
		lines = append(lines, fmt.Sprintf("case_id=%d", caseID))
	}
	for _, criterion := range in.Task.AcceptanceCriteria {
		if line := summaryOneLine(criterion); line != "" {
			lines = append(lines, "acceptance: "+line)
		}
	}
	for _, scope := range in.Task.Scope {
		if line := summaryOneLine(scope); line != "" {
			lines = append(lines, "scope: "+line)
		}
	}
	return summaryBullets(lines)
}

func summaryFilesBody(in SummaryInput) string {
	extracted := summaryExtract(in.Messages)
	lines := make([]string, 0, len(extracted.paths)+len(extracted.commands)+len(extracted.signatures))
	lines = append(lines, extracted.paths...)
	lines = append(lines, extracted.commands...)
	lines = append(lines, extracted.signatures...)
	return summaryBullets(lines)
}

func summaryErrorsBody(in SummaryInput) string {
	return summaryBullets(summaryExtract(in.Messages).errors)
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

// summaryContextBody renders the pinned facts plus the exact identifiers,
// paths, commands, errors, numbers and signatures preserved from the folded
// transcript.
func summaryContextBody(in SummaryInput) string {
	lines := make([]string, 0, maxSummaryContextItems+MaxPinnedFacts)
	for _, fact := range capPinnedFacts(in.PinnedFacts) {
		if line := summaryOneLine(fact); line != "" {
			lines = append(lines, "- pinned_fact: "+line)
		}
	}
	if in.WorkItemID > 0 {
		lines = append(lines, fmt.Sprintf("- work_item: %d", in.WorkItemID))
	}
	extracted := summaryExtract(in.Messages)
	for _, item := range extracted.ids {
		if len(lines) >= maxSummaryContextItems+MaxPinnedFacts {
			break
		}
		lines = append(lines, "- "+item)
	}
	for _, item := range extracted.errors {
		if len(lines) >= maxSummaryContextItems+MaxPinnedFacts {
			break
		}
		lines = append(lines, "- error: "+item)
	}
	for _, item := range extracted.numbers {
		if len(lines) >= maxSummaryContextItems+MaxPinnedFacts {
			break
		}
		lines = append(lines, "- "+item)
	}
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
)

// summaryExtract scans the folded transcript for the exact material the summary
// must preserve: ids, file paths, commands, error strings, numbers/signatures.
// Deduplication is order-preserving so the render is deterministic.
func summaryExtract(messages []Message) summaryExtracted {
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
			add("signature", summarySignaturePattern.FindAllString(line, -1), nil)
			if summaryErrorLinePattern.MatchString(line) {
				add("error", []string{truncateRunes(line, 200)}, nil)
			}
		}
	}
	return extracted
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

func uniqueSummaryIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
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

// filterStalePriorSummaryLines drops prior lines whose plan item is gone from
// the current snapshot or whose status no longer matches the DB. Everything
// else is kept so still-true prior facts survive the merge.
func filterStalePriorSummaryLines(body string, plan []agentcontext.PlanItemSnapshot) string {
	if body == "" || len(plan) == 0 {
		return body
	}
	statuses := make(map[int64]string, len(plan))
	for _, item := range plan {
		statuses[item.ID] = summaryPlanStatus(item.Status)
	}
	lines := make([]string, 0)
	for _, rawLine := range strings.Split(body, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
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
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

var summaryPriorPlanPattern = regexp.MustCompile(`plan_item id=(\d+)(?:\s+status=([^\s]+))?`)

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

// BuildSummaryRequest builds the R-A5-8 summary request: the current
// conversation's stable prefix and tool definitions are reused byte for byte,
// the summary range after the covered watermark is replayed verbatim and the
// compaction instruction is the last user message.
func BuildSummaryRequest(in SummaryInput) llmapp.ChatRequest {
	system := strings.TrimSpace(in.StablePrefix)
	if system == "" {
		system = summaryCompactionSystemPrompt
	}
	messages := make([]llmapp.ChatMessage, 0, len(in.Messages)+2)
	messages = append(messages, llmapp.ChatMessage{Role: "system", Content: system})
	for _, message := range SelectSummaryMessages(in.Messages, in.PreviousCoveredMessageID) {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		messages = append(messages, llmapp.ChatMessage{
			Role:       message.Role,
			Content:    message.Content,
			ToolCallID: message.ToolCallID,
		})
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

// SelectSummaryMessages returns the messages newer than the covered watermark
// (R-A5-12). IDs are monotonic in a conversation, so a summary only folds the
// messages the previous revision did not cover.
func SelectSummaryMessages(messages []Message, coveredMessageID int64) []Message {
	if coveredMessageID <= 0 {
		return messages
	}
	selected := make([]Message, 0, len(messages))
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

// NewCoveredMessageID applies the R-A5-12 monotonic watermark rule. A zero
// candidate means "no new message" and keeps the previous watermark.
func NewCoveredMessageID(previous, candidate int64) (int64, error) {
	if previous < 0 || candidate < 0 {
		return 0, errors.New("agentapp: summary covered_message_id must not be negative")
	}
	if candidate == 0 {
		return previous, nil
	}
	if candidate < previous {
		return 0, fmt.Errorf("%w: previous=%d candidate=%d", ErrSummaryWatermarkStale, previous, candidate)
	}
	return candidate, nil
}

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
// range. Image parts are replaced by the R-A5-5 attachment marker and their
// bytes/data_refs never enter the summary; only text parts are replayed.
func SummaryMessages(messages []ConversationMessage) []Message {
	converted := make([]Message, 0, len(messages))
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
			}
		}
		content := strings.TrimSpace(strings.Join(parts, "\n"))
		if content == "" {
			continue
		}
		converted = append(converted, Message{
			ID:         message.ID,
			Role:       message.Role,
			Content:    content,
			ToolCallID: "",
		})
	}
	return converted
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
func (s *CompactionSummarizer) Summarize(ctx context.Context, in SummaryInput) SummaryResult {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return s.fallback(ctx, in, err)
		}
	}
	if s == nil || s.chat == nil {
		return s.fallback(ctx, in, errors.New("compaction summarizer is not configured"))
	}
	response, err := s.chat.Chat(ctx, BuildSummaryRequest(in))
	if err != nil {
		return s.fallback(ctx, in, err)
	}
	final := in
	final.Draft = strings.TrimSpace(response.Content)
	result := BuildSummaryResult(final)
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
	if result.Truncated {
		s.publishTruncated(ctx, in, result)
	}
	return result
}

// fallback returns the deterministic nine-section summary and records why the
// model path was skipped.
func (s *CompactionSummarizer) fallback(ctx context.Context, in SummaryInput, cause error) SummaryResult {
	result := BuildSummaryResult(in)
	result.UsedFallback = true
	if cause != nil {
		result.FallbackReason = cause.Error()
		s.logWarn(ctx, "context compaction: summary generation failed; using deterministic summary", "error", cause)
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

// NewSummaryLedger returns an empty in-process summary ledger.
func NewSummaryLedger() *SummaryLedger {
	return &SummaryLedger{now: time.Now, latest: make(map[int64]SummaryRecord)}
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

// Commit renders the persistence commit of one summary result.
func (r SummaryResult) Commit(in SummaryInput) SummaryCommit {
	meta := map[string]any{
		"trigger":        string(in.Trigger),
		"summary_tokens": r.Tokens,
		"summary_max":    in.summaryMax(),
		"truncated":      r.Truncated,
		"cached_tokens":  r.CachedTokens,
		"used_fallback":  r.UsedFallback,
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
		PinnedFacts:      append([]string(nil), in.PinnedFacts...),
		CoveredMessageID: r.CoveredMessageID,
		SummaryMeta:      meta,
		TokenEstimate:    r.Tokens,
	}
}
