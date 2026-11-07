"use client"

import { useId, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { InlineError } from "@/components/ui/ErrorState"

import type { CaseRevisionRollbackView, CaseVersionStatus, DiscoveryRevisionData } from "./types"

export type DiscoveryRevisionNoticeProps = {
  revision: DiscoveryRevisionData
  /** True for non-admin viewers: the notice is readable but not actionable. */
  readOnly?: boolean
  /** Runs the api-client rollback request; supplied by the feature integration. */
  onRollback?: (
    caseVersionId: string,
    reason?: string,
  ) => Promise<CaseRevisionRollbackView | undefined> | CaseRevisionRollbackView | undefined
  /** Called after a successful rollback so the caller refetches its snapshots. */
  onRolledBack?: (result: CaseRevisionRollbackView | undefined) => void
}

const LOW_CONFIDENCE_LABEL: Record<string, string> = {
  insufficient_evidence: "支撑证据少于 2 条",
  missing_requirement: "缺少需求关联",
  requirement_not_current: "需求已不是当前版本",
  requirement_not_clarified: "需求尚未澄清",
  resource_not_approved: "资源未批准",
  target_out_of_scope: "目标不在版本范围",
  duplicate_target: "目标已被相同内容的已确认用例覆盖",
  incomplete_content: "步骤或断言不完整",
  low_confidence: "探索证据不足或需求关联缺失",
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

/** Returns a positive decimal id as a string; `0`/empty/non-ids are `null`. */
function decimalID(value: unknown): string | null {
  if (typeof value === "string") {
    const trimmed = value.trim()
    return /^[1-9]\d*$/.test(trimmed) ? trimmed : null
  }
  if (typeof value === "number" && Number.isSafeInteger(value) && value > 0) {
    return String(value)
  }
  return null
}

/**
 * Maps the EV-12 `case.revision.auto_confirmed` payload to the notice model.
 * Both the discovery auto-confirmation and the rollback replacement travel on
 * this event; `trigger`/`rollback_of` distinguish them (spec R-EX-6/EV-12).
 */
export function discoveryRevisionFromEventPayload(payload: unknown): DiscoveryRevisionData | null {
  if (!isRecord(payload)) return null
  const caseVersionId = decimalID(payload.case_version_id)
  if (caseVersionId === null) return null
  return {
    caseId: decimalID(payload.case_id) ?? undefined,
    caseVersionId,
    status: "confirmed",
    trigger: typeof payload.trigger === "string" ? payload.trigger : undefined,
    autoConfirmedBy:
      typeof payload.auto_confirmed_by === "string"
        ? payload.auto_confirmed_by
        : typeof payload.actor === "string"
          ? payload.actor
          : null,
    rollbackOf: decimalID(payload.rollback_of),
    sourceObservationId: decimalID(payload.source_observation_id),
    resourceId: decimalID(payload.resource_id),
    confidence: typeof payload.confidence === "string" ? payload.confidence : null,
  }
}

/**
 * Detects a historical discovery-origin case revision from its sources. The
 * design case-detail GET does not expose `case_version.trigger` yet, so a
 * reloaded page can only rely on the additive draft's provenance paths
 * (`discovery_delta` / `discovery_observation`). `inferred` keeps the notice
 * from claiming an auto-confirmation it cannot prove.
 */
export function discoveryRevisionFromCaseVersion(version: {
  case_version_id: string
  revision: number
  status: CaseVersionStatus
  sources: Array<{ path: string }>
}): DiscoveryRevisionData | null {
  const discovered = version.sources.some(
    (source) => source.path === "discovery_delta" || source.path === "discovery_observation",
  )
  if (!discovered) return null
  return {
    caseVersionId: version.case_version_id,
    revision: version.revision,
    status: version.status,
    inferred: true,
    lowConfidenceReason: version.status === "draft" ? "low_confidence" : null,
  }
}

/**
 * W2-12 discovery revision notice (FE-19): shows the system auto-confirmed
 * revision, the low-confidence draft that still needs a human confirmation, or
 * the replacement revision produced by a rollback. A rollback is an
 * audit/replacement operation: the case is kept and the UI must never present
 * it as a deletion (W2-C11). The actual POST lives in the caller's
 * `onRollback` handler so every entry point shares the api-client wrapper.
 */
export function DiscoveryRevisionNotice({
  revision,
  readOnly = false,
  onRollback,
  onRolledBack,
}: DiscoveryRevisionNoticeProps) {
  const reasonInputID = useId()
  const [reason, setReason] = useState("")
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [result, setResult] = useState<CaseRevisionRollbackView | null>(null)
  const [submitted, setSubmitted] = useState(false)

  const isRollback =
    revision.trigger === "rollback" ||
    (typeof revision.rollbackOf === "string" && revision.rollbackOf.length > 0)
  const isDiscovery =
    isRollback ||
    revision.trigger === "discovery_delta" ||
    revision.inferred === true ||
    (revision.lowConfidenceReason !== null && revision.lowConfidenceReason !== undefined)
  const isLowConfidence = !isRollback && revision.status === "draft" && isDiscovery
  const confirmedBySystem =
    revision.autoConfirmedBy === undefined ||
    revision.autoConfirmedBy === null ||
    revision.autoConfirmedBy === "system"
  const isAutoConfirmed =
    !isRollback &&
    revision.status === "confirmed" &&
    revision.trigger === "discovery_delta" &&
    revision.inferred !== true &&
    confirmedBySystem
  const canRollback = isAutoConfirmed && !readOnly && onRollback !== undefined

  const lowConfidenceLabel =
    revision.lowConfidenceReason !== null && revision.lowConfidenceReason !== undefined
      ? (LOW_CONFIDENCE_LABEL[revision.lowConfidenceReason] ?? "低置信，需要人工确认")
      : null

  const replacementID = result?.case_version_id ?? revision.caseVersionId
  const originalID = result?.rolled_back_from ?? revision.rollbackOf ?? null
  const rollbackSummary =
    result !== null || isRollback
      ? `已回滚到替代 revision ${replacementID}${
          originalID !== null ? `（原 revision ${originalID}）` : ""
        }`
      : null

  async function runRollback(): Promise<void> {
    if (busy || onRollback === undefined) return
    setBusy(true)
    setError(null)
    const trimmed = reason.trim()
    try {
      const next =
        trimmed.length > 0
          ? await onRollback(revision.caseVersionId, trimmed)
          : await onRollback(revision.caseVersionId)
      const normalized = next ?? null
      if (normalized !== null) setResult(normalized)
      setSubmitted(true)
      onRolledBack?.(normalized ?? undefined)
    } catch (cause: unknown) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  return (
    <section
      data-testid="discovery-revision-notice"
      aria-label="探索修订"
      className="space-y-1.5 rounded-lg border border-border bg-muted/20 px-3 py-2"
    >
      <header className="flex flex-wrap items-center gap-1.5">
        <h3 className="text-xs font-semibold text-foreground">探索修订</h3>
        {isRollback ? <Badge tone="info">已回滚</Badge> : null}
        {isAutoConfirmed ? <Badge tone="success">自动确认</Badge> : null}
        {isLowConfidence ? <Badge tone="warning">低置信草案</Badge> : null}
        {!isRollback && !isAutoConfirmed && !isLowConfidence ? (
          <Badge tone="neutral">探索修订</Badge>
        ) : null}
        <span className="ml-auto text-[10px] text-muted-foreground">
          {revision.caseId !== undefined ? `用例 ${revision.caseId} · ` : ""}
          revision {revision.caseVersionId}
        </span>
      </header>

      {isRollback ? (
        <p className="text-[11px] text-foreground">
          回滚完成。回滚为审计/替代语义，用例保留在版本历史中。
        </p>
      ) : null}

      {isAutoConfirmed ? (
        <p className="text-[11px] text-foreground">
          系统按探索判据直接确认该修订，无需人工操作；当前 wave 不变，下一 wave 才会执行新用例。
        </p>
      ) : null}

      {isLowConfidence ? (
        <p className="text-[11px] text-foreground">
          草案需人工确认后才能进入执行。
          {lowConfidenceLabel !== null ? `原因：${lowConfidenceLabel}。` : ""}
        </p>
      ) : null}

      {!isRollback && !isAutoConfirmed && !isLowConfidence ? (
        <p className="text-[11px] text-foreground">
          该用例由执行期探索追加生成，已确认；确认来自人工操作或历史投影，确认来源以实时事件为准。
        </p>
      ) : null}

      {revision.trigger === "discovery_delta" ||
      revision.sourceObservationId !== null ||
      revision.confidence !== null ? (
        <p className="text-[10px] text-muted-foreground">
          {revision.trigger === "discovery_delta" ? "trigger=discovery_delta" : null}
          {revision.sourceObservationId !== null && revision.sourceObservationId !== undefined
            ? ` · 来源观测 ${revision.sourceObservationId}`
            : ""}
          {revision.confidence !== null && revision.confidence !== undefined
            ? ` · 置信 ${revision.confidence}`
            : ""}
        </p>
      ) : null}

      {rollbackSummary !== null ? (
        <p
          data-testid="discovery-rollback-result"
          role="status"
          className="text-[11px] text-success"
        >
          {rollbackSummary}
        </p>
      ) : null}

      {submitted && result === null ? (
        <p role="status" className="text-[11px] text-muted-foreground">
          已提交回滚，等待最新 revision。
        </p>
      ) : null}

      {error !== null ? <InlineError error={error} /> : null}

      {canRollback ? (
        <div className="flex flex-wrap items-end gap-1.5">
          <div className="min-w-40 flex-1">
            <label htmlFor={reasonInputID} className="block text-[10px] text-muted-foreground">
              回滚原因（可选）
            </label>
            <input
              id={reasonInputID}
              value={reason}
              maxLength={2000}
              onChange={(change) => setReason(change.target.value)}
              className="mt-0.5 w-full rounded-md border border-border bg-background px-2 py-1 text-[11px] text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              placeholder="写入审计的说明"
            />
          </div>
          <Button
            size="sm"
            variant="danger"
            disabled={busy}
            onClick={() => {
              void runRollback()
            }}
          >
            {busy ? "回滚中…" : result !== null ? "再次回滚（同一替代 revision）" : "回滚"}
          </Button>
          <span className="w-full text-[10px] text-muted-foreground">
            回滚会 supersede 原修订并生成替代 revision，用例仍保留。
          </span>
        </div>
      ) : null}

      {isAutoConfirmed && readOnly ? (
        <p className="text-[10px] text-muted-foreground">只读：回滚操作仅管理员可用。</p>
      ) : null}
    </section>
  )
}
