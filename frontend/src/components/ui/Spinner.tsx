import { Loader2 } from "lucide-react"

import { cn } from "./cn"

export type SpinnerProps = {
  size?: "sm" | "md" | "lg"
  label?: string
  className?: string
}

const SIZE_CLASSES = {
  sm: "size-3.5",
  md: "size-5",
  lg: "size-7",
}

export function Spinner({ size = "md", label = "加载中", className }: SpinnerProps) {
  return (
    <span
      role="status"
      aria-label={label}
      className={cn("inline-flex items-center gap-2", className)}
    >
      <Loader2
        className={cn("animate-spin text-muted-foreground", SIZE_CLASSES[size])}
        aria-hidden="true"
      />
    </span>
  )
}

export type LoadingCardProps = {
  label?: string
}

/** Centered spinner block used while a route or session is loading. */
export function LoadingCard({ label = "正在加载…" }: LoadingCardProps) {
  return (
    <div className="flex min-h-40 items-center justify-center gap-2 text-xs text-muted-foreground">
      <Spinner label={label} />
      <span>{label}</span>
    </div>
  )
}
