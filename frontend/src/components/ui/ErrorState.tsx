import { TriangleAlert } from "lucide-react"

import { cn } from "@/lib/utils"

export type ErrorStateProps = {
  /** Short headline naming the failed operation. */
  title: string
  /** User-facing explanation; never a raw server stack trace. */
  message: string
  /** API request id for support correlation, when the failure carried one. */
  requestId?: string
  /** Recovers the failed read; the button is omitted when recovery is impossible. */
  onRetry?: () => void
  retryLabel?: string
  className?: string
}

/**
 * Shared error state (spec §8.3): alert semantics, an optional request id for
 * correlation, and a retry action when the caller can re-run the read. It is
 * deliberately content-only so pages can keep their surrounding data visible.
 */
export function ErrorState({
  title,
  message,
  requestId,
  onRetry,
  retryLabel = "重试",
  className,
}: ErrorStateProps) {
  return (
    <div
      role="alert"
      data-testid="error-state"
      className={cn(
        "space-y-3 rounded-xl border border-destructive/30 bg-destructive/10 px-4 py-4 text-sm text-destructive",
        className,
      )}
    >
      <p className="flex items-start gap-2 font-medium">
        <TriangleAlert className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
        <span>{title}</span>
      </p>
      <p className="break-words">{message}</p>
      {requestId === undefined || requestId === "" ? null : (
        <p data-testid="error-state-request-id" className="font-mono text-xs opacity-80">
          {`请求 ID：${requestId}`}
        </p>
      )}
      {onRetry === undefined ? null : (
        <button
          type="button"
          onClick={onRetry}
          className="rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {retryLabel}
        </button>
      )}
    </div>
  )
}
