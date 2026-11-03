import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import type { ComponentProps } from "react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import { RequirementCleanupBar } from "./RequirementCleanupBar"

const selectedDrafts = [
  { id: "6001", title: "退款到账时效" },
  { id: "6004", title: "退款到账时效（重复）" },
]

function renderBar(overrides: Partial<ComponentProps<typeof RequirementCleanupBar>> = {}) {
  const onDelete = vi.fn()
  const onMerge = vi.fn()
  const onDedupe = vi.fn()
  const props: ComponentProps<typeof RequirementCleanupBar> = {
    draftCount: 2,
    selectedDrafts,
    onDelete,
    onMerge,
    onDedupe,
    ...overrides,
  }
  render(<RequirementCleanupBar {...props} />)
  return { onDelete, onMerge, onDedupe }
}

describe("RequirementCleanupBar", () => {
  afterEach(cleanup)

  it("renders nothing when there is no draft to clean and no feedback", () => {
    render(
      <RequirementCleanupBar
        draftCount={0}
        selectedDrafts={[]}
        onDelete={vi.fn()}
        onMerge={vi.fn()}
        onDedupe={vi.fn()}
      />,
    )
    expect(screen.queryByTestId("requirement-cleanup-bar")).not.toBeInTheDocument()
  })

  it("requires confirmation before deleting selected drafts", () => {
    const { onDelete } = renderBar()

    fireEvent.click(screen.getByTestId("cleanup-delete"))
    expect(onDelete).not.toHaveBeenCalled()
    expect(screen.getByText(/标记为 obsolete/)).toBeInTheDocument()

    fireEvent.click(screen.getByTestId("cleanup-confirm"))
    expect(onDelete).toHaveBeenCalledTimes(1)
    expect(onDelete).toHaveBeenCalledWith(["6001", "6004"])
  })

  it("cancels a pending delete without calling the API", () => {
    const { onDelete } = renderBar()

    fireEvent.click(screen.getByTestId("cleanup-delete"))
    fireEvent.click(screen.getByTestId("cleanup-cancel"))

    expect(onDelete).not.toHaveBeenCalled()
    expect(screen.queryByTestId("cleanup-confirm")).not.toBeInTheDocument()
  })

  it("merges selected drafts into the chosen target after confirmation", () => {
    const { onMerge } = renderBar()

    fireEvent.click(screen.getByTestId("cleanup-merge"))
    fireEvent.change(screen.getByTestId("cleanup-merge-target"), { target: { value: "6004" } })
    fireEvent.click(screen.getByTestId("cleanup-confirm"))

    expect(onMerge).toHaveBeenCalledWith("6004", ["6001"])
  })

  it("keeps merge disabled for a single selected draft", () => {
    renderBar({ selectedDrafts: [selectedDrafts[0]] })

    expect(screen.getByTestId("cleanup-merge")).toBeDisabled()
    expect(screen.getByText("至少选择 2 条草案才能合并")).toBeInTheDocument()
  })

  it("confirm-gates dedupe and disables it while a cleanup is running", () => {
    const { onDedupe } = renderBar()

    fireEvent.click(screen.getByTestId("cleanup-dedupe"))
    expect(onDedupe).not.toHaveBeenCalled()
    fireEvent.click(screen.getByTestId("cleanup-confirm"))
    expect(onDedupe).toHaveBeenCalledTimes(1)

    cleanup()
    renderBar({ busy: true })
    expect(screen.getByTestId("cleanup-dedupe")).toBeDisabled()
    expect(screen.getByTestId("cleanup-delete")).toBeDisabled()
  })

  it("only offers dedupe when at least two drafts exist", () => {
    renderBar({ draftCount: 1, selectedDrafts: [] })
    expect(screen.queryByTestId("cleanup-dedupe")).not.toBeInTheDocument()
  })

  it("shows the result notice and readable errors", () => {
    renderBar({ notice: "已删除 2 条草案（标记为 obsolete）" })
    expect(screen.getByTestId("cleanup-notice")).toHaveTextContent("已删除 2 条草案")

    cleanup()
    renderBar({
      error: new ApiError({
        status: 409,
        code: "state_conflict",
        message: "only a draft requirement can be deleted; roll back a clarified requirement first",
      }),
    })
    expect(screen.getByText(/roll back a clarified requirement first/)).toBeInTheDocument()
  })
})
