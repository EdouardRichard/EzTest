import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const api = vi.hoisted(() => ({ rejectStage: vi.fn() }))
vi.mock("./api", () => api)

import type { StageView } from "@/features/design/types"

import { StageRejectAction } from "./StageRejectAction"

const rejectedView: StageView = {
  version_id: "2001",
  stage: "clarifying",
  stage_entered_at: "2026-11-01T09:00:00Z",
  blocker: null,
}

describe("StageRejectAction", () => {
  beforeEach(() => {
    api.rejectStage.mockReset()
  })

  afterEach(cleanup)

  it("rejects the requirement gate with the typed reason and target stage", async () => {
    api.rejectStage.mockResolvedValue(rejectedView)
    const onRejected = vi.fn()

    render(
      <StageRejectAction versionId="2001" stage="requirement_review" onRejected={onRejected} />,
    )

    fireEvent.click(screen.getByRole("button", { name: "退回澄清" }))
    const dialog = await screen.findByRole("dialog", { name: "退回上一阶段" })
    const submit = within(dialog).getByRole("button", { name: "确认退回" })
    expect(submit).toBeDisabled()

    fireEvent.change(within(dialog).getByLabelText("退回原因"), {
      target: { value: "验收标准缺失" },
    })
    fireEvent.click(submit)

    await waitFor(() =>
      expect(api.rejectStage).toHaveBeenCalledWith("2001", {
        reason: "验收标准缺失",
        target_stage: "clarifying",
      }),
    )
    await waitFor(() => expect(onRejected).toHaveBeenCalledWith(rejectedView))
  })

  it("rejects the case gate back to designing", async () => {
    api.rejectStage.mockResolvedValue({ ...rejectedView, stage: "designing" })

    render(<StageRejectAction versionId="2001" stage="case_review" />)

    fireEvent.click(screen.getByRole("button", { name: "退回设计" }))
    const dialog = await screen.findByRole("dialog", { name: "退回上一阶段" })
    fireEvent.change(within(dialog).getByLabelText("退回原因"), {
      target: { value: "覆盖不足" },
    })
    fireEvent.click(within(dialog).getByRole("button", { name: "确认退回" }))

    await waitFor(() =>
      expect(api.rejectStage).toHaveBeenCalledWith("2001", {
        reason: "覆盖不足",
        target_stage: "designing",
      }),
    )
  })

  it("renders nothing outside the two hard gates", () => {
    render(<StageRejectAction versionId="2001" stage="executing" />)
    expect(screen.queryByRole("button")).not.toBeInTheDocument()
  })

  it("keeps the dialog open and shows the API error after a failed reject", async () => {
    api.rejectStage.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "invalid_stage_for_gate",
        message: "当前阶段不是硬门",
        requestId: "req_reject",
      }),
    )

    render(<StageRejectAction versionId="2001" stage="requirement_review" />)
    fireEvent.click(screen.getByRole("button", { name: "退回澄清" }))
    const dialog = await screen.findByRole("dialog", { name: "退回上一阶段" })
    fireEvent.change(within(dialog).getByLabelText("退回原因"), { target: { value: "补充证据" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "确认退回" }))

    expect(await within(dialog).findByText("当前阶段不是硬门")).toBeInTheDocument()
  })
})
