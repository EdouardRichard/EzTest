import { clsx } from "clsx"
import { memo } from "react"

import { StatusBadge } from "@/components/ui/StatusBadge"
import {
  getRequirementPriorityLabel,
  getRequirementStatusToken,
  NODE_KIND_TOKENS,
} from "../chain-tokens"
import type { ChainSnapshotNode } from "../chain-types"

export type RequirementNodeProps = {
  data: ChainSnapshotNode
  selected?: boolean
}

/**
 * Requirement card of the exploration chain (layer 0). Shows the requirement
 * title, status, priority, coverage gap and the number of derived cases. The
 * accessible label carries the same information, so selection never depends on
 * colour alone.
 */
export const RequirementNode = memo(function RequirementNode({
  data,
  selected = false,
}: RequirementNodeProps) {
  const kind = NODE_KIND_TOKENS.requirement
  const status = getRequirementStatusToken(data.status)
  const Icon = kind.icon
  const uncovered = data.uncovered === true
  const caseCount = data.caseCount ?? 0
  const priority = getRequirementPriorityLabel(data.priority)

  const labelParts = [`需求：${data.title}`, status.label]
  if (uncovered) labelParts.push("未覆盖")
  labelParts.push(`${caseCount} 个用例`)

  return (
    <article
      data-testid="chain-node-requirement"
      data-node-id={data.id}
      data-kind="requirement"
      data-status={data.status}
      data-uncovered={uncovered ? "true" : undefined}
      aria-label={labelParts.join("，")}
      data-selected={selected ? "true" : undefined}
      className={clsx(
        "w-[240px] rounded-2xl border border-blue-500/30 bg-card p-3 shadow-sm",
        selected && "ring-2 ring-ring",
      )}
    >
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
        <span
          data-testid="chain-node-requirement-priority"
          className="ml-auto rounded-full bg-muted px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground"
        >
          {priority}
        </span>
      </header>

      <h3 className="mt-1.5 line-clamp-2 text-sm font-semibold leading-snug text-card-foreground">
        {data.title}
      </h3>

      <footer className="mt-2 flex flex-wrap items-center gap-1.5">
        <StatusBadge variant={status.badgeVariant}>{status.label}</StatusBadge>
        {uncovered ? (
          <span
            data-testid="chain-node-requirement-uncovered"
            className="rounded-full border border-red-500/30 bg-red-500/10 px-2 py-0.5 text-[10px] font-medium leading-none text-red-700 dark:text-red-300"
          >
            未覆盖
          </span>
        ) : null}
        <span
          data-testid="chain-node-requirement-case-count"
          className="text-[10px] text-muted-foreground"
        >
          {`${caseCount} 个用例`}
        </span>
      </footer>
    </article>
  )
})
