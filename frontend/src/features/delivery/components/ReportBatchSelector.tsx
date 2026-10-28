"use client"

import { BATCH_KIND_LABELS, BATCH_STATUS_LABELS, labelOf } from "../labels"
import type { ReportBatchSummary } from "../types"

const SELECT_CLASS =
  "rounded-lg border border-border bg-card px-2.5 py-1.5 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"

export type ReportBatchSelectorProps = {
  /** Batches of the version, newest first (P6 batch DTO subset). */
  batches: ReportBatchSummary[]
  value: string
  disabled: boolean
  onChange: (batchId: string) => void
}

/** Batch dropdown of the report tab, with kind/status and pass/fail counts. */
export function ReportBatchSelector({
  batches,
  value,
  disabled,
  onChange,
}: ReportBatchSelectorProps) {
  return (
    <label className="flex items-center gap-2 text-xs text-muted-foreground">
      <span>批次</span>
      <select
        data-testid="report-batch-selector"
        value={value}
        onChange={(event) => onChange(event.target.value)}
        disabled={disabled}
        className={SELECT_CLASS}
      >
        {batches.map((batch) =>
          batch.id === "" ? null : (
            <option key={batch.id} value={batch.id}>
              {`Batch #${batch.batchNo} · ${labelOf(BATCH_KIND_LABELS, batch.kind)} · ${labelOf(BATCH_STATUS_LABELS, batch.status)}${
                batch.counts === undefined
                  ? ""
                  : ` · 通过 ${batch.counts.passed} / 失败 ${batch.counts.failed}`
              }`}
            </option>
          ),
        )}
      </select>
    </label>
  )
}
