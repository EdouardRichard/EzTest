import { describe, expect, it } from "vitest"

import {
  autoCollapsedRequirements,
  CHAIN_AUTO_COLLAPSE_THRESHOLD,
  CHAIN_CASE_WIDTH,
  CHAIN_LAYER_GAP,
  CHAIN_REQUIREMENT_HEIGHT,
  CHAIN_REQUIREMENT_WIDTH,
  CHAIN_RESULT_HEIGHT,
  CHAIN_ROW_GAP,
  type ChainCaseInput,
  type ChainRequirementInput,
  type ChainResultInput,
  isAbnormalResultStatus,
  layoutChain,
  matchesResultFilter,
} from "./chain-layout"

const R1 = "1844674407370955101"
const R2 = "1844674407370955102"
const C1 = "1844674407370955201"
const C2 = "1844674407370955202"
const C3 = "1844674407370955203"
const CR1 = "1844674407370955301"
const CR2 = "1844674407370955302"
const CR3 = "1844674407370955303"

function requirement(
  id: string,
  title: string,
  sortOrder: number,
  overrides: Partial<ChainRequirementInput> = {},
): ChainRequirementInput {
  return { id, title, status: "clarified", sortOrder, ...overrides }
}

function caseInput(
  id: string,
  title: string,
  requirementIds: string[],
  sortOrder: number,
  overrides: Partial<ChainCaseInput> = {},
): ChainCaseInput {
  return { id, title, requirementIds, sortOrder, status: "executed", ...overrides }
}

function resultInput(
  id: string,
  caseNodeId: string,
  status: ChainResultInput["status"],
  sortOrder: number,
  overrides: Partial<ChainResultInput> = {},
): ChainResultInput {
  return { id, caseNodeId, status, sortOrder, ...overrides }
}

function baseInput() {
  return {
    requirements: [requirement(R1, "登录需求", 0), requirement(R2, "下单需求", 1)],
    cases: [
      caseInput(C1, "登录成功", [R1], 0),
      caseInput(C2, "提交订单", [R2], 1),
      caseInput(C3, "未关联用例", [], 2),
    ],
    results: [
      resultInput(CR1, C1, "passed", 0),
      resultInput(CR2, C2, "failed", 1),
      resultInput(CR3, C3, "blocked", 2),
    ],
  }
}

function nodeById(result: ReturnType<typeof layoutChain>, id: string) {
  const node = result.nodes.find((candidate) => candidate.id === id)
  if (node === undefined) throw new Error(`missing node ${id}`)
  return node
}

describe("layoutChain", () => {
  it("returns an empty layout for an empty chain", () => {
    const result = layoutChain({ requirements: [], cases: [], results: [] })

    expect(result.nodes).toEqual([])
    expect(result.edges).toEqual([])
    expect(result.visibleIds).toEqual([])
    expect(result.hiddenIds).toEqual([])
    expect(result.totalCount).toBe(0)
    expect(result.bounds).toEqual({ x: 0, y: 0, width: 0, height: 0 })
  })

  it("lays requirements, cases and results out in three deterministic layers", () => {
    const result = layoutChain(baseInput())

    expect(result.nodes).toHaveLength(8)
    expect(result.totalCount).toBe(8)

    const requirementNode = nodeById(result, R1)
    const caseNode = nodeById(result, C1)
    const resultNode = nodeById(result, CR1)

    expect(requirementNode.kind).toBe("requirement")
    expect(requirementNode.layer).toBe(0)
    expect(requirementNode.x).toBe(0)
    expect(requirementNode.y).toBe(0)
    expect(requirementNode.w).toBe(CHAIN_REQUIREMENT_WIDTH)
    expect(requirementNode.h).toBe(CHAIN_REQUIREMENT_HEIGHT)

    const caseX = CHAIN_REQUIREMENT_WIDTH + CHAIN_LAYER_GAP
    expect(caseNode.kind).toBe("case")
    expect(caseNode.layer).toBe(1)
    expect(caseNode.x).toBe(caseX)
    expect(caseNode.w).toBe(CHAIN_CASE_WIDTH)
    expect(caseNode.y).toBe(requirementNode.y)

    expect(resultNode.kind).toBe("result")
    expect(resultNode.layer).toBe(2)
    expect(resultNode.x).toBe(caseX + CHAIN_CASE_WIDTH + CHAIN_LAYER_GAP)
    expect(resultNode.y).toBe(caseNode.y)

    // The second requirement starts below everything the first group occupies.
    const secondRequirement = nodeById(result, R2)
    expect(secondRequirement.y).toBeGreaterThan(caseNode.y + caseNode.h)
    expect(nodeById(result, C2).y).toBeGreaterThanOrEqual(secondRequirement.y)
  })

  it("is deterministic regardless of the input array order", () => {
    const input = baseInput()
    const shuffled = {
      requirements: [input.requirements[1], input.requirements[0]],
      cases: [input.cases[2], input.cases[0], input.cases[1]],
      results: [input.results[1], input.results[2], input.results[0]],
    }

    const forward = layoutChain(input)
    const backward = layoutChain(shuffled)

    expect(backward.visibleIds).toEqual(forward.visibleIds)
    expect(backward.nodes).toEqual(forward.nodes)
    expect(backward.edges).toEqual(forward.edges)
    expect(backward.bounds).toEqual(forward.bounds)
  })

  it("orders requirement groups, sibling cases and stacked results", () => {
    const input = {
      requirements: [requirement(R2, "下单需求", 1), requirement(R1, "登录需求", 0)],
      cases: [
        caseInput(C2, "提交订单", [R2], 10),
        caseInput(C1, "登录成功", [R1], 0),
        caseInput(C3, "未关联用例", [], 20),
      ],
      results: [
        resultInput(CR1, C1, "passed", 0),
        resultInput(CR2, C2, "failed", 10),
        resultInput(CR3, C3, "blocked", 20),
      ],
    }

    const result = layoutChain(input)

    expect(result.visibleIds).toEqual([R1, C1, CR1, R2, C2, CR2, C3, CR3])
    expect(nodeById(result, R1).y).toBeLessThan(nodeById(result, R2).y)
    expect(nodeById(result, C1).y).toBeLessThan(nodeById(result, C2).y)
    expect(nodeById(result, CR1).y).toBeLessThan(nodeById(result, CR2).y)
    expect(nodeById(result, C3).y).toBeGreaterThan(nodeById(result, CR2).y)
  })

  it("stacks several results of one case vertically", () => {
    const retry = resultInput(CR3, C1, "failed", 1)
    const result = layoutChain({
      requirements: [requirement(R1, "登录需求", 0)],
      cases: [caseInput(C1, "登录成功", [R1], 0)],
      results: [resultInput(CR1, C1, "passed", 0), retry],
    })

    const first = nodeById(result, CR1)
    const second = nodeById(result, CR3)
    expect(second.y).toBe(first.y + CHAIN_RESULT_HEIGHT + CHAIN_ROW_GAP)
    expect(second.x).toBe(first.x)
  })

  it("creates deterministic edges from requirements to cases and cases to results", () => {
    const result = layoutChain(baseInput())

    expect(result.edges.map((edge) => edge.id)).toEqual([
      `${R1}->${C1}`,
      `${C1}->${CR1}`,
      `${R2}->${C2}`,
      `${C2}->${CR2}`,
      `${C3}->${CR3}`,
    ])
    expect(result.edges.map((edge) => edge.kind)).toEqual([
      "requirement-case",
      "case-result",
      "requirement-case",
      "case-result",
      "case-result",
    ])
  })

  it("places a case linked to several requirements under its earliest group and links both", () => {
    const shared: ChainCaseInput = {
      ...caseInput(C1, "共享用例", [R2, R1], 0),
    }
    const result = layoutChain({
      requirements: [requirement(R1, "登录需求", 0), requirement(R2, "下单需求", 1)],
      cases: [shared],
      results: [],
    })

    expect(nodeById(result, C1).y).toBe(nodeById(result, R1).y)
    expect(result.edges.map((edge) => edge.id)).toEqual([`${R1}->${C1}`, `${R2}->${C1}`])
  })

  it("collapses one requirement group and hides its descendants", () => {
    const result = layoutChain(baseInput(), { collapsed: [R1] })

    expect(result.visibleIds).toEqual([R1, R2, C2, CR2, C3, CR3])
    expect(result.hiddenIds).toEqual([C1, CR1])
    expect(nodeById(result, R1)).toMatchObject({
      collapsed: true,
      hasChildren: true,
      descendantCount: 2,
    })
    expect(result.edges.some((edge) => edge.targetId === C1)).toBe(false)
  })

  it("keeps a result whose case is missing from the graph as a synthetic case", () => {
    const orphanCase = "1844674407370955299"
    const orphanResult = resultInput(CR1, orphanCase, "failed", 0, { title: "已删除用例" })
    const result = layoutChain({ requirements: [], cases: [], results: [orphanResult] })

    expect(nodeById(result, orphanCase).kind).toBe("case")
    expect(nodeById(result, orphanCase).title).toBe("已删除用例")
    expect(nodeById(result, CR1).kind).toBe("result")
    expect(result.edges.map((edge) => edge.id)).toEqual([`${orphanCase}->${CR1}`])
  })
})

describe("autoCollapsedRequirements", () => {
  function inputWithTotal(total: number) {
    const requirements: ChainRequirementInput[] = [requirement(R1, "登录需求", 0)]
    const cases: ChainCaseInput[] = []
    const results: ChainResultInput[] = []
    const remaining = total - requirements.length
    for (let index = 0; index < remaining; index += 1) {
      cases.push(caseInput(`case-${String(index).padStart(4, "0")}`, `用例 ${index}`, [], index))
    }
    return { requirements, cases, results }
  }

  it("collapses every requirement once the chain exceeds the threshold", () => {
    const above = inputWithTotal(CHAIN_AUTO_COLLAPSE_THRESHOLD + 1)

    expect(autoCollapsedRequirements(above)).toEqual([R1])
  })

  it("leaves requirements expanded at or below the threshold", () => {
    const atThreshold = inputWithTotal(CHAIN_AUTO_COLLAPSE_THRESHOLD)

    expect(autoCollapsedRequirements(atThreshold)).toEqual([])
  })
})

describe("result status helpers", () => {
  it("classifies abnormal result statuses", () => {
    expect(isAbnormalResultStatus("failed")).toBe(true)
    expect(isAbnormalResultStatus("blocked")).toBe(true)
    expect(isAbnormalResultStatus("inconclusive")).toBe(true)
    expect(isAbnormalResultStatus("interrupted")).toBe(true)
    expect(isAbnormalResultStatus("passed")).toBe(false)
    expect(isAbnormalResultStatus("skipped")).toBe(false)
  })

  it("groups pending and running under the pending filter", () => {
    expect(matchesResultFilter("pending", "pending")).toBe(true)
    expect(matchesResultFilter("running", "pending")).toBe(true)
    expect(matchesResultFilter("failed", "pending")).toBe(false)
    expect(matchesResultFilter("failed", "failed")).toBe(true)
    expect(matchesResultFilter("failed", "")).toBe(true)
  })
})
