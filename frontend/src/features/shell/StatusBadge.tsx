import { clsx } from "clsx"
import type { ReactNode } from "react"

export type StatusBadgeVariant = "success" | "warning" | "danger" | "info" | "neutral"

const VARIANT_CLASSES: Record<StatusBadgeVariant, string> = {
  success: "border-emerald-500/25 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300",
  warning: "border-amber-500/25 bg-amber-500/10 text-amber-700 dark:text-amber-300",
  danger: "border-red-500/25 bg-red-500/10 text-red-700 dark:text-red-300",
  info: "border-sky-500/25 bg-sky-500/10 text-sky-700 dark:text-sky-300",
  neutral: "border-border bg-muted text-muted-foreground",
}

const DOT_CLASSES: Record<StatusBadgeVariant, string> = {
  success: "bg-emerald-500",
  warning: "bg-amber-500",
  danger: "bg-red-500",
  info: "bg-sky-500",
  neutral: "bg-muted-foreground",
}

type StatusBadgeProps = {
  variant?: StatusBadgeVariant
  children: ReactNode
  dot?: boolean
  className?: string
}

/**
 * Shared status pill. The text always states the status, so meaning never
 * depends on colour alone.
 */
export function StatusBadge({
  variant = "neutral",
  children,
  dot = true,
  className,
}: StatusBadgeProps) {
  return (
    <span
      className={clsx(
        "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-[11px] font-medium leading-none",
        VARIANT_CLASSES[variant],
        className,
      )}
    >
      {dot ? (
        <span
          aria-hidden="true"
          className={clsx("h-1.5 w-1.5 rounded-full", DOT_CLASSES[variant])}
        />
      ) : null}
      {children}
    </span>
  )
}
