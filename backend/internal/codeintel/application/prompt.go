package application

import (
	"fmt"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// PromptVersionDefault is the P4.1 prompt version (design spec §6.2/§8.3).
// EZTEST_CODEINTEL_PROMPT_VERSION may override it; the effective value
// participates in the file-analysis cache key and in fact versioning.
const PromptVersionDefault = "p4.1"

// maxPromptFileLines bounds the numbered file content of one file-analysis
// prompt (design spec §8.3: "{编号行文本，最多 2000 行}"). The cut is reported
// inside the prompt so the model knows the file continues.
const maxPromptFileLines = 2000

// Bounds applied to the free-text parts of the impact prompt so one large
// version cannot produce an unbounded request.
const (
	maxImpactTitleRunes       = 200
	maxImpactDescriptionRunes = 500
)

// fileAnalysisSystem is the verbatim T1 system prompt of design spec §8.3. The
// "EzTest 的只读代码分析器" marker is the contract the deterministic fake LLM
// matches at acceptance time.
const fileAnalysisSystem = `你是 EzTest 的只读代码分析器。用户会给出一个源码文件（某次 Git 提交中的版本）及其路径。
你的任务：提取与页面/路由/接口/组件/依赖相关的结构化事实，并概括该文件的变更语义。
规则：
1. 只依据给定文件内容判断；无法确定的事实不要输出，禁止猜测或编造。
2. 每条事实必须能定位到文件中的证据行（1-based 闭区间）；证据必须来自给定文件。
3. kind 只能是 route/page/api/client_call/component/module/symbol 之一；不假设语言或框架。
4. 路由/接口/调用点必须使用代码中实际出现的路径与方法字符串；方法统一大写。
5. 置信度：证据充分且直读为 high；由代码推断为 medium；不确定为 low。
6. changeSummary 说明"这个文件改了什么/负责什么"，不臆造提交信息。
7. 只输出一个 JSON 对象，符合用户给出的 schema；不要 Markdown 代码围栏或解释文字。`

// impactAnalysisSystem is the verbatim impact-reasoning system prompt of design
// spec §8.4. The "EzTest 的只读变更影响分析器" marker is the contract the
// deterministic fake LLM matches at acceptance time.
const impactAnalysisSystem = `你是 EzTest 的只读变更影响分析器。输入是一次代码变更的结构化事实摘要与版本需求清单。
任务：判断哪些需求可能受该变更影响。
规则：
1. 只依据给定事实与需求文本推理；不要引用未给出的代码或需求。
2. 每条受影响需求必须给出 reason（≤300 字）与证据（只能是给定变更文件/事实证据中的 file + 行号区间）。
3. 无法判断时返回空数组；不确定时 confidence=low。
4. requirementId 必须使用给定清单中的十进制字符串 ID，不得编造。
5. 只输出一个 JSON 对象，符合 schema；不要 Markdown 或解释。`

// fileAnalysisSchema is the verbatim user-message schema of design spec §8.3.
// The three %s verbs receive the repository-relative file path.
const fileAnalysisSchema = `{
  "changeSummary": "string",
  "facts": [{
    "kind": "route|page|api|client_call|component|module|symbol",
    "name": "string",
    "path": "%s",
    "startLine": 1, "endLine": 1,
    "signature": "string",
    "confidence": "high|medium|low",
    "summary": "string",
    "evidence": [{"file": "%s", "startLine": 1, "endLine": 1}]
  }],
  "edges": [{
    "from": {"kind": "component", "name": "string"},
    "to": {"kind": "route", "name": "string"},
    "rel": "contains|imports|defines|calls|renders|route_to_api",
    "confidence": "high|medium|low",
    "evidence": [{"file": "%s", "startLine": 1, "endLine": 1}]
  }]
}`

// impactAnalysisSchema is the verbatim user-message schema of design spec §8.4.
const impactAnalysisSchema = `{
  "affected": [{
    "requirementId": "1844...",
    "reason": "string",
    "confidence": "high|medium|low",
    "evidence": [{"file": "string", "startLine": 1, "endLine": 1}]
  }]
}`

// ImpactPromptInput is the input of one AI impact-reasoning prompt (design spec
// §6.6/§8.4). ChangedFiles are the T0 change-set entries; Facts are the current
// (already evidence-validated) facts of the analysed files.
type ImpactPromptInput struct {
	Requirements  []RequirementInfo
	ChangedFiles  []domain.ChangedFile
	Facts         []AIFact
	ChangeSummary string
}

// BuildFilePrompt returns the system and user prompt for one file of the
// analysed commit. The user message contains a 1-based numbered copy of the
// file content (at most maxPromptFileLines lines, with an explicit truncation
// marker) and the output JSON schema of design spec §8.3. It never includes
// another file and never includes another repository's content.
//
// The fixed signature carries the repository key only, so the user message
// names the repository by that key (design spec §8.3 names "{repoKey}
// （{repoName}）"; the display name is not available at this layer).
func BuildFilePrompt(repoKey, commit, path string, content []byte) (system, user string) {
	lines := splitPromptLines(content)
	totalLines := len(lines)
	shown := lines
	if len(shown) > maxPromptFileLines {
		shown = shown[:maxPromptFileLines]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "仓库：%s\n", repoKey)
	fmt.Fprintf(&b, "提交：%s\n", commit)
	fmt.Fprintf(&b, "文件：%s\n", path)
	fmt.Fprintf(&b, "文件总行数：%d\n", totalLines)
	b.WriteString("文件内容（行号见左侧，仅用于定位证据）：\n")
	b.WriteString("<file-content>\n")
	for index, line := range shown {
		fmt.Fprintf(&b, "%d | %s\n", index+1, line)
	}
	if totalLines > len(shown) {
		fmt.Fprintf(&b, "……（已截断：仅显示前 %d 行，共 %d 行）\n", len(shown), totalLines)
	}
	b.WriteString("</file-content>\n\n")
	b.WriteString("输出 JSON schema：\n")
	b.WriteString(fmt.Sprintf(fileAnalysisSchema, path, path, path))

	return fileAnalysisSystem, b.String()
}

// BuildImpactPrompt returns the system and user prompt for one AI impact
// reasoning call (design spec §8.4). The user message carries the version
// requirement list (bounded by domain.MaxRequirementsInPrompt), the changed
// files, the analysed facts and the change summary, followed by the output
// schema. Facts carry only kind/name/path/summary/confidence/file:line — never
// code content beyond what the prompt sections require.
func BuildImpactPrompt(in ImpactPromptInput) (system, user string) {
	requirements := in.Requirements
	if len(requirements) > domain.MaxRequirementsInPrompt {
		requirements = requirements[:domain.MaxRequirementsInPrompt]
	}

	var b strings.Builder
	b.WriteString("版本需求清单（id | 标题 | 描述摘要）：\n")
	if len(requirements) == 0 {
		b.WriteString("(无)\n")
	}
	for _, requirement := range requirements {
		fmt.Fprintf(&b, "%d | %s | %s\n",
			requirement.ID,
			truncateRunes(requirement.Title, maxImpactTitleRunes),
			truncateRunes(requirement.Description, maxImpactDescriptionRunes))
	}
	if len(in.Requirements) > len(requirements) {
		fmt.Fprintf(&b, "……（需求过多，仅显示前 %d 条）\n", len(requirements))
	}

	b.WriteString("\n变更文件（path | status）：\n")
	if len(in.ChangedFiles) == 0 {
		b.WriteString("(无)\n")
	}
	for _, file := range in.ChangedFiles {
		fmt.Fprintf(&b, "%s | %s\n", file.Path, file.Status)
	}

	b.WriteString("\n变更事实（kind | name | path | summary | confidence | evidence）：\n")
	if len(in.Facts) == 0 {
		b.WriteString("(无)\n")
	}
	for _, fact := range in.Facts {
		fmt.Fprintf(&b, "%s | %s | %s | %s | %s | %s\n",
			fact.Kind,
			fact.Name,
			fact.Path,
			emptyDash(fact.Summary),
			fact.Confidence,
			formatPromptEvidence(fact.Evidence))
	}

	b.WriteString("\n变更摘要：\n")
	b.WriteString(in.ChangeSummary)
	b.WriteString("\n\n输出 JSON schema：\n")
	b.WriteString(impactAnalysisSchema)

	return impactAnalysisSystem, b.String()
}

// splitPromptLines normalises file content for prompt rendering: split on "\n",
// strip a trailing "\r" from every line (CRLF -> LF) and drop the implicit
// empty line produced by a trailing newline.
func splitPromptLines(content []byte) []string {
	text := string(content)
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" && strings.HasSuffix(text, "\n") {
		lines = lines[:len(lines)-1]
	}
	for index, line := range lines {
		lines[index] = strings.TrimSuffix(line, "\r")
	}
	return lines
}

// formatPromptEvidence renders evidence as file:startLine-endLine entries.
func formatPromptEvidence(evidence []domain.Evidence) string {
	if len(evidence) == 0 {
		return "(无)"
	}
	parts := make([]string, 0, len(evidence))
	for _, ev := range evidence {
		if ev.StartLine <= 0 || ev.EndLine < ev.StartLine {
			parts = append(parts, ev.File)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s:%d-%d", ev.File, ev.StartLine, ev.EndLine))
	}
	return strings.Join(parts, "; ")
}

// truncateRunes cuts s to at most max runes and marks the cut with an ellipsis.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// emptyDash renders an empty prompt field as "-".
func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
