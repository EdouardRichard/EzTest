"use client"

import { Button } from "@/components/ui/Button"
import { InlineError } from "@/components/ui/ErrorState"

export type BulkConfirmBarProps = {
  selectedCount: number
  confirming?: boolean
  /** Result line, e.g. "已确认 3 条，跳过 2 条已确认需求". */
  notice?: string | null
  error?: unknown
  onConfirm: () => void
}

/** Selection summary + batch confirmation action (spec §6.1, P1-D8). */
export function BulkConfirmBar({
  selectedCount,
  confirming = false,
  notice = null,
  error = null,
  onConfirm,
}: BulkConfirmBarProps) {
  if (selectedCount === 0) return null

  return (
    <div
      data-testid="bulk-confirm-bar"
      className="mx-3 mt-2.5 rounded-lg border border-dashed border-primary/40 bg-primary/5 px-2.5 py-2 text-[11px] text-muted-foreground"
    >
      <div className="flex items-center gap-2">
        <span>已选 {selectedCount} 条</span>
        <Button size="sm" disabled={confirming} onClick={onConfirm} className="ml-auto">
          {confirming ? "确认中…" : "批量确认已澄清"}
        </Button>
      </div>
      {notice !== null ? (
        <p role="status" className="mt-1 text-[11px] text-success">
          {notice}
        </p>
      ) : null}
      {error !== null ? (
        <div className="mt-1">
          <InlineError error={error} />
        </div>
      ) : null}
    </div>
  )
}
