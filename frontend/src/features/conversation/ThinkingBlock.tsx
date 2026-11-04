"use client"

import { ChevronRight } from "lucide-react"
import { useState } from "react"

import { cn } from "@/components/ui/cn"

import type { ThinkingPart } from "./types"

export type ThinkingBlockProps = {
  part: ThinkingPart
}

/** "思考了 Ns": sub-second thoughts still read as one second. */
function thoughtSeconds(durationMs: number): string {
  if (!Number.isFinite(durationMs) || durationMs <= 0) return "0s"
  return `${Math.max(1, Math.round(durationMs / 1000))}s`
}

/**
 * One collapsible thinking part (spec §9.1). It is collapsed by default so the
 * thread stays scannable; only the duration is visible until the user expands.
 */
export function ThinkingBlock({ part }: ThinkingBlockProps) {
  const [open, setOpen] = useState(false)

  return (
    <div
      data-testid="thinking-block"
      className="rounded-lg border border-border/70 bg-muted/30 px-2 py-1.5"
    >
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((current) => !current)}
        className="flex w-full items-center gap-1.5 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <ChevronRight
          className={cn(
            "size-3.5 shrink-0 text-muted-foreground transition-transform",
            open && "rotate-90",
          )}
          aria-hidden="true"
        />
        <span className="text-[11px] font-medium text-muted-foreground">
          思考了 {thoughtSeconds(part.duration_ms)}
        </span>
      </button>
      {open ? (
        <p className="mt-1 whitespace-pre-wrap pl-5 text-[11px] leading-relaxed text-muted-foreground">
          {part.text}
        </p>
      ) : null}
    </div>
  )
}
