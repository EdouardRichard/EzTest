import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { DesignMindmap } from "./DesignMindmap"
import { designGraphFixture } from "./test-fixtures"

function renderMindmap(overrides: Partial<Parameters<typeof DesignMindmap>[0]> = {}) {
  const props = {
    graph: designGraphFixture,
    selectedCaseId: null,
    onSelectCase: vi.fn(),
    onConfirmCase: vi.fn(),
    onConfirmScenario: vi.fn(),
    onConfirmRequirement: vi.fn(),
    ...overrides,
  }
  const { container } = render(<DesignMindmap {...props} />)
  return { ...props, container }
}

describe("DesignMindmap", () => {
  afterEach(cleanup)

  it("renders requirement, scenario and case nodes from the graph", async () => {
    renderMindmap()

    expect(screen.getByTestId("design-mindmap")).toBeInTheDocument()
    // Titles are asserted through the React Flow node test ids: the filter
    // options now carry the same plain titles, so getByText would be ambiguous.
    expect(await screen.findByTestId("rf__node-requirement-6001")).toHaveTextContent("退款到账时效")
    expect(screen.getByTestId("rf__node-scenario-5001")).toHaveTextContent("正常退款")
    expect(screen.getByText("退款 T+1 到账")).toBeInTheDocument()
    expect(screen.getByText("已澄清 v2")).toBeInTheDocument()
    expect(screen.getByTestId("case-card-7001")).toBeInTheDocument()
  })

  it("shows a confirm button on draft cases only", async () => {
    const props = renderMindmap()

    await screen.findByTestId("btn-confirm-case-7001")
    expect(screen.getByTestId("btn-confirm-case-7001")).toBeEnabled()
    expect(screen.queryByTestId("btn-confirm-case-7003")).not.toBeInTheDocument()
    expect(screen.queryByTestId("btn-confirm-case-7005")).not.toBeInTheDocument()

    fireEvent.click(screen.getByTestId("btn-confirm-case-7001"))
    expect(props.onConfirmCase).toHaveBeenCalledWith("7001")
  })

  it("confirms a whole scenario or requirement and disables empty scopes", async () => {
    const props = renderMindmap()

    await screen.findByTestId("btn-confirm-scenario-5001")
    fireEvent.click(screen.getByTestId("btn-confirm-scenario-5001"))
    expect(props.onConfirmScenario).toHaveBeenCalledWith("5001")
    expect(screen.getByTestId("btn-confirm-scenario-5002")).toBeDisabled()

    fireEvent.click(screen.getByTestId("btn-confirm-requirement-6001"))
    expect(props.onConfirmRequirement).toHaveBeenCalledWith("6001")
    expect(screen.getByTestId("btn-confirm-requirement-6002")).toBeDisabled()
  })

  it("selects a case and marks the selected card", async () => {
    const props = renderMindmap({ selectedCaseId: "7001" })

    const card = await screen.findByTestId("case-card-7001")
    expect(card).toHaveAttribute("data-selected", "true")
    fireEvent.click(screen.getByTestId("case-select-7001"))
    expect(props.onSelectCase).toHaveBeenCalledWith("7001")
  })

  it("shows SUT chips, page chips and the cross-system marker", async () => {
    renderMindmap()

    await screen.findByTestId("case-card-7003")
    expect(screen.getByTestId("case-card-7003")).toHaveTextContent("跨系统")
    expect(screen.getByTestId("case-card-7001")).toHaveTextContent("P1")
    expect(screen.getByTestId("case-card-7001")).toHaveTextContent("draft")
  })

  it("exposes zoom and fit controls", async () => {
    renderMindmap()

    expect(await screen.findByTestId("design-zoom-in")).toBeInTheDocument()
    expect(screen.getByTestId("design-zoom-out")).toBeInTheDocument()
    expect(screen.getByTestId("design-fit-view")).toBeInTheDocument()
    fireEvent.click(screen.getByTestId("design-zoom-in"))
  })

  it("renders the graph overview minimap", async () => {
    // D-07: the inline canvas clipped a tall graph with no overview; the
    // minimap is the approved discoverability affordance.
    renderMindmap()

    const minimap = await screen.findByTestId("rf__minimap")
    expect(minimap).toBeInTheDocument()
    expect(within(minimap).getByRole("img", { name: "脑图缩略图" })).toBeInTheDocument()
  })

  it("renders minimap nodes once node dimensions are measured", async () => {
    // R2-D-03: the minimap reads `userNode.measured`, which only exists when the
    // controlled `nodes` prop feeds React Flow's dimensions changes back in.
    // Without that merge the minimap renders only its title and mask.
    const { container } = renderMindmap()

    await screen.findByTestId("rf__minimap")
    await waitFor(() => {
      expect(container.querySelectorAll(".react-flow__minimap-node").length).toBeGreaterThan(0)
    })
  })

  it("keeps the canvas above a minimum readable height", async () => {
    renderMindmap()

    expect(await screen.findByTestId("design-mindmap-canvas")).toHaveClass("min-h-[180px]")
  })

  it("hides the empty batch bar in compact mode and reveals it after a selection", async () => {
    // Short viewports prioritise the canvas: an empty batch bar costs ~45px.
    renderMindmap({ compact: true })

    await screen.findByTestId("case-card-7001")
    expect(screen.queryByTestId("case-batch-bar")).not.toBeInTheDocument()

    fireEvent.click(screen.getByTestId("case-checkbox-7001"))
    expect(await screen.findByTestId("case-batch-bar")).toHaveTextContent("已选 1 条")
  })

  it("keeps the always-visible batch bar outside compact mode", async () => {
    renderMindmap()

    expect(await screen.findByTestId("case-batch-bar")).toBeInTheDocument()
  })

  it("scrolls the mindmap section when a compact selection pushes past the 220px graph pane", async () => {
    // D-01 follow-up: a 220px graph pane cannot hold compact filters (~41) +
    // the batch bar (~45) + the 180px canvas floor. The section owns the
    // internal scroll instead of clipping the canvas and its bottom-right
    // minimap; the bars must not be squeezed by the flex column.
    renderMindmap({ compact: true })

    await screen.findByTestId("case-card-7001")
    fireEvent.click(screen.getByTestId("case-checkbox-7001"))

    const batchBar = await screen.findByTestId("case-batch-bar")
    expect(batchBar).toHaveClass("shrink-0")

    const section = screen.getByTestId("design-mindmap")
    expect(section).toHaveClass("overflow-auto")
    expect(section).not.toHaveClass("overflow-hidden")

    expect(screen.getByTestId("design-case-filters")).toHaveClass("shrink-0")
    expect(screen.getByTestId("design-mindmap-canvas")).toHaveClass("min-h-[180px]")
    expect(screen.getByTestId("rf__minimap")).toBeInTheDocument()
  })

  it("renders compact filters with accessible labels on a single short row", async () => {
    renderMindmap({ compact: true })

    const filters = screen.getByTestId("design-case-filters")
    expect(filters).toHaveClass("py-1")
    // The visible labels become screen-reader-only, not removed.
    expect(screen.getByLabelText("搜索")).toBe(screen.getByTestId("design-search"))
    expect(within(filters).getByLabelText("状态")).toBeInTheDocument()
  })
})
