import type { ChainNodeKind, ChainNodeView } from "./types"

/**
 * Deterministic four-column chain layout (spec §9.4):
 * requirement → scenario/case → wave/run → issue/report/closure.
 *
 * Positions are local UI state only; the server never persists layout. The
 * same graph always yields the same coordinates, and no node is dropped.
 */
export const CHAIN_COLUMN_X = [0, 340, 680, 1020] as const

/** Vertical distance between two stacked nodes; cards are ~96px tall. */
export const CHAIN_ROW_GAP = 132

/** Horizontal band used to place the detail/closing nodes. */
const COLUMN_INDEX: Record<ChainNodeKind, number> = {
  requirement: 0,
  scenario: 1,
  case_version: 1,
  wave: 2,
  case_run: 2,
  issue: 3,
  report: 3,
  closure: 3,
}

export type ChainLayoutNode = {
  node: ChainNodeView
  position: { x: number; y: number }
}

function compareRefIds(left: ChainNodeView, right: ChainNodeView): number {
  try {
    const leftId = BigInt(left.ref_id)
    const rightId = BigInt(right.ref_id)
    if (leftId === rightId) return left.id.localeCompare(right.id)
    return leftId < rightId ? -1 : 1
  } catch {
    return left.id.localeCompare(right.id)
  }
}

/** Pure column layout; requirement nodes lead each column and keep graph order stable. */
export function layoutChain(nodes: ChainNodeView[]): ChainLayoutNode[] {
  const columns: ChainNodeView[][] = [[], [], [], []]
  for (const node of nodes) {
    columns[COLUMN_INDEX[node.kind]].push(node)
  }

  const layout: ChainLayoutNode[] = []
  columns.forEach((columnNodes, columnIndex) => {
    columnNodes.sort(compareRefIds)
    columnNodes.forEach((node, rowIndex) => {
      layout.push({
        node,
        position: {
          x: CHAIN_COLUMN_X[columnIndex],
          y: rowIndex * CHAIN_ROW_GAP,
        },
      })
    })
  })
  return layout
}
