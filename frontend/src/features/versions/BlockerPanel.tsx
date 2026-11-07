"use client"

import { useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { Textarea } from "@/components/ui/Textarea"
import type { StageView, VersionStage } from "@/features/design/types"
import { formatDateTime } from "@/lib/format"

import {
  blockVersion,
  parseStageBlocker,
  type StageBlocker,
  stageGateDetails,
  unblockVersion,
} from "./api"

export type BlockerPanelProps = {
  /** Version scope of the block/unblock transport. */
  versionId?: string
  /**
   * Effective stage. The detail view is only rendered while the stage is
   * `blocked`; a `paused` version uses `stage.blocker` as its pause reason and
   * must not show the blocker panel (B §7.2 / Task 25 concern 7).
   */
  stage?: VersionStage
  /** Structured blocker (`parseStageBlocker(stage.blocker)`). */
  blocker?: StageBlocker | null
  /** `stage_entered_at` while blocked (the "blocked since" timestamp). */
  blockedSince?: string | null
  /** Echo of the server projection after a successful block. */
  onBlocked?: (view: StageView) => void
  /** Echo of the restored projection after a successful unblock. */
  onUnblocked?: (view: StageView) => void
  /** Disables every write control (for example while the page is loading). */
  disabled?: boolean
}

type BlockerFieldErrors = {
  reason?: string
  action?: string
  next?: string
}

function validateBlocker(reason: string, action: string, next: string): BlockerFieldErrors {
  const errors: BlockerFieldErrors = {}
  if (reason.trim() === "") errors.reason = "请填写阻塞原因"
  if (action.trim() === "") errors.action = "请填写修复动作"
  if (next.trim() === "") errors.next = "请填写下一步"
  return errors
}

/**
 * Blocked/paused entry and detail (v0.22 R-B4-4/R-B4-6). While blocked it
 * renders the structured kind/reason/action/next, the blocked-since timestamp
 * and the validated unblock action; otherwise it offers the "record blocker"
 * form, whose reason/action/next are required before anything is sent. The
 * server remains the validation authority (`Blocker.Normalize`).
 */
export function BlockerPanel({
  versionId,
  stage,
  blocker = null,
  blockedSince = null,
  onBlocked,
  onUnblocked,
  disabled = false,
}: BlockerPanelProps) {
  const [local, setLocal] = useState<{ signature: string; view: StageView } | null>(null)
  const [editing, setEditing] = useState(false)
  const [kind, setKind] = useState("")
  const [reason, setReason] = useState("")
  const [action, setAction] = useState("")
  const [next, setNext] = useState("")
  const [fieldErrors, setFieldErrors] = useState<BlockerFieldErrors>({})
  const [saving, setSaving] = useState(false)
  const [actionError, setActionError] = useState<unknown>(null)

  const signature = `${stage ?? ""}|${blocker === null ? "" : JSON.stringify(blocker)}|${blockedSince ?? ""}`
  const projected = local !== null && local.signature === signature ? local.view : null
  const effectiveStage = projected?.stage ?? stage
  const effectiveBlocker = projected !== null ? parseStageBlocker(projected.blocker) : blocker
  const effectiveSince = projected?.stage_entered_at ?? blockedSince
  const blocked = effectiveStage === "blocked" || (effectiveStage === undefined && blocker !== null)
  const paused = effectiveStage === "paused"
  const writesDisabled = disabled || saving || versionId === undefined || versionId === ""

  const gate = stageGateDetails(actionError)
  const activeKind =
    effectiveBlocker !== null && effectiveBlocker.kind !== "" ? effectiveBlocker.kind : "manual"

  async function submitBlock(): Promise<void> {
    if (writesDisabled || versionId === undefined || versionId === "") return
    const errors = validateBlocker(reason, action, next)
    setFieldErrors(errors)
    if (Object.keys(errors).length > 0) return

    setSaving(true)
    setActionError(null)
    try {
      const view = await blockVersion(versionId, {
        kind: kind.trim() === "" ? "manual" : kind.trim(),
        reason: reason.trim(),
        action: action.trim(),
        next: next.trim(),
      })
      if (view) setLocal({ signature, view })
      setEditing(false)
      setKind("")
      setReason("")
      setAction("")
      setNext("")
      onBlocked?.(view)
    } catch (cause) {
      setActionError(cause)
    } finally {
      setSaving(false)
    }
  }

  async function submitUnblock(): Promise<void> {
    if (writesDisabled || versionId === undefined || versionId === "") return
    setSaving(true)
    setActionError(null)
    try {
      const view = await unblockVersion(versionId)
      if (view) setLocal({ signature, view })
      onUnblocked?.(view)
    } catch (cause) {
      setActionError(cause)
    } finally {
      setSaving(false)
    }
  }

  const errorNotice =
    actionError === null ? null : (
      <div data-testid="blocker-action-error" className="space-y-1">
        {gate !== null ? (
          <div data-testid="blocker-gate-error" className="space-y-1">
            <InlineError error={actionError} />
            <p className="text-[11px] text-muted-foreground">
              门禁 {gate.gate}：{gate.reason}
              {gate.action !== "" ? ` · 修复：${gate.action}` : ""}
              {gate.next !== "" ? ` · 下一步：${gate.next}` : ""}
            </p>
          </div>
        ) : (
          <InlineError error={actionError} />
        )}
      </div>
    )

  // A paused version is a distinct suspension (B §7.2): never show the blocker
  // detail or form for it.
  if (paused) return null

  if (blocked) {
    return (
      <section
        data-testid="blocker-panel"
        aria-label="版本阻塞"
        className="space-y-2 rounded-xl border border-destructive/30 bg-destructive/5 px-3 py-2"
      >
        <header className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex flex-wrap items-center gap-1.5">
            <h3 className="text-sm font-semibold text-destructive">版本阻塞</h3>
            <span data-testid="blocker-kind">
              <Badge tone="danger">{activeKind}</Badge>
            </span>
            {effectiveSince !== null && effectiveSince !== undefined && effectiveSince !== "" ? (
              <span data-testid="blocker-since" className="text-[11px] text-muted-foreground">
                阻塞于 {formatDateTime(effectiveSince)}
              </span>
            ) : null}
          </div>
          <Button
            size="sm"
            variant="secondary"
            disabled={writesDisabled}
            onClick={() => {
              void submitUnblock()
            }}
          >
            {saving ? "处理中…" : "解除阻塞"}
          </Button>
        </header>

        {effectiveBlocker === null ? (
          <p className="text-[11px] text-muted-foreground">
            该版本处于阻塞状态，但没有记录结构化阻塞原因。
          </p>
        ) : (
          <dl className="grid gap-2 text-[11px] sm:grid-cols-3">
            <div>
              <dt className="font-medium text-muted-foreground">原因</dt>
              <dd className="text-foreground">
                {effectiveBlocker.reason === "" ? "未记录" : effectiveBlocker.reason}
              </dd>
            </div>
            <div>
              <dt className="font-medium text-muted-foreground">修复动作</dt>
              <dd className="text-foreground">
                {effectiveBlocker.action === "" ? "未记录" : effectiveBlocker.action}
              </dd>
            </div>
            <div>
              <dt className="font-medium text-muted-foreground">下一步</dt>
              <dd className="text-foreground">
                {effectiveBlocker.next === "" ? "未记录" : effectiveBlocker.next}
              </dd>
            </div>
          </dl>
        )}

        {errorNotice}
      </section>
    )
  }

  return (
    <section
      data-testid="blocker-form-section"
      aria-label="版本阻塞"
      className="space-y-2 rounded-xl border border-border bg-card px-3 py-2"
    >
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h3 className="text-sm font-semibold text-foreground">版本阻塞</h3>
          <p className="text-[11px] text-muted-foreground">
            当前无阻塞。遇到执行器离线、账号失效等问题时可记录结构化阻塞，恢复前会校验原因是否已清除。
          </p>
        </div>
        {!editing ? (
          <Button
            size="sm"
            variant="secondary"
            disabled={writesDisabled}
            onClick={() => {
              setEditing(true)
              setFieldErrors({})
              setActionError(null)
            }}
          >
            记录阻塞
          </Button>
        ) : null}
      </header>

      {editing ? (
        <form
          aria-label="记录阻塞"
          className="grid gap-2 sm:grid-cols-2"
          onSubmit={(event) => {
            event.preventDefault()
            void submitBlock()
          }}
        >
          <Field
            id="blocker-kind"
            label="阻塞类型"
            hint="可选：executor/account/database 等；留空按 manual 记录。"
          >
            <Input
              id="blocker-kind"
              value={kind}
              disabled={writesDisabled}
              placeholder="executor"
              onChange={(event) => setKind(event.target.value)}
            />
          </Field>
          <Field id="blocker-reason" label="阻塞原因" required error={fieldErrors.reason}>
            <Textarea
              id="blocker-reason"
              value={reason}
              disabled={writesDisabled}
              maxLength={2000}
              aria-invalid={fieldErrors.reason !== undefined}
              placeholder="例如：执行器离线"
              onChange={(event) => {
                setReason(event.target.value)
                setFieldErrors((previous) => ({ ...previous, reason: undefined }))
              }}
            />
          </Field>
          <Field id="blocker-action" label="修复动作" required error={fieldErrors.action}>
            <Textarea
              id="blocker-action"
              value={action}
              disabled={writesDisabled}
              maxLength={2000}
              aria-invalid={fieldErrors.action !== undefined}
              placeholder="例如：启动执行器并检查心跳"
              onChange={(event) => {
                setAction(event.target.value)
                setFieldErrors((previous) => ({ ...previous, action: undefined }))
              }}
            />
          </Field>
          <Field id="blocker-next" label="下一步" required error={fieldErrors.next}>
            <Textarea
              id="blocker-next"
              value={next}
              disabled={writesDisabled}
              maxLength={2000}
              aria-invalid={fieldErrors.next !== undefined}
              placeholder="例如：重试当前 wave"
              onChange={(event) => {
                setNext(event.target.value)
                setFieldErrors((previous) => ({ ...previous, next: undefined }))
              }}
            />
          </Field>
          <div className="flex items-center gap-2 sm:col-span-2">
            <Button
              size="sm"
              disabled={writesDisabled}
              onClick={() => {
                void submitBlock()
              }}
            >
              {saving ? "提交中…" : "确认阻塞"}
            </Button>
            <Button
              size="sm"
              variant="secondary"
              disabled={saving}
              onClick={() => {
                setEditing(false)
                setFieldErrors({})
                setActionError(null)
              }}
            >
              取消
            </Button>
          </div>
        </form>
      ) : null}

      {errorNotice}
    </section>
  )
}
