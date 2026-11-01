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
  useReactFlow,
} from "@xyflow/react"
import { useEffect, useMemo } from "react"

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

function readMetaBoolean(meta: Record<string, unknown> | undefined, key: string): boolean {
  return meta?.[key] === true
}

/** Kind-specific P6 typed meta lines rendered on the card. */
function nodeMetaLines(node: ChainNodeView): string[] {
  const meta = node.meta
  const lines: string[] = []
  switch (node.kind) {
    case "requirement": {
      const cases = readMetaNumber(meta, "case_count")
      const executed = readMetaNumber(meta, "executed_count")
      const failed = readMetaNumber(meta, "failed_count")
      if (cases !== null)
        lines.push(`用例 ${cases} · 已执行 ${executed ?? 0} · 失败 ${failed ?? 0}`)
      if (readMetaBoolean(meta, "uncovered")) lines.push("覆盖缺口：无关联用例")
      break
    }
    case "scenario": {
      const cases = readMetaNumber(meta, "case_count")
      if (cases !== null) lines.push(`用例 ${cases}`)
      break
    }
    case "case_version": {
      const steps = readMetaNumber(meta, "step_count")
      const assertions = readMetaNumber(meta, "assertion_count")
      if (steps !== null) lines.push(`步骤 ${steps} · 断言 ${assertions ?? 0}`)
      if (readMetaBoolean(meta, "stale")) lines.push("用例版本已过期")
      break
    }
    case "wave": {
      const trigger = readMetaString(meta, "trigger")
      if (trigger.length > 0) lines.push(`触发 ${trigger}`)
      break
    }
    case "case_run": {
      const attempt = readMetaNumber(meta, "attempt")
      const duration = readMetaNumber(meta, "duration_ms")
      const evidence = readMetaNumber(meta, "evidence_count")
      const parts: string[] = []
      if (attempt !== null) parts.push(`第 ${attempt} 次`)
      if (duration !== null) parts.push(`${duration}ms`)
      if (evidence !== null) parts.push(`证据 ${evidence}`)
      if (parts.length > 0) lines.push(parts.join(" · "))
      break
    }
    case "issue": {
      const severity = readMetaString(meta, "severity")
      const status = readMetaString(meta, "issue_status")
      const category = readMetaString(meta, "category")
      const codeRefs = readMetaNumber(meta, "code_ref_count")
      const parts = [severity, status, category].filter((part) => part.length > 0)
      if (codeRefs !== null && codeRefs > 0) parts.push(`代码引用 ${codeRefs}`)
      if (parts.length > 0) lines.push(parts.join(" · "))
      break
    }
    case "report": {
      const kind = readMetaString(meta, "report_kind")
      const status = readMetaString(meta, "report_status")
      const revision = readMetaNumber(meta, "revision")
      const parts = [kind, status].filter((part) => part.length > 0)
      if (revision !== null) parts.push(`v${revision}`)
      if (parts.length > 0) lines.push(parts.join(" · "))
      break
    }
    case "closure": {
      const status = readMetaString(meta, "closure_status")
      if (status.length > 0) lines.push(`闭环 ${status}`)
      break
    }
  }
  return lines
}

function ChainNodeCard({ data }: NodeProps) {
  const { node, selected, onSelectNode } = data as unknown as ChainNodeData
  const meta = node.meta
  const result = readMetaString(meta, "result")
  const status = node.status ?? ""
  const evidenceCount = readMetaNumber(meta, "evidence_count")
  const stepCount = readMetaNumber(meta, "step_count")
  const assertionCount = readMetaNumber(meta, "assertion_count")
  const metaLines = nodeMetaLines(node)

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
        "motion-reduce:transition-none",
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

      {metaLines.length > 0 ? (
        <div className="mt-1 space-y-0.5">
          {metaLines.map((line) => (
            <p key={line} className="truncate text-[10px] text-muted-foreground">
              {line}
            </p>
          ))}
        </div>
      ) : null}

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
  /** P6: auto-center the followed node when the snapshot/selection changes. */
  follow?: boolean
  followNodeId?: string | null
  /** P6: user pan/zoom pauses follow; the host exposes a resume button. */
  onUserMove?: () => void
}

function prefersReducedMotion(): boolean {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return false
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches
}

function ChainCanvas({
  nodes,
  edges,
  selectedNodeId,
  onSelectNode,
  follow = false,
  followNodeId = null,
  onUserMove,
}: ChainGraphProps) {
  const layout = useMemo(() => layoutChain(nodes), [nodes])
  const { setCenter } = useReactFlow()
  const followedPosition = useMemo(
    () => layout.find((entry) => entry.node.id === followNodeId)?.position ?? null,
    [followNodeId, layout],
  )

  // P6 follow: center the selected/first node. The user pan/zoom handler pauses
  // follow in the host; this effect only re-centers while follow is true.
  useEffect(() => {
    if (!follow || followedPosition === null) return
    setCenter(followedPosition.x + 130, followedPosition.y + 48, {
      zoom: 1,
      duration: prefersReducedMotion() ? 0 : 300,
    })
  }, [follow, followedPosition, setCenter])

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
      data-follow={follow ? "true" : "false"}
      aria-label="探索链路图"
      className="relative h-[480px] w-full overflow-hidden rounded-xl border border-border bg-card motion-reduce:transition-none lg:h-[620px]"
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
        onMoveStart={(event) => {
          // Programmatic fitView/center passes a null event; only a real user
          // pan/zoom pauses follow.
          if (event) onUserMove?.()
        }}
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
