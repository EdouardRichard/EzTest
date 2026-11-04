"use client"

import { AlertTriangle } from "lucide-react"

import { Button } from "@/components/ui/Button"

import type { ErrorPart as ErrorPartPayload } from "./types"

export type ErrorPartProps = {
  part: ErrorPartPayload
  /** Runs the action promised by the error (e.g. retrying the failed turn). */
  onAction?: (part: ErrorPartPayload) => void
}

/**
 * User-visible failure with the three-part message (code/message/action).
 * `retryable` errors default to a "重试" action even when the backend omits the
 * explicit action label.
 */
export function ErrorPart({ part, onAction }: ErrorPartProps) {
  const actionLabel = part.action?.trim() || (part.retryable === true ? "重试" : null)

  return (
    <div
      data-testid="error-part"
      role="alert"
      className="flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2"
    >
      <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-destructive" aria-hidden="true" />
      <div className="min-w-0 flex-1">
        <p className="text-[11px] font-medium text-destructive">{part.message}</p>
        <p className="mt-0.5 text-[10px] text-muted-foreground">错误码：{part.code}</p>
      </div>
      {actionLabel !== null && onAction !== undefined ? (
        <Button
          variant={part.retryable === true ? "primary" : "secondary"}
          size="sm"
          onClick={() => onAction(part)}
        >
          {actionLabel}
        </Button>
      ) : null}
    </div>
  )
}
