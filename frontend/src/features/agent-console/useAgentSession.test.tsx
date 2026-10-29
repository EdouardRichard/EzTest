import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError, resolveApiUrl } from "@/lib/api-client"
import type { ConnectSSEOptions, SSEEvent } from "@/lib/sse-client"

import type { AgentEvent, AgentMessageDetail, AgentSession } from "./agent-api"

const mocks = vi.hoisted(() => ({
  connectSSE: vi.fn(),
  stop: vi.fn(),
  fetchActivities: vi.fn(),
  getMessage: vi.fn(),
  getToken: vi.fn(),
  options: [] as ConnectSSEOptions[],
}))

vi.mock("@/lib/sse-client", () => ({ connectSSE: mocks.connectSSE }))
vi.mock("@/lib/auth-storage", () => ({ getToken: mocks.getToken }))
vi.mock("./agent-api", () => ({
  fetchActivities: mocks.fetchActivities,
  getMessage: mocks.getMessage,
}))

import { useAgentSession } from "./useAgentSession"

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

function toSseEvent(activity: AgentEvent): SSEEvent {
  return {
    id: String(activity.seq),
    event: "agent.activity",
    data: JSON.stringify(activity),
  }
}

function latestOptions(): ConnectSSEOptions {
  const options = mocks.options.at(-1)
  if (!options) throw new Error("connectSSE was never called")
  return options
}

function renderSessionHook(
  sessionId: number | null = 1,
  session: AgentSession | null = makeSession(),
) {
  return renderHook(() => useAgentSession(sessionId, session))
}

describe("useAgentSession", () => {
  beforeEach(() => {
    mocks.connectSSE.mockReset()
    mocks.stop.mockReset()
    mocks.fetchActivities.mockReset()
    mocks.getMessage.mockReset()
    mocks.getToken.mockReset()
    mocks.options.length = 0
    mocks.getToken.mockReturnValue("test-token")
    mocks.connectSSE.mockImplementation((options: ConnectSSEOptions) => {
      mocks.options.push(options)
      return mocks.stop
    })
    mocks.fetchActivities.mockResolvedValue({
      items: [],
      snapshot_cursor: 0,
      has_more: false,
    })
  })

  afterEach(() => {
    cleanup()
  })

  it("merges the snapshot with live activities and dedupes by seq", async () => {
    const userMessage = makeActivity(1, {
      kind: "user_message",
      message_id: 1,
      text: "检查运行时健康",
    })
    const runStarted = makeActivity(2, { kind: "run_started", run_id: 9 })
    mocks.fetchActivities.mockResolvedValue({
      items: [userMessage, runStarted],
      snapshot_cursor: 2,
      has_more: false,
    })

    const { result } = renderSessionHook(1, makeSession({ running: true }))

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.activities.map((event) => event.seq)).toEqual([1, 2])
    expect(result.current.running).toBe(true)
    expect(result.current.hasMore).toBe(false)
    expect(mocks.fetchActivities).toHaveBeenCalledWith(1, { limit: 50 })

    const options = latestOptions()
    expect(options.url).toBe(resolveApiUrl("/api/stream?channel=agent%3A1"))
    expect(options.token).toBe("test-token")
    expect(options.lastEventId).toBe(2)

    act(() => {
      // A duplicate seq (already in the snapshot) and an out-of-order new seq.
      options.onEvent(toSseEvent(runStarted))
      options.onEvent(
        toSseEvent(
          makeActivity(3, {
            kind: "text",
            run_id: 9,
            message_id: 42,
            part: 0,
            final: true,
            text: "运行时健康",
          }),
        ),
      )
      // Non-numeric seq and a non-agent event type must both be ignored.
      options.onEvent({
        event: "agent.activity",
        data: JSON.stringify({ type: "agent.activity", seq: "4", detail: {} }),
      })
      options.onEvent({
        event: "other",
        data: JSON.stringify({ type: "other", seq: 4, detail: {} }),
      })
    })

    await waitFor(() =>
      expect(result.current.activities.map((event) => event.seq)).toEqual([1, 2, 3]),
    )
  })

  it("treats a missing snapshot channel as empty and still opens the stream", async () => {
    mocks.fetchActivities.mockRejectedValue(
      new ApiError({ status: 404, code: "not_found", message: "channel not found" }),
    )

    const { result } = renderSessionHook()

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toBe("")
    expect(result.current.activities).toEqual([])
    expect(["connecting", "live"]).toContain(result.current.status)
    expect(mocks.connectSSE).toHaveBeenCalledTimes(1)
    expect(latestOptions().lastEventId).toBe(0)
  })

  it("keeps the stream open after a snapshot failure and retries on demand", async () => {
    mocks.fetchActivities.mockRejectedValue(
      new ApiError({ status: 500, code: "internal", message: "snapshot failed" }),
    )

    const { result } = renderSessionHook()

    await waitFor(() => expect(result.current.error).toBe("snapshot failed"))
    expect(result.current.loading).toBe(false)
    expect(result.current.status).not.toBe("closed")
    expect(mocks.connectSSE).toHaveBeenCalledTimes(1)

    mocks.fetchActivities.mockResolvedValue({
      items: [makeActivity(1, { kind: "user_message", message_id: 1, text: "重试后" })],
      snapshot_cursor: 1,
      has_more: false,
    })
    act(() => result.current.reconnect())

    await waitFor(() => expect(result.current.error).toBe(""))
    await waitFor(() => expect(mocks.connectSSE).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(result.current.activities.map((event) => event.seq)).toEqual([1]))
    expect(latestOptions().lastEventId).toBe(1)
  })

  it("reconnects a closed stream on demand without dropping activities", async () => {
    mocks.fetchActivities.mockResolvedValue({
      items: [makeActivity(1, { kind: "user_message", message_id: 1, text: "保留" })],
      snapshot_cursor: 1,
      has_more: false,
    })

    const { result } = renderSessionHook()
    await waitFor(() => expect(result.current.loading).toBe(false))
    const first = latestOptions()

    act(() => first.onStatus("closed"))
    expect(result.current.status).toBe("closed")

    act(() => result.current.reconnect())

    await waitFor(() => expect(result.current.status).toBe("connecting"))
    expect(result.current.activities.map((event) => event.seq)).toEqual([1])
    await waitFor(() => expect(mocks.connectSSE).toHaveBeenCalledTimes(2))
    expect(latestOptions()).not.toBe(first)
  })

  it("does not reconnect or clear the transcript when the parent running flag flips", async () => {
    mocks.fetchActivities.mockResolvedValue({
      items: [makeActivity(1, { kind: "user_message", message_id: 1, text: "保留" })],
      snapshot_cursor: 1,
      has_more: false,
    })

    const { result, rerender } = renderHook(
      ({ session }: { session: AgentSession }) => useAgentSession(1, session),
      { initialProps: { session: makeSession({ running: false }) } },
    )
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.activities.map((event) => event.seq)).toEqual([1])
    expect(mocks.fetchActivities).toHaveBeenCalledTimes(1)
    expect(mocks.connectSSE).toHaveBeenCalledTimes(1)

    rerender({ session: makeSession({ running: true }) })

    expect(result.current.activities.map((event) => event.seq)).toEqual([1])
    expect(mocks.fetchActivities).toHaveBeenCalledTimes(1)
    expect(mocks.connectSSE).toHaveBeenCalledTimes(1)
  })

  it("clears the error after a successful refresh or load-earlier", async () => {
    mocks.fetchActivities
      .mockResolvedValueOnce({
        items: [
          makeActivity(5, {
            kind: "text",
            run_id: 3,
            message_id: 10,
            part: 0,
            final: true,
            text: "五",
          }),
        ],
        snapshot_cursor: 5,
        has_more: true,
      })
      .mockRejectedValueOnce(new ApiError({ status: 500, code: "internal", message: "刷新失败" }))
      .mockResolvedValueOnce({
        items: [
          makeActivity(4, {
            kind: "text",
            run_id: 3,
            message_id: 10,
            part: 0,
            final: true,
            text: "四",
          }),
        ],
        snapshot_cursor: 5,
        has_more: false,
      })

    const { result } = renderSessionHook()
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.hasMore).toBe(true)

    await act(async () => {
      await result.current.refresh()
    })
    expect(result.current.error).toBe("刷新失败")

    await act(async () => {
      await result.current.loadEarlier()
    })
    expect(result.current.error).toBe("")
    expect(result.current.activities.map((event) => event.seq)).toEqual([4, 5])
  })

  it("refetches on stream reset and exposes the reconnecting status", async () => {
    mocks.fetchActivities
      .mockResolvedValueOnce({
        items: [makeActivity(1, { kind: "user_message", message_id: 1, text: "第一条" })],
        snapshot_cursor: 1,
        has_more: false,
      })
      .mockResolvedValueOnce({
        items: [
          makeActivity(1, { kind: "user_message", message_id: 1, text: "第一条" }),
          makeActivity(2, { kind: "run_started", run_id: 7 }),
        ],
        snapshot_cursor: 2,
        has_more: false,
      })

    const { result } = renderSessionHook()

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.status).toBe("connecting")

    const options = latestOptions()
    act(() => options.onStatus("reconnecting"))
    expect(result.current.status).toBe("reconnecting")

    act(() => options.onReset?.({ reason: "cursor_expired" }))
    await waitFor(() => expect(mocks.fetchActivities).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(result.current.activities.map((event) => event.seq)).toEqual([1, 2]))
    expect(result.current.running).toBe(true)
  })

  it("tracks running from lifecycle events and refreshes after run_finished", async () => {
    const { result } = renderSessionHook(1, makeSession({ running: false }))

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.running).toBe(false)
    const options = latestOptions()

    act(() => options.onEvent(toSseEvent(makeActivity(1, { kind: "run_started", run_id: 9 }))))
    expect(result.current.running).toBe(true)

    act(() =>
      options.onEvent(
        toSseEvent(
          makeActivity(2, {
            kind: "run_finished",
            run_id: 9,
            status: "done",
            terminal_reason: "completed",
          }),
        ),
      ),
    )

    await waitFor(() => expect(result.current.running).toBe(false))
    await waitFor(() => expect(mocks.fetchActivities).toHaveBeenCalledTimes(2))
  })

  it("loads earlier activities with before_seq and prepends them", async () => {
    mocks.fetchActivities
      .mockResolvedValueOnce({
        items: [
          makeActivity(5, {
            kind: "text",
            run_id: 3,
            message_id: 10,
            part: 0,
            final: true,
            text: "五",
          }),
          makeActivity(6, {
            kind: "text",
            run_id: 3,
            message_id: 10,
            part: 1,
            final: true,
            text: "六",
          }),
        ],
        snapshot_cursor: 6,
        has_more: true,
      })
      .mockResolvedValueOnce({
        items: [
          makeActivity(3, {
            kind: "text",
            run_id: 3,
            message_id: 10,
            part: 0,
            final: true,
            text: "三",
          }),
          makeActivity(4, {
            kind: "text",
            run_id: 3,
            message_id: 10,
            part: 1,
            final: true,
            text: "四",
          }),
        ],
        snapshot_cursor: 6,
        has_more: false,
      })

    const { result } = renderSessionHook()

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.hasMore).toBe(true)
    expect(result.current.activities.map((event) => event.seq)).toEqual([5, 6])

    await act(async () => {
      await result.current.loadEarlier()
    })

    expect(mocks.fetchActivities).toHaveBeenNthCalledWith(2, 1, { before_seq: 5, limit: 50 })
    expect(result.current.activities.map((event) => event.seq)).toEqual([3, 4, 5, 6])
    expect(result.current.hasMore).toBe(false)
  })

  it("caches message details by id", async () => {
    const detail: AgentMessageDetail = {
      id: 42,
      session_id: 1,
      run_id: 9,
      role: "assistant",
      content: [{ type: "text", text: "完整回答" }],
      reasoning: "完整推理",
      tool_calls: null,
      tool_call_id: null,
      tool_name: null,
      usage: null,
      created_at: "2026-10-27T00:00:00Z",
    }
    mocks.getMessage.mockResolvedValue(detail)
    const { result } = renderSessionHook()

    await waitFor(() => expect(result.current.loading).toBe(false))

    const first = await result.current.getDetail(42)
    const second = await result.current.getDetail(42)

    expect(first).toBe(detail)
    expect(second).toBe(detail)
    expect(mocks.getMessage).toHaveBeenCalledTimes(1)
    expect(mocks.getMessage).toHaveBeenCalledWith(1, 42)
  })

  it("shows the admin-only error on 403 but still opens the stream for a retry", async () => {
    mocks.fetchActivities.mockRejectedValue(
      new ApiError({ status: 403, code: "forbidden", message: "forbidden" }),
    )

    const { result } = renderSessionHook()

    await waitFor(() => expect(result.current.error).toBe("仅管理员可用"))
    expect(result.current.loading).toBe(false)
    expect(result.current.status).toBe("connecting")
    expect(mocks.connectSSE).toHaveBeenCalledTimes(1)
  })

  it("reports a missing token instead of opening the stream", async () => {
    mocks.getToken.mockReturnValue(null)

    const { result } = renderSessionHook()

    await waitFor(() => expect(result.current.error).toBe("当前会话没有访问令牌，请重新登录。"))
    expect(result.current.status).toBe("closed")
    expect(mocks.fetchActivities).not.toHaveBeenCalled()
    expect(mocks.connectSSE).not.toHaveBeenCalled()
  })

  it("stops the stream on session change and unmount and ignores stale events", async () => {
    const { result, rerender, unmount } = renderHook(
      ({ id }: { id: number }) => useAgentSession(id, makeSession()),
      { initialProps: { id: 1 } },
    )

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(mocks.stop).not.toHaveBeenCalled()
    const firstOptions = latestOptions()

    rerender({ id: 2 })

    await waitFor(() => expect(mocks.stop).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(mocks.fetchActivities).toHaveBeenCalledWith(2, { limit: 50 }))
    await waitFor(() => expect(result.current.loading).toBe(false))

    act(() =>
      firstOptions.onEvent(
        toSseEvent(
          makeActivity(99, {
            kind: "text",
            run_id: 3,
            message_id: 10,
            part: 0,
            final: true,
            text: "过期事件",
          }),
        ),
      ),
    )
    expect(result.current.activities.map((event) => event.seq)).not.toContain(99)

    unmount()
    expect(mocks.stop).toHaveBeenCalledTimes(2)
  })
})
