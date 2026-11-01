import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ImpactPanel } from "../components/ImpactPanel"
import { candidateRequirementFixture, candidatesFixture } from "../test-fixtures"

afterEach(cleanup)

function renderPanel(overrides: Partial<Parameters<typeof ImpactPanel>[0]> = {}) {
  const props = {
    candidates: candidatesFixture,
    pendingIds: {},
    focusedCandidateId: null,
    degraded: false,
    onToggleSelected: vi.fn(),
    onFocusCandidate: vi.fn(),
    ...overrides,
  }
  render(<ImpactPanel {...props} />)
  return props
}

describe("ImpactPanel", () => {
  it("groups candidates by subject kind with source and reason", () => {
    renderPanel({ focusedCandidateId: candidateRequirementFixture.id })

    expect(screen.getByRole("region", { name: "影响候选" })).toBeInTheDocument()
    expect(screen.getByText("需求")).toBeInTheDocument()
    expect(screen.getByText("用例版本")).toBeInTheDocument()
    expect(screen.getByText("需求描述了变更的退款流程")).toBeInTheDocument()
    expect(screen.getByText("直接")).toBeInTheDocument()
    expect(screen.getByText("关系")).toBeInTheDocument()
    expect(screen.getByLabelText("选择影响候选 需求 6001").closest("li")).toHaveAttribute(
      "aria-current",
      "true",
    )
  })

  it("toggles a candidate selection through the checkbox", () => {
    const props = renderPanel()

    fireEvent.click(screen.getByLabelText("选择影响候选 需求 6001"))
    expect(props.onToggleSelected).toHaveBeenCalledWith(candidateRequirementFixture, true)
    expect(screen.getByLabelText("选择影响候选 需求 6001").closest("li")).toBeInTheDocument()
  })

  it("disables the checkbox while the PATCH is pending", () => {
    renderPanel({ pendingIds: { "8101": true } })

    expect(screen.getByLabelText("选择影响候选 需求 6001")).toBeDisabled()
    expect(screen.getByText("保存中…")).toBeInTheDocument()
  })

  it("focuses a candidate for deep linking", () => {
    const props = renderPanel()

    fireEvent.click(screen.getByRole("button", { name: "定位影响候选 需求 6001" }))
    expect(props.onFocusCandidate).toHaveBeenCalledWith("8101")
  })

  it("shows the AI degraded warning and an empty state", () => {
    const { rerender } = render(
      <ImpactPanel candidates={[]} pendingIds={{}} degraded onToggleSelected={vi.fn()} />,
    )
    expect(screen.getByText("AI 推理降级")).toBeInTheDocument()
    expect(screen.getByText("暂无影响候选")).toBeInTheDocument()

    rerender(
      <ImpactPanel
        candidates={candidatesFixture}
        pendingIds={{}}
        degraded={false}
        onToggleSelected={vi.fn()}
      />,
    )
    expect(screen.queryByText("AI 推理降级")).not.toBeInTheDocument()
  })
})
