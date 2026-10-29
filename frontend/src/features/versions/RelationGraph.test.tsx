import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { RelationGraph } from "./RelationGraph"
import type { Requirement, RequirementRelation } from "./types"

/** Snowflake IDs stay decimal strings end-to-end (spec §7.1). */
const REQ_A = "1844674407370956001"
const REQ_B = "1844674407370956002"
const REQ_C = "1844674407370956003"

function requirement(
  overrides: Partial<Requirement> & Pick<Requirement, "id" | "title">,
): Requirement {
  return {
    versionId: "1844674407370955161",
    description: "",
    acceptanceCriteria: "",
    priority: "should",
    status: "draft",
    source: "user",
    lastUpdateSource: "user",
    revision: 1,
    confirmedAt: null,
    confirmedBy: null,
    openQuestionCount: 0,
    relationCount: 0,
    attachmentCount: 0,
    createdBy: 1,
    updatedBy: 1,
    createdAt: "2026-10-27T10:00:00Z",
    updatedAt: "2026-10-27T10:00:00Z",
    ...overrides,
  }
}

function relation(
  overrides: Partial<RequirementRelation> &
    Pick<RequirementRelation, "id" | "fromRequirementId" | "toRequirementId" | "relationType">,
): RequirementRelation {
  return {
    versionId: "1844674407370955161",
    description: "",
    source: "user",
    createdBy: 1,
    createdAt: "2026-10-27T10:00:00Z",
    updatedAt: "2026-10-27T10:00:00Z",
    ...overrides,
  }
}

const REQUIREMENTS: Requirement[] = [
  requirement({ id: REQ_A, title: "订单创建" }),
  requirement({ id: REQ_B, title: "库存扣减" }),
  requirement({ id: REQ_C, title: "支付回调" }),
]

const RELATIONS: RequirementRelation[] = [
  relation({
    id: "1844674407370956101",
    fromRequirementId: REQ_A,
    toRequirementId: REQ_B,
    relationType: "depends_on",
  }),
  relation({
    id: "1844674407370956102",
    fromRequirementId: REQ_B,
    toRequirementId: REQ_C,
    relationType: "conflicts_with",
  }),
  relation({
    id: "1844674407370956103",
    fromRequirementId: REQ_C,
    toRequirementId: REQ_A,
    relationType: "overlaps",
  }),
  relation({
    id: "1844674407370956104",
    fromRequirementId: REQ_A,
    toRequirementId: REQ_C,
    relationType: "shares_scope",
  }),
]

describe("RelationGraph", () => {
  afterEach(cleanup)

  it("renders one deterministically placed node per requirement", () => {
    render(<RelationGraph requirements={REQUIREMENTS} relations={RELATIONS} onSelect={vi.fn()} />)

    expect(screen.getByTestId(`requirement-node-${REQ_A}`)).toBeTruthy()
    expect(screen.getByTestId(`requirement-node-${REQ_B}`)).toBeTruthy()
    expect(screen.getByTestId(`requirement-node-${REQ_C}`)).toBeTruthy()

    // Circular layout: first node at 12 o'clock, the rest evenly spaced.
    expect(screen.getByTestId(`requirement-node-${REQ_A}`).getAttribute("transform")).toBe(
      "translate(260 55)",
    )
    expect(screen.getByTestId(`requirement-node-${REQ_B}`).getAttribute("transform")).toBe(
      "translate(377 258)",
    )
    expect(screen.getByTestId(`requirement-node-${REQ_C}`).getAttribute("transform")).toBe(
      "translate(143 258)",
    )
    expect(screen.getByText("订单创建")).toBeTruthy()
    expect(screen.getByText("库存扣减")).toBeTruthy()
    expect(screen.getByText("支付回调")).toBeTruthy()
    expect(screen.getByText("共 4 条关系")).toBeTruthy()
  })

  it("draws depends_on as an arrow and the other relation types as plain lines", () => {
    render(<RelationGraph requirements={REQUIREMENTS} relations={RELATIONS} onSelect={vi.fn()} />)

    const dependsOn = screen.getByTestId("relation-edge-1844674407370956101")
    expect(dependsOn.getAttribute("data-relation-type")).toBe("depends_on")
    expect(dependsOn.getAttribute("marker-end")).toContain("url(#")

    for (const id of ["1844674407370956102", "1844674407370956103", "1844674407370956104"]) {
      const edge = screen.getByTestId(`relation-edge-${id}`)
      expect(edge.getAttribute("marker-end")).toBeNull()
    }
    expect(
      screen.getByTestId("relation-edge-1844674407370956102").getAttribute("data-relation-type"),
    ).toBe("conflicts_with")
    expect(
      screen.getByTestId("relation-edge-1844674407370956103").getAttribute("data-relation-type"),
    ).toBe("overlaps")
    expect(
      screen.getByTestId("relation-edge-1844674407370956104").getAttribute("data-relation-type"),
    ).toBe("shares_scope")
  })

  it("names every relation type and direction in a text legend", () => {
    render(<RelationGraph requirements={REQUIREMENTS} relations={RELATIONS} onSelect={vi.fn()} />)

    expect(screen.getByText("依赖（有向，箭头指向被依赖需求）")).toBeTruthy()
    expect(screen.getByText("冲突（无向）")).toBeTruthy()
    expect(screen.getByText("重叠（无向）")).toBeTruthy()
    expect(screen.getByText("共享范围（无向）")).toBeTruthy()
  })

  it("selects a requirement by click and by keyboard", () => {
    const onSelect = vi.fn()
    render(<RelationGraph requirements={REQUIREMENTS} relations={RELATIONS} onSelect={onSelect} />)

    fireEvent.click(screen.getByTestId(`requirement-node-${REQ_A}`))
    expect(onSelect).toHaveBeenLastCalledWith(REQ_A)

    fireEvent.keyDown(screen.getByTestId(`requirement-node-${REQ_B}`), { key: "Enter" })
    expect(onSelect).toHaveBeenLastCalledWith(REQ_B)
  })

  it("shows an empty hint without requirements and another one without relations", () => {
    const { rerender } = render(
      <RelationGraph requirements={[]} relations={[]} onSelect={vi.fn()} />,
    )
    expect(screen.getByText("暂无可绘制关系的需求。")).toBeTruthy()

    rerender(<RelationGraph requirements={REQUIREMENTS} relations={[]} onSelect={vi.fn()} />)
    expect(screen.getByText("暂无关系，添加关系后将在此展示关系图。")).toBeTruthy()
    expect(screen.getByTestId(`requirement-node-${REQ_A}`)).toBeTruthy()
  })

  it("shows loading and error states instead of a zero relation count", () => {
    const { rerender } = render(
      <RelationGraph requirements={REQUIREMENTS} relations={[]} onSelect={vi.fn()} loading />,
    )

    expect(screen.getByText("关系加载中…")).toBeTruthy()
    expect(screen.queryByText("共 0 条关系")).toBeNull()
    expect(screen.queryByText("暂无关系，添加关系后将在此展示关系图。")).toBeNull()

    rerender(
      <RelationGraph
        requirements={REQUIREMENTS}
        relations={[]}
        onSelect={vi.fn()}
        error="offline"
      />,
    )

    expect(screen.getByRole("alert").textContent).toContain("加载关系失败：offline")
    expect(screen.queryByText("共 0 条关系")).toBeNull()
    expect(screen.queryByText("暂无关系，添加关系后将在此展示关系图。")).toBeNull()
  })

  it("keeps node role semantics and a visible focus ring for keyboard users", () => {
    render(<RelationGraph requirements={REQUIREMENTS} relations={RELATIONS} onSelect={vi.fn()} />)

    const node = screen.getByTestId(`requirement-node-${REQ_A}`)
    expect(node.getAttribute("role")).toBe("button")
    expect(node.getAttribute("tabindex")).toBe("0")
    expect(node.getAttribute("class")).toContain("group")

    const circle = node.querySelector("circle")
    expect(circle?.getAttribute("class")).toContain("group-focus-visible:stroke-ring")
    expect(circle?.getAttribute("class")).toContain("group-focus-visible:[stroke-width:3px]")
  })
})
