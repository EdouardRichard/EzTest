import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"

import type { ChainSnapshotNode } from "../chain-types"
import { ResultNode } from "./ResultNode"

const RESULT: ChainSnapshotNode = {
  id: "result:1844674407370955302",
  kind: "result",
  title: "提交订单",
  status: "failed",
  caseRunId: "1844674407370955302",
  caseNodeId: "1844674407370955202",
  attempt: 2,
  durationMs: 1500,
  failureClass: "application",
  evidenceCount: 2,
  summary: "提交订单返回 500",
}

afterEach(cleanup)

describe("ResultNode", () => {
  it("renders status, attempt, duration, evidence count, failure class and summary", () => {
    render(<ResultNode data={RESULT} />)

    const card = screen.getByTestId("chain-node-result")
    expect(card.getAttribute("data-node-id")).toBe(RESULT.id)
    expect(card.textContent).toContain("失败")
    expect(screen.getByTestId("chain-node-result-attempt").textContent).toBe("第 2 次")
    expect(screen.getByTestId("chain-node-result-duration").textContent).toBe("1.5 s")
    expect(screen.getByTestId("chain-node-result-evidence").textContent).toBe("证据 2")
    expect(screen.getByTestId("chain-node-result-failure").textContent).toContain("应用缺陷")
    expect(screen.getByTestId("chain-node-result-summary").textContent).toBe("提交订单返回 500")
    expect(card.getAttribute("aria-label")).toContain("提交订单")
    expect(card.getAttribute("aria-label")).toContain("失败")
  })

  it("keeps sub-second durations in milliseconds", () => {
    render(<ResultNode data={{ ...RESULT, durationMs: 800 }} />)

    expect(screen.getByTestId("chain-node-result-duration").textContent).toBe("800 ms")
  })

  it("does not render a failure badge for a passed run", () => {
    render(
      <ResultNode data={{ ...RESULT, status: "passed", failureClass: "", evidenceCount: 0 }} />,
    )

    expect(screen.getByTestId("chain-node-result").textContent).toContain("通过")
    expect(screen.queryByTestId("chain-node-result-failure")).toBeNull()
    expect(screen.getByTestId("chain-node-result-evidence").textContent).toBe("证据 0")
  })
})
