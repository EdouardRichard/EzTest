import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  listProfiles: vi.fn(),
  listSessions: vi.fn(),
  createSession: vi.fn(),
  getSession: vi.fn(),
  deleteSession: vi.fn(),
}))

vi.mock("./agent-api", () => ({
  listProfiles: mocks.listProfiles,
  listSessions: mocks.listSessions,
  createSession: mocks.createSession,
  getSession: mocks.getSession,
  deleteSession: mocks.deleteSession,
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

vi.mock("./SessionPane", () => ({
  SessionPane: ({
    sessionId,
    session,
  }: {
    sessionId: number | null
    session: { id: number; title: string } | null
  }) => (
    <div
      data-testid="session-pane-mock"
      data-session-id={sessionId === null ? undefined : sessionId}
      data-session-title={session?.title}
    >
      session-pane-mock
    </div>
  ),
}))

vi.mock("./ConfigPane", () => ({
  ConfigPane: ({ profiles }: { profiles: unknown[]; onSaved: (profile: unknown) => void }) => (
    <div data-testid="config-pane-mock" data-profile-count={profiles.length}>
      config-pane-mock
    </div>
  ),
}))

import { ApiError } from "@/lib/api-client"

import { AgentConsole } from "./AgentConsole"
import type { AgentProfile, AgentSession } from "./agent-api"

const PROFILE: AgentProfile = {
  id: 2,
  key: "runtime-diagnostic",
  name: "运行时验证 Agent",
  description: "",
  builtin: true,
  enabled: true,
  system_prompt: "you are a runtime diagnostic agent",
  model: "",
  allowed_tools: ["file.list", "file.read"],
  max_turns: 12,
  run_seconds: 600,
  hard_timeout_seconds: 60,
  thinking: "enabled",
  reasoning_effort: "max",
  max_output_tokens: 32768,
  created_at: "2026-10-27T00:00:00Z",
  updated_at: "2026-10-27T00:00:00Z",
}

function makeSession(
  id: number,
  title: string,
  overrides: Partial<AgentSession> = {},
): AgentSession {
  return {
    id,
    profile_id: PROFILE.id,
    profile_key: PROFILE.key,
    title,
    created_by: 1,
    running: false,
    active_run: null,
    last_run: null,
    created_at: "2026-10-27T00:00:00Z",
    updated_at: "2026-10-27T00:00:00Z",
    last_activity_at: "2026-10-27T00:00:00Z",
    ...overrides,
  }
}

describe("AgentConsole", () => {
  beforeEach(() => {
    mocks.listProfiles.mockReset()
    mocks.listSessions.mockReset()
    mocks.createSession.mockReset()
    mocks.getSession.mockReset()
    mocks.deleteSession.mockReset()
    mocks.listProfiles.mockResolvedValue([PROFILE])
    mocks.listSessions.mockResolvedValue({ items: [], has_more: false, next_cursor: null })
  })

  afterEach(() => {
    cleanup()
    window.history.replaceState(null, "", "/")
  })

  it("shows the admin-only empty state on 403 and no console controls", async () => {
    mocks.listProfiles.mockRejectedValue(
      new ApiError({ status: 403, code: "forbidden", message: "forbidden" }),
    )

    render(<AgentConsole />)

    expect(await screen.findByText("仅管理员可用")).toBeTruthy()
    expect(screen.queryByRole("button", { name: "新建会话" })).toBeNull()
    expect(screen.queryByRole("tab", { name: "会话" })).toBeNull()
  })

  it("loads profiles and sessions in parallel and renders both tabs", async () => {
    mocks.listSessions.mockResolvedValue({
      items: [makeSession(1, "会话一")],
      has_more: false,
      next_cursor: null,
    })

    render(<AgentConsole />)

    expect(await screen.findByText("会话一")).toBeTruthy()
    expect(screen.getByRole("heading", { name: "Agent 运行时控制台" })).toBeTruthy()
    expect(screen.getByRole("tab", { name: "会话" })).toBeTruthy()
    expect(screen.getByRole("tab", { name: "配置" })).toBeTruthy()
    const emptyPane = screen.getByTestId("session-pane-mock")
    expect(emptyPane.getAttribute("data-session-id")).toBeNull()
    expect(emptyPane.getAttribute("data-session-title")).toBeNull()
    await waitFor(() => expect(mocks.listProfiles).toHaveBeenCalledTimes(1))
    expect(mocks.listSessions).toHaveBeenCalledWith({ limit: 20 })

    // Radix Tabs activates on mousedown (focus follows in a real browser).
    fireEvent.mouseDown(screen.getByRole("tab", { name: "配置" }))
    const configPane = await screen.findByTestId("config-pane-mock")
    expect(configPane.getAttribute("data-profile-count")).toBe("1")
  })

  it("creates and selects a session and syncs ?session= into the URL", async () => {
    mocks.listSessions.mockResolvedValue({
      items: [makeSession(1, "会话一")],
      has_more: false,
      next_cursor: null,
    })
    mocks.createSession.mockResolvedValue(makeSession(2, "新会话"))

    render(<AgentConsole />)
    await screen.findByText("会话一")

    fireEvent.click(screen.getByRole("button", { name: "新建会话" }))

    await waitFor(() => expect(mocks.createSession).toHaveBeenCalledTimes(1))
    const created = await screen.findByRole("button", { name: "新会话" })
    expect(created.getAttribute("aria-current")).toBe("true")
    await waitFor(() => expect(window.location.search).toBe("?session=2"))
    let pane = screen.getByTestId("session-pane-mock")
    expect(pane.getAttribute("data-session-id")).toBe("2")
    expect(pane.getAttribute("data-session-title")).toBe("新会话")

    fireEvent.click(screen.getByRole("button", { name: "会话一" }))
    await waitFor(() => expect(window.location.search).toBe("?session=1"))
    expect(screen.getByRole("button", { name: "会话一" }).getAttribute("aria-current")).toBe("true")
    pane = screen.getByTestId("session-pane-mock")
    expect(pane.getAttribute("data-session-id")).toBe("1")
    expect(pane.getAttribute("data-session-title")).toBe("会话一")
  })

  it("selects the session named by the ?session= deep link", async () => {
    window.history.replaceState(null, "", "?session=2")
    mocks.listSessions.mockResolvedValue({
      items: [makeSession(1, "会话一"), makeSession(2, "会话二")],
      has_more: false,
      next_cursor: null,
    })

    render(<AgentConsole />)

    const linked = await screen.findByRole("button", { name: "会话二" })
    await waitFor(() => expect(linked.getAttribute("aria-current")).toBe("true"))
    const pane = screen.getByTestId("session-pane-mock")
    expect(pane.getAttribute("data-session-id")).toBe("2")
    expect(pane.getAttribute("data-session-title")).toBe("会话二")
  })

  it("loads a deep-linked session that is not on the first page", async () => {
    window.history.replaceState(null, "", "?session=7")
    mocks.listSessions.mockResolvedValue({
      items: [makeSession(1, "会话一")],
      has_more: true,
      next_cursor: "next-page",
    })
    mocks.getSession.mockResolvedValue(makeSession(7, "深层会话"))

    render(<AgentConsole />)

    await waitFor(() => expect(mocks.getSession).toHaveBeenCalledWith(7))
    await waitFor(() => {
      const pane = screen.getByTestId("session-pane-mock")
      expect(pane.getAttribute("data-session-id")).toBe("7")
      expect(pane.getAttribute("data-session-title")).toBe("深层会话")
    })
  })

  it("removes a deleted session and clears its deep link", async () => {
    window.history.replaceState(null, "", "?session=1")
    mocks.listSessions.mockResolvedValue({
      items: [makeSession(1, "会话一"), makeSession(2, "会话二")],
      has_more: false,
      next_cursor: null,
    })
    mocks.deleteSession.mockResolvedValue(undefined)

    render(<AgentConsole />)
    await screen.findByRole("button", { name: "会话一" })

    fireEvent.click(screen.getAllByRole("button", { name: "删除会话" })[0])

    await waitFor(() => expect(mocks.deleteSession).toHaveBeenCalledWith(1))
    await waitFor(() => expect(screen.queryByRole("button", { name: "会话一" })).toBeNull())
    expect(screen.getByRole("button", { name: "会话二" })).toBeTruthy()
    await waitFor(() => expect(window.location.search).toBe(""))
  })
})
