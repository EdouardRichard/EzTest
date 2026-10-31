"use client"

import {
  Background,
  BackgroundVariant,
  Controls,
  type Edge,
  Handle,
  MarkerType,
  MiniMap,
  type Node,
  type NodeProps,
  Position,
  ReactFlow,
  ReactFlowProvider,
} from "@xyflow/react"
import { useMemo } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { cn } from "@/components/ui/cn"

import { layoutChain } from "./layout"
import {
  CHAIN_NODE_KIND_LABELS,
  CHAIN_NODE_TONE_CLASSES,
  type ChainEdgeView,
  type ChainNodeView,
} from "./types"

import "@xyflow/react/dist/style.css"

type ChainNodeData = {
  node: ChainNodeView
  selected: boolean
  onSelectNode: (nodeId: string) => void
}

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

function readMetaString(meta: Record<string, unknown> | undefined, key: string): string {
  const value = meta?.[key]
  return typeof value === "string" ? value : ""
}

function readMetaNumber(meta: Record<string, unknown> | undefined, key: string): number | null {
  const value = meta?.[key]
  return typeof value === "number" && Number.isFinite(value) ? value : null
}

function ChainNodeCard({ data }: NodeProps) {
  const { node, selected, onSelectNode } = data as unknown as ChainNodeData
  const meta = node.meta
  const result = readMetaString(meta, "result")
  const status = node.status ?? ""
  const evidenceCount = readMetaNumber(meta, "evidence_count")
  const stepCount = readMetaNumber(meta, "step_count")
  const assertionCount = readMetaNumber(meta, "assertion_count")

  return (
    <button
      type="button"
      data-testid={`chain-node-${node.id}`}
      data-kind={node.kind}
      data-selected={selected ? "true" : "false"}
      aria-label={`查看${CHAIN_NODE_KIND_LABELS[node.kind]}节点 ${node.label}`}
      onClick={() => onSelectNode(node.id)}
      className={cn(
        "w-[260px] cursor-pointer rounded-xl border px-3 py-2.5 text-left shadow-sm transition-colors",
        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        CHAIN_NODE_TONE_CLASSES[node.kind],
        selected ? "ring-1 ring-primary" : null,
      )}
    >
      <Handle type="target" position={Position.Left} />
      <Handle type="source" position={Position.Right} />

      <div className="flex flex-wrap items-center gap-1">
        <span className="text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
          {CHAIN_NODE_KIND_LABELS[node.kind]}
        </span>
        <span className="text-[10px] text-muted-foreground">#{node.ref_id}</span>
        {status.length > 0 ? (
          <Badge tone={STATUS_TONE[status] ?? "neutral"} className="ml-auto">
            {status}
          </Badge>
        ) : null}
      </div>

      <p className="mt-1 line-clamp-2 text-xs font-semibold text-foreground">{node.label}</p>

      <div className="mt-1 flex flex-wrap items-center gap-1.5">
        {result.length > 0 ? <Badge tone={STATUS_TONE[result] ?? "neutral"}>{result}</Badge> : null}
        {node.kind === "case_run" && evidenceCount !== null ? (
          <span className="text-[10px] text-muted-foreground">证据 {evidenceCount}</span>
        ) : null}
        {node.kind === "case_run" && stepCount !== null ? (
          <span className="text-[10px] text-muted-foreground">
            步骤 {stepCount} · 断言 {assertionCount ?? 0}
          </span>
        ) : null}
        {node.version_sut_id !== null && node.version_sut_id !== undefined ? (
          <span className="rounded-full bg-accent px-1.5 py-0.5 text-[10px] text-accent-foreground">
            SUT {node.version_sut_id}
          </span>
        ) : null}
      </div>
    </button>
  )
}

const NODE_TYPES = { chain: ChainNodeCard } as const

export type ChainGraphProps = {
  nodes: ChainNodeView[]
  edges: ChainEdgeView[]
  selectedNodeId: string | null
  onSelectNode: (nodeId: string) => void
}

function ChainCanvas({ nodes, edges, selectedNodeId, onSelectNode }: ChainGraphProps) {
  const layout = useMemo(() => layoutChain(nodes), [nodes])

  const flowNodes = useMemo<Node[]>(
    () =>
      layout.map(({ node, position }) => ({
        id: node.id,
        type: "chain",
        position,
        selected: node.id === selectedNodeId,
        data: {
          node,
          selected: node.id === selectedNodeId,
          onSelectNode,
        },
      })),
    [layout, onSelectNode, selectedNodeId],
  )

  const flowEdges = useMemo<Edge[]>(
    () =>
      edges.map((edge) => ({
        id: `${edge.from}->${edge.to}:${edge.kind}`,
        source: edge.from,
        target: edge.to,
        type: "smoothstep",
        label: undefined,
        markerEnd: { type: MarkerType.ArrowClosed },
        style: { stroke: "var(--color-border)" },
      })),
    [edges],
  )

  return (
    <section
      data-testid="chain-graph"
      data-node-count={nodes.length}
      data-edge-count={edges.length}
      aria-label="探索链路图"
      className="relative h-[620px] w-full overflow-hidden rounded-xl border border-border bg-card"
    >
      <ReactFlow
        nodes={flowNodes}
        edges={flowEdges}
        nodeTypes={NODE_TYPES}
        fitView
        fitViewOptions={{ padding: 0.2 }}
        minZoom={0.12}
        maxZoom={1.6}
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable
        proOptions={{ hideAttribution: true }}
      >
        <Background variant={BackgroundVariant.Dots} gap={20} size={1} />
        <MiniMap pannable zoomable />
        <Controls showInteractive={false} />
      </ReactFlow>
    </section>
  )
}

/** Read-only chain canvas; pan/zoom/fit only, positions are deterministic local UI. */
export function ChainGraph(props: ChainGraphProps) {
  return (
    <ReactFlowProvider>
      <ChainCanvas {...props} />
    </ReactFlowProvider>
  )
}
