"use client"

import { type KeyboardEvent, useEffect, useId, useRef, useState } from "react"

export type IssueDismissDialogProps = {
  busy: boolean
  error: string
  onCancel: () => void
  onSubmit: (reason: string) => void
}

/**
 * Dismissal dialog of the issue tab (spec §10.3): the reason is required, both
 * client-side (submit stays disabled) and server-side
 * (`issue_dismiss_reason_required`). Escape cancels.
 */
export function IssueDismissDialog({ busy, error, onCancel, onSubmit }: IssueDismissDialogProps) {
  const titleId = useId()
  const [reason, setReason] = useState("")
  const textareaRef = useRef<HTMLTextAreaElement | null>(null)

  useEffect(() => {
    textareaRef.current?.focus()
  }, [])

  const trimmed = reason.trim()

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>): void {
    if (event.key === "Escape") {
      event.stopPropagation()
      onCancel()
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4">
      <button
        type="button"
        tabIndex={-1}
        aria-label="关闭驳回弹窗遮罩"
        onClick={onCancel}
        className="absolute inset-0 bg-black/60"
      />
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        data-testid="issue-dismiss-dialog"
        onKeyDown={handleKeyDown}
        className="relative w-full max-w-md space-y-3 rounded-2xl border border-border bg-card p-4 shadow-2xl"
      >
        <h2 id={titleId} className="text-base font-semibold">
          驳回问题
        </h2>
        <p className="text-xs text-muted-foreground">
          驳回是终态操作，必须填写原因。原因会记录在问题详情中。
        </p>
        <label className="grid gap-1 text-xs text-muted-foreground">
          <span>驳回原因（必填）</span>
          <textarea
            ref={textareaRef}
            data-testid="issue-dismiss-reason"
            value={reason}
            rows={3}
            maxLength={500}
            onChange={(event) => setReason(event.target.value)}
            className="rounded-lg border border-border bg-background px-2.5 py-2 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          />
        </label>
        {error === "" ? null : (
          <p
            role="alert"
            data-testid="issue-dismiss-error"
            className="rounded-lg border border-destructive/30 bg-destructive/10 px-2 py-1.5 text-xs text-destructive"
          >
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <button
            type="button"
            onClick={onCancel}
            className="rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            取消
          </button>
          <button
            type="button"
            onClick={() => onSubmit(trimmed)}
            disabled={busy || trimmed === ""}
            className="rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            确认驳回
          </button>
        </div>
      </div>
    </div>
  )
}
