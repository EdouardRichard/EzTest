/**
 * Deterministic three-layer layout for the P6 execution chain graph (spec §9.3).
 *
 * The chain is a layered graph: requirements (layer 0) → cases (layer 1) →
 * execution results (layer 2). Layout is a pure function of the inputs plus the
 * collapsed requirement set: requirement groups follow `sortOrder`, cases of one
 * requirement follow their own `sortOrder`, and every sibling tie is broken by
 * the decimal id. The same input therefore always produces the same coordinates
 * regardless of array order, which the canvas relies on to keep nodes stable
 * while SSE events refresh the data.
 *
 * P6 does not fetch issue data (P7 owns issues); the issue layer is represented
 * by the tab's placeholder, not by this module.
 */

import type { CaseRunStatus, FailureClass } from "./types"

/** Requirement card size in layout units before canvas zoom. */
export const CHAIN_REQUIREMENT_WIDTH = 200
export const CHAIN_REQUIREMENT_HEIGHT = 64
/** Case card size. */
export const CHAIN_CASE_WIDTH = 220
export const CHAIN_CASE_HEIGHT = 72
/** Result card size. */
export const CHAIN_RESULT_WIDTH = 184
export const CHAIN_RESULT_HEIGHT = 56
/** Horizontal gap between two layers. */
export const CHAIN_LAYER_GAP = 80
/** Vertical gap between stacked results of the same case. */
export const CHAIN_ROW_GAP = 16
/** Vertical gap between two requirement groups. */
export const CHAIN_GROUP_GAP = 32
/** Above this many nodes the tab collapses every requirement group by default. */
export const CHAIN_AUTO_COLLAPSE_THRESHOLD = 200

/** One requirement rendered in layer 0. */
export type ChainRequirementInput = {
  id: string
  title: string
  /** Raw requirement status; the graph renders a Chinese label for it. */
  status?: string
  /** Display order of the requirement group; ties break by `id`. */
  sortOrder?: number
}

/** One case rendered in layer 1. */
export type ChainCaseInput = {
  id: string
  title: string
  /** Requirement ids this case is linked to (any of them may draw an edge). */
  requirementIds?: readonly string[]
  status?: string
  sortOrder?: number
  /** AI-supplemented cases are drawn with a dashed border and an AI badge. */
  aiSupplemented?: boolean
}

/** One execution result rendered in layer 2. */
export type ChainResultInput = {
  id: string
  caseNodeId: string
  status: CaseRunStatus
  /** Falls back to the case title when the DTO omits one. */
  title?: string
  summary?: string
  durationMs?: number
  failureClass?: FailureClass | null
  evidenceIds?: readonly string[]
  sortOrder?: number
}

export type ChainLayoutInput = {
  requirements: readonly ChainRequirementInput[]
  cases: readonly ChainCaseInput[]
  results?: readonly ChainResultInput[]
}

export type ChainLayoutOptions = {
  /** Requirement ids whose cases and results are hidden. */
  collapsed?: readonly string[] | ReadonlySet<string>
}

export type ChainNodeKind = "requirement" | "case" | "result"
export type ChainLayer = 0 | 1 | 2

/** One visible node with top-left coordinates in layout units. */
export type ChainNode = {
  /** Domain id: requirement id, case node id or case-run id. */
  id: string
  kind: ChainNodeKind
  layer: ChainLayer
  title: string
  /** Raw status of the node's own kind (empty when the DTO omits it). */
  status: string
  x: number
  y: number
  w: number
  h: number
  /** Requirement ids linked to this node (empty for a requirement node). */
  requirementIds: string[]
  /** Case node id of a case/result node; null for a requirement node. */
  caseNodeId: string | null
  /** The input row this node renders; never mutated by the layout. */
  requirement?: ChainRequirementInput
  case?: ChainCaseInput
  result?: ChainResultInput
  /** True when the underlying data has children, collapsed or not. */
  hasChildren: boolean
  /** Descendants in the full input; used by the collapsed-count badge. */
  descendantCount: number
  /** True only on a requirement node whose group is collapsed. */
  collapsed: boolean
}

export type ChainEdgeKind = "requirement-case" | "case-result"

/** One visible parent → child link. */
export type ChainEdge = {
  id: string
  kind: ChainEdgeKind
  sourceId: string
  targetId: string
}

export type ChainBounds = {
  x: number
  y: number
  width: number
  height: number
}

export type ChainLayoutResult = {
  /** Visible nodes in pre-order (requirement group, case, results). */
  nodes: ChainNode[]
  /** Links between visible nodes only. */
  edges: ChainEdge[]
  /** Tight bounding box of the visible nodes, in layout units. */
  bounds: ChainBounds
  visibleIds: string[]
  /** Nodes hidden by a collapsed requirement, sorted by id. */
  hiddenIds: string[]
  /** Total unique node count before collapsing (auto-collapse input). */
  totalCount: number
}

/** Result statuses that the "仅异常" filter surfaces. */
export const ABNORMAL_RESULT_STATUSES: readonly CaseRunStatus[] = [
  "failed",
  "blocked",
  "inconclusive",
  "interrupted",
]

/** Status filter values of the exploration tab ("" means every status). */
export type ChainResultFilter =
  | ""
  | "passed"
  | "failed"
  | "blocked"
  | "inconclusive"
  | "skipped"
  | "interrupted"
  | "pending"

/** True when a result status counts as an anomaly (failed/blocked/…). */
export function isAbnormalResultStatus(status: CaseRunStatus): boolean {
  return ABNORMAL_RESULT_STATUSES.includes(status)
}

/** Applies one status filter; the pending bucket also covers `running`. */
export function matchesResultFilter(status: CaseRunStatus, filter: ChainResultFilter): boolean {
  if (filter === "") return true
  if (filter === "pending") return status === "pending" || status === "running"
  return status === filter
}

function toCollapsedSet(
  collapsed: readonly string[] | ReadonlySet<string> | undefined,
): Set<string> {
  if (collapsed === undefined) return new Set<string>()
  return new Set<string>(collapsed as Iterable<string>)
}

type Ordered = { id: string; sortOrder?: number }

/** Sibling order: explicit `sortOrder`, then the decimal id for determinism. */
function compareOrder(a: Ordered, b: Ordered): number {
  const aOrder = a.sortOrder ?? 0
  const bOrder = b.sortOrder ?? 0
  if (aOrder !== bOrder) return aOrder - bOrder
  if (a.id === b.id) return 0
  return a.id < b.id ? -1 : 1
}

/** Unique rows by id, keeping the first occurrence of the sorted input. */
function uniqueById<T extends { id: string }>(rows: readonly T[]): T[] {
  const seen = new Set<string>()
  const unique: T[] = []
  for (const row of rows) {
    if (seen.has(row.id)) continue
    seen.add(row.id)
    unique.push(row)
  }
  return unique
}

/** Number of unique requirements, cases and results in one chain input. */
function countInputNodes(input: ChainLayoutInput): number {
  const requirementIds = new Set(input.requirements.map((row) => row.id))
  const caseIds = new Set(input.cases.map((row) => row.id))
  const resultIds = new Set((input.results ?? []).map((row) => row.id))
  for (const result of input.results ?? []) caseIds.add(result.caseNodeId)
  return requirementIds.size + caseIds.size + resultIds.size
}

/**
 * Requirement ids that should start collapsed: all of them once the full graph
 * exceeds the threshold, otherwise none (spec §9.3: large chains fold by
 * requirement group).
 */
export function autoCollapsedRequirements(
  input: ChainLayoutInput,
  threshold: number = CHAIN_AUTO_COLLAPSE_THRESHOLD,
): string[] {
  if (countInputNodes(input) <= threshold) return []
  return [...new Set(input.requirements.map((row) => row.id))].sort()
}

/**
 * Turns the flat requirement/case/result rows into a positioned layered chain.
 *
 * Robustness: a result whose case is absent from the graph gets a synthetic
 * case node (and therefore stays visible), while cases without a known
 * requirement are laid out as an unassigned group after the requirement groups.
 * Nothing is silently dropped.
 */
export function layoutChain(
  input: ChainLayoutInput,
  options: ChainLayoutOptions = {},
): ChainLayoutResult {
  const collapsed = toCollapsedSet(options.collapsed)
  const requirements = uniqueById([...input.requirements].sort(compareOrder))
  const results = uniqueById([...(input.results ?? [])].sort(compareOrder))
  const cases = uniqueById([...input.cases].sort(compareOrder))

  const caseById = new Map<string, ChainCaseInput>()
  for (const row of cases) caseById.set(row.id, row)
  for (const result of results) {
    if (caseById.has(result.caseNodeId)) continue
    caseById.set(result.caseNodeId, {
      id: result.caseNodeId,
      title: result.title === undefined || result.title === "" ? "未知用例" : result.title,
      requirementIds: [],
      sortOrder: Number.MAX_SAFE_INTEGER,
    })
  }
  const allCases = [...caseById.values()].sort(compareOrder)

  const requirementOrder = new Map<string, number>()
  for (const [index, row] of requirements.entries()) requirementOrder.set(row.id, index)

  const linkedRequirementIds = (row: ChainCaseInput): string[] => {
    const ids = [...new Set(row.requirementIds ?? [])].filter((id) => requirementOrder.has(id))
    return ids.sort((left, right) => {
      const leftOrder = requirementOrder.get(left) ?? Number.MAX_SAFE_INTEGER
      const rightOrder = requirementOrder.get(right) ?? Number.MAX_SAFE_INTEGER
      if (leftOrder !== rightOrder) return leftOrder - rightOrder
      return left < right ? -1 : left > right ? 1 : 0
    })
  }

  const resultsByCase = new Map<string, ChainResultInput[]>()
  for (const result of results) {
    const siblings = resultsByCase.get(result.caseNodeId)
    if (siblings === undefined) resultsByCase.set(result.caseNodeId, [result])
    else siblings.push(result)
  }
  for (const siblings of resultsByCase.values()) siblings.sort(compareOrder)

  /** The earliest linked requirement decides which group a case sits in. */
  const primaryRequirement = (row: ChainCaseInput): string | null => {
    const ids = linkedRequirementIds(row)
    return ids[0] ?? null
  }

  const casesByRequirement = new Map<string, ChainCaseInput[]>()
  for (const requirement of requirements) casesByRequirement.set(requirement.id, [])
  const unassigned: ChainCaseInput[] = []
  for (const row of allCases) {
    const primary = primaryRequirement(row)
    if (primary === null) {
      unassigned.push(row)
      continue
    }
    casesByRequirement.get(primary)?.push(row)
  }

  const nodes: ChainNode[] = []
  const edges: ChainEdge[] = []
  const visibleIds = new Set<string>()
  const hidden = new Set<string>()
  const edgeIds = new Set<string>()

  const caseX = CHAIN_REQUIREMENT_WIDTH + CHAIN_LAYER_GAP
  const resultX = caseX + CHAIN_CASE_WIDTH + CHAIN_LAYER_GAP

  function addEdge(kind: ChainEdgeKind, sourceId: string, targetId: string): void {
    const id = `${sourceId}->${targetId}`
    if (edgeIds.has(id)) return
    edgeIds.add(id)
    edges.push({ id, kind, sourceId, targetId })
  }

  function pushCase(row: ChainCaseInput, y: number): number {
    const requirementIds = linkedRequirementIds(row)
    nodes.push({
      id: row.id,
      kind: "case",
      layer: 1,
      title: row.title,
      status: row.status ?? "",
      x: caseX,
      y,
      w: CHAIN_CASE_WIDTH,
      h: CHAIN_CASE_HEIGHT,
      requirementIds,
      caseNodeId: row.id,
      case: row,
      hasChildren: (resultsByCase.get(row.id)?.length ?? 0) > 0,
      descendantCount: resultsByCase.get(row.id)?.length ?? 0,
      collapsed: false,
    })
    visibleIds.add(row.id)

    const siblings = resultsByCase.get(row.id) ?? []
    let cursor = y
    for (const result of siblings) {
      nodes.push({
        id: result.id,
        kind: "result",
        layer: 2,
        title: result.title === undefined || result.title === "" ? row.title : result.title,
        status: result.status,
        x: resultX,
        y: cursor,
        w: CHAIN_RESULT_WIDTH,
        h: CHAIN_RESULT_HEIGHT,
        requirementIds,
        caseNodeId: row.id,
        result,
        hasChildren: false,
        descendantCount: 0,
        collapsed: false,
      })
      visibleIds.add(result.id)
      cursor += CHAIN_RESULT_HEIGHT + CHAIN_ROW_GAP
    }

    for (const requirementId of requirementIds) addEdge("requirement-case", requirementId, row.id)
    for (const result of siblings) addEdge("case-result", row.id, result.id)

    const resultsHeight =
      siblings.length === 0
        ? 0
        : siblings.length * CHAIN_RESULT_HEIGHT + (siblings.length - 1) * CHAIN_ROW_GAP
    return Math.max(CHAIN_CASE_HEIGHT, resultsHeight)
  }

  let cursorY = 0
  for (const requirement of requirements) {
    const group = casesByRequirement.get(requirement.id) ?? []
    const isCollapsed = collapsed.has(requirement.id)
    const descendantCount =
      group.length + group.reduce((sum, row) => sum + (resultsByCase.get(row.id)?.length ?? 0), 0)

    nodes.push({
      id: requirement.id,
      kind: "requirement",
      layer: 0,
      title: requirement.title,
      status: requirement.status ?? "",
      x: 0,
      y: cursorY,
      w: CHAIN_REQUIREMENT_WIDTH,
      h: CHAIN_REQUIREMENT_HEIGHT,
      requirementIds: [requirement.id],
      caseNodeId: null,
      requirement,
      hasChildren: group.length > 0,
      descendantCount,
      collapsed: isCollapsed,
    })
    visibleIds.add(requirement.id)

    if (isCollapsed) {
      for (const row of group) {
        hidden.add(row.id)
        for (const result of resultsByCase.get(row.id) ?? []) hidden.add(result.id)
      }
      cursorY += CHAIN_REQUIREMENT_HEIGHT + CHAIN_GROUP_GAP
      continue
    }

    let groupCursor = cursorY
    for (const row of group) {
      const blockHeight = pushCase(row, groupCursor)
      groupCursor += blockHeight + CHAIN_ROW_GAP
    }
    if (group.length > 0) groupCursor -= CHAIN_ROW_GAP
    cursorY = Math.max(cursorY + CHAIN_REQUIREMENT_HEIGHT, groupCursor) + CHAIN_GROUP_GAP
  }

  let unassignedCursor = cursorY
  for (const row of unassigned) {
    const blockHeight = pushCase(row, unassignedCursor)
    unassignedCursor += blockHeight + CHAIN_ROW_GAP
  }

  const resultIds = new Set(results.map((row) => row.id))
  for (const id of resultIds) {
    if (!visibleIds.has(id)) hidden.add(id)
  }
  for (const id of caseById.keys()) {
    if (!visibleIds.has(id)) hidden.add(id)
  }

  // Edges are collected while laying out groups (a shared case can be linked
  // from a requirement that is not visible yet); keep only fully visible links.
  const visibleEdges = edges.filter(
    (edge) => visibleIds.has(edge.sourceId) && visibleIds.has(edge.targetId),
  )

  let bounds: ChainBounds = { x: 0, y: 0, width: 0, height: 0 }
  if (nodes.length > 0) {
    let minX = Number.POSITIVE_INFINITY
    let minY = Number.POSITIVE_INFINITY
    let maxX = Number.NEGATIVE_INFINITY
    let maxY = Number.NEGATIVE_INFINITY
    for (const node of nodes) {
      minX = Math.min(minX, node.x)
      minY = Math.min(minY, node.y)
      maxX = Math.max(maxX, node.x + node.w)
      maxY = Math.max(maxY, node.y + node.h)
    }
    bounds = { x: minX, y: minY, width: maxX - minX, height: maxY - minY }
  }

  return {
    nodes,
    edges: visibleEdges,
    bounds,
    visibleIds: nodes.map((node) => node.id),
    hiddenIds: [...hidden].sort(),
    totalCount: requirements.length + allCases.length + results.length,
  }
}
