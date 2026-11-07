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
	// An <img> is an external reference, not an embedded image: it is rendered
	// once, as a marker inside its surrounding paragraph. It must not also be
	// emitted as a separate "image" element (that would double count the same
	// visible item). Coverage.ImagesFound counts embedded image bytes only, so
	// HTML references never inflate it.
	if strings.Count(result.Text, "登录流程图") != 1 || strings.Count(result.Text, "/assets/login.png") != 1 {
		t.Fatalf("HTML image reference rendered more than once:\n%s", result.Text)
	}
	counts := map[string]int{}
	for _, element := range result.Elements {
		counts[element.Kind]++
	}
	wantCounts := map[string]int{"heading": 2, "paragraph": 2, "list_item": 2, "table_row": 2}
	if len(result.Elements) != 8 {
		t.Fatalf("HTML element count = %d, want 8: %+v", len(result.Elements), result.Elements)
	}
	for kind, want := range wantCounts {
		if counts[kind] != want {
			t.Fatalf("element kind %q count = %d, want %d: %+v", kind, counts[kind], want, result.Elements)
		}
	}
	if counts["image"] != 0 {
		t.Fatalf("HTML image double counted as a separate element: %+v", result.Elements)
	}
	if result.Coverage.ImagesFound != 0 || result.Coverage.ImagesParsed != 0 {
		t.Fatalf("HTML image references counted as embedded images: %+v", result.Coverage)
	}
	if _, ok := result.Coverage.ElementCounts["image"]; ok {
		t.Fatalf("HTML element counts include an image element: %+v", result.Coverage.ElementCounts)
	}
	if result.Coverage.Skipped != 0 || result.Coverage.Found != result.Coverage.Parsed {
		t.Fatalf("coverage = %+v skipped=%+v", result.Coverage, result.Skipped)
	}
	if result.Coverage.Parsed != len(result.Elements) {
		t.Fatalf("parsed = %d, elements = %d", result.Coverage.Parsed, len(result.Elements))
	}
}
