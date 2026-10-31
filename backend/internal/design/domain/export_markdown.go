package design

import (
	"fmt"
	"strings"
)

// RenderMarkdown renders the hierarchical case-design document (spec §8).
func RenderMarkdown(data ExportData) ([]byte, error) {
	ctx := newExportContext(data)
	var builder strings.Builder
	fmt.Fprintf(&builder, "# 用例设计：%s\n\n", ctx.Title)
	fmt.Fprintf(&builder, "> 生成时间：%s\n\n", ctx.GeneratedAt)
	if len(data.Requirements) == 0 {
		builder.WriteString("暂无已澄清需求。\n")
	}
	for _, requirement := range data.Requirements {
		fmt.Fprintf(&builder, "## 需求：%s（v%d，%s）\n\n", requirement.Title, requirement.Revision, requirement.Status)
		fmt.Fprintf(&builder, "- 目标 SUT：%s\n\n", formatIDs(requirement.TargetVersionSUTIDs))
		if len(requirement.Scenarios) == 0 {
			builder.WriteString("未设计\n\n")
			continue
		}
		for _, scenario := range requirement.Scenarios {
			fmt.Fprintf(&builder, "### 场景：%s\n\n", scenario.Name)
			if strings.TrimSpace(scenario.Description) != "" {
				fmt.Fprintf(&builder, "%s\n\n", scenario.Description)
			}
			if len(scenario.Cases) == 0 {
				builder.WriteString("未设计\n\n")
				continue
			}
			for _, item := range scenario.Cases {
				renderMarkdownCase(&builder, item)
			}
		}
	}
	return []byte(builder.String()), nil
}

func renderMarkdownCase(builder *strings.Builder, item ExportCase) {
	fmt.Fprintf(builder, "#### 用例：%s（v%d，%s，%s）\n\n", item.Title, item.Revision, item.Status, item.Priority)
	fmt.Fprintf(builder, "- 用例 ID：%s\n", formatInt64(item.CaseID))
	fmt.Fprintf(builder, "- 版本 ID：%s\n", formatInt64(item.VersionID))
	fmt.Fprintf(builder, "- 默认目标 SUT：%s\n", formatOptionalID(item.DefaultVersionSUTID))
	fmt.Fprintf(builder, "- 版本 SUT：%s\n", formatIDs(item.VersionSUTIDs))
	fmt.Fprintf(builder, "- 页面：%s\n", formatIDs(item.PageIDs))
	fmt.Fprintf(builder, "- API：%s\n", formatIDs(item.APIIDs))
	builder.WriteString("\n")
	if strings.TrimSpace(item.Description) != "" {
		fmt.Fprintf(builder, "%s\n\n", item.Description)
	}
	if strings.TrimSpace(item.Preconditions) != "" {
		fmt.Fprintf(builder, "**前置条件**：%s\n\n", item.Preconditions)
	}
	if len(item.TestData) > 0 && string(item.TestData) != "{}" && string(item.TestData) != "null" {
		testData := item.TestData
		if normalized, err := NormalizeJSONObject(item.TestData); err == nil {
			testData = normalized
		}
		fmt.Fprintf(builder, "**测试数据**：`%s`\n\n", string(testData))
	}

	builder.WriteString("| 序号 | 动作 | 预期 | 目标 SUT | 页面 | API | 账号角色 |\n")
	builder.WriteString("|---|---|---|---|---|---|---|\n")
	if len(item.Steps) == 0 {
		builder.WriteString("| - | 未设计 | - | - | - | - | - |\n")
	}
	for _, step := range item.Steps {
		fmt.Fprintf(builder, "| %d | %s | %s | %s | %s | %s | %s |\n",
			step.Seq, escapeMarkdownCell(step.Action), escapeMarkdownCell(step.Expected),
			formatOptionalID(step.TargetVersionSUTID), formatOptionalID(step.TargetPageID),
			formatOptionalID(step.TargetAPIID), step.AccountRole)
	}
	builder.WriteString("\n")

	if len(item.Assertions) > 0 {
		builder.WriteString("**断言**\n\n")
		for _, assertion := range item.Assertions {
			fmt.Fprintf(builder, "- 步骤 %d · %s：%s → %s\n", assertion.StepSeq, assertion.Kind,
				assertion.Target, assertion.Expected)
		}
		builder.WriteString("\n")
	}
	if len(item.Sources) > 0 {
		builder.WriteString("**来源**\n\n")
		for _, source := range item.Sources {
			fmt.Fprintf(builder, "- %s:%s %s\n", source.Kind, source.RefID, source.Path)
		}
		builder.WriteString("\n")
	}
}

// escapeMarkdownCell keeps table rendering intact for pipes/newlines.
func escapeMarkdownCell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\r\n", " ")
	return strings.ReplaceAll(value, "\n", " ")
}
