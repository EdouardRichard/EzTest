"use client"

import { useId, useState } from "react"

import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Textarea } from "@/components/ui/Textarea"
import type { StageView, VersionStage } from "@/features/design/types"

import { rejectStage, type StageRejectTarget } from "./api"

export type StageRejectActionProps = {
  versionId: string
  /** Deterministic stage projection; only the two hard gates can reject. */
  stage?: VersionStage | null
  /** Echo of the server projection after a successful reject. */
  onRejected?: (view: StageView) => void
  disabled?: boolean
}

type RejectGate = {
  target: StageRejectTarget
  label: string
}

/** Maps the two hard gates to their rejection target and button copy. */
function rejectGate(stage: VersionStage | null | undefined): RejectGate | null {
  if (stage === "requirement_review") return { target: "clarifying", label: "退回澄清" }
  if (stage === "case_review") return { target: "designing", label: "退回设计" }
  return null
}

/**
 * Hard-gate rejection action (API-13 / v0.22 B-4). Only the requirement and case
 * review gates can move one stage back; the reason is mandatory and audited, and
 * nothing is deleted — the gate then has to be confirmed again.
 */
export function StageRejectAction({
  versionId,
  stage = null,
  onRejected,
  disabled = false,
}: StageRejectActionProps) {
  const reasonId = useId()
  const gate = rejectGate(stage)
  const [open, setOpen] = useState(false)
  const [reason, setReason] = useState("")
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)

  if (gate === null) return null

  const trimmed = reason.trim()
  const submitDisabled = busy || disabled || trimmed.length === 0

  async function submit(): Promise<void> {
    if (gate === null || submitDisabled) return
    setBusy(true)
    setError(null)
    try {
      const view = await rejectStage(versionId, {
        reason: trimmed,
        target_stage: gate.target,
      })
      setOpen(false)
      setReason("")
      onRejected?.(view)
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <Button
        size="sm"
        variant="secondary"
        data-testid="stage-reject"
        disabled={disabled}
        onClick={() => {
          setReason("")
          setError(null)
          setOpen(true)
        }}
      >
        {gate.label}
      </Button>

      <Dialog
        open={open}
        onClose={() => {
          if (!busy) setOpen(false)
        }}
        title="退回上一阶段"
        description="退回只移动阶段并写入审计，不删除需求/用例；重新确认门禁后由既有生成流程按需重建。"
        footer={
          <>
            <Button variant="secondary" size="sm" disabled={busy} onClick={() => setOpen(false)}>
              取消
            </Button>
            <Button
              size="sm"
              disabled={submitDisabled}
              onClick={() => {
                void submit()
              }}
            >
              {busy ? "退回中…" : "确认退回"}
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          <Field
            id={reasonId}
            label="退回原因"
            required
            hint="必填（1–2000 字）；会写入 version.stage_decision 审计。"
          >
            <Textarea
              id={reasonId}
              value={reason}
              maxLength={2000}
              disabled={busy}
              placeholder="例如：验收标准缺失，需要补充澄清"
              onChange={(event) => setReason(event.target.value)}
            />
          </Field>
          <p className="text-[11px] text-muted-foreground">
            目标阶段：{gate.target === "clarifying" ? "澄清中" : "设计中"}（{gate.target}）
          </p>
          {error !== null ? <InlineError error={error} /> : null}
        </div>
      </Dialog>
    </>
  )
}
