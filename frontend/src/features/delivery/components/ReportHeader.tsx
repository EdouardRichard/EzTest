"use client"

import type { ReactNode } from "react"

import { StatusBadge } from "@/components/ui/StatusBadge"

import { BATCH_KIND_LABELS, labelOf, REPORT_STATUS_LABELS, reportStatusVariant } from "../labels"
import type { ReportBatchSummary, ReportDetail } from "../types"

export type ReportHeaderProps = {
  report: ReportDetail
  batch?: ReportBatchSummary
  regenerating: boolean
  onRegenerate: () => void
  /** Caller-owned action slot (the export button for the report tab). */
  actions?: ReactNode
}

/**
 * Report header of spec §10.4: batch number/kind, revision, lifecycle status,
 * publish time, the degraded marker and the stale banner with its regenerate
 * action. It never downloads anything itself; export is a caller slot.
 */
export function ReportHeader({
  report,
  batch,
  regenerating,
  onRegenerate,
  actions,
}: ReportHeaderProps) {
  const batchNo = batch?.batchNo ?? report.batchNo
  const kind = batch?.kind ?? report.batchKind
  const degraded = report.content.degraded

  return (
    <div className="space-y-2">
      <header className="flex flex-wrap items-start justify-between gap-3 rounded-2xl border border-border bg-card p-4">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="text-base font-semibold">报告</h2>
            <span
              data-testid="report-revision-badge"
              className="rounded-full border border-border bg-muted px-2 py-0.5 text-[11px] font-medium text-muted-foreground"
            >
              revision {report.revision}
            </span>
            <StatusBadge variant={reportStatusVariant(report.status)}>
              {labelOf(REPORT_STATUS_LABELS, report.status)}
            </StatusBadge>
            {degraded ? (
              <StatusBadge variant="warning" dot={false}>
                AI 降级
              </StatusBadge>
            ) : null}
          </div>
          <p className="flex flex-wrap gap-x-2 text-[11px] text-muted-foreground">
            <span>Batch #{batchNo}</span>
            <span aria-hidden="true">·</span>
            <span>{labelOf(BATCH_KIND_LABELS, kind)}</span>
            <span aria-hidden="true">·</span>
            <span>发布 {report.publishedAt ?? "—"}</span>
            <span aria-hidden="true">·</span>
            <span title={report.inputFingerprint}>
              指纹 {report.inputFingerprint.slice(0, 12)}…
            </span>
          </p>
        </div>
        {actions === undefined ? null : <div className="flex flex-wrap gap-2">{actions}</div>}
      </header>

      {degraded ? (
        <p
          data-testid="report-degraded"
          role="status"
          className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
        >
          AI 分析不可用，报告内容可能不完整（degraded）。
        </p>
      ) : null}

      {report.status === "stale" ? (
        <div
          data-testid="report-stale-banner"
          role="status"
          className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
        >
          <span>
            报告已过期
            {report.staleReason === "" ? "" : `：${report.staleReason}`}
            {" · "}
            批次结果或问题状态已变化，请重新生成新的 revision。
          </span>
          <button
            type="button"
            data-testid="report-regenerate-btn"
            onClick={onRegenerate}
            disabled={regenerating}
            className="rounded-lg border border-border bg-card px-2.5 py-1 text-xs font-medium text-foreground transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50"
          >
            {regenerating ? "生成中…" : "重新生成"}
          </button>
        </div>
      ) : null}
    </div>
  )
}
