import { clsx } from "clsx"
import { memo } from "react"

import { StatusBadge } from "@/components/ui/StatusBadge"
import { CHAIN_CARD_SIZES } from "../chain-layout"
import {
  CASE_STATUS_TOKENS,
  getCasePriorityLabel,
  getCaseStatusToken,
  NODE_KIND_TOKENS,
} from "../chain-tokens"
import type { ChainSnapshotNode } from "../chain-types"

export type CaseNodeProps = {
  data: ChainSnapshotNode
  selected?: boolean
}

/**
 * Case card of the exploration chain (layer 1). An AI-supplemented case gets a
 * dashed border plus an "AI 补充" badge, a case that needs revalidation gets the
 * amber ring + badge (spec §5.4), and an impacted case carries its analysis
 * reason. Icon, text and ring always encode the same facts, so colour is never
 * the only signal.
 */
export const CaseNode = memo(function CaseNode({ data, selected = false }: CaseNodeProps) {
  const kind = NODE_KIND_TOKENS.case
  const status = getCaseStatusToken(data.status)
  const Icon = kind.icon
  const aiSupplemented = data.source === "ai_supplement"
  const needsRevalidation = data.revalidation === true || data.status === "needs_revalidation"
  const impacted = data.impacted === true
  const priority = getCasePriorityLabel(data.priority)
  const impactReason = data.impactReason ?? ""
  // Both rings are token-driven and mutually exclusive: when a revalidating
  // case is also selected, the selected ring wins so two `ring-*` colour
  // utilities never compete for the same box shadow.
  const revalidationRing = CASE_STATUS_TOKENS.needs_revalidation.ringClass

  const labelParts = [`用例：${data.title}`, status.label]
  if (priority !== "—") labelParts.push(priority)
  if (aiSupplemented) labelParts.push("AI 补充")
  if (needsRevalidation) labelParts.push("需重验")
  if (impacted) labelParts.push(impactReason === "" ? "受影响" : `受影响：${impactReason}`)

  return (
    <article
      data-testid="chain-node-case"
      data-node-id={data.id}
      data-kind="case"
      data-status={data.status}
      data-source={data.source}
      data-revalidation={needsRevalidation ? "true" : undefined}
      data-impacted={impacted ? "true" : undefined}
      aria-label={labelParts.join("，")}
      data-selected={selected ? "true" : undefined}
      className={clsx(
        "relative overflow-hidden rounded-2xl border bg-card p-3 shadow-sm",
        aiSupplemented ? "border-dashed border-indigo-500/50" : "border-border",
        needsRevalidation && "border-amber-500",
        selected ? "ring-2 ring-ring" : needsRevalidation && `ring-2 ${revalidationRing}`,
      )}
      style={{
        width: CHAIN_CARD_SIZES.case.width,
        height: CHAIN_CARD_SIZES.case.height,
      }}
    >
      {aiSupplemented ? (
        <span
          data-testid="chain-supplement-badge"
          className="absolute right-1 top-1 z-10 rounded-full border border-dashed border-indigo-500/50 bg-card px-1.5 py-0.5 text-[10px] font-semibold leading-none text-indigo-700 shadow-sm dark:text-indigo-300"
        >
          <span data-testid="chain-node-case-ai-badge">AI 补充</span>
        </span>
      ) : null}

      <header className="flex items-center gap-1.5">
        <span
          aria-hidden="true"
          className={clsx(
            "inline-flex h-5 w-5 items-center justify-center rounded-md border",
            kind.badgeClass,
          )}
        >
          <Icon className="h-3 w-3" />
        </span>
        <span className="text-[10px] font-semibold tracking-wide text-muted-foreground">
          {kind.label}
        </span>
      </header>

      <h3 className="mt-1.5 line-clamp-2 text-sm font-semibold leading-snug text-card-foreground">
        {data.title}
      </h3>

      <footer className="mt-2 flex flex-wrap items-center gap-1.5">
        <StatusBadge variant={status.badgeVariant}>{status.label}</StatusBadge>
        <span
          data-testid="chain-node-case-priority"
          className="rounded-full bg-muted px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground"
        >
          {priority}
        </span>
        {needsRevalidation ? (
          <span
            data-testid="chain-revalidation-badge"
            className={clsx(
              "rounded-full border px-2 py-0.5 text-[10px] font-medium leading-none",
              CASE_STATUS_TOKENS.needs_revalidation.badgeClass,
            )}
          >
            <span data-testid="chain-node-case-revalidation">需重验</span>
          </span>
        ) : null}
        {impacted ? (
          <span
            data-testid="chain-node-case-impacted"
            className="rounded-full border border-amber-500/30 bg-amber-500/10 px-2 py-0.5 text-[10px] font-medium leading-none text-amber-700 dark:text-amber-300"
          >
            受影响
          </span>
        ) : null}
      </footer>

      {impacted && impactReason !== "" ? (
        <p className="mt-1.5 line-clamp-2 text-[10px] text-muted-foreground">{impactReason}</p>
      ) : null}
    </article>
  )
})
