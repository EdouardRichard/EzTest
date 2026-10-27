import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { SSEStatus } from "@/lib/sse-client"

import type { AgentEvent, AgentSession } from "./agent-api"
import type { AgentSessionState } from "./useAgentSession"

const mocks = vi.hoisted(() => ({
  useAgentSession: vi.fn(),
  sendMessage: vi.fn(),
  stopSession: vi.fn(),
}))

vi.mock("./useAgentSession", () => ({ useAgentSession: mocks.useAgentSession }))
vi.mock("./agent-api", () => ({
  sendMessage: mocks.sendMessage,
  stopSession: mocks.stopSession,
}))
vi.mock("@/lib/api-client", () => {
  class ApiError extends Error {
    readonly status: number
    constructor(init: { status: number; message: string }) {
      super(init.message)
      this.name = "ApiError"
      this.status = init.status
    }
  }
  return { ApiError }
})

import { ApiError } from "@/lib/api-client"

import { SessionPane } from "./SessionPane"

const SESSION: AgentSession = {
  id: 1,
  profile_id: 2,
  profile_key: "runtime-diagnostic",
  title: "检查运行时健康",
  created_by: 1,
  running: false,
  active_run: null,
  last_run: null,
  created_at: "2026-10-27T00:00:00Z",
  updated_at: "2026-10-27T00:00:00Z",
  last_activity_at: "2026-10-27T00:00:00Z",
}

function makeSession(overrides: Partial<AgentSession> = {}): AgentSession {
  return { ...SESSION, ...overrides }
}

function makeActivity(seq: number, detail: Record<string, unknown>): AgentEvent {
  return {
    seq,
    channel: "agent:1",
    type: "agent.activity",
    ts: "2026-10-27T00:00:00Z",
    summary: `activity ${seq}`,
    agent_role: "runtime-diagnostic",
    session_id: "1",
    detail,
  }
}

function hookState(overrides: Partial<AgentSessionState> = {}): AgentSessionState {
  return {
    activities: [],
    status: "live" as SSEStatus,
    loading: false,
    error: "",
    running: false,
    hasMore: false,
    loadEarlier: vi.fn(async () => {}),
    refresh: vi.fn(async () => {}),
    getDetail: vi.fn(),
    ...overrides,
  }
}

function fillAndSend(message: string) {
  fireEvent.change(screen.getByRole("textbox", { name: "发送消息" }), {
    target: { value: message },
  })
  fireEvent.click(screen.getByRole("button", { name: "发送" }))
}

describe("SessionPane", () => {
  beforeEach(() => {
    mocks.useAgentSession.mockReset()
    mocks.sendMessage.mockReset()
    mocks.stopSession.mockReset()
    mocks.sendMessage.mockResolvedValue({ run_id: 9, status: "accepted" })
    mocks.stopSession.mockResolvedValue({ status: "stopping" })
  })

  afterEach(() => {
    cleanup()
  })

  it("prompts to pick a session when none is selected", () => {
    mocks.useAgentSession.mockReturnValue(hookState({ status: "closed" }))

    render(<SessionPane sessionId={null} session={null} />)

    expect(screen.getByText("选择或新建会话")).toBeTruthy()
    expect(screen.queryByRole("textbox", { name: "发送消息" })).toBeNull()
    expect(mocks.useAgentSession).toHaveBeenCalledWith(null, null)
  })

  it("renders the title, running badge and reconnecting banner without an empty state", () => {
    mocks.useAgentSession.mockReturnValue(hookState({ running: true, status: "reconnecting" }))
    const session = makeSession()

    render(<SessionPane sessionId={1} session={session} />)

    expect(screen.getByRole("heading", { name: "检查运行时健康" })).toBeTruthy()
    expect(screen.getByText("运行中")).toBeTruthy()
    expect(screen.getByText("重连中")).toBeTruthy()
    expect(screen.getByRole("button", { name: "停止" })).toBeTruthy()
    expect(screen.queryByText("暂无消息")).toBeNull()
    expect(mocks.useAgentSession).toHaveBeenCalledWith(1, session)
  })

  it("sends a message and refreshes the transcript", async () => {
    const hook = hookState()
    mocks.useAgentSession.mockReturnValue(hook)

    render(<SessionPane sessionId={1} session={makeSession()} />)
    fillAndSend("检查运行时健康")

    await waitFor(() => expect(mocks.sendMessage).toHaveBeenCalledWith(1, "检查运行时健康"))
    await waitFor(() => expect(hook.refresh).toHaveBeenCalledTimes(1))
  })

  it("stops the active run and refreshes the transcript", async () => {
    const hook = hookState({ running: true })
    mocks.useAgentSession.mockReturnValue(hook)

    render(<SessionPane sessionId={1} session={makeSession()} />)
    fireEvent.click(screen.getByRole("button", { name: "停止" }))

    await waitFor(() => expect(mocks.stopSession).toHaveBeenCalledWith(1))
    await waitFor(() => expect(hook.refresh).toHaveBeenCalledTimes(1))
  })

  it("maps every terminal run status to its label", () => {
    const cases: [string, string][] = [
      ["done", "已完成"],
      ["aborted", "已中止"],
      ["failed", "失败"],
      ["interrupted", "已中断"],
    ]

    for (const [status, label] of cases) {
      mocks.useAgentSession.mockReturnValue(
        hookState({
          activities: [makeActivity(1, { kind: "run_finished", run_id: 9, status })],
        }),
      )
      const view = render(<SessionPane sessionId={1} session={makeSession()} />)
      expect(screen.getByText(label)).toBeTruthy()
      view.unmount()
    }
  })

  it("loads earlier activities through the hook", () => {
    const hook = hookState({
      hasMore: true,
      activities: [
        makeActivity(1, {
          kind: "text",
          run_id: 9,
          message_id: 42,
          part: 0,
          final: true,
          text: "旧消息",
        }),
      ],
    })
    mocks.useAgentSession.mockReturnValue(hook)

    render(<SessionPane sessionId={1} session={makeSession()} />)
    fireEvent.click(screen.getByRole("button", { name: "加载更早" }))

    expect(hook.loadEarlier).toHaveBeenCalledTimes(1)
  })

  it("surfaces hook errors and the 409 conflict message", async () => {
    mocks.useAgentSession.mockReturnValue(hookState({ error: "仅管理员可用" }))
    const first = render(<SessionPane sessionId={1} session={makeSession()} />)
    expect(screen.getByRole("alert").textContent).toContain("仅管理员可用")
    first.unmount()

    const hook = hookState()
    mocks.useAgentSession.mockReturnValue(hook)
    mocks.sendMessage.mockRejectedValue(
      new ApiError({ status: 409, code: "conflict", message: "run_active" }),
    )
    render(<SessionPane sessionId={1} session={makeSession()} />)
    fillAndSend("再发一条")

    await waitFor(() => expect(mocks.sendMessage).toHaveBeenCalledTimes(1))
    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("上一条仍在执行")
    expect(alert.textContent).toContain("run_active")
    expect(hook.refresh).not.toHaveBeenCalled()
  })

  it("shows the 503 API message when the run cannot start", async () => {
    mocks.useAgentSession.mockReturnValue(hookState())
    mocks.sendMessage.mockRejectedValue(
      new ApiError({ status: 503, code: "unavailable", message: "llm_not_configured" }),
    )

    render(<SessionPane sessionId={1} session={makeSession()} />)
    fillAndSend("检查运行时健康")

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("llm_not_configured")
  })
})
