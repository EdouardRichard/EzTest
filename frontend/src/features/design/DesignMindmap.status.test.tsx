import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const reactFlow = vi.hoisted(() => ({
  fitView: vi.fn(),
  zoomIn: vi.fn(),
  zoomOut: vi.fn(),
}))

// Keep the real React Flow renderer/provider (so nodes reach the DOM) but
// replace the imperative hook so fitView calls can be observed in jsdom.
vi.mock("@xyflow/react", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@xyflow/react")>()
  return { ...actual, useReactFlow: () => reactFlow }
})

import type { CaseRunVerdict } from "@/features/execution/types"

import { DesignMindmap } from "./DesignMindmap"
import { designGraphFixture } from "./test-fixtures"

function renderMindmap(overrides: Partial<Parameters<typeof DesignMindmap>[0]> = {}) {
  const props: Parameters<typeof DesignMindmap>[0] = {
    graph: designGraphFixture,
    selectedCaseId: null,
    onSelectCase: vi.fn(),
    onConfirmCase: vi.fn(),
    onConfirmScenario: vi.fn(),
    onConfirmRequirement: vi.fn(),
    ...overrides,
  }
  const view = render(<DesignMindmap {...props} />)
  const rerender = (next: Partial<Parameters<typeof DesignMindmap>[0]>) => {
    view.rerender(<DesignMindmap {...props} {...next} />)
  }
  return { props, rerender }
}

/** A refetch that returns the same graph hash but new object instances. */
function sameHashRefreshedGraph(): typeof designGraphFixture {
  return {
    ...designGraphFixture,
    requirements: designGraphFixture.requirements.map((requirement) => ({
      ...requirement,
      scenarios: requirement.scenarios.map((scenario) => ({
        ...scenario,
        cases: scenario.cases.map((designCase) => ({ ...designCase })),
      })),
    })),
  }
}

function changeFilter(testId: string, value: string): void {
  fireEvent.change(screen.getByTestId(testId), { target: { value } })
}

const ALL_RESULTS: Record<string, CaseRunVerdict> = {
  "7001": "passed",
  "7003": "failed",
  "7005": "blocked",
}

describe("DesignMindmap status colours", () => {
  beforeEach(() => {
    reactFlow.fitView.mockReset()
    reactFlow.zoomIn.mockReset()
    reactFlow.zoomOut.mockReset()
  })

  afterEach(() => {
    cleanup()
    vi.useRealTimers()
  })

  it("colours case nodes by version status", async () => {
    renderMindmap()

    const draft = await screen.findByTestId("rf__node-case-7001")
    const confirmed = screen.getByTestId("rf__node-case-7003")
    const stale = screen.getByTestId("rf__node-case-7005")

    expect(draft).toHaveClass("case-status-draft")
    expect(draft).toHaveClass("[&>div]:border-info/60")
    expect(confirmed).toHaveClass("case-status-confirmed")
    expect(confirmed).toHaveClass("[&>div]:border-success/60")
    expect(stale).toHaveClass("case-status-stale")
    expect(stale).toHaveClass("[&>div]:border-warning/60")
  })

  it("colours case nodes by execution result", async () => {
    renderMindmap({ executionResults: ALL_RESULTS })

    const passed = await screen.findByTestId("rf__node-case-7001")
    const failed = screen.getByTestId("rf__node-case-7003")
    const blocked = screen.getByTestId("rf__node-case-7005")

    expect(passed).toHaveClass("case-result-passed")
    expect(passed).toHaveClass("[&>div]:shadow-[inset_4px_0_0_var(--color-success)]")
    expect(failed).toHaveClass("case-result-failed")
    expect(failed).toHaveClass("[&>div]:shadow-[inset_4px_0_0_var(--color-destructive)]")
    expect(blocked).toHaveClass("case-result-blocked")
    expect(blocked).toHaveClass("[&>div]:shadow-[inset_4px_0_0_var(--color-warning)]")
  })

  it("hides nodes that do not match the status filter", async () => {
    renderMindmap()
    await screen.findByTestId("case-card-7001")

    changeFilter("design-filter-status", "confirmed")

    await waitFor(() => expect(screen.queryByTestId("case-card-7001")).not.toBeInTheDocument())
    expect(screen.getByTestId("case-card-7003")).toBeInTheDocument()
    expect(screen.queryByTestId("case-card-7005")).not.toBeInTheDocument()
    // The empty scenario (5002) has no matching case and is pruned too.
    expect(screen.queryByTestId("rf__node-scenario-5002")).not.toBeInTheDocument()
    expect(screen.getByTestId("rf__node-scenario-5001")).toBeInTheDocument()
  })

  it("hides nodes that do not match the execution result filter", async () => {
    renderMindmap({ executionResults: ALL_RESULTS })
    await screen.findByTestId("case-card-7001")

    changeFilter("design-filter-result", "failed")

    await waitFor(() => expect(screen.queryByTestId("case-card-7001")).not.toBeInTheDocument())
    expect(screen.getByTestId("case-card-7003")).toBeInTheDocument()
    expect(screen.queryByTestId("case-card-7005")).not.toBeInTheDocument()
  })

  it("hides whole requirement and scenario branches when filtered", async () => {
    renderMindmap()
    await screen.findByTestId("case-card-7001")

    changeFilter("design-filter-requirement", "6002")

    await waitFor(() =>
      expect(screen.queryByTestId("rf__node-requirement-6001")).not.toBeInTheDocument(),
    )
    expect(screen.queryByTestId("case-card-7001")).not.toBeInTheDocument()
    expect(screen.getByTestId("rf__node-requirement-6002")).toBeInTheDocument()

    // Selecting a requirement resets the scenario filter.
    changeFilter("design-filter-requirement", "all")
    await screen.findByTestId("case-card-7001")
    changeFilter("design-filter-scenario", "5002")

    await waitFor(() => expect(screen.queryByTestId("case-card-7001")).not.toBeInTheDocument())
    expect(screen.getByTestId("case-card-7005")).toBeInTheDocument()
    expect(screen.queryByTestId("rf__node-scenario-5001")).not.toBeInTheDocument()
    expect(screen.queryByTestId("rf__node-requirement-6002")).not.toBeInTheDocument()
    expect(screen.getByTestId("rf__node-requirement-6001")).toBeInTheDocument()
  })

  it("highlights search matches and fits the view to them", async () => {
    renderMindmap()
    await screen.findByTestId("case-card-7001")

    fireEvent.change(screen.getByTestId("design-search"), { target: { value: "T+1" } })

    await waitFor(() =>
      expect(screen.getByTestId("rf__node-case-7001")).toHaveClass("mindmap-search-hit"),
    )
    expect(screen.getByTestId("rf__node-case-7003")).not.toHaveClass("mindmap-search-hit")

    await waitFor(() => expect(reactFlow.fitView).toHaveBeenCalled())
    const options = reactFlow.fitView.mock.calls.at(-1)?.[0] as {
      nodes?: Array<{ id: string }>
    }
    expect(options.nodes?.map((node) => node.id)).toEqual(["case-7001"])
  })

  it("throttles fitView while the search query changes rapidly", () => {
    vi.useFakeTimers()
    renderMindmap()
    const search = screen.getByTestId("design-search")

    fireEvent.change(search, { target: { value: "退" } })
    fireEvent.change(search, { target: { value: "退款" } })
    fireEvent.change(search, { target: { value: "退款 T" } })

    // Leading edge fires once; the burst is coalesced into one trailing fit.
    expect(reactFlow.fitView).toHaveBeenCalledTimes(1)
    act(() => vi.advanceTimersByTime(249))
    expect(reactFlow.fitView).toHaveBeenCalledTimes(1)
    act(() => vi.advanceTimersByTime(1))
    expect(reactFlow.fitView).toHaveBeenCalledTimes(2)

    const options = reactFlow.fitView.mock.calls.at(-1)?.[0] as {
      nodes?: Array<{ id: string }>
    }
    expect(options.nodes?.map((node) => node.id)).toEqual(["case-7001"])
  })

  it("does not fit a stale match after the search is cleared", () => {
    vi.useFakeTimers()
    renderMindmap()
    const search = screen.getByTestId("design-search")

    fireEvent.change(search, { target: { value: "退款" } })
    fireEvent.change(search, { target: { value: "退款 T" } })
    fireEvent.change(search, { target: { value: "" } })

    act(() => vi.advanceTimersByTime(400))
    expect(reactFlow.fitView).toHaveBeenCalledTimes(1)
  })

  it("disables the result filter until results exist and resets it when they disappear", async () => {
    const { rerender } = renderMindmap()
    await screen.findByTestId("case-card-7001")

    expect(screen.getByTestId("design-filter-result")).toBeDisabled()
    expect(screen.getByTestId("design-filter-result")).toHaveAttribute(
      "title",
      "暂无执行结果，执行后可筛选",
    )

    rerender({ executionResults: ALL_RESULTS })
    await waitFor(() => expect(screen.getByTestId("design-filter-result")).toBeEnabled())
    changeFilter("design-filter-result", "failed")
    await waitFor(() => expect(screen.queryByTestId("case-card-7001")).not.toBeInTheDocument())
    expect(screen.getByTestId("case-card-7003")).toBeInTheDocument()

    rerender({ executionResults: {} })
    await waitFor(() => expect(screen.getByTestId("design-filter-result")).toBeDisabled())
    expect(screen.getByTestId("design-filter-result")).toHaveValue("all")
    expect(screen.getByTestId("case-card-7001")).toBeInTheDocument()
  })

  it("labels filter options by title/name without id suffixes", async () => {
    renderMindmap()
    await screen.findByTestId("case-card-7001")

    // Option labels stay the plain graph titles; node text is asserted through
    // the React Flow node test ids instead of a `· #id` collision workaround.
    expect(screen.getByRole("option", { name: "退款到账时效" })).toBeInTheDocument()
    expect(screen.getByRole("option", { name: "正常退款" })).toBeInTheDocument()
    expect(screen.getByTestId("rf__node-requirement-6001")).toHaveTextContent("退款到账时效")
    expect(screen.getByTestId("rf__node-scenario-5001")).toHaveTextContent("正常退款")
  })

  it("keeps the mindmap full height instead of a fixed canvas height", async () => {
    renderMindmap()

    const section = await screen.findByTestId("design-mindmap")
    expect(section).toHaveClass("h-full")
    expect(section).not.toHaveClass("h-[560px]")
  })

  it("refreshes rendered scenario data after a same-hash graph refresh", async () => {
    const { rerender } = renderMindmap()
    await screen.findByTestId("scenario-node-5001")
    expect(screen.getByTestId("scenario-node-5001")).toHaveTextContent("SUT 1 个 · 页面 1 个")

    const refreshedGraph = sameHashRefreshedGraph()
    const scenario = refreshedGraph.requirements[0]?.scenarios[0]
    if (scenario === undefined) throw new Error("fixture is missing the scenario")
    scenario.version_sut_ids = ["1001", "1002"]
    scenario.page_ids = ["2301", "2302"]
    scenario.description = "审核通过后原路退回（更新）"

    rerender({ graph: refreshedGraph })

    await waitFor(() =>
      expect(screen.getByTestId("scenario-node-5001")).toHaveTextContent("SUT 2 个 · 页面 2 个"),
    )
  })

  it("reuses cached positions but rebuilds node data for a same-hash refresh", async () => {
    const { cachedLayoutDesignGraph } = await import("./layout")

    const first = cachedLayoutDesignGraph(designGraphFixture)
    const refreshedGraph = sameHashRefreshedGraph()
    const refreshed = cachedLayoutDesignGraph(refreshedGraph)

    expect(refreshed).not.toBe(first)
    expect(refreshed.nodes.map((node) => [node.id, node.position])).toEqual(
      first.nodes.map((node) => [node.id, node.position]),
    )

    const requirementNode = refreshed.nodes.find((node) => node.id === "requirement-6001")
    expect(requirementNode?.data.requirement).toBe(refreshedGraph.requirements[0])
    const caseNode = refreshed.nodes.find((node) => node.id === "case-7001")
    expect(caseNode?.data.kind).toBe("case")
    if (caseNode?.data.kind !== "case") throw new Error("expected a case node")
    expect(caseNode.data.case).toBe(refreshedGraph.requirements[0]?.scenarios[0]?.cases[0])

    const changed = cachedLayoutDesignGraph({
      ...designGraphFixture,
      graph_hash: "f".repeat(64),
    })
    expect(changed).not.toBe(first)
    expect(changed.nodes).not.toBe(first.nodes)
  })

  it("returns refreshed scenario data when its description changes under the same graph hash", async () => {
    const { cachedLayoutDesignGraph, matchDesignNodes } = await import("./layout")

    const first = cachedLayoutDesignGraph(designGraphFixture)
    const refreshedGraph = sameHashRefreshedGraph()
    const scenario = refreshedGraph.requirements[0]?.scenarios[0]
    if (scenario === undefined) throw new Error("fixture is missing the scenario")
    scenario.description = "审核通过后原路退回（更新）"

    const refreshed = cachedLayoutDesignGraph(refreshedGraph)
    const firstScenarioNode = first.nodes.find((node) => node.id === "scenario-5001")
    const refreshedScenarioNode = refreshed.nodes.find((node) => node.id === "scenario-5001")

    expect(refreshedScenarioNode?.position).toEqual(firstScenarioNode?.position)
    expect(refreshedScenarioNode?.data.kind).toBe("scenario")
    if (refreshedScenarioNode?.data.kind !== "scenario") throw new Error("expected a scenario node")
    expect(refreshedScenarioNode.data.scenario.description).toBe("审核通过后原路退回（更新）")
    expect(matchDesignNodes(refreshed, "更新").map((node) => node.id)).toEqual(["scenario-5001"])
  })
})
