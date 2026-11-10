import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { StageView } from "@/features/design/types"

import { StageBar } from "./StageBar"

const fetchMock = vi.fn<typeof fetch>()

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function apiError(status: number, code: string, message: string, details: unknown): Response {
  return jsonResponse({ error: { code, message, details } }, status)
}

function stageView(overrides: Partial<StageView> = {}): StageView {
  return {
    version_id: "2001",
    stage: "requirement_review",
    stage_entered_at: "2026-11-01T07:00:00Z",
    blocker: null,
    ...overrides,
  }
}

describe("StageBar", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it("highlights the current stage and marks passed/future stages", () => {
    render(
      <StageBar
        versionId="2001"
        stage="clarifying"
        stageEnteredAt="2026-11-01T06:00:00Z"
        from="created"
        actor="host"
      />,
    )

    const current = screen.getByTestId("stage-item-clarifying")
    expect(current).toHaveAttribute("aria-current", "step")
    expect(current).toHaveAttribute("data-state", "current")
    expect(current).toHaveTextContent("澄清中")
    expect(screen.getByTestId("stage-item-created")).toHaveAttribute("data-state", "past")

    const future = screen.getByTestId("stage-item-requirement_review")
    expect(future).toHaveAttribute("data-state", "future")
    expect(future).toHaveAttribute("aria-disabled", "true")

    expect(screen.getByTestId("stage-entered-at")).toHaveTextContent("2026-11-01")
    const transition = screen.getByTestId("stage-transition")
    expect(transition).toHaveTextContent("created")
    expect(transition).toHaveTextContent("host")
  })

  it("keeps only the stage pills and write actions in compact mode", () => {
    render(
      <StageBar versionId="2001" stage="executing" stageEnteredAt="2026-11-01T09:00:00Z" compact />,
    )

    const bar = screen.getByTestId("stage-bar")
    expect(bar).toHaveAttribute("data-compact", "true")
    // The header row (title + entered-at) is gone.
    expect(screen.queryByText("版本阶段")).not.toBeInTheDocument()
    expect(screen.queryByTestId("stage-entered-at")).not.toBeInTheDocument()
    // Pills and action buttons survive.
    expect(screen.getByTestId("stage-item-executing")).toHaveAttribute("aria-current", "step")
    expect(screen.getByTestId("stage-item-created")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "暂停版本" })).toBeInTheDocument()
  })

  it("keeps the header row and data-compact=false without the flag", () => {
    render(<StageBar versionId="2001" stage="executing" stageEnteredAt="2026-11-01T09:00:00Z" />)

    expect(screen.getByTestId("stage-bar")).toHaveAttribute("data-compact", "false")
    expect(screen.getByText("版本阶段")).toBeInTheDocument()
    expect(screen.getByTestId("stage-entered-at")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "暂停版本" })).toBeInTheDocument()
  })

  it("offers no manual stage advance control in any workflow stage", () => {
    const { rerender } = render(
      <StageBar versionId="2001" stage="clarifying" stageEnteredAt="2026-11-01T06:00:00Z" />,
    )

    expect(screen.queryByRole("button", { name: /推进/ })).not.toBeInTheDocument()

    rerender(
      <StageBar
        versionId="2001"
        stage="requirement_review"
        stageEnteredAt="2026-11-01T07:00:00Z"
      />,
    )
    expect(screen.queryByRole("button", { name: /推进/ })).not.toBeInTheDocument()
  })

  it("shows typed 409 gate details inline and keeps the current stage", async () => {
    fetchMock.mockResolvedValueOnce(
      apiError(409, "stage_gate_failed", "阶段门禁未通过", {
        gate: "requirement",
        reason: "unconfirmed_requirements",
        action: "确认全部需求",
        next: "requirement_review",
      }),
    )

    render(
      <StageBar
        versionId="2001"
        stage="requirement_review"
        stageEnteredAt="2026-11-01T07:00:00Z"
      />,
    )

    fireEvent.click(screen.getByRole("button", { name: "暂停版本" }))

    const gateError = await screen.findByTestId("stage-gate-error")
    expect(gateError).toHaveTextContent("阶段门禁未通过")
    expect(gateError).toHaveTextContent("unconfirmed_requirements")
    expect(gateError).toHaveTextContent("确认全部需求")
    expect(screen.getByTestId("stage-item-requirement_review")).toHaveAttribute(
      "aria-current",
      "step",
    )
  })

  it("shows a blocked chip with the structured blocker reason", () => {
    render(
      <StageBar
        versionId="2001"
        stage="blocked"
        stageEnteredAt="2026-11-01T08:00:00Z"
        blocker={{
          kind: "executor",
          reason: "executor offline",
          action: "start executor",
          next: "retry",
        }}
      />,
    )

    const chip = screen.getByTestId("stage-blocker-chip")
    expect(chip).toHaveTextContent("executor offline")
    expect(chip).toHaveTextContent("executor")
    expect(screen.queryByRole("button", { name: /推进到/ })).not.toBeInTheDocument()
    expect(screen.getByText(/先解除阻塞/)).toBeInTheDocument()
  })

  it("shows the paused indicator and resumes the version", async () => {
    const resumed = stageView({ stage: "executing", stage_entered_at: "2026-11-01T09:00:00Z" })
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: resumed }))
    const onStageChanged = vi.fn()

    render(
      <StageBar
        versionId="2001"
        stage="paused"
        pausedReason="执行器维护"
        onStageChanged={onStageChanged}
      />,
    )

    const paused = screen.getByTestId("stage-paused-indicator")
    expect(paused).toHaveTextContent("已暂停")
    expect(paused).toHaveTextContent("执行器维护")
    expect(screen.queryByRole("button", { name: /推进到/ })).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "恢复版本" }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    const [url, init] = fetchMock.mock.calls[0]
    expect(String(url)).toBe("http://localhost:13333/api/versions/2001/stage/resume")
    expect(init?.method).toBe("POST")
    await waitFor(() => expect(onStageChanged).toHaveBeenCalledWith(resumed))
    await waitFor(() =>
      expect(screen.getByTestId("stage-item-executing")).toHaveAttribute("aria-current", "step"),
    )
  })

  it("pauses an active version with a user reason", async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ data: stageView({ stage: "paused", blocker: "用户暂停" }) }),
    )
    const onStageChanged = vi.fn()

    render(
      <StageBar
        versionId="2001"
        stage="clarifying"
        stageEnteredAt="2026-11-01T06:00:00Z"
        onStageChanged={onStageChanged}
      />,
    )

    fireEvent.click(screen.getByRole("button", { name: "暂停版本" }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    const [url, init] = fetchMock.mock.calls[0]
    expect(String(url)).toBe("http://localhost:13333/api/versions/2001/stage/pause")
    expect(init?.method).toBe("POST")
    expect(JSON.parse(String(init?.body))).toEqual({ reason: "用户暂停" })
    await waitFor(() => expect(onStageChanged).toHaveBeenCalled())
    expect(screen.getByTestId("stage-paused-indicator")).toHaveTextContent("已暂停")
  })
})
