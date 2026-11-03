import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { useEffect, useState } from "react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { FloatingPanel } from "./FloatingPanel"

afterEach(() => {
  cleanup()
  document.body.style.overflow = ""
})

function Child({ onMount }: { onMount: () => void }) {
  const [value, setValue] = useState("")
  useEffect(() => {
    onMount()
  }, [onMount])
  return (
    <div>
      <button type="button">内容按钮</button>
      <input aria-label="草稿" value={value} onChange={(event) => setValue(event.target.value)} />
    </div>
  )
}

function PanelHarness({
  open,
  onClose = vi.fn(),
  prefix = "floating-chat",
  onChildMount = vi.fn(),
}: {
  open: boolean
  onClose?: () => void
  prefix?: string
  onChildMount?: () => void
}) {
  return (
    <FloatingPanel open={open} onClose={onClose} title="需求澄清对话" testIdPrefix={prefix}>
      <Child onMount={onChildMount} />
    </FloatingPanel>
  )
}

function Counter() {
  const [count, setCount] = useState(0)
  return (
    <button type="button" onClick={() => setCount((value) => value + 1)}>
      计数 {count}
    </button>
  )
}

function FocusHarness() {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button type="button" data-testid="open-focus-panel" onClick={() => setOpen(true)}>
        打开浮层
      </button>
      <FloatingPanel
        open={open}
        onClose={() => setOpen(false)}
        title="焦点浮层"
        testIdPrefix="focus-panel"
      >
        <button type="button">第一个内容按钮</button>
        <button type="button">最后一个内容按钮</button>
      </FloatingPanel>
    </>
  )
}

function HiddenFocusHarness() {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button type="button" data-testid="open-hidden-focus-panel" onClick={() => setOpen(true)}>
        打开隐藏焦点浮层
      </button>
      <FloatingPanel
        open={open}
        onClose={() => setOpen(false)}
        title="隐藏焦点浮层"
        testIdPrefix="hidden-focus-panel"
      >
        <button type="button">第一个可见按钮</button>
        <button type="button">最后一个可见按钮</button>
        <button type="button" hidden data-testid="hidden-attr-button">
          隐藏属性按钮
        </button>
        <button type="button" style={{ display: "none" }} data-testid="display-none-button">
          不显示按钮
        </button>
      </FloatingPanel>
    </>
  )
}

function InternalTriggerHarness() {
  const [open, setOpen] = useState(false)
  return (
    <FloatingPanel
      open={open}
      onClose={() => setOpen(false)}
      title="内部触发浮层"
      testIdPrefix="internal-trigger-panel"
    >
      <button type="button" data-testid="internal-trigger" onClick={() => setOpen(true)}>
        展开内部浮层
      </button>
      <button type="button">内部内容按钮</button>
    </FloatingPanel>
  )
}

describe("FloatingPanel", () => {
  it("keeps children inline while closed and portals a labelled 90vw × 90vh dialog while open", () => {
    const { container, rerender } = render(<PanelHarness open={false} />)

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
    expect(container).toContainElement(screen.getByLabelText("草稿"))

    rerender(<PanelHarness open />)

    const dialog = screen.getByRole("dialog", { name: "需求澄清对话" })
    expect(dialog).toHaveAttribute("aria-modal", "true")
    // 90vw × 90vh with page margins; below the small breakpoint it becomes a
    // full-screen sheet.
    expect(dialog).toHaveClass("h-[90vh]", "w-[90vw]", "max-sm:h-dvh", "max-sm:w-full")
    expect(document.body).toContainElement(dialog)
    // The dialog lives in a body-level portal, not inside the caller's subtree.
    expect(container.contains(dialog)).toBe(false)
    expect(screen.getByTestId("floating-chat-close")).toBeInTheDocument()
    expect(screen.getByTestId("floating-chat-backdrop")).toBeInTheDocument()
  })

  it("moves the mounted content into the panel without remounting it and restores it on close", () => {
    const onMount = vi.fn()
    const { container, rerender } = render(<PanelHarness open={false} onChildMount={onMount} />)
    const input = screen.getByLabelText("草稿")
    fireEvent.change(input, { target: { value: "展开后仍要保留的草稿" } })
    expect(onMount).toHaveBeenCalledTimes(1)

    rerender(<PanelHarness open onChildMount={onMount} />)

    const dialog = screen.getByRole("dialog", { name: "需求澄清对话" })
    expect(dialog).toContainElement(screen.getByLabelText("草稿"))
    expect(screen.getByLabelText("草稿")).toBe(input)
    expect(input).toHaveValue("展开后仍要保留的草稿")
    expect(container.contains(input)).toBe(false)
    expect(onMount).toHaveBeenCalledTimes(1)

    rerender(<PanelHarness open={false} onChildMount={onMount} />)

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
    expect(container).toContainElement(screen.getByLabelText("草稿"))
    expect(screen.getByLabelText("草稿")).toBe(input)
    expect(input).toHaveValue("展开后仍要保留的草稿")
    expect(onMount).toHaveBeenCalledTimes(1)
  })

  it("keeps React event handlers working after the content moves into the panel", () => {
    const { rerender } = render(
      <FloatingPanel open={false} onClose={vi.fn()} title="需求澄清对话">
        <Counter />
      </FloatingPanel>,
    )
    fireEvent.click(screen.getByRole("button", { name: "计数 0" }))
    expect(screen.getByRole("button", { name: "计数 1" })).toBeInTheDocument()

    rerender(
      <FloatingPanel open onClose={vi.fn()} title="需求澄清对话">
        <Counter />
      </FloatingPanel>,
    )
    fireEvent.click(screen.getByRole("button", { name: "计数 1" }))
    expect(screen.getByRole("button", { name: "计数 2" })).toBeInTheDocument()
  })

  it("closes on Escape, the close button and the backdrop but not on content clicks", () => {
    const onClose = vi.fn()
    render(<PanelHarness open onClose={onClose} />)

    fireEvent.keyDown(window, { key: "Escape" })
    expect(onClose).toHaveBeenCalledTimes(1)

    fireEvent.click(screen.getByTestId("floating-chat-close"))
    expect(onClose).toHaveBeenCalledTimes(2)

    fireEvent.click(screen.getByTestId("floating-chat-backdrop"))
    expect(onClose).toHaveBeenCalledTimes(3)

    fireEvent.click(screen.getByRole("button", { name: "内容按钮" }))
    expect(onClose).toHaveBeenCalledTimes(3)
  })

  it("wraps the first Shift+Tab after opening from the dialog itself to the last tabbable element", () => {
    render(<FocusHarness />)
    const trigger = screen.getByTestId("open-focus-panel")
    trigger.focus()
    fireEvent.click(trigger)

    const dialog = screen.getByRole("dialog", { name: "焦点浮层" })
    expect(dialog).toHaveFocus()

    const last = screen.getByRole("button", { name: "最后一个内容按钮" })
    fireEvent.keyDown(dialog, { key: "Tab", shiftKey: true })
    expect(last).toHaveFocus()
    expect(dialog.contains(last)).toBe(true)
  })

  it("skips hidden and display:none elements when finding the last tabbable element", () => {
    render(<HiddenFocusHarness />)
    fireEvent.click(screen.getByTestId("open-hidden-focus-panel"))

    const dialog = screen.getByRole("dialog", { name: "隐藏焦点浮层" })
    const close = screen.getByTestId("hidden-focus-panel-close")
    const lastVisible = screen.getByRole("button", { name: "最后一个可见按钮" })
    const hidden = screen.getByTestId("hidden-attr-button")
    const displayNone = screen.getByTestId("display-none-button")

    // Shift+Tab from the first tabbable (close) must wrap to the last *visible*
    // element, not to a trailing hidden/display:none button that cannot take focus.
    close.focus()
    fireEvent.keyDown(dialog, { key: "Tab", shiftKey: true })
    expect(lastVisible).toHaveFocus()
    expect(hidden).not.toHaveFocus()
    expect(displayNone).not.toHaveFocus()

    // Tab from the last visible element must also wrap to the first.
    lastVisible.focus()
    fireEvent.keyDown(dialog, { key: "Tab" })
    expect(close).toHaveFocus()
  })

  it("traps Tab inside the dialog and restores focus to the trigger on close", () => {
    render(<FocusHarness />)
    const trigger = screen.getByTestId("open-focus-panel")
    trigger.focus()
    fireEvent.click(trigger)

    const dialog = screen.getByRole("dialog", { name: "焦点浮层" })
    expect(dialog).toHaveFocus()

    const close = screen.getByTestId("focus-panel-close")
    const last = screen.getByRole("button", { name: "最后一个内容按钮" })

    last.focus()
    fireEvent.keyDown(dialog, { key: "Tab" })
    expect(close).toHaveFocus()

    close.focus()
    fireEvent.keyDown(dialog, { key: "Tab", shiftKey: true })
    expect(last).toHaveFocus()

    fireEvent.keyDown(window, { key: "Escape" })
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
    expect(trigger).toHaveFocus()
  })

  it("restores focus to a trigger inside the panel content when it closes", () => {
    render(<InternalTriggerHarness />)
    const trigger = screen.getByTestId("internal-trigger")
    trigger.focus()
    fireEvent.click(trigger)

    const dialog = screen.getByRole("dialog", { name: "内部触发浮层" })
    expect(dialog).toHaveFocus()

    fireEvent.keyDown(window, { key: "Escape" })
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
    // Moving the content host into the dialog blurs a focused descendant, so
    // the restore target must be captured before the move, not after it.
    expect(trigger).toHaveFocus()
  })

  it("locks body scroll while open and restores the previous value", () => {
    document.body.style.overflow = "auto"
    const { rerender } = render(<PanelHarness open />)
    expect(document.body.style.overflow).toBe("hidden")

    rerender(<PanelHarness open={false} />)
    expect(document.body.style.overflow).toBe("auto")
  })
})
