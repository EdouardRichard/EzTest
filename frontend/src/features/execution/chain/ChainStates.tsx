"use client"

import { GitBranch, Loader2, TriangleAlert } from "lucide-react"

import { EmptyState } from "@/components/ui/EmptyState"
import type { SSEStatus } from "@/lib/sse-client"
import { cn } from "@/lib/utils"

/** Loading skeleton of the exploration chain (spec §5.5: 骨架屏). */
export function ChainSkeleton({ className }: { className?: string }) {
  return (
    <div
      data-testid="chain-skeleton"
      role="status"
      className={cn(
        "flex min-h-[420px] items-center justify-center gap-2 rounded-2xl border border-dashed border-border bg-card/60 px-4 py-10 text-sm text-muted-foreground",
        className,
      )}
    >
      <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
      探索链路加载中…
    </div>
  )
}

export type ChainEmptyProps = {
  /** True when filters narrowed the graph to nothing; changes the wording. */
  filtered?: boolean
}

/** Independent empty state, distinct from an empty filtered result (spec §5.5). */
export function ChainEmpty({ filtered = false }: ChainEmptyProps) {
  if (filtered) {
    return (
      <p
        data-testid="exploration-filtered-empty"
        className="rounded-xl border border-dashed border-border px-4 py-10 text-center text-sm text-muted-foreground"
      >
        没有符合筛选条件的链路节点
      </p>
    )
  }
  return (
    <div data-testid="exploration-empty">
      <EmptyState
        icon={GitBranch}
        title="暂无探索链路"
        description="版本的需求、用例与执行结果会在这里形成需求 → 用例 → 结果链路。"
      />
    </div>
  )
}

type RetryProps = {
  message: string
  onRetry: () => void
}

/** Red full-failure state with retry (spec §5.5: 全失败红条 + 重试). */
export function ChainError({ message, onRetry }: RetryProps) {
  return (
    <div
      data-testid="exploration-error"
      role="alert"
      className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-destructive/30 bg-destructive/10 px-4 py-3 text-sm text-destructive"
    >
      <p>{`加载探索链路失败：${message}`}</p>
      <button
        type="button"
        data-testid="chain-error-retry"
        onClick={onRetry}
        className="inline-flex min-h-11 items-center rounded-lg border border-border bg-card px-3 text-xs font-medium text-foreground hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        重试
      </button>
    </div>
  )
}

/** Amber banner shown when a refresh failed but a previous snapshot is kept. */
export function ChainDegraded({ message, onRetry }: RetryProps) {
  return (
    <div
      data-testid="exploration-degraded"
      role="alert"
      className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-amber-500/25 bg-amber-500/10 px-4 py-3 text-sm text-amber-700 dark:text-amber-300"
    >
      <p>{`链路刷新失败（保留上次数据）：${message}`}</p>
      <button
        type="button"
        data-testid="chain-degraded-retry"
        onClick={onRetry}
        className="inline-flex min-h-11 items-center rounded-lg border border-border bg-card px-3 text-xs font-medium text-foreground hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        重试
      </button>
    </div>
  )
}

export type ChainTruncatedProps = {
  /** Nodes rendered in the snapshot after the server-side budget. */
  shown: number
  /** Real pre-truncation total from `snapshot.counts`. */
  total: number
  /** Chinese layer names whose `truncated` flag is true. */
  layers: readonly string[]
}

/** Amber truncation banner with the real counts (spec §5.5 / C5). */
export function ChainTruncated({ shown, total, layers }: ChainTruncatedProps) {
  return (
    <div
      data-testid="chain-truncated"
      role="status"
      className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-4 py-3 text-xs text-amber-700 dark:text-amber-300"
    >
      {`已显示前 ${shown} 条（共 ${total} 条），请收窄筛选（${layers.join("、")}层已截断）。`}
    </div>
  )
}

const STREAM_STATUS_LABELS: Record<SSEStatus, string> = {
  connecting: "重连中",
  reconnecting: "重连中",
  live: "实时",
  closed: "未连接",
}

/** Version-stream status; "重连中" must not be mistaken for an empty chain. */
export function ChainStreamStatus({ status }: { status: SSEStatus }) {
  return (
    <p
      data-testid="chain-stream-status"
      data-state={status}
      role="status"
      aria-live="polite"
      className={cn(
        "text-xs",
        status === "live"
          ? "text-muted-foreground"
          : "font-medium text-amber-700 dark:text-amber-300",
      )}
    >
      {STREAM_STATUS_LABELS[status]}
    </p>
  )
}

/** Issue-layer failure banner; never renders "0 个问题" (spec §5.5). */
export function ChainIssuesDegraded({ message }: { message: string }) {
  return (
    <p
      data-testid="chain-issues-degraded"
      role="alert"
      className="mt-2 flex items-start gap-1.5 rounded-lg border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
    >
      <TriangleAlert className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
      <span>{`问题数据加载失败：${message} 问题节点可能不完整（AI 分析降级时同样可能不完整）。`}</span>
    </p>
  )
}
