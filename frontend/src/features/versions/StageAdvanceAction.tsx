"use client"

import { useState } from "react"

import { Button } from "@/components/ui/Button"
import { InlineError } from "@/components/ui/ErrorState"
import type { StageView, VersionStage } from "@/features/design/types"

import { advanceStage, stageGateDetails, stageTransitionDetails } from "./api"
import { stageLabel } from "./stage"

export type StageAdvanceActionProps = {
  versionId: string
  /** Deterministic stage projection; null while loading/unavailable. */
  stage?: VersionStage | null
  /** Echo of the server projection after a successful advance. */
  onAdvanced?: (view: StageView) => void
  disabled?: boolean
}

/** Stages where a forward advance is not a valid write. */
const NON_ADVANCEABLE: ReadonlySet<VersionStage> = new Set<VersionStage>([
  "completed",
  "blocked",
  "paused",
])

/**
 * Version-header stage advance (W1-4A). It calls the dedicated
 * `POST /api/versions/{id}/stage/advance` route instead of guessing a target
 * stage; the server stage machine remains the single writer and typed 409 gate
 * details are rendered inline.
 */
export function StageAdvanceAction({
  versionId,
  stage = null,
  onAdvanced,
  disabled = false,
}: StageAdvanceActionProps) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)

  const stageKnown = stage !== null && stage !== undefined
  const disabledReason = !stageKnown
    ? "阶段投影加载中"
    : NON_ADVANCEABLE.has(stage)
      ? `当前阶段「${stageLabel(stage)}」不可推进`
      : undefined
  const writesDisabled = disabled || busy || disabledReason !== undefined

  const gate = stageGateDetails(error)
  const transition = stageTransitionDetails(error)

  async function advance(): Promise<void> {
    if (writesDisabled) return
    setBusy(true)
    setError(null)
    setNotice(null)
    try {
      const view = await advanceStage(versionId)
      setNotice(`已推进到「${stageLabel(view.stage)}」`)
      onAdvanced?.(view)
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  return (
    <span className="inline-flex flex-wrap items-center gap-2">
      <Button
        size="sm"
        variant="secondary"
        data-testid="stage-advance"
        title={disabledReason}
        disabled={writesDisabled}
        onClick={() => {
          void advance()
        }}
      >
        {busy ? "推进中…" : "推进阶段"}
      </Button>

      {notice !== null ? (
        <span data-testid="stage-advance-notice" role="status" className="text-[11px] text-success">
          {notice}
        </span>
      ) : null}
      {error !== null ? (
        <span className="inline-flex max-w-full flex-wrap items-center gap-2">
          <InlineError error={error} />
          {gate !== null ? (
            <span data-testid="stage-advance-gate" className="text-[11px] text-muted-foreground">
              门禁 {gate.gate}：{gate.reason}
              {gate.action !== "" ? ` · 修复：${gate.action}` : ""}
              {gate.next !== "" ? ` · 下一步：${gate.next}` : ""}
            </span>
          ) : transition !== null ? (
            <span className="text-[11px] text-muted-foreground">
              当前阶段 {transition.from} → {transition.to}
            </span>
          ) : null}
        </span>
      ) : null}
    </span>
  )
}
