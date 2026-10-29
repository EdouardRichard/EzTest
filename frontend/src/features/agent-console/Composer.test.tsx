import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { Composer } from "./Composer"

function button(name: string): HTMLButtonElement {
  return screen.getByRole("button", { name }) as HTMLButtonElement
}

function renderComposer(overrides: Partial<Parameters<typeof Composer>[0]> = {}) {
  const props: Parameters<typeof Composer>[0] = {
    running: false,
    onSend: vi.fn(),
    onStop: vi.fn(),
    ...overrides,
  }
  const view = render(<Composer {...props} />)
  return { ...view, props }
}

describe("Composer", () => {
  afterEach(() => {
    cleanup()
  })

  it("sends on Enter and keeps Shift+Enter as a newline", () => {
    const { props } = renderComposer()
    const textarea = screen.getByRole("textbox", { name: "发送消息" }) as HTMLTextAreaElement

    fireEvent.change(textarea, { target: { value: "  检查运行时健康  " } })
    fireEvent.keyDown(textarea, { key: "Enter", shiftKey: true })
    expect(props.onSend).not.toHaveBeenCalled()
    expect(textarea.value).toBe("  检查运行时健康  ")

    fireEvent.keyDown(textarea, { key: "Enter" })
    expect(props.onSend).toHaveBeenCalledTimes(1)
    expect(props.onSend).toHaveBeenCalledWith("检查运行时健康")
    expect(textarea.value).toBe("")
  })

  it("never sends an empty message", () => {
    const { props } = renderComposer()
    const textarea = screen.getByRole("textbox", { name: "发送消息" })

    fireEvent.keyDown(textarea, { key: "Enter" })
    fireEvent.change(textarea, { target: { value: "   " } })
    fireEvent.keyDown(textarea, { key: "Enter" })

    expect(props.onSend).not.toHaveBeenCalled()
    expect(button("发送").disabled).toBe(true)
  })

  it("disables sending while running and offers the stop control", () => {
    const { props } = renderComposer({ running: true })

    expect(button("发送").disabled).toBe(true)
    const textarea = screen.getByRole("textbox", { name: "发送消息" }) as HTMLTextAreaElement
    expect(textarea.disabled).toBe(true)

    fireEvent.click(button("停止"))
    expect(props.onStop).toHaveBeenCalledTimes(1)
  })

  it("shows the stopping state and the API error message", () => {
    renderComposer({ running: true, stopping: true, error: "上一条仍在执行：run_active" })

    const stop = button("停止")
    expect(stop.disabled).toBe(true)
    expect(stop.textContent).toContain("停止中")
    expect(screen.getByRole("alert").textContent).toContain("上一条仍在执行：run_active")
  })

  it("disables the composer while a send is in flight", () => {
    renderComposer({ sending: true })

    expect(button("发送").disabled).toBe(true)
    expect(
      (screen.getByRole("textbox", { name: "发送消息" }) as HTMLTextAreaElement).disabled,
    ).toBe(true)
  })
})
