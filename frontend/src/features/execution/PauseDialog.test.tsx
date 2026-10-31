import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { PauseDialog, type PauseTarget } from "./PauseDialog"

const waveTarget: PauseTarget = {
  kind: "wave",
  id: "9201",
  title: "波次 #9201",
}

function renderDialog(
  overrides: {
    target?: PauseTarget
    onSubmit?: (input: { mode: "graceful" | "immediate"; reason?: string }) => Promise<void>
    onClose?: () => void
  } = {},
) {
  const onSubmit = overrides.onSubmit ?? vi.fn().mockResolvedValue(undefined)
  const onClose = overrides.onClose ?? vi.fn()
  render(
    <PauseDialog
      open
      target={overrides.target ?? waveTarget}
      onClose={onClose}
      onSubmit={onSubmit}
    />,
  )
  return { onSubmit, onClose }
}

describe("PauseDialog", () => {
  afterEach(cleanup)

  it("defaults to graceful pause and submits the mode with a reason", async () => {
    const { onSubmit } = renderDialog()

    expect(screen.getByRole("dialog", { name: "暂停执行" })).toBeInTheDocument()
    expect(screen.getByRole("radio", { name: /优雅暂停/ })).toBeChecked()

    fireEvent.change(screen.getByLabelText("暂停原因（可选）"), {
      target: { value: "等待登录接管" },
    })
    fireEvent.click(screen.getByRole("button", { name: "暂停" }))

    await waitFor(() => {
      expect(onSubmit).toHaveBeenCalledWith({ mode: "graceful", reason: "等待登录接管" })
    })
  })

  it("requires an explicit second confirmation for immediate pause", async () => {
    const { onSubmit } = renderDialog()

    fireEvent.click(screen.getByRole("radio", { name: /立即暂停/ }))
    expect(screen.getByRole("alert")).toHaveTextContent("会中断正在执行的步骤")

    const submit = screen.getByRole("button", { name: "暂停" })
    expect(submit).toBeDisabled()

    fireEvent.click(screen.getByLabelText("我了解立即暂停会中断在飞步骤"))
    expect(submit).toBeEnabled()
    fireEvent.click(submit)

    await waitFor(() => {
      expect(onSubmit).toHaveBeenCalledWith({ mode: "immediate", reason: "" })
    })
  })

  it("disables the actions while the pause request is in flight", async () => {
    const pending = new Promise<void>(() => {})
    renderDialog({ onSubmit: () => pending })

    fireEvent.click(screen.getByRole("button", { name: "暂停" }))

    expect(screen.getByRole("button", { name: "暂停中…" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "取消" })).toBeDisabled()
  })

  it("explains that an already-paused target is an idempotent no-op", () => {
    renderDialog({ target: { ...waveTarget, alreadyPaused: true } })

    expect(screen.getByRole("status")).toHaveTextContent("重复暂停不会产生新的暂停记录")
    expect(screen.getByRole("button", { name: "暂停" })).toBeDisabled()
  })

  it("forces immediate pause for a case-run target (spec §5.7)", () => {
    renderDialog({ target: { kind: "case_run", id: "9304", title: "订单详情时间窗口" } })

    expect(screen.getByRole("radio", { name: /立即暂停/ })).toBeChecked()
    expect(screen.queryByRole("radio", { name: /优雅暂停/ })).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: "暂停" })).toBeDisabled()
  })

  it("shows the API error and keeps the dialog open when the pause fails", async () => {
    const onSubmit = vi.fn().mockRejectedValue(new Error("该波次已处于暂停状态"))
    renderDialog({ onSubmit })

    fireEvent.click(screen.getByRole("button", { name: "暂停" }))

    expect(await screen.findByText("该波次已处于暂停状态")).toBeInTheDocument()
    expect(screen.getByRole("dialog", { name: "暂停执行" })).toBeInTheDocument()
  })
})
