package render

// reportTemplateV3 is the frozen, built-in HTML template of report template
// version "3" (v0.2.4-G SC-G1/G2, the human-useful fixed report). It is never
// user editable. The document is fully self-contained: styles are inline and
// nothing loads an external asset (the evidence links navigate to the existing
// content endpoint, they do not embed remote bytes). All runtime strings are
// printed through html/template's context-aware escaping; user-provided text is
// never wrapped in template.HTML.
//
// The structure is FIXED and organized by defect (SC-G1 I3): overview (version
// info + AC verification coverage rate + execution stats + defect summary +
// deterministic conclusion + revision/fingerprint/time + fixed Chinese term
// definitions) → five-element defect cards (关联需求/执行步骤/验收标准/预期/实际,
// fixed order, with natural-language context and viewable evidence) → non-issue
// failure explanations → traceability chain → evidence appendix → AC verification
// matrix (with judge display and failed-run hints) → waived content. The core
// sections are non-omittable (the renderer forces them on for v3). The AI
// narrative stays an isolated candidate block; the application guard keeps it
// from contradicting the deterministic data.
//
// T10: template version 3 is frozen byte-for-byte. TestTemplateV3Golden pins its
// SHA-256; the card field order and the term definitions are part of the contract.
const reportTemplateV3 = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
:root { color-scheme: light; }
* { box-sizing: border-box; }
body { margin: 0; padding: 32px 24px; background: #ffffff; color: #1f2328; font-family: system-ui, -apple-system, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif; font-size: 14px; line-height: 1.6; }
main { max-width: 1100px; margin: 0 auto; }
h1 { margin: 0 0 12px; font-size: 24px; line-height: 1.3; }
h2 { margin: 0 0 14px; padding-bottom: 6px; border-bottom: 1px solid #d0d7de; font-size: 18px; }
h3 { margin: 18px 0 10px; font-size: 15px; }
h4 { margin: 12px 0 6px; font-size: 14px; }
section { margin: 0 0 28px; padding-top: 20px; border-top: 1px solid #d0d7de; }
.meta { display: flex; flex-wrap: wrap; gap: 6px 24px; margin: 0 0 12px; color: #59636e; }
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
.conclusion { margin: 0 0 16px; padding: 12px 16px; border-left: 4px solid #0969da; background: #f6f8fa; font-weight: 600; }
.conclusion[data-conclusion="defect"] { border-left-color: #cf222e; background: #ffebe9; }
.conclusion[data-conclusion="unverified"] { border-left-color: #d4a72c; background: #fff8c5; }
.conclusion[data-conclusion="ok"] { border-left-color: #1a7f37; background: #dafbe1; }
.terms { margin: 0 0 16px; }
.terms dt { font-weight: 600; margin-top: 6px; }
.terms dd { margin: 0 0 4px; color: #59636e; }
.narrative { margin: 0 0 16px; padding: 12px 16px; border-left: 3px solid #0969da; background: #f6f8fa; }
.narrative-line { margin: 0; white-space: pre-wrap; }
.defect-card { margin: 0 0 20px; padding: 16px; border: 1px solid #d0d7de; border-radius: 6px; }
.defect-card > header { display: flex; flex-wrap: wrap; gap: 6px 16px; align-items: baseline; margin: 0 0 10px; }
.defect-card h3 { margin: 0; font-size: 16px; }
.card-context { margin: 0 0 12px; padding: 10px 12px; background: #f6f8fa; border-radius: 4px; }
.card-context p { margin: 2px 0; }
.card-element { margin: 0 0 10px; padding: 8px 12px; border-left: 3px solid #d0d7de; }
.card-element > h4 { margin: 0 0 6px; color: #59636e; }
.card-element ol, .card-element ul { margin: 4px 0; padding-left: 20px; }
.evidence-link { color: #0969da; text-decoration: none; }
.evidence-link:hover { text-decoration: underline; }
.ac-failed-hint { display: block; margin-top: 4px; padding: 6px 8px; border: 1px solid #d4a72c; background: #fff8c5; color: #7d4e00; }
.state { font-weight: 600; }
.state-verified { color: #1a7f37; }
.state-unverified { color: #7d4e00; }
.state-failed { color: #cf222e; }
.state-uncovered { color: #59636e; }
.judge { margin: 12px 0; padding: 12px; border: 1px dashed #8250df; border-radius: 4px; }
.judge-findings { margin: 6px 0; padding-left: 20px; }
.badge { display: inline-block; padding: 2px 8px; border-radius: 10px; background: #fff8c5; border: 1px solid #d4a72c; color: #7d4e00; font-weight: 600; }
.waived { margin: 0 0 16px; padding: 12px 16px; border: 1px solid #d4a72c; background: #fff8c5; }
.wave-env { margin: 0 0 16px; padding: 12px; border: 1px solid #d0d7de; border-radius: 4px; }
.env-unverifiable { margin: 6px 0; padding: 8px 12px; border: 1px solid #d4a72c; background: #fff8c5; color: #7d4e00; }
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
<section id="overview" data-section="summary">
<h2>概览</h2>
<dl class="meta">
<div><dt>报告 ID</dt><dd>{{if .ReportID}}#{{.ReportID}}{{else}}—{{end}}</dd></div>
<div><dt>报告版本</dt><dd>{{if .ReportRevision}}r{{.ReportRevision}}{{else}}—{{end}}</dd></div>
<div><dt>输入指纹</dt><dd class="mono">{{if .InputFingerprint}}{{.InputFingerprint}}{{else}}—{{end}}</dd></div>
<div><dt>模板版本</dt><dd>{{.TemplateVersion}}</dd></div>
<div><dt>闭环方式</dt><dd>{{closureKindLabel .ClosureKind}}</dd></div>
</dl>
<p class="ac-rate">{{.ACRate}}</p>
<h3>执行统计</h3>
<table class="counts">
<thead><tr><th>执行总数</th><th>通过</th><th>失败</th><th>受阻</th><th>不确定</th><th>跳过</th><th>其他</th></tr></thead>
<tbody><tr><td class="num">{{.Summary.Total}}</td><td class="num">{{.Summary.Passed}}</td><td class="num">{{.Summary.Failed}}</td><td class="num">{{.Summary.Blocked}}</td><td class="num">{{.Summary.Inconclusive}}</td><td class="num">{{.Summary.Skipped}}</td><td class="num">{{.Summary.Other}}</td></tr></tbody>
</table>
<table class="counts">
<thead><tr><th>问题</th><th>未关闭问题</th><th>证据</th><th>需求</th><th>已闭环需求</th></tr></thead>
<tbody><tr><td class="num">{{.Summary.Issues}}</td><td class="num">{{.Summary.OpenIssues}}</td><td class="num">{{.Summary.Evidence}}</td><td class="num">{{.Summary.Requirements}}</td><td class="num">{{.Summary.RequirementsClosed}}</td></tr></tbody>
</table>
<h3>结论</h3>
<p class="conclusion" data-conclusion="{{.Conclusion.Level}}">{{.Conclusion.Text}}</p>
<h3>术语释义</h3>
<dl class="terms">
{{range .Terms}}<dt>{{.Term}}</dt><dd>{{.Definition}}</dd>
{{end}}
</dl>
{{if .HasNarrative}}
<div class="narrative" data-narrative="candidate">
<h3>AI 叙述（候选）</h3>
{{range .NarrativeLines}}<p class="narrative-line">{{if .}}{{.}}{{else}}&nbsp;{{end}}</p>
{{end}}
</div>
{{end}}
</section>
<section id="defects" data-section="issues">
<h2>缺陷五要素卡片</h2>
{{if .DefectCards}}
{{range .DefectCards}}
<article class="defect-card" data-issue-id="{{.IssueID}}">
<header><h3>{{.Title}}</h3><span>问题 #{{.IssueID}}</span><span>状态：{{issueStatusLabel .Status}}</span><span>分类：{{issueCategoryLabel .Category}}</span><span>严重度：{{severityLabel .Severity}}</span></header>
<div class="card-context">
{{if .RepTruncated}}<p class="empty" data-rep-truncated="true">代表执行 #{{.RepresentativeRunID}} 不在本次显示范围（已截断）{{if .ContextRunID}}；需求/验收标准按关联执行 #{{.ContextRunID}} 定位{{end}}。</p>{{end}}
{{if .CaseTitle}}<p><strong>用例：</strong>{{.CaseTitle}}</p>{{end}}
{{if .CaseDescription}}<p><strong>用例说明：</strong>{{.CaseDescription}}</p>{{end}}
{{if .CasePreconditions}}<p><strong>前置条件：</strong>{{.CasePreconditions}}</p>{{end}}
{{if .SUTName}}<p><strong>被测系统：</strong>{{.SUTName}}</p>{{end}}
{{if .SUTDescription}}<p><strong>系统说明：</strong>{{.SUTDescription}}</p>{{end}}
</div>
<div class="card-element" data-element="requirement">
<h4>① 关联需求</h4>
{{if .HasRequirement}}<p>需求 #{{.RequirementID}}：{{.RequirementTitle}}</p>{{if .UserStory}}<p>{{.UserStory}}</p>{{end}}{{else if .ContextTruncated}}<p class="empty">关联执行不在本次显示范围（已截断），无法定位关联需求。</p>{{else}}<p class="empty">无关联需求</p>{{end}}
</div>
<div class="card-element" data-element="steps">
<h4>② 执行步骤</h4>
{{if .Steps}}<ol>{{range .Steps}}<li>步骤 {{.Seq}}：{{.Action}}{{if .TargetDescription}} —— 目标：{{.TargetDescription}}{{end}}{{if .Expected}}（预期：{{.Expected}}）{{end}}</li>{{end}}</ol>{{else if or .RepTruncated .ContextTruncated}}<p class="empty">代表执行不在本次显示范围（已截断），执行步骤不可显示。</p>{{else}}<p class="empty">无步骤信息</p>{{end}}
</div>
<div class="card-element" data-element="ac">
<h4>③ 验收标准（AC）</h4>
{{if .ACs}}<ul>{{range .ACs}}<li><span class="mono">{{.ACID}}</span>：给定 {{.Given}}；当 {{.When}}；则 {{.Then}}（<span class="state state-{{.State}}">{{acStateLabel .State}}</span>）</li>{{end}}</ul>{{else if or .RepTruncated .ContextTruncated}}<p class="empty">代表执行不在本次显示范围（已截断），验收标准定位不完整。</p>{{else}}<p class="empty">无关联验收标准</p>{{end}}
</div>
<div class="card-element" data-element="expected">
<h4>④ 预期结果</h4>
<p>{{if .Expected}}{{.Expected}}{{else}}—{{end}}</p>
</div>
<div class="card-element" data-element="actual">
<h4>⑤ 实际结果</h4>
<p>{{if .Actual}}{{.Actual}}{{else}}—{{end}}</p>
{{if .Reason}}<p><strong>失败原因：</strong>{{.Reason}}</p>{{end}}
{{if .Evidence}}<p><strong>可看证据：</strong></p><ul>{{range .Evidence}}<li>{{if .ContentPath}}<a class="evidence-link" href="{{.ContentPath}}">查看证据 #{{.ID}}（{{.Kind}}）</a>{{else}}证据 #{{.ID}}（{{.Kind}}）{{end}} <span class="mono">{{.SHA256}}</span></li>{{end}}</ul>{{if .EvidenceTruncated}}<p class="empty" data-evidence-truncated="true">证据超出显示上限（已截断）。</p>{{end}}{{else if .EvidenceTruncated}}<p class="empty" data-evidence-truncated="true">证据超出显示上限（已截断）。</p>{{else}}<p class="empty">无代表 run 证据</p>{{end}}
</div>
<p class="meta"><span>代表执行：{{if .RepresentativeRunID}}case_run #{{.RepresentativeRunID}}{{else}}—{{end}}</span><span>其余关联执行：{{joinInt64s .OtherRunIDs}}</span></p>
</article>
{{end}}
{{else}}<p class="empty">无缺陷（空数据）</p>{{end}}
</section>
<section id="failure-explanations" data-section="failure-explanations">
<h2>非应用缺陷失败说明</h2>
{{if .FailureExplanations}}
<table>
<thead><tr><th>执行 ID</th><th>用例</th><th>状态</th><th>失败分类</th><th>为什么没过</th></tr></thead>
<tbody>
{{range .FailureExplanations}}<tr data-failure-run-id="{{.CaseRunID}}"><td class="num">{{.CaseRunID}}</td><td>{{.CaseTitle}}</td><td>{{runStatusLabel .Status}}</td><td>{{failureClassLabel .EffectiveFailureClass}}</td><td>{{.Explanation}}</td></tr>
{{end}}
</tbody>
</table>
{{else}}<p class="empty">无非应用缺陷失败（空数据）</p>{{end}}
</section>
<section id="traceability" data-section="traceability">
<h2>追溯链</h2>
<p class="empty">需求 → 验收标准 AC → 用例 → case_run → 步骤 → 断言 → 证据</p>
{{if .Traceability}}
<table>
<thead><tr><th>需求</th><th>验收标准</th><th>用例版本</th><th>用例</th><th>执行 ID</th><th>失败步骤</th><th>失败断言</th><th>证据</th></tr></thead>
<tbody>
{{range .Traceability}}<tr data-trace-run-id="{{.CaseRunID}}"><td>{{if .RequirementID}}#{{.RequirementID}} {{.RequirementTitle}}{{else}}—{{end}}</td><td>{{joinStrings .ACIDs}}</td><td class="num">{{.CaseVersionID}}</td><td>{{.CaseTitle}}</td><td class="num">{{.CaseRunID}}</td><td class="num">{{if .StepSeq}}{{.StepSeq}}{{else}}—{{end}}</td><td class="num">{{if .AssertionSeq}}{{.AssertionSeq}}{{else}}—{{end}}</td><td>{{joinInt64s .EvidenceIDs}}</td></tr>
{{end}}
</tbody>
</table>
{{else}}<p class="empty">无追溯链（空数据）</p>{{end}}
</section>
<section id="environment" data-section="environment">
<h2>环境核验</h2>
{{if .Waves}}
{{range .Waves}}
<div class="wave-env" data-wave-id="{{.ID}}">
<h3>波次 #{{.ID}}</h3>
{{if .EnvironmentVerifiable}}
<p>计划环境指纹：<span class="mono">{{if .EnvironmentFingerprint}}{{.EnvironmentFingerprint}}{{else}}—{{end}}</span></p>
<p>计划代码基线：<span class="mono">{{if .CodeBaseline}}{{.CodeBaseline}}{{else}}—{{end}}</span></p>
{{if .PlannedEnvironment}}<ul>{{range .PlannedEnvironment}}<li>SUT #{{.SUTID}}：base_url={{if .BaseURL}}{{.BaseURL}}{{else}}—{{end}}；页面资产={{joinInt64s .PageIDs}}；API资产={{joinInt64s .APIIDs}}；数据库资产={{joinInt64s .DatabaseIDs}}；账号角色={{joinStrings .AccountRoles}}{{if .RepositoryBaselines}}；代码基线={{range .RepositoryBaselines}}仓库#{{.RepoID}}@{{.CommitSHA}} {{end}}{{end}}</li>{{end}}</ul>{{end}}
{{else}}
<p class="env-unverifiable" data-env-unverifiable="true"><strong>环境不可核验</strong>{{if .EnvironmentUnverifiableReason}}：{{.EnvironmentUnverifiableReason}}{{end}}</p>
{{end}}
{{if .EnvironmentDivergences}}
<p><strong>计划 vs 实际执行环境分歧（显式披露，不阻塞发布）：</strong></p>
<table>
<thead><tr><th>执行 ID</th><th>用例版本</th><th>计划环境指纹</th><th>实际环境指纹</th><th>可读差异</th></tr></thead>
<tbody>
{{range .EnvironmentDivergences}}<tr data-divergence-run-id="{{.CaseRunID}}"><td class="num">{{.CaseRunID}}</td><td class="num">{{.CaseVersionID}}</td><td class="mono">{{.PlannedFingerprint}}</td><td class="mono">{{.ActualFingerprint}}</td><td>{{if .Differences}}<ul>{{range .Differences}}<li>{{.}}</li>{{end}}</ul>{{else}}—{{end}}</td></tr>
{{end}}
</tbody>
</table>
{{end}}
</div>
{{end}}
{{else}}<p class="empty">无波次环境信息（空数据）</p>{{end}}
</section>
<section id="evidence" data-section="evidence">
<h2>证据附录</h2>
<p class="meta"><span>证据总数（截断前）：{{.EvidenceTotalCount}}</span></p>
{{if .EvidenceLinks}}
<table>
<thead><tr><th>ID</th><th>执行 ID</th><th>类型</th><th>可看证据</th><th>SHA-256</th></tr></thead>
<tbody>
{{range .EvidenceLinks}}<tr data-evidence-id="{{.ID}}"><td class="num">{{.ID}}</td><td class="num">{{.CaseRunID}}</td><td>{{.Kind}}</td><td>{{if .ContentPath}}<a class="evidence-link" href="{{.ContentPath}}">查看证据</a>{{else}}—{{end}}</td><td class="mono">{{.SHA256}}</td></tr>
{{end}}
</tbody>
</table>
{{else}}<p class="empty">无证据（空数据）</p>{{end}}
</section>
<section id="ac-matrix" data-section="coverage">
<h2>AC 验证矩阵</h2>
<p class="ac-rate">{{.ACRate}}</p>
{{if .ACVerification.Truncated}}<aside class="truncation" data-ac-truncated="true"><strong>AC 矩阵截断说明</strong><p>AC 矩阵规模超过显示上限，已显式截断：显示 {{.ACVerification.Displayed}} 条，截断前共 {{.ACVerification.TotalBeforeCap}} 条。统计计数仍反映全集。</p></aside>{{end}}
{{if .ACMatrixRenderTruncated}}<aside class="truncation" data-ac-render-truncated="true"><strong>AC 矩阵渲染截断说明</strong><p>AC 矩阵超过渲染显示上限，已显式截断：显示 {{len .ACMatrix}} 条，共 {{.ACMatrixRenderTotal}} 条。验证状态统计仍反映全集。</p></aside>{{end}}
{{if .ACMatrix}}
<table>
<thead><tr><th>需求</th><th>AC</th><th>给定</th><th>当</th><th>则</th><th>验证状态</th><th>覆盖用例版本</th></tr></thead>
<tbody>
{{range .ACMatrix}}<tr data-ac-id="{{.ACID}}"><td class="num">{{.RequirementID}}</td><td class="mono">{{.ACID}}</td><td>{{.Given}}</td><td>{{.When}}</td><td>{{.Then}}</td><td><span class="state state-{{.State}}">{{.StateLabel}}</span>{{if .HasFailedHint}}<span class="ac-failed-hint" data-ac-failed-hint="true">{{.FailedRunHint}}</span>{{end}}</td><td>{{joinInt64s .CoveringCaseVersionIDs}}</td></tr>
{{end}}
</tbody>
</table>
{{else}}<p class="empty">无验收标准（空数据）</p>{{end}}
<div class="judge" data-section="judge">
<h3>裁判评估（设计裁判事实）</h3>
{{if .JudgeRows}}
{{range .JudgeRows}}{{$row := .}}
<div class="judge-row" data-judge-case-id="{{.CaseID}}">
<p><strong>用例 #{{.CaseID}}</strong>（用例版本 #{{.CaseVersionID}}，需求 #{{.RequirementID}}）：评审状态 <span class="state">{{reviewStateLabel .ReviewState}}</span>；裁判结论 <span class="state">{{verdictLabel .Verdict}}</span></p>
{{if .Findings}}<ul class="judge-findings">{{range .Findings}}<li>[{{.Dimension}} / {{.Severity}}] {{.Message}}{{if .CaseRef}}（{{.CaseRef}}）{{end}}</li>{{end}}</ul>{{else}}<p class="empty">无裁判发现</p>{{end}}
{{if .MatrixKeys}}<p><strong>候选 AC 覆盖矩阵（裁判候选，非验证真值）：</strong></p><ul>{{range .MatrixKeys}}<li><span class="mono">{{.}}</span> → {{joinStrings (index $row.Matrix .)}}</li>{{end}}</ul>{{end}}
{{if .DroppedCount}}<p class="empty">裁判候选矩阵已丢弃 {{.DroppedCount}} 条畸形条目（显式披露，非静默）。</p>{{end}}
</div>
{{end}}
{{else}}<p class="empty">无裁判事实（空数据）</p>{{end}}
</div>
</section>
{{if .HasWaived}}
<section id="waived" data-section="waived">
<h2>豁免说明</h2>
<div class="waived">
{{if .VersionWaived}}
<p><span class="badge">未验证（unverified）</span> 本版本经管理员豁免关闭，闭环完整性被 override，存在未验证内容，请人工判断剩余风险。</p>
<p><strong>豁免理由：</strong>{{if .Waived.Reason}}{{.Waived.Reason}}{{else}}豁免理由不可用{{end}}</p>
<p><strong>操作者：</strong>{{if .Waived.Operator}}#{{.Waived.Operator}}{{else}}豁免上下文不可用（操作者/时间未记录）{{end}}</p>
<p><strong>豁免时间：</strong>{{.Waived.WaivedAt}}</p>
{{else}}
<p><span class="badge">未验证（unverified）</span> 存在需求级豁免：管理员 override 了下列需求的闭环完整性检查，被豁免需求的验收完整性未经完整验证，请人工判断剩余风险。</p>
{{end}}
<h3>被豁免需求</h3>
{{if .Waived.Requirements}}<ul>{{range .Waived.Requirements}}<li>需求 #{{.ID}}：{{.Title}}；豁免理由：{{if .Reason}}{{.Reason}}{{else}}豁免理由不可用{{end}}；操作者：{{if .Operator}}#{{.Operator}}{{else}}操作者未记录{{end}}；豁免时间：{{if .WaivedAt}}{{.WaivedAt}}{{else}}未记录{{end}}</li>{{end}}</ul>{{else}}<p class="empty">无需求级豁免</p>{{end}}
<h3>未验证验收标准范围</h3>
{{if .Waived.UnverifiedACs}}<ul>{{range .Waived.UnverifiedACs}}<li>需求 #{{.RequirementID}} · <span class="mono">{{.ACID}}</span>：<span class="state state-{{.State}}">{{.StateLabel}}</span></li>{{end}}</ul>{{else}}<p class="empty">无未验证验收标准</p>{{end}}
</div>
</section>
{{end}}
<footer>本报告由 EzTest 自动生成，格式固定。统计数字与五要素来自确定性报告快照；AI 叙述仅为候选文本，不改变确定性数据。证据链接指向内容查看端点。</footer>
</main>
</body>
</html>
`
