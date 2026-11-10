"use client"

import { useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { InlineError } from "@/components/ui/ErrorState"
import type { StageView, VersionStage } from "@/features/design/types"
import { formatDateTime } from "@/lib/format"

import {
  parseStageBlocker,
  pauseVersion,
  resumeVersion,
  type StageBlocker,
  stageGateDetails,
  stageTransitionDetails,
} from "./api"
import { stageLabel } from "./stage"

/** The eight deterministic workflow stages of the v0.22 stage machine (B §7.1). */
const WORKFLOW_STAGES: readonly VersionStage[] = [
  "created",
  "clarifying",
  "requirement_review",
  "designing",
  "case_review",
  "executing",
  "reporting",
  "completed",
]

export type StageBarProps = {
  /** Version scope of the stage write transport. Without it the controls are disabled. */
  versionId?: string
  /** Effective stage from `GET /api/versions/{id}/stage`; blocked/paused are overlays. */
  stage: VersionStage
  /** `stage_entered_at` of the current (or suspension) stage. */
  stageEnteredAt?: string | null
  /** Previous stage from the last `version.stage.changed` event (optional context). */
  from?: VersionStage | null
  /** Actor from the last stage event (for example `host` or a user name). */
  actor?: string
  /** Structured blocker; rendered while the version is blocked. */
  blocker?: StageBlocker | null
  /** Pause reason (`version.blocker` also carries it while paused). */
  pausedReason?: string | null
  /** Echo of the server projection after a successful request/pause/resume. */
  onStageChanged?: (view: StageView) => void
  /** Passed stages may be inspected; no navigation is implied or performed. */
  onStageSelect?: (stage: VersionStage) => void
  /** Disables every write control (for example while the page projection is loading). */
  disabled?: boolean
  /**
   * Short design viewports hide the "版本阶段 / 进入于" header row only; the
   * stage pills, blocked/paused overlays and write actions stay available
   * (R2-D-01).
   */
  compact?: boolean
}

/**
 * Version stage bar (v0.22 R-B4-6/FE-6). It renders the deterministic workflow,
 * highlights the current stage, disables future stages, shows `stage_entered_at`,
 * exposes the blocked/paused overlay with its reason and sends typed
 * `stage/pause` and `stage/resume` writes. Stage advancement is driven by the
 * workflow itself (CTAs, gates and reconcilers), so the bar has no manual
 * advance control. A 409 gate response is
 * rendered inline with its structured `gate/reason/action/next` details; the bar
 * never infers a stage locally after a failed write.
 */
export function StageBar({
  versionId,
  stage,
  stageEnteredAt = null,
  from = null,
  actor,
  blocker = null,
  pausedReason = null,
  onStageChanged,
  onStageSelect,
  disabled = false,
  compact = false,
}: StageBarProps) {
  const [local, setLocal] = useState<{ signature: string; view: StageView } | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [actionError, setActionError] = useState<unknown>(null)

  // A local projection is only trusted while the props it was derived from are
  // unchanged; a newer server projection always wins (same pattern as the
  // structured question card).
  const signature = `${stage}|${stageEnteredAt ?? ""}|${blocker === null ? "" : JSON.stringify(blocker)}`
  const projected = local !== null && local.signature === signature ? local.view : null
  const activeStage = projected?.stage ?? stage
  const activeEnteredAt = projected?.stage_entered_at ?? stageEnteredAt
  const activeBlocker = projected !== null ? parseStageBlocker(projected.blocker) : blocker
  const activePausedReason =
    activeStage === "paused" ? (projected?.blocker ?? pausedReason ?? null) : null

  const blocked = activeStage === "blocked"
  const paused = activeStage === "paused"
  const canPause = !blocked && !paused && activeStage !== "completed"
  const currentIndex = WORKFLOW_STAGES.indexOf(activeStage)
  const writesDisabled = disabled || submitting || versionId === undefined || versionId === ""

  const gate = stageGateDetails(actionError)
  const transition = stageTransitionDetails(actionError)
  const hasTransitionContext =
    (from !== null && from !== undefined) || (actor !== undefined && actor !== "")

  async function run(action: () => Promise<StageView>): Promise<void> {
    if (writesDisabled) return
    setSubmitting(true)
    setActionError(null)
    try {
      const view = await action()
      if (view) {
        setLocal({ signature, view })
        onStageChanged?.(view)
      }
    } catch (cause) {
      // Keep the current stage: a rejected gate must not look advanced.
      setActionError(cause)
    } finally {
      setSubmitting(false)
    }
  }

  function pause(): void {
    if (versionId === undefined || versionId === "") return
    void run(() => pauseVersion(versionId, "用户暂停"))
  }

  function resume(): void {
    if (versionId === undefined || versionId === "") return
    void run(() => resumeVersion(versionId))
  }

  return (
    <section
      data-testid="stage-bar"
      data-compact={compact ? "true" : "false"}
      aria-label="版本阶段"
      className="space-y-2 rounded-xl border border-border bg-card px-3 py-2"
    >
      {compact ? null : (
        <header className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex flex-wrap items-center gap-1.5">
            <h3 className="text-sm font-semibold text-foreground">版本阶段</h3>
            <Badge tone={blocked ? "danger" : paused ? "warning" : "info"}>
              {stageLabel(activeStage)}
            </Badge>
          </div>
          <span data-testid="stage-entered-at" className="text-[11px] text-muted-foreground">
            进入于 {formatDateTime(activeEnteredAt)}
          </span>
        </header>
      )}

      <ol aria-label="阶段流转" className="flex flex-wrap items-center gap-1">
        {WORKFLOW_STAGES.map((item, index) => {
          const state =
            currentIndex < 0
              ? "unknown"
              : index === currentIndex
                ? "current"
                : index < currentIndex
                  ? "past"
                  : "future"
          const future = state === "future"
          const stateClasses =
            state === "current"
              ? "border-primary/40 bg-primary/10 font-semibold text-foreground"
              : state === "past"
                ? "border-border bg-muted/40 text-muted-foreground"
                : "border-dashed border-border bg-card text-muted-foreground opacity-60"
          const label = stageLabel(item)
          return (
            <li
              key={item}
              data-testid={`stage-item-${item}`}
              data-state={state}
              aria-current={state === "current" ? "step" : undefined}
              aria-disabled={future ? "true" : undefined}
              className={`inline-flex items-center rounded-full border px-2 py-0.5 text-[11px] ${stateClasses}`}
            >
              {state === "past" && onStageSelect !== undefined ? (
                <button
                  type="button"
                  className="rounded-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  aria-label={`查看阶段 ${item}`}
                  onClick={() => onStageSelect(item)}
                >
                  {label}
                </button>
              ) : (
                label
              )}
            </li>
          )
        })}
      </ol>

      {hasTransitionContext ? (
        <p data-testid="stage-transition" className="text-[11px] text-muted-foreground">
          {from !== null && from !== undefined ? (
            <span>
              上一阶段：{from}（{stageLabel(from)}）
            </span>
          ) : null}
          {actor !== undefined && actor !== "" ? (
            <span className="ml-2">操作者：{actor}</span>
          ) : null}
        </p>
      ) : null}

      {blocked ? (
        <div className="space-y-1">
          <p
            data-testid="stage-blocker-chip"
            className="rounded-md border border-destructive/30 bg-destructive/5 px-2 py-1 text-[11px] text-destructive"
          >
            已阻塞 · {activeBlocker?.kind ?? "manual"}
            {activeBlocker !== null && activeBlocker.reason !== ""
              ? `：${activeBlocker.reason}`
              : ""}
          </p>
          <p className="text-[11px] text-muted-foreground">请先解除阻塞后再继续。</p>
        </div>
      ) : null}

      {paused ? (
        <p
          data-testid="stage-paused-indicator"
          className="rounded-md border border-warning/40 bg-warning/10 px-2 py-1 text-[11px] text-warning"
        >
          已暂停
          {activePausedReason !== null && activePausedReason.trim() !== ""
            ? ` · 暂停原因：${activePausedReason.trim()}`
            : ""}
        </p>
      ) : null}

      <div className="flex flex-wrap items-center gap-2">
        {canPause ? (
          <Button
            size="sm"
            variant="secondary"
            disabled={writesDisabled}
            onClick={pause}
            aria-label="暂停版本"
          >
            暂停版本
          </Button>
        ) : null}
        {paused ? (
          <Button
            size="sm"
            variant="secondary"
            disabled={writesDisabled}
            onClick={resume}
            aria-label="恢复版本"
          >
            恢复版本
          </Button>
        ) : null}
      </div>

      {actionError !== null ? (
        <div data-testid="stage-action-error" className="space-y-1">
          {gate !== null ? (
            <div data-testid="stage-gate-error" className="space-y-1">
              <InlineError error={actionError} />
              <p className="text-[11px] text-muted-foreground">
                门禁 {gate.gate}：{gate.reason}
                {gate.action !== "" ? ` · 修复：${gate.action}` : ""}
                {gate.next !== "" ? ` · 下一步：${gate.next}` : ""}
              </p>
            </div>
          ) : transition !== null ? (
            <div data-testid="stage-transition-error" className="space-y-1">
              <InlineError error={actionError} />
              <p className="text-[11px] text-muted-foreground">
                当前阶段 {transition.from} → {transition.to}
                {transition.action !== "" ? ` · ${transition.action}` : ""}
              </p>
            </div>
          ) : (
            <InlineError error={actionError} />
          )}
        </div>
      ) : null}
    </section>
  )
}
