package design

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"
)

// opmlDocument is the OPML 2.0 document skeleton (spec §8).
type opmlDocument struct {
	XMLName xml.Name `xml:"opml"`
	Version string   `xml:"version,attr"`
	Head    opmlHead `xml:"head"`
	Body    opmlBody `xml:"body"`
}

type opmlHead struct {
	Title string `xml:"title"`
}

type opmlBody struct {
	Outlines []opmlOutline `xml:"outline"`
}

type opmlOutline struct {
	Text     string        `xml:"text,attr"`
	Note     string        `xml:"_note,attr,omitempty"`
	Outlines []opmlOutline `xml:"outline,omitempty"`
}

// RenderOPML renders the OPML 2.0 export. Case outlines carry a _note with the
// metadata, preconditions, test data, steps and assertions.
func RenderOPML(data ExportData) ([]byte, error) {
	ctx := newExportContext(data)
	document := opmlDocument{
		Version: "2.0",
		Head:    opmlHead{Title: "用例设计：" + ctx.Title},
		Body:    opmlBody{Outlines: make([]opmlOutline, 0, len(data.Requirements))},
	}
	for _, requirement := range data.Requirements {
		requirementOutline := opmlOutline{
			Text: fmt.Sprintf("%s（v%d，%s）", requirement.Title, requirement.Revision, requirement.Status),
			Note: fmt.Sprintf("目标 SUT: %s", formatIDs(requirement.TargetVersionSUTIDs)),
		}
		for _, scenario := range requirement.Scenarios {
			scenarioOutline := opmlOutline{Text: scenario.Name, Note: scenario.Description}
			for _, item := range scenario.Cases {
				scenarioOutline.Outlines = append(scenarioOutline.Outlines, opmlOutline{
					Text: fmt.Sprintf("%s（v%d，%s，%s）", item.Title, item.Revision, item.Status, item.Priority),
					Note: opmlCaseNote(item),
				})
			}
			requirementOutline.Outlines = append(requirementOutline.Outlines, scenarioOutline)
		}
		document.Body.Outlines = append(document.Body.Outlines, requirementOutline)
	}
	encoded, err := xml.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("design: render OPML: %w", err)
	}
	var builder strings.Builder
	builder.WriteString(xml.Header)
	builder.Write(encoded)
	builder.WriteString("\n")
	return []byte(builder.String()), nil
}

// opmlCaseNote renders the case note deterministically.
func opmlCaseNote(item ExportCase) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "状态: %s\n", item.Status)
	fmt.Fprintf(&builder, "优先级: %s\n", item.Priority)
	fmt.Fprintf(&builder, "默认 SUT: %s\n", formatOptionalID(item.DefaultVersionSUTID))
	if strings.TrimSpace(item.Preconditions) != "" {
		fmt.Fprintf(&builder, "前置条件: %s\n", item.Preconditions)
	}
	if len(item.TestData) > 0 && string(item.TestData) != "{}" && string(item.TestData) != "null" {
		testData := item.TestData
		var object map[string]any
		if err := json.Unmarshal(testData, &object); err == nil {
			if normalized, err := json.Marshal(object); err == nil {
				testData = normalized
			}
		}
		fmt.Fprintf(&builder, "测试数据: %s\n", string(testData))
	}
	for _, step := range item.Steps {
		fmt.Fprintf(&builder, "步骤 %d: %s -> %s\n", step.Seq, step.Action, step.Expected)
	}
	for _, assertion := range item.Assertions {
		fmt.Fprintf(&builder, "断言 %d: %s %s -> %s\n", assertion.StepSeq, assertion.Kind, assertion.Target, assertion.Expected)
	}
	for _, source := range item.Sources {
		fmt.Fprintf(&builder, "来源: %s:%s %s\n", source.Kind, source.RefID, source.Path)
	}
	return builder.String()
}
