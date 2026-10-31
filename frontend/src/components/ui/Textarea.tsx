import type { TextareaHTMLAttributes } from "react"

import { cn } from "./cn"

export type TextareaProps = TextareaHTMLAttributes<HTMLTextAreaElement>

const TEXTAREA_CLASSES =
  "min-h-24 w-full resize-y rounded-md border border-border bg-input px-3 py-2 text-sm " +
  "text-foreground placeholder:text-muted-foreground transition-colors " +
  "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 " +
  "focus-visible:ring-offset-background disabled:cursor-not-allowed disabled:opacity-50 " +
  "aria-invalid:border-destructive aria-invalid:ring-destructive/30"

export function Textarea({ className, ...props }: TextareaProps) {
  return <textarea className={cn(TEXTAREA_CLASSES, className)} {...props} />
}
