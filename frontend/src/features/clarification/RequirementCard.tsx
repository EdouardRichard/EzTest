"use client"

import { Badge } from "@/components/ui/Badge"
import { cn } from "@/components/ui/cn"

import { REQUIREMENT_STATUS_LABEL, REQUIREMENT_STATUS_TONE } from "./labels"
import type { RequirementCardPart } from "./types"

export type RequirementCardProps = {
  part: RequirementCardPart
  onSelect?: (requirementId: string) => void
  className?: string
}

/** Inline requirement card produced by the Curator inside an AI message. */
export function RequirementCard({ part, onSelect, className }: RequirementCardProps) {
  return (
    <button
      type="button"
      data-testid={`requirement-card-${part.requirement_id}`}
      onClick={() => onSelect?.(part.requirement_id)}
      className={cn(
        "w-full rounded-lg border border-border bg-background/70 px-3 py-2 text-left",
        "transition-colors hover:border-primary/40 hover:bg-accent/40",
        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        className,
      )}
    >
      <span className="flex flex-wrap items-center gap-2">
        <span className="text-xs font-semibold text-foreground">{part.title}</span>
        <Badge tone={REQUIREMENT_STATUS_TONE[part.status]}>
          {REQUIREMENT_STATUS_LABEL[part.status]}
        </Badge>
        <span className="ml-auto text-[10px] text-muted-foreground">v{part.revision}</span>
      </span>
    </button>
  )
}
