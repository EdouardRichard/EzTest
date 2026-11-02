package design

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"strings"
	"testing"
	"time"
)

func exportFixture() ExportData {
	generatedAt := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	return ExportData{
		VersionID:    1001,
		VersionTitle: "登录版本",
		GeneratedAt:  generatedAt,
		Requirements: []ExportRequirement{
			{
				ID: 3001, RequirementVersionID: 3002, Revision: 2, Title: "登录",
				Status: "clarified", TargetVersionSUTIDs: []int64{9001},
				Scenarios: []ExportScenario{
					{
						ID: 5001, Name: "正常登录", Description: "正常路径",
						VersionSUTIDs: []int64{9001, 9002},
						Cases: []ExportCase{
							{
								CaseID: 7001, VersionID: 7002, Revision: 1, Title: "账号密码登录成功",
								Description: "使用有效账号", Preconditions: "已有可用账号",
								TestData: json.RawMessage(`{"username":"admin","password":"secret"}`),
								Priority: "P1", Status: CaseStatusDraft,
								DefaultVersionSUTID: int64Ptr(9001),
								VersionSUTIDs:       []int64{9001, 9002},
								CreatedAt:           generatedAt,
								Steps: []CaseStep{
									{Seq: 1, Action: "打开登录页", Expected: "显示, \"表单\"", TargetPageID: int64Ptr(6001), AccountRole: "admin"},
									{Seq: 2, Action: "提交\n账号", Expected: "登录成功", TargetVersionSUTID: int64Ptr(9002), TargetAPIID: int64Ptr(4002)},
								},
								Assertions: []CaseAssertion{
									{StepSeq: 1, Kind: "ui", Target: "欢迎文案", Expected: "欢迎回来"},
								},
								Sources: []CaseSource{{Kind: "requirement", RefID: "3002"}},
							},
							{
								CaseID: 7011, VersionID: 7012, Revision: 1, Title: "旧登录",
								Priority: "P2", Status: CaseStatusStale,
								DefaultVersionSUTID: int64Ptr(9001),
								VersionSUTIDs:       []int64{9001},
								CreatedAt:           generatedAt,
							},
						},
					},
				},
			},
			{
				ID: 3101, RequirementVersionID: 3102, Revision: 1, Title: "未设计需求",
				Status: "clarified", TargetVersionSUTIDs: []int64{},
			},
		},
	}
}

func TestMarkdownContainsHierarchyAndSteps(t *testing.T) {
	rendered, err := RenderMarkdown(exportFixture())
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	text := string(rendered)
	for _, expected := range []string{
		"# 用例设计：登录版本",
		"> 生成时间：2026-10-31T12:00:00Z",
		"## 需求：登录（v2，clarified）",
		"### 场景：正常登录",
		"#### 用例：账号密码登录成功（v1，draft，P1）",
		"| 1 | 打开登录页 | 显示, \"表单\" |",
		"- 步骤 1 · ui：欢迎文案 → 欢迎回来",
		"- requirement:3002",
		"## 需求：未设计需求（v1，clarified）",
		"未设计",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("markdown missing %q\n---\n%s", expected, text)
		}
	}
	if strings.Contains(text, "superseded") {
		t.Fatalf("markdown leaked a superseded version:\n%s", text)
	}
}

func TestMermaidEscapesLabelsAndTruncates(t *testing.T) {
	longName := strings.Repeat("长场景", 30)
	data := exportFixture()
	data.VersionTitle = "登录(测试)[v2]{x}"
	data.Requirements[0].Scenarios[0].Name = longName
	rendered, err := RenderMermaid(data)
	if err != nil {
		t.Fatalf("RenderMermaid: %v", err)
	}
	text := string(rendered)
	if !strings.HasPrefix(text, "mindmap\n") {
		t.Fatalf("mermaid header = %q", text[:20])
	}
	if !strings.Contains(text, "登录（测试）【v2】｛x｝") {
		t.Fatalf("root label not escaped:\n%s", text)
	}
	if strings.Contains(text, longName) {
		t.Fatal("long label was not truncated")
	}
	if !strings.Contains(text, "（draft，P1）") {
		t.Fatalf("case leaf missing status/priority:\n%s", text)
	}
	if strings.Contains(text, "()") || strings.Contains(text, "\n\n\n") {
		t.Fatalf("unescaped brackets or stray blank lines:\n%s", text)
	}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if len([]rune(line)) > 200 {
			t.Fatalf("line too long: %q", line)
		}
	}
}

func TestOPMLParsesAndHasNotes(t *testing.T) {
	rendered, err := RenderOPML(exportFixture())
	if err != nil {
		t.Fatalf("RenderOPML: %v", err)
	}
	var document opmlDocument
	if err := xml.Unmarshal(rendered, &document); err != nil {
		t.Fatalf("OPML does not parse: %v\n%s", err, rendered)
	}
	if document.Version != "2.0" || !strings.Contains(document.Head.Title, "登录版本") {
		t.Fatalf("document head = %+v", document)
	}
	var caseOutline *opmlOutline
	var walk func(outlines []opmlOutline)
	walk = func(outlines []opmlOutline) {
		for index := range outlines {
			outline := outlines[index]
			if strings.Contains(outline.Text, "账号密码登录成功") {
				copied := outline
				caseOutline = &copied
			}
			walk(outline.Outlines)
		}
	}
	walk(document.Body.Outlines)
	if caseOutline == nil {
		t.Fatal("case outline missing from OPML")
	}
	for _, expected := range []string{"状态: draft", "优先级: P1", "前置条件: 已有可用账号", "步骤 1:", "断言 1:", "来源: requirement:3002"} {
		if !strings.Contains(caseOutline.Note, expected) {
			t.Fatalf("case note missing %q: %q", expected, caseOutline.Note)
		}
	}
}

func TestCSVOneRowPerStepAndEscapes(t *testing.T) {
	data := exportFixture()
	rendered, err := RenderCSV(data)
	if err != nil {
		t.Fatalf("RenderCSV: %v", err)
	}
	reader := csv.NewReader(bytes.NewReader(rendered))
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("CSV does not parse: %v\n%s", err, rendered)
	}
	if len(records) == 0 {
		t.Fatal("empty CSV")
	}
	if strings.Join(records[0], ",") != strings.Join(csvHeader, ",") {
		t.Fatalf("header = %v", records[0])
	}
	// 2 steps for case 1 + 1 empty-step row for the stale case + 1 row for the
	// undesigned requirement.
	if len(records) != 1+4 {
		t.Fatalf("rows = %d, want 5\n%v", len(records), records)
	}
	stepRow := records[1]
	if len(stepRow) != len(csvHeader) {
		t.Fatalf("row width = %d, want %d", len(stepRow), len(csvHeader))
	}
	if stepRow[15] != "打开登录页" || stepRow[16] != "显示, \"表单\"" {
		t.Fatalf("escaped step row = %v", stepRow)
	}
	if !strings.Contains(stepRow[21], "ui:欢迎回来") {
		t.Fatalf("assertions column = %q", stepRow[21])
	}
	noStep := records[3]
	if noStep[14] != "" || noStep[7] != "旧登录" {
		t.Fatalf("no-step row = %v", noStep)
	}
	if strings.HasPrefix(string(rendered), "\ufeff") {
		t.Fatal("CSV must not start with a BOM")
	}
}

func TestExportSameContentHashStable(t *testing.T) {
	first := exportFixture()
	second := exportFixture()
	// JSON object key order must not change the rendered content.
	second.Requirements[0].Scenarios[0].Cases[0].TestData = json.RawMessage(`{"password":"secret","username":"admin"}`)
	for _, format := range []string{ExportFormatMarkdown, ExportFormatMermaid, ExportFormatOPML, ExportFormatCSV} {
		firstBytes, err := RenderExport(first, format)
		if err != nil {
			t.Fatalf("RenderExport(%s): %v", format, err)
		}
		secondBytes, err := RenderExport(second, format)
		if err != nil {
			t.Fatalf("RenderExport(%s) second: %v", format, err)
		}
		if !bytes.Equal(firstBytes, secondBytes) {
			t.Fatalf("format %s is not deterministic across equivalent JSON key order", format)
		}
		firstHash := sha256.Sum256(firstBytes)
		secondHash := sha256.Sum256(secondBytes)
		if hex.EncodeToString(firstHash[:]) != hex.EncodeToString(secondHash[:]) {
			t.Fatalf("format %s hash mismatch", format)
		}
	}
	unknown := "xlsx"
	if _, err := RenderExport(first, unknown); err == nil {
		t.Fatal("unknown format accepted")
	}
}
