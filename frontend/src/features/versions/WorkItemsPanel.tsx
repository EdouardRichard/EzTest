"use client"

import Link from "next/link"
import { useCallback, useEffect, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { formatDateTime } from "@/lib/format"

import { listWorkItems, type WorkItem } from "./api"
import { WorkItemDetailPanel } from "./WorkItemDetailPanel"

export type WorkItemsPanelProps = {
  versionId: string
  /** Deep link `?work_item=` selects a work item on mount. */
  initialWorkItemId?: string
  /** SSE-driven refresh nonce. */
  refreshKey?: number
}

const STATE_TONE: Record<string, "neutral" | "info" | "success" | "warning" | "danger"> = {
  open: "info",
  claimed: "info",
  running: "info",
  waiting_human: "warning",
  done: "success",
  failed: "danger",
  cancelled: "neutral",
}

function workItemHref(versionId: string, workItemId: string): string {
  const params = new URLSearchParams({ tab: "overview", work_item: workItemId })
  return `/versions/${encodeURIComponent(versionId)}?${params.toString()}`
}

/**
 * Version work-item list (`GET /api/versions/{id}/work-items`). Rows link to
 * `?work_item=` deep links; a selected row mounts {@link WorkItemDetailPanel}
 * which reads the single work-item projection.
 */
export function WorkItemsPanel({
  versionId,
  initialWorkItemId,
  refreshKey = 0,
}: WorkItemsPanelProps) {
  const [items, setItems] = useState<WorkItem[] | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [selectedId, setSelectedId] = useState<string | null>(initialWorkItemId ?? null)

  const load = useCallback(async () => {
    setError(null)
    try {
      setItems(await listWorkItems(versionId))
    } catch (cause) {
      setItems(null)
      setError(cause)
    }
  }, [versionId])

  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey is the SSE nonce.
  useEffect(() => {
    void load()
  }, [load, refreshKey])

  useEffect(() => {
    if (initialWorkItemId !== undefined && initialWorkItemId !== "") {
      setSelectedId(initialWorkItemId)
    }
  }, [initialWorkItemId])

  return (
    <section
      data-testid="work-items-panel"
      aria-label="作业与任务单"
      className="space-y-2 rounded-lg border border-border bg-card px-3 py-2.5"
    >
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">作业与任务单</h2>
          <p className="text-[11px] text-muted-foreground">
            最近 {items?.length ?? 0} 条 work-item · 详情与结果只含引用摘要
          </p>
        </div>
        <Button size="sm" variant="secondary" onClick={() => void load()}>
          刷新
        </Button>
      </header>

      {error !== null ? (
        <ErrorState error={error} title="作业列表加载失败" onRetry={() => void load()} />
      ) : null}

      {items === null && error === null ? (
        <LoadingCard label="正在加载作业列表…" />
      ) : items !== null && items.length === 0 ? (
        <EmptyState
          title="该版本暂无作业"
          description="探索、用例生成、执行与报告流水线产生的 work-item 会在这里出现。"
        />
      ) : items !== null ? (
        <ul className="space-y-1.5">
          {items.map((item) => (
            <li
              key={item.id}
              className={
                item.id === selectedId
                  ? "rounded-md border border-primary/40 bg-primary/5 px-2 py-1.5"
                  : "rounded-md border border-border px-2 py-1.5"
              }
            >
              <div className="flex flex-wrap items-center gap-1.5 text-[11px]">
                <Link
                  href={workItemHref(versionId, item.id)}
                  className="font-medium text-foreground underline underline-offset-2"
                >
                  work-item #{item.id} 深链
                </Link>
                <Badge tone={STATE_TONE[item.state] ?? "neutral"}>{item.state}</Badge>
                <span className="text-muted-foreground">{item.kind}</span>
                <span className="text-muted-foreground">· {formatDateTime(item.created_at)}</span>
                <button
                  type="button"
                  aria-label={`查看 work-item #${item.id}`}
                  className="ml-auto rounded-md border border-border px-2 py-0.5 text-muted-foreground hover:bg-muted hover:text-foreground"
                  onClick={() => setSelectedId(item.id)}
                >
                  查看详情
                </button>
              </div>
              <p className="mt-0.5 truncate text-[11px] text-foreground">{item.goal}</p>
            </li>
          ))}
        </ul>
      ) : null}

      {selectedId !== null ? (
        <WorkItemDetailPanel
          versionId={versionId}
          workItemId={selectedId}
          onClose={() => setSelectedId(null)}
        />
      ) : null}
    </section>
  )
}
