"use client"

import { ArrowRight, CircleDot, PauseCircle } from "lucide-react"

import type { BatchCounts } from "@/features/execution/types"

import type { ContinuePhase, Version } from "./types"

/** Subset of the active batch the banner renders; the execution Batch fits it. */
export type ContinuingBatchView = {
  batch_no: number
  paused?: boolean
  counts: BatchCounts
}

/** Human labels for the three continuing phases (spec §10.1). */
const PHASE_LABELS: Record<Exclude<ContinuePhase, "">, string> = {
  clarifying: "增量澄清",
  designing: "增量设计",
  executing: "增量执行",
}

/** Terminal counts of a batch: cases that no longer need execution work. */
function completedCaseCount(counts: BatchCounts): number {
  return (
    counts.passed +
    counts.failed +
    counts.blocked +
    counts.inconclusive +
    counts.skipped +
    counts.interrupted
  )
}

export type ContinuingBannerProps = {
  version: Version
  /** The active batch of a continuing version, when one is running. */
  activeBatch?: ContinuingBatchView | null
  onClarify?: () => void
  onOpenPlan?: () => void
  onOpenSessions?: () => void
}

/**
 * Continuing-state banner (spec §10.1): the batch badge plus the incremental
 * phase, stacked with 已暂停 when the version/batch is paused. The action is
 * phase-specific: clarify, open the incremental plan, or follow the running
 * batch. It renders nothing for a version that is not continuing.
 */
export function ContinuingBanner({
  version,
  activeBatch = null,
  onClarify,
  onOpenPlan,
  onOpenSessions,
}: ContinuingBannerProps) {
  if (version.status !== "continuing") return null

  const batchNo = activeBatch?.batch_no ?? (version.batch_no ?? 0) + 1
  const paused = version.paused === true || activeBatch?.paused === true
  const phase = version.continuePhase ?? ""
  const phaseLabel = paused ? "已暂停" : phase === "" ? "进行中" : PHASE_LABELS[phase]
  const completed = activeBatch === null ? 0 : completedCaseCount(activeBatch.counts)
  const total = activeBatch?.counts.total ?? 0

  return (
    <section
      data-testid="continuing-banner"
      role="status"
      className="flex flex-wrap items-center gap-2 rounded-xl border border-sky-500/40 bg-sky-500/10 px-3 py-2 text-sm text-sky-800 dark:text-sky-200"
    >
      {paused ? (
        <PauseCircle className="h-4 w-4 shrink-0" aria-hidden="true" />
      ) : (
        <CircleDot className="h-4 w-4 shrink-0" aria-hidden="true" />
      )}
      <span className="font-semibold">{`继续中（第 ${batchNo} 批）`}</span>
      <span className="text-muted-foreground" aria-hidden="true">
        ·
      </span>
      <span data-testid="continue-phase-label">{phaseLabel}</span>

      {phase === "executing" && activeBatch !== null ? (
        <span className="text-xs">{`已执行 ${completed}/${total} 个用例`}</span>
      ) : null}

      {paused ? (
        <span className="text-xs">暂停期间仍可继续录入需求，恢复后执行增量批次。</span>
      ) : phase === "clarifying" ? (
        <button
          type="button"
          onClick={() => onClarify?.()}
          className="ml-auto inline-flex items-center gap-1 rounded-lg border border-sky-500/40 bg-card px-2.5 py-1 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          去澄清
          <ArrowRight className="h-3 w-3" aria-hidden="true" />
        </button>
      ) : phase === "designing" ? (
        <button
          type="button"
          onClick={() => onOpenPlan?.()}
          className="ml-auto inline-flex items-center gap-1 rounded-lg border border-sky-500/40 bg-card px-2.5 py-1 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          查看增量计划
          <ArrowRight className="h-3 w-3" aria-hidden="true" />
        </button>
      ) : phase === "executing" ? (
        <button
          type="button"
          onClick={() => onOpenSessions?.()}
          className="ml-auto inline-flex items-center gap-1 rounded-lg border border-sky-500/40 bg-card px-2.5 py-1 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          查看执行
          <ArrowRight className="h-3 w-3" aria-hidden="true" />
        </button>
      ) : null}
    </section>
  )
}
