"use client"

import { FileBarChart2, Loader2 } from "lucide-react"
import { useMemo, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"

import { describeDeliveryError, ensureReport, regenerateReport } from "../api"
import { useReport, useReportBatches, useReports } from "../hooks/useReports"
import type { DeliveryStream } from "../types"
import { ReportBatchSelector } from "./ReportBatchSelector"
import { ReportBody } from "./ReportBody"
import { ReportDiff } from "./ReportDiff"
import { ReportExportButton } from "./ReportExportButton"
import { ReportHeader } from "./ReportHeader"

export type ReportsTabProps = {
  /** P3-owned version id; never re-parsed (spec §7.1). */
  versionId: string
  /**
   * Controlled batch selection. When omitted the tab defaults to the newest
   * batch and remembers the user's choice itself.
   */
  batchId?: string
  onBatchChange?: (batchId: string) => void
  /** Controlled report revision for the `?report=` deep link. */
  reportId?: string
  /** Notified when the selected report revision changes. */
  onReportChange?: (reportId: string) => void
  /** Route a report-body issue link through the page; default is `?tab=issues`. */
  onOpenIssue?: (issueId: string) => void
  /** Route a report-body case link through the page; default is `?tab=results`. */
  onOpenCase?: (caseRunId: string, caseNodeId: string) => void
  /** Version stream owned by the page; omit to subscribe inside the tab. */
  stream?: DeliveryStream
}

const BUTTON_CLASS =
  "inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"

/**
 * Version-scoped report tab (spec §10.4): batch selector, latest revision of
 * the batch, stale banner with regenerate, deterministic content sections,
 * report diff against a base revision and the verified Markdown export.
 */
export function ReportsTab({
  versionId,
  batchId,
  onBatchChange,
  reportId,
  onReportChange,
  onOpenIssue,
  onOpenCase,
  stream,
}: ReportsTabProps) {
  const [selectedBatchId, setSelectedBatchId] = useState("")
  const [selectedReportId, setSelectedReportId] = useState("")
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState("")
  const [notice, setNotice] = useState("")

  const batchPage = useReportBatches(versionId, stream)
  const preferredBatchId = batchId ?? selectedBatchId
  const selectedBatch =
    batchPage.batches.find((batch) => batch.id === preferredBatchId) ?? batchPage.batches[0] ?? null

  const explicitReportId = reportId !== undefined && reportId !== "" ? reportId : selectedReportId
  const report = useReport(versionId, explicitReportId, stream)

  // The controlled report may belong to another batch than the newest one; its
  // own batch becomes the active batch so the selector matches the deep link.
  const reportBatchId =
    reportId !== undefined && reportId !== "" ? (report.report?.batchId ?? "") : ""
  const activeBatchId = reportBatchId !== "" ? reportBatchId : (selectedBatch?.id ?? "")

  const batchReports = useReports(
    versionId,
    activeBatchId === "" ? {} : { batchId: activeBatchId },
    stream,
  )
  const allReports = useReports(versionId, {}, stream)

  const fallbackReportId = batchReports.reports[0]?.id ?? ""
  const effectiveReportId = explicitReportId !== "" ? explicitReportId : fallbackReportId
  const activeReport = useReport(versionId, effectiveReportId, stream)

  const batchNoById = useMemo(() => {
    const map: Record<string, number> = {}
    for (const batch of batchPage.batches) map[batch.id] = batch.batchNo
    return map
  }, [batchPage.batches])

  function selectReport(nextReportId: string): void {
    if (reportId === undefined) setSelectedReportId(nextReportId)
    onReportChange?.(nextReportId)
  }

  function handleBatchChange(nextBatchId: string): void {
    if (batchId === undefined) setSelectedBatchId(nextBatchId)
    selectReport("")
    setActionError("")
    setNotice("")
    onBatchChange?.(nextBatchId)
  }

  async function handleEnsure(): Promise<void> {
    if (activeBatchId === "" || busy) return
    setBusy(true)
    setActionError("")
    setNotice("")
    try {
      const created = await ensureReport(versionId, activeBatchId)
      selectReport(created.id)
      await Promise.all([batchReports.refresh(), allReports.refresh()])
      setNotice(`已生成 revision ${created.revision}`)
    } catch (caught) {
      setActionError(describeDeliveryError(caught))
    } finally {
      setBusy(false)
    }
  }

  async function handleRegenerate(): Promise<void> {
    const current = activeReport.report
    if (current === null || busy) return
    setBusy(true)
    setActionError("")
    setNotice("")
    try {
      const next = await regenerateReport(versionId, current.id)
      selectReport(next.id)
      await Promise.all([batchReports.refresh(), allReports.refresh()])
      setNotice(
        next.id === current.id ? "当前已是最新 revision" : `已生成 revision ${next.revision}`,
      )
    } catch (caught) {
      setActionError(describeDeliveryError(caught))
    } finally {
      setBusy(false)
    }
  }

  let body = null
  if (batchPage.loading && batchPage.batches.length === 0) {
    body = (
      <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
        正在加载批次…
      </p>
    )
  } else if (batchPage.error !== "") {
    body = (
      <p
        role="alert"
        data-testid="report-error"
        className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
      >
        加载批次失败：{batchPage.error}
      </p>
    )
  } else if (batchPage.batches.length === 0) {
    body = (
      <div data-testid="report-empty">
        <EmptyState
          icon={FileBarChart2}
          title="还没有执行批次"
          description="版本完成执行并生成批次后，这里会展示该批次的交付报告。"
        />
      </div>
    )
  } else if (batchReports.loading && batchReports.reports.length === 0) {
    body = (
      <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
        正在加载报告…
      </p>
    )
  } else if (batchReports.error !== "") {
    body = (
      <p
        role="alert"
        data-testid="report-error"
        className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
      >
        加载报告失败：{batchReports.error}
      </p>
    )
  } else if (effectiveReportId !== "" && activeReport.loading && activeReport.report === null) {
    body = (
      <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
        正在加载报告内容…
      </p>
    )
  } else if (effectiveReportId !== "" && activeReport.error !== "") {
    body = (
      <p
        role="alert"
        data-testid="report-error"
        className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
      >
        加载报告失败：{activeReport.error}
      </p>
    )
  } else if (effectiveReportId === "" && batchReports.reports.length === 0) {
    body = (
      <div
        data-testid="report-empty"
        className="space-y-3 rounded-2xl border border-dashed border-border px-4 py-8 text-center"
      >
        <p className="text-sm font-medium">该批次尚无报告</p>
        <p className="text-xs text-muted-foreground">
          执行结束后可生成该批次的交付报告（revision 1）。
        </p>
        <button
          type="button"
          data-testid="report-generate-btn"
          onClick={() => void handleEnsure()}
          disabled={busy}
          className={BUTTON_CLASS}
        >
          {busy ? "生成中…" : "生成报告"}
        </button>
      </div>
    )
  } else if (activeReport.report !== null) {
    const matchedBatch =
      selectedBatch !== null && selectedBatch.id === activeReport.report.batchId
        ? selectedBatch
        : undefined
    body = (
      <div className="space-y-4">
        <ReportHeader
          report={activeReport.report}
          batch={matchedBatch}
          regenerating={busy}
          onRegenerate={() => void handleRegenerate()}
          actions={<ReportExportButton versionId={versionId} reportId={activeReport.report.id} />}
        />
        <ReportBody
          versionId={versionId}
          content={activeReport.report.content}
          onOpenIssue={onOpenIssue}
          onOpenCase={onOpenCase}
        />
        <ReportDiff
          versionId={versionId}
          reportId={activeReport.report.id}
          reports={allReports.reports}
          batchNoById={batchNoById}
        />
      </div>
    )
  }

  return (
    <section aria-label="报告" data-testid="report-tab" className="space-y-4">
      <header className="flex flex-wrap items-end justify-between gap-2">
        <div>
          <h2 className="text-base font-semibold">报告</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            批次交付报告：revision、stale 提示、差异对比与 Markdown 导出。
          </p>
        </div>
        <ReportBatchSelector
          batches={batchPage.batches}
          value={activeBatchId}
          disabled={batchPage.loading || batchPage.batches.length === 0}
          onChange={handleBatchChange}
        />
      </header>

      {actionError === "" ? null : (
        <p
          role="alert"
          data-testid="report-action-error"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          {actionError}
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

      {body}
    </section>
  )
}
