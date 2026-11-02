"use client"

import { useEffect, useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"

import type { PauseMode } from "./types"

export type PauseTarget = {
  kind: "wave" | "case_run"
  id: string
  title: string
  /** The server treats a repeated pause as a no-op; explain that up front. */
  alreadyPaused?: boolean
}

export type PauseInput = {
  mode: PauseMode
  reason?: string
}

export type PauseDialogProps = {
  open: boolean
  target: PauseTarget | null
  onClose: () => void
  /** Resolves when the pause request succeeded; throws ApiError otherwise. */
  onSubmit: (input: PauseInput) => Promise<void>
}

const MODES: { value: PauseMode; label: string; hint: string }[] = [
  {
    value: "graceful",
    label: "优雅暂停（默认）",
    hint: "在飞用例继续跑完，不再派发新的用例；恢复后继续派发。",
  },
  {
    value: "immediate",
    label: "立即暂停",
    hint: "取消正在执行的步骤，已完成的步骤和断言结果保留。",
  },
]

/**
 * Wave/case-run pause dialog (spec §5.7, §12.2). Graceful is the default and
 * needs one click; immediate is destructive for in-flight steps and therefore
 * asks for a second explicit acknowledgement. A repeated pause is idempotent
 * on the server, so an already-paused target explains that instead of failing.
 */
export function PauseDialog({ open, target, onClose, onSubmit }: PauseDialogProps) {
  const [mode, setMode] = useState<PauseMode>("graceful")
  const [reason, setReason] = useState("")
  const [acknowledged, setAcknowledged] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<unknown>(null)

  // biome-ignore lint/correctness/useExhaustiveDependencies: a new target resets the dialog form
  useEffect(() => {
    if (!open) return
    // Case-level pause is immediate-only (spec §5.7); waves default to graceful.
    setMode(target?.kind === "case_run" ? "immediate" : "graceful")
    setReason("")
    setAcknowledged(false)
    setSubmitting(false)
    setError(null)
  }, [open, target?.kind, target?.id])

  if (target === null) return null

  const needsAcknowledgement = mode === "immediate" && !acknowledged
  const alreadyPaused = target.alreadyPaused === true
  const targetLabel = target.kind === "wave" ? "波次" : "用例"
  const modes =
    target.kind === "case_run" ? MODES.filter((item) => item.value === "immediate") : MODES

  async function submit(): Promise<void> {
    if (submitting || alreadyPaused || needsAcknowledgement) return
    setSubmitting(true)
    setError(null)
    try {
      await onSubmit({ mode, reason: reason.trim() })
      onClose()
    } catch (cause) {
      setError(cause)
      setSubmitting(false)
    }
  }

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="暂停执行"
      description={`即将暂停${targetLabel}「${target.title}」。暂停不是版本状态，版本头仍显示运行中。`}
      footer={
        <>
          <Button variant="secondary" size="sm" disabled={submitting} onClick={onClose}>
            取消
          </Button>
          <Button
            size="sm"
            disabled={submitting || alreadyPaused || needsAcknowledgement}
            onClick={() => {
              void submit()
            }}
          >
            {submitting ? "暂停中…" : "暂停"}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        {alreadyPaused ? (
          <p
            role="status"
            className="rounded-lg border border-warning/30 bg-warning/10 px-3 py-2 text-[11px] text-warning"
          >
            该{targetLabel}已处于暂停状态，重复暂停不会产生新的暂停记录。
          </p>
        ) : null}

        <fieldset className="space-y-2">
          <legend className="text-xs font-medium text-foreground">暂停方式</legend>
          {modes.map((option) => (
            <label
              key={option.value}
              className="flex cursor-pointer items-start gap-2 rounded-lg border border-border bg-card px-3 py-2"
            >
              <input
                type="radio"
                name="pause-mode"
                value={option.value}
                checked={mode === option.value}
                disabled={submitting || alreadyPaused}
                onChange={() => setMode(option.value)}
                className="mt-0.5 size-3.5 accent-primary"
              />
              <span className="space-y-0.5">
                <span className="block text-xs font-medium text-foreground">{option.label}</span>
                <span className="block text-[11px] text-muted-foreground">{option.hint}</span>
              </span>
            </label>
          ))}
        </fieldset>

        {mode === "immediate" ? (
          <Alert tone="warning" title="立即暂停会影响在飞用例">
            立即暂停会中断正在执行的步骤；已完成的步骤与断言结果会保留，恢复后从首个未通过步骤续跑。
            <label className="mt-2 flex items-center gap-2 text-[11px]">
              <input
                type="checkbox"
                checked={acknowledged}
                disabled={submitting || alreadyPaused}
                onChange={(event) => setAcknowledged(event.target.checked)}
                className="size-3.5 accent-destructive"
              />
              我了解立即暂停会中断在飞步骤
            </label>
          </Alert>
        ) : null}

        <Field id="pause-reason" label="暂停原因（可选）">
          <Input
            id="pause-reason"
            value={reason}
            maxLength={200}
            disabled={submitting || alreadyPaused}
            placeholder="例如：等待手工验证码"
            onChange={(event) => setReason(event.target.value)}
          />
        </Field>

        {error !== null ? <InlineError error={error} /> : null}
      </div>
    </Dialog>
  )
}
