/**
 * Deterministic tidy-tree layout for the P5 case mindmap (spec §9.2).
 *
 * The layout is a pure function of the graph plus the collapsed set: sibling
 * order comes from `sortOrder` (tie-broken by the decimal node id) and the
 * depth comes from the parent chain, so the same input always yields the same
 * coordinates regardless of array order. Cards are laid out with one column per
 * leaf: a node is centred over the span of its whole child subtrees, which
 * keeps every depth readable without overlapping cards.
 */

import type { CaseNodeView } from "./types"

/** Card size in layout units before any canvas zoom is applied. */
export const DEFAULT_NODE_WIDTH = 216
export const DEFAULT_NODE_HEIGHT = 88
/** Gap between two sibling subtrees. */
export const DEFAULT_HORIZONTAL_GAP = 32
/** Gap between two depth levels (parent row and child row). */
export const DEFAULT_VERTICAL_GAP = 40

/** Layout knobs; only `collapsed` is needed by the canvas. */
export type MindmapLayoutOptions = {
  /** Scenario ids whose descendants are hidden from the layout. */
  collapsed?: readonly string[] | ReadonlySet<string>
  nodeWidth?: number
  nodeHeight?: number
  horizontalGap?: number
  verticalGap?: number
}

/** One visible card with top-left coordinates in layout units. */
export type LayoutNode = {
  id: string
  /** The DTO this card renders; never mutated by the layout. */
  node: CaseNodeView
  /** 0 for roots; grows downwards. */
  depth: number
  x: number
  y: number
  w: number
  h: number
  /** True when the underlying graph has children, collapsed or not. */
  hasChildren: boolean
  /** Descendants in the full graph; used by the collapsed-count badge. */
  descendantCount: number
}

/** One visible parent → child link. */
export type LayoutEdge = {
  id: string
  parentId: string
  childId: string
}

export type LayoutBounds = {
  x: number
  y: number
  width: number
  height: number
}

export type LayoutResult = {
  /** Visible cards in pre-order (the canvas keyboard navigation order). */
  nodes: LayoutNode[]
  /** Links between visible cards only; hidden subtrees contribute no edges. */
  edges: LayoutEdge[]
  /** Tight bounding box of the visible cards, in layout units. */
  bounds: LayoutBounds
  visibleIds: string[]
  /** Nodes hidden by a collapsed ancestor, sorted by id. */
  hiddenIds: string[]
}

type BuildNode = {
  node: CaseNodeView
  children: BuildNode[]
  subtreeWidth: number
  x: number
  y: number
  depth: number
}

/** Sibling order: server `sortOrder`, then the decimal id for determinism. */
function compareNodes(a: CaseNodeView, b: CaseNodeView): number {
  if (a.sortOrder !== b.sortOrder) return a.sortOrder - b.sortOrder
  if (a.id === b.id) return 0
  return a.id < b.id ? -1 : 1
}

function toCollapsedSet(
  collapsed: readonly string[] | ReadonlySet<string> | undefined,
): Set<string> {
  if (collapsed === undefined) return new Set<string>()
  return new Set<string>(collapsed as Iterable<string>)
}

/**
 * Turns the flat DTO list into a positioned mindmap.
 *
 * Robustness: nodes with a missing parent (or themselves as parent) become
 * roots, and parent cycles are broken so every node still appears exactly once
 * — the canvas must never lose a case because the server graph is odd.
 */
export function layoutMindmap(
  nodes: CaseNodeView[],
  options: MindmapLayoutOptions = {},
): LayoutResult {
  const nodeWidth = options.nodeWidth ?? DEFAULT_NODE_WIDTH
  const nodeHeight = options.nodeHeight ?? DEFAULT_NODE_HEIGHT
  const horizontalGap = options.horizontalGap ?? DEFAULT_HORIZONTAL_GAP
  const verticalGap = options.verticalGap ?? DEFAULT_VERTICAL_GAP
  const collapsed = toCollapsedSet(options.collapsed)

  const nodeById = new Map<string, CaseNodeView>()
  for (const node of nodes) nodeById.set(node.id, node)

  const childLists = new Map<string, CaseNodeView[]>()
  const rootCandidates: CaseNodeView[] = []
  for (const node of nodes) {
    const parentId = node.parentId
    const hasParent = parentId !== null && parentId !== node.id && nodeById.has(parentId)
    if (!hasParent) {
      rootCandidates.push(node)
      continue
    }
    const siblings = childLists.get(parentId)
    if (siblings === undefined) childLists.set(parentId, [node])
    else siblings.push(node)
  }
  for (const siblings of childLists.values()) siblings.sort(compareNodes)
  rootCandidates.sort(compareNodes)

  // Cycle/orphan safety: walk from the declared roots, then promote anything
  // still unreachable (a parent cycle has no root) to an extra root, in stable
  // order. Building with a `placed` set keeps each node exactly once.
  const sortedAll = [...nodes].sort(compareNodes)
  const reachable = new Set<string>()
  function markReachable(node: CaseNodeView): void {
    if (reachable.has(node.id)) return
    reachable.add(node.id)
    for (const child of childLists.get(node.id) ?? []) markReachable(child)
  }
  for (const root of rootCandidates) markReachable(root)
  for (const node of sortedAll) {
    if (reachable.has(node.id)) continue
    rootCandidates.push(node)
    markReachable(node)
  }

  const placed = new Set<string>()
  const preorder: BuildNode[] = []
  function build(node: CaseNodeView, depth: number): BuildNode {
    placed.add(node.id)
    const tree: BuildNode = { node, children: [], subtreeWidth: nodeWidth, x: 0, y: 0, depth }
    preorder.push(tree)
    if (collapsed.has(node.id)) return tree
    for (const child of childLists.get(node.id) ?? []) {
      if (placed.has(child.id)) continue
      tree.children.push(build(child, depth + 1))
    }
    return tree
  }

  const roots: BuildNode[] = []
  for (const candidate of rootCandidates) {
    if (placed.has(candidate.id)) continue
    roots.push(build(candidate, 0))
  }

  function measure(tree: BuildNode): number {
    if (tree.children.length === 0) {
      tree.subtreeWidth = nodeWidth
      return tree.subtreeWidth
    }
    let childrenWidth = 0
    for (const child of tree.children) childrenWidth += measure(child)
    childrenWidth += horizontalGap * (tree.children.length - 1)
    tree.subtreeWidth = Math.max(nodeWidth, childrenWidth)
    return tree.subtreeWidth
  }
  for (const root of roots) measure(root)

  function place(tree: BuildNode, left: number): void {
    tree.x = left + tree.subtreeWidth / 2 - nodeWidth / 2
    tree.y = tree.depth * (nodeHeight + verticalGap)
    if (tree.children.length === 0) return
    let childrenWidth = 0
    for (const child of tree.children) childrenWidth += child.subtreeWidth
    childrenWidth += horizontalGap * (tree.children.length - 1)
    let cursor = left + (tree.subtreeWidth - childrenWidth) / 2
    for (const child of tree.children) {
      place(child, cursor)
      cursor += child.subtreeWidth + horizontalGap
    }
  }
  let cursor = 0
  for (const root of roots) {
    place(root, cursor)
    cursor += root.subtreeWidth + horizontalGap
  }

  function descendantCountOf(id: string): number {
    const seen = new Set<string>([id])
    const stack = [...(childLists.get(id) ?? [])]
    let count = 0
    while (stack.length > 0) {
      const current = stack.pop()
      if (current === undefined || seen.has(current.id)) continue
      seen.add(current.id)
      count += 1
      for (const child of childLists.get(current.id) ?? []) stack.push(child)
    }
    return count
  }

  const layoutNodes: LayoutNode[] = preorder.map((tree) => ({
    id: tree.node.id,
    node: tree.node,
    depth: tree.depth,
    x: tree.x,
    y: tree.y,
    w: nodeWidth,
    h: nodeHeight,
    hasChildren: (childLists.get(tree.node.id)?.length ?? 0) > 0,
    descendantCount: descendantCountOf(tree.node.id),
  }))

  const edges: LayoutEdge[] = []
  for (const tree of preorder) {
    for (const child of tree.children) {
      edges.push({
        id: `${tree.node.id}->${child.node.id}`,
        parentId: tree.node.id,
        childId: child.node.id,
      })
    }
  }

  let bounds: LayoutBounds = { x: 0, y: 0, width: 0, height: 0 }
  if (layoutNodes.length > 0) {
    let minX = Number.POSITIVE_INFINITY
    let minY = Number.POSITIVE_INFINITY
    let maxX = Number.NEGATIVE_INFINITY
    let maxY = Number.NEGATIVE_INFINITY
    for (const node of layoutNodes) {
      minX = Math.min(minX, node.x)
      minY = Math.min(minY, node.y)
      maxX = Math.max(maxX, node.x + node.w)
      maxY = Math.max(maxY, node.y + node.h)
    }
    bounds = { x: minX, y: minY, width: maxX - minX, height: maxY - minY }
  }

  const visibleIds = preorder.map((tree) => tree.node.id)
  const hiddenIds = sortedAll
    .filter((node) => !placed.has(node.id))
    .map((node) => node.id)
    .sort()

  return { nodes: layoutNodes, edges, bounds, visibleIds, hiddenIds }
}
