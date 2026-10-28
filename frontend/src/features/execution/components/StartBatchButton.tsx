"use client"

import { Loader2, Play } from "lucide-react"

import type { VersionStatus } from "@/features/versions/types"

/** Statuses in which a new batch may be created (spec §5.1 gate is the server's). */
const EXECUTABLE_STATUSES: readonly VersionStatus[] = ["executing", "continuing"]

export type StartBatchButtonProps = {
  versionStatus: VersionStatus
  /** An active planned/running batch already occupies the single-batch slot. */
  hasActiveBatch: boolean
  busy?: boolean
  disabled?: boolean
  onStart?: () => void
}

/**
 * 「开始执行」 header action (spec §9.1): only an executing/continuing version
 * without an active batch can start one. Gating lives here so the header and
 * the empty tabs share the same rule; the server still re-checks every gate.
 */
export function StartBatchButton({
  versionStatus,
  hasActiveBatch,
  busy = false,
  disabled = false,
  onStart,
}: StartBatchButtonProps) {
  if (!EXECUTABLE_STATUSES.includes(versionStatus) || hasActiveBatch) return null

  return (
    <button
      type="button"
      data-testid="exec-start-btn"
      onClick={() => onStart?.()}
      disabled={busy || disabled}
      className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      {busy ? (
        <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
      ) : (
        <Play className="h-4 w-4" aria-hidden="true" />
      )}
      {busy ? "创建批次中…" : "开始执行"}
    </button>
  )
}
