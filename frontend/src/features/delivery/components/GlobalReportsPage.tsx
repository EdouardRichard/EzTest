"use client"

import { FileBarChart2, Loader2 } from "lucide-react"
import { useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge } from "@/components/ui/StatusBadge"

import { useGlobalReports } from "../hooks/useReports"
import { useVersionOptions } from "../hooks/useVersionOptions"
import { labelOf, REPORT_STATUS_LABELS, reportStatusVariant } from "../labels"
import type { GlobalReportFilters, ReportDeepLink } from "../types"

export type GlobalReportsPageProps = {
  /** Optional router hook; the default is a normal `<a>` navigation. */
  onOpenReport?: (deepLink: ReportDeepLink) => void
}

/**
 * Read-only cross-version report centre (spec §7.10/§10.4): version/status
 * filters and a deep link `versions/{id}?tab=report&report={id}` per row.
 */
export function GlobalReportsPage({ onOpenReport }: GlobalReportsPageProps) {
  const versions = useVersionOptions()
  const [versionId, setVersionId] = useState("")
  const [status, setStatus] = useState("")

  const page = useGlobalReports({
    versionId,
    status: status as GlobalReportFilters["status"],
  })

  return (
    <section data-testid="global-reports-page" className="space-y-4">
      <header>
        <h1 className="text-2xl font-semibold tracking-tight">报告中心</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          跨版本报告概览（只读）。点击深链到对应版本「报告」Tab 查看内容与差异。
        </p>
      </header>

      <div className="flex flex-wrap items-end gap-2 rounded-xl border border-border bg-card p-3">
        <label className="grid gap-1 text-[11px] text-muted-foreground">
          <span>版本</span>
          <select
            data-testid="global-reports-version-filter"
            value={versionId}
            onChange={(event) => setVersionId(event.target.value)}
            disabled={versions.loading}
            className="rounded-lg border border-border bg-card px-2.5 py-1.5 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"
          >
            <option value="">全部版本</option>
            {versions.options.map((version) => (
              <option key={version.id} value={version.id}>
                {version.label === "" ? version.name : `${version.name}（${version.label}）`}
              </option>
            ))}
          </select>
        </label>

        <label className="grid gap-1 text-[11px] text-muted-foreground">
          <span>状态</span>
          <select
            data-testid="global-reports-status-filter"
            value={status}
            onChange={(event) => setStatus(event.target.value)}
            className="rounded-lg border border-border bg-card px-2.5 py-1.5 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <option value="">全部状态</option>
            <option value="published">已发布</option>
            <option value="stale">已过期</option>
            <option value="draft">草稿</option>
          </select>
        </label>
      </div>

      {page.loading && page.reports.length === 0 ? (
        <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          正在加载跨版本报告…
        </p>
      ) : page.error !== "" ? (
        <div
          role="alert"
          data-testid="global-reports-error"
          className="flex flex-wrap items-center gap-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          <span>加载报告失败：{page.error}</span>
          <button
            type="button"
            onClick={() => void page.refresh()}
            className="rounded-lg border border-border bg-card px-2.5 py-1 text-xs font-medium text-foreground hover:bg-muted"
          >
            重试
          </button>
        </div>
      ) : page.reports.length === 0 ? (
        <EmptyState
          icon={FileBarChart2}
          title="暂无跨版本报告"
          description="批次执行结束后生成报告，即可在此按版本与状态查看。"
        />
      ) : (
        <ul className="divide-y divide-border rounded-2xl border border-border bg-card">
          {page.reports.map((report) => {
            const deepLink: ReportDeepLink = {
              versionId: report.deepLink.versionId,
              tab: report.deepLink.tab,
              reportId: report.deepLink.reportId,
            }
            const href = `/versions/${encodeURIComponent(deepLink.versionId)}?tab=${encodeURIComponent(
              deepLink.tab,
            )}&report=${encodeURIComponent(deepLink.reportId)}`
            return (
              <li
                key={report.id}
                data-testid={`global-report-row-${report.id}`}
                className="flex flex-wrap items-center gap-2 px-3 py-2"
              >
                <a
                  href={href}
                  onClick={(event) => {
                    if (onOpenReport === undefined) return
                    event.preventDefault()
                    onOpenReport(deepLink)
                  }}
                  className="min-w-0 flex-1 truncate text-sm font-medium text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
                >
                  {report.title}
                </a>
                <span className="text-xs text-muted-foreground">{report.versionName}</span>
                <span className="text-xs text-muted-foreground">Batch #{report.batchNo}</span>
                <span className="text-xs text-muted-foreground">revision {report.revision}</span>
                <StatusBadge variant={reportStatusVariant(report.status)}>
                  {labelOf(REPORT_STATUS_LABELS, report.status)}
                </StatusBadge>
              </li>
            )
          })}
        </ul>
      )}

      {page.hasMore ? (
        <button
          type="button"
          data-testid="global-reports-load-more"
          onClick={() => void page.loadMore()}
          disabled={page.loadingMore}
          className="rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium text-foreground hover:bg-muted disabled:opacity-50"
        >
          {page.loadingMore ? "加载中…" : "加载更多"}
        </button>
      ) : null}
    </section>
  )
}
