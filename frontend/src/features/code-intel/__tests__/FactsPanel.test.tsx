import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import { FactsPanel } from "../components/FactsPanel"
import { factsFixture, fileWindowFixture } from "../test-fixtures"

afterEach(cleanup)

function renderPanel(overrides: Partial<Parameters<typeof FactsPanel>[0]> = {}) {
  const props = {
    facts: factsFixture,
    hasMore: false,
    loading: false,
    error: null,
    focusedFactId: null,
    fileWindow: null,
    fileWindowFact: null,
    fileWindowLoading: false,
    fileWindowError: null,
    onOpenFile: vi.fn(),
    onReview: vi.fn(),
    onRetry: vi.fn(),
    onRetryFileWindow: vi.fn(),
    ...overrides,
  }
  render(<FactsPanel {...props} />)
  return props
}

describe("FactsPanel", () => {
  it("groups facts by kind, marks low-confidence clues and offers review", () => {
    const props = renderPanel({ focusedFactId: "9101" })

    expect(screen.getByRole("region", { name: "代码事实" })).toBeInTheDocument()
    expect(screen.getByText("路由")).toBeInTheDocument()
    expect(screen.getByText("客户端调用")).toBeInTheDocument()
    expect(screen.getByText("线索")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "查看事实 /refund" }).closest("li")).toHaveAttribute(
      "aria-current",
      "true",
    )

    fireEvent.click(screen.getByRole("button", { name: "查看事实 /refund" }))
    expect(props.onOpenFile).toHaveBeenCalledWith(factsFixture[0])

    fireEvent.click(screen.getAllByRole("button", { name: "复核" })[0])
    expect(props.onReview).toHaveBeenCalledWith(factsFixture[0])
  })

  it("warns when only the first fact page is shown", () => {
    renderPanel({ hasMore: true })
    expect(screen.getByText("仅显示前 100 条（还有更多）")).toBeInTheDocument()
  })

  it("renders the fact file window with highlighted lines", () => {
    renderPanel({
      fileWindow: fileWindowFixture,
      fileWindowFact: factsFixture[0],
    })

    expect(screen.getByRole("region", { name: "文件窗口" })).toHaveTextContent(
      "src/pages/RefundPage.tsx",
    )
    expect(document.querySelector('[data-line="12"]')).toHaveAttribute("aria-current", "true")
    expect(document.querySelector('[data-line="13"]')).toHaveAttribute("aria-current", "true")
    expect(document.querySelector('[data-line="10"]')).not.toHaveAttribute("aria-current")
  })

  it("shows truncated and binary file windows as reduced content", () => {
    const { rerender } = render(
      <FactsPanel
        facts={factsFixture}
        hasMore={false}
        loading={false}
        error={null}
        fileWindow={{ ...fileWindowFixture, truncated: true }}
        fileWindowFact={factsFixture[0]}
        fileWindowLoading={false}
        fileWindowError={null}
        onOpenFile={vi.fn()}
        onReview={vi.fn()}
        onRetry={vi.fn()}
        onRetryFileWindow={vi.fn()}
      />,
    )
    expect(screen.getByText("文件窗口已截断")).toBeInTheDocument()

    rerender(
      <FactsPanel
        facts={factsFixture}
        hasMore={false}
        loading={false}
        error={null}
        fileWindow={{ ...fileWindowFixture, binary: true, content: "" }}
        fileWindowFact={factsFixture[0]}
        fileWindowLoading={false}
        fileWindowError={null}
        onOpenFile={vi.fn()}
        onReview={vi.fn()}
        onRetry={vi.fn()}
        onRetryFileWindow={vi.fn()}
      />,
    )
    expect(screen.getByText("二进制文件不显示内容")).toBeInTheDocument()
  })

  it("handles loading, empty and error states", () => {
    const { rerender } = render(
      <FactsPanel
        facts={[]}
        hasMore={false}
        loading
        error={null}
        fileWindow={null}
        fileWindowFact={null}
        fileWindowLoading={false}
        fileWindowError={null}
        onOpenFile={vi.fn()}
        onReview={vi.fn()}
        onRetry={vi.fn()}
        onRetryFileWindow={vi.fn()}
      />,
    )
    expect(screen.getByText("正在加载代码事实…")).toBeInTheDocument()

    rerender(
      <FactsPanel
        facts={[]}
        hasMore={false}
        loading={false}
        error={null}
        fileWindow={null}
        fileWindowFact={null}
        fileWindowLoading={false}
        fileWindowError={null}
        onOpenFile={vi.fn()}
        onReview={vi.fn()}
        onRetry={vi.fn()}
        onRetryFileWindow={vi.fn()}
      />,
    )
    expect(screen.getByText("暂无代码事实")).toBeInTheDocument()

    const onRetry = vi.fn()
    rerender(
      <FactsPanel
        facts={[]}
        hasMore={false}
        loading={false}
        error={new ApiError({ status: 503, code: "unavailable", message: "事实服务不可用" })}
        fileWindow={null}
        fileWindowFact={null}
        fileWindowLoading={false}
        fileWindowError={null}
        onOpenFile={vi.fn()}
        onReview={vi.fn()}
        onRetry={onRetry}
        onRetryFileWindow={vi.fn()}
      />,
    )
    expect(screen.getByText("代码事实加载失败")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(onRetry).toHaveBeenCalledTimes(1)
  })
})
