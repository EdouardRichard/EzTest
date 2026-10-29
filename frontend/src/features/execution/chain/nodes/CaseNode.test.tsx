import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"

import { CHAIN_CARD_SIZES } from "../chain-layout"
import { CASE_STATUS_TOKENS } from "../chain-tokens"
import type { ChainSnapshotNode } from "../chain-types"
import { CaseNode } from "./CaseNode"

const CASE: ChainSnapshotNode = {
  id: "case:1844674407370955201",
  kind: "case",
  title: "登录成功",
  status: "executed",
  source: "user_input",
  revalidation: false,
  priority: "P2",
  requirementIds: ["1844674407370955101"],
  stepCount: 3,
  assertionCount: 2,
  impacted: false,
  impactReason: "",
}

afterEach(cleanup)

describe("CaseNode", () => {
  it("renders the title, case status and priority", () => {
    render(<CaseNode data={CASE} />)

    const card = screen.getByTestId("chain-node-case")
    expect(card.getAttribute("data-node-id")).toBe(CASE.id)
    expect(card.textContent).toContain("登录成功")
    expect(card.textContent).toContain("已执行")
    expect(screen.getByTestId("chain-node-case-priority").textContent).toBe("P2 中")
    expect(card.getAttribute("aria-label")).toContain("登录成功")
    expect(card.getAttribute("aria-label")).toContain("已执行")
  })

  it("marks an AI-supplemented case with a dashed card and a corner badge", () => {
    render(<CaseNode data={{ ...CASE, source: "ai_supplement" }} />)

    const card = screen.getByTestId("chain-node-case")
    expect(card.className).toContain("border-dashed")
    expect(screen.getByTestId("chain-node-case-ai-badge").textContent).toBe("AI 补充")
    expect(card.getAttribute("aria-label")).toContain("AI 补充")
  })

  it("draws an amber ring and badge for a case that needs revalidation", () => {
    render(<CaseNode data={{ ...CASE, status: "needs_revalidation", revalidation: true }} />)

    const card = screen.getByTestId("chain-node-case")
    expect(card.getAttribute("data-revalidation")).toBe("true")
    expect(card.className).toContain(CASE_STATUS_TOKENS.needs_revalidation.ringClass)
    expect(card.className).not.toContain("ring-ring")
    expect(screen.getByTestId("chain-node-case-revalidation").textContent).toBe("需重验")
  })

  it("lets the selected ring win over the revalidation ring", () => {
    render(
      <CaseNode data={{ ...CASE, status: "needs_revalidation", revalidation: true }} selected />,
    )

    const card = screen.getByTestId("chain-node-case")
    expect(card.className).toContain("ring-ring")
    expect(card.className).not.toContain("ring-amber-500")
  })

  it("marks an impacted case with its impact reason", () => {
    render(<CaseNode data={{ ...CASE, impacted: true, impactReason: "下单接口变更" }} />)

    const card = screen.getByTestId("chain-node-case")
    expect(card.getAttribute("data-impacted")).toBe("true")
    expect(screen.getByTestId("chain-node-case-impacted").textContent).toBe("受影响")
    expect(card.getAttribute("aria-label")).toContain("下单接口变更")
  })

  it("fills the fixed card slot from the shared layout constants", () => {
    render(<CaseNode data={{ ...CASE, impacted: true, impactReason: "下单接口变更" }} />)

    const card = screen.getByTestId("chain-node-case")
    expect(card.style.width).toBe(`${CHAIN_CARD_SIZES.case.width}px`)
    expect(card.style.height).toBe(`${CHAIN_CARD_SIZES.case.height}px`)
    expect(card.className).toContain("overflow-hidden")
    expect(card.querySelector("h3")?.getAttribute("class")).toContain("line-clamp-2")
  })
})
