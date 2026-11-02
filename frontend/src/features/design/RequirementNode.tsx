"use client"

import { Handle, type NodeProps, Position } from "@xyflow/react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"

import { type DesignNodeData, requirementDraftCount } from "./layout"
import type { DesignNodeActions } from "./nodeActions"
import type { DesignRequirement } from "./types"

type RequirementData = Extract<DesignNodeData, { kind: "requirement" }> & {
  actions: DesignNodeActions
}

/** Requirement node: title, clarified revision, coverage and "确认全部". */
export function RequirementNode({ data }: NodeProps) {
  const node = data as unknown as RequirementData
  const requirement: DesignRequirement = node.requirement
  const draftCount = requirementDraftCount(requirement)
  const caseCount = requirement.scenarios.reduce(
    (total, scenario) => total + scenario.cases.length,
    0,
  )
  const confirming = node.actions.confirmingKey === `requirement:${requirement.requirement_id}`

  return (
    <div
      data-testid={`requirement-node-${requirement.requirement_id}`}
      className="w-[280px] rounded-xl border border-primary/30 bg-card px-3 py-2.5 shadow-sm"
    >
      <Handle type="source" position={Position.Bottom} className="!bg-primary" />
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="min-w-0 flex-1 truncate text-sm font-semibold text-foreground">
          {requirement.title}
        </span>
        <Badge tone="info">
          {requirement.status === "clarified" ? "已澄清" : requirement.status} v
          {requirement.revision}
        </Badge>
      </div>
      <p className="mt-1 text-[11px] text-muted-foreground">
        场景 {requirement.scenarios.length} · 用例 {caseCount} · draft {draftCount}
      </p>
      <p className="text-[11px] text-muted-foreground">
        SUT {requirement.target_version_sut_ids.length} 个目标
      </p>
      <Button
        size="sm"
        variant="secondary"
        className="mt-2 w-full"
        data-testid={`btn-confirm-requirement-${requirement.requirement_id}`}
        disabled={draftCount === 0 || confirming}
        onClick={() => node.actions.onConfirmRequirement(requirement.requirement_id)}
      >
        {confirming ? "确认中…" : "确认全部"}
      </Button>
    </div>
  )
}
