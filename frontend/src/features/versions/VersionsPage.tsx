"use client"

import { Eye, Layers, Loader2, Plus, RefreshCw } from "lucide-react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { useCallback, useEffect, useRef, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge } from "@/components/ui/StatusBadge"
import { ApiError } from "@/lib/api-client"

import { listVersions } from "./api"
import { CreateVersionDialog } from "./CreateVersionDialog"
import type { Version, VersionStatus } from "./types"
import { VERSION_STATUS_ORDER, versionStatusLabel, versionStatusVariant } from "./VersionHeader"

const PAGE_SIZE = 20

function describeError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "未知错误"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  if (error.status === 401) return "登录状态已失效，请重新登录。"
  const reason = typeof error.details.reason === "string" ? error.details.reason : ""
  if (reason !== "") return `${error.message}（${reason}）`
  return error.message
}

function formatTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString("zh-CN")
}

/**
 * `/versions` list (spec §9.1/§9.2): status filter, cursor pagination, empty /
 * loading / error states, and the create dialog entry. Every row links to the
 * detail route by the version's raw string ID.
 */
export function VersionsPage() {
  const router = useRouter()
  const requestRef = useRef(0)

  const [versions, setVersions] = useState<Version[]>([])
  const [statusFilter, setStatusFilter] = useState<VersionStatus | "">("")
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState("")
  const [nextCursor, setNextCursor] = useState("")
  const [hasMore, setHasMore] = useState(false)
  const [dialogOpen, setDialogOpen] = useState(false)

  const loadFirstPage = useCallback(async (status: VersionStatus | "") => {
    const requestId = ++requestRef.current
    setLoading(true)
    setError("")
    try {
      const result = await listVersions(
        status === "" ? { limit: PAGE_SIZE } : { status, limit: PAGE_SIZE },
      )
      if (requestRef.current !== requestId) return
      setVersions(result.items)
      setNextCursor(result.nextCursor)
      setHasMore(result.hasMore)
    } catch (caught) {
      if (requestRef.current === requestId) setError(describeError(caught))
    } finally {
      if (requestRef.current === requestId) setLoading(false)
    }
  }, [])

  useEffect(() => {
    void loadFirstPage(statusFilter)
  }, [loadFirstPage, statusFilter])

  async function loadMore(): Promise<void> {
    if (!hasMore || nextCursor === "" || loadingMore) return
    const requestId = requestRef.current
    setLoadingMore(true)
    setError("")
    try {
      const result = await listVersions(
        statusFilter === ""
          ? { cursor: nextCursor, limit: PAGE_SIZE }
          : { status: statusFilter, cursor: nextCursor, limit: PAGE_SIZE },
      )
      if (requestRef.current !== requestId) return
      setVersions((previous) => [...previous, ...result.items])
      setNextCursor(result.nextCursor)
      setHasMore(result.hasMore)
    } catch (caught) {
      if (requestRef.current === requestId) setError(describeError(caught))
    } finally {
      if (requestRef.current === requestId) setLoadingMore(false)
    }
  }

  function openVersion(version: Version): void {
    router.push(`/versions/${version.id}`)
  }

  function handleCreated(created: Version): void {
    setDialogOpen(false)
    router.push(`/versions/${created.id}`)
  }

  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="text-2xl font-semibold tracking-tight">版本</h1>
            <StatusBadge variant="info">P3</StatusBadge>
          </div>
          <p className="mt-1 text-sm text-muted-foreground">
            版本列表、多需求录入与澄清工作台；点击行进入版本详情。
          </p>
        </div>
        <div className="flex flex-wrap gap-1.5">
          <button
            type="button"
            onClick={() => setDialogOpen(true)}
            className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <Plus className="h-4 w-4" aria-hidden="true" />
            新建版本
          </button>
          <button
            type="button"
            onClick={() => void loadFirstPage(statusFilter)}
            disabled={loading}
            className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <RefreshCw className={`h-4 w-4 ${loading ? "animate-spin" : ""}`} aria-hidden="true" />
            刷新
          </button>
        </div>
      </header>

      <div className="flex flex-wrap items-center gap-2">
        <label
          htmlFor="version-status-filter"
          className="text-xs font-medium text-muted-foreground"
        >
          状态筛选
        </label>
        <select
          id="version-status-filter"
          value={statusFilter}
          onChange={(event) => setStatusFilter(event.target.value as VersionStatus | "")}
          className="rounded-lg border border-border bg-input px-3 py-1.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <option value="">全部</option>
          {VERSION_STATUS_ORDER.map((status) => (
            <option key={status} value={status}>
              {versionStatusLabel(status)}
            </option>
          ))}
        </select>
      </div>

      {error === "" ? null : (
        <div
          role="alert"
          className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          <span>加载版本列表失败：{error}</span>
          <button
            type="button"
            onClick={() => void loadFirstPage(statusFilter)}
            className="rounded-lg border border-border bg-card px-2.5 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            重试
          </button>
        </div>
      )}

      {loading && versions.length === 0 ? (
        <p className="flex items-center gap-2 rounded-xl border border-dashed border-border px-4 py-10 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          正在加载版本…
        </p>
      ) : null}

      {!loading && error === "" && versions.length === 0 ? (
        <EmptyState
          icon={Layers}
          title="暂无版本"
          description="点击「新建版本」创建第一个版本，然后录入需求并开始澄清。"
        />
      ) : null}

      {versions.length === 0 ? null : (
        <div className="overflow-x-auto rounded-xl border border-border">
          <table className="min-w-full divide-y divide-border text-sm">
            <caption className="sr-only">版本列表</caption>
            <thead className="bg-muted/50 text-left text-[11px] uppercase tracking-wide text-muted-foreground">
              <tr>
                <th scope="col" className="px-3 py-2 font-medium">
                  名称 / 标识
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  状态
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  需求
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  已确认
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  待澄清
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  更新时间
                </th>
                <th scope="col" className="px-3 py-2 text-right font-medium">
                  操作
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {versions.map((version) => (
                <tr
                  key={version.id}
                  tabIndex={0}
                  onClick={(event) => {
                    const target = event.target as HTMLElement
                    if (target.closest("a, button")) return
                    openVersion(version)
                  }}
                  onKeyDown={(event) => {
                    if (event.key === "Enter" && event.target === event.currentTarget) {
                      openVersion(version)
                    }
                  }}
                  className="cursor-pointer transition-colors hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  <td className="px-3 py-2 align-top">
                    <div className="font-medium">{version.name}</div>
                    <div className="mt-0.5 flex flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
                      <span className="font-mono">
                        {version.label === "" ? "—" : version.label}
                      </span>
                      <span>·</span>
                      <span className="max-w-[20rem] truncate">
                        {version.description === "" ? "无描述" : version.description}
                      </span>
                    </div>
                  </td>
                  <td className="px-3 py-2 align-top">
                    <StatusBadge variant={versionStatusVariant(version.status)}>
                      {versionStatusLabel(version.status)}
                    </StatusBadge>
                  </td>
                  <td className="px-3 py-2 align-top">{version.requirementCount}</td>
                  <td className="px-3 py-2 align-top">{version.clarifiedCount}</td>
                  <td className="px-3 py-2 align-top">{version.openQuestionCount}</td>
                  <td className="px-3 py-2 align-top text-xs text-muted-foreground">
                    {formatTime(version.updatedAt)}
                  </td>
                  <td className="px-3 py-2 align-top text-right">
                    <Link
                      href={`/versions/${version.id}`}
                      onClick={(event) => event.stopPropagation()}
                      className="inline-flex items-center gap-1 text-xs font-medium text-sky-600 transition-colors hover:text-sky-500 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring dark:text-sky-400"
                    >
                      <Eye className="h-3.5 w-3.5" aria-hidden="true" />
                      查看
                    </Link>
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
            onClick={() => void loadMore()}
            disabled={loadingMore}
            className="rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {loadingMore ? "加载中…" : "加载更多"}
          </button>
        </div>
      ) : null}

      <CreateVersionDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        onCreated={handleCreated}
      />
    </div>
  )
}
