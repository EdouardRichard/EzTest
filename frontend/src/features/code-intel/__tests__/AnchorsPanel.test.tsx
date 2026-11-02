import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import { AnchorsPanel } from "../components/AnchorsPanel"
import { anchorFixture, anchorsFixture } from "../test-fixtures"

afterEach(cleanup)

function renderPanel(overrides: Partial<Parameters<typeof AnchorsPanel>[0]> = {}) {
  const props = {
    anchors: anchorsFixture,
    loading: false,
    error: null,
    busyAnchorId: null,
    focusedAnchorId: null,
    onValidate: vi.fn(),
    onDelete: vi.fn(),
    onFocusAnchor: vi.fn(),
    onRetry: vi.fn(),
    ...overrides,
  }
  render(<AnchorsPanel {...props} />)
  return props
}

describe("AnchorsPanel", () => {
  it("renders anchor statuses and file:line locations", () => {
    renderPanel({ focusedAnchorId: anchorFixture.id })

    expect(screen.getByRole("region", { name: "代码锚点" })).toBeInTheDocument()
    expect(screen.getByText("有效")).toBeInTheDocument()
    expect(screen.getByText("内容已变")).toBeInTheDocument()
    expect(screen.getByText("失效")).toBeInTheDocument()
    expect(screen.getAllByText("src/pages/RefundPage.tsx:12-16")).toHaveLength(1)
    expect(screen.getByRole("listitem", { name: /8201/ })).toHaveAttribute("aria-current", "true")
  })

  it("validates, deletes and focuses an anchor", () => {
    const props = renderPanel()

    fireEvent.click(screen.getByRole("button", { name: "校验锚点 8201" }))
    expect(props.onValidate).toHaveBeenCalledWith("8201")

    fireEvent.click(screen.getByRole("button", { name: "删除锚点 8201" }))
    expect(props.onDelete).toHaveBeenCalledWith("8201")

    fireEvent.click(screen.getByRole("button", { name: "定位锚点 8201" }))
    expect(props.onFocusAnchor).toHaveBeenCalledWith("8201")
  })

  it("disables the row actions while one anchor is busy", () => {
    renderPanel({ busyAnchorId: "8201" })

    expect(screen.getByRole("button", { name: "校验锚点 8201" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "删除锚点 8201" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "校验锚点 8202" })).toBeEnabled()
  })

  it("handles loading, empty and error states", () => {
    const { rerender } = render(
      <AnchorsPanel
        anchors={[]}
        loading
        error={null}
        busyAnchorId={null}
        onValidate={vi.fn()}
        onDelete={vi.fn()}
        onRetry={vi.fn()}
      />,
    )
    expect(screen.getByText("正在加载代码锚点…")).toBeInTheDocument()

    rerender(
      <AnchorsPanel
        anchors={[]}
        loading={false}
        error={null}
        busyAnchorId={null}
        onValidate={vi.fn()}
        onDelete={vi.fn()}
        onRetry={vi.fn()}
      />,
    )
    expect(screen.getByText("暂无代码锚点")).toBeInTheDocument()

    const onRetry = vi.fn()
    rerender(
      <AnchorsPanel
        anchors={[]}
        loading={false}
        error={new ApiError({ status: 404, code: "not_found", message: "版本不存在" })}
        busyAnchorId={null}
        onValidate={vi.fn()}
        onDelete={vi.fn()}
        onRetry={onRetry}
      />,
    )
    expect(screen.getByText("代码锚点加载失败")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(onRetry).toHaveBeenCalledTimes(1)
  })
})
