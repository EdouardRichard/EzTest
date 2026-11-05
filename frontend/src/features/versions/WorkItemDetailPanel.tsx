"use client"

import { useCallback, useEffect, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { formatDateTime } from "@/lib/format"

import { getWorkItem, type WorkItem } from "./api"

export type WorkItemDetailPanelProps = {
  versionId: string
  workItemId: string
  onClose?: () => void
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

function record(value: Record<string, unknown> | undefined): string {
  if (value === undefined) return "—"
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return "—"
  }
}

/**
 * One work-item projection (`GET /api/versions/{id}/work-items/{workItemId}`).
 * The payload/result are references and summaries only; prompts and credentials
 * never leave the backend.
 */
export function WorkItemDetailPanel({ versionId, workItemId, onClose }: WorkItemDetailPanelProps) {
  const [item, setItem] = useState<WorkItem | null>(null)
  const [error, setError] = useState<unknown>(null)

  const load = useCallback(async () => {
    setError(null)
    try {
      setItem(await getWorkItem(versionId, workItemId))
    } catch (cause) {
      setItem(null)
      setError(cause)
    }
  }, [versionId, workItemId])

  useEffect(() => {
    void load()
  }, [load])

  return (
    <section
      data-testid="work-item-detail"
      aria-label="work-item 详情"
      className="space-y-2 rounded-lg border border-border bg-card px-3 py-2.5"
    >
      <header className="flex flex-wrap items-center gap-2">
        <h3 className="text-xs font-semibold text-foreground">work-item #{workItemId}</h3>
        {item !== null ? (
          <Badge tone={STATE_TONE[item.state] ?? "neutral"}>{item.state}</Badge>
        ) : null}
        {onClose !== undefined ? (
          <button
            type="button"
            className="ml-auto rounded-md px-2 py-0.5 text-[11px] text-muted-foreground hover:bg-muted hover:text-foreground"
            onClick={onClose}
          >
            收起
          </button>
        ) : null}
      </header>

      {error !== null ? (
        <ErrorState error={error} title="work-item 加载失败" onRetry={() => void load()} />
      ) : item === null ? (
        <LoadingCard label="正在加载 work-item…" />
      ) : (
        <div className="space-y-2 text-[11px]">
          <dl className="grid grid-cols-2 gap-2">
            <div>
              <dt className="text-muted-foreground">类型</dt>
              <dd className="font-medium text-foreground">{item.kind}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">状态</dt>
              <dd className="font-medium text-foreground">{item.state}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">创建者</dt>
              <dd className="font-medium text-foreground">{item.created_by}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">尝试次数</dt>
              <dd className="font-medium text-foreground">{item.attempt}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">创建时间</dt>
              <dd className="font-medium text-foreground">{formatDateTime(item.created_at)}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">更新时间</dt>
              <dd className="font-medium text-foreground">{formatDateTime(item.updated_at)}</dd>
            </div>
          </dl>

          <div>
            <p className="text-muted-foreground">目标</p>
            <p className="text-foreground">{item.goal}</p>
          </div>

          {item.depends_on !== undefined && item.depends_on !== null ? (
            <p className="text-muted-foreground">
              依赖：
              {item.depends_on.length === 0
                ? "无"
                : item.depends_on.map((id) => `#${id}`).join("、")}
            </p>
          ) : null}

          <div>
            <p className="text-muted-foreground">结果引用</p>
            <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-all rounded-md bg-muted/40 p-2 text-[11px] text-foreground">
              {record(item.result)}
            </pre>
          </div>
        </div>
      )}
    </section>
  )
}
