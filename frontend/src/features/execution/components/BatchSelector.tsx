"use client"

import type { Batch, BatchStatus } from "../types"

const BATCH_STATUS_LABELS: Record<BatchStatus, string> = {
  planned: "待执行",
  running: "执行中",
  done: "已完成",
  failed: "失败",
  cancelled: "已取消",
}

export type BatchSelectorProps = {
  /** Batches of the current version, newest first (as `useBatches` returns them). */
  batches: Batch[]
  /** Selected batch id; empty falls back to the first option. */
  value: string
  onChange?: (batchId: string) => void
  /** Disables the control while a batch page is loading. */
  loading?: boolean
  disabled?: boolean
}

/**
 * Header batch switcher (spec §9.1/§9.4). The owning container loads the page
 * with `useBatches` and owns the selection, so the same batch id can be passed
 * to the report tab and the execution tabs. Renders nothing when the version
 * has no batches yet — the start button owns that empty state.
 */
export function BatchSelector({
  batches,
  value,
  onChange,
  loading = false,
  disabled = false,
}: BatchSelectorProps) {
  if (batches.length === 0) return null

  const selected =
    value !== "" && batches.some((batch) => batch.id === value) ? value : (batches[0]?.id ?? "")

  return (
    <label className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
      <span>批次</span>
      <select
        data-testid="exec-batch-selector"
        aria-label="选择执行批次"
        value={selected}
        onChange={(event) => onChange?.(event.target.value)}
        disabled={loading || disabled}
        className="rounded-lg border border-border bg-card px-2.5 py-1.5 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
      >
        {batches.map((batch) => (
          <option key={batch.id} value={batch.id}>
            {`批次 #${batch.batch_no}（${BATCH_STATUS_LABELS[batch.status]}）`}
          </option>
        ))}
      </select>
    </label>
  )
}
