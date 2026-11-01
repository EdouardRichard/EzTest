package render

// reportTemplate is the fixed, built-in HTML template of report template
// version "1" (spec §6.2). It is never user editable. The document is fully
// self-contained: styles are inline and nothing references an external asset.
// All runtime strings are printed through html/template's context-aware
// escaping; user-provided text is never wrapped in template.HTML.
const reportTemplate = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
:root { color-scheme: light; }
* { box-sizing: border-box; }
body { margin: 0; padding: 32px 24px; background: #ffffff; color: #1f2328; font-family: system-ui, -apple-system, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif; font-size: 14px; line-height: 1.5; }
main { max-width: 1100px; margin: 0 auto; }
h1 { margin: 0 0 12px; font-size: 24px; line-height: 1.3; }
h2 { margin: 0 0 14px; padding-bottom: 6px; border-bottom: 1px solid #d0d7de; font-size: 18px; }
h3 { margin: 20px 0 10px; font-size: 15px; }
section { margin: 0 0 28px; padding-top: 20px; border-top: 1px solid #d0d7de; }
.meta { display: flex; flex-wrap: wrap; gap: 6px 24px; margin: 0; color: #59636e; }
.meta div { display: flex; gap: 6px; }
.meta dt { font-weight: 600; }
.meta dd { margin: 0; }
table { width: 100%; margin: 0 0 16px; border-collapse: collapse; }
th, td { padding: 6px 8px; border-bottom: 1px solid #d0d7de; text-align: left; vertical-align: top; }
th { background: #f6f8fa; color: #59636e; font-weight: 600; }
.num { text-align: right; font-variant-numeric: tabular-nums; }
.mono { font-family: ui-monospace, SFMono-Regular, Consolas, monospace; font-size: 12px; word-break: break-all; }
.empty { margin: 0 0 16px; color: #59636e; font-style: italic; }
.truncation { margin: 0 0 20px; padding: 12px 16px; border: 1px solid #d4a72c; background: #fff8c5; }
.truncation p { margin: 6px 0 0; }
.narrative { margin: 0 0 16px; padding: 12px 16px; border-left: 3px solid #0969da; background: #f6f8fa; }
.narrative-line { margin: 0; white-space: pre-wrap; }
footer { margin-top: 32px; padding-top: 12px; border-top: 1px solid #d0d7de; color: #59636e; font-size: 12px; }
</style>
</head>
<body>
<main data-template-version="{{.TemplateVersion}}">
<header class="report-header">
<h1>{{.Title}}</h1>
<dl class="meta">
<div><dt>报告类型</dt><dd>{{.KindLabel}}</dd></div>
<div><dt>版本</dt><dd>#{{.VersionID}}</dd></div>
{{if .HasWave}}<div><dt>波次</dt><dd>#{{.WaveID}}</dd></div>{{end}}
<div><dt>生成时间</dt><dd>{{.GeneratedAt}}</dd></div>
</dl>
</header>
{{if .Truncated}}<aside class="truncation" data-truncated="true"><strong>报告截断说明</strong><p>{{.TruncationNote}}</p></aside>{{end}}
{{if .Sections.Summary}}
<section id="summary" data-section="summary">
<h2>概览</h2>
<table class="counts">
<thead><tr><th>执行总数</th><th>通过</th><th>失败</th><th>受阻</th><th>不确定</th><th>跳过</th><th>其他</th></tr></thead>
<tbody><tr><td class="num">{{.Summary.Total}}</td><td class="num">{{.Summary.Passed}}</td><td class="num">{{.Summary.Failed}}</td><td class="num">{{.Summary.Blocked}}</td><td class="num">{{.Summary.Inconclusive}}</td><td class="num">{{.Summary.Skipped}}</td><td class="num">{{.Summary.Other}}</td></tr></tbody>
</table>
<table class="counts">
<thead><tr><th>问题</th><th>未关闭问题</th><th>证据</th><th>波次</th><th>需求</th><th>已闭环需求</th></tr></thead>
<tbody><tr><td class="num">{{.Summary.Issues}}</td><td class="num">{{.Summary.OpenIssues}}</td><td class="num">{{.Summary.Evidence}}</td><td class="num">{{.Summary.Waves}}</td><td class="num">{{.Summary.Requirements}}</td><td class="num">{{.Summary.RequirementsClosed}}</td></tr></tbody>
</table>
{{if .HasNarrative}}
<div class="narrative" data-narrative="candidate">
<h3>AI 叙述（候选）</h3>
{{range .NarrativeLines}}<p class="narrative-line">{{if .}}{{.}}{{else}}&nbsp;{{end}}</p>
{{end}}
</div>
{{end}}
{{if .Requirements}}
<h3>需求</h3>
<table>
<thead><tr><th>ID</th><th>标题</th><th>状态</th><th>闭环</th><th>闭环轮次</th><th>重开次数</th><th>关闭时间</th></tr></thead>
<tbody>
{{range .Requirements}}<tr data-requirement-id="{{.ID}}"><td class="num">{{.ID}}</td><td>{{.Title}}</td><td>{{.Status}}</td><td>{{int64ptr .ClosureID}}</td><td class="num">{{.CloseRound}}</td><td class="num">{{.ReopenCount}}</td><td>{{timeptr .ClosedAt}}</td></tr>
{{end}}
</tbody>
</table>
{{end}}
</section>
{{end}}
{{if .Sections.Results}}
<section id="results" data-section="results">
<h2>结果</h2>
{{if .Waves}}
<h3>波次</h3>
<table>
<thead><tr><th>ID</th><th>状态</th><th>触发</th><th>总数</th><th>通过</th><th>失败</th><th>受阻</th><th>不确定</th><th>报告</th><th>结束时间</th></tr></thead>
<tbody>
{{range .Waves}}<tr data-wave-id="{{.ID}}"><td class="num">{{.ID}}</td><td>{{.Status}}</td><td>{{.Trigger}}</td><td class="num">{{.Counts.Total}}</td><td class="num">{{.Counts.Passed}}</td><td class="num">{{.Counts.Failed}}</td><td class="num">{{.Counts.Blocked}}</td><td class="num">{{.Counts.Inconclusive}}</td><td>{{int64ptr .ReportID}}</td><td>{{timeptr .FinishedAt}}</td></tr>
{{end}}
</tbody>
</table>
{{if .SUTRows}}
<h3>按版本 SUT 统计</h3>
<table>
<thead><tr><th>波次</th><th>版本 SUT</th><th>总数</th><th>通过</th><th>失败</th><th>受阻</th><th>不确定</th></tr></thead>
<tbody>
{{range .SUTRows}}<tr data-sut-row="{{.WaveID}}-{{.VersionSUTID}}"><td class="num">{{.WaveID}}</td><td>{{.Label}}</td><td class="num">{{.Total}}</td><td class="num">{{.Passed}}</td><td class="num">{{.Failed}}</td><td class="num">{{.Blocked}}</td><td class="num">{{.Inconclusive}}</td></tr>
{{end}}
</tbody>
</table>
{{end}}
{{end}}
{{if .CaseRuns}}
<h3>执行结果</h3>
<table>
<thead><tr><th>ID</th><th>波次</th><th>用例</th><th>状态</th><th>失败分类</th><th>分类</th><th>严重度</th><th>步骤（失败/总数）</th><th>断言（失败/总数）</th><th>摘要</th><th>证据</th></tr></thead>
<tbody>
{{range .CaseRuns}}<tr data-case-run-id="{{.ID}}"><td class="num">{{.ID}}</td><td class="num">{{.WaveID}}</td><td>{{.CaseTitle}}</td><td>{{.Status}}</td><td>{{.FailureClass}}</td><td>{{.Category}}</td><td>{{.Severity}}</td><td class="num">{{.StepFailedCount}}/{{.StepCount}}</td><td class="num">{{.AssertionFailedCount}}/{{.AssertionCount}}</td><td>{{.Summary}}</td><td class="num">{{len .Evidence}}</td></tr>
{{end}}
</tbody>
</table>
{{else}}<p class="empty">无执行结果（空数据）</p>{{end}}
</section>
{{end}}
{{if .Sections.Issues}}
<section id="issues" data-section="issues">
<h2>问题</h2>
{{if .Issues}}
<table>
<thead><tr><th>ID</th><th>标题</th><th>状态</th><th>分类</th><th>严重度</th><th>来源</th><th>合并来源数</th><th>关联执行数</th></tr></thead>
<tbody>
{{range .Issues}}<tr data-issue-id="{{.ID}}"><td class="num">{{.ID}}</td><td>{{.Title}}</td><td>{{.Status}}</td><td>{{.Category}}</td><td>{{.Severity}}</td><td>{{.Source}}</td><td class="num">{{len .SourceIssueIDs}}</td><td class="num">{{len .CaseRunIDs}}</td></tr>
{{end}}
</tbody>
</table>
{{else}}<p class="empty">无问题（空数据）</p>{{end}}
</section>
{{end}}
{{if .Sections.Coverage}}
<section id="coverage" data-section="coverage">
<h2>覆盖</h2>
<table class="counts">
<thead><tr><th>需求总数</th><th>已覆盖需求</th><th>用例版本总数</th><th>已覆盖用例版本</th></tr></thead>
<tbody><tr><td class="num">{{.Coverage.RequirementsTotal}}</td><td class="num">{{.Coverage.RequirementsCovered}}</td><td class="num">{{.Coverage.CaseVersionsTotal}}</td><td class="num">{{.Coverage.CaseVersionsCovered}}</td></tr></tbody>
</table>
{{if .Coverage.Gaps}}
<h3>覆盖缺口</h3>
<table>
<thead><tr><th>需求 ID</th><th>用例版本 ID</th><th>原因</th></tr></thead>
<tbody>
{{range .Coverage.Gaps}}<tr data-gap-id="{{.RequirementID}}-{{.CaseVersionID}}"><td class="num">{{.RequirementID}}</td><td class="num">{{.CaseVersionID}}</td><td>{{.Reason}}</td></tr>
{{end}}
</tbody>
</table>
{{else}}<p class="empty">无覆盖缺口</p>{{end}}
</section>
{{end}}
{{if .Sections.Evidence}}
<section id="evidence" data-section="evidence">
<h2>证据</h2>
{{if .EvidenceRows}}
<table>
<thead><tr><th>ID</th><th>执行 ID</th><th>类型</th><th>版本 SUT</th><th>步骤</th><th>断言</th><th>SHA-256</th><th>大小（字节）</th></tr></thead>
<tbody>
{{range .EvidenceRows}}<tr data-evidence-id="{{.ID}}"><td class="num">{{.ID}}</td><td class="num">{{.CaseRunID}}</td><td>{{.Kind}}</td><td>{{int64ptr .VersionSUTID}}</td><td>{{intptr .StepSeq}}</td><td>{{intptr .AssertionSeq}}</td><td class="mono">{{.SHA256}}</td><td class="num">{{.SizeBytes}}</td></tr>
{{end}}
</tbody>
</table>
{{else}}<p class="empty">无证据（空数据）</p>{{end}}
</section>
{{end}}
<footer>本报告由 EzTest 自动生成。统计数字来自确定性报告快照；AI 叙述仅为候选文本，不改变确定性数据。</footer>
</main>
</body>
</html>
`
