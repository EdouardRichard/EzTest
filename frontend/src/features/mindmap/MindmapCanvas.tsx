"use client"

import {
  type KeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"

import { layoutMindmap } from "./mindmap-layout"
import type {
  CaseNodeKind,
  CaseNodeStatus,
  CaseNodeView,
  CaseSourceKind,
  MindmapGraph,
} from "./types"

/** Zoom range enforced by both the buttons and the wheel handler (spec §9.2). */
export const MIN_ZOOM = 0.5
export const MAX_ZOOM = 2
export const ZOOM_STEP = 1.25

/** Empty space around the laid-out cards inside the viewBox. */
const VIEW_PADDING = 64
const MIN_VIEW_WIDTH = 480
const MIN_VIEW_HEIGHT = 320

const BUTTON_CLASS =
  "inline-flex h-8 min-w-8 items-center justify-center rounded-lg border border-border bg-card px-2.5 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

const KIND_LABEL: Record<CaseNodeKind, string> = {
  scenario: "场景",
  case: "用例",
}

const SOURCE_LABEL: Record<CaseSourceKind, string> = {
  user_input: "人工",
  ai_generated: "AI",
  ai_supplement: "AI 补充",
}

const STATUS_LABEL: Record<CaseNodeStatus, string> = {
  draft: "草稿",
  reviewed: "已评审",
  executed: "已执行",
  supplemented: "已补充",
  merged: "已合并",
  rejected: "已驳回",
  needs_revalidation: "需重验",
}

function clampZoom(value: number): number {
  return Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, value))
}

/** Card titles are clipped to keep the SVG text inside the fixed-width card. */
function clipTitle(title: string, maxRunes: number): string {
  const runes = Array.from(title)
  if (runes.length <= maxRunes) return title
  return `${runes.slice(0, maxRunes).join("")}…`
}

function nodeLabel(node: CaseNodeView, collapsed: boolean, descendantCount: number): string {
  const parts = [
    `${KIND_LABEL[node.kind]}：${node.title}`,
    node.priority,
    STATUS_LABEL[node.status],
  ]
  if (node.openBlockerCount > 0) parts.push(`blocker ×${node.openBlockerCount}`)
  if (collapsed) parts.push(`${descendantCount} 项已折叠`)
  return parts.join("，")
}

type DragState = {
  startX: number
  startY: number
  panX: number
  panY: number
  /** Layout units per screen pixel at the moment the drag started. */
  scale: number
}

export type MindmapCanvasProps = {
  /** Server-authoritative graph snapshot; the canvas never mutates it. */
  graph: MindmapGraph
  selectedId?: string | null
  onSelect: (nodeId: string) => void
  /** Scenario ids whose descendants are hidden. */
  collapsed?: readonly string[] | ReadonlySet<string>
  onToggle?: (nodeId: string) => void
}

/**
 * Case mindmap canvas (spec §9.2/§9.3): a hand-rolled SVG tidy-tree with
 * deterministic layout, pan/zoom, collapse/expand and roving-focus keyboard
 * navigation. Collapsing and selecting stay local concerns; editing lives in
 * the side panels owned by the page, so the canvas is safe in frozen mode too.
 */
export function MindmapCanvas({
  graph,
  selectedId = null,
  onSelect,
  collapsed,
  onToggle,
}: MindmapCanvasProps) {
  const collapsedSet = useMemo(
    () => new Set<string>((collapsed ?? []) as Iterable<string>),
    [collapsed],
  )
  const layout = useMemo(
    () => layoutMindmap(graph.nodes, { collapsed: collapsedSet }),
    [graph.nodes, collapsedSet],
  )

  const visibleSet = useMemo(() => new Set(layout.visibleIds), [layout.visibleIds])
  const layoutById = useMemo(
    () => new Map(layout.nodes.map((node) => [node.id, node])),
    [layout.nodes],
  )
  /** First child in pre-order; layout order already matches sibling order. */
  const firstChildOf = useMemo(() => {
    const firstChild = new Map<string, string>()
    for (const node of layout.nodes) {
      const parentId = node.node.parentId
      if (parentId !== null && !firstChild.has(parentId)) firstChild.set(parentId, node.id)
    }
    return firstChild
  }, [layout.nodes])
  const impactedCaseIds = useMemo(
    () => new Set((graph.impactCases ?? []).map((impact) => impact.caseNodeId)),
    [graph.impactCases],
  )

  const [focusedId, setFocusedId] = useState<string | null>(null)
  const [zoom, setZoom] = useState(1)
  const [pan, setPan] = useState({ x: 0, y: 0 })

  const itemRefs = useRef(new Map<string, SVGGElement>())
  const viewportRef = useRef<HTMLDivElement | null>(null)
  const dragRef = useRef<DragState | null>(null)
  /** Focus moves after commit so the roving tabindex and DOM focus stay in sync. */
  const pendingFocusRef = useRef<string | null>(null)

  const activeId = useMemo(() => {
    if (focusedId !== null && visibleSet.has(focusedId)) return focusedId
    if (selectedId !== null && visibleSet.has(selectedId)) return selectedId
    return layout.visibleIds[0] ?? null
  }, [focusedId, selectedId, visibleSet, layout.visibleIds])

  // A new version is a new canvas: drop stale zoom/pan.
  // biome-ignore lint/correctness/useExhaustiveDependencies: the version id is not read inside, but a version switch must reset the viewport.
  useEffect(() => {
    setZoom(1)
    setPan({ x: 0, y: 0 })
  }, [graph.versionId])

  const zoomBy = useCallback((factor: number): void => {
    setZoom((current) => clampZoom(current * factor))
  }, [])

  // Native listener so wheel zoom can prevent page scrolling (spec §9.2).
  useEffect(() => {
    const element = viewportRef.current
    if (element === null) return
    const handleWheel = (event: WheelEvent): void => {
      event.preventDefault()
      zoomBy(event.deltaY < 0 ? ZOOM_STEP : 1 / ZOOM_STEP)
    }
    element.addEventListener("wheel", handleWheel, { passive: false })
    return () => element.removeEventListener("wheel", handleWheel)
  }, [zoomBy])

  const bounds = layout.bounds
  const viewBox = useMemo(() => {
    if (layout.nodes.length === 0) {
      return { x: 0, y: 0, width: MIN_VIEW_WIDTH, height: MIN_VIEW_HEIGHT }
    }
    return {
      x: bounds.x - VIEW_PADDING,
      y: bounds.y - VIEW_PADDING,
      width: Math.max(bounds.width + VIEW_PADDING * 2, MIN_VIEW_WIDTH),
      height: Math.max(bounds.height + VIEW_PADDING * 2, MIN_VIEW_HEIGHT),
    }
  }, [layout.nodes.length, bounds])

  const centerX = layout.nodes.length === 0 ? viewBox.width / 2 : bounds.x + bounds.width / 2
  const centerY = layout.nodes.length === 0 ? viewBox.height / 2 : bounds.y + bounds.height / 2
  const contentTransform = `translate(${pan.x} ${pan.y}) translate(${centerX} ${centerY}) scale(${zoom}) translate(${-centerX} ${-centerY})`

  const focusNode = useCallback((nodeId: string): void => {
    const element = itemRefs.current.get(nodeId)
    if (element === undefined) return
    const focus = (element as SVGGElement & { focus?: () => void }).focus
    if (typeof focus === "function") focus.call(element)
  }, [])

  // Roving focus: after the active node changes, move DOM focus to it so the
  // next Tab/arrow keystroke lands inside the tree (spec §9.2).
  useEffect(() => {
    if (activeId === null) return
    if (pendingFocusRef.current !== activeId) return
    pendingFocusRef.current = null
    focusNode(activeId)
  }, [activeId, focusNode])

  function activate(nodeId: string): void {
    pendingFocusRef.current = nodeId
    setFocusedId(nodeId)
  }

  function handleKeyDown(event: KeyboardEvent<SVGSVGElement>): void {
    if (activeId === null) return
    const active = layoutById.get(activeId)
    if (active === undefined) return
    const index = layout.visibleIds.indexOf(activeId)
    let nextId: string | null = null
    let handled = true

    switch (event.key) {
      case "ArrowDown":
        nextId = layout.visibleIds[Math.min(index + 1, layout.visibleIds.length - 1)] ?? null
        break
      case "ArrowUp":
        nextId = layout.visibleIds[Math.max(index - 1, 0)] ?? null
        break
      case "ArrowRight":
        if (active.hasChildren) {
          if (collapsedSet.has(activeId)) onToggle?.(activeId)
          else nextId = firstChildOf.get(activeId) ?? null
        }
        break
      case "ArrowLeft":
        if (active.hasChildren && !collapsedSet.has(activeId)) {
          onToggle?.(activeId)
        } else {
          const parentId = active.node.parentId
          if (parentId !== null && visibleSet.has(parentId)) nextId = parentId
        }
        break
      case "Home":
        nextId = layout.visibleIds[0] ?? null
        break
      case "End":
        nextId = layout.visibleIds[layout.visibleIds.length - 1] ?? null
        break
      case "Enter":
        event.preventDefault()
        onSelect(activeId)
        return
      default:
        handled = false
    }

    if (!handled) return
    event.preventDefault()
    if (nextId !== null && nextId !== activeId) activate(nextId)
  }

  function handlePointerDown(event: ReactPointerEvent<HTMLDivElement>): void {
    if (event.button !== 0 && event.pointerType !== "touch") return
    const target = event.target as Element | null
    if (target !== null && typeof target.closest === "function") {
      if (target.closest("[role='treeitem']") !== null) return
    }
    const element = viewportRef.current
    const scale =
      element !== null && element.clientWidth > 0 ? viewBox.width / element.clientWidth : 1
    dragRef.current = {
      startX: event.clientX,
      startY: event.clientY,
      panX: pan.x,
      panY: pan.y,
      scale,
    }
    const capture = event.currentTarget.setPointerCapture
    if (typeof capture === "function") {
      try {
        capture.call(event.currentTarget, event.pointerId)
      } catch {
        // jsdom has no pointer capture; dragging still works without it.
      }
    }
  }

  function handlePointerMove(event: ReactPointerEvent<HTMLDivElement>): void {
    const drag = dragRef.current
    if (drag === null) return
    setPan({
      x: drag.panX + (event.clientX - drag.startX) * drag.scale,
      y: drag.panY + (event.clientY - drag.startY) * drag.scale,
    })
  }

  function endDrag(): void {
    dragRef.current = null
  }

  const visibleCount = layout.visibleIds.length
  const canvasLabel = `用例脑图：${visibleCount} 个可见节点${
    layout.hiddenIds.length > 0 ? `，${layout.hiddenIds.length} 个已折叠` : ""
  }`

  return (
    <section
      data-testid="mindmap-canvas"
      data-zoom={zoom}
      aria-label="用例脑图"
      className="flex min-h-0 min-w-0 flex-col gap-2 rounded-2xl border border-border bg-card p-3"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-xs text-muted-foreground">
          拖拽平移 · 滚轮缩放（0.5–2）· ↑↓←→ 移动 · Enter 选中
        </p>
        <div className="flex items-center gap-1.5">
          <button
            type="button"
            data-testid="mindmap-zoom-out"
            aria-label="缩小画布"
            className={BUTTON_CLASS}
            onClick={() => zoomBy(1 / ZOOM_STEP)}
          >
            −
          </button>
          <span
            data-testid="mindmap-zoom-level"
            aria-live="polite"
            className="w-12 text-center text-xs tabular-nums text-muted-foreground"
          >
            {`${Math.round(zoom * 100)}%`}
          </span>
          <button
            type="button"
            data-testid="mindmap-zoom-in"
            aria-label="放大画布"
            className={BUTTON_CLASS}
            onClick={() => zoomBy(ZOOM_STEP)}
          >
            +
          </button>
          <button
            type="button"
            data-testid="mindmap-fit"
            aria-label="适应画布"
            className={BUTTON_CLASS}
            onClick={() => {
              setZoom(1)
              setPan({ x: 0, y: 0 })
            }}
          >
            适应
          </button>
        </div>
      </div>

      <div
        ref={viewportRef}
        data-testid="mindmap-viewport"
        className="relative h-[70vh] min-h-[420px] w-full touch-none overflow-hidden rounded-xl border border-border bg-muted/20"
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
        onPointerUp={endDrag}
        onPointerCancel={endDrag}
        onPointerLeave={endDrag}
      >
        {visibleCount === 0 ? (
          <p className="flex h-full items-center justify-center px-4 text-center text-sm text-muted-foreground">
            暂无脑图节点，先生成或新建用例。
          </p>
        ) : (
          <svg
            // biome-ignore lint/a11y/noNoninteractiveElementToInteractiveRole: SVG has no native tree container; the WAI-ARIA tree pattern is the accessible equivalent.
            role="tree"
            aria-label={canvasLabel}
            data-zoom={zoom}
            tabIndex={-1}
            viewBox={`${viewBox.x} ${viewBox.y} ${viewBox.width} ${viewBox.height}`}
            className="h-full w-full cursor-grab select-none outline-none active:cursor-grabbing"
            onKeyDown={handleKeyDown}
          >
            <g transform={contentTransform}>
              {layout.edges.map((edge) => {
                const parent = layoutById.get(edge.parentId)
                const child = layoutById.get(edge.childId)
                if (parent === undefined || child === undefined) return null
                const startX = parent.x + parent.w / 2
                const startY = parent.y + parent.h
                const endX = child.x + child.w / 2
                const endY = child.y
                const midY = (startY + endY) / 2
                return (
                  <path
                    key={edge.id}
                    data-testid={`mindmap-edge-${edge.id}`}
                    d={`M ${startX} ${startY} C ${startX} ${midY}, ${endX} ${midY}, ${endX} ${endY}`}
                    fill="none"
                    stroke="var(--border, #cbd5e1)"
                    strokeWidth={1.5}
                  />
                )
              })}

              {layout.nodes.map((node) => {
                const data = node.node
                const isCollapsed = collapsedSet.has(node.id)
                const isSelected = selectedId === node.id
                const isActive = activeId === node.id
                const impacted = impactedCaseIds.has(node.id)
                const kindLabel = KIND_LABEL[data.kind]
                const sourceLabel = SOURCE_LABEL[data.source]
                const statusLabel = STATUS_LABEL[data.status]
                const blockerLabel =
                  data.openBlockerCount > 0 ? `blocker ×${data.openBlockerCount}` : ""
                const impactLabel =
                  data.kind === "case" ? (impacted ? "可能受影响" : "未受影响") : ""
                const footer = [blockerLabel, impactLabel].filter((part) => part !== "").join(" · ")

                return (
                  <g
                    key={node.id}
                    ref={(element) => {
                      if (element === null) itemRefs.current.delete(node.id)
                      else itemRefs.current.set(node.id, element)
                    }}
                    data-testid={
                      data.kind === "scenario" ? `scenario-node-${node.id}` : `case-node-${node.id}`
                    }
                    data-active={isActive ? "true" : undefined}
                    role="treeitem"
                    aria-level={node.depth + 1}
                    aria-selected={isSelected}
                    aria-expanded={node.hasChildren ? !isCollapsed : undefined}
                    aria-label={nodeLabel(data, isCollapsed, node.descendantCount)}
                    tabIndex={isActive ? 0 : -1}
                    className="group cursor-pointer focus-visible:outline-none"
                    onClick={() => {
                      setFocusedId(node.id)
                      focusNode(node.id)
                      onSelect(node.id)
                    }}
                  >
                    <title>{`${kindLabel}：${data.title}（${data.priority}，${statusLabel}）`}</title>
                    <rect
                      x={node.x}
                      y={node.y}
                      width={node.w}
                      height={node.h}
                      rx={12}
                      fill="var(--card, #ffffff)"
                      stroke={isSelected ? "var(--ring, #3b82f6)" : "var(--border, #cbd5e1)"}
                      strokeWidth={isSelected ? 2.5 : 1.5}
                      strokeDasharray={data.kind === "scenario" ? "6 4" : undefined}
                      className="group-focus-visible:stroke-ring group-focus-visible:[stroke-width:2.5px]"
                    />
                    {isSelected ? (
                      <rect
                        x={node.x}
                        y={node.y + 12}
                        width={4}
                        height={node.h - 24}
                        rx={2}
                        fill="var(--primary, #2563eb)"
                      />
                    ) : null}

                    <text
                      x={node.x + 14}
                      y={node.y + 24}
                      fontSize={13}
                      fontWeight={600}
                      fill="var(--card-foreground, #0f172a)"
                    >
                      {clipTitle(data.title, 13)}
                    </text>

                    <text
                      x={node.x + 14}
                      y={node.y + 46}
                      fontSize={10.5}
                      fill="var(--muted-foreground, #5b6779)"
                    >
                      {`${kindLabel} · ${sourceLabel}`}
                    </text>
                    <text
                      x={node.x + node.w - 14}
                      y={node.y + 46}
                      fontSize={10.5}
                      textAnchor="end"
                      fill="var(--muted-foreground, #5b6779)"
                    >
                      {`${data.priority} · ${statusLabel}`}
                    </text>

                    {footer === "" ? null : (
                      <text
                        x={node.x + 14}
                        y={node.y + 70}
                        fontSize={10}
                        fill={
                          data.openBlockerCount > 0
                            ? "var(--destructive, #dc2626)"
                            : "var(--muted-foreground, #5b6779)"
                        }
                      >
                        {footer}
                      </text>
                    )}
                    {isCollapsed ? (
                      <text
                        x={node.x + node.w - 14}
                        y={node.y + 70}
                        fontSize={10}
                        textAnchor="end"
                        fill="var(--muted-foreground, #5b6779)"
                      >
                        {`已折叠 ${node.descendantCount}`}
                      </text>
                    ) : null}

                    {node.hasChildren ? (
                      // biome-ignore lint/a11y/useSemanticElements: SVG cannot host a native button; the ARIA button role carries the toggle.
                      <g
                        role="button"
                        tabIndex={-1}
                        data-testid={`mindmap-toggle-${node.id}`}
                        aria-label={`${isCollapsed ? "展开" : "折叠"} ${data.title}`}
                        className="cursor-pointer focus-visible:outline-none"
                        onClick={(event) => {
                          event.stopPropagation()
                          setFocusedId(node.id)
                          onToggle?.(node.id)
                        }}
                      >
                        <circle
                          cx={node.x + node.w - 20}
                          cy={node.y + 20}
                          r={9}
                          fill="var(--muted, #eef2f7)"
                          stroke="var(--border, #cbd5e1)"
                        />
                        <text
                          x={node.x + node.w - 20}
                          y={node.y + 24}
                          fontSize={12}
                          textAnchor="middle"
                          fill="var(--foreground, #0f172a)"
                        >
                          {isCollapsed ? "+" : "−"}
                        </text>
                      </g>
                    ) : null}
                  </g>
                )
              })}
            </g>
          </svg>
        )}
      </div>
    </section>
  )
}
