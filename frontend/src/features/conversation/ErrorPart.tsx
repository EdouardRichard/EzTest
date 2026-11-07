"use client"

import { AlertTriangle } from "lucide-react"

import { Button } from "@/components/ui/Button"

import type { ErrorPart as ErrorPartPayload } from "./types"

export type ErrorPartProps = {
  part: ErrorPartPayload
  /** Runs the action promised by the error (e.g. retrying the failed turn). */
  onAction?: (part: ErrorPartPayload) => void
}

function detailString(details: Record<string, unknown> | undefined, key: string): string {
  const value = details?.[key]
  return typeof value === "string" ? value.trim() : ""
}

function detailStrings(details: Record<string, unknown> | undefined, key: string): string[] {
  const value = details?.[key]
  if (!Array.isArray(value)) return []
  return value.filter((item): item is string => typeof item === "string" && item.trim() !== "")
}

/**
 * User-visible failure with the three-part message (code/message/action) plus
 * the v0.22 E-1 correction data (R-E1-3): the rejected field path, the legal
 * values and the hint. `retryable` errors default to a "重试" action even when
 * the backend omits the explicit action label. The legacy `details` bag is
 * still honored so persisted parts written before Task 33 render the same.
 */
export function ErrorPart({ part, onAction }: ErrorPartProps) {
  const actionLabel = part.action?.trim() || (part.retryable === true ? "重试" : null)
  const field = part.field?.trim() || detailString(part.details, "field")
  const allowed = part.allowed?.length ? part.allowed : detailStrings(part.details, "allowed")
  const hint = part.hint?.trim() || detailString(part.details, "hint")

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
        {field !== "" ? (
          <p className="mt-0.5 text-[10px] text-muted-foreground">
            字段：<code className="break-all font-mono text-foreground">{field}</code>
          </p>
        ) : null}
        {allowed.length > 0 ? (
          <p className="mt-0.5 text-[10px] text-muted-foreground">
            允许值：<code className="break-all font-mono text-foreground">{allowed.join(", ")}</code>
          </p>
        ) : null}
        {hint !== "" ? (
          <p className="mt-0.5 text-[10px] text-muted-foreground">{hint}</p>
        ) : null}
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
