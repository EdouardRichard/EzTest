import type { ReactNode } from "react"

import { cn } from "./cn"

export type FieldProps = {
  id: string
  label: string
  hint?: string
  error?: string
  required?: boolean
  children: ReactNode
  className?: string
}

/** Label + control + hint/error wrapper with stable ids for aria wiring. */
export function Field({ id, label, hint, error, required, children, className }: FieldProps) {
  const hintId = `${id}-hint`
  const errorId = `${id}-error`

  return (
    <div className={cn("flex flex-col gap-1.5", className)}>
      <label
        htmlFor={id}
        className={cn(
          "text-xs font-medium text-foreground",
          required && "after:ml-1 after:text-destructive after:content-['*']",
        )}
      >
        {label}
      </label>
      {children}
      {error ? (
        <p id={errorId} role="alert" className="text-xs text-destructive">
          {error}
        </p>
      ) : hint ? (
        <p id={hintId} className="text-xs text-muted-foreground">
          {hint}
        </p>
      ) : null}
    </div>
  )
}
