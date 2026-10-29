"use client"

import {
  Background,
  BackgroundVariant,
  Controls,
  type EdgeTypes,
  Handle,
  MarkerType,
  MiniMap,
  type Node,
  type NodeMouseHandler,
  type NodeProps,
  type NodeTypes,
  type OnMoveStart,
  Position,
  ReactFlow,
  ReactFlowProvider,
} from "@xyflow/react"
import "@xyflow/react/dist/style.css"
import { memo, useCallback, useMemo } from "react"

import type { LaidEdge, PositionedNode } from "./chain-layout"
import { layoutChain } from "./chain-layout"
import type { ChainSnapshot, ChainSnapshotNode } from "./chain-types"
import { ChainEdge, type ChainFlowEdge, getChainEdgeMarkerColor } from "./edges/ChainEdge"
import { CaseNode } from "./nodes/CaseNode"
import { IssueNode } from "./nodes/IssueNode"
import { RequirementNode } from "./nodes/RequirementNode"
import { ResultNode } from "./nodes/ResultNode"
import { useChainViewport, usePrefersReducedMotion } from "./use-chain-viewport"

/** One React Flow node of the exploration chain. */
export type ChainFlowNode = Node<ChainSnapshotNode, ChainSnapshotNode["kind"]>

export type ChainCanvasProps = {
  /** One read-model snapshot; positions are recomputed inside the canvas. */
  snapshot: ChainSnapshot
  /** Backend node id selected in the drawer/URL. */
  selectedId?: string | null
  /** Called with the backend node id of a clicked card. */
  onSelect?: (nodeId: string) => void
  /** Requirement ids whose cases/results/issues are hidden. */
  collapsed?: readonly string[] | ReadonlySet<string>
  /** Double-clicking a requirement card requests a collapse toggle. */
  onToggleCollapse?: (requirementId: string) => void
  /** Centres the viewport on the running result (Task 11 toolbar). */
  follow?: boolean
}

/**
 * Invisible connection anchors.
 *
 * The Task 9 cards are pure content articles and deliberately carry no handles;
 * React Flow still needs one handle bound per side to resolve edge endpoints.
 * The anchors are absolute (they add no size or padding to the card) and fully
 * transparent, because the chain is read-only and never connects nodes.
 */
const NODE_ANCHOR_STYLE = { opacity: 0, pointerEvents: "none" as const }

function ChainNodeAnchors() {
  return (
    <>
      <Handle
        type="target"
        position={Position.Left}
        isConnectable={false}
        tabIndex={-1}
        aria-hidden="true"
        className="chain-node-anchor"
        style={NODE_ANCHOR_STYLE}
      />
      <Handle
        type="source"
        position={Position.Right}
        isConnectable={false}
        tabIndex={-1}
        aria-hidden="true"
        className="chain-node-anchor"
        style={NODE_ANCHOR_STYLE}
      />
    </>
  )
}

type ChainNodeProps = NodeProps<ChainFlowNode>

/**
 * The four node types are the Task 9 cards plus the invisible anchors. No
 * wrapper adds width, height or padding; the card owns its `CHAIN_CARD_SIZES`
 * dimensions. Selection is injected through `selectedId` (node.selected).
 */
const ChainRequirementNode = memo(function ChainRequirementNode({
  data,
  selected,
}: ChainNodeProps) {
  return (
    <>
      <RequirementNode data={data} selected={selected} />
      <ChainNodeAnchors />
    </>
  )
})

const ChainCaseNode = memo(function ChainCaseNode({ data, selected }: ChainNodeProps) {
  return (
    <>
      <CaseNode data={data} selected={selected} />
      <ChainNodeAnchors />
    </>
  )
})

const ChainResultNode = memo(function ChainResultNode({ data, selected }: ChainNodeProps) {
  return (
    <>
      <ResultNode data={data} selected={selected} />
      <ChainNodeAnchors />
    </>
  )
})

const ChainIssueNode = memo(function ChainIssueNode({ data, selected }: ChainNodeProps) {
  return (
    <>
      <IssueNode data={data} selected={selected} />
      <ChainNodeAnchors />
    </>
  )
})

// Module scope keeps the identities stable across renders (React Flow warns
// about fresh nodeTypes/edgeTypes objects on every render).
const NODE_TYPES = {
  requirement: ChainRequirementNode,
  case: ChainCaseNode,
  result: ChainResultNode,
  issue: ChainIssueNode,
} as unknown as NodeTypes

const EDGE_TYPES = { chain: ChainEdge } as unknown as EdgeTypes

const FIT_VIEW_OPTIONS = { padding: 0.2 } as const

function buildFlowNodes(
  nodes: PositionedNode[],
  selectedId: string | null,
  reducedMotion: boolean,
): ChainFlowNode[] {
  return nodes.map((node) => {
    const running = node.kind === "result" && node.data.status === "running"
    return {
      id: node.id,
      type: node.kind,
      position: { x: node.x, y: node.y },
      data: node.data,
      selected: selectedId !== null && node.id === selectedId,
      // Sizes come from the same CHAIN_CARD_SIZES-driven layout; the card itself
      // renders at exactly these dimensions, so this only seeds measurement.
      initialWidth: node.width,
      initialHeight: node.height,
      draggable: false,
      connectable: false,
      className: running
        ? reducedMotion
          ? "chain-node-running"
          : "chain-node-running animate-pulse"
        : undefined,
    }
  })
}

function buildFlowEdges(
  edges: LaidEdge[],
  nodeById: ReadonlyMap<string, ChainSnapshotNode>,
  reducedMotion: boolean,
): ChainFlowEdge[] {
  return edges.map((edge) => {
    const target = nodeById.get(edge.targetId)
    const targetStatus = target?.kind === "result" ? target.status : undefined
    const running = targetStatus === "running"
    return {
      id: edge.id,
      source: edge.sourceId,
      target: edge.targetId,
      type: "chain",
      data: { kind: edge.kind, targetStatus },
      animated: running && !reducedMotion,
      markerEnd: {
        type: MarkerType.ArrowClosed,
        color: getChainEdgeMarkerColor(edge.kind, targetStatus),
      },
      className: running ? "chain-edge-running" : undefined,
    }
  })
}

/** Requirement id inside a possibly-collapsed Set/array, sorted for a stable key. */
function collapsedKeyOf(collapsed: readonly string[] | ReadonlySet<string> | undefined): string {
  if (collapsed === undefined) return ""
  return [...new Set(collapsed)].sort().join("|")
}

function ChainCanvasFlow({
  snapshot,
  selectedId = null,
  onSelect,
  collapsed,
  onToggleCollapse,
  follow = false,
}: ChainCanvasProps) {
  const reducedMotion = usePrefersReducedMotion()

  // The public prop may be a fresh array/Set on every render (the toolbar keeps
  // its state that way); a serialized key keeps layout and React Flow nodes
  // stable unless the set of collapsed requirements really changed.
  const collapsedKey = useMemo(() => collapsedKeyOf(collapsed), [collapsed])
  const collapsedIds = useMemo(
    () => (collapsedKey === "" ? [] : collapsedKey.split("|")),
    [collapsedKey],
  )

  const layout = useMemo(
    () => layoutChain(snapshot.nodes, { edges: snapshot.edges, collapsed: collapsedIds }),
    [snapshot.nodes, snapshot.edges, collapsedIds],
  )

  const nodeById = useMemo(
    () => new Map(snapshot.nodes.map((node) => [node.id, node])),
    [snapshot.nodes],
  )

  const flowNodes = useMemo(
    () => buildFlowNodes(layout.nodes, selectedId, reducedMotion),
    [layout.nodes, selectedId, reducedMotion],
  )

  const flowEdges = useMemo(
    () => buildFlowEdges(layout.edges, nodeById, reducedMotion),
    [layout.edges, nodeById, reducedMotion],
  )

  const runningNode = useMemo(() => {
    const node = layout.nodes.find(
      (candidate) => candidate.kind === "result" && candidate.data.status === "running",
    )
    if (node === undefined) return null
    return { id: node.id, x: node.x, y: node.y, width: node.width, height: node.height }
  }, [layout.nodes])

  const { followPaused, pauseFollow, resumeFollow } = useChainViewport({
    follow,
    target: runningNode,
    reducedMotion,
  })

  const handleNodeClick = useCallback<NodeMouseHandler<ChainFlowNode>>(
    (_event, node) => {
      onSelect?.(node.id)
    },
    [onSelect],
  )

  const handleNodeDoubleClick = useCallback<NodeMouseHandler<ChainFlowNode>>(
    (_event, node) => {
      if (node.type !== "requirement") return
      onToggleCollapse?.(node.id)
    },
    [onToggleCollapse],
  )

  const handleMoveStart = useCallback<OnMoveStart>(
    (event) => {
      // A null event means React Flow itself moved the viewport (setCenter /
      // fitView); only a real gesture may pause follow-execution.
      if (event !== null) pauseFollow()
    },
    [pauseFollow],
  )

  return (
    <div
      data-testid="chain-canvas"
      className="relative h-[600px] w-full overflow-hidden rounded-xl border border-border bg-background"
    >
      <ReactFlow
        nodes={flowNodes}
        edges={flowEdges}
        nodeTypes={NODE_TYPES}
        edgeTypes={EDGE_TYPES}
        onNodeClick={handleNodeClick}
        onNodeDoubleClick={handleNodeDoubleClick}
        onMoveStart={handleMoveStart}
        fitView
        fitViewOptions={FIT_VIEW_OPTIONS}
        onlyRenderVisibleElements
        // React Flow's d3 zoom binds a native dblclick handler on the pane that
        // stops propagation, which would swallow the card's double-click
        // (collapse toggle). Zooming stays available via Controls and the wheel.
        zoomOnDoubleClick={false}
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable
        disableKeyboardA11y
        nodesFocusable={false}
        edgesFocusable={false}
      >
        <Background variant={BackgroundVariant.Dots} gap={16} size={1} />
        <div data-testid="chain-minimap">
          <MiniMap pannable zoomable />
        </div>
        <Controls />
      </ReactFlow>
      {follow ? (
        <div
          data-testid="chain-follow-status"
          data-follow-paused={followPaused ? "true" : "false"}
          className="absolute left-3 top-3 z-10 flex items-center gap-2 rounded-full border border-border bg-card/95 px-2.5 py-1 text-[11px] font-medium text-muted-foreground shadow-sm"
        >
          <span>
            {followPaused ? "跟随已暂停" : runningNode === null ? "等待执行" : "跟随执行中"}
          </span>
          {followPaused ? (
            <button
              type="button"
              data-testid="chain-follow-resume"
              onClick={resumeFollow}
              className="rounded-full border border-border px-2 py-0.5 text-[10px] font-semibold text-foreground hover:bg-muted"
            >
              恢复跟随
            </button>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}

/**
 * React Flow canvas of the exploration chain (P8 Task 10).
 *
 * Task 11 must load this module through `next/dynamic(..., { ssr: false })`;
 * the component is a Client Component and imports the React Flow stylesheet.
 */
export function ChainCanvas(props: ChainCanvasProps) {
  return (
    <ReactFlowProvider>
      <ChainCanvasFlow {...props} />
    </ReactFlowProvider>
  )
}

export default ChainCanvas
