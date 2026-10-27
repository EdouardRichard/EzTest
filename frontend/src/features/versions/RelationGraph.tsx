"use client"

import { Loader2 } from "lucide-react"
import { type KeyboardEvent, useId } from "react"

import { StatusBadge } from "@/components/ui/StatusBadge"

import type { RelationType, Requirement, RequirementRelation } from "./types"

/**
 * Display metadata for the four relation kinds (spec §6.4). Only `depends_on`
 * is directed; the legend states direction in text so meaning never relies on
 * colour or an arrow shape alone (spec §9.5).
 */
export const RELATION_TYPE_META: Record<
  RelationType,
  { label: string; directed: boolean; legend: string }
> = {
  depends_on: { label: "依赖", directed: true, legend: "依赖（有向，箭头指向被依赖需求）" },
  conflicts_with: { label: "冲突", directed: false, legend: "冲突（无向）" },
  overlaps: { label: "重叠", directed: false, legend: "重叠（无向）" },
  shares_scope: { label: "共享范围", directed: false, legend: "共享范围（无向）" },
}

const VIEW_WIDTH = 520
const VIEW_HEIGHT = 380
const CENTER_X = 260
const CENTER_Y = 190
const LAYOUT_RADIUS = 135
const NODE_RADIUS = 24
const ARROW_GAP = 4

/** Deterministic circular layout: node 0 at 12 o'clock, the rest evenly spaced. */
function layoutNodes(requirements: Requirement[]): NodePosition[] {
  const count = requirements.length
  if (count === 0) return []
  if (count === 1) {
    return [{ requirement: requirements[0], x: CENTER_X, y: CENTER_Y }]
  }
  return requirements.map((requirement, index) => {
    const angle = -Math.PI / 2 + (2 * Math.PI * index) / count
    return {
      requirement,
      x: Math.round(CENTER_X + LAYOUT_RADIUS * Math.cos(angle)),
      y: Math.round(CENTER_Y + LAYOUT_RADIUS * Math.sin(angle)),
    }
  })
}

type NodePosition = {
  requirement: Requirement
  x: number
  y: number
}

type EdgeGeometry = {
  x1: number
  y1: number
  x2: number
  y2: number
}

/** Trims an edge at both node borders so arrowheads stay visible. */
function edgeGeometry(from: NodePosition, to: NodePosition): EdgeGeometry {
  const dx = to.x - from.x
  const dy = to.y - from.y
  const length = Math.hypot(dx, dy) || 1
  const offset = NODE_RADIUS + ARROW_GAP
  return {
    x1: from.x + (dx / length) * offset,
    y1: from.y + (dy / length) * offset,
    x2: to.x - (dx / length) * offset,
    y2: to.y - (dy / length) * offset,
  }
}

/** Short label inside the node; the full title stays in the tooltip/aria-label. */
function shortTitle(title: string): string {
  return title.length > 10 ? `${title.slice(0, 10)}…` : title
}

const EDGE_STROKE: Record<RelationType, string> = {
  depends_on: "#0284c7",
  conflicts_with: "#dc2626",
  overlaps: "#d97706",
  shares_scope: "#7c3aed",
}

const EDGE_DASH: Record<RelationType, string | undefined> = {
  depends_on: undefined,
  conflicts_with: "6 4",
  overlaps: "2 4",
  shares_scope: "6 4",
}

export type RelationGraphProps = {
  requirements: Requirement[]
  relations: RequirementRelation[]
  /** True while relations are (re)loading; the stale count is hidden meanwhile. */
  loading?: boolean
  /** Relation load failure from the workbench; shown instead of a zero count. */
  error?: string
  /** Selects the requirement a node represents; the workbench owns selection. */
  onSelect: (requirementId: string) => void
}

/**
 * Requirement relation graph (spec §6.4, §9.2): a simple SVG with a
 * deterministic circular layout, one node per requirement, `depends_on` edges
 * with an arrowhead and plain lines for the three undirected kinds, a text
 * legend, and node selection by click or keyboard.
 */
export function RelationGraph({
  requirements,
  relations,
  loading = false,
  error = "",
  onSelect,
}: RelationGraphProps) {
  // React ids contain ":" characters, which are awkward inside SVG url(#...)
  // references; strip everything outside the safe fragment alphabet.
  const arrowId = `relation-arrow-${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`
  const nodes = layoutNodes(requirements)
  const nodeById = new Map(nodes.map((node) => [node.requirement.id, node]))

  const edges = relations.flatMap((relation) => {
    const from = nodeById.get(relation.fromRequirementId)
    const to = nodeById.get(relation.toRequirementId)
    if (from === undefined || to === undefined) return []
    return [{ relation, from, to }]
  })

  function handleNodeKeyDown(event: KeyboardEvent<SVGGElement>, requirementId: string): void {
    if (event.key !== "Enter" && event.key !== " ") return
    event.preventDefault()
    onSelect(requirementId)
  }

  return (
    <section
      aria-label="需求关系图"
      className="min-w-0 rounded-2xl border border-border bg-card p-4"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-base font-semibold">需求关系图</h2>
        {loading ? (
          <StatusBadge variant="neutral">关系加载中…</StatusBadge>
        ) : error !== "" ? (
          <StatusBadge variant="danger">关系加载失败</StatusBadge>
        ) : (
          <StatusBadge variant="neutral">{`共 ${relations.length} 条关系`}</StatusBadge>
        )}
      </div>

      {error === "" ? null : (
        <p
          role="alert"
          className="mt-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          加载关系失败：{error}
        </p>
      )}

      {loading ? (
        <p className="mt-3 flex items-center justify-center gap-2 rounded-xl border border-dashed border-border px-3 py-6 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          正在加载关系…
        </p>
      ) : error !== "" ? null : requirements.length === 0 ? (
        <p className="mt-3 rounded-xl border border-dashed border-border px-3 py-6 text-center text-sm text-muted-foreground">
          暂无可绘制关系的需求。
        </p>
      ) : (
        <>
          <p className="mt-1 text-xs text-muted-foreground">
            节点按创建顺序环形排布；点击节点可选中对应需求。
          </p>

          <svg
            role="img"
            aria-label={`需求关系图：${requirements.length} 个需求节点，${relations.length} 条关系`}
            viewBox={`0 0 ${VIEW_WIDTH} ${VIEW_HEIGHT}`}
            className="mt-2 h-auto w-full"
          >
            <defs>
              <marker
                id={arrowId}
                viewBox="0 0 10 10"
                refX="10"
                refY="5"
                markerWidth="6"
                markerHeight="6"
                orient="auto-start-reverse"
              >
                <path d="M0 0 L10 5 L0 10 z" fill={EDGE_STROKE.depends_on} />
              </marker>
            </defs>

            {edges.map(({ relation, from, to }) => {
              const geometry = edgeGeometry(from, to)
              return (
                <line
                  key={relation.id}
                  data-testid={`relation-edge-${relation.id}`}
                  data-relation-type={relation.relationType}
                  x1={geometry.x1}
                  y1={geometry.y1}
                  x2={geometry.x2}
                  y2={geometry.y2}
                  stroke={EDGE_STROKE[relation.relationType]}
                  strokeWidth={2}
                  strokeDasharray={EDGE_DASH[relation.relationType]}
                  markerEnd={
                    relation.relationType === "depends_on" ? `url(#${arrowId})` : undefined
                  }
                />
              )
            })}

            {nodes.map(({ requirement, x, y }) => (
              // biome-ignore lint/a11y/useSemanticElements: SVG node groups cannot be native <button> elements; role="button" plus keyboard handling is the accessible SVG pattern.
              <g
                key={requirement.id}
                data-testid={`requirement-node-${requirement.id}`}
                role="button"
                tabIndex={0}
                aria-label={`选择需求 ${requirement.title}`}
                transform={`translate(${x} ${y})`}
                onClick={() => onSelect(requirement.id)}
                onKeyDown={(event) => handleNodeKeyDown(event, requirement.id)}
                className="group cursor-pointer focus-visible:outline-none"
              >
                <title>{`需求：${requirement.title}`}</title>
                <circle
                  r={NODE_RADIUS}
                  fill="var(--muted, #f1f5f9)"
                  stroke="var(--border, #cbd5e1)"
                  strokeWidth={1.5}
                  className="group-focus-visible:stroke-ring group-focus-visible:[stroke-width:3px]"
                />
                <text textAnchor="middle" y={4} fontSize={10} fill="var(--foreground, #0f172a)">
                  {shortTitle(requirement.title)}
                </text>
              </g>
            ))}
          </svg>

          {relations.length === 0 ? (
            <p className="mt-1 text-xs text-muted-foreground">
              暂无关系，添加关系后将在此展示关系图。
            </p>
          ) : null}

          <ul
            aria-label="关系图图例"
            className="mt-3 grid gap-1.5 border-t border-border pt-3 sm:grid-cols-2"
          >
            {(Object.keys(RELATION_TYPE_META) as RelationType[]).map((type) => (
              <li key={type} className="flex items-center gap-2 text-[11px] text-muted-foreground">
                <span
                  aria-hidden="true"
                  className="inline-block h-0.5 w-5 shrink-0"
                  style={{
                    backgroundColor: EDGE_STROKE[type],
                    borderTop:
                      EDGE_DASH[type] === undefined ? undefined : `2px dashed ${EDGE_STROKE[type]}`,
                  }}
                />
                {RELATION_TYPE_META[type].legend}
              </li>
            ))}
          </ul>
        </>
      )}
    </section>
  )
}
