import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  deleteSession: vi.fn(),
}))

vi.mock("./agent-api", () => ({
  deleteSession: mocks.deleteSession,
}))

import type { AgentSession } from "./agent-api"
import { SessionList } from "./SessionList"

function makeSession(
  id: number,
  title: string,
  overrides: Partial<AgentSession> = {},
): AgentSession {
  return {
    id,
    profile_id: 2,
    profile_key: "runtime-diagnostic",
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

function renderList(overrides: Partial<Parameters<typeof SessionList>[0]> = {}) {
  const props: Parameters<typeof SessionList>[0] = {
    sessions: [],
    selectedId: null,
    loading: false,
    hasMore: false,
    onSelect: vi.fn(),
    onCreate: vi.fn(),
    onLoadMore: vi.fn(),
    ...overrides,
  }
  const view = render(<SessionList {...props} />)
  return { ...view, props }
}

describe("SessionList", () => {
  beforeEach(() => {
    mocks.deleteSession.mockReset()
    mocks.deleteSession.mockResolvedValue(undefined)
  })

  afterEach(() => {
    cleanup()
  })

  it("renders session titles, highlights the selection and marks running sessions", () => {
    const { props } = renderList({
      sessions: [makeSession(1, "运行中的会话", { running: true }), makeSession(2, "已完成的会话")],
      selectedId: 2,
    })

    expect(screen.getByText("运行中")).toBeTruthy()
    expect(screen.getByRole("button", { name: "已完成的会话" }).getAttribute("aria-current")).toBe(
      "true",
    )
    expect(
      screen.getByRole("button", { name: "运行中的会话" }).getAttribute("aria-current"),
    ).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "运行中的会话" }))
    expect(props.onSelect).toHaveBeenCalledWith(1)
  })

  it("creates sessions from the new button and shows the empty state", () => {
    const { props } = renderList()

    expect(screen.getByText("暂无会话")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "新建会话" }))
    expect(props.onCreate).toHaveBeenCalledTimes(1)
  })

  it("loads older pages and deletes a session after the API confirms", async () => {
    const onDeleted = vi.fn()
    const { props } = renderList({
      sessions: [makeSession(1, "会话一")],
      selectedId: 1,
      hasMore: true,
      onDeleted,
    })

    fireEvent.click(screen.getByRole("button", { name: "加载更早" }))
    expect(props.onLoadMore).toHaveBeenCalledTimes(1)

    fireEvent.click(screen.getByRole("button", { name: "删除会话" }))
    await waitFor(() => expect(mocks.deleteSession).toHaveBeenCalledWith(1))
    await waitFor(() => expect(onDeleted).toHaveBeenCalledWith(1))
  })

  it("keeps the session and reports the error when deletion fails", async () => {
    mocks.deleteSession.mockRejectedValue(new Error("boom"))
    const onDeleted = vi.fn()
    renderList({
      sessions: [makeSession(1, "会话一")],
      selectedId: 1,
      onDeleted,
    })

    fireEvent.click(screen.getByRole("button", { name: "删除会话" }))

    expect(await screen.findByRole("alert")).toBeTruthy()
    expect(onDeleted).not.toHaveBeenCalled()
    expect(screen.getByRole("button", { name: "会话一" })).toBeTruthy()
  })
})
