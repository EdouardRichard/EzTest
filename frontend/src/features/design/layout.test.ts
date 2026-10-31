import { describe, expect, it } from "vitest"

import { layoutDesignGraph } from "./layout"
import { designGraphFixture } from "./test-fixtures"
import type { DesignGraph } from "./types"

const TIMESTAMP = "2026-10-31T12:00:00Z"

type DesignLayout = ReturnType<typeof layoutDesignGraph>

function nodeById(layout: DesignLayout, id: string) {
  const node = layout.nodes.find((candidate) => candidate.id === id)
  if (node === undefined) throw new Error(`missing layout node ${id}`)
  return node
}

function graphWithCases(count: number): DesignGraph {
  const cases = Array.from({ length: count }, (_, index) => ({
    case_id: String(70_000 + index),
    current_version_id: String(71_000 + index),
    revision: 1,
    title: `用例 ${index + 1}`,
    priority: "P1" as const,
    status: "draft" as const,
    default_version_sut_id: "1002",
    version_sut_ids: ["1002"],
    page_ids: [],
    api_ids: [],
    step_count: 2,
    assertion_count: 1,
    created_at: TIMESTAMP,
  }))
  return {
    ...designGraphFixture,
    requirements: [
      {
        requirement_id: "6001",
        requirement_version_id: "6101",
        revision: 1,
        title: "退款到账时效",
        status: "clarified",
        design_status: "draft",
        target_version_sut_ids: ["1002"],
        scenarios: [
          {
            id: "5001",
            requirement_version_id: "6101",
            name: "正常退款",
            description: "",
            version_sut_ids: ["1002"],
            page_ids: [],
            api_ids: [],
            sort: 0,
            cases,
          },
        ],
      },
    ],
  }
}

describe("layoutDesignGraph", () => {
  it("returns no nodes or edges for an empty graph", () => {
    const layout = layoutDesignGraph({ ...designGraphFixture, requirements: [] })
    expect(layout.nodes).toEqual([])
    expect(layout.edges).toEqual([])
  })

  it("lays the requirement/scenario/case hierarchy out in columns", () => {
    const layout = layoutDesignGraph(designGraphFixture)

    const nodeIds = layout.nodes.map((node) => node.id)
    expect(nodeIds).toContain("requirement-6001")
    expect(nodeIds).toContain("scenario-5001")
    expect(nodeIds).toContain("case-7001")
    expect(nodeIds).toContain("requirement-6002")

    const requirement = nodeById(layout, "requirement-6001")
    const scenario = nodeById(layout, "scenario-5001")
    const designCase = nodeById(layout, "case-7001")
    expect(requirement.type).toBe("requirement")
    expect(scenario.type).toBe("scenario")
    expect(designCase.type).toBe("case")

    expect(requirement.position.x).toBeLessThan(scenario.position.x)
    expect(scenario.position.x).toBeLessThan(designCase.position.x)
    for (const node of layout.nodes) {
      expect(Number.isFinite(node.position.x)).toBe(true)
      expect(Number.isFinite(node.position.y)).toBe(true)
    }

    expect(layout.edges).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ source: "requirement-6001", target: "scenario-5001" }),
        expect.objectContaining({ source: "scenario-5001", target: "case-7001" }),
      ]),
    )
  })

  it("is deterministic for the same graph", () => {
    expect(layoutDesignGraph(designGraphFixture)).toEqual(layoutDesignGraph(designGraphFixture))
  })

  it("keeps cross-SUT targets on the case node data", () => {
    const layout = layoutDesignGraph(designGraphFixture)
    const node = layout.nodes.find((candidate) => candidate.id === "case-7003")
    expect(node?.data.kind).toBe("case")
    if (node?.data.kind !== "case") throw new Error("expected a case node")
    expect(node.data.case.version_sut_ids).toEqual(["1001", "1002"])
    expect(node.data.case.priority).toBe("P0")
  })

  it("lays out 200 cases with unique, deterministic coordinates", () => {
    const layout = layoutDesignGraph(graphWithCases(200))
    expect(layout.nodes).toHaveLength(202)
    expect(layout.edges).toHaveLength(201)

    const coordinates = new Set(layout.nodes.map((node) => `${node.position.x},${node.position.y}`))
    expect(coordinates.size).toBe(layout.nodes.length)
    expect(layoutDesignGraph(graphWithCases(200))).toEqual(layout)
  })
})
