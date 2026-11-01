"use client"

import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useCallback, useEffect, useState } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Select } from "@/components/ui/Select"
import { LoadingCard } from "@/components/ui/Spinner"
import { Table, type TableColumn } from "@/components/ui/Table"
import { formatDateTime } from "@/lib/format"

import { downloadHTML, getReport, getRevision } from "./api"
import {
  type GlobalReportListOptions,
  type GlobalReportView,
  listGlobalReports,
} from "./global-api"
import { ReportDetailPanel } from "./ReportDetailPanel"
import {
  REPORT_KIND_LABELS,
  REPORT_STATUS_LABELS,
  type ReportDetailView,
  type ReportKind,
  type ReportRevisionView,
  type ReportStatus,
} from "./types"

const PAGE_SIZE = 20

const REPORT_KINDS = Object.keys(REPORT_KIND_LABELS) as ReportKind[]
const REPORT_STATUSES = Object.keys(REPORT_STATUS_LABELS) as ReportStatus[]

const STATUS_TONE: Record<ReportStatus, BadgeTone> = {
  draft: "neutral",
  published: "success",
  stale: "warning",
}

type GlobalReportFilters = {
  kind: ReportKind | "all"
  status: ReportStatus | "all"
  versionId: string
}

function oneOf<T extends string>(raw: string | null, allowed: readonly T[]): T | "all" {
  if (raw !== null && (allowed as readonly string[]).includes(raw)) return raw as T
  return "all"
}

function filtersFromParams(params: URLSearchParams): GlobalReportFilters {
  return {
    kind: oneOf(params.get("kind"), REPORT_KINDS),
    status: oneOf(params.get("status"), REPORT_STATUSES),
    versionId: params.get("version_id")?.trim() ?? "",
  }
}

function sameFilters(left: GlobalReportFilters, right: GlobalReportFilters): boolean {
  return (
    left.kind === right.kind && left.status === right.status && left.versionId === right.versionId
  )
}

function hasActiveFilters(filters: GlobalReportFilters): boolean {
  return filters.kind !== "all" || filters.status !== "all" || filters.versionId.length > 0
}

function listOptions(filters: GlobalReportFilters, before?: string): GlobalReportListOptions {
  const options: GlobalReportListOptions = { limit: PAGE_SIZE }
  if (filters.kind !== "all") options.kind = filters.kind
  if (filters.status !== "all") options.status = filters.status
  if (filters.versionId.length > 0) options.versionId = filters.versionId
  if (before !== undefined && before.length > 0) options.before = before
  return options
}

function revisionLabel(report: GlobalReportView): string {
  const current = report.current_revision
  if (current === null || current === undefined) return "暂无修订"
  return `v${current.revision} · ${REPORT_STATUS_LABELS[current.status]}`
}

/**
 * P6 global reports workspace (`/reports`, spec §4.3.5/§9). Cross-version,
 * login-only and strictly read-only: the list shows each report's current
 * revision, rows deep-link into the owning version workspace and the detail
 * panel hides download/regenerate/export actions. Opening HTML reuses the
 * version-scoped read endpoint; failures stay visible inline.
 */
export function GlobalReportsPage() {
  const router = useRouter()
  const searchParams = useSearchParams()

  const [filters, setFilters] = useState<GlobalReportFilters>(() => filtersFromParams(searchParams))
  const [reports, setReports] = useState<GlobalReportView[]>([])
  const [nextCursor, setNextCursor] = useState("")
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)

  const [selected, setSelected] = useState<GlobalReportView | null>(null)
  const [detail, setDetail] = useState<ReportDetailView | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [detailError, setDetailError] = useState<unknown>(null)
  const [detailToken, setDetailToken] = useState(0)

  const [selectedRevision, setSelectedRevision] = useState<number | null>(null)
  const [revisionView, setRevisionView] = useState<ReportRevisionView | null>(null)
  const [opening, setOpening] = useState(false)
  const [actionError, setActionError] = useState<unknown>(null)

  useEffect(() => {
    const next = filtersFromParams(searchParams)
    setFilters((current) => (sameFilters(current, next) ? current : next))
  }, [searchParams])

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken forces a retry
  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    void listGlobalReports(listOptions(filters))
      .then((page) => {
        if (cancelled) return
        setReports(page.items)
        setNextCursor(page.nextCursor)
        setHasMore(page.hasMore)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setError(cause)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [filters, reloadToken])

  // biome-ignore lint/correctness/useExhaustiveDependencies: detailToken forces a retry
  useEffect(() => {
    if (selected === null) {
      setDetail(null)
      setDetailLoading(false)
      setDetailError(null)
      return undefined
    }
    let cancelled = false
    setDetail(null)
    setRevisionView(null)
    setDetailLoading(true)
    setDetailError(null)
    void getReport(selected.version_id, selected.id)
      .then((next) => {
        if (!cancelled) setDetail(next)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setDetailError(cause)
      })
      .finally(() => {
        if (!cancelled) setDetailLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [selected, detailToken])

  const effectiveRevisionNumber =
    selectedRevision ?? detail?.current_revision?.revision ?? detail?.revisions[0]?.revision ?? null

  // Fetch the selected revision metadata when the dropdown differs from the
  // implicit current revision.
  useEffect(() => {
    if (selected === null || selectedRevision === null) {
      setRevisionView(null)
      return undefined
    }
    let cancelled = false
    void getRevision(selected.version_id, selected.id, selectedRevision)
      .then((next) => {
        if (!cancelled) setRevisionView(next)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setActionError(cause)
      })
    return () => {
      cancelled = true
    }
  }, [selected, selectedRevision])

  const revision = (() => {
    if (detail === null || effectiveRevisionNumber === null) return null
    if (revisionView !== null && revisionView.revision === effectiveRevisionNumber) {
      return revisionView
    }
    return (
      detail.revisions.find((item) => item.revision === effectiveRevisionNumber) ??
      (detail.current_revision?.revision === effectiveRevisionNumber
        ? detail.current_revision
        : null)
    )
  })()

  const writeUrl = useCallback(
    (next: GlobalReportFilters) => {
      const params = new URLSearchParams()
      if (next.kind !== "all") params.set("kind", next.kind)
      if (next.status !== "all") params.set("status", next.status)
      if (next.versionId.length > 0) params.set("version_id", next.versionId)
      const query = params.toString()
      router.replace(query.length > 0 ? `/reports?${query}` : "/reports")
    },
    [router],
  )

  const applyFilters = useCallback(
    (next: GlobalReportFilters) => {
      setFilters(next)
      writeUrl(next)
    },
    [writeUrl],
  )

  function updateFilter<Key extends keyof GlobalReportFilters>(
    key: Key,
    value: GlobalReportFilters[Key],
  ): void {
    applyFilters({ ...filters, [key]: value })
  }

  async function loadMore(): Promise<void> {
    if (loadingMore || !hasMore || nextCursor.length === 0) return
    setLoadingMore(true)
    try {
      const page = await listGlobalReports(listOptions(filters, nextCursor))
      setReports((current) => [...current, ...page.items])
      setNextCursor(page.nextCursor)
      setHasMore(page.hasMore)
    } catch (cause) {
      setError(cause)
    } finally {
      setLoadingMore(false)
    }
  }

  function openReport(report: GlobalReportView): void {
    setSelected(report)
    setSelectedRevision(null)
    setRevisionView(null)
    setActionError(null)
  }

  async function openReportHTML(): Promise<void> {
    if (detail === null || revision === null || !revision.html_available) return
    setOpening(true)
    setActionError(null)
    try {
      const download = await downloadHTML(detail.version_id, detail.id, revision.revision)
      const objectUrl = URL.createObjectURL(download.blob)
      window.open(objectUrl, "_blank", "noopener,noreferrer")
      URL.revokeObjectURL(objectUrl)
    } catch (cause) {
      setActionError(cause)
    } finally {
      setOpening(false)
    }
  }

  const columns: TableColumn<GlobalReportView>[] = [
    {
      key: "report",
      header: "报告",
      render: (report) => (
        <div className="min-w-0">
          <button
            type="button"
            data-testid={`global-report-${report.id}`}
            onClick={() => openReport(report)}
            className="text-left text-xs font-medium text-foreground underline-offset-2 hover:underline"
          >
            {REPORT_KIND_LABELS[report.kind]}
            {report.kind === "wave" && report.wave_id !== null && report.wave_id !== undefined
              ? ` · 波次 #${report.wave_id}`
              : ""}
          </button>
        </div>
      ),
    },
    {
      key: "version",
      header: "版本",
      render: (report) => (
        <Link
          data-testid={`global-report-link-${report.id}`}
          href={report.url}
          className="inline-flex items-center gap-1 text-xs text-primary underline-offset-2 hover:underline"
        >
          {report.version_title.length > 0 ? report.version_title : `版本 #${report.version_id}`}
        </Link>
      ),
    },
    {
      key: "status",
      header: "状态",
      render: (report) => (
        <Badge tone={STATUS_TONE[report.status]}>{REPORT_STATUS_LABELS[report.status]}</Badge>
      ),
    },
    {
      key: "revision",
      header: "当前修订",
      render: (report) => (
        <span
          data-testid={`global-report-revision-${report.id}`}
          className="text-xs text-muted-foreground"
        >
          {revisionLabel(report)}
        </span>
      ),
    },
    {
      key: "updated",
      header: "更新时间",
      render: (report) => (
        <span className="text-xs text-muted-foreground">{formatDateTime(report.updated_at)}</span>
      ),
    },
  ]

  const filtered = hasActiveFilters(filters)

  return (
    <div data-testid="global-reports-page" className="space-y-4">
      <header>
        <h1 className="text-sm font-semibold text-foreground">全局报告</h1>
        <p className="mt-0.5 text-[11px] text-muted-foreground">
          跨版本只读视图；报告详情与 HTML 读取复用所属版本接口。
        </p>
      </header>

      <section aria-label="报告筛选" className="flex flex-wrap items-end gap-3">
        <Field id="global-reports-filter-kind" label="类型" className="w-36">
          <Select
            id="global-reports-filter-kind"
            value={filters.kind}
            onChange={(event) =>
              updateFilter("kind", event.target.value as GlobalReportFilters["kind"])
            }
          >
            <option value="all">全部类型</option>
            {REPORT_KINDS.map((kind) => (
              <option key={kind} value={kind}>
                {REPORT_KIND_LABELS[kind]}
              </option>
            ))}
          </Select>
        </Field>
        <Field id="global-reports-filter-status" label="状态" className="w-36">
          <Select
            id="global-reports-filter-status"
            value={filters.status}
            onChange={(event) =>
              updateFilter("status", event.target.value as GlobalReportFilters["status"])
            }
          >
            <option value="all">全部状态</option>
            {REPORT_STATUSES.map((status) => (
              <option key={status} value={status}>
                {REPORT_STATUS_LABELS[status]}
              </option>
            ))}
          </Select>
        </Field>
        {filters.versionId.length > 0 ? (
          <span className="inline-flex h-8 items-center gap-2 rounded-md border border-border bg-card px-2.5 text-xs text-foreground">
            版本 {filters.versionId}
            <button
              type="button"
              aria-label="清除版本筛选"
              className="text-muted-foreground hover:text-foreground"
              onClick={() => applyFilters({ ...filters, versionId: "" })}
            >
              ×
            </button>
          </span>
        ) : null}
      </section>

      {loading ? (
        <LoadingCard label="正在加载报告…" />
      ) : error !== null ? (
        <ErrorState
          error={error}
          title="报告列表加载失败"
          onRetry={() => setReloadToken((token) => token + 1)}
        />
      ) : reports.length === 0 ? (
        <EmptyState
          title={filtered ? "没有符合筛选条件的报告" : "暂无报告"}
          description={filtered ? "调整筛选条件后重试。" : "执行波次或版本收尾后会自动生成报告。"}
        />
      ) : (
        <Table
          ariaLabel="全局报告列表"
          columns={columns}
          rows={reports}
          rowKey={(report) => report.id}
        />
      )}

      {hasMore ? (
        <div className="flex justify-center">
          <Button
            variant="secondary"
            size="sm"
            data-testid="global-reports-load-more"
            disabled={loadingMore}
            onClick={() => {
              void loadMore()
            }}
          >
            {loadingMore ? "加载中…" : "加载更多"}
          </Button>
        </div>
      ) : null}

      {selected !== null ? (
        <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)]">
          <ReportDetailPanel
            versionId={selected.version_id}
            report={detail}
            revision={revision}
            revisionNumber={effectiveRevisionNumber}
            loading={detailLoading}
            error={detailError}
            readOnly
            onRetry={() => setDetailToken((token) => token + 1)}
            onRevisionChange={(nextRevision) => setSelectedRevision(nextRevision)}
            onOpenHTML={() => {
              void openReportHTML()
            }}
            opening={opening}
            regenerating={false}
            actionError={actionError}
            notice={null}
          />
        </div>
      ) : null}
    </div>
  )
}
