"use client"

import Link from "next/link"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"

import type { CaseCardPart, CaseCardStatus } from "./types"

const STATUS_LABEL: Record<CaseCardStatus, string> = {
  draft: "draft",
  confirmed: "confirmed",
  stale: "stale",
  superseded: "superseded",
}

const STATUS_TONE: Record<CaseCardStatus, BadgeTone> = {
  draft: "info",
  confirmed: "success",
  stale: "warning",
  superseded: "neutral",
}

export type CaseCardProps = {
  part: CaseCardPart
  /**
   * Inline open (design workspace selects the case in the detail layer).
   * Preferred over navigation so the conversation and the mindmap state stay.
   */
  onOpen?: (caseId: string) => void
  /**
   * Fallback deep link target. The reminder thread may render a case_card too,
   * and it has no inline design selection; linking to `?tab=design&case=` keeps
   * the action real instead of rendering a dead control.
   */
  versionId?: string
}

/** One `case_card` part: the case reference emitted into the conversation. */
export function CaseCard({ part, onOpen, versionId }: CaseCardProps) {
  const href =
    versionId === undefined
      ? undefined
      : `/versions/${encodeURIComponent(versionId)}?tab=design&case=${encodeURIComponent(
          part.case_id,
        )}`

  return (
    <div
      data-testid={`message-case-card-${part.case_id}`}
      className="flex w-full max-w-[320px] flex-col gap-1 rounded-lg border border-border bg-muted/30 px-2.5 py-2"
    >
      <div className="flex items-center gap-1.5">
        <span className="min-w-0 flex-1 truncate text-xs font-medium text-foreground">
          {part.title}
        </span>
        <Badge tone={STATUS_TONE[part.status]}>{STATUS_LABEL[part.status]}</Badge>
      </div>
      <div className="flex flex-wrap items-center gap-1.5 text-[10px] text-muted-foreground">
        <span>用例 #{part.case_id}</span>
        {part.case_version_id === undefined || part.case_version_id === null ? null : (
          <span>版本 #{part.case_version_id}</span>
        )}
        {onOpen === undefined ? (
          href === undefined ? null : (
            <Link
              href={href}
              className="ml-auto rounded-md border border-border bg-card px-2 py-0.5 text-[10px] font-medium text-foreground hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              打开用例
            </Link>
          )
        ) : (
          <Button
            size="sm"
            variant="secondary"
            data-testid={`message-case-card-open-${part.case_id}`}
            className="ml-auto h-6 px-2 text-[10px]"
            onClick={() => onOpen(part.case_id)}
          >
            打开用例
          </Button>
        )}
      </div>
    </div>
  )
}
