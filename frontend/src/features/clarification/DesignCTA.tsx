"use client"

import { Sparkles } from "lucide-react"
import Link from "next/link"

import { Button } from "@/components/ui/Button"
import type { VersionStage } from "@/features/design/types"

export type DesignCTAProps = {
  versionId: string
  /** `pending_design_count`: clarified requirements not activated into design. */
  pendingCount: number
  /** Deterministic version stage; drives whether "开始设计" can apply the gate. */
  stage?: VersionStage | null
  /** True while the requirement hard gate request is in flight. */
  starting?: boolean
  /**
   * Applies the requirement hard gate (`POST /stage/confirm-requirements`) and
   * navigates to the design workspace. Without it the CTA stays a deep link.
   */
  onStartDesign?: () => void
}

/**
 * Requirements-page design todo (P2): clarified requirements that still need a
 * design conversation. At the requirement gate the action applies the gate
 * (`requirement_review -> designing`) so "开始设计" really advances the stage;
 * otherwise it deep-links to the design tab.
 */
export function DesignCTA({
  versionId,
  pendingCount,
  stage = null,
  starting = false,
  onStartDesign,
}: DesignCTAProps) {
  if (pendingCount <= 0) return null

  const gateReady =
    stage === null ||
    stage === "requirement_review" ||
    stage === "clarifying" ||
    stage === "created"
  const startWithGate = onStartDesign !== undefined && gateReady

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
      {startWithGate ? (
        <Button
          size="sm"
          className="ml-auto"
          data-testid="design-cta-start"
          disabled={starting}
          onClick={onStartDesign}
        >
          {starting ? "提交需求门…" : "开始设计"}
        </Button>
      ) : (
        <Link
          href={`/versions/${encodeURIComponent(versionId)}?tab=design`}
          className="ml-auto inline-flex h-8 items-center rounded-md bg-primary px-3 text-xs font-medium text-primary-foreground transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
        >
          开始设计
        </Link>
      )}
    </section>
  )
}
