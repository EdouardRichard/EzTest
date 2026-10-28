"use client"

import { Loader2 } from "lucide-react"
import { useEffect, useState } from "react"

import { deliveryErrorReason, describeDeliveryError, diffReports } from "../api"
import { labelOf, REPORT_STATUS_LABELS } from "../labels"
import type { ReportDiff as ReportDiffView, ReportSummary } from "../types"

type DiffState = {
  status: "loading" | "ready" | "error" | "unavailable"
  diff: ReportDiffView | null
  error: string
}

/** The default base 404s when the version has no previous-batch report yet. */
function isMissingReport(error: unknown): boolean {
  if (deliveryErrorReason(error) === "report_not_found") return true
  return (
    typeof error === "object" && error !== null && (error as { status?: unknown }).status === 404
  )
}

export type ReportDiffProps = {
  versionId: string
  /** Target report revision. */
  reportId: string
  /** Candidate base reports of the same version (the current one is excluded). */
  reports: ReportSummary[]
  /** Batch number per batch id, for readable base labels. */
  batchNoById?: Record<string, number>
}

const COUNT_CARDS: readonly {
  key: keyof ReportDiffView["summary"]
  label: string
  testId: string
}[] = [
  { key: "addedIssues", label: "新增问题", testId: "report-diff-added-issues" },
  { key: "fixedIssues", label: "已修复", testId: "report-diff-fixed-issues" },
  { key: "regressed", label: "回归", testId: "report-diff-regressed" },
  { key: "recovered", label: "恢复", testId: "report-diff-recovered" },
  { key: "newCases", label: "新用例", testId: "report-diff-new-cases" },
  { key: "removedCases", label: "消失用例", testId: "report-diff-removed-cases" },
  { key: "changed", label: "状态变化", testId: "report-diff-changed" },
]

/**
 * Report-to-report diff (spec §10.4): the base defaults to the previous batch
 * report on the server, or an explicit revision chosen here. The summary
 * counters and the detailed classification lists both come from the API.
 */
export function ReportDiff({ versionId, reportId, reports, batchNoById = {} }: ReportDiffProps) {
  const [baseReportId, setBaseReportId] = useState("")
  const [baseTargetId, setBaseTargetId] = useState(reportId)
  const [state, setState] = useState<DiffState>({ status: "loading", diff: null, error: "" })

  // A base chosen for another revision must not leak into this one: reset it
  // before the diff request so a stale/self base is never sent.
  if (baseTargetId !== reportId) {
    setBaseTargetId(reportId)
    setBaseReportId("")
  }

  useEffect(() => {
    let cancelled = false
    setState({ status: "loading", diff: null, error: "" })
    const base = baseReportId === "" ? undefined : baseReportId
    diffReports(versionId, reportId, base)
      .then((diff) => {
        if (!cancelled) setState({ status: "ready", diff, error: "" })
      })
      .catch((caught: unknown) => {
        if (cancelled) return
        // The normal first-batch state: no previous report to diff against.
        if (base === undefined && isMissingReport(caught)) {
          setState({ status: "unavailable", diff: null, error: "" })
          return
        }
        setState({ status: "error", diff: null, error: describeDeliveryError(caught) })
      })
    return () => {
      cancelled = true
    }
  }, [versionId, reportId, baseReportId])

  const baseOptions = reports.filter((report) => report.id !== reportId)
  const diff = state.diff
  const empty =
    diff !== null &&
    diff.summary.addedIssues === 0 &&
    diff.summary.fixedIssues === 0 &&
    diff.summary.regressed === 0 &&
    diff.summary.recovered === 0 &&
    diff.summary.newCases === 0 &&
    diff.summary.removedCases === 0 &&
    diff.summary.changed === 0

  return (
    <section
      aria-label="报告差异"
      data-testid="report-diff"
      className="space-y-3 rounded-2xl border border-border bg-card p-4"
    >
      <header className="flex flex-wrap items-end justify-between gap-2">
        <div>
          <h3 className="text-sm font-semibold">批次差异</h3>
          <p className="text-[11px] text-muted-foreground">
            默认对比上一批次报告；也可显式选择其它 revision 作为基线。
          </p>
        </div>
        <label className="flex items-center gap-2 text-xs text-muted-foreground">
          <span>基线</span>
          <select
            data-testid="report-diff-base"
            value={baseReportId}
            onChange={(event) => setBaseReportId(event.target.value)}
            className="rounded-lg border border-border bg-card px-2.5 py-1.5 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <option value="">上一批次（默认）</option>
            {baseOptions.map((report) => {
              const batchNo = batchNoById[report.batchId]
              const batchLabel = batchNo === undefined ? "Batch —" : `Batch #${batchNo}`
              return (
                <option key={report.id} value={report.id}>
                  {`${batchLabel} · revision ${report.revision} · ${labelOf(REPORT_STATUS_LABELS, report.status)}`}
                </option>
              )
            })}
          </select>
        </label>
      </header>

      {state.status === "loading" ? (
        <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          正在加载差异…
        </p>
      ) : state.status === "unavailable" ? (
        <p
          role="status"
          data-testid="report-diff-unavailable"
          className="rounded-xl border border-dashed border-border px-3 py-4 text-center text-sm text-muted-foreground"
        >
          无上一批次报告，无法对比
        </p>
      ) : state.status === "error" ? (
        <p
          role="alert"
          data-testid="report-diff-error"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          加载报告差异失败：{state.error}
        </p>
      ) : diff === null ? null : (
        <>
          <dl className="grid grid-cols-2 gap-2 sm:grid-cols-4 lg:grid-cols-7">
            {COUNT_CARDS.map((card) => (
              <div
                key={card.testId}
                className="rounded-xl border border-border bg-muted/30 px-3 py-2"
              >
                <dt className="text-[11px] text-muted-foreground">{card.label}</dt>
                <dd data-testid={card.testId} className="mt-0.5 text-lg font-semibold tabular-nums">
                  {diff.summary[card.key]}
                </dd>
              </div>
            ))}
          </dl>

          {empty ? (
            <p className="rounded-xl border border-dashed border-border px-3 py-4 text-center text-sm text-muted-foreground">
              两版报告无差异
            </p>
          ) : (
            <div className="grid gap-3 lg:grid-cols-2">
              <DiffIssueList title="新增问题" rows={diff.addedIssues} />
              <DiffIssueList title="已修复问题" rows={diff.fixedIssues} />
              <DiffCaseList title="回归用例" rows={diff.regressedCases} />
              <DiffCaseList title="恢复用例" rows={diff.recoveredCases} />
              <DiffCaseList title="状态变化" rows={diff.changedCases} />
              <DiffNewCaseList title="新增用例" rows={diff.newCases} />
              <DiffNewCaseList title="消失用例" rows={diff.removedCases} />
            </div>
          )}
        </>
      )}
    </section>
  )
}

function DiffIssueList({ title, rows }: { title: string; rows: ReportDiffView["addedIssues"] }) {
  if (rows.length === 0) return null
  return (
    <div className="rounded-xl border border-border px-3 py-2">
      <h4 className="text-xs font-semibold text-muted-foreground">
        {title}（{rows.length}）
      </h4>
      <ul className="mt-1 space-y-0.5 text-xs">
        {rows.map((row) => (
          <li key={row.issueId} className="truncate">
            <span className="mr-1 font-mono text-muted-foreground">{row.severity}</span>
            {row.title}
          </li>
        ))}
      </ul>
    </div>
  )
}

function DiffCaseList({ title, rows }: { title: string; rows: ReportDiffView["regressedCases"] }) {
  if (rows.length === 0) return null
  return (
    <div className="rounded-xl border border-border px-3 py-2">
      <h4 className="text-xs font-semibold text-muted-foreground">
        {title}（{rows.length}）
      </h4>
      <ul className="mt-1 space-y-0.5 text-xs">
        {rows.map((row) => (
          <li key={row.caseNodeId} className="truncate">
            {row.title}
            <span className="ml-1 text-muted-foreground">
              {row.from} → {row.to}
            </span>
          </li>
        ))}
      </ul>
    </div>
  )
}

function DiffNewCaseList({ title, rows }: { title: string; rows: ReportDiffView["newCases"] }) {
  if (rows.length === 0) return null
  return (
    <div className="rounded-xl border border-border px-3 py-2">
      <h4 className="text-xs font-semibold text-muted-foreground">
        {title}（{rows.length}）
      </h4>
      <ul className="mt-1 space-y-0.5 text-xs">
        {rows.map((row) => (
          <li key={row.caseNodeId} className="truncate">
            {row.title}
            <span className="ml-1 text-muted-foreground">{row.source}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}
