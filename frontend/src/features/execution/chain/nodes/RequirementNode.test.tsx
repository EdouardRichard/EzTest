import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"

import type { ChainSnapshotNode } from "../chain-types"
import { RequirementNode } from "./RequirementNode"

const REQUIREMENT: ChainSnapshotNode = {
  id: "requirement:1844674407370955101",
  kind: "requirement",
  title: "登录需求",
  status: "clarified",
  priority: "must",
  uncovered: true,
  caseCount: 3,
  executedCount: 2,
  failedCount: 1,
  openQuestionCount: 0,
}

afterEach(cleanup)

describe("RequirementNode", () => {
  it("renders the title, requirement status, priority and coverage summary", () => {
    render(<RequirementNode data={REQUIREMENT} />)

    const card = screen.getByTestId("chain-node-requirement")
    expect(card.getAttribute("data-node-id")).toBe(REQUIREMENT.id)
    expect(card.textContent).toContain("登录需求")
    expect(card.textContent).toContain("已澄清")
    expect(screen.getByTestId("chain-node-requirement-priority").textContent).toBe("必须")
    expect(screen.getByTestId("chain-node-requirement-case-count").textContent).toBe("3 个用例")
    expect(screen.getByTestId("chain-node-requirement-uncovered").textContent).toBe("未覆盖")
  })

  it("includes title and status in the accessible label", () => {
    render(<RequirementNode data={REQUIREMENT} selected />)

    const card = screen.getByTestId("chain-node-requirement")
    const label = card.getAttribute("aria-label") ?? ""
    expect(label).toContain("登录需求")
    expect(label).toContain("已澄清")
    expect(label).toContain("未覆盖")
    expect(card.getAttribute("data-selected")).toBe("true")
  })

  it("omits the uncovered badge for a covered requirement", () => {
    render(<RequirementNode data={{ ...REQUIREMENT, uncovered: false }} />)

    expect(screen.queryByTestId("chain-node-requirement-uncovered")).toBeNull()
  })
})
