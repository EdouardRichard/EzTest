package deliveryapp

// Deterministic-first report narrative (PO E2E Report B, task 108).
//
// The report writer model only proposes narrative text; the deterministic
// snapshot is the single source of truth for counts, coverage and case results.
// A model can therefore contradict its own data (Report B claimed FR-011/FR-014
// had "no case version obtained a valid execution result" while both cases
// passed, and invented coverage counts 2/11 and 7 not-run gaps). The guard in
// this file is the server-side authority:
//
//   - report.narrative_propose rejects a candidate that contradicts the
//     snapshot, so the writer can regenerate from report.data_read facts;
//   - ReportService.Publish replaces a still-contradictory candidate with a
//     deterministic narrative, so a published report never tells a result story
//     that conflicts with its numbered results/coverage sections.
//
// The checks are deliberately limited to claims the snapshot can verify
// deterministically: the canonical coverage fractions, the execution_not_run /
// execution_failed_coverage gap counts and absolute "no valid execution" claims
// for requirements whose case runs passed. Truthful text about uncovered
// requirements stays untouched.

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// ErrReportNarrativeContradictsResults reports a model-proposed narrative that
// contradicts the deterministic report snapshot. The snapshot wins: the
// proposal is rejected and the publisher falls back to deterministic text.
var ErrReportNarrativeContradictsResults = errors.New("delivery: report narrative contradicts deterministic results")

const (
	// reportNarrativeFallbackNotice heads the server-generated replacement text
	// so a reader can never mistake it for an AI analysis that was accepted.
	reportNarrativeFallbackNotice = "（AI 候选叙述与确定性结果矛盾，已由服务端替换为确定性摘要；以下统计与用例结果来自报告快照。）"
	// The deterministic fallback is bounded: reports may carry thousands of
	// runs, but the narrative only needs enough rows to state the result story.
	reportNarrativeMaxCaseRunLines     = 40
	reportNarrativeMaxRequirementLines = 40
)

var (
	reportCaseVersionCoverageClaim = regexp.MustCompile(`用例版本覆盖\s*[:：]\s*(\d+)\s*/\s*(\d+)`)
	reportRequirementCoverageClaim = regexp.MustCompile(`需求覆盖\s*[:：]\s*(\d+)\s*/\s*(\d+)`)
	reportNotRunGapClaim           = regexp.MustCompile(`未覆盖\s*(\d+)\s*个用例版本[^\n。；]{0,24}execution_not_run`)
	reportFailedCoverageGapClaim   = regexp.MustCompile(`未有效覆盖\s*(\d+)\s*个用例版本[^\n。；]{0,24}execution_failed_coverage`)
	reportRequirementCode          = regexp.MustCompile(`(?i)FR[\s_\-]*0*(\d+)`)
	reportNarrativeSentenceSplit   = regexp.MustCompile(`[\n。！？]+`)
)

// reportNoValidExecutionPhrases are the absolute/scoped ways a model dismisses
// deterministic case results. Scoped occurrences are checked against the
// requirement's passed runs; unscoped ones against the coverage snapshot.
var reportNoValidExecutionPhrases = []string{
	"没有任何用例版本获得有效执行结果",
	"没有任何用例版本获得有效执行",
	"所有用例版本均未获得有效执行",
	"没有任何有效执行结果",
	"没有任何有效执行",
	"无任何有效执行",
	"未获得有效执行",
	"未形成有效执行结果",
	"无有效执行结果",
	"没有有效执行结果",
	"无有效执行",
}

// reportAbsoluteNoExecutionPhrases mark a claim that covers every case version
// (no requirement code in the sentence), not one requirement.
var reportAbsoluteNoExecutionPhrases = []string{"没有任何用例版本", "所有用例版本均未"}

// reportNegationMarkers prevent instructions such as "不得表述为无有效执行"
// from being mistaken for the claim itself.
var reportNegationMarkers = []string{"不得", "不应", "不是", "并非", "而非", "避免", "不能", "不存在"}

// validateReportNarrative returns ErrReportNarrativeContradictsResults when the
// narrative states a deterministic fact (coverage count, gap count, executed
// requirement) that the snapshot disproves. An empty narrative is consistent by
// construction: the renderer omits the candidate section entirely.
func validateReportNarrative(snapshot delivery.ReportSnapshot, narrative string) error {
	if strings.TrimSpace(narrative) == "" {
		return nil
	}
	for _, match := range reportCaseVersionCoverageClaim.FindAllStringSubmatch(narrative, -1) {
		covered, total := reportAtoi(match[1]), reportAtoi(match[2])
		if covered != snapshot.Coverage.CaseVersionsCovered || total != snapshot.Coverage.CaseVersionsTotal {
			return fmt.Errorf("%w: 用例版本覆盖 %d / %d，确定性覆盖为 %d / %d",
				ErrReportNarrativeContradictsResults, covered, total,
				snapshot.Coverage.CaseVersionsCovered, snapshot.Coverage.CaseVersionsTotal)
		}
	}
	for _, match := range reportRequirementCoverageClaim.FindAllStringSubmatch(narrative, -1) {
		covered, total := reportAtoi(match[1]), reportAtoi(match[2])
		if covered != snapshot.Coverage.RequirementsCovered || total != snapshot.Coverage.RequirementsTotal {
			return fmt.Errorf("%w: 需求覆盖 %d / %d，确定性覆盖为 %d / %d",
				ErrReportNarrativeContradictsResults, covered, total,
				snapshot.Coverage.RequirementsCovered, snapshot.Coverage.RequirementsTotal)
		}
	}
	notRun, failedCoverage := reportCoverageGapCounts(snapshot.Coverage.Gaps)
	for _, match := range reportNotRunGapClaim.FindAllStringSubmatch(narrative, -1) {
		if claimed := reportAtoi(match[1]); claimed != notRun {
			return fmt.Errorf("%w: 未覆盖 %d 个用例版本（execution_not_run），确定性缺口为 %d 个",
				ErrReportNarrativeContradictsResults, claimed, notRun)
		}
	}
	for _, match := range reportFailedCoverageGapClaim.FindAllStringSubmatch(narrative, -1) {
		if claimed := reportAtoi(match[1]); claimed != failedCoverage {
			return fmt.Errorf("%w: 未有效覆盖 %d 个用例版本（execution_failed_coverage），确定性缺口为 %d 个",
				ErrReportNarrativeContradictsResults, claimed, failedCoverage)
		}
	}
	for _, sentence := range reportNarrativeSentenceSplit.Split(narrative, -1) {
		sentence = strings.TrimSpace(sentence)
		if sentence == "" || !reportClaimsNoValidExecution(sentence) {
			continue
		}
		codes := reportRequirementCodeTokens(sentence)
		if len(codes) == 0 {
			if reportClaimsAbsoluteNoExecution(sentence) && snapshot.Coverage.CaseVersionsCovered > 0 {
				return fmt.Errorf("%w: 已有 %d 个用例版本获得 passed 结果，不得表述为没有任何用例版本获得有效执行",
					ErrReportNarrativeContradictsResults, snapshot.Coverage.CaseVersionsCovered)
			}
			continue
		}
		for _, code := range codes {
			if reportRequirementHasPassingResult(snapshot, code) {
				return fmt.Errorf("%w: %s 已有 passed 用例结果，不得表述为无有效执行",
					ErrReportNarrativeContradictsResults, code)
			}
		}
	}
	return nil
}

// reportNarrativeForSnapshot is the publish-time guard: a contradictory
// candidate is replaced by deterministic text instead of being rendered.
func reportNarrativeForSnapshot(snapshot delivery.ReportSnapshot, narrative string) string {
	if err := validateReportNarrative(snapshot, narrative); err != nil {
		return deterministicReportNarrative(snapshot)
	}
	return narrative
}

// deterministicReportNarrative renders the authoritative result story from the
// snapshot. Every number here is computed from the same snapshot the report
// sections render, so the replacement can never contradict them.
func deterministicReportNarrative(snapshot delivery.ReportSnapshot) string {
	var builder strings.Builder
	builder.WriteString(reportNarrativeFallbackNotice)
	builder.WriteString("\n\n## 确定性执行结果（服务端生成）\n")

	statusCounts := make(map[string]int, len(snapshot.CaseRuns))
	other := 0
	for _, run := range snapshot.CaseRuns {
		statusCounts[run.Status]++
		switch run.Status {
		case "passed", "failed", "blocked", "inconclusive", "skipped":
		default:
			other++
		}
	}
	fmt.Fprintf(&builder, "- 执行总数 %d：通过（passed）%d、失败（failed）%d、受阻（blocked）%d、不确定（inconclusive）%d、跳过（skipped）%d、其他 %d。\n",
		len(snapshot.CaseRuns), statusCounts["passed"], statusCounts["failed"], statusCounts["blocked"],
		statusCounts["inconclusive"], statusCounts["skipped"], other)

	if len(snapshot.CaseRuns) > 0 {
		builder.WriteString("- 用例结果：\n")
		for index, run := range snapshot.CaseRuns {
			if index == reportNarrativeMaxCaseRunLines {
				fmt.Fprintf(&builder, "  - 其余 %d 条用例结果见报告结果表。\n", len(snapshot.CaseRuns)-index)
				break
			}
			fmt.Fprintf(&builder, "  - %s：%s（步骤失败 %d/%d，断言失败 %d/%d，证据 %d）。\n",
				reportCaseRunLabel(run), run.Status, run.StepFailedCount, run.StepCount,
				run.AssertionFailedCount, run.AssertionCount, len(run.Evidence))
		}
	}

	fmt.Fprintf(&builder, "- 用例版本覆盖：%d / %d；需求覆盖：%d / %d。\n",
		snapshot.Coverage.CaseVersionsCovered, snapshot.Coverage.CaseVersionsTotal,
		snapshot.Coverage.RequirementsCovered, snapshot.Coverage.RequirementsTotal)
	notRun, failedCoverage := reportCoverageGapCounts(snapshot.Coverage.Gaps)
	fmt.Fprintf(&builder, "- 覆盖缺口：execution_not_run %d 个；execution_failed_coverage %d 个。\n", notRun, failedCoverage)

	if len(snapshot.Coverage.ByRequirement) > 0 {
		builder.WriteString("- 需求覆盖：\n")
		for index, row := range snapshot.Coverage.ByRequirement {
			if index == reportNarrativeMaxRequirementLines {
				fmt.Fprintf(&builder, "  - 其余 %d 个需求见报告覆盖章节。\n", len(snapshot.Coverage.ByRequirement)-index)
				break
			}
			fmt.Fprintf(&builder, "  - %s：覆盖 %d / %d（execution_not_run %d、execution_failed_coverage %d）。\n",
				reportRequirementLabel(snapshot, row.RequirementID), row.CaseVersionsCovered, row.CaseVersionsTotal,
				row.CaseVersionsNotRun, row.CaseVersionsFailedCoverage)
		}
	}

	openIssues := 0
	for _, issue := range snapshot.Issues {
		if issue.Status == delivery.IssueStatusOpen {
			openIssues++
		}
	}
	fmt.Fprintf(&builder, "- 问题 %d 个（未关闭 %d）。\n", len(snapshot.Issues), openIssues)
	if len(snapshot.TruncatedSections) > 0 {
		fmt.Fprintf(&builder, "- 已截断章节：%s。\n", strings.Join(snapshot.TruncatedSections, "、"))
	}
	return builder.String()
}

// reportCoverageGapCounts splits the deterministic gap list by reason.
func reportCoverageGapCounts(gaps []delivery.CoverageGapSnapshot) (notRun, failedCoverage int) {
	for _, gap := range gaps {
		switch gap.Reason {
		case "execution_not_run":
			notRun++
		case "execution_failed_coverage":
			failedCoverage++
		}
	}
	return notRun, failedCoverage
}

// reportClaimsNoValidExecution reports whether the sentence contains a
// no-valid-execution claim that is not negated ("不得...", "并非...", ...).
func reportClaimsNoValidExecution(sentence string) bool {
	for _, phrase := range reportNoValidExecutionPhrases {
		for offset := 0; offset < len(sentence); {
			index := strings.Index(sentence[offset:], phrase)
			if index < 0 {
				break
			}
			start := offset + index
			if !reportPhraseNegated(sentence, start) {
				return true
			}
			offset = start + len(phrase)
		}
	}
	return false
}

// reportPhraseNegated reports whether the text right before the phrase carries a
// negation marker, so an instruction about the claim is not read as the claim.
func reportPhraseNegated(text string, start int) bool {
	prefix := []rune(text[:start])
	if len(prefix) > 8 {
		prefix = prefix[len(prefix)-8:]
	}
	window := string(prefix)
	for _, marker := range reportNegationMarkers {
		if strings.Contains(window, marker) {
			return true
		}
	}
	return false
}

// reportClaimsAbsoluteNoExecution reports whether the claim covers every case
// version rather than the requirements named in the sentence.
func reportClaimsAbsoluteNoExecution(sentence string) bool {
	for _, phrase := range reportAbsoluteNoExecutionPhrases {
		if strings.Contains(sentence, phrase) {
			return true
		}
	}
	return false
}

// reportRequirementCodeTokens returns the distinct requirement codes (as
// written, e.g. "FR-011") mentioned in the text.
func reportRequirementCodeTokens(text string) []string {
	tokens := make([]string, 0, 2)
	seen := make(map[string]bool, 2)
	for _, match := range reportRequirementCode.FindAllString(text, -1) {
		digits := reportRequirementDigits(match)
		if digits == "" || seen[digits] {
			continue
		}
		seen[digits] = true
		tokens = append(tokens, match)
	}
	return tokens
}

// reportRequirementDigits normalizes a requirement code to its decimal digits so
// "FR-011" in a narrative matches "AC-FR-011-01" in a case title.
func reportRequirementDigits(token string) string {
	match := reportRequirementCode.FindStringSubmatch(token)
	if match == nil {
		return ""
	}
	digits, err := strconv.ParseUint(match[1], 10, 64)
	if err != nil {
		return ""
	}
	return strconv.FormatUint(digits, 10)
}

// reportTextMentionsRequirement reports whether text mentions the given
// requirement code (digits compared, so FR-011 / FR011 / AC-FR-011-01 match).
func reportTextMentionsRequirement(text, code string) bool {
	want := reportRequirementDigits(code)
	if want == "" {
		return false
	}
	for _, candidate := range reportRequirementCode.FindAllString(text, -1) {
		if reportRequirementDigits(candidate) == want {
			return true
		}
	}
	return false
}

// reportRequirementHasPassingResult reports whether the snapshot proves the
// requirement produced at least one passed deterministic result. Version reports
// carry requirement titles, so their authoritative per-requirement coverage row
// decides; wave reports do not carry requirement titles, but their case runs are
// already scoped to the wave, so a passed case named after the requirement
// disproves the claim.
func reportRequirementHasPassingResult(snapshot delivery.ReportSnapshot, code string) bool {
	matchedRequirement := false
	matchedCoverage := false
	for _, requirement := range snapshot.Requirements {
		if !reportTextMentionsRequirement(requirement.Title, code) {
			continue
		}
		matchedRequirement = true
		for _, coverage := range snapshot.Coverage.ByRequirement {
			if coverage.RequirementID != requirement.ID {
				continue
			}
			matchedCoverage = true
			if coverage.CaseVersionsCovered > 0 {
				return true
			}
		}
	}
	if matchedRequirement && matchedCoverage {
		return false
	}
	for _, run := range snapshot.CaseRuns {
		if run.Status == "passed" && reportTextMentionsRequirement(run.CaseTitle, code) {
			return true
		}
	}
	return false
}

// reportCaseRunLabel labels a fallback narrative line. Titles are report data
// (never evidence bytes or credentials); a run without a title keeps its ID.
func reportCaseRunLabel(run delivery.CaseRunSnapshot) string {
	if title := strings.TrimSpace(run.CaseTitle); title != "" {
		return title
	}
	return "用例版本 #" + strconv.FormatInt(run.CaseVersionID, 10)
}

// reportRequirementLabel names a per-requirement coverage line.
func reportRequirementLabel(snapshot delivery.ReportSnapshot, requirementID int64) string {
	for _, requirement := range snapshot.Requirements {
		if requirement.ID != requirementID {
			continue
		}
		if title := strings.TrimSpace(requirement.Title); title != "" {
			return title
		}
	}
	return "需求 #" + strconv.FormatInt(requirementID, 10)
}

// reportAtoi parses a decimal capture of one of the narrative regexes.
func reportAtoi(value string) int {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return -1
	}
	return parsed
}
