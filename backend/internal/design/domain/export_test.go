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
				Status: "clarified",
				Scenarios: []ExportScenario{
					{
						ID: 5001, Name: "正常登录", Description: "正常路径",
						Cases: []ExportCase{
							{
								CaseID: 7001, VersionID: 7002, Revision: 1, Title: "账号密码登录成功",
								Description: "使用有效账号", Preconditions: "已有可用账号",
								SutDescription: "商城前台登录页与登录接口。",
								CoveredACs:     []string{"AC-1", "AC-2"},
								TestData:       json.RawMessage(`{"username":"admin","password":"secret"}`),
								Priority:       "P1", Status: CaseStatusDraft,
								CreatedAt: generatedAt,
								Steps: []CaseStep{
									{Seq: 1, Action: "打开登录页", Expected: "显示, \"表单\"", TargetDescription: "登录页账号输入框", AccountRole: "admin"},
									{Seq: 2, Action: "提交\n账号", Expected: "登录成功", TargetDescription: "POST /login"},
								},
								Assertions: []CaseAssertion{
									{StepSeq: 1, Kind: "ui", Target: "欢迎文案", Expected: "欢迎回来"},
								},
								Sources: []CaseSource{{Kind: "requirement", RefID: "3002"}},
							},
							{
								CaseID: 7011, VersionID: 7012, Revision: 1, Title: "旧登录",
								Priority: "P2", Status: CaseStatusStale,
								SutDescription: "商城前台登录页。",
								CoveredACs:     []string{"AC-1"},
								CreatedAt:      generatedAt,
							},
						},
					},
				},
			},
			{
				ID: 3101, RequirementVersionID: 3102, Revision: 1, Title: "未设计需求",
				Status: "clarified",
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
		"- 被测系统：商城前台登录页与登录接口。",
		"- 覆盖验收标准：AC-1, AC-2",
		"| 序号 | 动作 | 预期 | 目标 | 账号角色 |",
		"| 1 | 打开登录页 | 显示, \"表单\" | 登录页账号输入框 | admin |",
		"- 步骤 1 · ui：欢迎文案 → 欢迎回来",
		"- requirement:3002",
		"## 需求：未设计需求（v1，clarified）",
		"未设计",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("markdown missing %q\n---\n%s", expected, text)
		}
	}
	// The v0.2.4-B export describes the system under test in prose; the retired
	// resource-id lines must not come back.
	for _, gone := range []string{"默认目标 SUT", "版本 SUT", "- 页面：", "- API：", "9001", "6001"} {
		if strings.Contains(text, gone) {
			t.Fatalf("markdown still carries the retired resource column %q\n---\n%s", gone, text)
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
	for _, expected := range []string{
		"状态: draft", "优先级: P1",
		"被测系统: 商城前台登录页与登录接口。",
		"覆盖验收标准: AC-1, AC-2",
		"前置条件: 已有可用账号",
		"步骤 1: 打开登录页 -> 显示, \"表单\"（目标: 登录页账号输入框）",
		"断言 1:", "来源: requirement:3002",
	} {
		if !strings.Contains(caseOutline.Note, expected) {
			t.Fatalf("case note missing %q: %q", expected, caseOutline.Note)
		}
	}
	if strings.Contains(caseOutline.Note, "默认 SUT") {
		t.Fatalf("case note still carries the retired default-SUT line: %q", caseOutline.Note)
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
	// Resolve columns by header name: the v0.2.4-B header is 19 wide (three
	// case-level resource columns and three step-level ones were replaced by
	// sut_description/covered_acs and step_target_description), so pinned numeric
	// offsets would silently keep passing against the wrong column.
	column := make(map[string]int, len(csvHeader))
	for index, name := range csvHeader {
		column[name] = index
	}
	for _, name := range []string{"sut_description", "covered_acs", "step_action", "step_expected", "step_target_description", "account_role", "assertions"} {
		if _, ok := column[name]; !ok {
			t.Fatalf("csv header lost column %q: %v", name, csvHeader)
		}
	}
	for _, gone := range []string{"sut_names", "page_names", "api_targets", "step_target_sut", "step_page", "step_api"} {
		if _, ok := column[gone]; ok {
			t.Fatalf("csv header still carries the retired column %q: %v", gone, csvHeader)
		}
	}

	stepRow := records[1]
	if len(stepRow) != len(csvHeader) {
		t.Fatalf("row width = %d, want %d", len(stepRow), len(csvHeader))
	}
	if stepRow[column["step_action"]] != "打开登录页" || stepRow[column["step_expected"]] != "显示, \"表单\"" {
		t.Fatalf("escaped step row = %v", stepRow)
	}
	if stepRow[column["step_target_description"]] != "登录页账号输入框" {
		t.Fatalf("step_target_description = %q", stepRow[column["step_target_description"]])
	}
	if stepRow[column["sut_description"]] != "商城前台登录页与登录接口。" {
		t.Fatalf("sut_description = %q", stepRow[column["sut_description"]])
	}
	if stepRow[column["covered_acs"]] != "AC-1, AC-2" {
		t.Fatalf("covered_acs = %q", stepRow[column["covered_acs"]])
	}
	if !strings.Contains(stepRow[column["assertions"]], "ui:欢迎回来") {
		t.Fatalf("assertions column = %q", stepRow[column["assertions"]])
	}
	noStep := records[3]
	if len(noStep) != len(csvHeader) {
		t.Fatalf("no-step row width = %d, want %d", len(noStep), len(csvHeader))
	}
	if noStep[column["step_seq"]] != "" || noStep[column["case_title"]] != "旧登录" {
		t.Fatalf("no-step row = %v", noStep)
	}
	// The undesigned-requirement filler row must still be padded to the full
	// header width after the column count changed.
	filler := records[4]
	if len(filler) != len(csvHeader) {
		t.Fatalf("filler row width = %d, want %d (%v)", len(filler), len(csvHeader), filler)
	}
	if filler[column["scenario_name"]] != "未设计" {
		t.Fatalf("filler row = %v", filler)
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
