import type { ReactNode } from "react"

import { cn } from "./cn"

export type AlertTone = "info" | "success" | "warning" | "danger"

export type AlertProps = {
  tone?: AlertTone
  title?: ReactNode
  children?: ReactNode
  className?: string
}

const TONE_CLASSES: Record<AlertTone, string> = {
  info: "border-border bg-muted/60 text-foreground",
  success: "border-success/30 bg-success/10 text-success",
  warning: "border-warning/30 bg-warning/10 text-warning",
  danger: "border-destructive/30 bg-destructive/10 text-destructive",
}

export function Alert({ tone = "info", title, children, className }: AlertProps) {
  const assertive = tone === "danger" || tone === "warning"

  return (
    <div
      role={assertive ? "alert" : "status"}
      data-tone={tone}
      className={cn(
        "rounded-lg border px-4 py-3 text-xs leading-relaxed",
        TONE_CLASSES[tone],
        className,
      )}
    >
      {title ? <p className="mb-0.5 font-semibold">{title}</p> : null}
      {children ? <div>{children}</div> : null}
    </div>
  )
}
