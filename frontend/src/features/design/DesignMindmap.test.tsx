import { cleanup, fireEvent, render, screen } from "@testing-library/react"
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
  render(<DesignMindmap {...props} />)
  return props
}

describe("DesignMindmap", () => {
  afterEach(cleanup)

  it("renders requirement, scenario and case nodes from the graph", async () => {
    renderMindmap()

    expect(screen.getByTestId("design-mindmap")).toBeInTheDocument()
    expect(await screen.findByText("退款到账时效")).toBeInTheDocument()
    expect(screen.getByText("正常退款")).toBeInTheDocument()
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
})
