package attachments_test

import (
	"strings"
	"testing"
)

// TestHTMLFullyExtracts covers R-A7-3 for HTML: visible text, heading
// hierarchy, lists, tables and image alt/src references, while raw tags,
// script/style bodies and the document head must not become body text.
func TestHTMLFullyExtracts(t *testing.T) {
	result, err := extractResult(t, "requirements.html", "text/html", []byte(htmlFullyFixture))
	if err != nil {
		t.Fatalf("html: %v", err)
	}
	for _, want := range []string{
		"需求概述", "3.2 用户登录", "可见", "& 实体",
		"手机号", "验证码", "| 字段 | 说明 |", "| 手机号 | 表格单元格 |",
		"登录流程图", "/assets/login.png",
	} {
		if !strings.Contains(result.Text, want) {
			t.Fatalf("missing %q in extracted HTML:\n%s", want, result.Text)
		}
	}
	for _, unwanted := range []string{"<h1>", "<p>", "<table>", "脚本不可见", "color:red", "不可见标题"} {
		if strings.Contains(result.Text, unwanted) {
			t.Fatalf("raw/invisible HTML leaked %q:\n%s", unwanted, result.Text)
		}
	}
	levelOne, levelTwo := false, false
	for _, element := range result.Elements {
		switch {
		case element.Kind == "heading" && element.Level == 1 && element.Text == "需求概述":
			levelOne = true
		case element.Kind == "heading" && element.Level == 2 && element.Text == "3.2 用户登录":
			levelTwo = true
			if element.HeadingPath != "需求概述 > 3.2 用户登录" {
				t.Fatalf("heading path = %q", element.HeadingPath)
			}
		}
	}
	if !levelOne || !levelTwo {
		t.Fatalf("heading hierarchy lost: %+v", result.Elements)
	}
	if !findElement(result.Elements, "list_item", "手机号") || !findElement(result.Elements, "table_row", "表格单元格") {
		t.Fatalf("list/table elements missing: %+v", result.Elements)
	}
	if !findElement(result.Elements, "image", "/assets/login.png") {
		t.Fatalf("image reference missing: %+v", result.Elements)
	}
	if result.Coverage.Skipped != 0 || result.Coverage.Found != result.Coverage.Parsed {
		t.Fatalf("coverage = %+v skipped=%+v", result.Coverage, result.Skipped)
	}
	if result.Coverage.Parsed != len(result.Elements) {
		t.Fatalf("parsed = %d, elements = %d", result.Coverage.Parsed, len(result.Elements))
	}
}
