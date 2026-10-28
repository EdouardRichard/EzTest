"use client"

import type { Batch, BatchStatus } from "../types"
import {
  batchKindLabel,
  batchOptionLabel,
  batchOriginLabel,
  batchParentLabel,
  effectiveBatchKind,
} from "./batch-kind"

const BATCH_STATUS_LABELS: Record<BatchStatus, string> = {
  planned: "待执行",
  running: "执行中",
  done: "已完成",
  failed: "失败",
  cancelled: "已取消",
}

const KIND_BADGE_CLASS =
  "inline-flex items-center gap-1 rounded-full border border-border bg-muted px-2 py-0.5 text-[11px] font-medium text-foreground"

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
 * Header batch switcher (spec §9.1/§9.4, P7 §10.6). The owning container loads
 * the page with `useBatches` and owns the selection, so the same batch id can be
 * passed to the report tab and the execution tabs. Every option carries the P7
 * kind (基线/增量/补全); the selected batch also shows its kind badge, parent and
 * the supplement Agent origin. Renders nothing when the version has no batches
 * yet — the start button owns that empty state.
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
  const selectedBatch = batches.find((batch) => batch.id === selected) ?? batches[0]
  const parentLabel = selectedBatch === undefined ? "" : batchParentLabel(selectedBatch, batches)
  const originLabel = selectedBatch === undefined ? "" : batchOriginLabel(selectedBatch)

  return (
    <div className="flex flex-wrap items-center gap-2">
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
          {batches.map((batch) => {
            const parentLabel = batchParentLabel(batch, batches)
            return (
              <option key={batch.id} value={batch.id}>
                {`${batchOptionLabel(batch, BATCH_STATUS_LABELS[batch.status])}${
                  parentLabel === "" ? "" : ` · ${parentLabel}`
                }`}
              </option>
            )
          })}
        </select>
      </label>

      {selectedBatch === undefined ? null : (
        <span
          data-testid="batch-kind-badge"
          data-kind={effectiveBatchKind(selectedBatch)}
          className={KIND_BADGE_CLASS}
        >
          {batchKindLabel(selectedBatch)}
        </span>
      )}
      {originLabel === "" ? null : (
        <span data-testid="batch-origin" className="text-[11px] text-muted-foreground">
          {originLabel}
        </span>
      )}
      {parentLabel === "" ? null : (
        <span data-testid="batch-parent" className="text-[11px] text-muted-foreground">
          {parentLabel}
        </span>
      )}
    </div>
  )
}
