import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { RequirementSidebar } from "./RequirementSidebar"
import {
  requirementClosedFixture,
  requirementFixture,
  requirementSecondFixture,
} from "./test-fixtures"
import type { TargetOptions } from "./types"

const TARGET_OPTIONS: TargetOptions = {
  suts: [
    { id: "1001", label: "订单中心" },
    { id: "1002", label: "支付网关" },
  ],
  pages: [
    { id: "1301", label: "订单列表" },
    { id: "2301", label: "退款详情" },
  ],
  apis: [],
}

function renderSidebar(overrides: Partial<Parameters<typeof RequirementSidebar>[0]> = {}) {
  const props = {
    requirements: [requirementFixture, requirementSecondFixture, requirementClosedFixture],
    targetOptions: TARGET_OPTIONS,
    filter: "all" as const,
    onFilterChange: vi.fn(),
    selectedIds: [],
    onToggleSelection: vi.fn(),
    activeRequirementId: null,
    onSelectRequirement: vi.fn(),
    ...overrides,
  }
  return { props, ...render(<RequirementSidebar {...props} />) }
}

describe("RequirementSidebar", () => {
  afterEach(cleanup)

  it("renders counts, status filters and every requirement", () => {
    renderSidebar()

    expect(screen.getByTestId("requirement-sidebar")).toHaveTextContent("需求 3")
    expect(screen.getByText("已澄清 1 · 草案 1 · 已闭环 1")).toBeInTheDocument()
    expect(screen.getByTestId("req-filter-all")).toHaveTextContent("全部 3")
    expect(screen.getByTestId("req-filter-draft")).toHaveTextContent("草案 1")
    expect(screen.getByTestId("req-filter-clarified")).toHaveTextContent("已澄清 1")
    expect(screen.getByTestId("req-filter-closed")).toHaveTextContent("已闭环 1")
    expect(screen.getByText("退款到账时效")).toBeInTheDocument()
    expect(screen.getByText("大额订单人工审核")).toBeInTheDocument()
    expect(screen.getByText("购物车结算金额校验")).toBeInTheDocument()
  })

  it("filters the list by status and notifies the parent", () => {
    const onFilterChange = vi.fn()
    renderSidebar({ filter: "draft", onFilterChange })

    expect(screen.getByText("退款到账时效")).toBeInTheDocument()
    expect(screen.queryByText("大额订单人工审核")).not.toBeInTheDocument()
    expect(screen.getByTestId("req-filter-draft")).toHaveAttribute("aria-pressed", "true")

    fireEvent.click(screen.getByTestId("req-filter-clarified"))
    expect(onFilterChange).toHaveBeenCalledWith("clarified")
  })

  it("toggles selection and selects the active requirement", () => {
    const onToggleSelection = vi.fn()
    const onSelectRequirement = vi.fn()
    renderSidebar({ onToggleSelection, onSelectRequirement })

    fireEvent.click(screen.getByRole("checkbox", { name: "选择退款到账时效" }))
    expect(onToggleSelection).toHaveBeenCalledWith("6001")

    fireEvent.click(screen.getByRole("button", { name: "退款到账时效" }))
    expect(onSelectRequirement).toHaveBeenCalledWith("6001")
  })

  it("marks the active requirement and shows its target chips", () => {
    renderSidebar({ activeRequirementId: "6001" })

    const item = screen.getByTestId("requirement-item-6001")
    expect(item).toHaveAttribute("data-active", "true")
    expect(item).toHaveTextContent("支付网关")
    expect(item).toHaveTextContent("退款详情")
    expect(item).toHaveTextContent("v1")
    expect(item).toHaveTextContent("P1")
  })

  it("checks the selected requirement boxes", () => {
    renderSidebar({ selectedIds: ["6002"] })

    expect(screen.getByRole("checkbox", { name: "选择大额订单人工审核" })).toBeChecked()
    expect(screen.getByRole("checkbox", { name: "选择退款到账时效" })).not.toBeChecked()
  })

  it("shows the empty state when the filter has no requirements", () => {
    renderSidebar({ requirements: [], filter: "all" })
    expect(screen.getByText("暂无需求")).toBeInTheDocument()
  })

  it("renders the bulk bar and editor slots", () => {
    renderSidebar({
      bulk: <div data-testid="bulk-slot">bulk</div>,
      editor: <div data-testid="editor-slot">editor</div>,
    })
    expect(screen.getByTestId("bulk-slot")).toBeInTheDocument()
    expect(screen.getByTestId("editor-slot")).toBeInTheDocument()
  })
})
