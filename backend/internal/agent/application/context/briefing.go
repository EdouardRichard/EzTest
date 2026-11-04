// Package agentcontext derives the version briefing (spec §6.2) appended to the
// agent context every round. The briefing is never persisted: it is rebuilt
// from a narrow read port and rendered deterministically, so the same data
// always produces byte-identical text. The cache-first assembler, boundary
// compaction and context_packet persistence live in the follow-up P3 tasks.
package agentcontext

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// BriefingTokenBudget is the total render envelope from spec R-A8-5 (≤3k
// tokens). Render enforces it in two passes: it first cuts every regular group
// to its own section budget, then, when the whole text is still over the
// envelope, it squeezes the six regular groups deterministically until the
// render fits. The goal/stage head and the open questions are protected and are
// never truncated. The mandatory prefixes of the plan and findings groups (the
// current/next plan items and the top-3 facts, spec R-A8-5) are protected too:
// a section budget may not cut them, and the total-budget pass reserves their
// runes before sharing the rest. The only render allowed to exceed the envelope
// is the documented exception where the protected content plus the fixed
// headings and the one-rune truncation marker every regular group needs already
// exceed it; that render is returned unchanged.
const BriefingTokenBudget = 3000

// briefingBudgetRunes is the largest rune count whose conservative estimate
// still fits BriefingTokenBudget: the estimator is ceil(0.6×runes), so
// ceil(0.6 × 3000×5/3) = 3000.
const briefingBudgetRunes = BriefingTokenBudget * 5 / 3

// Stable section names. They are the machine identity of a briefing group and
// stay fixed across renders.
const (
	SectionRequirements  = "requirements"
	SectionCases         = "cases"
	SectionPlan          = "plan"
	SectionFindings      = "findings"
	SectionExecution     = "execution"
	SectionKnowledge     = "knowledge"
	SectionOpenQuestions = "open_questions"
)

const (
	// Per-section content budgets from spec R-A8-5. The goal/stage head and the
	// open-questions group are intentionally exempt: they are never truncated.
	briefingRequirementsBudget = 600
	briefingCasesBudget        = 300
	briefingPlanBudget         = 600
	briefingFindingsBudget     = 800
	briefingExecutionBudget    = 200
	briefingKnowledgeBudget    = 200

	// maxPlanItems is the plan cap from spec §6.2 (open items ≤5). The current
	// and next items count toward it but are always rendered. maxFindingItems
	// is the findings top-K bound; maxMandatoryFindings is the R-A8-5 top-3
	// floor that no truncation may cut. maxKnowledgeItems is the knowledge
	// top-K bound.
	maxPlanItems         = 5
	maxFindingItems      = 5
	maxMandatoryFindings = 3
	maxKnowledgeItems    = 5

	planCurrentPrefix = "当前："
	planNextPrefix    = "下一步："

	noneText         = "无"
	truncationMarker = "…"
	unknownText      = "未知"
)

const (
	sectionTitleGoal          = "目标/阶段"
	sectionTitleRequirements  = "需求"
	sectionTitleCases         = "用例"
	sectionTitlePlan          = "计划"
	sectionTitleFindings      = "发现"
	sectionTitleExecution     = "执行"
	sectionTitleKnowledge     = "知识"
	sectionTitleOpenQuestions = "未决"
)

// Section is one ordered block of a briefing group. Name is a stable machine
// key, Title is the rendered sub-heading and Content is the already-rendered
// body. A group with several sections renders each of them in slice order.
type Section struct {
	Name    string
	Title   string
	Content string
}

// Briefing is the derived version briefing (spec §6.2). Goal and Stage form the
// always-present head; every other field is one group in the fixed render
// order. The type is never persisted and carries no timestamps, so equal data
// always renders to equal bytes.
type Briefing struct {
	Goal          string
	Stage         string
	Requirements  []Section
	Cases         []Section
	Plan          []Section
	Findings      []Section
	Execution     []Section
	Knowledge     []Section
	OpenQuestions []Section

	// mandatoryPlan and mandatoryFindings are the protected prefixes of the
	// plan and findings groups (spec R-A8-5): the current and next plan items
	// and the top-3 facts. They are derived from the same source data as the
	// section content, are always a prefix of it, and are never persisted.
	// Render keeps them even when a section or the whole briefing is over
	// budget. Both are empty for a zero Briefing and for groups without data.
	mandatoryPlan     string
	mandatoryFindings string
}

// BriefingBuilder builds version briefings from a Reader.
type BriefingBuilder struct {
	reader Reader
}

// NewBriefingBuilder wires the briefing builder to its read port.
func NewBriefingBuilder(reader Reader) *BriefingBuilder {
	return &BriefingBuilder{reader: reader}
}

// BuildBriefing derives the briefing of one version. It validates the version
// ID, loads the source through the read port and returns a fully populated
// Briefing: every group is present, empty groups carry noneText, and no slice
// is nil, so callers can render without defensive checks.
func (b *BriefingBuilder) BuildBriefing(ctx context.Context, versionID int64) (Briefing, error) {
	if versionID <= 0 {
		return Briefing{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if b == nil || b.reader == nil {
		return Briefing{}, errors.New("briefing: read port is not configured")
	}
	source, err := b.reader.Load(ctx, versionID)
	if err != nil {
		return Briefing{}, fmt.Errorf("load briefing source for version %d: %w", versionID, err)
	}
	return newBriefing(source), nil
}

// newBriefing is the pure derivation from the read model to the briefing. It
// never fails: malformed source entries are dropped or rendered as noneText
// instead of failing the context round.
func newBriefing(source Source) Briefing {
	stage := strings.TrimSpace(source.Version.Stage)
	if blocker := oneLine(source.Version.Blocker); blocker != "" {
		if stage == "" {
			stage = unknownText
		}
		stage += "（阻塞：" + blocker + "）"
	}
	return Briefing{
		Goal:              oneLine(source.Version.Goal),
		Stage:             stage,
		Requirements:      briefingSection(SectionRequirements, sectionTitleRequirements, requirementsBody(source.Requirements)),
		Cases:             briefingSection(SectionCases, sectionTitleCases, casesBody(source.Cases)),
		Plan:              briefingSection(SectionPlan, sectionTitlePlan, planBody(source.Plan)),
		Findings:          briefingSection(SectionFindings, sectionTitleFindings, findingsBody(source.Findings)),
		Execution:         briefingSection(SectionExecution, sectionTitleExecution, executionBody(source.Execution)),
		Knowledge:         briefingSection(SectionKnowledge, sectionTitleKnowledge, knowledgeBody(source.Knowledge)),
		OpenQuestions:     briefingSection(SectionOpenQuestions, sectionTitleOpenQuestions, openQuestionsBody(source.OpenQuestions)),
		mandatoryPlan:     planMandatoryBody(source.Plan),
		mandatoryFindings: findingsMandatoryBody(source.Findings),
	}
}

// briefingSection returns the single section of one group, defaulting an empty
// body to noneText so "no data" is visible to the model instead of omitted.
func briefingSection(name, title, body string) []Section {
	if strings.TrimSpace(body) == "" {
		body = noneText
	}
	return []Section{{Name: name, Title: title, Content: body}}
}

// Render formats the briefing deterministically. The section order is fixed
// (head, requirements, cases, plan, findings, execution, knowledge, open
// questions). Over-budget regular sections are truncated independently but
// never below their mandatory prefix; the goal/stage head and the open
// questions are never truncated. When the independently truncated text still
// exceeds BriefingTokenBudget, the optional tails of the six regular groups are
// squeezed proportionally until the total fits, while the mandatory prefixes
// stay. Only the documented protected-content exception can leave the render
// above the envelope.
func (b Briefing) Render() string {
	groups := briefingGroups(b)
	head := renderHead(b)

	bodies := make([]string, len(groups))
	protectedRunes := make([]int, len(groups))
	for index, group := range groups {
		body := renderSectionBody(group.sections)
		if strings.TrimSpace(body) == "" {
			body = noneText
		}
		protectedRunes[index] = protectedPrefixRunes(body, group.mandatory)
		if group.budget > 0 {
			body = truncateSectionBody(body, group.budget, protectedRunes[index])
		}
		bodies[index] = body
	}

	rendered := assembleBriefing(head, groups, bodies)
	if estimateBriefingTokens(rendered) <= BriefingTokenBudget {
		return rendered
	}
	return squeezeBriefing(head, groups, bodies, protectedRunes, rendered)
}

// squeezeBriefing is the total-budget pass. The last group (open questions) and
// the mandatory prefixes are protected; everything else may shrink. It returns
// the per-section-truncated render unchanged in the documented exception where
// the protected content plus the fixed headings and one marker per regular
// group already exceed the envelope.
func squeezeBriefing(head string, groups []briefingGroup, bodies []string, protectedRunes []int, rendered string) string {
	regularGroups := len(groups) - 1
	regularRunes := 0
	protectedTotal := 0
	optionalGroups := 0
	for index := 0; index < regularGroups; index++ {
		bodyRunes := utf8.RuneCountInString(bodies[index])
		regularRunes += bodyRunes
		protectedTotal += protectedRunes[index]
		if bodyRunes > protectedRunes[index] {
			optionalGroups++
		}
	}
	fixedRunes := utf8.RuneCountInString(rendered) - regularRunes
	available := briefingBudgetRunes - fixedRunes - protectedTotal - optionalGroups
	if available < 0 {
		return rendered
	}
	allocations := optionalSectionAllocations(groups[:regularGroups], bodies[:regularGroups], protectedRunes[:regularGroups], available)
	for index := 0; index < regularGroups; index++ {
		bodyRunes := utf8.RuneCountInString(bodies[index])
		if bodyRunes <= protectedRunes[index] {
			continue
		}
		bodies[index] = truncateToRunes(bodies[index], protectedRunes[index]+1+allocations[index])
	}
	return assembleBriefing(head, groups, bodies)
}

// protectedPrefixRunes returns the rune length of the protected prefix of one
// group body. A mandatory prefix that is not a literal prefix of the body is
// treated as absent, so a hand-built Briefing without derived mandatory
// content renders exactly as before.
func protectedPrefixRunes(body, mandatory string) int {
	if mandatory == "" || !strings.HasPrefix(body, mandatory) {
		return 0
	}
	return utf8.RuneCountInString(mandatory)
}

// truncateSectionBody cuts one group body to its section budget without ever
// cutting its protected prefix. A group whose mandatory prefix alone exceeds
// the budget keeps the whole prefix and adds the truncation marker only when
// there is optional content to drop, so current/next and the top-3 facts stay
// visible even when the section is over budget (spec R-A8-5).
func truncateSectionBody(body string, budget, protectedRunes int) string {
	limit := budget * 5 / 3
	bodyRunes := utf8.RuneCountInString(body)
	if bodyRunes <= limit {
		return body
	}
	minimum := protectedRunes
	if bodyRunes > protectedRunes {
		minimum = protectedRunes + 1
	}
	if limit < minimum {
		limit = minimum
	}
	return truncateToRunes(body, limit)
}

// optionalSectionAllocations distributes the rune allowance left after the
// protected content over the optional tails of the regular groups. Each group
// with an optional tail first reserves one rune for its truncation marker; the
// rest is shared proportionally to the group's section budget, so a group with
// a larger spec budget keeps a larger share, and a tail never exceeds the room
// its own section budget leaves after the protected prefix. Integer arithmetic
// and a fixed remainder order make the split deterministic.
func optionalSectionAllocations(groups []briefingGroup, bodies []string, protectedRunes []int, availableRunes int) []int {
	allocations := make([]int, len(groups))
	if len(groups) == 0 {
		return allocations
	}
	weights := make([]int, len(groups))
	maxOptional := make([]int, len(groups))
	weightTotal := 0
	for index, group := range groups {
		optional := utf8.RuneCountInString(bodies[index]) - protectedRunes[index]
		if optional <= 0 {
			continue
		}
		weight := group.budget * 5 / 3
		if weight < 2 {
			weight = 2
		}
		weights[index] = weight
		weightTotal += weight
		budgetRoom := weight - protectedRunes[index] - 1
		if budgetRoom < 0 {
			budgetRoom = 0
		}
		maxOptional[index] = optional - 1
		if maxOptional[index] > budgetRoom {
			maxOptional[index] = budgetRoom
		}
	}
	if weightTotal == 0 {
		return allocations
	}
	remaining := availableRunes
	if remaining < 0 {
		remaining = 0
	}
	assigned := 0
	for index := range groups {
		if weights[index] == 0 {
			continue
		}
		share := remaining * weights[index] / weightTotal
		if share > maxOptional[index] {
			share = maxOptional[index]
		}
		allocations[index] = share
		assigned += share
	}
	for assigned < remaining {
		progressed := false
		for index := range groups {
			if assigned >= remaining {
				break
			}
			if weights[index] == 0 || allocations[index] >= maxOptional[index] {
				continue
			}
			allocations[index]++
			assigned++
			progressed = true
		}
		if !progressed {
			break
		}
	}
	return allocations
}

// briefingGroup couples one rendered group with its content budget and its
// mandatory prefix. A zero budget means the group is protected and never
// truncated; an empty mandatory prefix means the group has no protected part.
type briefingGroup struct {
	title     string
	sections  []Section
	budget    int
	mandatory string
}

// briefingGroups returns the groups in the fixed spec §6.2 order. It allocates a
// fresh slice every render; the values themselves are immutable.
func briefingGroups(b Briefing) []briefingGroup {
	return []briefingGroup{
		{title: sectionTitleRequirements, sections: b.Requirements, budget: briefingRequirementsBudget},
		{title: sectionTitleCases, sections: b.Cases, budget: briefingCasesBudget},
		{title: sectionTitlePlan, sections: b.Plan, budget: briefingPlanBudget, mandatory: b.mandatoryPlan},
		{title: sectionTitleFindings, sections: b.Findings, budget: briefingFindingsBudget, mandatory: b.mandatoryFindings},
		{title: sectionTitleExecution, sections: b.Execution, budget: briefingExecutionBudget},
		{title: sectionTitleKnowledge, sections: b.Knowledge, budget: briefingKnowledgeBudget},
		{title: sectionTitleOpenQuestions, sections: b.OpenQuestions},
	}
}

// renderHead renders the always-present goal/stage head. It is never truncated.
func renderHead(b Briefing) string {
	var out strings.Builder
	out.WriteString("## ")
	out.WriteString(sectionTitleGoal)
	out.WriteString("\n目标：")
	out.WriteString(orNone(b.Goal))
	out.WriteString("\n阶段：")
	out.WriteString(orNone(b.Stage))
	return out.String()
}

// assembleBriefing concatenates the head and the groups in fixed order. Every
// body is already non-empty; the callers substitute noneText when a group has no
// content.
func assembleBriefing(head string, groups []briefingGroup, bodies []string) string {
	var out strings.Builder
	out.WriteString(head)
	for index, group := range groups {
		out.WriteString("\n\n## ")
		out.WriteString(group.title)
		out.WriteString("\n")
		out.WriteString(bodies[index])
	}
	return out.String()
}

// renderSectionBody joins the non-empty sections of one group in slice order.
// When a group has more than one non-empty section, its title prefixes the body
// as a sub-heading; the single-section case (the normal briefing) stays clean.
func renderSectionBody(sections []Section) string {
	nonEmpty := 0
	for _, section := range sections {
		if strings.TrimSpace(section.Content) != "" {
			nonEmpty++
		}
	}
	parts := make([]string, 0, nonEmpty)
	for _, section := range sections {
		content := strings.TrimSpace(section.Content)
		if content == "" {
			continue
		}
		if nonEmpty > 1 {
			if title := strings.TrimSpace(section.Title); title != "" {
				content = "### " + title + "\n" + content
			}
		}
		parts = append(parts, content)
	}
	return strings.Join(parts, "\n\n")
}

// truncateToRunes cuts text to at most maxRunes runes while staying UTF-8 safe,
// reserving the ellipsis inside the limit so a truncated body never exceeds it.
// Callers only pass positive limits; a zero limit yields an empty body.
func truncateToRunes(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	if maxRunes == 1 {
		return truncationMarker
	}
	prefix := strings.TrimRight(string(runes[:maxRunes-1]), " \t\r\n")
	if prefix == "" {
		return truncationMarker
	}
	return prefix + truncationMarker
}

// estimateBriefingTokens is the conservative briefing estimator from spec §6.3:
// at least 0.6 tokens per rune, rounded up. It over-estimates real model tokens
// (English text is normally ~4 runes per token) and keeps the briefing safe.
func estimateBriefingTokens(text string) int {
	return (utf8.RuneCountInString(text)*3 + 4) / 5
}

func orNone(text string) string {
	if text = strings.TrimSpace(text); text == "" {
		return noneText
	}
	return text
}

// oneLine trims and collapses all whitespace so a value sourced from the
// database cannot break the single-line bullet layout.
func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func bulletLines(values []string) []string {
	lines := make([]string, 0, len(values))
	for _, value := range values {
		if line := oneLine(value); line != "" {
			lines = append(lines, "- "+line)
		}
	}
	return lines
}

func requirementsBody(in RequirementsSnapshot) string {
	confirmed := requirementBullets(in.Confirmed)
	gaps := bulletLines(in.Gaps)
	if len(confirmed) == 0 && len(gaps) == 0 {
		return ""
	}
	parts := make([]string, 0, 2)
	if len(confirmed) > 0 {
		parts = append(parts, fmt.Sprintf("已确认需求（%d）：\n%s", len(confirmed), strings.Join(confirmed, "\n")))
	}
	if len(gaps) > 0 {
		parts = append(parts, fmt.Sprintf("未决缺口（%d）：\n%s", len(gaps), strings.Join(gaps, "\n")))
	}
	return strings.Join(parts, "\n")
}

func requirementBullets(requirements []RequirementSnapshot) []string {
	lines := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		title := oneLine(requirement.Title)
		acceptance := oneLine(requirement.Acceptance)
		switch {
		case title == "" && acceptance == "":
			continue
		case title == "":
			lines = append(lines, "- 验收："+acceptance)
		case acceptance == "":
			lines = append(lines, "- "+title)
		default:
			lines = append(lines, "- "+title+"（验收："+acceptance+"）")
		}
	}
	return lines
}

func casesBody(in CasesSnapshot) string {
	draft := nonNegative(in.Draft)
	confirmed := nonNegative(in.Confirmed)
	stale := nonNegative(in.Stale)
	gaps := bulletLines(in.CoverageGaps)
	if draft == 0 && confirmed == 0 && stale == 0 && len(gaps) == 0 {
		return ""
	}
	parts := make([]string, 0, 2)
	if draft != 0 || confirmed != 0 || stale != 0 {
		parts = append(parts, fmt.Sprintf("草稿 %d / 已确认 %d / 失效 %d", draft, confirmed, stale))
	}
	if len(gaps) > 0 {
		parts = append(parts, fmt.Sprintf("覆盖缺口（%d）：\n%s", len(gaps), strings.Join(gaps, "\n")))
	}
	return strings.Join(parts, "\n")
}

// planTerminalStatuses are the statuses that mean a plan item no longer needs
// work (spec R-A8-5). Current and next are always chosen among the other items.
var planTerminalStatuses = map[string]bool{
	"done":      true,
	"completed": true,
	"skipped":   true,
	"cancelled": true,
	"canceled":  true,
}

// planItemView is one renderable plan item: its normalized status and its
// already-rendered label.
type planItemView struct {
	status string
	label  string
}

// planItemTerminal reports whether a plan status is terminal. An empty or
// unknown status counts as open, so a malformed row still shows up as work.
func planItemTerminal(status string) bool {
	return planTerminalStatuses[strings.ToLower(oneLine(status))]
}

// planViews returns the renderable plan items in plan order. Items without a
// title are dropped exactly as before: there is nothing to show for them.
func planViews(items []PlanItemSnapshot) []planItemView {
	views := make([]planItemView, 0, len(items))
	for _, item := range items {
		if oneLine(item.Title) == "" {
			continue
		}
		views = append(views, planItemView{status: oneLine(item.Status), label: planItemLabel(item)})
	}
	return views
}

// planItemLabel renders one plan item without its bullet: the status, the title
// and the dependency reference.
func planItemLabel(item PlanItemSnapshot) string {
	label := ""
	if status := oneLine(item.Status); status != "" {
		label = "[" + status + "] "
	}
	label += oneLine(item.Title)
	if dependencies := dependencyText(item.DependsOn); dependencies != "" {
		label += "（依赖：" + dependencies + "）"
	}
	return label
}

// planCurrentAndNext returns the indexes of the current and next plan items
// (spec R-A8-5): current is the first non-terminal item in plan order and next
// is the following non-terminal item. Either index is -1 when it does not
// exist; no item is fabricated.
func planCurrentAndNext(views []planItemView) (current, next int) {
	current, next = -1, -1
	for index := range views {
		if planItemTerminal(views[index].status) {
			continue
		}
		if current < 0 {
			current = index
			continue
		}
		next = index
		break
	}
	return current, next
}

// planMandatoryLines returns the always-visible plan prefix with explicit
// labels and the indexes it covers.
func planMandatoryLines(views []planItemView) (lines []string, current, next int) {
	current, next = planCurrentAndNext(views)
	if current >= 0 {
		lines = append(lines, planCurrentPrefix+views[current].label)
	}
	if next >= 0 {
		lines = append(lines, planNextPrefix+views[next].label)
	}
	return lines, current, next
}

// planBody renders the plan group plus its cap marker. The current and next
// items come first with explicit labels and count toward maxPlanItems; the
// remaining slots show the upcoming open items in plan order, then the finished
// ones, so the model keeps seeing history when there is room.
func planBody(items []PlanItemSnapshot) string {
	views := planViews(items)
	if len(views) == 0 {
		return ""
	}
	mandatory, current, next := planMandatoryLines(views)
	lines := append(make([]string, 0, maxPlanItems), mandatory...)
	shown := make([]bool, len(views))
	if current >= 0 {
		shown[current] = true
	}
	if next >= 0 {
		shown[next] = true
	}
	for _, terminal := range []bool{false, true} {
		for index := range views {
			if len(lines) == maxPlanItems {
				break
			}
			if shown[index] || planItemTerminal(views[index].status) != terminal {
				continue
			}
			lines = append(lines, "- "+views[index].label)
			shown[index] = true
		}
	}
	shownCount := 0
	for _, visible := range shown {
		if visible {
			shownCount++
		}
	}
	if remaining := len(views) - shownCount; remaining > 0 {
		lines = append(lines, fmt.Sprintf("（另有 %d 项未列出）", remaining))
	}
	return strings.Join(lines, "\n")
}

// planMandatoryBody returns the protected plan prefix (spec R-A8-5): the
// current item and the following non-terminal item, with explicit labels.
func planMandatoryBody(items []PlanItemSnapshot) string {
	views := planViews(items)
	if len(views) == 0 {
		return ""
	}
	lines, _, _ := planMandatoryLines(views)
	return strings.Join(lines, "\n")
}

func dependencyText(values []string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if line := oneLine(value); line != "" {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, "、")
}

// rankedFacts returns a deterministic top-first copy of the findings, ordered
// by factLess so the same source data always yields the same selection.
func rankedFacts(items []FactSnapshot) []FactSnapshot {
	ranked := append([]FactSnapshot(nil), items...)
	sort.Slice(ranked, func(left, right int) bool {
		return factLess(ranked[left], ranked[right])
	})
	return ranked
}

func findingsBody(items []FactSnapshot) string {
	lines := make([]string, 0, maxFindingItems)
	for _, fact := range rankedFacts(items) {
		if len(lines) == maxFindingItems {
			break
		}
		if line := factLine(fact); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// findingsMandatoryBody returns the protected findings prefix (spec R-A8-5):
// the top-3 ranked facts. It is always a prefix of findingsBody.
func findingsMandatoryBody(items []FactSnapshot) string {
	lines := make([]string, 0, maxMandatoryFindings)
	for _, fact := range rankedFacts(items) {
		if len(lines) == maxMandatoryFindings {
			break
		}
		if line := factLine(fact); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func factLine(fact FactSnapshot) string {
	kind := oneLine(fact.Kind)
	key := oneLine(fact.Key)
	summary := oneLine(fact.Summary)
	label := ""
	switch {
	case kind != "" && key != "":
		label = "[" + kind + "] " + key
	case kind != "":
		label = "[" + kind + "]"
	case key != "":
		label = key
	}
	switch {
	case label != "" && summary != "":
		label += "：" + summary
	case label == "" && summary != "":
		label = summary
	case label == "":
		return ""
	}
	if confidence := formatConfidence(fact.Confidence); confidence != "" {
		label += "（置信 " + confidence + "）"
	}
	return "- " + label
}

// factLess ranks findings for the top-K selection: highest confidence first
// (missing confidence last), then most recent, then stable identity fields so
// the result never depends on the row order returned by the read port.
func factLess(left, right FactSnapshot) bool {
	if compared := compareConfidence(left.Confidence, right.Confidence); compared != 0 {
		return compared < 0
	}
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.After(right.CreatedAt)
	}
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	if left.Key != right.Key {
		return left.Key < right.Key
	}
	if left.Summary != right.Summary {
		return left.Summary < right.Summary
	}
	return left.ID < right.ID
}

func executionBody(in ExecutionSnapshot) string {
	status := oneLine(in.WaveStatus)
	blocker := oneLine(in.Blocker)
	passed := nonNegative(in.Passed)
	failed := nonNegative(in.Failed)
	blocked := nonNegative(in.Blocked)
	skipped := nonNegative(in.Skipped)
	if status == "" && blocker == "" && passed+failed+blocked+skipped == 0 {
		return ""
	}
	if status == "" {
		status = unknownText
	}
	body := fmt.Sprintf("最近 wave：%s（通过 %d / 失败 %d / 阻塞 %d / 跳过 %d）", status, passed, failed, blocked, skipped)
	if blocker != "" {
		body += "\n阻塞：" + blocker
	}
	return body
}

// knowledgeApprovedStatus is the only knowledge status eligible for injection
// (spec §10.1). A snapshot without a status is treated as approved for
// compatibility with read ports that only return injectable rows.
const knowledgeApprovedStatus = "approved"

// knowledgeBody renders the injectable knowledge top-K. Items that are not
// approved or whose source version is gone (SourceStale) are never injected;
// the version briefing is read-only diagnosis input, not a revocation bypass.
func knowledgeBody(items []KnowledgeSnapshot) string {
	injectable := make([]KnowledgeSnapshot, 0, len(items))
	for _, item := range items {
		if item.SourceStale {
			continue
		}
		if item.Status != "" && item.Status != knowledgeApprovedStatus {
			continue
		}
		injectable = append(injectable, item)
	}
	ranked := injectable
	sort.Slice(ranked, func(left, right int) bool {
		if compared := compareConfidence(ranked[left].Confidence, ranked[right].Confidence); compared != 0 {
			return compared < 0
		}
		if ranked[left].Title != ranked[right].Title {
			return ranked[left].Title < ranked[right].Title
		}
		if ranked[left].Summary != ranked[right].Summary {
			return ranked[left].Summary < ranked[right].Summary
		}
		return ranked[left].ID < ranked[right].ID
	})
	lines := make([]string, 0, maxKnowledgeItems)
	for _, item := range ranked {
		if len(lines) == maxKnowledgeItems {
			break
		}
		title := oneLine(item.Title)
		summary := oneLine(item.Summary)
		label := ""
		switch {
		case title != "" && summary != "":
			label = title + "：" + summary
		case title != "":
			label = title
		case summary != "":
			label = summary
		default:
			continue
		}
		if confidence := formatConfidence(item.Confidence); confidence != "" {
			label += "（置信 " + confidence + "）"
		}
		lines = append(lines, "- "+label)
	}
	return strings.Join(lines, "\n")
}

// compareConfidence ranks higher confidence first and missing confidence last.
func compareConfidence(left, right *float64) int {
	switch {
	case left == nil && right == nil:
		return 0
	case left == nil:
		return 1
	case right == nil:
		return -1
	case *left > *right:
		return -1
	case *left < *right:
		return 1
	default:
		return 0
	}
}

func formatConfidence(confidence *float64) string {
	if confidence == nil {
		return ""
	}
	return strconv.FormatFloat(*confidence, 'f', 2, 64)
}

func openQuestionsBody(in OpenQuestionsSnapshot) string {
	questions := bulletLines(in.Questions)
	humanRequests := bulletLines(in.HumanRequests)
	if len(questions) == 0 && len(humanRequests) == 0 {
		return ""
	}
	parts := make([]string, 0, 2)
	if len(questions) > 0 {
		parts = append(parts, "问题：\n"+strings.Join(questions, "\n"))
	}
	if len(humanRequests) > 0 {
		parts = append(parts, "人工请求：\n"+strings.Join(humanRequests, "\n"))
	}
	return strings.Join(parts, "\n")
}
