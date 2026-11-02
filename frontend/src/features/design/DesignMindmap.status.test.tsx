import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
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
  render(<DesignMindmap {...props} />)
  return props
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

  afterEach(cleanup)

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

  it("throttles fitView while the search query changes rapidly", async () => {
    renderMindmap()
    await screen.findByTestId("case-card-7001")
    const search = screen.getByTestId("design-search")

    fireEvent.change(search, { target: { value: "退" } })
    fireEvent.change(search, { target: { value: "退款" } })
    fireEvent.change(search, { target: { value: "退款 T" } })

    await waitFor(() => expect(reactFlow.fitView).toHaveBeenCalledTimes(2), { timeout: 2000 })
  })

  it("does not fit a stale match after the search is cleared", async () => {
    renderMindmap()
    await screen.findByTestId("case-card-7001")
    const search = screen.getByTestId("design-search")

    fireEvent.change(search, { target: { value: "退款" } })
    fireEvent.change(search, { target: { value: "退款 T" } })
    fireEvent.change(search, { target: { value: "" } })

    await new Promise((resolve) => setTimeout(resolve, 400))
    expect(reactFlow.fitView).toHaveBeenCalledTimes(1)
  })

  it("reuses the layout for an unchanged graph version", async () => {
    const { cachedLayoutDesignGraph } = await import("./layout")

    const first = cachedLayoutDesignGraph(designGraphFixture)
    const refreshed = cachedLayoutDesignGraph({ ...designGraphFixture })
    expect(refreshed).toBe(first)

    const changed = cachedLayoutDesignGraph({
      ...designGraphFixture,
      graph_hash: "f".repeat(64),
    })
    expect(changed).not.toBe(first)
    expect(changed.nodes).not.toBe(first.nodes)
  })
})
