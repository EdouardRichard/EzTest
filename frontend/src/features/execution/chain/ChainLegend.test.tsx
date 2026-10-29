import { cleanup, render, screen, within } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"

import { ChainLegend } from "./ChainLegend"
import { EDGE_KIND_TOKENS, NODE_KIND_TOKENS } from "./chain-tokens"
import type { ChainEdgeKindDTO, ChainNodeKindDTO } from "./chain-types"

const NODE_KINDS: ChainNodeKindDTO[] = ["requirement", "case", "result", "issue"]
const EDGE_KINDS: ChainEdgeKindDTO[] = [
  "requirement_case",
  "case_result",
  "result_issue",
  "case_issue",
  "impact_case",
]

afterEach(() => {
  cleanup()
})

describe("ChainLegend", () => {
  it("lists the four node kinds and five edge kinds with their labels", () => {
    render(<ChainLegend />)

    expect(screen.getByTestId("chain-legend")).not.toBeNull()
    expect(screen.getAllByTestId(/^chain-legend-node-/)).toHaveLength(NODE_KINDS.length)
    expect(screen.getAllByTestId(/^chain-legend-edge-/)).toHaveLength(EDGE_KINDS.length)

    for (const kind of NODE_KINDS) {
      const item = screen.getByTestId(`chain-legend-node-${kind}`)
      expect(item.textContent).toContain(NODE_KIND_TOKENS[kind].label)
      expect(item.getAttribute("data-color")).toBe(NODE_KIND_TOKENS[kind].color)
    }
    for (const kind of EDGE_KINDS) {
      const item = screen.getByTestId(`chain-legend-edge-${kind}`)
      expect(item.textContent).toContain(EDGE_KIND_TOKENS[kind].label)
      expect(item.getAttribute("data-color")).toBe(EDGE_KIND_TOKENS[kind].color)
    }
  })

  it("reads the case_issue violet token instead of inventing a colour", () => {
    render(<ChainLegend />)

    const caseIssue = screen.getByTestId("chain-legend-edge-case_issue")
    expect(caseIssue.getAttribute("data-color")).toBe(EDGE_KIND_TOKENS.case_issue.color)
    expect(caseIssue.getAttribute("data-color")).toBe("violet")
    expect(caseIssue.getAttribute("data-stroke")).toBe(EDGE_KIND_TOKENS.case_issue.stroke)
    // The related result_issue edge stays red; the two kinds must not merge.
    expect(screen.getByTestId("chain-legend-edge-result_issue").getAttribute("data-color")).toBe(
      EDGE_KIND_TOKENS.result_issue.color,
    )
  })

  it("draws impact_case as the only dashed relation", () => {
    render(<ChainLegend />)

    for (const kind of EDGE_KINDS) {
      const item = screen.getByTestId(`chain-legend-edge-${kind}`)
      expect(item.getAttribute("data-line-style")).toBe(kind === "impact_case" ? "dashed" : "solid")
    }
  })

  it("shows an empty hint and the abnormal/truncation explanation", () => {
    const { rerender } = render(<ChainLegend />)
    expect(screen.queryByTestId("chain-legend-empty")).toBeNull()
    expect(screen.getByTestId("chain-legend-note").textContent).toContain("仅异常")
    expect(screen.getByTestId("chain-legend-note").textContent).toContain("截断")

    rerender(<ChainLegend empty />)
    const hint = screen.getByTestId("chain-legend-empty")
    expect(hint.textContent).toContain("暂无")
  })

  it("keeps the legend readable without colour: every item has text and a glyph", () => {
    render(<ChainLegend />)

    const legend = screen.getByTestId("chain-legend")
    expect(within(legend).getAllByRole("listitem")).toHaveLength(
      NODE_KINDS.length + EDGE_KINDS.length,
    )
    for (const kind of NODE_KINDS) {
      expect(screen.getByTestId(`chain-legend-node-${kind}`).querySelector("svg")).not.toBeNull()
    }
    for (const kind of EDGE_KINDS) {
      expect(screen.getByTestId(`chain-legend-edge-${kind}`).querySelector("svg")).not.toBeNull()
    }
  })
})
