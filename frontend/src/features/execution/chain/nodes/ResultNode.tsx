import { clsx } from "clsx"
import { memo } from "react"

import { StatusBadge } from "@/components/ui/StatusBadge"
import { FailureClassBadge } from "../../components/FailureClassBadge"
import type { FailureClass } from "../../types"
import { getResultStatusToken, NODE_KIND_TOKENS } from "../chain-tokens"
import type { ChainSnapshotNode } from "../chain-types"

export type ResultNodeProps = {
  data: ChainSnapshotNode
  selected?: boolean
}

const FAILURE_CLASSES: ReadonlySet<FailureClass> = new Set<FailureClass>([
  "application",
  "environment",
  "session",
  "model",
  "case",
])

function toFailureClass(value: string | undefined): FailureClass | null {
  if (value === undefined || value === "") return null
  return FAILURE_CLASSES.has(value as FailureClass) ? (value as FailureClass) : null
}

/** Renders a duration in ms below one second and in seconds above it. */
export function formatChainDuration(durationMs: number | undefined): string {
  if (durationMs === undefined || durationMs <= 0) return ""
  if (durationMs < 1000) return `${durationMs} ms`
  return `${(durationMs / 1000).toFixed(1)} s`
}

/**
 * Execution-result card of the exploration chain (layer 2). The status token
 * drives icon, colour and label; attempt, duration, evidence count, failure
 * classification and summary answer the "what happened" question without
 * opening the drawer.
 */
export const ResultNode = memo(function ResultNode({ data, selected = false }: ResultNodeProps) {
  const kind = NODE_KIND_TOKENS.result
  const status = getResultStatusToken(data.status)
  const Icon = status.icon
  const KindIcon = kind.icon
  const attempt = data.attempt ?? 1
  const duration = formatChainDuration(data.durationMs)
  const evidenceCount = data.evidenceCount ?? 0
  const failureClass = toFailureClass(data.failureClass)
  const title = data.title === "" ? "执行结果" : data.title
  const summary = data.summary ?? ""

  const labelParts = [`结果：${title}`, status.label, `第 ${attempt} 次`]
  if (duration !== "") labelParts.push(`耗时 ${duration}`)
  if (evidenceCount > 0) labelParts.push(`证据 ${evidenceCount}`)

  return (
    <article
      data-testid="chain-node-result"
      data-node-id={data.id}
      data-kind="result"
      data-status={data.status}
      aria-label={labelParts.join("，")}
      data-selected={selected ? "true" : undefined}
      className={clsx(
        "w-[236px] rounded-2xl border border-border bg-card p-3 shadow-sm",
        selected && "ring-2 ring-ring",
      )}
    >
      <header className="flex items-center gap-1.5">
        <span
          aria-hidden="true"
          className={clsx(
            "inline-flex h-5 w-5 items-center justify-center rounded-md border",
            status.badgeClass,
          )}
        >
          <Icon className="h-3 w-3" />
        </span>
        <span className="text-[10px] font-semibold tracking-wide text-muted-foreground">
          {kind.label}
        </span>
        <span
          data-testid="chain-node-result-attempt"
          className="ml-auto text-[10px] font-medium text-muted-foreground"
        >
          {`第 ${attempt} 次`}
        </span>
      </header>

      <h3 className="mt-1.5 line-clamp-1 text-sm font-semibold leading-snug text-card-foreground">
        {title}
      </h3>

      <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
        <StatusBadge variant={status.badgeVariant}>{status.label}</StatusBadge>
        {failureClass === null ? null : (
          <span data-testid="chain-node-result-failure">
            <FailureClassBadge failureClass={failureClass} />
          </span>
        )}
      </div>

      <div className="mt-1.5 flex flex-wrap items-center gap-2 text-[10px] text-muted-foreground">
        {duration === "" ? null : <span data-testid="chain-node-result-duration">{duration}</span>}
        <span data-testid="chain-node-result-evidence">{`证据 ${evidenceCount}`}</span>
        <span aria-hidden="true" className="ml-auto inline-flex items-center gap-1">
          <KindIcon className="h-3 w-3" />
        </span>
      </div>

      {summary === "" ? null : (
        <p
          data-testid="chain-node-result-summary"
          className="mt-1.5 line-clamp-2 text-[10px] leading-relaxed text-muted-foreground"
        >
          {summary}
        </p>
      )}
    </article>
  )
})
