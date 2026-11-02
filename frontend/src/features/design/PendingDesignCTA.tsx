"use client"

import { Sparkles } from "lucide-react"
import Link from "next/link"

import { Button } from "@/components/ui/Button"

export type PendingDesignCTAProps = {
  versionId: string
  /** `pending_design_count` from the design graph/overview. */
  pendingCount: number
  /** True once the version has any design conversation (latest one is active). */
  hasConversation: boolean
  onCreateConversation?: () => void
  /** When set, render a jump link instead of a create button (requirements page). */
  href?: string
}

/**
 * "N 条待设计" call-to-action (spec §10.2). Without a design conversation it
 * offers to create the first one; once a conversation exists new clarified
 * requirements are auto-joined into the latest round.
 */
export function PendingDesignCTA({
  versionId,
  pendingCount,
  hasConversation,
  onCreateConversation,
  href,
}: PendingDesignCTAProps) {
  if (pendingCount <= 0) return null

  return (
    <section
      data-testid="pending-design-cta"
      data-version-id={versionId}
      aria-label="设计待办"
      className="surface-card flex flex-wrap items-center gap-2 px-3 py-2.5"
    >
      <Sparkles className="size-4 text-primary" aria-hidden="true" />
      <p className="text-xs text-foreground">
        <span className="font-semibold">{pendingCount}</span> 条待设计
      </p>
      {hasConversation ? (
        <p className="text-[11px] text-muted-foreground">新需求会自动并入当前设计对话</p>
      ) : href !== undefined ? (
        <Link
          href={href}
          className="ml-auto inline-flex h-8 items-center rounded-md border border-border bg-card px-3 text-xs font-medium text-foreground hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          前往设计
        </Link>
      ) : onCreateConversation !== undefined ? (
        <Button size="sm" className="ml-auto" onClick={onCreateConversation}>
          新建设计对话
        </Button>
      ) : null}
    </section>
  )
}
