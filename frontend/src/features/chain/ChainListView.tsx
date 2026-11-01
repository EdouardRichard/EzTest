"use client"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { cn } from "@/components/ui/cn"

import { CHAIN_NODE_KIND_LABELS, type ChainNodeView } from "./types"

const STATUS_TONE: Record<string, BadgeTone> = {
  open: "warning",
  draft: "neutral",
  clarified: "info",
  confirmed: "success",
  active: "info",
  running: "info",
  paused: "warning",
  done: "success",
  passed: "success",
  failed: "danger",
  blocked: "warning",
  merged: "info",
  stale: "warning",
  published: "success",
  closed: "neutral",
}

export type ChainListViewProps = {
  nodes: ChainNodeView[]
  selectedNodeId: string | null
  onSelectNode: (nodeId: string) => void
}

function metaSummary(node: ChainNodeView): string {
  const meta = node.meta ?? {}
  const parts: string[] = []
  for (const key of [
    "severity",
    "issue_status",
    "failure_class",
    "report_kind",
    "report_status",
    "trigger",
  ]) {
    const value = meta[key]
    if (typeof value === "string" && value.length > 0) parts.push(value)
  }
  for (const key of ["case_count", "executed_count", "failed_count", "attempt", "duration_ms"]) {
    const value = meta[key]
    if (typeof value === "number" && Number.isFinite(value)) parts.push(`${key} ${value}`)
  }
  return parts.join(" · ")
}

/**
 * P6 list view: the same server read model as the canvas, rendered as an
 * accessible list for keyboard/screen-reader users and narrow (1024px) layouts.
 * Selecting a row keeps the existing detail-sheet behavior.
 */
export function ChainListView({ nodes, selectedNodeId, onSelectNode }: ChainListViewProps) {
  if (nodes.length === 0) {
    return (
      <section
        data-testid="chain-list-view"
        aria-label="探索链路列表"
        className="surface-card px-3 py-4 text-xs text-muted-foreground"
      >
        暂无链路节点。
      </section>
    )
  }
  return (
    <section
      data-testid="chain-list-view"
      aria-label="探索链路列表"
      className="surface-card overflow-hidden motion-reduce:transition-none"
    >
      <ul aria-label="链路节点列表" className="divide-y divide-border">
        {nodes.map((node) => {
          const selected = node.id === selectedNodeId
          return (
            <li key={node.id}>
              <button
                type="button"
                data-testid={`chain-list-node-${node.id}`}
                aria-current={selected ? "true" : undefined}
                aria-label={`打开${CHAIN_NODE_KIND_LABELS[node.kind]}节点 ${node.label}`}
                onClick={() => onSelectNode(node.id)}
                className={cn(
                  "flex min-h-11 w-full flex-wrap items-center gap-2 px-3 py-2 text-left transition-colors",
                  "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                  "motion-reduce:transition-none",
                  selected ? "bg-accent/60" : "hover:bg-muted/60",
                )}
              >
                <span className="w-20 shrink-0 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
                  {CHAIN_NODE_KIND_LABELS[node.kind]}
                </span>
                <span className="min-w-0 flex-1 truncate text-xs font-medium text-foreground">
                  {node.label}
                </span>
                <span className="text-[10px] text-muted-foreground">#{node.ref_id}</span>
                {node.status !== null && node.status !== undefined && node.status.length > 0 ? (
                  <Badge tone={STATUS_TONE[node.status] ?? "neutral"}>{node.status}</Badge>
                ) : null}
                {metaSummary(node).length > 0 ? (
                  <span className="hidden w-full truncate text-[10px] text-muted-foreground sm:block">
                    {metaSummary(node)}
                  </span>
                ) : null}
              </button>
            </li>
          )
        })}
      </ul>
    </section>
  )
}
