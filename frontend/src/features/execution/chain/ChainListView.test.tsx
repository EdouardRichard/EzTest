import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { ChainListView } from "./ChainListView"
import type { ChainSnapshot, ChainSnapshotEdge, ChainSnapshotNode } from "./chain-types"

const R1 = "1844674407370955101"
const C1 = "1844674407370955201"
const CR1 = "1844674407370955301"
const I1 = "1844674407370955401"
const I2 = "1844674407370955402"

function node(
  id: string,
  kind: ChainSnapshotNode["kind"],
  overrides: Partial<ChainSnapshotNode> = {},
): ChainSnapshotNode {
  return {
    id: `${kind}:${id}`,
    kind,
    title: `${kind} ${id}`,
    status: kind === "result" ? "failed" : "open",
    ...overrides,
  }
}

const NODES: ChainSnapshotNode[] = [
  node(R1, "requirement", { title: "登录需求", status: "clarified" }),
  node(C1, "case", { title: "登录成功", status: "executed", requirementIds: [R1] }),
  node(CR1, "result", {
    title: "登录成功",
    status: "failed",
    caseNodeId: C1,
    caseRunId: CR1,
    evidenceCount: 1,
  }),
  node(I1, "issue", { title: "提交失败", status: "open", severity: "major", evidenceCount: 2 }),
  node(I2, "issue", { title: "缺需求", status: "confirmed", severity: "minor", evidenceCount: 0 }),
]

const EDGES: ChainSnapshotEdge[] = [
  {
    id: "e-req-case",
    source: `requirement:${R1}`,
    target: `case:${C1}`,
    kind: "requirement_case",
  },
  { id: "e-case-result", source: `case:${C1}`, target: `result:${CR1}`, kind: "case_result" },
  {
    id: "e-result-issue",
    source: `result:${CR1}`,
    target: `issue:${I1}`,
    kind: "result_issue",
  },
]

const SNAPSHOT: ChainSnapshot = {
  versionId: "version:1",
  batch: { id: "batch:1", batchNo: 1, kind: "full", status: "completed" },
  nodes: NODES,
  edges: EDGES,
  counts: {
    requirements: 1,
    cases: 1,
    results: 1,
    issues: 2,
    byResultStatus: {},
    byIssueSeverity: {},
    uncoveredRequirements: 0,
    revalidationCases: 0,
    supplementCases: 0,
  },
  truncated: { requirements: false, cases: false, results: false, issues: false },
  generatedAt: "2026-10-29T00:00:00Z",
}

afterEach(() => {
  cleanup()
})

describe("ChainListView", () => {
  it("lists every visible node and the unlinked issues as a table", () => {
    render(<ChainListView snapshot={SNAPSHOT} onSelect={vi.fn()} />)

    const table = screen.getByTestId("chain-list-view")
    expect(table.tagName).toBe("TABLE")
    expect(table.textContent).toContain("类型")
    expect(table.textContent).toContain("标题")
    expect(table.textContent).toContain("状态")
    expect(table.textContent).toContain("关联")
    expect(table.textContent).toContain("证据")

    // 4 layout nodes + the unlinked issue I2.
    expect(screen.getAllByTestId(/^chain-list-row-/)).toHaveLength(5)

    const requirementRow = screen.getByTestId(`chain-list-row-${R1}`)
    expect(requirementRow.getAttribute("data-kind")).toBe("requirement")
    expect(requirementRow.textContent).toContain("需求")
    expect(requirementRow.textContent).toContain("登录需求")
    expect(requirementRow.textContent).toContain("已澄清")

    const resultRow = screen.getByTestId(`chain-list-row-${CR1}`)
    expect(resultRow.getAttribute("data-status")).toBe("failed")
    expect(resultRow.textContent).toContain("失败")
    expect(resultRow.textContent).toContain("登录成功")
    expect(resultRow.textContent).toContain("证据 1")

    const unlinked = screen.getByTestId(`chain-list-row-${I2}`)
    expect(unlinked.textContent).toContain("缺需求")
    expect(unlinked.textContent).toContain("未关联")
  })

  it("opens a node through its keyboard-reachable title button", () => {
    const onSelect = vi.fn()
    render(<ChainListView snapshot={SNAPSHOT} onSelect={onSelect} />)

    const button = screen.getByTestId(`chain-list-open-${CR1}`)
    expect(button.tagName).toBe("BUTTON")
    expect(button.getAttribute("aria-label")).toContain("结果")
    expect(button.getAttribute("aria-label")).toContain("登录成功")

    button.focus()
    expect(document.activeElement).toBe(button)
    fireEvent.keyDown(button, { key: "Enter" })
    expect(onSelect).toHaveBeenCalledTimes(1)
    expect(onSelect).toHaveBeenCalledWith(`result:${CR1}`)
  })

  it("hides a collapsed requirement group but keeps its unlinked issues", () => {
    render(
      <ChainListView snapshot={SNAPSHOT} collapsed={[`requirement:${R1}`]} onSelect={vi.fn()} />,
    )

    expect(screen.getByTestId(`chain-list-row-${R1}`)).not.toBeNull()
    expect(screen.queryByTestId(`chain-list-row-${C1}`)).toBeNull()
    expect(screen.queryByTestId(`chain-list-row-${CR1}`)).toBeNull()
    expect(screen.queryByTestId(`chain-list-row-${I1}`)).toBeNull()
    expect(screen.getByTestId(`chain-list-row-${I2}`)).not.toBeNull()
  })

  it("marks the selected row with aria-selected", () => {
    render(<ChainListView snapshot={SNAPSHOT} selectedId={`case:${C1}`} onSelect={vi.fn()} />)

    expect(screen.getByTestId(`chain-list-row-${C1}`).getAttribute("aria-selected")).toBe("true")
    expect(screen.getByTestId(`chain-list-row-${R1}`).getAttribute("aria-selected")).toBe("false")
  })
})
