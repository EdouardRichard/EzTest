package design

import (
	"fmt"
	"strings"
)

// RenderMermaid renders the mindmap view (spec §8). Labels are truncated to 60
// runes, brackets are replaced with full-width equivalents and newlines collapse
// into spaces so the mindmap stays parseable.
func RenderMermaid(data ExportData) ([]byte, error) {
	ctx := newExportContext(data)
	var builder strings.Builder
	builder.WriteString("mindmap\n")
	fmt.Fprintf(&builder, "  root((%s))\n", mermaidLabel(ctx.Title))
	for _, requirement := range data.Requirements {
		label := fmt.Sprintf("%s（v%d，%s）", requirement.Title, requirement.Revision, requirement.Status)
		fmt.Fprintf(&builder, "    %s\n", mermaidLabel(label))
		for _, scenario := range requirement.Scenarios {
			fmt.Fprintf(&builder, "      %s\n", mermaidLabel(scenario.Name))
			for _, item := range scenario.Cases {
				caseLabel := fmt.Sprintf("%s（%s，%s）", item.Title, item.Status, item.Priority)
				fmt.Fprintf(&builder, "        %s\n", mermaidLabel(caseLabel))
			}
		}
	}
	builder.WriteString("\n")
	return []byte(builder.String()), nil
}

// mermaidReplacements swaps characters that break a mindmap node label.
var mermaidReplacements = strings.NewReplacer(
	"(", "（", ")", "）",
	"[", "【", "]", "】",
	"{", "｛", "}", "｝",
	"\r\n", " ", "\n", " ", "\r", " ",
)

// mermaidLabel truncates and escapes one mindmap label.
func mermaidLabel(raw string) string {
	label := strings.TrimSpace(raw)
	runes := []rune(label)
	if len(runes) > ExportMermaidLabelMax {
		label = string(runes[:ExportMermaidLabelMax])
	}
	return mermaidReplacements.Replace(label)
}
