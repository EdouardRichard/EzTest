import type { NodeChange } from "@xyflow/react"

/** Measured node box, in flow coordinates, keyed by node id. */
export type NodeMeasurements = Readonly<Record<string, { width: number; height: number }>>

/**
 * Keeps the measured box React Flow reports for each node (R2-D-03).
 *
 * The design canvas passes `nodes` as a controlled prop, so React Flow's
 * `dimensions` changes only survive when they are merged back into that prop.
 * `MiniMap` reads `node.internals.userNode.measured`; without this feedback it
 * renders the title and mask only, with zero node thumbnails.
 *
 * Non-dimensions changes and unmeasured (0×0) boxes are ignored. When no size
 * actually changed the current record is returned unchanged, so the `useState`
 * update bails out and measurement → render → measurement cannot loop.
 */
export function mergeMeasuredDimensions(
  measurements: NodeMeasurements,
  changes: NodeChange[],
): NodeMeasurements {
  let next: Record<string, { width: number; height: number }> | null = null
  for (const change of changes) {
    if (change.type !== "dimensions") continue
    const dimensions = change.dimensions
    // jsdom and hidden nodes report 0×0 until a real layout pass.
    if (dimensions === undefined || !(dimensions.width > 0) || !(dimensions.height > 0)) continue
    const current = next?.[change.id] ?? measurements[change.id]
    if (
      current !== undefined &&
      current.width === dimensions.width &&
      current.height === dimensions.height
    ) {
      continue
    }
    if (next === null) next = { ...measurements }
    next[change.id] = { width: dimensions.width, height: dimensions.height }
  }
  return next ?? measurements
}
