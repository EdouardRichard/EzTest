"use client"

import { Handle, type NodeProps, Position } from "@xyflow/react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { cn } from "@/components/ui/cn"

import type { DesignNodeData } from "./layout"
import { CASE_VERSION_STATUS_LABEL, type DesignNodeActions } from "./nodeActions"
import type { CaseVersionStatus } from "./types"

type CaseData = Extract<DesignNodeData, { kind: "case" }> & {
  actions: DesignNodeActions
  selected: boolean
}

const STATUS_TONE: Record<Exclude<CaseVersionStatus, "superseded">, BadgeTone> = {
  draft: "info",
  confirmed: "success",
  stale: "warning",
}

/** One case card: status, priority, SUT/page chips and the draft-only confirm. */
export function CaseCard({ data }: NodeProps) {
  const node = data as unknown as CaseData
  const designCase = node.case
  const confirming = node.actions.confirmingKey === `case:${designCase.case_id}`
  const crossSystem = designCase.version_sut_ids.length > 1

  return (
    <div
      data-testid={`case-card-${designCase.case_id}`}
      data-selected={node.selected ? "true" : "false"}
      className={cn(
        "w-[300px] rounded-xl border bg-card px-3 py-2.5 shadow-sm transition-colors",
        node.selected ? "border-primary ring-1 ring-primary/40" : "border-border",
      )}
    >
      <Handle type="target" position={Position.Top} />
      <div className="flex flex-wrap items-center gap-1.5">
        <button
          type="button"
          data-testid={`case-select-${designCase.case_id}`}
          aria-label={`查看用例 ${designCase.title}`}
          onClick={() => node.actions.onSelectCase(designCase.case_id)}
          className="min-w-0 flex-1 truncate text-left text-sm font-medium text-foreground hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {designCase.title}
        </button>
        <Badge tone={STATUS_TONE[designCase.status]}>
          {CASE_VERSION_STATUS_LABEL[designCase.status]}
        </Badge>
      </div>
      <div className="mt-1 flex flex-wrap items-center gap-1">
        <span className="text-[11px] font-medium text-muted-foreground">{designCase.priority}</span>
        {designCase.version_sut_ids.map((sutId) => (
          <span
            key={sutId}
            className="rounded-full bg-accent px-1.5 py-0.5 text-[10px] text-accent-foreground"
          >
            SUT {sutId}
          </span>
        ))}
        {designCase.page_ids.map((pageId) => (
          <span
            key={pageId}
            className="rounded-full bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground"
          >
            页面 {pageId}
          </span>
        ))}
        {crossSystem ? <Badge tone="warning">跨系统</Badge> : null}
      </div>
      <p className="mt-1 text-[10px] text-muted-foreground">
        v{designCase.revision} · 步骤 {designCase.step_count} · 断言 {designCase.assertion_count}
      </p>
      {designCase.status === "draft" ? (
        <Button
          size="sm"
          variant="secondary"
          className="mt-2 w-full"
          data-testid={`btn-confirm-case-${designCase.case_id}`}
          disabled={confirming}
          onClick={() => node.actions.onConfirmCase(designCase.case_id)}
        >
          {confirming ? "确认中…" : "确认用例"}
        </Button>
      ) : null}
    </div>
  )
}
