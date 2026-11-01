"use client"

import { Bell, RefreshCw, X } from "lucide-react"
import Link from "next/link"
import { useEffect, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { PendingDesignCTA } from "@/features/design/PendingDesignCTA"
import { listHumanRequests } from "@/features/execution/api"
import { HUMAN_REQUEST_KIND_LABEL } from "@/features/execution/labels"
import type { HumanRequest } from "@/features/execution/types"
import { getVersion } from "@/features/versions/api"
import type { Version } from "@/features/versions/types"
import { formatDateTime } from "@/lib/format"

export type TodoDrawerProps = {
  /** Current version context; `null` shows the no-version empty state. */
  versionId: string | null
  open: boolean
  onClose: () => void
}

/**
 * Read-only current-version todo drawer (spec §9.5): open human requests plus
 * the pending-design CTA. It never resolves/cancels a request and never adds a
 * `/api/todos` endpoint; links deep-link into the execution/design tabs.
 */
export function TodoDrawer({ versionId, open, onClose }: TodoDrawerProps) {
  const [version, setVersion] = useState<Version | null>(null)
  const [requests, setRequests] = useState<HumanRequest[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken forces a refresh
  useEffect(() => {
    if (!open) return undefined
    if (versionId === null) {
      setVersion(null)
      setRequests([])
      setLoading(false)
      setError(null)
      return undefined
    }
    let cancelled = false
    setLoading(true)
    setError(null)
    void Promise.all([getVersion(versionId), listHumanRequests(versionId, { status: "open" })])
      .then(([nextVersion, items]) => {
        if (cancelled) return
        setVersion(nextVersion)
        setRequests(items.filter((request) => request.status === "open"))
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
  }, [open, versionId, reloadToken])

  if (!open) return null

  const pendingDesignCount = version?.pending_design_count ?? 0
  const empty = requests.length === 0 && pendingDesignCount === 0

  return (
    <div className="fixed inset-0 z-50">
      <button
        type="button"
        aria-label="关闭待办遮罩"
        onClick={onClose}
        className="absolute inset-0 h-full w-full bg-slate-950/40 backdrop-blur-[1px]"
      />
      <aside
        role="dialog"
        aria-modal="true"
        aria-label="待办"
        data-testid="todo-drawer"
        className="absolute inset-y-0 right-0 flex w-full max-w-md flex-col border-l border-border bg-card text-card-foreground shadow-2xl"
      >
        <header className="flex items-start justify-between gap-3 border-b border-border px-5 py-4">
          <div className="min-w-0">
            <p className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
              <Bell className="size-3.5" aria-hidden="true" />
              当前版本待办
            </p>
            <h2 className="mt-0.5 truncate text-sm font-semibold text-foreground">
              {version?.title ?? "待办"}
            </h2>
          </div>
          <div className="flex items-center gap-1">
            {versionId !== null ? (
              <Button
                variant="ghost"
                size="sm"
                aria-label="刷新待办"
                disabled={loading}
                onClick={() => setReloadToken((token) => token + 1)}
              >
                <RefreshCw className="size-3.5" aria-hidden="true" />
              </Button>
            ) : null}
            <Button variant="ghost" size="sm" aria-label="关闭待办" onClick={onClose}>
              <X className="size-4" aria-hidden="true" />
            </Button>
          </div>
        </header>

        <div className="flex-1 space-y-4 overflow-y-auto px-5 py-4">
          {versionId === null ? (
            <EmptyState
              title="未选择版本"
              description="打开某个版本详情后可查看该版本的待处理事项。"
              className="border-none bg-transparent"
            />
          ) : loading ? (
            <LoadingCard label="正在加载待办…" />
          ) : error !== null ? (
            <ErrorState
              error={error}
              title="待办加载失败"
              onRetry={() => setReloadToken((token) => token + 1)}
            />
          ) : (
            <>
              {empty ? (
                <EmptyState
                  title="暂无待办"
                  description="当前版本没有待处理的人工请求，也没有待设计需求。"
                  className="border-none bg-transparent py-6"
                />
              ) : null}

              <section aria-label="人工请求待办" className="space-y-2">
                <h3 className="text-xs font-semibold text-foreground">
                  人工请求 {requests.length}
                </h3>
                {requests.length === 0 ? (
                  <p className="text-[11px] text-muted-foreground">暂无待处理的人工请求。</p>
                ) : (
                  <ul className="space-y-2">
                    {requests.map((request) => (
                      <li
                        key={request.id}
                        data-testid={`todo-human-request-${request.id}`}
                        className="rounded-lg border border-border bg-card px-3 py-2"
                      >
                        <div className="flex flex-wrap items-center gap-1.5">
                          <Badge tone="warning">{HUMAN_REQUEST_KIND_LABEL[request.kind]}</Badge>
                          {request.payload.version_sut_id !== undefined &&
                          request.payload.version_sut_id !== null ? (
                            <span className="text-[10px] text-muted-foreground">
                              SUT {request.payload.version_sut_id}
                            </span>
                          ) : null}
                          <Link
                            href={humanRequestHref(versionId, request)}
                            className="ml-auto text-xs text-primary underline-offset-2 hover:underline"
                          >
                            去处理
                          </Link>
                        </div>
                        <p className="mt-1 text-xs text-foreground">{request.payload.reason}</p>
                        <p className="mt-0.5 text-[10px] text-muted-foreground">
                          过期时间 {formatDateTime(request.expires_at)}
                        </p>
                      </li>
                    ))}
                  </ul>
                )}
              </section>

              <section aria-label="待设计待办" className="space-y-2">
                <h3 className="text-xs font-semibold text-foreground">待设计</h3>
                {pendingDesignCount > 0 ? (
                  <PendingDesignCTA
                    versionId={versionId}
                    pendingCount={pendingDesignCount}
                    hasConversation={false}
                    href={`/versions/${encodeURIComponent(versionId)}?tab=design`}
                  />
                ) : (
                  <p className="text-[11px] text-muted-foreground">暂无待设计需求。</p>
                )}
              </section>
            </>
          )}
        </div>
      </aside>
    </div>
  )
}

function humanRequestHref(versionId: string, request: HumanRequest): string {
  const params = new URLSearchParams({ tab: "execution" })
  if (request.case_run_id !== null) params.set("case_run", request.case_run_id)
  return `/versions/${encodeURIComponent(versionId)}?${params.toString()}`
}

export type TodoDrawerButtonProps = {
  /** Derived from the current route; no button is rendered outside a version. */
  versionId: string | null
}

/** Topbar todo entry: open count + the shared read-only drawer. */
export function TodoDrawerButton({ versionId }: TodoDrawerButtonProps) {
  const [open, setOpen] = useState(false)
  const [openCount, setOpenCount] = useState<number | null>(null)

  useEffect(() => {
    if (versionId === null) {
      setOpenCount(null)
      return undefined
    }
    let cancelled = false
    void listHumanRequests(versionId, { status: "open" })
      .then((items) => {
        if (!cancelled) setOpenCount(items.filter((item) => item.status === "open").length)
      })
      .catch(() => {
        if (!cancelled) setOpenCount(null)
      })
    return () => {
      cancelled = true
    }
  }, [versionId])

  if (versionId === null) return null

  return (
    <>
      <Button
        size="sm"
        variant="secondary"
        data-testid="todo-drawer-trigger"
        aria-label="打开待办"
        onClick={() => setOpen(true)}
      >
        待办{openCount === null ? "" : ` ${openCount}`}
      </Button>
      <TodoDrawer versionId={versionId} open={open} onClose={() => setOpen(false)} />
    </>
  )
}
