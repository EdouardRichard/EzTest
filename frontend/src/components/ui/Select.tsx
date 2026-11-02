import type { SelectHTMLAttributes } from "react"

import { cn } from "./cn"

export type SelectProps = SelectHTMLAttributes<HTMLSelectElement>

const SELECT_CLASSES =
  "h-9 w-full rounded-md border border-border bg-input px-3 text-sm text-foreground " +
  "transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring " +
  "focus-visible:ring-offset-1 focus-visible:ring-offset-background " +
  "disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive"

export function Select({ className, children, ...props }: SelectProps) {
  return (
    <select className={cn(SELECT_CLASSES, className)} {...props}>
      {children}
    </select>
  )
}
