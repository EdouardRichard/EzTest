import type { InputHTMLAttributes } from "react"

import { cn } from "./cn"

export type InputProps = InputHTMLAttributes<HTMLInputElement>

const INPUT_CLASSES =
  "h-9 w-full rounded-md border border-border bg-input px-3 text-sm text-foreground " +
  "placeholder:text-muted-foreground transition-colors " +
  "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 " +
  "focus-visible:ring-offset-background disabled:cursor-not-allowed disabled:opacity-50 " +
  "aria-invalid:border-destructive aria-invalid:ring-destructive/30"

export function Input({ className, ...props }: InputProps) {
  return <input className={cn(INPUT_CLASSES, className)} {...props} />
}
