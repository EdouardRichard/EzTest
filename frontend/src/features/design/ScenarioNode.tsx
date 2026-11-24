"use client"

import { Handle, type NodeProps, Position } from "@xyflow/react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"

import type { DesignNodeData } from "./layout"
import type { DesignNodeActions } from "./nodeActions"

type ScenarioData = Extract<DesignNodeData, { kind: "scenario" }> & {
  actions: DesignNodeActions
  /** D-09: hidden below the minimum action zoom (React Flow scales the node). */
  destructiveActionsDisabled?: boolean
}

/** Scenario node: name, case count, description and "确认场景". */
export function ScenarioNode({ data }: NodeProps) {
  const node = data as unknown as ScenarioData
  const { scenario } = node
  const confirming = node.actions.confirmingKey === `scenario:${scenario.id}`

  return (
    <div
      data-testid={`scenario-node-${scenario.id}`}
      className="w-[260px] rounded-xl border border-border bg-card px-3 py-2.5 shadow-sm"
    >
      <Handle type="target" position={Position.Top} />
      <Handle type="source" position={Position.Bottom} />
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="min-w-0 flex-1 truncate text-sm font-semibold text-foreground">
          {scenario.name}
        </span>
        <Badge tone="neutral">用例 {scenario.cases.length}</Badge>
      </div>
      {/* v0.2.4-B: the SUT/page count line read the removed version_sut_ids/page_ids
          arrays. Under the natural-language contract a scenario summarizes its
          prose description instead. */}
      {scenario.description !== "" ? (
        <p className="mt-1 text-[11px] text-muted-foreground">{scenario.description}</p>
      ) : null}
      {node.actions.caseReviewEnabled === false ||
      node.destructiveActionsDisabled === true ? null : (
        <Button
          size="sm"
          variant="secondary"
          className="mt-2 w-full"
          data-testid={`btn-confirm-scenario-${scenario.id}`}
          disabled={node.draftCaseCount === 0 || confirming}
          onClick={() => node.actions.onConfirmScenario(scenario.id)}
        >
          {confirming ? "确认中…" : "确认场景"}
        </Button>
      )}
    </div>
  )
}
