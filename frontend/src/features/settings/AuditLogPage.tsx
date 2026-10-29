"use client"

import { Download, FileJson, RefreshCw, ScrollText, Search } from "lucide-react"
import { type FormEvent, useCallback, useEffect, useState } from "react"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { Skeleton } from "@/components/ui/Skeleton"
import { StatusBadge } from "@/components/ui/StatusBadge"
import { apiGetPage } from "@/lib/api-client"

import {
  auditExportFilename,
  buildAuditCSV,
  buildAuditMarkdown,
  downloadTextFile,
} from "./audit-export"
import {
  AUDIT_PAGE_SIZE,
  type AuditEntry,
  type AuditFilters,
  EMPTY_AUDIT_FILTERS,
} from "./audit-types"

type LoadError = {
  message: string
  requestId: string
}

/**
 * Reads the request id without importing the concrete ApiError class, so a
 * mocked/fetch-layer failure that carries the same fields still correlates.
 */
function describeError(error: unknown): LoadError {
  let requestId = ""
  if (typeof error === "object" && error !== null) {
    const candidate = (error as { requestId?: unknown }).requestId
    if (typeof candidate === "string") requestId = candidate
  }
  if (error instanceof Error) {
    const status = (error as { status?: unknown }).status
    if (status === 0) {
      return { message: "无法连接 API 服务，请确认后端已启动。", requestId }
    }
    return { message: error.message, requestId }
  }
  return { message: "未知错误", requestId }
}

/** Converts a `datetime-local` value to the RFC3339 bound the API expects. */
function toRFC3339(value: string): string {
  if (value.trim() === "") return ""
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return ""
  return parsed.toISOString()
}

/**
 * Builds the audit list URL from the filter form. Empty fields are omitted so
 * the request stays identical to the unfiltered page; `from`/`to` are sent as
 * RFC3339 timestamps.
 */
export function buildAuditQuery(
  filters: AuditFilters,
  cursor = "",
  limit = AUDIT_PAGE_SIZE,
): string {
  const search = new URLSearchParams()
  search.set("limit", String(limit))
  const actor = filters.actor.trim()
  if (actor !== "") search.set("actor", actor)
  const action = filters.action.trim()
  if (action !== "") search.set("action", action)
  const from = toRFC3339(filters.from)
  if (from !== "") search.set("from", from)
  const to = toRFC3339(filters.to)
  if (to !== "") search.set("to", to)
  if (cursor !== "") search.set("cursor", cursor)
  return `/api/admin/audit?${search.toString()}`
}

/** Renders one metadata document without assuming a particular shape. */
function metadataText(metadata: Record<string, unknown>): string {
  return JSON.stringify(metadata, null, 2)
}

/**
 * Admin audit viewer (spec §8.2): read-only list with actor/action/time
 * filters, cursor pagination, a per-entry detail panel and browser-generated
 * CSV/Markdown exports. The endpoint stays `GET /api/admin/audit`; the server
 * enforces the admin role.
 */
export function AuditLogPage() {
  const [entries, setEntries] = useState<AuditEntry[]>([])
  const [cursor, setCursor] = useState("")
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState<LoadError | null>(null)
  const [selected, setSelected] = useState<AuditEntry | null>(null)

  const [filters, setFilters] = useState<AuditFilters>(EMPTY_AUDIT_FILTERS)
  const [applied, setApplied] = useState<AuditFilters>(EMPTY_AUDIT_FILTERS)

  const load = useCallback(
    async (reset: boolean, nextCursor = "") => {
      if (reset) {
        setLoading(true)
        setError(null)
      } else {
        setLoadingMore(true)
      }
      try {
        const page = await apiGetPage<AuditEntry>(buildAuditQuery(applied, nextCursor))
        setEntries((previous) => (reset ? page.items : [...previous, ...page.items]))
        setCursor(page.nextCursor)
        setHasMore(page.hasMore)
        if (reset) setSelected(null)
      } catch (caught) {
        setError(describeError(caught))
      } finally {
        setLoading(false)
        setLoadingMore(false)
      }
    },
    [applied],
  )

  useEffect(() => {
    void load(true)
  }, [load])

  function handleFilter(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setApplied({ ...filters })
  }

  function handleReset() {
    setFilters(EMPTY_AUDIT_FILTERS)
    setApplied(EMPTY_AUDIT_FILTERS)
  }

  function handleExport(format: "csv" | "md") {
    if (entries.length === 0) return
    if (format === "csv") {
      downloadTextFile(auditExportFilename("csv"), buildAuditCSV(entries), "text/csv")
      return
    }
    downloadTextFile(auditExportFilename("md"), buildAuditMarkdown(entries), "text/markdown")
  }

  const showEmpty = !loading && error === null && entries.length === 0

  return (
    <section aria-labelledby="audit-log-title" className="space-y-4">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h2 id="audit-log-title" className="text-base font-semibold">
              审计日志
            </h2>
            <StatusBadge>只读</StatusBadge>
          </div>
          <p className="mt-1 text-sm leading-relaxed text-muted-foreground">
            记录谁在何时对哪个对象执行了什么操作。筛选与导出都在浏览器本地完成。
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <button
            type="button"
            onClick={() => handleExport("csv")}
            disabled={entries.length === 0}
            className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <Download className="h-3.5 w-3.5" aria-hidden="true" />
            导出 CSV
          </button>
          <button
            type="button"
            onClick={() => handleExport("md")}
            disabled={entries.length === 0}
            className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <FileJson className="h-3.5 w-3.5" aria-hidden="true" />
            导出 Markdown
          </button>
          <button
            type="button"
            onClick={() => void load(true)}
            disabled={loading}
            className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
            刷新
          </button>
        </div>
      </header>

      {hasMore ? (
        <p className="text-xs text-muted-foreground">
          导出仅包含已加载的 {entries.length} 条记录；还有更多记录未加载，请先加载更多。
        </p>
      ) : null}

      <form
        aria-label="审计筛选"
        onSubmit={handleFilter}
        className="grid grid-cols-1 gap-3 rounded-xl border border-border bg-card p-4 sm:grid-cols-2 lg:grid-cols-4"
      >
        <label className="space-y-1 text-xs font-medium text-muted-foreground">
          操作者 ID
          <input
            type="number"
            min={1}
            value={filters.actor}
            onChange={(event) => setFilters({ ...filters, actor: event.target.value })}
            placeholder="例如 1"
            className="w-full rounded-lg border border-border bg-background px-2.5 py-1.5 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          />
        </label>
        <label className="space-y-1 text-xs font-medium text-muted-foreground">
          操作
          <input
            type="text"
            value={filters.action}
            onChange={(event) => setFilters({ ...filters, action: event.target.value })}
            placeholder="例如 version.created"
            className="w-full rounded-lg border border-border bg-background px-2.5 py-1.5 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          />
        </label>
        <label className="space-y-1 text-xs font-medium text-muted-foreground">
          开始时间
          <input
            type="datetime-local"
            value={filters.from}
            onChange={(event) => setFilters({ ...filters, from: event.target.value })}
            className="w-full rounded-lg border border-border bg-background px-2.5 py-1.5 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          />
        </label>
        <label className="space-y-1 text-xs font-medium text-muted-foreground">
          结束时间
          <input
            type="datetime-local"
            value={filters.to}
            onChange={(event) => setFilters({ ...filters, to: event.target.value })}
            className="w-full rounded-lg border border-border bg-background px-2.5 py-1.5 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          />
        </label>
        <div className="flex items-center gap-2 sm:col-span-2 lg:col-span-4">
          <button
            type="submit"
            className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <Search className="h-3.5 w-3.5" aria-hidden="true" />
            筛选
          </button>
          <button
            type="button"
            onClick={handleReset}
            className="rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            重置
          </button>
        </div>
      </form>

      {error === null ? null : (
        <ErrorState
          title="无法加载审计日志"
          message={error.message}
          requestId={error.requestId}
          onRetry={() => void load(true)}
        />
      )}

      {loading ? <Skeleton lines={4} label="正在加载审计日志" /> : null}

      {showEmpty ? (
        <EmptyState
          icon={ScrollText}
          title="没有符合条件的审计记录"
          description="调整筛选条件或等待新的操作产生记录。"
        />
      ) : null}

      {entries.length === 0 ? null : (
        <div className="overflow-x-auto rounded-xl border border-border bg-card">
          <table className="w-full min-w-[720px] border-collapse text-sm">
            <caption className="sr-only">审计日志列表</caption>
            <thead>
              <tr className="border-b border-border text-left text-xs uppercase tracking-wide text-muted-foreground">
                <th scope="col" className="px-3 py-2 font-medium">
                  时间
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  操作
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  操作者
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  目标
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  请求 ID
                </th>
                <th scope="col" className="px-3 py-2 font-medium text-right">
                  详情
                </th>
              </tr>
            </thead>
            <tbody>
              {entries.map((entry) => (
                <tr key={entry.id} className="border-b border-border/60 last:border-0">
                  <td className="whitespace-nowrap px-3 py-2 text-muted-foreground">
                    {new Date(entry.created_at).toLocaleString("zh-CN")}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs">{entry.action}</td>
                  <td className="px-3 py-2">
                    {entry.actor_user_id === null ? "—" : entry.actor_user_id}
                  </td>
                  <td className="px-3 py-2">
                    {entry.target_type === ""
                      ? entry.target_id
                      : `${entry.target_type}${entry.target_id === "" ? "" : ` #${entry.target_id}`}`}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs text-muted-foreground">
                    {entry.request_id === "" ? "—" : entry.request_id}
                  </td>
                  <td className="px-3 py-2 text-right">
                    <button
                      type="button"
                      aria-label={`查看记录 ${entry.id}`}
                      onClick={() => setSelected(entry)}
                      className="rounded-lg border border-border bg-card px-2.5 py-1 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    >
                      查看
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {hasMore ? (
        <div className="flex justify-center">
          <button
            type="button"
            onClick={() => void load(false, cursor)}
            disabled={loadingMore}
            className="rounded-lg border border-border bg-card px-4 py-2 text-sm font-medium text-foreground transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {loadingMore ? "加载中…" : "加载更多"}
          </button>
        </div>
      ) : null}

      {selected === null ? null : (
        <section
          aria-labelledby="audit-detail-title"
          className="rounded-xl border border-border bg-card p-4"
        >
          <div className="flex items-center justify-between gap-2">
            <h3 id="audit-detail-title" className="text-sm font-semibold">
              记录详情
            </h3>
            <button
              type="button"
              onClick={() => setSelected(null)}
              className="rounded-lg border border-border px-2.5 py-1 text-xs text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              关闭
            </button>
          </div>
          <dl className="mt-3 grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
            <div>
              <dt className="text-xs text-muted-foreground">记录 ID</dt>
              <dd className="font-mono">{selected.id}</dd>
            </div>
            <div>
              <dt className="text-xs text-muted-foreground">时间</dt>
              <dd>{new Date(selected.created_at).toLocaleString("zh-CN")}</dd>
            </div>
            <div>
              <dt className="text-xs text-muted-foreground">操作</dt>
              <dd className="font-mono text-xs">{selected.action}</dd>
            </div>
            <div>
              <dt className="text-xs text-muted-foreground">操作者 ID</dt>
              <dd>{selected.actor_user_id === null ? "匿名" : selected.actor_user_id}</dd>
            </div>
            <div>
              <dt className="text-xs text-muted-foreground">目标类型</dt>
              <dd>{selected.target_type === "" ? "—" : selected.target_type}</dd>
            </div>
            <div>
              <dt className="text-xs text-muted-foreground">目标 ID</dt>
              <dd className="font-mono">{selected.target_id === "" ? "—" : selected.target_id}</dd>
            </div>
            <div>
              <dt className="text-xs text-muted-foreground">请求 ID</dt>
              <dd className="font-mono text-xs">
                {selected.request_id === "" ? "—" : selected.request_id}
              </dd>
            </div>
            <div>
              <dt className="text-xs text-muted-foreground">IP</dt>
              <dd className="font-mono text-xs">{selected.ip === "" ? "—" : selected.ip}</dd>
            </div>
            <div className="sm:col-span-2">
              <dt className="text-xs text-muted-foreground">User-Agent</dt>
              <dd className="break-all font-mono text-xs">
                {selected.user_agent === "" ? "—" : selected.user_agent}
              </dd>
            </div>
            <div className="sm:col-span-2">
              <dt className="text-xs text-muted-foreground">元数据</dt>
              <dd>
                <pre className="mt-1 overflow-x-auto rounded-lg bg-muted/60 p-2 font-mono text-xs">
                  {metadataText(selected.metadata)}
                </pre>
              </dd>
            </div>
          </dl>
        </section>
      )}
    </section>
  )
}
