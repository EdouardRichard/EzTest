/**
 * Deterministic four-layer layout for the execution chain graph (spec §9.3,
 * P8 §5.1/§5.3).
 *
 * The module exports two pure functions:
 *
 * - `layoutChain(nodes, options)` is the read-model layout used by the React
 *   Flow canvas. It takes the Task 8 `ChainSnapshotNode[]` plus the snapshot's
 *   semantic edges and returns positioned four-layer nodes, the visible edges
 *   and the tight bounds. The backend already returns each layer in display
 *   order (spec §5.1: requirements/cases by `sortOrder,id`, results by
 *   `attempt desc`, issues by severity rank), so the layout keeps the input
 *   array order untouched instead of re-sorting by id; the same ordered
 *   snapshot always produces the same coordinates. Cases whose requirement was
 *   filtered out stay visible in an unassigned group, and results/issues
 *   without a case are kept visible via a synthetic case instead of being
 *   silently dropped.
 * - `layoutLegacy(input, options)` is the temporary compatibility adapter for
 *   the hand-written SVG `ChainGraph` and `ExplorationTab` (Task 10 删除 once
 *   the React Flow canvas replaces it). It keeps the P7 input shape, ids and
 *   coordinates byte-for-byte so the current canvas keeps its visual behaviour
 *   until then.
 *
 * Issue attachment priority follows the read model (spec §5.1): a `result_issue`
 * edge wins, then a `case_issue` edge, then the DTO's `caseRunId`/`caseNodeId`
 * fallback for robustness. Issues that cannot be attached at all are returned
 * in `unlinkedIssues` instead of being silently dropped.
 */

import type { CaseRunStatus, FailureClass } from "../types"
import type { ChainEdgeKindDTO, ChainSnapshotEdge, ChainSnapshotNode } from "./chain-types"

/** Requirement card size in layout units before canvas zoom. */
export const CHAIN_REQUIREMENT_WIDTH = 200
export const CHAIN_REQUIREMENT_HEIGHT = 64
/** Case card size. */
export const CHAIN_CASE_WIDTH = 220
export const CHAIN_CASE_HEIGHT = 72
/** Result card size. */
export const CHAIN_RESULT_WIDTH = 184
export const CHAIN_RESULT_HEIGHT = 56
/** Issue card size (red diamond plus severity/status text). */
export const CHAIN_ISSUE_WIDTH = 136
export const CHAIN_ISSUE_HEIGHT = 52
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

/** One delivery issue rendered in layer 3 (spec §10.5). */
export type ChainIssueInput = {
  id: string
  title: string
  status?: string
  severity?: string
  confidence?: string
  category?: string
  /** Case node the issue belongs to; null for a requirement-gap/human issue. */
  caseNodeId?: string | null
  /** Case run the issue belongs to; used to resolve the case when it is null. */
  caseRunId?: string | null
  /** Evidence refs of the issue, opened in the results tab's viewer. */
  evidenceIds?: readonly string[]
  sortOrder?: number
}

export type ChainLayoutInput = {
  requirements: readonly ChainRequirementInput[]
  cases: readonly ChainCaseInput[]
  results?: readonly ChainResultInput[]
  issues?: readonly ChainIssueInput[]
}

export type LegacyChainLayoutOptions = {
  /** Requirement ids whose cases and results are hidden. */
  collapsed?: readonly string[] | ReadonlySet<string>
}

export type ChainNodeKind = "requirement" | "case" | "result" | "issue"
export type ChainLayer = 0 | 1 | 2 | 3

/** One visible node with top-left coordinates in layout units. */
export type ChainNode = {
  /** Domain id: requirement id, case node id, case-run id or issue id. */
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
  /** Case node id of a case/result/issue node; null for a requirement node. */
  caseNodeId: string | null
  /** The input row this node renders; never mutated by the layout. */
  requirement?: ChainRequirementInput
  case?: ChainCaseInput
  result?: ChainResultInput
  issue?: ChainIssueInput
  /** True when the underlying data has children, collapsed or not. */
  hasChildren: boolean
  /** Descendants in the full input; used by the collapsed-count badge. */
  descendantCount: number
  /** True only on a requirement node whose group is collapsed. */
  collapsed: boolean
}

export type ChainEdgeKind = "requirement-case" | "case-result" | "issue-link"

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

export type LegacyChainLayoutResult = {
  /** Visible nodes in pre-order (requirement group, case, results, issues). */
  nodes: ChainNode[]
  /** Links between visible nodes only. */
  edges: ChainEdge[]
  /** Tight bounding box of the visible nodes, in layout units. */
  bounds: ChainBounds
  visibleIds: string[]
  /** Nodes hidden by a collapsed requirement, sorted by id. */
  hiddenIds: string[]
  /** Issues that cannot be attached to a case of this graph, sorted. */
  unlinkedIssues: ChainIssueInput[]
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

/** Number of unique requirements, cases, results and issues in one input. */
function countInputNodes(input: ChainLayoutInput): number {
  const requirementIds = new Set(input.requirements.map((row) => row.id))
  const caseIds = new Set(input.cases.map((row) => row.id))
  const resultIds = new Set((input.results ?? []).map((row) => row.id))
  const issueIds = new Set((input.issues ?? []).map((row) => row.id))
  for (const result of input.results ?? []) caseIds.add(result.caseNodeId)
  for (const issue of input.issues ?? []) {
    if (issue.caseNodeId !== null && issue.caseNodeId !== undefined) {
      caseIds.add(issue.caseNodeId)
    }
  }
  return requirementIds.size + caseIds.size + resultIds.size + issueIds.size
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
 * Task 10 删除：`layoutLegacy` 只服务于当前手工 SVG `ChainGraph`；React Flow
 * 画布替换后，本函数与 `LegacyChainLayoutResult`/`LegacyChainLayoutOptions`、
 * `ChainGraph` 一起移除。
 *
 * Robustness: a result whose case is absent from the graph gets a synthetic
 * case node (and therefore stays visible), while cases without a known
 * requirement are laid out as an unassigned group after the requirement groups.
 * Nothing is silently dropped.
 */
export function layoutLegacy(
  input: ChainLayoutInput,
  options: LegacyChainLayoutOptions = {},
): LegacyChainLayoutResult {
  const collapsed = toCollapsedSet(options.collapsed)
  const requirements = uniqueById([...input.requirements].sort(compareOrder))
  const results = uniqueById([...(input.results ?? [])].sort(compareOrder))
  const cases = uniqueById([...input.cases].sort(compareOrder))
  const issues = uniqueById([...(input.issues ?? [])].sort(compareOrder))

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

  const resultCaseById = new Map<string, string>()
  for (const result of results) resultCaseById.set(result.id, result.caseNodeId)

  /** Case an issue hangs off, resolved through its case run when necessary. */
  const issueCaseId = (issue: ChainIssueInput): string | null => {
    const explicit = issue.caseNodeId ?? ""
    if (explicit !== "") return explicit
    const caseRunId = issue.caseRunId ?? ""
    if (caseRunId === "") return null
    return resultCaseById.get(caseRunId) ?? null
  }

  const linkedIssues: ChainIssueInput[] = []
  const unlinkedIssues: ChainIssueInput[] = []
  for (const issue of issues) {
    const caseId = issueCaseId(issue)
    if (caseId === null) {
      unlinkedIssues.push(issue)
      continue
    }
    linkedIssues.push(issue)
    if (caseById.has(caseId)) continue
    caseById.set(caseId, {
      id: caseId,
      title: "未知用例",
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

  const issuesByCase = new Map<string, ChainIssueInput[]>()
  for (const issue of linkedIssues) {
    const caseId = issueCaseId(issue)
    if (caseId === null) continue
    const siblings = issuesByCase.get(caseId)
    if (siblings === undefined) issuesByCase.set(caseId, [issue])
    else siblings.push(issue)
  }
  for (const siblings of issuesByCase.values()) siblings.sort(compareOrder)

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
  const issueX = resultX + CHAIN_RESULT_WIDTH + CHAIN_LAYER_GAP

  function addEdge(kind: ChainEdgeKind, sourceId: string, targetId: string): void {
    const id = `${sourceId}->${targetId}`
    if (edgeIds.has(id)) return
    edgeIds.add(id)
    edges.push({ id, kind, sourceId, targetId })
  }

  function pushCase(row: ChainCaseInput, y: number): number {
    const requirementIds = linkedRequirementIds(row)
    const siblings = resultsByCase.get(row.id) ?? []
    const issueSiblings = issuesByCase.get(row.id) ?? []
    const childCount = siblings.length + issueSiblings.length
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
      hasChildren: childCount > 0,
      descendantCount: childCount,
      collapsed: false,
    })
    visibleIds.add(row.id)

    const resultIdsOfCase = new Set(siblings.map((result) => result.id))
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

    let issueCursor = y
    for (const issue of issueSiblings) {
      nodes.push({
        id: issue.id,
        kind: "issue",
        layer: 3,
        title: issue.title,
        status: issue.status ?? "",
        x: issueX,
        y: issueCursor,
        w: CHAIN_ISSUE_WIDTH,
        h: CHAIN_ISSUE_HEIGHT,
        requirementIds,
        caseNodeId: row.id,
        issue,
        hasChildren: false,
        descendantCount: 0,
        collapsed: false,
      })
      visibleIds.add(issue.id)
      const caseRunId = issue.caseRunId ?? ""
      const sourceId = caseRunId !== "" && resultIdsOfCase.has(caseRunId) ? caseRunId : row.id
      addEdge("issue-link", sourceId, issue.id)
      issueCursor += CHAIN_ISSUE_HEIGHT + CHAIN_ROW_GAP
    }

    for (const requirementId of requirementIds) addEdge("requirement-case", requirementId, row.id)
    for (const result of siblings) addEdge("case-result", row.id, result.id)

    const resultsHeight =
      siblings.length === 0
        ? 0
        : siblings.length * CHAIN_RESULT_HEIGHT + (siblings.length - 1) * CHAIN_ROW_GAP
    const issuesHeight =
      issueSiblings.length === 0
        ? 0
        : issueSiblings.length * CHAIN_ISSUE_HEIGHT + (issueSiblings.length - 1) * CHAIN_ROW_GAP
    return Math.max(CHAIN_CASE_HEIGHT, resultsHeight, issuesHeight)
  }

  let cursorY = 0
  for (const requirement of requirements) {
    const group = casesByRequirement.get(requirement.id) ?? []
    const isCollapsed = collapsed.has(requirement.id)
    const descendantCount = group.reduce(
      (sum, row) =>
        sum +
        1 +
        (resultsByCase.get(row.id)?.length ?? 0) +
        (issuesByCase.get(row.id)?.length ?? 0),
      0,
    )

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
        for (const issue of issuesByCase.get(row.id) ?? []) hidden.add(issue.id)
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
    unlinkedIssues,
    totalCount: requirements.length + allCases.length + results.length + issues.length,
  }
}

// ---------------------------------------------------------------------------
// Read-model layout (P8 Task 9): input is the Task 8 snapshot nodes + edges.
// ---------------------------------------------------------------------------

/**
 * Card size per chain layer, in layout units (spec §5.4: 232–240px wide).
 *
 * This is the single source of truth for both sides of the canvas: the layout
 * positions every node with these values and the node cards in `nodes/*.tsx`
 * render their root `<article>` at exactly this width/height. Heights cover the
 * tallest content a card can show (two-line titles, wrapped badges, summary and
 * impact lines); text beyond that is clamped and clipped by the card itself.
 */
export const CHAIN_CARD_SIZES: Record<
  ChainSnapshotNode["kind"],
  { width: number; height: number }
> = {
  requirement: { width: 240, height: 120 },
  case: { width: 240, height: 160 },
  result: { width: 236, height: 160 },
  issue: { width: 232, height: 140 },
}

/** Horizontal gap between two layers of the read-model layout. */
export const CHAIN_READ_MODEL_LAYER_GAP = 96
/** Vertical gap between stacked results/issues of one node. */
export const CHAIN_READ_MODEL_ROW_GAP = 16
/** Vertical gap between two requirement groups. */
export const CHAIN_READ_MODEL_GROUP_GAP = 32

const READ_MODEL_LAYER_BY_KIND: Record<ChainSnapshotNode["kind"], ChainLayer> = {
  requirement: 0,
  case: 1,
  result: 2,
  issue: 3,
}

/** One visible node of the read-model layout. */
export type PositionedNode = {
  /** Backend node id (`<kind>:<decimal id>`). */
  id: string
  kind: ChainSnapshotNode["kind"]
  layer: ChainLayer
  /** Top-left coordinates in layout units. */
  x: number
  y: number
  width: number
  height: number
  /** The snapshot node this card renders; never mutated by the layout. */
  data: ChainSnapshotNode
  /** The data title, convenient for selection lists and tests. */
  title: string
  /** Visible requirement node ids linked to this node (own id for a requirement). */
  requirementIds: string[]
  /** Backend case node id; null for a requirement node. */
  caseNodeId: string | null
  /** Backend result node id when an issue hangs off a result; else null. */
  resultNodeId: string | null
  hasChildren: boolean
  descendantCount: number
  /** True only on a requirement node whose group is collapsed. */
  collapsed: boolean
}

/** One visible semantic edge of the read-model layout. */
export type LaidEdge = {
  /** Backend edge id, or a synthesized `<kind>:<source>:<target>` id. */
  id: string
  kind: ChainEdgeKindDTO | string
  sourceId: string
  targetId: string
}

export type ChainLayoutOptions = {
  /** Requirement node ids (full or decimal) whose cases/results/issues are hidden. */
  collapsed?: readonly string[] | ReadonlySet<string>
  /** Semantic edges of the snapshot; authoritative for linking. */
  edges?: readonly ChainSnapshotEdge[]
}

export type ChainLayoutResult = {
  /** Visible nodes in pre-order (requirement group, case, results, issues). */
  nodes: PositionedNode[]
  /** Links between visible nodes only. */
  edges: LaidEdge[]
  /** Tight bounding box of the visible nodes, in layout units. */
  bounds: ChainBounds
  visibleIds: string[]
  /** Nodes hidden by a collapsed requirement, sorted by decimal id. */
  hiddenIds: string[]
  /** Issues that cannot be attached to a case of this graph, sorted. */
  unlinkedIssues: ChainSnapshotNode[]
  /** Unique input nodes (including unlinked issues and synthetic cases). */
  totalCount: number
}

function chainDomainId(raw: string): string {
  const separator = raw.indexOf(":")
  return separator === -1 ? raw : raw.slice(separator + 1)
}

function chainKindOf(raw: string): string {
  const separator = raw.indexOf(":")
  return separator === -1 ? "" : raw.slice(0, separator)
}

function fullChainNodeId(kind: ChainSnapshotNode["kind"], raw: string): string {
  const value = raw.trim()
  if (value === "") return `${kind}:`
  return value.includes(":") ? value : `${kind}:${value}`
}

function normalizeDecimal(raw: string): string {
  const digits = raw.replace(/^0+(?=\d)/, "")
  return digits === "" ? "0" : digits
}

/** Compares two `<kind>:<decimal id>` ids numerically, without Number(). */
function compareDecimalIds(left: string, right: string): number {
  const a = normalizeDecimal(chainDomainId(left))
  const b = normalizeDecimal(chainDomainId(right))
  if (a.length !== b.length) return a.length - b.length
  if (a === b) return 0
  return a < b ? -1 : 1
}

function uniqueSnapshotNodes(nodes: readonly ChainSnapshotNode[]): ChainSnapshotNode[] {
  const seen = new Set<string>()
  const unique: ChainSnapshotNode[] = []
  for (const node of nodes) {
    if (node === null || typeof node !== "object") continue
    if (typeof node.id !== "string" || !(node.kind in READ_MODEL_LAYER_BY_KIND)) continue
    if (seen.has(node.id)) continue
    seen.add(node.id)
    unique.push(node)
  }
  return unique
}

function uniqueSnapshotEdges(edges: readonly ChainSnapshotEdge[]): ChainSnapshotEdge[] {
  const seen = new Set<string>()
  const unique: ChainSnapshotEdge[] = []
  for (const edge of edges) {
    if (edge === null || typeof edge !== "object") continue
    if (typeof edge.source !== "string" || typeof edge.target !== "string") continue
    const key = `${edge.kind}|${edge.source}|${edge.target}`
    if (seen.has(key)) continue
    seen.add(key)
    unique.push(edge)
  }
  return unique
}

/** Deterministic fallback order for parallel edges of the same pair. */
function compareSnapshotEdges(left: ChainSnapshotEdge, right: ChainSnapshotEdge): number {
  const leftKey = `${left.kind}|${left.source}|${left.target}|${left.id}`
  const rightKey = `${right.kind}|${right.source}|${right.target}|${right.id}`
  return leftKey < rightKey ? -1 : leftKey > rightKey ? 1 : 0
}

function syntheticCaseNode(id: string, title: string): ChainSnapshotNode {
  return { id, kind: "case", title, status: "" }
}

function appendToMap<K, V>(map: Map<K, V[]>, key: K, value: V): void {
  const existing = map.get(key)
  if (existing === undefined) map.set(key, [value])
  else existing.push(value)
}

/**
 * Lays the Task 8 read-model nodes out as a four-layer graph, preserving the
 * order the backend returned for every layer (spec §5.1).
 *
 * The layout is a pure function of the (ordered) nodes + visible set; it never
 * calls `Number()` on an id. Coordinates keep the same meaning as the legacy
 * layout (top-left corner in layout units), but sizes follow the wider React
 * Flow cards of spec §5.4.
 */
export function layoutChain(
  nodes: readonly ChainSnapshotNode[],
  options: ChainLayoutOptions = {},
): ChainLayoutResult {
  const inputNodes = uniqueSnapshotNodes(nodes)
  const inputEdges = uniqueSnapshotEdges(options.edges ?? []).sort(compareSnapshotEdges)

  // Backend order is authoritative (requirements/cases `sortOrder,id`, results
  // `attempt desc`, issues `severity rank,created_at desc`); filter each layer
  // without sorting so the canvas mirrors the API response.
  const requirements = inputNodes.filter((node) => node.kind === "requirement")
  const realCases = inputNodes.filter((node) => node.kind === "case")
  const results = inputNodes.filter((node) => node.kind === "result")
  const issues = inputNodes.filter((node) => node.kind === "issue")

  const requirementById = new Map(requirements.map((node) => [node.id, node]))
  const resultByDomain = new Map(results.map((node) => [chainDomainId(node.id), node]))
  const issueById = new Map(issues.map((node) => [node.id, node]))

  // --- Snapshot edges: requirement links, case→result links, issue attachments.
  const requirementIdsByCase = new Map<string, Set<string>>()
  const caseIdByResult = new Map<string, string>()
  const resultIssueEdges: ChainSnapshotEdge[] = []
  const caseIssueEdges: ChainSnapshotEdge[] = []

  for (const edge of inputEdges) {
    const sourceKind = chainKindOf(edge.source)
    const targetKind = chainKindOf(edge.target)

    if (edge.kind === "requirement_case" || edge.kind === "impact_case") {
      if (sourceKind !== "requirement" || targetKind !== "case") continue
      const ids = requirementIdsByCase.get(edge.target)
      if (ids === undefined) requirementIdsByCase.set(edge.target, new Set([edge.source]))
      else ids.add(edge.source)
      continue
    }
    if (edge.kind === "case_result") {
      if (sourceKind !== "case" || targetKind !== "result") continue
      if (!caseIdByResult.has(edge.target)) caseIdByResult.set(edge.target, edge.source)
      continue
    }
    if (edge.kind === "result_issue") {
      if (sourceKind === "result" && targetKind === "issue") resultIssueEdges.push(edge)
      continue
    }
    if (edge.kind === "case_issue") {
      if (sourceKind === "case" && targetKind === "issue") caseIssueEdges.push(edge)
    }
  }

  // --- Cases: keep results visible through a synthetic case when a result's
  // case node was filtered out of the response (nothing is silently dropped).
  const caseById = new Map<string, ChainSnapshotNode>(realCases.map((node) => [node.id, node]))
  const syntheticCaseIds = new Set<string>()
  const resolvedCaseByResult = new Map<string, string>()

  function ensureCase(caseId: string, title: string): string {
    if (caseById.has(caseId)) return caseId
    caseById.set(caseId, syntheticCaseNode(caseId, title === "" ? "未知用例" : title))
    syntheticCaseIds.add(caseId)
    return caseId
  }

  for (const result of results) {
    let caseId = caseIdByResult.get(result.id)
    if (caseId === undefined) {
      const raw = (result.caseNodeId ?? "").trim()
      caseId =
        raw === "" ? `case:unresolved-${chainDomainId(result.id)}` : fullChainNodeId("case", raw)
    }
    if (chainKindOf(caseId) !== "case") caseId = fullChainNodeId("case", caseId)
    const title = result.title ?? ""
    resolvedCaseByResult.set(result.id, ensureCase(caseId, title))
  }

  // --- Issue attachments: result_issue wins, then case_issue, then the DTO
  // fallback (caseRunId hit first, then caseNodeId) mirroring spec §5.1.
  const attachmentByIssue = new Map<string, { caseId: string; resultId: string | null }>()
  for (const edge of resultIssueEdges) {
    if (!issueById.has(edge.target) || attachmentByIssue.has(edge.target)) continue
    const caseId = resolvedCaseByResult.get(edge.source)
    if (caseId === undefined) continue
    attachmentByIssue.set(edge.target, { caseId, resultId: edge.source })
  }
  for (const edge of caseIssueEdges) {
    if (!issueById.has(edge.target) || attachmentByIssue.has(edge.target)) continue
    if (!caseById.has(edge.source)) continue
    attachmentByIssue.set(edge.target, { caseId: edge.source, resultId: null })
  }
  for (const issue of issues) {
    if (attachmentByIssue.has(issue.id)) continue
    const caseRunId = (issue.caseRunId ?? "").trim()
    if (caseRunId !== "") {
      const result = resultByDomain.get(caseRunId)
      if (result !== undefined) {
        const caseId = resolvedCaseByResult.get(result.id)
        if (caseId !== undefined) {
          attachmentByIssue.set(issue.id, { caseId, resultId: result.id })
          continue
        }
      }
    }
    const rawCaseId = (issue.caseNodeId ?? "").trim()
    if (rawCaseId === "") continue
    const caseId = fullChainNodeId("case", rawCaseId)
    if (caseById.has(caseId)) attachmentByIssue.set(issue.id, { caseId, resultId: null })
  }

  // --- Children grouped by case; unlinked issues stay in a side list.
  const resultsByCase = new Map<string, ChainSnapshotNode[]>()
  for (const result of results) {
    const caseId = resolvedCaseByResult.get(result.id)
    if (caseId !== undefined) appendToMap(resultsByCase, caseId, result)
  }
  const issuesByCase = new Map<string, ChainSnapshotNode[]>()
  const issuesByResult = new Map<string, ChainSnapshotNode[]>()
  for (const issue of issues) {
    const attachment = attachmentByIssue.get(issue.id)
    if (attachment === undefined) continue
    appendToMap(issuesByCase, attachment.caseId, issue)
    if (attachment.resultId !== null) appendToMap(issuesByResult, attachment.resultId, issue)
  }
  const unlinkedIssues = issues.filter((issue) => !attachmentByIssue.has(issue.id))

  // --- Requirement grouping: a case follows its earliest visible requirement;
  // cases without a visible requirement stay in an unassigned group.
  const requirementOrder = new Map<string, number>()
  for (const [index, requirement] of requirements.entries()) {
    requirementOrder.set(requirement.id, index)
  }

  function linkedRequirementIds(caseId: string): string[] {
    const ids = requirementIdsByCase.get(caseId)
    if (ids === undefined) return []
    return [...ids]
      .filter((id) => requirementById.has(id))
      .sort((left, right) => {
        const leftOrder = requirementOrder.get(left) ?? Number.MAX_SAFE_INTEGER
        const rightOrder = requirementOrder.get(right) ?? Number.MAX_SAFE_INTEGER
        if (leftOrder !== rightOrder) return leftOrder - rightOrder
        return compareDecimalIds(left, right)
      })
  }

  // Map insertion order: backend-ordered real cases first, then synthetic
  // cases in the order their orphan result/issue was encountered.
  const allCases = [...caseById.values()]
  const casesByRequirement = new Map<string, ChainSnapshotNode[]>()
  for (const requirement of requirements) casesByRequirement.set(requirement.id, [])
  const unassignedCases: ChainSnapshotNode[] = []
  for (const caseNode of allCases) {
    const [primary] = linkedRequirementIds(caseNode.id)
    if (primary === undefined) unassignedCases.push(caseNode)
    else casesByRequirement.get(primary)?.push(caseNode)
  }

  const collapsed = new Set<string>(options.collapsed ?? [])
  const isCollapsed = (requirement: ChainSnapshotNode): boolean =>
    collapsed.has(requirement.id) || collapsed.has(chainDomainId(requirement.id))

  // --- Position the visible nodes.
  const positioned: PositionedNode[] = []
  const visibleIds = new Set<string>()
  const hiddenIds = new Set<string>()
  const sizes = CHAIN_CARD_SIZES
  const caseX = sizes.requirement.width + CHAIN_READ_MODEL_LAYER_GAP
  const resultX = caseX + sizes.case.width + CHAIN_READ_MODEL_LAYER_GAP
  const issueX = resultX + sizes.result.width + CHAIN_READ_MODEL_LAYER_GAP

  function pushCase(caseNode: ChainSnapshotNode, y: number): number {
    const requirementIds = linkedRequirementIds(caseNode.id)
    const caseResults = resultsByCase.get(caseNode.id) ?? []
    const caseIssues = issuesByCase.get(caseNode.id) ?? []
    const childCount = caseResults.length + caseIssues.length

    positioned.push({
      id: caseNode.id,
      kind: "case",
      layer: 1,
      x: caseX,
      y,
      width: sizes.case.width,
      height: sizes.case.height,
      data: caseNode,
      title: caseNode.title,
      requirementIds,
      caseNodeId: caseNode.id,
      resultNodeId: null,
      hasChildren: childCount > 0,
      descendantCount: childCount,
      collapsed: false,
    })
    visibleIds.add(caseNode.id)

    let resultCursor = y
    for (const result of caseResults) {
      const issueCount = issuesByResult.get(result.id)?.length ?? 0
      positioned.push({
        id: result.id,
        kind: "result",
        layer: 2,
        x: resultX,
        y: resultCursor,
        width: sizes.result.width,
        height: sizes.result.height,
        data: result,
        title: result.title,
        requirementIds,
        caseNodeId: caseNode.id,
        resultNodeId: result.id,
        hasChildren: issueCount > 0,
        descendantCount: issueCount,
        collapsed: false,
      })
      visibleIds.add(result.id)
      resultCursor += sizes.result.height + CHAIN_READ_MODEL_ROW_GAP
    }

    let issueCursor = y
    for (const issue of caseIssues) {
      const attachment = attachmentByIssue.get(issue.id)
      positioned.push({
        id: issue.id,
        kind: "issue",
        layer: 3,
        x: issueX,
        y: issueCursor,
        width: sizes.issue.width,
        height: sizes.issue.height,
        data: issue,
        title: issue.title,
        requirementIds,
        caseNodeId: caseNode.id,
        resultNodeId: attachment?.resultId ?? null,
        hasChildren: false,
        descendantCount: 0,
        collapsed: false,
      })
      visibleIds.add(issue.id)
      issueCursor += sizes.issue.height + CHAIN_READ_MODEL_ROW_GAP
    }

    const resultsHeight =
      caseResults.length === 0
        ? 0
        : caseResults.length * sizes.result.height +
          (caseResults.length - 1) * CHAIN_READ_MODEL_ROW_GAP
    const issuesHeight =
      caseIssues.length === 0
        ? 0
        : caseIssues.length * sizes.issue.height +
          (caseIssues.length - 1) * CHAIN_READ_MODEL_ROW_GAP
    return Math.max(sizes.case.height, resultsHeight, issuesHeight)
  }

  let cursorY = 0
  for (const requirement of requirements) {
    const group = casesByRequirement.get(requirement.id) ?? []
    const collapsedGroup = isCollapsed(requirement)
    const descendantCount = group.reduce(
      (sum, caseNode) =>
        sum +
        1 +
        (resultsByCase.get(caseNode.id)?.length ?? 0) +
        (issuesByCase.get(caseNode.id)?.length ?? 0),
      0,
    )

    positioned.push({
      id: requirement.id,
      kind: "requirement",
      layer: 0,
      x: 0,
      y: cursorY,
      width: sizes.requirement.width,
      height: sizes.requirement.height,
      data: requirement,
      title: requirement.title,
      requirementIds: [requirement.id],
      caseNodeId: null,
      resultNodeId: null,
      hasChildren: group.length > 0,
      descendantCount,
      collapsed: collapsedGroup,
    })
    visibleIds.add(requirement.id)

    if (collapsedGroup) {
      for (const caseNode of group) {
        hiddenIds.add(caseNode.id)
        for (const result of resultsByCase.get(caseNode.id) ?? []) hiddenIds.add(result.id)
        for (const issue of issuesByCase.get(caseNode.id) ?? []) hiddenIds.add(issue.id)
      }
      cursorY += sizes.requirement.height + CHAIN_READ_MODEL_GROUP_GAP
      continue
    }

    let groupCursor = cursorY
    for (const caseNode of group) {
      groupCursor += pushCase(caseNode, groupCursor) + CHAIN_READ_MODEL_ROW_GAP
    }
    if (group.length > 0) groupCursor -= CHAIN_READ_MODEL_ROW_GAP
    cursorY = Math.max(cursorY + sizes.requirement.height, groupCursor) + CHAIN_READ_MODEL_GROUP_GAP
  }

  let unassignedCursor = cursorY
  for (const caseNode of unassignedCases) {
    unassignedCursor += pushCase(caseNode, unassignedCursor) + CHAIN_READ_MODEL_ROW_GAP
  }

  // Nodes that are neither visible nor explicitly collapsed (defensive) are
  // reported as hidden; unlinked issues are listed separately, never hidden.
  for (const node of inputNodes) {
    if (visibleIds.has(node.id)) continue
    if (node.kind === "issue" && !attachmentByIssue.has(node.id)) continue
    hiddenIds.add(node.id)
  }

  // --- Visible semantic edges, in source pre-order. Requirement links only
  // keep the kinds the snapshot actually emitted (no invented coverage edge).
  const originalEdgeByTriple = new Map<string, ChainSnapshotEdge>()
  for (const edge of inputEdges) {
    originalEdgeByTriple.set(`${edge.kind}|${edge.source}|${edge.target}`, edge)
  }
  const laidEdges: LaidEdge[] = []
  const seenEdgeKeys = new Set<string>()

  function pushEdge(kind: string, sourceId: string, targetId: string): void {
    if (!visibleIds.has(sourceId) || !visibleIds.has(targetId)) return
    const key = `${kind}|${sourceId}|${targetId}`
    if (seenEdgeKeys.has(key)) return
    seenEdgeKeys.add(key)
    const original = originalEdgeByTriple.get(key)
    laidEdges.push({
      id: original?.id ?? `${kind}:${chainDomainId(sourceId)}:${chainDomainId(targetId)}`,
      kind,
      sourceId,
      targetId,
    })
  }

  for (const node of positioned) {
    if (node.kind === "case") {
      for (const requirementId of node.requirementIds) {
        for (const kind of ["requirement_case", "impact_case"] as const) {
          if (originalEdgeByTriple.has(`${kind}|${requirementId}|${node.id}`)) {
            pushEdge(kind, requirementId, node.id)
          }
        }
      }
      continue
    }
    if (node.kind === "result") {
      if (node.caseNodeId !== null) pushEdge("case_result", node.caseNodeId, node.id)
      continue
    }
    if (node.kind !== "issue") continue
    const attachment = attachmentByIssue.get(node.id)
    if (attachment === undefined) continue
    if (attachment.resultId !== null) pushEdge("result_issue", attachment.resultId, node.id)
    else pushEdge("case_issue", attachment.caseId, node.id)
  }

  // --- Bounds.
  let bounds: ChainBounds = { x: 0, y: 0, width: 0, height: 0 }
  if (positioned.length > 0) {
    let minX = Number.POSITIVE_INFINITY
    let minY = Number.POSITIVE_INFINITY
    let maxX = Number.NEGATIVE_INFINITY
    let maxY = Number.NEGATIVE_INFINITY
    for (const node of positioned) {
      minX = Math.min(minX, node.x)
      minY = Math.min(minY, node.y)
      maxX = Math.max(maxX, node.x + node.width)
      maxY = Math.max(maxY, node.y + node.height)
    }
    bounds = { x: minX, y: minY, width: maxX - minX, height: maxY - minY }
  }

  const sortedHiddenIds = [...hiddenIds].sort((left, right) => {
    const diff = compareDecimalIds(left, right)
    if (diff !== 0) return diff
    return left < right ? -1 : left > right ? 1 : 0
  })

  return {
    nodes: positioned,
    edges: laidEdges,
    bounds,
    visibleIds: positioned.map((node) => node.id),
    hiddenIds: sortedHiddenIds,
    unlinkedIssues,
    totalCount: inputNodes.length + syntheticCaseIds.size,
  }
}

/**
 * Requirement node ids that should start collapsed: all of them once the whole
 * graph exceeds the threshold, otherwise none (spec §5.5: >200 auto-collapse).
 */
export function autoCollapsedChainRequirements(
  nodes: readonly ChainSnapshotNode[],
  threshold: number = CHAIN_AUTO_COLLAPSE_THRESHOLD,
): string[] {
  const unique = uniqueSnapshotNodes(nodes)
  if (unique.length <= threshold) return []
  return unique
    .filter((node) => node.kind === "requirement")
    .map((node) => node.id)
    .sort(compareDecimalIds)
}
