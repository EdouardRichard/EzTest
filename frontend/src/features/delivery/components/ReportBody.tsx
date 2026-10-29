"use client"

import { StatusBadge } from "@/components/ui/StatusBadge"

import {
  caseStatusLabel,
  FAILURE_CLASS_LABELS,
  ISSUE_SEVERITY_LABELS,
  ISSUE_STATUS_LABELS,
  labelOf,
  severityVariant,
} from "../labels"
import type { ReportContent, ReportContentBatch } from "../types"

const TH_CLASS = "px-3 py-2 text-left text-xs font-medium text-muted-foreground"
const TD_CLASS = "px-3 py-2 text-sm"
const LINK_CLASS = "text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"

function sourceLabel(source: string): string {
  if (source === "ai_supplement") return "补充"
  if (source === "baseline") return "基线"
  return source === "" ? "—" : source
}

/** Sum of the per-case evidence counters, shown on the summary card. */
function evidenceTotal(content: ReportContent): number {
  return content.cases.reduce((sum, item) => sum + item.evidenceCount, 0)
}

/**
 * D49 fallback marker. A legacy snapshot has neither field recorded and shows
 * "未记录" (history is never rewritten); a new snapshot records both, where an
 * empty reason is an explicit "no fallback".
 */
function fallbackLabel(batch: ReportContentBatch): string {
  if (batch.baseFallbackReason === undefined && batch.historyRewritten === undefined) {
    return "未记录"
  }
  if (batch.historyRewritten === true) {
    const reason =
      batch.baseFallbackReason === undefined || batch.baseFallbackReason === ""
        ? "history_rewritten"
        : batch.baseFallbackReason
    return `${reason}（历史已回退）`
  }
  if (batch.baseFallbackReason !== undefined && batch.baseFallbackReason !== "") {
    return batch.baseFallbackReason
  }
  return "无回退"
}

/** The batch base commit, falling back to the version code baseline (D49). */
function baselineCommit(content: ReportContent): string {
  if (content.batch.codeBaseCommit !== "") return content.batch.codeBaseCommit
  return content.version.codeBaselineCommit
}

export type ReportBodyProps = {
  /** Version id used by the issue/case deep links; never re-parsed. */
  versionId: string
  content: ReportContent
  /** Route the issue link through the page; default is the `?tab=issues` link. */
  onOpenIssue?: (issueId: string) => void
  /** Route the case link through the page; default is the `?tab=results` link. */
  onOpenCase?: (caseRunId: string, caseNodeId: string) => void
}

/**
 * Deterministic report sections of spec §10.4: summary cards, case table with
 * its source badge, issue summary, requirement coverage, risks and appendix.
 * Every value comes from the `eztest.report.v1` snapshot; nothing is derived
 * from execution APIs, so two reads of the same revision render identically.
 *
 * Case rows deep-link to their case-run results and issue rows to the issues
 * tab; `onOpenCase`/`onOpenIssue` let the hosting page intercept routing. The
 * snapshot carries only evidence counts (no evidence ids), so a single evidence
 * cannot be deep-linked from here — the case-run results page owns that link.
 */
export function ReportBody({ versionId, content, onOpenIssue, onOpenCase }: ReportBodyProps) {
  const summary = content.summary
  const versionBase = `/versions/${encodeURIComponent(versionId)}`

  return (
    <div className="space-y-4">
      <section aria-label="报告摘要" data-testid="report-summary">
        <dl className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-4">
          {[
            { label: "用例总数", value: summary.caseCount, testId: "report-summary-case-count" },
            { label: "通过", value: summary.passed, testId: "report-summary-passed" },
            { label: "失败", value: summary.failed, testId: "report-summary-failed" },
            { label: "阻塞", value: summary.blocked, testId: "report-summary-blocked" },
            { label: "不确定", value: summary.inconclusive, testId: "report-summary-inconclusive" },
            { label: "疑似不稳定", value: summary.flakyCount, testId: "report-summary-flaky" },
            { label: "证据数", value: evidenceTotal(content), testId: "report-summary-evidence" },
            { label: "问题数", value: content.issues.length, testId: "report-summary-issues" },
          ].map((card) => (
            <div key={card.testId} className="rounded-xl border border-border bg-card px-3 py-2">
              <dt className="text-[11px] text-muted-foreground">{card.label}</dt>
              <dd data-testid={card.testId} className="mt-0.5 text-lg font-semibold tabular-nums">
                {card.value}
              </dd>
            </div>
          ))}
        </dl>
      </section>

      <section aria-label="用例明细" className="space-y-2">
        <h3 className="text-sm font-semibold">用例明细</h3>
        <div className="overflow-x-auto rounded-xl border border-border">
          <table data-testid="report-cases" className="min-w-full">
            <caption className="sr-only">报告用例明细</caption>
            <thead className="bg-muted/50">
              <tr>
                <th scope="col" className={TH_CLASS}>
                  用例
                </th>
                <th scope="col" className={TH_CLASS}>
                  来源
                </th>
                <th scope="col" className={TH_CLASS}>
                  状态
                </th>
                <th scope="col" className={TH_CLASS}>
                  失败分类
                </th>
                <th scope="col" className={TH_CLASS}>
                  步骤
                </th>
                <th scope="col" className={TH_CLASS}>
                  断言
                </th>
                <th scope="col" className={TH_CLASS}>
                  证据
                </th>
              </tr>
            </thead>
            <tbody>
              {content.cases.length === 0 ? (
                <tr>
                  <td colSpan={7} className="px-3 py-6 text-center text-xs text-muted-foreground">
                    该批次没有用例结果。
                  </td>
                </tr>
              ) : (
                content.cases.map((row) => (
                  <tr
                    key={`${row.caseNodeId}:${row.caseRunId}`}
                    data-testid={`report-case-row-${row.caseRunId}`}
                    className="border-t border-border"
                  >
                    <td className={`${TD_CLASS} max-w-[18rem] truncate`} title={row.title}>
                      <a
                        data-testid={`report-case-link-${row.caseRunId}`}
                        href={`${versionBase}?tab=results&caseRun=${encodeURIComponent(row.caseRunId)}`}
                        onClick={(event) => {
                          if (onOpenCase === undefined) return
                          event.preventDefault()
                          onOpenCase(row.caseRunId, row.caseNodeId)
                        }}
                        className={LINK_CLASS}
                      >
                        {row.title}
                      </a>
                    </td>
                    <td className={TD_CLASS}>
                      <span
                        data-testid="report-case-source"
                        data-source={row.source}
                        className="rounded-full border border-border bg-muted px-2 py-0.5 text-[11px] text-muted-foreground"
                      >
                        {sourceLabel(row.source)}
                      </span>
                    </td>
                    <td className={TD_CLASS}>{caseStatusLabel(row.status)}</td>
                    <td className={TD_CLASS}>
                      {row.failureClass === ""
                        ? "—"
                        : labelOf(FAILURE_CLASS_LABELS, row.failureClass)}
                    </td>
                    <td className={`${TD_CLASS} tabular-nums`}>{row.stepCount}</td>
                    <td className={`${TD_CLASS} tabular-nums`}>{row.assertionCount}</td>
                    <td className={`${TD_CLASS} tabular-nums`}>{row.evidenceCount}</td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      </section>

      <section aria-label="问题汇总" className="space-y-2">
        <h3 className="text-sm font-semibold">问题汇总</h3>
        {content.issues.length === 0 ? (
          <p className="text-xs text-muted-foreground">本批次没有问题。</p>
        ) : (
          <ul
            data-testid="report-issues"
            className="divide-y divide-border rounded-xl border border-border"
          >
            {content.issues.map((issue) => (
              <li key={issue.issueId} className="flex flex-wrap items-center gap-2 px-3 py-2">
                <StatusBadge variant={severityVariant(issue.severity)}>
                  {labelOf(ISSUE_SEVERITY_LABELS, issue.severity)}
                </StatusBadge>
                <a
                  data-testid={`report-issue-link-${issue.issueId}`}
                  href={`${versionBase}?tab=issues&issue=${encodeURIComponent(issue.issueId)}`}
                  onClick={(event) => {
                    if (onOpenIssue === undefined) return
                    event.preventDefault()
                    onOpenIssue(issue.issueId)
                  }}
                  className={`min-w-0 flex-1 truncate text-sm ${LINK_CLASS}`}
                >
                  {issue.title}
                </a>
                <span className="text-[11px] text-muted-foreground">
                  {labelOf(ISSUE_STATUS_LABELS, issue.status)}
                </span>
                {issue.codeRefs[0] === undefined ? null : (
                  <span className="font-mono text-[11px] text-muted-foreground">
                    {issue.codeRefs[0].path}:{issue.codeRefs[0].line}
                  </span>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>

      <section aria-label="需求覆盖" className="space-y-2">
        <h3 className="text-sm font-semibold">需求覆盖</h3>
        {content.requirements.length === 0 ? (
          <p className="text-xs text-muted-foreground">本批次没有需求覆盖数据。</p>
        ) : (
          <ul
            data-testid="report-requirements"
            className="divide-y divide-border rounded-xl border border-border"
          >
            {content.requirements.map((requirement) => (
              <li
                key={requirement.requirementId}
                className="flex flex-wrap items-center gap-2 px-3 py-2"
              >
                <span className="min-w-0 flex-1 truncate text-sm">{requirement.title}</span>
                <span className="text-[11px] text-muted-foreground">
                  用例 {requirement.caseCount} · 通过 {requirement.passed} · 失败{" "}
                  {requirement.failed}
                </span>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section aria-label="风险与结论" className="space-y-2">
        <h3 className="text-sm font-semibold">风险与结论</h3>
        {content.risks.length === 0 ? (
          <p className="text-xs text-muted-foreground">无额外风险。</p>
        ) : (
          <ul
            data-testid="report-risks"
            className="list-disc space-y-1 rounded-xl border border-border px-6 py-3 text-sm"
          >
            {content.risks.map((risk) => (
              <li key={risk}>{risk}</li>
            ))}
          </ul>
        )}
      </section>

      <section aria-label="附录" data-testid="report-appendix">
        <dl className="grid gap-x-6 gap-y-1 rounded-xl border border-border bg-muted/30 px-3 py-2 text-xs sm:grid-cols-2">
          <div>
            <dt className="text-muted-foreground">环境</dt>
            <dd>{content.appendix.environment === "" ? "—" : content.appendix.environment}</dd>
          </div>
          <div>
            <dt className="text-muted-foreground">系统</dt>
            <dd>{content.appendix.system === "" ? "—" : content.appendix.system}</dd>
          </div>
          <div>
            <dt className="text-muted-foreground">代码基线</dt>
            <dd data-testid="report-batch-baseline" className="break-all font-mono">
              {baselineCommit(content) === "" ? "—" : baselineCommit(content)}
            </dd>
          </div>
          <div>
            <dt className="text-muted-foreground">回退标记</dt>
            <dd data-testid="report-batch-fallback">{fallbackLabel(content.batch)}</dd>
          </div>
          <div>
            <dt className="text-muted-foreground">代码提交</dt>
            <dd className="break-all font-mono">{content.appendix.commit || "—"}</dd>
          </div>
          <div>
            <dt className="text-muted-foreground">生成时间</dt>
            <dd>
              <time dateTime={content.appendix.generatedAt}>
                {content.appendix.generatedAt || "—"}
              </time>
            </dd>
          </div>
        </dl>
      </section>
    </div>
  )
}
