"use client"

import { Download, FileText, Loader2 } from "lucide-react"
import { type ReactNode, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge } from "@/components/ui/StatusBadge"

import { exportReport } from "../api"
import { describeExecutionError } from "../execution-errors"
import { useBatches, useReport } from "../hooks/useExecution"
import type { Batch, BatchStatus, ReportCase, ReportView } from "../types"
import {
  CaseStatusBadge,
  FAILURE_CLASS_LABELS,
  formatDuration,
  ReportSummary,
} from "./ReportSummary"

const BATCH_STATUS_LABELS: Record<BatchStatus, string> = {
  planned: "待执行",
  running: "执行中",
  done: "已完成",
  failed: "失败",
  cancelled: "已取消",
}

const BUTTON_CLASS =
  "inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

function reportFilename(batch: Batch): string {
  return `execution-report-batch-${batch.batch_no}.md`
}

/** Downloads one blob through a short-lived object URL, always revoking it. */
function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement("a")
  anchor.href = url
  anchor.download = filename
  anchor.rel = "noopener"
  anchor.style.display = "none"
  document.body.appendChild(anchor)

  try {
    anchor.click()
  } finally {
    anchor.remove()
    URL.revokeObjectURL(url)
  }
}

type ReportMetaProps = {
  report: ReportView
  batch: Batch
}

/** Batch identity the report was generated for: baseline, commit, env, time. */
function ReportMeta({ report, batch }: ReportMetaProps) {
  const environment = report.environment
  return (
    <dl className="grid gap-x-6 gap-y-1 rounded-xl border border-border bg-muted/30 px-3 py-2 text-xs sm:grid-cols-2">
      <div className="min-w-0">
        <dt className="text-muted-foreground">基线</dt>
        <dd data-testid="report-baseline" className="break-all font-mono">
          {batch.baseline_hash === "" ? "未记录" : batch.baseline_hash}
        </dd>
      </div>
      <div className="min-w-0">
        <dt className="text-muted-foreground">代码提交</dt>
        <dd data-testid="report-commit" className="break-all font-mono">
          {batch.code_baseline_commit === null || batch.code_baseline_commit === ""
            ? "未记录"
            : batch.code_baseline_commit}
        </dd>
      </div>
      <div className="min-w-0">
        <dt className="text-muted-foreground">环境</dt>
        <dd data-testid="report-environment">
          {[environment.system, environment.environment, environment.base_url]
            .filter((part) => part !== "")
            .join(" · ")}
        </dd>
      </div>
      <div className="min-w-0">
        <dt className="text-muted-foreground">生成时间</dt>
        <dd data-testid="report-generated-at">
          <time dateTime={report.generated_at}>{report.generated_at}</time>
        </dd>
      </div>
    </dl>
  )
}

type CaseRowProps = {
  row: ReportCase
}

function CaseRow({ row }: CaseRowProps) {
  return (
    <tr data-testid={`report-case-row-${row.case_run_id}`} className="border-t border-border">
      <td className="max-w-[18rem] truncate px-3 py-2 font-medium" title={row.title}>
        {row.title}
      </td>
      <td className="px-3 py-2">
        <CaseStatusBadge status={row.status} />
      </td>
      <td className="px-3 py-2">
        {row.failure_class === null ? (
          <span className="text-muted-foreground">—</span>
        ) : (
          <StatusBadge
            variant={row.failure_class === "application" ? "danger" : "warning"}
            dot={false}
          >
            {FAILURE_CLASS_LABELS[row.failure_class]}
          </StatusBadge>
        )}
      </td>
      <td className="px-3 py-2 text-right tabular-nums">{row.steps}</td>
      <td className="px-3 py-2 text-right tabular-nums">{row.assertions}</td>
      <td className="px-3 py-2 text-right tabular-nums">{row.evidence}</td>
      <td className="px-3 py-2 text-right tabular-nums">{formatDuration(row.duration_ms)}</td>
    </tr>
  )
}

function ReportCases({ report }: { report: ReportView }) {
  return (
    <div className="overflow-x-auto rounded-xl border border-border">
      <table data-testid="report-cases" className="min-w-full text-sm">
        <caption className="sr-only">报告用例明细</caption>
        <thead className="bg-muted/50 text-left text-xs text-muted-foreground">
          <tr>
            <th scope="col" className="px-3 py-2 font-medium">
              用例
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              状态
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              失败分类
            </th>
            <th scope="col" className="px-3 py-2 text-right font-medium">
              步骤
            </th>
            <th scope="col" className="px-3 py-2 text-right font-medium">
              断言
            </th>
            <th scope="col" className="px-3 py-2 text-right font-medium">
              证据
            </th>
            <th scope="col" className="px-3 py-2 text-right font-medium">
              耗时
            </th>
          </tr>
        </thead>
        <tbody>
          {report.cases.length === 0 ? (
            <tr>
              <td
                colSpan={7}
                data-testid="report-cases-empty"
                className="px-3 py-6 text-center text-xs text-muted-foreground"
              >
                该批次没有用例结果。
              </td>
            </tr>
          ) : (
            report.cases.map((row) => <CaseRow key={row.case_run_id} row={row} />)
          )}
        </tbody>
      </table>
    </div>
  )
}

export type ReportTabProps = {
  /** P3-owned version id; never re-parsed (spec §7.1). */
  versionId: string
  /**
   * Optional controlled batch selection. When omitted the tab defaults to the
   * newest batch from `useBatches` and remembers the user's choice itself.
   */
  batchId?: string
  /** Notified after the user picks another batch. */
  onBatchChange?: (batchId: string) => void
}

/**
 * Report tab of the Batch 1 deliverable (spec §9.3): batch switcher, summary
 * cards, per-case table and server-rendered Markdown export. The export goes
 * through `apiStreamFetch` (via `exportReport`), so the bearer and error
 * handling stay in the shared client and the filename comes from
 * `Content-Disposition` with a deterministic fallback. P7 issue/diff fields are
 * intentionally absent.
 */
export function ReportTab({ versionId, batchId, onBatchChange }: ReportTabProps) {
  const batches = useBatches(versionId)
  const [selectedBatchId, setSelectedBatchId] = useState("")
  const [exporting, setExporting] = useState(false)
  const [exportError, setExportError] = useState("")
  const [notice, setNotice] = useState("")

  const preferredId = batchId ?? selectedBatchId
  const activeBatch =
    batches.batches.find((candidate) => candidate.id === preferredId) ?? batches.batches[0] ?? null
  const activeBatchId = activeBatch?.id ?? ""
  const report = useReport(versionId, activeBatchId)

  function handleBatchChange(nextBatchId: string): void {
    if (batchId === undefined) setSelectedBatchId(nextBatchId)
    onBatchChange?.(nextBatchId)
    setExportError("")
    setNotice("")
  }

  async function handleExport(): Promise<void> {
    if (activeBatch === null || exporting) return
    setExporting(true)
    setExportError("")
    setNotice("")
    try {
      const download = await exportReport(versionId, activeBatch.id, {
        fallbackFilename: reportFilename(activeBatch),
      })
      downloadBlob(download.blob, download.filename)
      setNotice(`已导出 ${download.filename}`)
    } catch (caught) {
      setExportError(describeExecutionError(caught))
    } finally {
      setExporting(false)
    }
  }

  let body: ReactNode
  if (batches.loading && batches.batches.length === 0) {
    body = (
      <p data-testid="report-loading" role="status" className="text-sm text-muted-foreground">
        正在加载批次…
      </p>
    )
  } else if (batches.error !== "") {
    body = (
      <p
        role="alert"
        className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
      >
        {batches.error}
      </p>
    )
  } else if (batches.batches.length === 0) {
    body = (
      <div data-testid="report-empty">
        <EmptyState
          icon={FileText}
          title="还没有执行批次"
          description="版本进入执行阶段并冻结用例基线后，从顶部「开始执行」创建批次，完成后即可在这里查看报告。"
        />
      </div>
    )
  } else if (report.error !== "") {
    body = (
      <p
        role="alert"
        className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
      >
        {report.error}
      </p>
    )
  } else if (report.report === null) {
    body = (
      <p data-testid="report-loading" role="status" className="text-sm text-muted-foreground">
        正在生成报告…
      </p>
    )
  } else {
    body = (
      <div className="space-y-4">
        <ReportSummary summary={report.report.summary} />
        {activeBatch === null ? null : <ReportMeta report={report.report} batch={activeBatch} />}
        <ReportCases report={report.report} />
      </div>
    )
  }

  return (
    <section aria-label="报告" data-testid="report-tab" className="space-y-4" aria-busy={exporting}>
      <header className="flex flex-wrap items-start justify-between gap-3 rounded-2xl border border-border bg-card p-4">
        <div className="min-w-0">
          <h2 className="text-base font-semibold">报告</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            Batch 1 执行报告 · 覆盖 Batch 1 执行结果，不含问题与报告差异（P7 提供）。
          </p>
        </div>

        <div className="flex flex-wrap items-end gap-2">
          <label htmlFor="report-batch-select" className="text-xs text-muted-foreground">
            批次
          </label>
          <select
            id="report-batch-select"
            data-testid="report-batch-selector"
            value={activeBatchId}
            onChange={(event) => handleBatchChange(event.target.value)}
            disabled={batches.loading || batches.batches.length === 0}
            className="rounded-lg border border-border bg-card px-2.5 py-1.5 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"
          >
            {batches.batches.map((candidate) => (
              <option key={candidate.id} value={candidate.id}>
                {`批次 #${candidate.batch_no}（${BATCH_STATUS_LABELS[candidate.status]}）`}
              </option>
            ))}
          </select>

          {activeBatch === null ? null : (
            <button
              type="button"
              data-testid="report-export-btn"
              onClick={() => void handleExport()}
              disabled={exporting || report.report === null}
              className={BUTTON_CLASS}
            >
              {exporting ? (
                <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
              ) : (
                <Download className="h-4 w-4" aria-hidden="true" />
              )}
              {exporting ? "导出中…" : "导出 Markdown"}
            </button>
          )}
        </div>
      </header>

      {body}

      {exportError === "" ? null : (
        <p
          role="alert"
          data-testid="report-export-error"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          {exportError}
        </p>
      )}
      {notice === "" ? null : (
        <p
          role="status"
          className="rounded-xl border border-emerald-500/30 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300"
        >
          {notice}
        </p>
      )}
    </section>
  )
}
