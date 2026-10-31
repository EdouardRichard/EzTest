import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import { BulkConfirmBar } from "./BulkConfirmBar"

describe("BulkConfirmBar", () => {
  afterEach(cleanup)

  it("is hidden when nothing is selected", () => {
    const { container } = render(<BulkConfirmBar selectedCount={0} onConfirm={vi.fn()} />)
    expect(container).toBeEmptyDOMElement()
  })

  it("confirms the selected requirements", () => {
    const onConfirm = vi.fn()
    render(<BulkConfirmBar selectedCount={2} onConfirm={onConfirm} />)

    expect(screen.getByTestId("bulk-confirm-bar")).toHaveTextContent("已选 2 条")
    fireEvent.click(screen.getByRole("button", { name: "批量确认已澄清" }))
    expect(onConfirm).toHaveBeenCalledTimes(1)
  })

  it("disables the action while confirming", () => {
    render(<BulkConfirmBar selectedCount={1} confirming onConfirm={vi.fn()} />)
    expect(screen.getByRole("button", { name: "确认中…" })).toBeDisabled()
  })

  it("shows how many requirements were confirmed or skipped", () => {
    render(
      <BulkConfirmBar
        selectedCount={1}
        onConfirm={vi.fn()}
        notice="已确认 3 条，跳过 2 条已确认需求"
      />,
    )
    expect(screen.getByRole("status")).toHaveTextContent("已确认 3 条，跳过 2 条已确认需求")
  })

  it("shows batch validation details inline", () => {
    render(
      <BulkConfirmBar
        selectedCount={2}
        onConfirm={vi.fn()}
        error={
          new ApiError({
            status: 400,
            code: "invalid_argument",
            message: "存在非法草案",
            details: { requirement_id: "6002" },
          })
        }
      />,
    )
    expect(screen.getByRole("alert")).toHaveTextContent("存在非法草案")
  })
})
