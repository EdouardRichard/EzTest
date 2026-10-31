"use client"

import { Sparkles } from "lucide-react"
import Link from "next/link"

export type DesignCTAProps = {
  versionId: string
  /** `pending_design_count`: clarified requirements not activated into design. */
  pendingCount: number
}

/**
 * Requirements-page design todo (P2): clarified requirements that still need a
 * design conversation. The action deep-links to the version's design tab where
 * the first conversation can be created (spec §10.2).
 */
export function DesignCTA({ versionId, pendingCount }: DesignCTAProps) {
  if (pendingCount <= 0) return null

  return (
    <section
      data-testid="design-cta"
      aria-label="设计待办"
      className="surface-card flex flex-wrap items-center gap-2 px-3 py-2.5"
    >
      <Sparkles className="size-4 text-primary" aria-hidden="true" />
      <p className="text-xs text-foreground">
        <span className="font-semibold">{pendingCount}</span> 条需求待设计
      </p>
      <Link
        href={`/versions/${encodeURIComponent(versionId)}?tab=design`}
        className="ml-auto inline-flex h-8 items-center rounded-md bg-primary px-3 text-xs font-medium text-primary-foreground transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
      >
        开始设计
      </Link>
    </section>
  )
}
