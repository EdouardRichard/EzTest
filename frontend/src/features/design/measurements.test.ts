import type { NodeChange } from "@xyflow/react"
import { describe, expect, it } from "vitest"

import { mergeMeasuredDimensions } from "./measurements"

function dimensionsChange(id: string, width: number, height: number): NodeChange {
  return { id, type: "dimensions", dimensions: { width, height } }
}

describe("mergeMeasuredDimensions", () => {
  it("records positive dimensions reported by a dimensions change", () => {
    const merged = mergeMeasuredDimensions({}, [dimensionsChange("requirement-1", 180, 48)])

    expect(merged).toEqual({ "requirement-1": { width: 180, height: 48 } })
  })

  it("ignores non-dimensions changes and keeps the current record", () => {
    const current = { "requirement-1": { width: 180, height: 48 } }
    const changes: NodeChange[] = [
      { id: "requirement-1", type: "select", selected: true },
      { id: "requirement-1", type: "position", position: { x: 10, y: 20 } },
    ]

    expect(mergeMeasuredDimensions(current, changes)).toBe(current)
  })

  it("keeps the same record when the reported size is unchanged", () => {
    const current = { "requirement-1": { width: 180, height: 48 } }

    expect(mergeMeasuredDimensions(current, [dimensionsChange("requirement-1", 180, 48)])).toBe(
      current,
    )
  })

  it("ignores zero width or height, since unmeasured nodes report them", () => {
    const current = { "requirement-1": { width: 180, height: 48 } }
    const changes = [
      dimensionsChange("requirement-1", 0, 48),
      dimensionsChange("requirement-1", 180, 0),
      dimensionsChange("scenario-1", 0, 0),
    ]

    expect(mergeMeasuredDimensions(current, changes)).toBe(current)
  })

  it("updates only the changed node and shares the untouched entries", () => {
    const current = {
      "requirement-1": { width: 180, height: 48 },
      "scenario-1": { width: 180, height: 48 },
    }

    const merged = mergeMeasuredDimensions(current, [dimensionsChange("scenario-1", 200, 60)])

    expect(merged).not.toBe(current)
    expect(merged["scenario-1"]).toEqual({ width: 200, height: 60 })
    expect(merged["requirement-1"]).toBe(current["requirement-1"])
  })
})
