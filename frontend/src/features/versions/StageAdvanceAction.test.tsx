import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const api = vi.hoisted(() => ({ advanceStage: vi.fn() }))
vi.mock(import("./api"), async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>()
  return { ...actual, advanceStage: api.advanceStage }
})

import type { StageView } from "@/features/design/types"

import { StageAdvanceAction } from "./StageAdvanceAction"

const advancedView: StageView = {
  version_id: "2001",
  stage: "designing",
  stage_entered_at: "2026-11-01T09:00:00Z",
  blocker: null,
}

describe("StageAdvanceAction", () => {
  beforeEach(() => {
    api.advanceStage.mockReset()
  })

  afterEach(cleanup)

  it("advances the stage through the dedicated advance route", async () => {
    api.advanceStage.mockResolvedValue(advancedView)
    const onAdvanced = vi.fn()

    render(
      <StageAdvanceAction versionId="2001" stage="requirement_review" onAdvanced={onAdvanced} />,
    )

    fireEvent.click(screen.getByTestId("stage-advance"))

    await waitFor(() => expect(api.advanceStage).toHaveBeenCalledWith("2001"))
    await waitFor(() => expect(onAdvanced).toHaveBeenCalledWith(advancedView))
    expect(screen.getByTestId("stage-advance-notice")).toHaveTextContent("已推进到")
  })

  it("disables advancing on a completed version", () => {
    render(<StageAdvanceAction versionId="2001" stage="completed" />)
    expect(screen.getByTestId("stage-advance")).toBeDisabled()
  })

  it("disables advancing while the stage projection is unknown", () => {
    render(<StageAdvanceAction versionId="2001" stage={null} />)
    expect(screen.getByTestId("stage-advance")).toBeDisabled()
  })

  it("shows the typed gate guidance when the advance is rejected", async () => {
    api.advanceStage.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "stage_gate_failed",
        message: "需求门未通过",
        requestId: "req_advance",
        details: {
          gate: "requirements_confirmed",
          reason: "存在未确认需求",
          action: "确认全部需求",
          next: "需求与澄清",
        },
      }),
    )

    render(<StageAdvanceAction versionId="2001" stage="requirement_review" />)
    fireEvent.click(screen.getByTestId("stage-advance"))

    expect(await screen.findByText("需求门未通过")).toBeInTheDocument()
    expect(screen.getByTestId("stage-advance-gate")).toHaveTextContent("确认全部需求")
  })
})
