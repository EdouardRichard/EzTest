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
  useReactFlow,
} from "@xyflow/react"
import "@xyflow/react/dist/style.css"
import {
  createContext,
  memo,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"

import { cn } from "@/lib/utils"

import { ChainKeyboardTree } from "./ChainKeyboardTree"
import type { LaidEdge, PositionedNode } from "./chain-layout"
import { layoutChain } from "./chain-layout"
import { CHAIN_COLOR_STYLES, NODE_KIND_TOKENS } from "./chain-tokens"
import type { ChainNodeKindDTO, ChainSnapshot, ChainSnapshotNode } from "./chain-types"
import {
  ChainEdge,
  type ChainFlowEdge,
  getChainEdgeLabel,
  getChainEdgeMarkerColor,
} from "./edges/ChainEdge"
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
  /** Esc on the keyboard tree; the tab closes the Sheet through this. */
  onEscape?: () => void
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

/** Duration of the viewport move that follows the keyboard-active node. */
const CHAIN_KEYBOARD_CENTER_DURATION_MS = 300
/** Window separating a requirement single click from a collapse double click. */
const CHAIN_REQUIREMENT_CLICK_DELAY_MS = 250

type ChainInteraction = {
  activeId: string | null
  collapsedRequirementIds: ReadonlySet<string>
  toggleableRequirementIds: ReadonlySet<string>
  onToggleCollapse?: (requirementId: string) => void
}

const EMPTY_INTERACTION: ChainInteraction = {
  activeId: null,
  collapsedRequirementIds: new Set(),
  toggleableRequirementIds: new Set(),
}

/**
 * The canvas-level active/collapse state. React Flow owns the node wrapper, so
 * the context is how the visible card learns that the hidden keyboard tree
 * moved and how the requirement card gets an explicit collapse control.
 */
const ChainInteractionContext = createContext<ChainInteraction>(EMPTY_INTERACTION)

function domainIdOf(nodeId: string): string {
  const separator = nodeId.indexOf(":")
  return separator === -1 ? nodeId : nodeId.slice(separator + 1)
}

/**
 * Per-node test/selection hook. React Flow owns the outer `.react-flow__node`
 * element, so the tab's stable `chain-node-<domain id>` selector and the node's
 * data attributes live on this content wrapper instead. It adds no size: the
 * card inside owns the `CHAIN_CARD_SIZES` dimensions.
 *
 * `tabIndex={-1}` makes the wrapper programmatically focusable only: clicking
 * a card focuses it before the Sheet opens, so the Sheet's focus-return goes
 * back to the node the user actually triggered instead of `<body>`. It stays
 * out of the Tab order; keyboard navigation lives in `ChainKeyboardTree`.
 */
function ChainNodeFrame({ data, children }: { data: ChainSnapshotNode; children: ReactNode }) {
  const { activeId } = useContext(ChainInteractionContext)
  const active = activeId === data.id
  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: React Flow owns the real interactive node; this wrapper only moves programmatic focus after a pointer click.
    // biome-ignore lint/a11y/useKeyWithClickEvents: keyboard access is provided by the sibling ChainKeyboardTree, not this pointer-only wrapper.
    <div
      data-testid={`chain-node-${domainIdOf(data.id)}`}
      data-node-id={data.id}
      data-kind={data.kind}
      data-status={data.status}
      data-active={active ? "true" : undefined}
      data-severity={data.kind === "issue" ? (data.severity ?? "") : undefined}
      data-source={data.kind === "case" ? data.source : undefined}
      data-revalidation={
        data.kind === "case" && (data.revalidation === true || data.status === "needs_revalidation")
          ? "true"
          : undefined
      }
      tabIndex={-1}
      className={cn(
        "rounded-2xl focus:outline-none",
        active && "ring-2 ring-ring ring-offset-2 ring-offset-background",
      )}
      onClick={(event) => event.currentTarget.focus()}
    >
      {children}
    </div>
  )
}

/**
 * The four node types are the Task 9 cards plus the invisible anchors. No
 * wrapper adds width, height or padding; the card owns its `CHAIN_CARD_SIZES`
 * dimensions. Selection is injected through `selectedId` (node.selected).
 */
const ChainRequirementNode = memo(function ChainRequirementNode({
  data,
  selected,
}: ChainNodeProps) {
  const interaction = useContext(ChainInteractionContext)
  const showCollapseToggle = interaction.toggleableRequirementIds.has(data.id)
  const toggleCollapse = interaction.onToggleCollapse
  return (
    <ChainNodeFrame data={data}>
      <RequirementNode
        data={data}
        selected={selected}
        collapsed={interaction.collapsedRequirementIds.has(data.id)}
        showCollapseToggle={showCollapseToggle}
        onToggleCollapse={
          showCollapseToggle && toggleCollapse !== undefined
            ? () => toggleCollapse(data.id)
            : undefined
        }
      />
      <ChainNodeAnchors />
    </ChainNodeFrame>
  )
})

const ChainCaseNode = memo(function ChainCaseNode({ data, selected }: ChainNodeProps) {
  return (
    <ChainNodeFrame data={data}>
      <CaseNode data={data} selected={selected} />
      <ChainNodeAnchors />
    </ChainNodeFrame>
  )
})

const ChainResultNode = memo(function ChainResultNode({ data, selected }: ChainNodeProps) {
  return (
    <ChainNodeFrame data={data}>
      <ResultNode data={data} selected={selected} />
      <ChainNodeAnchors />
    </ChainNodeFrame>
  )
})

const ChainIssueNode = memo(function ChainIssueNode({ data, selected }: ChainNodeProps) {
  return (
    <ChainNodeFrame data={data}>
      <IssueNode data={data} selected={selected} />
      <ChainNodeAnchors />
    </ChainNodeFrame>
  )
})

// Module scope keeps the identities stable across renders (React Flow warns
// about fresh nodeTypes/edgeTypes objects on every render).
const NODE_TYPES: NodeTypes = {
  requirement: ChainRequirementNode,
  case: ChainCaseNode,
  result: ChainResultNode,
  issue: ChainIssueNode,
}

const EDGE_TYPES: EdgeTypes = { chain: ChainEdge }

const FIT_VIEW_OPTIONS = { padding: 0.2 } as const
/** Spec §5.4: the canvas may zoom out to 0.1 before the graph stops shrinking. */
const MIN_ZOOM = 0.1

/**
 * MiniMap fill colour per node kind (spec §5.4: minimap nodes are coloured by
 * type). The token palette stays the single source; an unexpected node type
 * falls back to the neutral slate instead of inventing a colour.
 */
function chainMiniMapNodeColor(node: Node): string {
  const kind = typeof node.type === "string" ? node.type : ""
  return Object.hasOwn(NODE_KIND_TOKENS, kind)
    ? NODE_KIND_TOKENS[kind as ChainNodeKindDTO].stroke
    : CHAIN_COLOR_STYLES.slate.stroke
}

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
    const source = nodeById.get(edge.sourceId)
    const target = nodeById.get(edge.targetId)
    const targetStatus = target?.kind === "result" ? target.status : undefined
    const running = targetStatus === "running"
    // React Flow falls back to an English "Edge from X to Y" aria-label; the
    // localised one keeps the canvas free of untranslated screen-reader text.
    const kindLabel = getChainEdgeLabel(edge.kind)
    const sourceTitle = source?.title ?? edge.sourceId
    const targetTitle = target?.title ?? edge.targetId
    return {
      id: edge.id,
      source: edge.sourceId,
      target: edge.targetId,
      type: "chain",
      data: { kind: edge.kind, targetStatus },
      animated: running && !reducedMotion,
      ariaLabel: `${kindLabel === "" ? "链路关系" : kindLabel}：${sourceTitle} → ${targetTitle}`,
      markerEnd: {
        type: MarkerType.ArrowClosed,
        color: getChainEdgeMarkerColor(edge.kind, targetStatus),
      },
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
  onEscape,
}: ChainCanvasProps) {
  const reducedMotion = usePrefersReducedMotion()
  const { setCenter, getZoom } = useReactFlow()

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

  const layoutNodeById = useMemo(
    () => new Map(layout.nodes.map((node) => [node.id, node])),
    [layout.nodes],
  )

  // The hidden tree and the visible cards share one active node. The canvas owns
  // it so arrow-key moves can paint the focus ring on the card, not only on the
  // `sr-only` treeitem.
  const [activeNodeId, setActiveNodeId] = useState<string | null>(() => {
    if (selectedId !== null && layout.nodes.some((node) => node.id === selectedId))
      return selectedId
    return layout.nodes[0]?.id ?? null
  })
  const lastSelectedIdRef = useRef<string | null>(selectedId)

  // Selected nodes (canvas click, list row, deep link) become the active node,
  // but only on an actual selection change: SSE refreshes must not reset where
  // the keyboard user currently is.
  useEffect(() => {
    const nextSelectedId = selectedId ?? null
    if (nextSelectedId === lastSelectedIdRef.current) return
    lastSelectedIdRef.current = nextSelectedId
    if (
      nextSelectedId !== null &&
      nextSelectedId !== "" &&
      layout.nodes.some((node) => node.id === nextSelectedId)
    ) {
      setActiveNodeId(nextSelectedId)
    }
  }, [selectedId, layout.nodes])

  // Collapsing a group can hide the active node; fall back to the selection or
  // the first visible node so the roving tab stop never points at a hidden node.
  useEffect(() => {
    setActiveNodeId((previous) => {
      if (previous !== null && layout.nodes.some((node) => node.id === previous)) return previous
      if (
        selectedId !== null &&
        selectedId !== "" &&
        layout.nodes.some((node) => node.id === selectedId)
      ) {
        return selectedId
      }
      return layout.nodes[0]?.id ?? null
    })
  }, [layout.nodes, selectedId])

  const interactionState = useMemo(() => {
    const collapsedRequirementIds = new Set<string>()
    const toggleableRequirementIds = new Set<string>()
    for (const node of layout.nodes) {
      if (node.kind !== "requirement") continue
      if (node.collapsed) collapsedRequirementIds.add(node.id)
      if (node.hasChildren) toggleableRequirementIds.add(node.id)
    }
    return { collapsedRequirementIds, toggleableRequirementIds }
  }, [layout.nodes])

  const interaction = useMemo<ChainInteraction>(
    () => ({ activeId: activeNodeId, ...interactionState, onToggleCollapse }),
    [activeNodeId, interactionState, onToggleCollapse],
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

  const handleActiveChange = useCallback((nodeId: string): void => {
    setActiveNodeId(nodeId)
  }, [])

  const handleActiveNavigate = useCallback(
    (nodeId: string): void => {
      const target = layoutNodeById.get(nodeId)
      if (target === undefined) return
      // React Flow culls off-screen nodes; centring the moved node both reveals
      // it and gives the visible focus ring a stable screen position.
      void setCenter(target.x + target.width / 2, target.y + target.height / 2, {
        zoom: getZoom(),
        duration: reducedMotion ? 0 : CHAIN_KEYBOARD_CENTER_DURATION_MS,
      })
    },
    [layoutNodeById, setCenter, getZoom, reducedMotion],
  )

  const pendingSelectRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const clearPendingSelect = useCallback((): void => {
    if (pendingSelectRef.current === null) return
    clearTimeout(pendingSelectRef.current)
    pendingSelectRef.current = null
  }, [])
  useEffect(() => clearPendingSelect, [clearPendingSelect])

  const handleNodeClick = useCallback<NodeMouseHandler<ChainFlowNode>>(
    (_event, node) => {
      if (node.type !== "requirement" || onToggleCollapse === undefined) {
        // A pending requirement click must not fire later and steal the Sheet
        // after the user already opened another node.
        clearPendingSelect()
        onSelect?.(node.id)
        return
      }
      // A requirement single click may be the first half of a collapse double
      // click. Delay the Sheet just long enough for the second click to cancel
      // it, so the pointer collapse is not swallowed by the Sheet overlay.
      clearPendingSelect()
      pendingSelectRef.current = setTimeout(() => {
        pendingSelectRef.current = null
        onSelect?.(node.id)
      }, CHAIN_REQUIREMENT_CLICK_DELAY_MS)
    },
    [onSelect, onToggleCollapse, clearPendingSelect],
  )

  const handleKeyboardSelect = useCallback(
    (nodeId: string): void => {
      onSelect?.(nodeId)
    },
    [onSelect],
  )

  const handleNodeDoubleClick = useCallback<NodeMouseHandler<ChainFlowNode>>(
    (_event, node) => {
      if (node.type !== "requirement") return
      clearPendingSelect()
      onToggleCollapse?.(node.id)
    },
    [onToggleCollapse, clearPendingSelect],
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
    <ChainInteractionContext.Provider value={interaction}>
      <div
        data-testid="chain-canvas"
        className="relative h-full w-full overflow-hidden rounded-xl border border-border bg-background"
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
          minZoom={MIN_ZOOM}
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
            <MiniMap pannable zoomable nodeColor={chainMiniMapNodeColor} />
          </div>
          {/* No interactive lock: it would turn dragging/connecting/selection back on. */}
          <Controls showInteractive={false} />
        </ReactFlow>
        {/* The canvas is visual-only: this tree is the keyboard/screen-reader
            contract (roving focus + ARIA levels), while React Flow keeps
            nodesFocusable/edgesFocusable=false. The active id is controlled by
            the canvas so the visible card can carry the same focus ring. */}
        <ChainKeyboardTree
          nodes={layout.nodes}
          selectedId={selectedId}
          activeId={activeNodeId}
          onActiveChange={handleActiveChange}
          onActiveNavigate={handleActiveNavigate}
          onSelect={handleKeyboardSelect}
          onToggleCollapse={onToggleCollapse}
          onEscape={onEscape}
        />
        {follow ? (
          <div
            data-testid="chain-follow-slot"
            className="pointer-events-none absolute inset-x-0 bottom-3 z-20 flex justify-center px-3"
          >
            <div
              data-testid="chain-follow-status"
              data-follow-paused={followPaused ? "true" : "false"}
              className="pointer-events-auto flex items-center gap-2 rounded-full border border-border bg-card/95 px-2.5 py-1 text-[11px] font-medium text-muted-foreground shadow-sm"
            >
              <span>
                {followPaused ? "跟随已暂停" : runningNode === null ? "等待执行" : "跟随执行中"}
              </span>
              {followPaused ? (
                <button
                  type="button"
                  data-testid="chain-follow-resume"
                  onClick={resumeFollow}
                  className="rounded-full border border-border px-2 py-0.5 text-[10px] font-semibold text-foreground hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  恢复跟随
                </button>
              ) : null}
            </div>
          </div>
        ) : null}
      </div>
    </ChainInteractionContext.Provider>
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
