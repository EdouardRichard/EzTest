"use client"

import {
  Background,
  BackgroundVariant,
  Controls,
  type Edge,
  Handle,
  MiniMap,
  type Node,
  type NodeProps,
  Position,
  ReactFlow,
} from "@xyflow/react"
import "@xyflow/react/dist/style.css"

/**
 * P8 Task 5 spike only. Task 10 replaces this file with the real
 * `ChainCanvas.tsx`, so keep the surface deliberately small: pass nodes/edges
 * in and prove the library renders under Next + React 19.
 */
export type SpikeNodeData = { label: string }

export type SpikeFlowNode = Node<SpikeNodeData, "spike">

function SpikeNode({ data }: NodeProps<SpikeFlowNode>) {
  return (
    <div className="w-[180px] rounded-lg border border-border bg-card px-3 py-2 text-xs text-card-foreground shadow-sm">
      <Handle type="target" position={Position.Top} />
      <span>{data.label}</span>
      <Handle type="source" position={Position.Bottom} />
    </div>
  )
}

/** Defined at module scope so React Flow does not warn about new nodeTypes. */
const NODE_TYPES = { spike: SpikeNode } as const

export type ChainCanvasSpikeProps = {
  nodes: SpikeFlowNode[]
  edges: Edge[]
}

export default function ChainCanvasSpike({ nodes, edges }: ChainCanvasSpikeProps) {
  return (
    <div
      role="tree"
      aria-label={`探索链路画布（${nodes.length} 个节点）`}
      style={{ width: 800, height: 600 }}
      className="relative overflow-hidden rounded-xl border border-border bg-background"
    >
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={NODE_TYPES}
        // The spike owns the canvas role; React Flow's built-in keyboard
        // navigation is disabled so it cannot add competing tab stops. Formal
        // a11y verification lands in P8 Task 22.
        disableKeyboardA11y
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable={false}
      >
        <Background variant={BackgroundVariant.Dots} gap={16} size={1} />
        <MiniMap />
        <Controls />
      </ReactFlow>
    </div>
  )
}
