import { clsx } from "clsx"
import { memo } from "react"

import { StatusBadge } from "@/components/ui/StatusBadge"
import { CHAIN_CARD_SIZES } from "../chain-layout"
import {
  getIssueCategoryLabel,
  getIssueConfidenceLabel,
  getIssueSeverityToken,
  getIssueStatusToken,
  NODE_KIND_TOKENS,
} from "../chain-tokens"
import type { ChainSnapshotNode } from "../chain-types"

export type IssueNodeProps = {
  data: ChainSnapshotNode
  selected?: boolean
}

/**
 * Delivery-issue card of the exploration chain (layer 3). Severity, confirmation
 * status, category, confidence and evidence count are rendered as text; the
 * severity icon and colour are redundant encodings only (spec §5.7).
 */
export const IssueNode = memo(function IssueNode({ data, selected = false }: IssueNodeProps) {
  const severity = getIssueSeverityToken(data.severity)
  const status = getIssueStatusToken(data.status)
  const SeverityIcon = severity.icon
  const kind = NODE_KIND_TOKENS.issue
  const category = getIssueCategoryLabel(data.category)
  const confidence = getIssueConfidenceLabel(data.confidence)
  const evidenceCount = data.evidenceCount ?? 0

  const labelParts = [
    `问题：${data.title}`,
    `严重级 ${severity.label}`,
    status.label,
    `置信度 ${confidence}`,
  ]

  return (
    <article
      data-testid="chain-node-issue"
      data-node-id={data.id}
      data-kind="issue"
      data-status={data.status}
      data-severity={data.severity ?? ""}
      data-category={data.category ?? ""}
      aria-label={labelParts.join("，")}
      data-selected={selected ? "true" : undefined}
      className={clsx(
        "overflow-hidden rounded-2xl border border-red-500/25 bg-card p-3 shadow-sm",
        selected && "ring-2 ring-ring",
      )}
      style={{
        width: CHAIN_CARD_SIZES.issue.width,
        height: CHAIN_CARD_SIZES.issue.height,
      }}
    >
      <header className="flex items-center gap-1.5">
        <span
          aria-hidden="true"
          className={clsx(
            "inline-flex h-5 w-5 items-center justify-center rounded-md border",
            severity.badgeClass,
          )}
        >
          <SeverityIcon className="h-3 w-3" />
        </span>
        <span className="text-[10px] font-semibold tracking-wide text-muted-foreground">
          {kind.label}
        </span>
        <span
          data-testid="chain-node-issue-severity"
          className={clsx(
            "ml-auto rounded-full border px-2 py-0.5 text-[10px] font-medium leading-none",
            severity.badgeClass,
          )}
        >
          {severity.label}
        </span>
      </header>

      <h3 className="mt-1.5 line-clamp-2 text-sm font-semibold leading-snug text-card-foreground">
        {data.title}
      </h3>

      <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
        <StatusBadge variant={status.badgeVariant}>{status.label}</StatusBadge>
        <span
          data-testid="chain-node-issue-category"
          className="rounded-full bg-muted px-2 py-0.5 text-[10px] font-medium leading-none text-muted-foreground"
        >
          {category}
        </span>
        <span
          data-testid="chain-node-issue-confidence"
          className="rounded-full bg-muted px-2 py-0.5 text-[10px] font-medium leading-none text-muted-foreground"
        >
          {confidence}
        </span>
      </div>

      <footer className="mt-1.5 text-[10px] text-muted-foreground">
        <span data-testid="chain-node-issue-evidence">{`证据 ${evidenceCount}`}</span>
      </footer>
    </article>
  )
})
