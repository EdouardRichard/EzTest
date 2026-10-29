"use client"

import { PauseCircle } from "lucide-react"
import { useEffect, useState } from "react"

import type { PauseMode } from "../types"

export type PauseDialogProps = {
  open: boolean
  /** Mode selected when the dialog opens; defaults to a graceful pause. */
  mode?: PauseMode
  /** True while the pause request is in flight. */
  busy?: boolean
  /** Chinese action error from the owning container. */
  error?: string
  onConfirm: (mode: PauseMode) => void
  onClose?: () => void
}

const MODE_OPTIONS: readonly { value: PauseMode; label: string; description: string }[] = [
  {
    value: "graceful",
    label: "优雅暂停",
    description: "等待当前步骤完成后停止派发，运行中的用例回到 pending 等待恢复。",
  },
  {
    value: "immediate",
    label: "立即暂停",
    description: "立即取消 Agent run 与执行器动作，未完成步骤标记为中断。",
  },
]

const primaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
const secondaryButtonClass =
  "rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

/**
 * Pause confirmation dialog (spec §9.3). A graceful pause confirms directly;
 * choosing the immediate mode reveals the destructive warning and the same
 * submit button is the second confirmation. The dialog never calls the API
 * itself — the header owns the action, in-flight and error state.
 */
export function PauseDialog({
  open,
  mode = "graceful",
  busy = false,
  error = "",
  onConfirm,
  onClose,
}: PauseDialogProps) {
  const [selected, setSelected] = useState<PauseMode>(mode)

  useEffect(() => {
    if (open) setSelected(mode)
  }, [open, mode])

  if (!open) return null

  const immediate = selected === "immediate"

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/50 p-4 sm:items-center">
      <div
        data-testid="exec-pause-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="exec-pause-dialog-title"
        tabIndex={-1}
        onKeyDown={(event) => {
          if (event.key === "Escape") onClose?.()
        }}
        className="w-full max-w-lg space-y-3 rounded-2xl border border-border bg-card p-4 shadow-xl"
      >
        <div className="flex items-start gap-2">
          <span className="mt-0.5 grid h-8 w-8 shrink-0 place-items-center rounded-xl bg-amber-500/20 text-amber-700 dark:text-amber-200">
            <PauseCircle className="h-4 w-4" aria-hidden="true" />
          </span>
          <div className="min-w-0">
            <h2 id="exec-pause-dialog-title" className="text-base font-semibold">
              暂停执行
            </h2>
            <p className="mt-0.5 text-xs leading-relaxed text-muted-foreground">
              暂停后不再派发新用例；已暂停的批次可随时恢复。
            </p>
          </div>
        </div>

        {error === "" ? null : (
          <p
            role="alert"
            className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
          >
            {error}
          </p>
        )}

        <fieldset className="space-y-2">
          <legend className="sr-only">暂停方式</legend>
          {MODE_OPTIONS.map((option) => {
            const active = option.value === selected
            return (
              <button
                key={option.value}
                type="button"
                aria-pressed={active}
                data-testid={`exec-pause-mode-${option.value}`}
                onClick={() => setSelected(option.value)}
                className={
                  active
                    ? "w-full rounded-xl border border-primary/50 bg-primary/10 px-3 py-2 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    : "w-full rounded-xl border border-border bg-card px-3 py-2 text-left transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                }
              >
                <span className="text-sm font-medium">{option.label}</span>
                <span className="mt-0.5 block text-xs text-muted-foreground">
                  {option.description}
                </span>
              </button>
            )
          })}
        </fieldset>

        {immediate ? (
          <div className="space-y-1 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2">
            <p className="text-sm font-medium text-destructive">该用例将标记 interrupted</p>
            <p className="text-xs text-destructive/90">
              当前步骤会立即中断，恢复时先执行 cleanup，再从上一步继续。
            </p>
          </div>
        ) : null}

        <div className="flex justify-end gap-1.5">
          <button
            type="button"
            onClick={() => onClose?.()}
            disabled={busy}
            className={secondaryButtonClass}
          >
            取消
          </button>
          <button
            type="button"
            data-testid="exec-pause-confirm"
            onClick={() => onConfirm(selected)}
            disabled={busy}
            className={primaryButtonClass}
          >
            {busy ? "暂停中…" : immediate ? "确认立即暂停" : "确认暂停"}
          </button>
        </div>
      </div>
    </div>
  )
}
