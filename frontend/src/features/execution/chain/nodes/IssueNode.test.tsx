import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"

import type { ChainSnapshotNode } from "../chain-types"
import { IssueNode } from "./IssueNode"

const ISSUE: ChainSnapshotNode = {
  id: "issue:1844674407370955501",
  kind: "issue",
  title: "提交订单失败",
  status: "open",
  severity: "major",
  category: "defect",
  confidence: "high",
  caseRunId: "1844674407370955302",
  caseNodeId: "1844674407370955202",
  evidenceCount: 1,
  codeRefCount: 2,
}

afterEach(cleanup)

describe("IssueNode", () => {
  it("renders severity, status, category, confidence and evidence count", () => {
    render(<IssueNode data={ISSUE} />)

    const card = screen.getByTestId("chain-node-issue")
    expect(card.getAttribute("data-node-id")).toBe(ISSUE.id)
    expect(screen.getByTestId("chain-node-issue-severity").textContent).toBe("严重")
    expect(card.textContent).toContain("待确认")
    expect(screen.getByTestId("chain-node-issue-category").textContent).toBe("应用缺陷")
    expect(screen.getByTestId("chain-node-issue-confidence").textContent).toBe("高")
    expect(screen.getByTestId("chain-node-issue-evidence").textContent).toBe("证据 1")
    expect(card.getAttribute("aria-label")).toContain("提交订单失败")
    expect(card.getAttribute("aria-label")).toContain("严重")
    expect(card.getAttribute("aria-label")).toContain("待确认")
  })

  it("labels a blocker severity and hides an unknown category behind an em dash", () => {
    render(<IssueNode data={{ ...ISSUE, severity: "blocker", category: "" }} />)

    expect(screen.getByTestId("chain-node-issue-severity").textContent).toBe("阻断")
    expect(screen.getByTestId("chain-node-issue-category").textContent).toBe("—")
  })
})
