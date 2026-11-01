import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import { ProposalPanel } from "../components/ProposalPanel"
import { proposalApiFixture, proposalPageFixture } from "../test-fixtures"

afterEach(cleanup)

function renderPanel(overrides: Partial<Parameters<typeof ProposalPanel>[0]> = {}) {
  const props = {
    proposals: [proposalPageFixture, proposalApiFixture],
    loading: false,
    error: null,
    generating: false,
    result: null,
    onGenerate: vi.fn(),
    onRetry: vi.fn(),
    ...overrides,
  }
  render(<ProposalPanel {...props} />)
  return props
}

describe("ProposalPanel", () => {
  it("lists proposed pages and APIs with their source", () => {
    renderPanel()

    expect(screen.getByRole("region", { name: "页面/API 提议" })).toBeInTheDocument()
    expect(screen.getByText("退款页")).toBeInTheDocument()
    expect(screen.getByText("/refund")).toBeInTheDocument()
    expect(screen.getByText("POST /api/refunds")).toBeInTheDocument()
    expect(screen.getByText("代码")).toBeInTheDocument()
    expect(screen.getByText("AI")).toBeInTheDocument()
  })

  it("generates proposals and shows created/skipped counts", () => {
    const props = renderPanel({
      result: {
        pages: { created: 1, skipped: 0, ids: ["3001"] },
        apis: { created: 1, skipped: 2, ids: ["3101"] },
      },
    })

    expect(screen.getByText(/页面 新增 1 · 跳过 0/)).toBeInTheDocument()
    expect(screen.getByText(/API 新增 1 · 跳过 2/)).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "从代码生成提议" }))
    expect(props.onGenerate).toHaveBeenCalledTimes(1)
  })

  it("disables generation while the request is running", () => {
    renderPanel({ generating: true })

    expect(screen.getByRole("button", { name: "从代码生成提议" })).toBeDisabled()
  })

  it("handles empty and error states", () => {
    const { rerender } = render(
      <ProposalPanel
        proposals={[]}
        loading={false}
        error={null}
        generating={false}
        result={null}
        onGenerate={vi.fn()}
        onRetry={vi.fn()}
      />,
    )
    expect(screen.getByText("暂无页面/API 提议")).toBeInTheDocument()

    const onRetry = vi.fn()
    rerender(
      <ProposalPanel
        proposals={[]}
        loading={false}
        error={new ApiError({ status: 503, code: "unavailable", message: "提议服务不可用" })}
        generating={false}
        result={null}
        onGenerate={vi.fn()}
        onRetry={onRetry}
      />,
    )
    expect(screen.getByText("页面/API 提议加载失败")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(onRetry).toHaveBeenCalledTimes(1)
  })
})
