import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { StageView } from "@/features/design/types"

import { BlockerPanel } from "./BlockerPanel"

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
    stage: "clarifying",
    stage_entered_at: "2026-11-01T09:00:00Z",
    blocker: null,
    ...overrides,
  }
}

const blocker = {
  kind: "executor",
  reason: "executor offline",
  action: "start executor",
  next: "retry",
}

describe("BlockerPanel", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it("renders the structured blocker and unblocks the version", async () => {
    const restored = stageView()
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: restored }))
    const onUnblocked = vi.fn()

    render(
      <BlockerPanel
        versionId="2001"
        stage="blocked"
        blocker={blocker}
        blockedSince="2026-11-01T07:30:00Z"
        onUnblocked={onUnblocked}
      />,
    )

    const panel = screen.getByTestId("blocker-panel")
    expect(within(panel).getByTestId("blocker-kind")).toHaveTextContent("executor")
    expect(within(panel).getByText("executor offline")).toBeInTheDocument()
    expect(within(panel).getByText("start executor")).toBeInTheDocument()
    expect(within(panel).getByText("retry")).toBeInTheDocument()
    expect(screen.getByTestId("blocker-since")).toHaveTextContent("2026-11-01")

    fireEvent.click(within(panel).getByRole("button", { name: "解除阻塞" }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    const [url, init] = fetchMock.mock.calls[0]
    expect(String(url)).toBe("http://localhost:13333/api/versions/2001/stage/unblock")
    expect(init?.method).toBe("POST")
    await waitFor(() => expect(onUnblocked).toHaveBeenCalledWith(restored))
    await waitFor(() => expect(screen.queryByTestId("blocker-panel")).not.toBeInTheDocument())
  })

  it("shows the unblock gate action/next when the blocker is not cleared", async () => {
    fetchMock.mockResolvedValueOnce(
      apiError(409, "stage_gate_failed", "阻塞原因尚未清除", {
        gate: "blocker",
        reason: "blocker_not_cleared",
        action: "启动执行器",
        next: "retry",
      }),
    )

    render(<BlockerPanel versionId="2001" stage="blocked" blocker={blocker} />)
    fireEvent.click(screen.getByRole("button", { name: "解除阻塞" }))

    const gateError = await screen.findByTestId("blocker-gate-error")
    expect(gateError).toHaveTextContent("阻塞原因尚未清除")
    expect(gateError).toHaveTextContent("blocker_not_cleared")
    expect(gateError).toHaveTextContent("启动执行器")
    expect(screen.getByTestId("blocker-panel")).toBeInTheDocument()
  })

  it("rejects an empty blocker without calling the API", () => {
    render(<BlockerPanel versionId="2001" stage="clarifying" />)

    expect(screen.queryByTestId("blocker-panel")).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "记录阻塞" }))
    fireEvent.click(screen.getByRole("button", { name: "确认阻塞" }))

    expect(fetchMock).not.toHaveBeenCalled()
    expect(screen.getByText("请填写阻塞原因")).toBeInTheDocument()
    expect(screen.getByText("请填写修复动作")).toBeInTheDocument()
    expect(screen.getByText("请填写下一步")).toBeInTheDocument()
  })

  it("blocks the version with the structured required fields", async () => {
    const blockedView = stageView({
      stage: "blocked",
      stage_entered_at: "2026-11-01T08:00:00Z",
      blocker: JSON.stringify(blocker),
    })
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: blockedView }))
    const onBlocked = vi.fn()

    render(<BlockerPanel versionId="2001" stage="clarifying" onBlocked={onBlocked} />)

    fireEvent.click(screen.getByRole("button", { name: "记录阻塞" }))
    fireEvent.change(screen.getByLabelText("阻塞类型"), { target: { value: "executor" } })
    fireEvent.change(screen.getByLabelText("阻塞原因"), {
      target: { value: "executor offline" },
    })
    fireEvent.change(screen.getByLabelText("修复动作"), {
      target: { value: "start executor" },
    })
    fireEvent.change(screen.getByLabelText("下一步"), { target: { value: "retry" } })
    fireEvent.click(screen.getByRole("button", { name: "确认阻塞" }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    const [url, init] = fetchMock.mock.calls[0]
    expect(String(url)).toBe("http://localhost:13333/api/versions/2001/stage/block")
    expect(init?.method).toBe("POST")
    expect(JSON.parse(String(init?.body))).toEqual(blocker)
    await waitFor(() => expect(onBlocked).toHaveBeenCalledWith(blockedView))
    expect(screen.getByTestId("blocker-panel")).toHaveTextContent("executor offline")
  })

  it("does not render a blocker panel for a paused version", () => {
    render(
      <BlockerPanel
        versionId="2001"
        stage="paused"
        blocker={{ kind: "manual", reason: "用户暂停", action: "", next: "" }}
      />,
    )

    expect(screen.queryByTestId("blocker-panel")).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "记录阻塞" })).not.toBeInTheDocument()
  })
})
