import { describe, expect, it } from "vitest"
import {
  autoCollapsedChainRequirements,
  CHAIN_AUTO_COLLAPSE_THRESHOLD,
  CHAIN_CARD_SIZES,
  CHAIN_CASE_WIDTH,
  CHAIN_ISSUE_HEIGHT,
  CHAIN_ISSUE_WIDTH,
  CHAIN_LAYER_GAP,
  CHAIN_READ_MODEL_LAYER_GAP,
  CHAIN_READ_MODEL_ROW_GAP,
  CHAIN_REQUIREMENT_WIDTH,
  CHAIN_RESULT_WIDTH,
  isAbnormalResultStatus,
  layoutChain,
  layoutLegacy,
  matchesResultFilter,
} from "./chain-layout"
import type { ChainEdgeKindDTO, ChainSnapshotEdge, ChainSnapshotNode } from "./chain-types"

const R1 = "1844674407370955101"
const R2 = "1844674407370955102"
const C1 = "1844674407370955201"
const C2 = "1844674407370955202"
const C3 = "1844674407370955203"
const CR1 = "1844674407370955301"
const CR2 = "1844674407370955302"
const CR3 = "1844674407370955303"
const I1 = "1844674407370955401"
const I2 = "1844674407370955402"

function nodeId(kind: "requirement" | "case" | "result" | "issue", id: string): string {
  return `${kind}:${id}`
}

function requirementNode(
  id: string,
  title: string,
  overrides: Partial<ChainSnapshotNode> = {},
): ChainSnapshotNode {
  return {
    id: nodeId("requirement", id),
    kind: "requirement",
    title,
    status: "clarified",
    priority: "must",
    uncovered: false,
    caseCount: 0,
    ...overrides,
  }
}

function caseNode(
  id: string,
  title: string,
  overrides: Partial<ChainSnapshotNode> = {},
): ChainSnapshotNode {
  return {
    id: nodeId("case", id),
    kind: "case",
    title,
    status: "executed",
    source: "user_input",
    priority: "P2",
    requirementIds: [],
    ...overrides,
  }
}

function resultNode(
  id: string,
  caseId: string,
  status: string,
  overrides: Partial<ChainSnapshotNode> = {},
): ChainSnapshotNode {
  return {
    id: nodeId("result", id),
    kind: "result",
    title: "",
    status,
    caseRunId: id,
    caseNodeId: caseId,
    attempt: 1,
    durationMs: 0,
    failureClass: "",
    evidenceCount: 0,
    ...overrides,
  }
}

function issueNode(
  id: string,
  title: string,
  overrides: Partial<ChainSnapshotNode> = {},
): ChainSnapshotNode {
  return {
    id: nodeId("issue", id),
    kind: "issue",
    title,
    status: "open",
    severity: "major",
    ...overrides,
  }
}

function chainEdge(kind: ChainEdgeKindDTO, source: string, target: string): ChainSnapshotEdge {
  const sourceDomain = source.slice(source.indexOf(":") + 1)
  const targetDomain = target.slice(target.indexOf(":") + 1)
  return { id: `${kind}:${sourceDomain}:${targetDomain}`, source, target, kind }
}

function baseNodes(): ChainSnapshotNode[] {
  return [
    requirementNode(R1, "登录需求"),
    requirementNode(R2, "下单需求"),
    caseNode(C1, "登录成功"),
    caseNode(C2, "提交订单"),
    caseNode(C3, "未关联用例"),
    resultNode(CR1, C1, "passed", { title: "登录成功" }),
    resultNode(CR2, C2, "failed", { title: "提交订单" }),
    resultNode(CR3, C3, "blocked", { title: "未关联用例" }),
  ]
}

function baseEdges(): ChainSnapshotEdge[] {
  return [
    chainEdge("requirement_case", nodeId("requirement", R1), nodeId("case", C1)),
    chainEdge("requirement_case", nodeId("requirement", R2), nodeId("case", C2)),
    chainEdge("case_result", nodeId("case", C1), nodeId("result", CR1)),
    chainEdge("case_result", nodeId("case", C2), nodeId("result", CR2)),
    chainEdge("case_result", nodeId("case", C3), nodeId("result", CR3)),
  ]
}

function nodeById(result: ReturnType<typeof layoutChain>, id: string) {
  const node = result.nodes.find((candidate) => candidate.id === id)
  if (node === undefined) throw new Error(`missing node ${id}`)
  return node
}

function idsOfKind(
  result: ReturnType<typeof layoutChain>,
  kind: ChainSnapshotNode["kind"],
): string[] {
  return result.nodes.filter((node) => node.kind === kind).map((node) => node.id)
}

describe("layoutChain over the read model", () => {
  it("returns an empty layout for an empty chain", () => {
    const result = layoutChain([])

    expect(result.nodes).toEqual([])
    expect(result.edges).toEqual([])
    expect(result.visibleIds).toEqual([])
    expect(result.hiddenIds).toEqual([])
    expect(result.unlinkedIssues).toEqual([])
    expect(result.totalCount).toBe(0)
    expect(result.bounds).toEqual({ x: 0, y: 0, width: 0, height: 0 })
  })

  it("lays requirements, cases, results and issues out in four deterministic layers", () => {
    const result = layoutChain([...baseNodes(), issueNode(I1, "登录报错")], {
      edges: [
        ...baseEdges(),
        chainEdge("result_issue", nodeId("result", CR1), nodeId("issue", I1)),
      ],
    })

    expect(result.nodes).toHaveLength(9)
    expect(result.totalCount).toBe(9)

    const requirement = nodeById(result, nodeId("requirement", R1))
    const caseNodeResult = nodeById(result, nodeId("case", C1))
    const resultNodeResult = nodeById(result, nodeId("result", CR1))
    const issue = nodeById(result, nodeId("issue", I1))

    const caseX = CHAIN_CARD_SIZES.requirement.width + CHAIN_READ_MODEL_LAYER_GAP
    const resultX = caseX + CHAIN_CARD_SIZES.case.width + CHAIN_READ_MODEL_LAYER_GAP
    const issueX = resultX + CHAIN_CARD_SIZES.result.width + CHAIN_READ_MODEL_LAYER_GAP

    expect(requirement.kind).toBe("requirement")
    expect(requirement.layer).toBe(0)
    expect(requirement.x).toBe(0)
    expect(requirement.y).toBe(0)
    expect(requirement.width).toBe(CHAIN_CARD_SIZES.requirement.width)
    expect(requirement.height).toBe(CHAIN_CARD_SIZES.requirement.height)

    expect(caseNodeResult.kind).toBe("case")
    expect(caseNodeResult.layer).toBe(1)
    expect(caseNodeResult.x).toBe(caseX)
    expect(caseNodeResult.width).toBe(CHAIN_CARD_SIZES.case.width)
    expect(caseNodeResult.y).toBe(requirement.y)

    expect(resultNodeResult.kind).toBe("result")
    expect(resultNodeResult.layer).toBe(2)
    expect(resultNodeResult.x).toBe(resultX)
    expect(resultNodeResult.y).toBe(caseNodeResult.y)

    expect(issue.kind).toBe("issue")
    expect(issue.layer).toBe(3)
    expect(issue.x).toBe(issueX)
    expect(issue.width).toBe(CHAIN_CARD_SIZES.issue.width)
    expect(issue.height).toBe(CHAIN_CARD_SIZES.issue.height)

    // The second requirement starts below everything the first group occupies.
    const secondRequirement = nodeById(result, nodeId("requirement", R2))
    expect(secondRequirement.y).toBeGreaterThan(caseNodeResult.y + caseNodeResult.height)
    expect(nodeById(result, nodeId("case", C2)).y).toBeGreaterThanOrEqual(secondRequirement.y)
  })

  it("uses the shared card size constants on every positioned node", () => {
    const result = layoutChain([...baseNodes(), issueNode(I1, "登录报错", { caseNodeId: C1 })])

    for (const node of result.nodes) {
      expect({ width: node.width, height: node.height }).toEqual(CHAIN_CARD_SIZES[node.kind])
    }
  })

  it("sizes every card layer for its tallest content within the 232–240px width range", () => {
    for (const kind of ["requirement", "case", "result", "issue"] as const) {
      expect(CHAIN_CARD_SIZES[kind].width).toBeGreaterThanOrEqual(232)
      expect(CHAIN_CARD_SIZES[kind].width).toBeLessThanOrEqual(240)
    }
    expect(CHAIN_CARD_SIZES.requirement.height).toBeGreaterThanOrEqual(120)
    expect(CHAIN_CARD_SIZES.case.height).toBeGreaterThanOrEqual(160)
    expect(CHAIN_CARD_SIZES.result.height).toBeGreaterThanOrEqual(160)
    expect(CHAIN_CARD_SIZES.issue.height).toBeGreaterThanOrEqual(140)
  })

  it("preserves the backend order of every layer instead of re-sorting by id", () => {
    const nodes = [
      requirementNode(R2, "下单需求"),
      requirementNode(R1, "登录需求"),
      caseNode(C3, "未关联用例"),
      caseNode(C2, "提交订单"),
      caseNode(C1, "登录成功"),
      resultNode(CR3, C3, "blocked"),
      resultNode(CR2, C2, "failed"),
      resultNode(CR1, C1, "passed"),
      issueNode(I2, "订单阻断", { severity: "blocker" }),
      issueNode(I1, "登录报错", { severity: "major" }),
    ]
    const edges = [
      chainEdge("case_result", nodeId("case", C3), nodeId("result", CR3)),
      chainEdge("case_result", nodeId("case", C2), nodeId("result", CR2)),
      chainEdge("case_result", nodeId("case", C1), nodeId("result", CR1)),
      chainEdge("result_issue", nodeId("result", CR3), nodeId("issue", I2)),
      chainEdge("result_issue", nodeId("result", CR3), nodeId("issue", I1)),
    ]

    // The backend already returns requirements by `sortOrder,id`, cases by
    // `sortOrder,id`, results by `attempt desc` and issues by severity rank;
    // the layout must keep that array order untouched.
    const result = layoutChain(nodes, { edges })

    expect(idsOfKind(result, "requirement")).toEqual([
      nodeId("requirement", R2),
      nodeId("requirement", R1),
    ])
    expect(idsOfKind(result, "case")).toEqual([
      nodeId("case", C3),
      nodeId("case", C2),
      nodeId("case", C1),
    ])
    expect(idsOfKind(result, "result")).toEqual([
      nodeId("result", CR3),
      nodeId("result", CR2),
      nodeId("result", CR1),
    ])
    expect(idsOfKind(result, "issue")).toEqual([nodeId("issue", I2), nodeId("issue", I1)])
  })

  it("keeps the input order of requirements and sibling cases (300 before 200)", () => {
    const edges = [
      chainEdge("requirement_case", nodeId("requirement", "300"), nodeId("case", "300")),
      chainEdge("requirement_case", nodeId("requirement", "300"), nodeId("case", "200")),
    ]
    const result = layoutChain(
      [
        requirementNode("300", "需求 300"),
        requirementNode("200", "需求 200"),
        caseNode("300", "用例 300"),
        caseNode("200", "用例 200"),
      ],
      { edges },
    )

    expect(idsOfKind(result, "requirement")).toEqual([
      nodeId("requirement", "300"),
      nodeId("requirement", "200"),
    ])
    expect(idsOfKind(result, "case")).toEqual([nodeId("case", "300"), nodeId("case", "200")])
  })

  it("keeps the input order of requirement groups, sibling cases and stacked results", () => {
    const nodes = [
      requirementNode(R2, "下单需求"),
      requirementNode(R1, "登录需求"),
      caseNode(C2, "提交订单"),
      caseNode(C1, "登录成功"),
      caseNode(C3, "未关联用例"),
      resultNode(CR1, C1, "passed", { attempt: 1 }),
      resultNode(CR3, C1, "failed", { attempt: 2 }),
      resultNode(CR2, C2, "failed", { attempt: 1 }),
    ]
    const edges = [
      chainEdge("requirement_case", nodeId("requirement", R2), nodeId("case", C2)),
      chainEdge("requirement_case", nodeId("requirement", R1), nodeId("case", C1)),
      chainEdge("case_result", nodeId("case", C1), nodeId("result", CR1)),
      chainEdge("case_result", nodeId("case", C1), nodeId("result", CR3)),
      chainEdge("case_result", nodeId("case", C2), nodeId("result", CR2)),
    ]

    const result = layoutChain(nodes, { edges })

    expect(result.visibleIds).toEqual([
      nodeId("requirement", R2),
      nodeId("case", C2),
      nodeId("result", CR2),
      nodeId("requirement", R1),
      nodeId("case", C1),
      nodeId("result", CR1),
      nodeId("result", CR3),
      nodeId("case", C3),
    ])
    expect(nodeById(result, nodeId("requirement", R2)).y).toBeLessThan(
      nodeById(result, nodeId("requirement", R1)).y,
    )
    expect(nodeById(result, nodeId("result", CR1)).y).toBeLessThan(
      nodeById(result, nodeId("result", CR3)).y,
    )
    expect(nodeById(result, nodeId("case", C3)).y).toBeGreaterThan(
      nodeById(result, nodeId("case", C2)).y,
    )
  })

  it("stacks the results of one case in the input order", () => {
    const nodes = [
      requirementNode(R1, "登录需求"),
      caseNode(C1, "登录成功"),
      resultNode(CR3, C1, "failed", { attempt: 2 }),
      resultNode(CR1, C1, "passed", { attempt: 1 }),
    ]
    const edges = [
      chainEdge("requirement_case", nodeId("requirement", R1), nodeId("case", C1)),
      chainEdge("case_result", nodeId("case", C1), nodeId("result", CR1)),
      chainEdge("case_result", nodeId("case", C1), nodeId("result", CR3)),
    ]

    const result = layoutChain(nodes, { edges })

    const first = nodeById(result, nodeId("result", CR3))
    const second = nodeById(result, nodeId("result", CR1))
    expect(first.y).toBe(0)
    expect(second.y).toBe(CHAIN_CARD_SIZES.result.height + CHAIN_READ_MODEL_ROW_GAP)
    expect(second.x).toBe(first.x)
  })

  it("copies the snapshot edges in a deterministic pre-order and keeps their ids", () => {
    const issues = [issueNode(I1, "登录报错")]
    const result = layoutChain([...baseNodes(), ...issues], {
      edges: [
        ...baseEdges(),
        chainEdge("result_issue", nodeId("result", CR1), nodeId("issue", I1)),
      ],
    })

    expect(result.edges.map((edge) => edge.id)).toEqual([
      `requirement_case:${R1}:${C1}`,
      `case_result:${C1}:${CR1}`,
      `result_issue:${CR1}:${I1}`,
      `requirement_case:${R2}:${C2}`,
      `case_result:${C2}:${CR2}`,
      `case_result:${C3}:${CR3}`,
    ])
    expect(result.edges.map((edge) => edge.kind)).toEqual([
      "requirement_case",
      "case_result",
      "result_issue",
      "requirement_case",
      "case_result",
      "case_result",
    ])
    expect(result.edges[0]).toMatchObject({
      sourceId: nodeId("requirement", R1),
      targetId: nodeId("case", C1),
    })
  })

  it("places a case linked to several requirements under its earliest group and links both", () => {
    const shared = caseNode(C1, "共享用例", { requirementIds: [R1, R2] })
    const result = layoutChain(
      [requirementNode(R1, "登录需求"), requirementNode(R2, "下单需求"), shared],
      {
        edges: [
          chainEdge("requirement_case", nodeId("requirement", R2), nodeId("case", C1)),
          chainEdge("impact_case", nodeId("requirement", R1), nodeId("case", C1)),
        ],
      },
    )

    const caseNodeResult = nodeById(result, nodeId("case", C1))
    expect(caseNodeResult.y).toBe(nodeById(result, nodeId("requirement", R1)).y)
    expect(caseNodeResult.requirementIds).toEqual([
      nodeId("requirement", R1),
      nodeId("requirement", R2),
    ])
    expect(result.edges.map((edge) => edge.kind)).toEqual(["impact_case", "requirement_case"])
  })

  it("collapses one requirement group and hides its descendants", () => {
    const nodes = [...baseNodes(), issueNode(I1, "登录报错")]
    const edges = [
      ...baseEdges(),
      chainEdge("result_issue", nodeId("result", CR1), nodeId("issue", I1)),
    ]

    const result = layoutChain(nodes, { edges, collapsed: [R1] })

    expect(result.visibleIds).toEqual([
      nodeId("requirement", R1),
      nodeId("requirement", R2),
      nodeId("case", C2),
      nodeId("result", CR2),
      nodeId("case", C3),
      nodeId("result", CR3),
    ])
    expect(result.hiddenIds).toEqual([
      nodeId("case", C1),
      nodeId("result", CR1),
      nodeId("issue", I1),
    ])
    expect(nodeById(result, nodeId("requirement", R1))).toMatchObject({
      collapsed: true,
      hasChildren: true,
      descendantCount: 3,
    })
    expect(result.edges.some((edge) => edge.targetId === nodeId("case", C1))).toBe(false)
  })

  it("accepts the full node id in the collapsed set as well", () => {
    const result = layoutChain(baseNodes(), {
      edges: baseEdges(),
      collapsed: [nodeId("requirement", R1)],
    })

    expect(result.hiddenIds).toContain(nodeId("case", C1))
  })

  it("keeps a result whose case is missing from the graph as a synthetic case", () => {
    const orphanCase = "1844674407370955299"
    const nodes = [resultNode(CR1, orphanCase, "failed", { title: "已删除用例" })]
    const result = layoutChain(nodes)

    expect(nodeById(result, nodeId("case", orphanCase)).kind).toBe("case")
    expect(nodeById(result, nodeId("case", orphanCase)).title).toBe("已删除用例")
    expect(nodeById(result, nodeId("result", CR1)).kind).toBe("result")
    expect(result.edges.map((edge) => edge.id)).toEqual([`case_result:${orphanCase}:${CR1}`])
  })

  it("lays issues out as a fourth layer linked to their case or case run", () => {
    const nodes = [
      ...baseNodes(),
      issueNode(I1, "登录报错", { caseRunId: CR1, severity: "blocker" }),
      issueNode(I2, "订单失败", { caseNodeId: C2 }),
    ]
    const edges = [
      ...baseEdges(),
      chainEdge("result_issue", nodeId("result", CR1), nodeId("issue", I1)),
      chainEdge("case_issue", nodeId("case", C2), nodeId("issue", I2)),
    ]

    const result = layoutChain(nodes, { edges })

    const caseX = CHAIN_CARD_SIZES.requirement.width + CHAIN_READ_MODEL_LAYER_GAP
    const resultX = caseX + CHAIN_CARD_SIZES.case.width + CHAIN_READ_MODEL_LAYER_GAP
    const issueX = resultX + CHAIN_CARD_SIZES.result.width + CHAIN_READ_MODEL_LAYER_GAP
    const issue = nodeById(result, nodeId("issue", I1))

    expect(issue.kind).toBe("issue")
    expect(issue.layer).toBe(3)
    expect(issue.caseNodeId).toBe(nodeId("case", C1))
    expect(issue.x).toBe(issueX)
    expect(result.edges.find((edge) => edge.targetId === nodeId("issue", I1))).toMatchObject({
      kind: "result_issue",
      sourceId: nodeId("result", CR1),
    })
    // Without a result edge the issue hangs off its case node.
    expect(result.edges.find((edge) => edge.targetId === nodeId("issue", I2))).toMatchObject({
      kind: "case_issue",
      sourceId: nodeId("case", C2),
    })
  })

  it("resolves an issue's case from its case run and keeps it visible", () => {
    const nodes = [
      caseNode(C1, "登录成功"),
      resultNode(CR1, C1, "failed"),
      issueNode(I1, "提交订单失败", { caseRunId: CR1 }),
    ]
    const result = layoutChain(nodes)

    expect(nodeById(result, nodeId("issue", I1)).caseNodeId).toBe(nodeId("case", C1))
    expect(result.visibleIds).toContain(nodeId("issue", I1))
    expect(result.edges.some((edge) => edge.kind === "result_issue")).toBe(true)
  })

  it("lists issues without a case instead of dropping them from the layout", () => {
    const result = layoutChain([...baseNodes(), issueNode(I1, "需求缺口")], {
      edges: baseEdges(),
    })

    expect(result.unlinkedIssues.map((issue) => issue.id)).toEqual([nodeId("issue", I1)])
    expect(result.nodes.some((node) => node.id === nodeId("issue", I1))).toBe(false)
    expect(result.totalCount).toBe(9)
  })

  it("hides the issue nodes of a collapsed requirement group", () => {
    const nodes = [...baseNodes(), issueNode(I1, "登录报错")]
    const edges = [
      ...baseEdges(),
      chainEdge("result_issue", nodeId("result", CR1), nodeId("issue", I1)),
    ]
    const result = layoutChain(nodes, { edges, collapsed: [R1] })

    expect(result.hiddenIds).toEqual([
      nodeId("case", C1),
      nodeId("result", CR1),
      nodeId("issue", I1),
    ])
  })

  it("keeps the input order of issue nodes instead of ranking them by severity", () => {
    const major = issueNode(I1, "登录报错", { severity: "major" })
    const blocker = issueNode(I2, "订单阻断", { severity: "blocker" })
    const edges = [
      ...baseEdges(),
      chainEdge("result_issue", nodeId("result", CR1), nodeId("issue", I1)),
      chainEdge("result_issue", nodeId("result", CR1), nodeId("issue", I2)),
    ]

    const forward = layoutChain([...baseNodes(), major, blocker], { edges })
    const backward = layoutChain([...baseNodes(), blocker, major], { edges })

    expect(idsOfKind(forward, "issue")).toEqual([nodeId("issue", I1), nodeId("issue", I2)])
    expect(idsOfKind(backward, "issue")).toEqual([nodeId("issue", I2), nodeId("issue", I1)])
    expect(nodeById(forward, nodeId("issue", I1)).y).toBe(0)
    expect(nodeById(forward, nodeId("issue", I2)).y).toBe(
      CHAIN_CARD_SIZES.issue.height + CHAIN_READ_MODEL_ROW_GAP,
    )
  })

  it("wraps every visible node in the bounds", () => {
    const result = layoutChain([...baseNodes(), issueNode(I1, "登录报错")], {
      edges: [
        ...baseEdges(),
        chainEdge("result_issue", nodeId("result", CR1), nodeId("issue", I1)),
      ],
    })

    const maxX = Math.max(...result.nodes.map((node) => node.x + node.width))
    const maxY = Math.max(...result.nodes.map((node) => node.y + node.height))
    const minX = Math.min(...result.nodes.map((node) => node.x))
    const minY = Math.min(...result.nodes.map((node) => node.y))

    expect(result.bounds).toEqual({ x: minX, y: minY, width: maxX - minX, height: maxY - minY })
    // The bounds are exactly three layer gaps plus the four card widths; the
    // node components render the same fixed width/height from the constants.
    const expectedWidth =
      CHAIN_CARD_SIZES.requirement.width +
      CHAIN_READ_MODEL_LAYER_GAP +
      CHAIN_CARD_SIZES.case.width +
      CHAIN_READ_MODEL_LAYER_GAP +
      CHAIN_CARD_SIZES.result.width +
      CHAIN_READ_MODEL_LAYER_GAP +
      CHAIN_CARD_SIZES.issue.width
    expect(result.bounds.width).toBe(expectedWidth)
    for (const node of result.nodes) {
      expect(node.height).toBe(CHAIN_CARD_SIZES[node.kind].height)
    }
    for (const node of result.nodes) {
      expect(node.x).toBeGreaterThanOrEqual(result.bounds.x)
      expect(node.y).toBeGreaterThanOrEqual(result.bounds.y)
      expect(node.x + node.width).toBeLessThanOrEqual(result.bounds.x + result.bounds.width)
      expect(node.y + node.height).toBeLessThanOrEqual(result.bounds.y + result.bounds.height)
    }
  })

  it("keeps 64-bit decimal ids as strings end to end", () => {
    const huge = "18446744073709551615"
    const result = layoutChain([requirementNode(huge, "超大 ID 需求")])

    expect(result.nodes[0].id).toBe(`requirement:${huge}`)
    expect(result.nodes[0].data.id).toBe(`requirement:${huge}`)
    expect(result.visibleIds).toEqual([`requirement:${huge}`])
  })
})

describe("autoCollapsedChainRequirements", () => {
  function nodesWithTotal(total: number): ChainSnapshotNode[] {
    const nodes: ChainSnapshotNode[] = [requirementNode(R1, "登录需求")]
    for (let index = 0; index < total - 1; index += 1) {
      nodes.push(caseNode(`case-${String(index).padStart(4, "0")}`, `用例 ${index}`))
    }
    return nodes
  }

  it("collapses every requirement once the chain exceeds the threshold", () => {
    const above = nodesWithTotal(CHAIN_AUTO_COLLAPSE_THRESHOLD + 1)

    expect(autoCollapsedChainRequirements(above)).toEqual([nodeId("requirement", R1)])
  })

  it("leaves requirements expanded at or below the threshold", () => {
    const atThreshold = nodesWithTotal(CHAIN_AUTO_COLLAPSE_THRESHOLD)

    expect(autoCollapsedChainRequirements(atThreshold)).toEqual([])
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

describe("layoutLegacy compatibility adapter", () => {
  it("keeps the ids, coordinates and edges the current SVG canvas consumes", () => {
    const result = layoutLegacy({
      requirements: [{ id: R1, title: "登录需求", status: "clarified", sortOrder: 0 }],
      cases: [
        { id: C1, title: "登录成功", requirementIds: [R1], status: "executed", sortOrder: 0 },
      ],
      results: [{ id: CR1, caseNodeId: C1, status: "passed", sortOrder: 0 }],
    })

    expect(result.visibleIds).toEqual([R1, C1, CR1])
    expect(result.edges.map((edge) => edge.id)).toEqual([`${R1}->${C1}`, `${C1}->${CR1}`])
    expect(result.nodes[0]).toMatchObject({
      kind: "requirement",
      x: 0,
      y: 0,
      w: CHAIN_REQUIREMENT_WIDTH,
    })
    expect(result.nodes[1]).toMatchObject({
      kind: "case",
      x: CHAIN_REQUIREMENT_WIDTH + CHAIN_LAYER_GAP,
      w: CHAIN_CASE_WIDTH,
    })
    expect(result.nodes[2]).toMatchObject({
      kind: "result",
      x: CHAIN_REQUIREMENT_WIDTH + CHAIN_LAYER_GAP + CHAIN_CASE_WIDTH + CHAIN_LAYER_GAP,
      w: CHAIN_RESULT_WIDTH,
    })
  })

  it("keeps the legacy issue layer and collapsed-group behaviour", () => {
    const input = {
      requirements: [{ id: R1, title: "登录需求", sortOrder: 0 }],
      cases: [{ id: C1, title: "登录成功", requirementIds: [R1], sortOrder: 0 }],
      results: [{ id: CR1, caseNodeId: C1, status: "passed" as const, sortOrder: 0 }],
      issues: [
        {
          id: I1,
          title: "登录报错",
          status: "open",
          severity: "major",
          caseNodeId: C1,
          caseRunId: CR1,
          sortOrder: 0,
        },
      ],
    }

    const expanded = layoutLegacy(input)
    expect(expanded.visibleIds).toEqual([R1, C1, CR1, I1])
    expect(expanded.edges.find((edge) => edge.targetId === I1)).toMatchObject({
      kind: "issue-link",
      sourceId: CR1,
    })
    expect(expanded.nodes.find((node) => node.id === I1)).toMatchObject({
      w: CHAIN_ISSUE_WIDTH,
      h: CHAIN_ISSUE_HEIGHT,
    })

    const collapsed = layoutLegacy(input, { collapsed: [R1] })
    expect(collapsed.hiddenIds).toEqual([C1, CR1, I1])
  })
})
