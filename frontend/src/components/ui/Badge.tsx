import type { ReactNode } from "react"

import { cn } from "./cn"

export type BadgeTone = "neutral" | "info" | "success" | "warning" | "danger"

export type BadgeProps = {
  tone?: BadgeTone
  children: ReactNode
  className?: string
}

const TONE_CLASSES: Record<BadgeTone, string> = {
  neutral: "border-border bg-muted text-muted-foreground",
  info: "border-transparent bg-accent text-accent-foreground",
  success: "border-transparent bg-success/10 text-success",
  warning: "border-transparent bg-warning/10 text-warning",
  danger: "border-transparent bg-destructive/10 text-destructive",
}

export function Badge({ tone = "neutral", children, className }: BadgeProps) {
  return (
    <span
      data-tone={tone}
      className={cn(
        "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[11px] font-medium",
        TONE_CLASSES[tone],
        className,
      )}
    >
      {children}
    </span>
  )
}
