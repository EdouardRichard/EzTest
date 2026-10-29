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

import type { ChainNode, LegacyChainLayoutResult } from "../chain/chain-layout"
import type { CaseRunStatus } from "../types"

/** Zoom range enforced by the buttons and the wheel handler (spec §9.3). */
export const CHAIN_MIN_ZOOM = 0.5
export const CHAIN_MAX_ZOOM = 2
export const CHAIN_ZOOM_STEP = 1.25

/** Empty space around the laid-out nodes inside the viewBox. */
const VIEW_PADDING = 64
const MIN_VIEW_WIDTH = 480
const MIN_VIEW_HEIGHT = 320

const BUTTON_CLASS =
  "inline-flex h-8 min-w-8 items-center justify-center rounded-lg border border-border bg-card px-2.5 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

type ResultMeta = { label: string; glyph: string; color: string }

const RESULT_STATUS_META: Record<CaseRunStatus, ResultMeta> = {
  pending: { label: "待执行", glyph: "…", color: "#3b82f6" },
  running: { label: "执行中", glyph: "▶", color: "#3b82f6" },
  passed: { label: "通过", glyph: "✓", color: "#10b981" },
  failed: { label: "失败", glyph: "✕", color: "#ef4444" },
  blocked: { label: "阻塞", glyph: "!", color: "#64748b" },
  inconclusive: { label: "不确定", glyph: "?", color: "#f59e0b" },
  skipped: { label: "跳过", glyph: "–", color: "#94a3b8" },
  interrupted: { label: "中断", glyph: "■", color: "#f97316" },
}

const CASE_STATUS_LABELS: Record<string, string> = {
  draft: "草稿",
  reviewed: "已评审",
  executed: "已执行",
  supplemented: "已补充",
  merged: "已合并",
  rejected: "已驳回",
  needs_revalidation: "需重验",
}

const REQUIREMENT_STATUS_LABELS: Record<string, string> = {
  draft: "草稿",
  clarified: "已澄清",
  designed: "已设计",
  verified: "已验证",
  obsolete: "已废弃",
}

const ISSUE_STATUS_LABELS: Record<string, string> = {
  open: "待确认",
  confirmed: "已确认",
  dismissed: "已驳回",
}

const ISSUE_SEVERITY_LABELS: Record<string, string> = {
  blocker: "阻断",
  major: "严重",
  minor: "一般",
  info: "提示",
}

/** Red used by the issue diamond (meaning is also carried by the "!" glyph). */
const ISSUE_COLOR = "#ef4444"
/** Amber used by the needs_revalidation border and badge (spec §10.5). */
const REVALIDATION_COLOR = "#f59e0b"

function issueStatusLabel(status: string): string {
  return ISSUE_STATUS_LABELS[status] ?? (status === "" ? "—" : status)
}

function issueSeverityLabel(severity: string): string {
  return ISSUE_SEVERITY_LABELS[severity] ?? (severity === "" ? "—" : severity)
}

function resultMeta(status: string): ResultMeta {
  return (
    RESULT_STATUS_META[status as CaseRunStatus] ?? {
      label: status === "" ? "未知" : status,
      glyph: "•",
      color: "#94a3b8",
    }
  )
}

function caseStatusLabel(status: string): string {
  return CASE_STATUS_LABELS[status] ?? (status === "" ? "—" : status)
}

function requirementStatusLabel(status: string): string {
  return REQUIREMENT_STATUS_LABELS[status] ?? (status === "" ? "—" : status)
}

function clampZoom(value: number): number {
  return Math.min(CHAIN_MAX_ZOOM, Math.max(CHAIN_MIN_ZOOM, value))
}

/** Card titles are clipped to keep the SVG text inside the fixed-width card. */
function clipTitle(title: string, maxRunes: number): string {
  const runes = Array.from(title)
  if (runes.length <= maxRunes) return title
  return `${runes.slice(0, maxRunes).join("")}…`
}

function formatDuration(durationMs: number | undefined): string {
  if (durationMs === undefined || durationMs <= 0) return ""
  if (durationMs < 1000) return `${durationMs} ms`
  return `${(durationMs / 1000).toFixed(1)} s`
}

function statusLabelOf(node: ChainNode): string {
  if (node.kind === "result") return resultMeta(node.status).label
  if (node.kind === "case") return caseStatusLabel(node.status)
  if (node.kind === "issue") return issueStatusLabel(node.status)
  return requirementStatusLabel(node.status)
}

function nodeAriaLabel(node: ChainNode): string {
  const status = statusLabelOf(node)
  if (node.kind === "requirement") {
    const parts = [`需求：${node.title}`, status]
    if (node.hasChildren) parts.push(`${node.descendantCount} 个下游节点`)
    if (node.collapsed) parts.push("已折叠")
    return parts.join("，")
  }
  if (node.kind === "case") {
    const parts = [`用例：${node.title}`, status]
    if (node.case?.aiSupplemented === true) parts.push("AI 补充")
    if (node.status === "needs_revalidation") parts.push("需重验")
    return parts.join("，")
  }
  if (node.kind === "issue") {
    return [
      `问题：${node.title}`,
      `严重级 ${issueSeverityLabel(node.issue?.severity ?? "")}`,
      status,
    ].join("，")
  }
  const parts = [`结果：${node.title}`, status]
  const duration = formatDuration(node.result?.durationMs)
  if (duration !== "") parts.push(`耗时 ${duration}`)
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

export type ChainGraphProps = {
  /** Positioned nodes/edges produced by the temporary `layoutLegacy` adapter. */
  layout: LegacyChainLayoutResult
  selectedId: string | null
  onSelect: (node: ChainNode) => void
  onToggleRequirement: (requirementId: string) => void
}

/**
 * Hand-written SVG canvas of the exploration chain (spec §9.3): three layers
 * (requirement → case → result), status colour plus glyph/text, per-requirement
 * collapse, zoom/pan/fit and ARIA tree semantics. No graph library is used and
 * the component never fetches data; the tab owns data, filters and the drawer.
 */
export function ChainGraph({ layout, selectedId, onSelect, onToggleRequirement }: ChainGraphProps) {
  const [focusedId, setFocusedId] = useState<string | null>(null)
  const [zoom, setZoom] = useState(1)
  const [pan, setPan] = useState({ x: 0, y: 0 })

  const viewportRef = useRef<HTMLDivElement | null>(null)
  const itemRefs = useRef(new Map<string, SVGGElement>())
  const dragRef = useRef<DragState | null>(null)
  const pendingFocusRef = useRef<string | null>(null)

  const layoutById = useMemo(
    () => new Map(layout.nodes.map((node) => [node.id, node])),
    [layout.nodes],
  )

  const activeId = useMemo(() => {
    if (focusedId !== null && layoutById.has(focusedId)) return focusedId
    if (selectedId !== null && layoutById.has(selectedId)) return selectedId
    return layout.visibleIds[0] ?? null
  }, [focusedId, selectedId, layoutById, layout.visibleIds])

  const zoomBy = useCallback((factor: number): void => {
    setZoom((current) => clampZoom(current * factor))
  }, [])

  // Native listener so wheel zoom can prevent page scrolling.
  useEffect(() => {
    const element = viewportRef.current
    if (element === null) return
    const handleWheel = (event: WheelEvent): void => {
      event.preventDefault()
      zoomBy(event.deltaY < 0 ? CHAIN_ZOOM_STEP : 1 / CHAIN_ZOOM_STEP)
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

  // Roving focus: after the active node changes, move DOM focus to it.
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
        if (active.kind === "requirement" && active.hasChildren) {
          if (active.collapsed) onToggleRequirement(active.id)
          else {
            const child = layout.nodes.find(
              (node) => node.kind === "case" && node.requirementIds.includes(active.id),
            )
            nextId = child?.id ?? null
          }
        }
        break
      case "ArrowLeft":
        if (active.kind === "requirement" && active.hasChildren && !active.collapsed) {
          onToggleRequirement(active.id)
        } else {
          const parentId = active.requirementIds[0]
          if (parentId !== undefined && layoutById.has(parentId)) nextId = parentId
        }
        break
      case "Home":
        nextId = layout.visibleIds[0] ?? null
        break
      case "End":
        nextId = layout.visibleIds[layout.visibleIds.length - 1] ?? null
        break
      case "Enter":
      case " ":
        event.preventDefault()
        onSelect(active)
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
  const canvasLabel = `探索链路：${visibleCount} 个可见节点${
    layout.hiddenIds.length > 0 ? `，${layout.hiddenIds.length} 个已折叠` : ""
  }`

  return (
    <section
      data-testid="chain-graph"
      data-zoom={zoom}
      aria-label="探索链路图"
      className="flex min-h-0 min-w-0 flex-col gap-2 rounded-2xl border border-border bg-card p-3"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-xs text-muted-foreground">
          拖拽平移 · 滚轮缩放（0.5–2）· 方向键移动 · Enter 打开节点
        </p>
        <div className="flex items-center gap-1.5">
          <button
            type="button"
            data-testid="chain-zoom-out"
            aria-label="缩小链路图"
            className={BUTTON_CLASS}
            onClick={() => zoomBy(1 / CHAIN_ZOOM_STEP)}
          >
            −
          </button>
          <span
            data-testid="chain-zoom-level"
            aria-live="polite"
            className="w-12 text-center text-xs tabular-nums text-muted-foreground"
          >
            {`${Math.round(zoom * 100)}%`}
          </span>
          <button
            type="button"
            data-testid="chain-zoom-in"
            aria-label="放大链路图"
            className={BUTTON_CLASS}
            onClick={() => zoomBy(CHAIN_ZOOM_STEP)}
          >
            +
          </button>
          <button
            type="button"
            data-testid="chain-fit"
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
        data-testid="chain-viewport"
        className="relative max-h-[70vh] min-h-[420px] w-full touch-none overflow-auto rounded-xl border border-border bg-muted/20"
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
        onPointerUp={endDrag}
        onPointerCancel={endDrag}
        onPointerLeave={endDrag}
      >
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
              const source = layoutById.get(edge.sourceId)
              const target = layoutById.get(edge.targetId)
              if (source === undefined || target === undefined) return null
              const startX = source.x + source.w
              const startY = source.y + source.h / 2
              const endX = target.x
              const endY = target.y + target.h / 2
              const midX = (startX + endX) / 2
              const stroke =
                edge.kind === "issue-link"
                  ? ISSUE_COLOR
                  : edge.kind === "case-result" && target.kind === "result"
                    ? resultMeta(target.status).color
                    : "var(--border, #cbd5e1)"
              const dashed = edge.kind === "requirement-case" || edge.kind === "issue-link"
              return (
                <path
                  key={edge.id}
                  data-testid={`chain-edge-${edge.id}`}
                  data-kind={edge.kind}
                  d={`M ${startX} ${startY} C ${midX} ${startY}, ${midX} ${endY}, ${endX} ${endY}`}
                  fill="none"
                  stroke={stroke}
                  strokeWidth={1.5}
                  strokeDasharray={dashed ? "6 4" : undefined}
                  opacity={0.9}
                />
              )
            })}

            {layout.nodes.map((node) => {
              const isSelected = selectedId === node.id
              const isActive = activeId === node.id
              const label = statusLabelOf(node)
              const statusColor = node.kind === "result" ? resultMeta(node.status).color : ""

              return (
                <g
                  key={node.id}
                  ref={(element) => {
                    if (element === null) itemRefs.current.delete(node.id)
                    else itemRefs.current.set(node.id, element)
                  }}
                  data-testid={
                    node.kind === "issue" ? `chain-issue-node-${node.id}` : `chain-node-${node.id}`
                  }
                  data-kind={node.kind}
                  data-status={node.status}
                  data-severity={node.kind === "issue" ? node.issue?.severity : undefined}
                  data-revalidation={node.status === "needs_revalidation" ? "true" : undefined}
                  data-active={isActive ? "true" : undefined}
                  role="treeitem"
                  aria-level={node.layer + 1}
                  aria-selected={isSelected}
                  aria-expanded={
                    node.kind === "requirement" && node.hasChildren ? !node.collapsed : undefined
                  }
                  aria-label={nodeAriaLabel(node)}
                  tabIndex={isActive ? 0 : -1}
                  className="group cursor-pointer focus-visible:outline-none"
                  onClick={() => {
                    setFocusedId(node.id)
                    focusNode(node.id)
                    onSelect(node)
                  }}
                >
                  <title>{nodeAriaLabel(node)}</title>

                  {node.kind === "requirement" ? (
                    <>
                      <rect
                        x={node.x}
                        y={node.y}
                        width={node.w}
                        height={node.h}
                        rx={6}
                        fill="var(--card, #ffffff)"
                        stroke="var(--primary, #2563eb)"
                        strokeWidth={2.5}
                        strokeDasharray={node.collapsed ? "6 4" : undefined}
                        className="group-focus-visible:[stroke-width:3.5px]"
                      />
                      <text
                        x={node.x + 14}
                        y={node.y + 25}
                        fontSize={13}
                        fontWeight={700}
                        fill="var(--card-foreground, #0f172a)"
                      >
                        {clipTitle(node.title, 14)}
                      </text>
                      <text
                        x={node.x + 14}
                        y={node.y + 47}
                        fontSize={10.5}
                        fill="var(--muted-foreground, #5b6779)"
                      >
                        {node.collapsed
                          ? `已折叠 · ${node.descendantCount} 项`
                          : `需求 · ${requirementStatusLabel(node.status)}${
                              node.hasChildren ? ` · ${node.descendantCount} 项` : ""
                            }`}
                      </text>
                    </>
                  ) : null}

                  {node.kind === "case" ? (
                    <>
                      <rect
                        x={node.x}
                        y={node.y}
                        width={node.w}
                        height={node.h}
                        rx={10}
                        fill="var(--card, #ffffff)"
                        stroke={
                          node.status === "needs_revalidation"
                            ? REVALIDATION_COLOR
                            : "var(--border, #cbd5e1)"
                        }
                        strokeWidth={node.status === "needs_revalidation" ? 2 : 1.5}
                        strokeDasharray={node.case?.aiSupplemented === true ? "6 4" : undefined}
                        className="group-focus-visible:[stroke-width:2.5px]"
                      />
                      <text
                        x={node.x + 14}
                        y={node.y + 26}
                        fontSize={12.5}
                        fontWeight={600}
                        fill="var(--card-foreground, #0f172a)"
                      >
                        {clipTitle(node.title, 14)}
                      </text>
                      <text
                        x={node.x + 14}
                        y={node.y + 50}
                        fontSize={10.5}
                        fill="var(--muted-foreground, #5b6779)"
                      >
                        {`用例 · ${caseStatusLabel(node.status)}`}
                      </text>
                      {node.case?.aiSupplemented === true ? (
                        <g data-testid="chain-supplement-badge">
                          <rect
                            x={node.x + node.w - 34}
                            y={node.y + 10}
                            width={24}
                            height={16}
                            rx={8}
                            fill="var(--muted, #eef2f7)"
                          />
                          <text
                            x={node.x + node.w - 22}
                            y={node.y + 21.5}
                            fontSize={9}
                            textAnchor="middle"
                            fill="var(--muted-foreground, #5b6779)"
                          >
                            AI
                          </text>
                        </g>
                      ) : null}
                      {node.status === "needs_revalidation" ? (
                        <g data-testid="chain-revalidation-badge">
                          <rect
                            x={node.x + node.w - 62}
                            y={node.y + 40}
                            width={52}
                            height={16}
                            rx={8}
                            fill="#fef3c7"
                            stroke={REVALIDATION_COLOR}
                          />
                          <text
                            x={node.x + node.w - 36}
                            y={node.y + 51.5}
                            fontSize={9}
                            textAnchor="middle"
                            fill="#92400e"
                          >
                            需重验
                          </text>
                        </g>
                      ) : null}
                    </>
                  ) : null}

                  {node.kind === "issue" ? (
                    <>
                      {/* Red diamond of spec §10.5; the "!" glyph avoids a colour-only encoding. */}
                      <polygon
                        points={`${node.x + 18},${node.y + 8} ${node.x + 34},${node.y + 26} ${node.x + 18},${node.y + 44} ${node.x + 2},${node.y + 26}`}
                        fill={ISSUE_COLOR}
                        stroke="#b91c1c"
                        strokeWidth={1}
                      />
                      <text
                        x={node.x + 18}
                        y={node.y + 31}
                        fontSize={13}
                        fontWeight={700}
                        textAnchor="middle"
                        fill="#ffffff"
                      >
                        !
                      </text>
                      <text
                        x={node.x + 42}
                        y={node.y + 22}
                        fontSize={12}
                        fontWeight={600}
                        fill="var(--card-foreground, #0f172a)"
                      >
                        {clipTitle(node.title, 8)}
                      </text>
                      <text
                        x={node.x + 42}
                        y={node.y + 41}
                        fontSize={10.5}
                        fill="var(--muted-foreground, #5b6779)"
                      >
                        {`问题 · ${issueSeverityLabel(node.issue?.severity ?? "")} · ${issueStatusLabel(node.status)}`}
                      </text>
                    </>
                  ) : null}

                  {node.kind === "result" ? (
                    <>
                      <rect
                        x={node.x}
                        y={node.y}
                        width={node.w}
                        height={node.h}
                        rx={8}
                        fill="var(--card, #ffffff)"
                        stroke={statusColor}
                        strokeWidth={1.5}
                        className="group-focus-visible:[stroke-width:2.5px]"
                      />
                      <rect
                        x={node.x}
                        y={node.y + 8}
                        width={4}
                        height={node.h - 16}
                        rx={2}
                        fill={statusColor}
                      />
                      <text
                        x={node.x + 16}
                        y={node.y + 23}
                        fontSize={12}
                        fontWeight={600}
                        fill="var(--card-foreground, #0f172a)"
                      >
                        {clipTitle(node.title, 12)}
                      </text>
                      <text
                        x={node.x + 16}
                        y={node.y + 44}
                        fontSize={11}
                        fontWeight={700}
                        fill={statusColor}
                      >
                        {resultMeta(node.status).glyph}
                      </text>
                      <text
                        x={node.x + 30}
                        y={node.y + 44}
                        fontSize={10.5}
                        fill="var(--muted-foreground, #5b6779)"
                      >
                        {label}
                        {formatDuration(node.result?.durationMs) === ""
                          ? ""
                          : ` · ${formatDuration(node.result?.durationMs)}`}
                        {(node.result?.evidenceIds?.length ?? 0) === 0
                          ? ""
                          : ` · 证据 ${node.result?.evidenceIds?.length ?? 0}`}
                      </text>
                    </>
                  ) : null}

                  {isSelected ? (
                    <rect
                      x={node.x}
                      y={node.y}
                      width={node.w}
                      height={node.h}
                      rx={node.kind === "result" ? 8 : 10}
                      fill="none"
                      stroke="var(--ring, #3b82f6)"
                      strokeWidth={2.5}
                      pointerEvents="none"
                    />
                  ) : null}

                  {node.kind === "requirement" && node.hasChildren ? (
                    // biome-ignore lint/a11y/useSemanticElements: SVG cannot host a native button; the ARIA button role carries the toggle.
                    <g
                      role="button"
                      tabIndex={-1}
                      data-testid={`chain-toggle-${node.id}`}
                      aria-label={`${node.collapsed ? "展开" : "折叠"} ${node.title}`}
                      className="cursor-pointer focus-visible:outline-none"
                      onClick={(event) => {
                        event.stopPropagation()
                        setFocusedId(node.id)
                        onToggleRequirement(node.id)
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
                        {node.collapsed ? "+" : "−"}
                      </text>
                    </g>
                  ) : null}
                </g>
              )
            })}
          </g>
        </svg>
      </div>
    </section>
  )
}
