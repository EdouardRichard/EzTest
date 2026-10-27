package application

import (
	"fmt"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// TestPromptVersionDefault pins the P4.1 prompt version tag that participates
// in the cache key and fact version (design spec §6.2/§6.4).
func TestPromptVersionDefault(t *testing.T) {
	if PromptVersionDefault != "p4.1" {
		t.Fatalf("PromptVersionDefault = %q, want p4.1", PromptVersionDefault)
	}
}

// TestBuildFilePromptContainsSchemaAndPath pins the design spec §8.3 contract:
// the marker the fake LLM matches, the file locator and the output schema.
func TestBuildFilePromptContainsSchemaAndPath(t *testing.T) {
	system, user := BuildFilePrompt("web", "abcdef1234567890", "src/a.ts", []byte("export const x = 1"))

	if !strings.Contains(system, "EzTest 的只读代码分析器") {
		t.Fatalf("system prompt is missing the 只读代码分析器 marker: %q", system)
	}
	for _, marker := range []string{
		"route/page/api/client_call/component/module/symbol",
		"1-based",
		"JSON",
	} {
		if !strings.Contains(system, marker) {
			t.Fatalf("system prompt is missing %q", marker)
		}
	}

	for _, marker := range []string{
		"仓库：web",
		"提交：abcdef1234567890",
		"文件：src/a.ts",
		"文件总行数：1",
		"export const x = 1",
		`"changeSummary"`,
		`"facts"`,
		`"edges"`,
		`"route|page|api|client_call|component|module|symbol"`,
		`"high|medium|low"`,
		`"contains|imports|defines|calls|renders|route_to_api"`,
		`"startLine": 1, "endLine": 1`,
		"<file-content>",
	} {
		if !strings.Contains(user, marker) {
			t.Fatalf("user prompt is missing %q\nuser prompt:\n%s", marker, user)
		}
	}
	if !strings.Contains(user, `"path": "src/a.ts"`) {
		t.Fatalf("user prompt does not pin the schema path:\n%s", user)
	}
}

// TestBuildFilePromptNumbersLines prints 1-based line numbers so the model can
// cite evidence, and normalises CRLF.
func TestBuildFilePromptNumbersLines(t *testing.T) {
	_, user := BuildFilePrompt("web", "abc", "src/a.ts", []byte("first\r\nsecond"))

	if !strings.Contains(user, "1 | first") {
		t.Fatalf("user prompt is missing numbered line 1:\n%s", user)
	}
	if !strings.Contains(user, "2 | second") {
		t.Fatalf("user prompt is missing numbered line 2:\n%s", user)
	}
	if strings.Contains(user, "\r") {
		t.Fatalf("user prompt still contains CR bytes:\n%s", user)
	}
}

// TestBuildFilePromptCapsAt2000Lines proves oversized files are truncated with
// an explicit marker while the true total line count is still reported.
func TestBuildFilePromptCapsAt2000Lines(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 2500; i++ {
		fmt.Fprintf(&b, "line-%d\n", i)
	}
	_, user := BuildFilePrompt("web", "abc", "src/big.ts", []byte(b.String()))

	if !strings.Contains(user, "文件总行数：2500") {
		t.Fatalf("user prompt reports the wrong total line count:\n%s", user)
	}
	if !strings.Contains(user, "2000 | line-2000") {
		t.Fatalf("user prompt is missing line 2000")
	}
	if strings.Contains(user, "line-2001") {
		t.Fatalf("user prompt includes content past the 2000-line cap")
	}
	if !strings.Contains(user, "截断") {
		t.Fatalf("user prompt is missing the truncation marker")
	}
}

// TestBuildImpactPromptContainsSectionsAndSchema pins design spec §8.4: the
// system marker, the four input sections and the output schema.
func TestBuildImpactPromptContainsSectionsAndSchema(t *testing.T) {
	in := ImpactPromptInput{
		Requirements: []RequirementInfo{{
			ID:          1844,
			Title:       "支持订单退款",
			Description: "用户可对已支付订单发起退款",
			Status:      "active",
		}},
		ChangedFiles: []domain.ChangedFile{{
			Path:   "src/api/refund.ts",
			Status: "M",
		}},
		Facts: []AIFact{{
			Kind:       "client_call",
			Name:       "POST /api/refund",
			Path:       "src/api/refund.ts",
			Summary:    "发起退款请求",
			Confidence: "high",
			Evidence:   []domain.Evidence{{File: "src/api/refund.ts", StartLine: 2, EndLine: 4}},
		}},
		ChangeSummary: "调整退款接口调用",
	}

	system, user := BuildImpactPrompt(in)

	if !strings.Contains(system, "EzTest 的只读变更影响分析器") {
		t.Fatalf("system prompt is missing the 只读变更影响分析器 marker: %q", system)
	}
	for _, marker := range []string{
		"版本需求清单",
		"1844",
		"支持订单退款",
		"用户可对已支付订单发起退款",
		"变更文件",
		"src/api/refund.ts",
		"M",
		"变更事实",
		"client_call",
		"POST /api/refund",
		"发起退款请求",
		"high",
		"变更摘要",
		"调整退款接口调用",
		`"affected"`,
		`"requirementId"`,
		`"reason"`,
		`"high|medium|low"`,
	} {
		if !strings.Contains(user, marker) {
			t.Fatalf("impact user prompt is missing %q\nuser prompt:\n%s", marker, user)
		}
	}
}

// TestBuildImpactPromptCapsRequirements proves the prompt keeps at most the
// configured requirement bound (design spec §6.6).
func TestBuildImpactPromptCapsRequirements(t *testing.T) {
	input := ImpactPromptInput{
		Requirements: make([]RequirementInfo, 0, domain.MaxRequirementsInPrompt+5),
	}
	for i := 0; i < domain.MaxRequirementsInPrompt+5; i++ {
		input.Requirements = append(input.Requirements, RequirementInfo{
			ID:    int64(10000 + i),
			Title: fmt.Sprintf("需求-%d", 10000+i),
		})
	}

	_, user := BuildImpactPrompt(input)
	if !strings.Contains(user, "需求-10099") {
		t.Fatalf("impact prompt is missing the last accepted requirement")
	}
	if strings.Contains(user, "需求-10100") {
		t.Fatalf("impact prompt includes a requirement past the %d cap", domain.MaxRequirementsInPrompt)
	}
}
