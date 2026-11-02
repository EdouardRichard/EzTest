"use client"

import {
  Background,
  BackgroundVariant,
  type Edge,
  type Node,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
} from "@xyflow/react"
import { useMemo } from "react"

import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"

import { CaseCard } from "./CaseCard"
import { layoutDesignGraph } from "./layout"
import type { DesignNodeActions } from "./nodeActions"
import { RequirementNode } from "./RequirementNode"
import { ScenarioNode } from "./ScenarioNode"
import type { DesignGraph } from "./types"

import "@xyflow/react/dist/style.css"

const NODE_TYPES = {
  requirement: RequirementNode,
  scenario: ScenarioNode,
  case: CaseCard,
} as const

export type DesignMindmapProps = {
  graph: DesignGraph
  selectedCaseId?: string | null
  /** `${scope}:${targetId}` while a confirmation request is in flight. */
  confirmingKey?: string | null
  onSelectCase: (caseId: string) => void
  onConfirmCase: (caseId: string) => void
  onConfirmScenario: (scenarioId: string) => void
  onConfirmRequirement: (requirementId: string) => void
}

function MindmapCanvas({
  graph,
  selectedCaseId = null,
  confirmingKey = null,
  onSelectCase,
  onConfirmCase,
  onConfirmScenario,
  onConfirmRequirement,
}: DesignMindmapProps) {
  const { zoomIn, zoomOut, fitView } = useReactFlow()

  const actions = useMemo<DesignNodeActions>(
    () => ({
      onSelectCase,
      onConfirmCase,
      onConfirmScenario,
      onConfirmRequirement,
      confirmingKey,
    }),
    [onSelectCase, onConfirmCase, onConfirmScenario, onConfirmRequirement, confirmingKey],
  )

  const layout = useMemo(() => layoutDesignGraph(graph), [graph])

  const nodes = useMemo<Node[]>(
    () =>
      layout.nodes.map((node) => ({
        id: node.id,
        type: node.type,
        position: node.position,
        selected: node.id === `case-${selectedCaseId}`,
        data: { ...node.data, actions, selected: node.id === `case-${selectedCaseId}` },
      })),
    [layout, actions, selectedCaseId],
  )

  const edges = useMemo<Edge[]>(
    () => layout.edges.map((edge) => ({ ...edge, type: "smoothstep" })),
    [layout],
  )

  return (
    <section
      data-testid="design-mindmap"
      aria-label="用例脑图"
      className="relative h-[560px] w-full overflow-hidden rounded-xl border border-border bg-card"
    >
      <div className="absolute right-3 top-3 z-10 flex items-center gap-1">
        <Button
          size="sm"
          variant="secondary"
          data-testid="design-zoom-in"
          aria-label="放大"
          onClick={() => {
            void zoomIn()
          }}
        >
          ＋
        </Button>
        <Button
          size="sm"
          variant="secondary"
          data-testid="design-zoom-out"
          aria-label="缩小"
          onClick={() => {
            void zoomOut()
          }}
        >
          －
        </Button>
        <Button
          size="sm"
          variant="secondary"
          data-testid="design-fit-view"
          aria-label="适应画布"
          onClick={() => {
            void fitView({ padding: 0.2 })
          }}
        >
          适应
        </Button>
      </div>

      {nodes.length === 0 ? (
        <div className="flex h-full items-center justify-center p-6">
          <EmptyState
            title="暂无设计图"
            description="新建或打开设计对话后，设计团队会为您生成需求、场景与用例。"
            className="border-none bg-transparent"
          />
        </div>
      ) : (
        <ReactFlow
          nodes={nodes}
          edges={edges}
          nodeTypes={NODE_TYPES}
          fitView
          fitViewOptions={{ padding: 0.2 }}
          minZoom={0.2}
          nodesDraggable={false}
          nodesConnectable={false}
          proOptions={{ hideAttribution: true }}
          onNodeClick={(_event, node) => {
            if (node.type === "case") {
              onSelectCase(node.id.replace(/^case-/, ""))
            }
          }}
        >
          <Background variant={BackgroundVariant.Dots} gap={20} size={1} />
        </ReactFlow>
      )}
    </section>
  )
}

/** Read-only, pan/zoom React Flow mindmap; positions stay local (spec §6.3). */
export function DesignMindmap(props: DesignMindmapProps) {
  return (
    <ReactFlowProvider>
      <MindmapCanvas {...props} />
    </ReactFlowProvider>
  )
}
