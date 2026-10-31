"use client"

import { Sparkles } from "lucide-react"

import { Button } from "@/components/ui/Button"

export type DesignCTAProps = {
  /** `pending_design_count`; P1 equals the clarified requirement count. */
  pendingCount: number
}

/** P1 placeholder: clarified requirements wait for the P2 design conversation. */
export function DesignCTA({ pendingCount }: DesignCTAProps) {
  if (pendingCount <= 0) return null

  return (
    <section
      data-testid="design-cta"
      aria-label="设计待办"
      className="surface-card flex flex-wrap items-center gap-2 px-3 py-2.5"
    >
      <Sparkles className="size-4 text-primary" aria-hidden="true" />
      <p className="text-xs text-foreground">
        <span className="font-semibold">{pendingCount}</span> 条需求待设计 · P2 提供设计对话
      </p>
      <Button size="sm" variant="secondary" disabled title="P2 提供设计对话" className="ml-auto">
        开始设计（P2）
      </Button>
    </section>
  )
}
