package application

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// renderReportMarkdown renders the deterministic Markdown artifact of spec
// §16.3: header, execution summary, requirement coverage, case results, issues,
// risks/conclusion, appendix and the Mermaid coverage graph. The renderer is
// pure: the same content, revision, fingerprint and coverage mapping always
// produce the same bytes.
func renderReportMarkdown(content domain.ReportContent, revision int, fingerprint string, requirementCases map[int64][]int64) []byte {
	content = content.Normalized()
	var builder strings.Builder

	versionName := content.Version.Name
	if strings.TrimSpace(versionName) == "" {
		versionName = content.Version.Label
	}
	fmt.Fprintf(&builder, "# %s — Batch %d 报告（%s，revision %d）\n\n",
		reportMarkdownInline(versionName), content.Batch.BatchNo, reportMarkdownInline(content.Batch.Kind), revision)
	writeReportMarkdownHeader(&builder, content)

	builder.WriteString("## 1. 执行摘要\n\n")
	summary := content.Summary
	fmt.Fprintf(&builder,
		"- 用例：%d；通过 %d、失败 %d、阻塞 %d、不确定 %d、跳过 %d、中断 %d\n",
		summary.CaseCount, summary.Passed, summary.Failed, summary.Blocked,
		summary.Inconclusive, summary.Skipped, summary.Interrupted)
	fmt.Fprintf(&builder, "- 失败分类：%s\n", reportMarkdownFailureClasses(content.Cases))
	fmt.Fprintf(&builder, "- 问题：%s\n", reportMarkdownIssueStatuses(content.Issues))
	if summary.FlakyCount > 0 {
		fmt.Fprintf(&builder, "- 不稳定用例：%d\n", summary.FlakyCount)
	}

	writeReportMarkdownRequirements(&builder, content)
	writeReportMarkdownCases(&builder, content)
	writeReportMarkdownIssues(&builder, content)
	writeReportMarkdownRisks(&builder, content)

	builder.WriteString("## 6. 附录\n\n")
	fmt.Fprintf(&builder, "- 环境/系统：%s / %s\n",
		reportMarkdownPlaceholder(content.Appendix.Environment),
		reportMarkdownPlaceholder(content.Appendix.System))
	fmt.Fprintf(&builder, "- 代码：%s → %s\n",
		reportMarkdownPlaceholder(content.Batch.CodeBaseCommit),
		reportMarkdownPlaceholder(content.Batch.HeadCommit))
	fmt.Fprintf(&builder, "- 报告指纹：%s\n", fingerprint)

	writeReportMarkdownMermaid(&builder, content, requirementCases)
	return []byte(builder.String())
}

// writeReportMarkdownHeader writes the version/batch/commit header bullets.
func writeReportMarkdownHeader(builder *strings.Builder, content domain.ReportContent) {
	fmt.Fprintf(builder, "- 版本状态：%s\n", reportMarkdownPlaceholder(content.Version.Status))
	fmt.Fprintf(builder, "- 批次状态：%s\n", reportMarkdownPlaceholder(content.Batch.Status))
	fmt.Fprintf(builder, "- 代码基线：%s\n",
		reportMarkdownPlaceholder(content.Version.CodeBaselineCommit))
	fmt.Fprintf(builder, "- 回退标记：%s\n", reportMarkdownFallback(content.Batch))
	fmt.Fprintf(builder, "- 代码：%s → %s（commit）\n",
		reportMarkdownPlaceholder(content.Batch.CodeBaseCommit),
		reportMarkdownPlaceholder(content.Batch.HeadCommit))
	fmt.Fprintf(builder, "- 生成时间：%s\n", reportMarkdownPlaceholder(content.Appendix.GeneratedAt))
	if content.Degraded {
		builder.WriteString("- 降级：报告生成时存在 AI 分析/补全降级项，结论可能不完整\n")
	}
	builder.WriteString("\n")
}

// reportMarkdownFallback renders the D49 fallback marker: a legacy snapshot
// without the fields is "未记录", a new snapshot with an empty reason is "无",
// and a recorded reason is rendered verbatim with the history_rewritten note.
func reportMarkdownFallback(batch domain.ReportBatch) string {
	if batch.BaseFallbackReason == nil && batch.HistoryRewritten == nil {
		return "未记录"
	}
	if batch.HistoryRewritten != nil && *batch.HistoryRewritten {
		reason := domain.ReportBaseFallbackHistoryRewritten
		if batch.BaseFallbackReason != nil && *batch.BaseFallbackReason != "" {
			reason = *batch.BaseFallbackReason
		}
		return reason + "（历史已回退）"
	}
	if batch.BaseFallbackReason != nil && *batch.BaseFallbackReason != "" {
		return *batch.BaseFallbackReason
	}
	return "无"
}

// writeReportMarkdownRequirements writes the requirement coverage table.
func writeReportMarkdownRequirements(builder *strings.Builder, content domain.ReportContent) {
	builder.WriteString("## 2. 需求覆盖\n\n")
	builder.WriteString("| 需求 | 状态 | 用例数 | 通过 | 失败 |\n")
	builder.WriteString("| --- | --- | --- | --- | --- |\n")
	if len(content.Requirements) == 0 {
		builder.WriteString("| （无） | — | 0 | 0 | 0 |\n\n")
		return
	}
	for _, requirement := range content.Requirements {
		fmt.Fprintf(builder, "| %s | %s | %d | %d | %d |\n",
			reportMarkdownCell(requirement.Title), reportMarkdownCell(requirement.Status),
			requirement.CaseCount, requirement.Passed, requirement.Failed)
	}
	builder.WriteString("\n")
}

// writeReportMarkdownCases writes the case result table.
func writeReportMarkdownCases(builder *strings.Builder, content domain.ReportContent) {
	builder.WriteString("## 3. 用例结果\n\n")
	builder.WriteString("| 用例 | 来源 | 结果 | 分类 | 证据数 | 步骤/断言 |\n")
	builder.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	if len(content.Cases) == 0 {
		builder.WriteString("| （无用例） | — | — | — | 0 | 0/0 |\n\n")
		return
	}
	for _, item := range content.Cases {
		fmt.Fprintf(builder, "| %s | %s | %s | %s | %d | %d/%d |\n",
			reportMarkdownCell(item.Title), reportMarkdownCell(item.Source),
			reportMarkdownCell(item.Status), reportMarkdownCell(item.FailureClass),
			item.EvidenceCount, item.StepCount, item.AssertionCount)
	}
	builder.WriteString("\n")
}

// writeReportMarkdownIssues writes the issue table.
func writeReportMarkdownIssues(builder *strings.Builder, content domain.ReportContent) {
	builder.WriteString("## 4. 问题\n\n")
	builder.WriteString("| 严重级 | 标题 | 状态 | 置信度 | 关联用例 | 代码位置 |\n")
	builder.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	if len(content.Issues) == 0 {
		builder.WriteString("| （无） | — | — | — | — | — |\n\n")
		return
	}
	titles := reportMarkdownCaseTitles(content.Cases)
	for _, issue := range content.Issues {
		fmt.Fprintf(builder, "| %s | %s | %s | %s | %s | %s |\n",
			reportMarkdownCell(issue.Severity), reportMarkdownCell(issue.Title),
			reportMarkdownCell(issue.Status), reportMarkdownCell(issue.Confidence),
			reportMarkdownCell(reportMarkdownRelatedCase(issue.CaseNodeID, titles)),
			reportMarkdownCell(reportMarkdownCodeRefs(issue.CodeRefs)))
	}
	builder.WriteString("\n")
}

// writeReportMarkdownRisks writes the risk bullets and the deterministic
// conclusion.
func writeReportMarkdownRisks(builder *strings.Builder, content domain.ReportContent) {
	builder.WriteString("## 5. 风险与结论\n\n")
	if len(content.Risks) == 0 {
		builder.WriteString("- 风险：（无）\n")
	} else {
		for _, risk := range content.Risks {
			fmt.Fprintf(builder, "- %s\n", reportMarkdownInline(risk))
		}
	}
	fmt.Fprintf(builder, "- 结论：%d/%d 个用例通过\n\n", content.Summary.Passed, content.Summary.CaseCount)
}

// writeReportMarkdownMermaid writes the deterministic Mermaid coverage graph of
// spec §6.5/§16.3: requirements → covered cases → status, and cases → issues.
func writeReportMarkdownMermaid(builder *strings.Builder, content domain.ReportContent, requirementCases map[int64][]int64) {
	builder.WriteString("\n## 7. 覆盖关系图\n\n```mermaid\ngraph LR\n")

	caseIDs := make(map[int64]bool, len(content.Cases))
	caseOrder := make([]int64, 0, len(content.Cases))
	caseStatus := make(map[int64]string, len(content.Cases))
	for _, item := range content.Cases {
		id, ok := reportMarkdownParseID(item.CaseNodeID)
		if !ok {
			continue
		}
		caseIDs[id] = true
		caseStatus[id] = item.Status
		caseOrder = append(caseOrder, id)
	}
	sort.SliceStable(caseOrder, func(i, j int) bool { return caseOrder[i] < caseOrder[j] })
	caseTitles := reportMarkdownCaseTitles(content.Cases)

	for _, requirement := range content.Requirements {
		requirementID, ok := reportMarkdownParseID(requirement.RequirementID)
		if !ok {
			continue
		}
		node := "R" + strconv.FormatInt(requirementID, 10)
		fmt.Fprintf(builder, "  %s[\"%s\"]\n", node, reportMarkdownMermaidLabel(requirement.Title))
		covered := append([]int64(nil), requirementCases[requirementID]...)
		sort.SliceStable(covered, func(i, j int) bool { return covered[i] < covered[j] })
		for _, caseID := range covered {
			if !caseIDs[caseID] {
				continue
			}
			fmt.Fprintf(builder, "  %s --> C%d[\"%s\"]\n", node, caseID,
				reportMarkdownMermaidLabel(caseTitles[caseID]))
		}
	}
	for _, caseID := range caseOrder {
		fmt.Fprintf(builder, "  C%d --> S%d[\"%s\"]\n", caseID, caseID,
			reportMarkdownMermaidLabel(caseStatus[caseID]))
	}
	for _, issue := range content.Issues {
		issueID, ok := reportMarkdownParseID(issue.IssueID)
		if !ok {
			continue
		}
		caseID, caseOK := reportMarkdownParseID(issue.CaseNodeID)
		if !caseOK || !caseIDs[caseID] {
			continue
		}
		label := issue.Severity + "：" + issue.Title
		fmt.Fprintf(builder, "  C%d --> I%d[\"%s\"]\n", caseID, issueID,
			reportMarkdownMermaidLabel(label))
	}
	builder.WriteString("```\n")
}

// reportMarkdownFailureClasses renders the fixed-order failure class tally.
func reportMarkdownFailureClasses(cases []domain.ReportCase) string {
	counts := make(map[string]int, 5)
	for _, item := range cases {
		if item.FailureClass != "" {
			counts[item.FailureClass]++
		}
	}
	classes := []string{"application", "environment", "session", "model", "case"}
	parts := make([]string, 0, len(classes))
	for _, class := range classes {
		parts = append(parts, fmt.Sprintf("%s %d", class, counts[class]))
	}
	return strings.Join(parts, " / ")
}

// reportMarkdownIssueStatuses renders the open/confirmed/dismissed tally.
func reportMarkdownIssueStatuses(issues []domain.ReportIssue) string {
	counts := map[string]int{"open": 0, "confirmed": 0, "dismissed": 0}
	for _, issue := range issues {
		if _, ok := counts[issue.Status]; ok {
			counts[issue.Status]++
		}
	}
	return fmt.Sprintf("open %d / confirmed %d / dismissed %d",
		counts["open"], counts["confirmed"], counts["dismissed"])
}

// reportMarkdownCaseTitles indexes case titles by node id.
func reportMarkdownCaseTitles(cases []domain.ReportCase) map[int64]string {
	titles := make(map[int64]string, len(cases))
	for _, item := range cases {
		if id, ok := reportMarkdownParseID(item.CaseNodeID); ok {
			titles[id] = item.Title
		}
	}
	return titles
}

// reportMarkdownRelatedCase renders the issue's linked case title.
func reportMarkdownRelatedCase(caseNodeID string, titles map[int64]string) string {
	id, ok := reportMarkdownParseID(caseNodeID)
	if !ok {
		return ""
	}
	if title, found := titles[id]; found {
		return title
	}
	return "#" + caseNodeID
}

// reportMarkdownCodeRefs renders "path:line" joined locations.
func reportMarkdownCodeRefs(refs []domain.ReportCodeRef) string {
	if len(refs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(refs))
	for _, ref := range refs {
		parts = append(parts, fmt.Sprintf("%s:%d", ref.Path, ref.Line))
	}
	return strings.Join(parts, "、")
}

// reportMarkdownInline folds newlines into spaces and trims one cell value.
func reportMarkdownInline(value string) string {
	replacer := strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ", "\t", " ")
	return strings.TrimSpace(replacer.Replace(value))
}

// reportMarkdownPlaceholder returns value or the Chinese "（未绑定）" fallback.
func reportMarkdownPlaceholder(value string) string {
	if trimmed := reportMarkdownInline(value); trimmed != "" {
		return trimmed
	}
	return "（未绑定）"
}

// reportMarkdownCell prepares one Markdown table cell.
func reportMarkdownCell(value string) string {
	trimmed := strings.ReplaceAll(reportMarkdownInline(value), "|", `\|`)
	if trimmed == "" {
		return "—"
	}
	return trimmed
}

// reportMarkdownMermaidLabel prepares one quoted Mermaid label.
func reportMarkdownMermaidLabel(value string) string {
	inline := reportMarkdownInline(value)
	replacer := strings.NewReplacer(`"`, "'", "`", "'", "<", "＜", ">", "＞")
	return replacer.Replace(inline)
}

// reportMarkdownParseID parses a positive decimal-string id.
func reportMarkdownParseID(value string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
