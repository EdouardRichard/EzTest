"use client"

import { Loader2, PauseCircle, Play } from "lucide-react"

import { StatusBadge } from "@/components/ui/StatusBadge"

import type { Batch, BatchCounts, PauseMode } from "../types"

const PAUSE_MODE_LABELS: Record<PauseMode, string> = {
  graceful: "优雅暂停",
  immediate: "立即暂停",
}

/** Terminal counts of one batch: the cases that no longer need execution work. */
export function completedCaseCount(counts: BatchCounts): number {
  return (
    counts.passed +
    counts.failed +
    counts.blocked +
    counts.inconclusive +
    counts.skipped +
    counts.interrupted
  )
}

export type PausedBannerProps = {
  batch: Batch
  busy?: boolean
  error?: string
  onResume?: () => void
}

/**
 * Version-level paused banner (spec §9.1/§9.3). It shows whether the batch was
 * paused gracefully or immediately, the number of finished cases and — for an
 * immediate pause — how many runs were interrupted. Resume is the only action;
 * the header keeps the batch selector visible while paused.
 */
export function PausedBanner({ batch, busy = false, error = "", onResume }: PausedBannerProps) {
  const modeLabel = batch.pause_mode === "" ? "暂停" : PAUSE_MODE_LABELS[batch.pause_mode]
  const completed = completedCaseCount(batch.counts)
  const total = batch.counts.total
  const interrupted = batch.counts.interrupted

  return (
    <section
      data-testid="paused-banner"
      data-pause-mode={batch.pause_mode}
      data-completed={completed}
      data-total={total}
      role="status"
      className="space-y-2 rounded-xl border border-amber-500/40 bg-amber-500/10 px-3 py-2.5 text-amber-800 dark:text-amber-200"
    >
      <div className="flex flex-wrap items-center gap-2">
        <PauseCircle className="h-4 w-4 shrink-0" aria-hidden="true" />
        <span className="text-sm font-semibold">{`批次 #${batch.batch_no} ${modeLabel}`}</span>
        <StatusBadge variant="warning">{modeLabel}</StatusBadge>
        <span className="text-xs">{`已完成 ${completed}/${total} 个用例`}</span>
        {interrupted > 0 ? <span className="text-xs">{`已中断 ${interrupted} 个用例`}</span> : null}
        <button
          type="button"
          data-testid="exec-resume-btn"
          onClick={() => onResume?.()}
          disabled={busy}
          className="ml-auto inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {busy ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
          ) : (
            <Play className="h-3.5 w-3.5" aria-hidden="true" />
          )}
          {busy ? "恢复中…" : "恢复执行"}
        </button>
      </div>

      {batch.pause_reason === "" ? null : (
        <p className="text-xs">{`暂停原因：${batch.pause_reason}`}</p>
      )}

      {error === "" ? null : (
        <p role="alert" className="text-xs font-medium text-destructive">
          {error}
        </p>
      )}
    </section>
  )
}
