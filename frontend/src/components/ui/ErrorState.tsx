import type { ReactNode } from "react"

import { ApiError } from "@/lib/api-client"

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
  /** Typed API `details.next_action` (e.g. stage-gate guidance). */
  nextAction?: string
}

function describeError(error: unknown): ErrorDescription {
  if (error instanceof ApiError) {
    const nextAction =
      typeof error.details.next_action === "string" && error.details.next_action.length > 0
        ? error.details.next_action
        : undefined
    return {
      message: error.message,
      requestId: error.requestId,
      code: error.code,
      retryable: error.retryable,
      nextAction,
    }
  }
  if (error instanceof Error) return { message: error.message }
  if (typeof error === "string" && error.length > 0) return { message: error }
  return { message: "发生未知错误，请稍后重试" }
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
  children?: ReactNode
}

/** Compact one-line error for toolbars and forms. */
export function InlineError({ error, children }: InlineErrorProps) {
  const description = describeError(error)
  return (
    <p role="alert" className="text-xs text-destructive">
      {description.message}
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
