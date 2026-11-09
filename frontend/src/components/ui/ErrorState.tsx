import type { ReactNode } from "react"

import { ApiError } from "@/lib/api-client"
import { localizeApiError } from "@/lib/api-error-messages"

import { Button } from "./Button"
import { cn } from "./cn"

export type ErrorStateProps = {
  error: unknown
  title?: string
  onRetry?: () => void
  className?: string
}

type ErrorDescription = {
  message: string
  requestId?: string
  code?: string
  retryable?: boolean
  /** Localized or typed API `details.next_action` guidance. */
  nextAction?: string
}

function describeError(error: unknown): ErrorDescription {
  const localized = localizeApiError(error)
  if (error instanceof ApiError) {
    return {
      message: localized.message,
      requestId: error.requestId,
      code: error.code,
      retryable: error.retryable,
      nextAction: localized.nextAction,
    }
  }
  return { message: localized.message, nextAction: localized.nextAction }
}

export function ErrorState({ error, title = "加载失败", onRetry, className }: ErrorStateProps) {
  const description = describeError(error)

  return (
    <div
      className={cn(
        "flex flex-col items-start gap-2 rounded-xl border border-destructive/30",
        "bg-destructive/5 px-5 py-4",
        className,
      )}
    >
      <p className="text-sm font-semibold text-destructive">{title}</p>
      <p className="text-xs text-foreground">{description.message}</p>
      {description.nextAction ? (
        <p className="text-[11px] text-muted-foreground">下一步：{description.nextAction}</p>
      ) : null}
      <p className="text-[11px] text-muted-foreground">
        {description.code ? <span className="mr-2">{description.code}</span> : null}
        {description.requestId ? <span>请求 ID：{description.requestId}</span> : null}
      </p>
      {onRetry ? (
        <Button variant="secondary" size="sm" onClick={onRetry}>
          重试
        </Button>
      ) : null}
    </div>
  )
}

export type InlineErrorProps = {
  error: unknown
  /** Optional scenario-specific main copy that overrides the localized message. */
  message?: string
  children?: ReactNode
}

/** Compact one-line error for toolbars and forms. */
export function InlineError({ error, message, children }: InlineErrorProps) {
  const description = describeError(error)
  return (
    <p role="alert" className="text-xs text-destructive">
      {message ?? description.message}
      {description.nextAction ? (
        <span className="ml-2 text-muted-foreground">下一步：{description.nextAction}</span>
      ) : null}
      {description.requestId ? (
        <span className="ml-2 text-muted-foreground">({description.requestId})</span>
      ) : null}
      {children}
    </p>
  )
}
