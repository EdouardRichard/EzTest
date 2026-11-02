import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import { DiffDrawer } from "../components/DiffDrawer"
import { changedFilesFixture, diffFixture } from "../test-fixtures"

afterEach(cleanup)

const file = changedFilesFixture[0]

function renderDrawer(overrides: Partial<Parameters<typeof DiffDrawer>[0]> = {}) {
  const props = {
    open: true,
    file,
    diff: diffFixture(),
    loading: false,
    error: null,
    onClose: vi.fn(),
    onRetry: vi.fn(),
    ...overrides,
  }
  render(<DiffDrawer {...props} />)
  return props
}

describe("DiffDrawer", () => {
  it("renders the unified patch with add/remove lines", () => {
    renderDrawer({
      diff: diffFixture({ patch: "@@ -1,2 +1,2 @@\n-const a = 0\n+const a = 1" }),
    })

    const dialog = screen.getByRole("dialog", { name: file.path })
    expect(dialog).toHaveTextContent("新增")
    expect(screen.getByLabelText("文件 patch")).toHaveTextContent("-const a = 0")
    expect(screen.getByLabelText("文件 patch")).toHaveTextContent("+const a = 1")
  })

  it("explains binary, large and deleted files instead of rendering a patch", () => {
    const { rerender } = render(
      <DiffDrawer
        open
        file={file}
        diff={diffFixture({ binary: true, patch: null })}
        loading={false}
        error={null}
        onClose={vi.fn()}
        onRetry={vi.fn()}
      />,
    )
    expect(screen.getByText("二进制文件不显示 patch")).toBeInTheDocument()

    rerender(
      <DiffDrawer
        open
        file={file}
        diff={diffFixture({ large: true, patch: null })}
        loading={false}
        error={null}
        onClose={vi.fn()}
        onRetry={vi.fn()}
      />,
    )
    expect(screen.getByText("patch 超过 256 KiB，已截断")).toBeInTheDocument()

    rerender(
      <DiffDrawer
        open
        file={file}
        diff={diffFixture({ deleted: true, patch: null })}
        loading={false}
        error={null}
        onClose={vi.fn()}
        onRetry={vi.fn()}
      />,
    )
    expect(screen.getByText("文件已删除，无可用 patch")).toBeInTheDocument()
  })

  it("moves focus into the dialog and closes on Escape", async () => {
    const props = renderDrawer()

    const dialog = screen.getByRole("dialog", { name: file.path })
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true))

    fireEvent.keyDown(window, { key: "Escape" })
    expect(props.onClose).toHaveBeenCalledTimes(1)
  })

  it("shows a retryable error state", () => {
    const props = renderDrawer({
      diff: null,
      error: new ApiError({
        status: 503,
        code: "git_unavailable",
        message: "git 不可用",
        requestId: "req_diff",
        retryable: true,
      }),
    })

    expect(screen.getByText("文件 diff 加载失败")).toBeInTheDocument()
    expect(screen.getByText("git 不可用")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(props.onRetry).toHaveBeenCalledTimes(1)
  })
})
