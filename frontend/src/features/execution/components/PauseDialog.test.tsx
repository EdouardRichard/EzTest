import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { PauseDialog } from "./PauseDialog"

afterEach(() => {
  cleanup()
})

describe("PauseDialog", () => {
  it("requires a second confirmation before an immediate pause", () => {
    const onConfirm = vi.fn()
    render(<PauseDialog open onConfirm={onConfirm} onClose={vi.fn()} />)

    fireEvent.click(screen.getByTestId("exec-pause-mode-immediate"))

    expect(screen.getByText("该用例将标记 interrupted")).toBeTruthy()
    expect(onConfirm).not.toHaveBeenCalled()

    fireEvent.click(screen.getByTestId("exec-pause-confirm"))
    expect(onConfirm).toHaveBeenCalledWith("immediate")
  })

  it("opens with the immediate mode preselected and still waits for confirmation", () => {
    const onConfirm = vi.fn()
    render(<PauseDialog open mode="immediate" onConfirm={onConfirm} onClose={vi.fn()} />)

    expect(screen.getByText("该用例将标记 interrupted")).toBeTruthy()
    expect(screen.getByTestId("exec-pause-mode-immediate").getAttribute("aria-pressed")).toBe(
      "true",
    )
    expect(onConfirm).not.toHaveBeenCalled()
  })

  it("defaults to a graceful pause", () => {
    const onConfirm = vi.fn()
    render(<PauseDialog open onConfirm={onConfirm} onClose={vi.fn()} />)

    expect(screen.getByTestId("exec-pause-mode-graceful").getAttribute("aria-pressed")).toBe("true")
    expect(screen.queryByText("该用例将标记 interrupted")).toBeNull()

    fireEvent.click(screen.getByTestId("exec-pause-confirm"))
    expect(onConfirm).toHaveBeenCalledWith("graceful")
  })

  it("closes on Escape and renders nothing when closed", () => {
    const onClose = vi.fn()
    const { rerender } = render(<PauseDialog open onClose={onClose} onConfirm={vi.fn()} />)

    fireEvent.keyDown(screen.getByTestId("exec-pause-dialog"), { key: "Escape" })
    expect(onClose).toHaveBeenCalledTimes(1)

    rerender(<PauseDialog open={false} onClose={onClose} onConfirm={vi.fn()} />)
    expect(screen.queryByTestId("exec-pause-dialog")).toBeNull()
  })

  it("disables confirmation while the pause request is in flight", () => {
    const onConfirm = vi.fn()
    render(<PauseDialog open busy onConfirm={onConfirm} onClose={vi.fn()} />)

    const confirm = screen.getByTestId("exec-pause-confirm") as HTMLButtonElement
    expect(confirm.disabled).toBe(true)
    fireEvent.click(confirm)
    expect(onConfirm).not.toHaveBeenCalled()
  })

  it("shows the action error without losing the dialog", () => {
    render(
      <PauseDialog
        open
        error="批次已结束，无法执行该操作。"
        onConfirm={vi.fn()}
        onClose={vi.fn()}
      />,
    )

    expect(screen.getByRole("alert").textContent).toContain("批次已结束")
  })
})
