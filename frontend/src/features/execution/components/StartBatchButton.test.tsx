import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { StartBatchButton } from "./StartBatchButton"

afterEach(() => {
  cleanup()
})

describe("StartBatchButton", () => {
  it("renders for an executing version without an active batch", () => {
    const onStart = vi.fn()
    render(<StartBatchButton versionStatus="executing" hasActiveBatch={false} onStart={onStart} />)

    const button = screen.getByTestId("exec-start-btn")
    expect(button.textContent).toContain("开始执行")

    fireEvent.click(button)
    expect(onStart).toHaveBeenCalledTimes(1)
  })

  it("renders while a version is continuing", () => {
    render(<StartBatchButton versionStatus="continuing" hasActiveBatch={false} onStart={vi.fn()} />)

    expect(screen.getByTestId("exec-start-btn")).toBeTruthy()
  })

  it("is hidden while a batch is active", () => {
    render(<StartBatchButton versionStatus="executing" hasActiveBatch onStart={vi.fn()} />)

    expect(screen.queryByTestId("exec-start-btn")).toBeNull()
  })

  it("is hidden outside the executable statuses", () => {
    render(<StartBatchButton versionStatus="clarifying" hasActiveBatch={false} onStart={vi.fn()} />)

    expect(screen.queryByTestId("exec-start-btn")).toBeNull()
  })

  it("disables itself while starting", () => {
    const onStart = vi.fn()
    render(
      <StartBatchButton versionStatus="executing" hasActiveBatch={false} busy onStart={onStart} />,
    )

    const button = screen.getByTestId("exec-start-btn") as HTMLButtonElement
    expect(button.disabled).toBe(true)
    fireEvent.click(button)
    expect(onStart).not.toHaveBeenCalled()
  })
})
