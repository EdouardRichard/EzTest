import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { resolveApiUrl } from "@/lib/api-client"
import { setToken } from "@/lib/auth-storage"
import type { ConnectSSEOptions, SSEEvent } from "@/lib/sse-client"

import type { VersionEvent } from "./types"

const mocks = vi.hoisted(() => ({
  connectSSE: vi.fn(),
  stop: vi.fn(),
  options: [] as ConnectSSEOptions[],
}))

vi.mock("@/lib/sse-client", () => ({ connectSSE: mocks.connectSSE }))

import { useVersionStream } from "./useVersionStream"

const VERSION_ID = "1844674407370955161"
const OTHER_VERSION_ID = "1844674407370955162"

/** Records the order of the snapshot fetch and the stream connection. */
const callOrder: string[] = []

function makeEvent(seq: number, overrides: Partial<VersionEvent> = {}): VersionEvent {
  return {
    seq,
    channel: `version:${VERSION_ID}`,
    type: "requirement.added",
    version_id: VERSION_ID,
    batch_id: null,
    case_id: null,
    step_id: null,
    agent_role: null,
    session_id: null,
    ts: "2026-10-27T10:00:00Z",
    summary: `事件 ${seq}`,
    detail: {},
    refs: { evidence: [], case: [], requirement: [], code: [] },
    ...overrides,
  }
}

/** Builds the events snapshot envelope described in spec §7.5/§7.7. */
function eventsPage(events: VersionEvent[], cursor: number): unknown {
  return { data: events, snapshot_cursor: cursor, has_more: false }
}

function jsonResponse(payload: unknown, status = 200): Response {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "content-type": "application/json" },
  })
}

/** Stubs fetch with one response per snapshot request, last one repeating. */
function stubSnapshots(...payloads: unknown[]) {
  let index = 0
  const fetchMock = vi.fn().mockImplementation(async () => {
    callOrder.push("fetch")
    const payload = payloads[Math.min(index, payloads.length - 1)]
    index += 1
    return jsonResponse(payload)
  })
  vi.stubGlobal("fetch", fetchMock)
  return fetchMock
}

/** Stubs a failing snapshot response (404 no channel / 5xx read failure). */
function stubSnapshotError(status: number, code: string) {
  const fetchMock = vi.fn().mockImplementation(async () => {
    callOrder.push("fetch")
    return jsonResponse(
      {
        error: {
          code,
          message: "snapshot failed",
          request_id: "req_snapshot",
          retryable: status >= 500,
        },
      },
      status,
    )
  })
  vi.stubGlobal("fetch", fetchMock)
  return fetchMock
}

function latestOptions(): ConnectSSEOptions {
  const options = mocks.options.at(-1)
  if (!options) throw new Error("connectSSE was never called")
  return options
}

function toSseEvent(event: VersionEvent): SSEEvent {
  return { id: String(event.seq), event: event.type, data: JSON.stringify(event) }
}

describe("useVersionStream", () => {
  beforeEach(() => {
    mocks.connectSSE.mockReset()
    mocks.stop.mockReset()
    mocks.options.length = 0
    callOrder.length = 0
    sessionStorage.clear()
    setToken("test-token")
    mocks.connectSSE.mockImplementation((options: ConnectSSEOptions) => {
      callOrder.push("connect")
      mocks.options.push(options)
      return mocks.stop
    })
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it("loads the events snapshot before connecting the version stream", async () => {
    const fetchMock = stubSnapshots(eventsPage([makeEvent(1), makeEvent(2)], 2))

    const { result } = renderHook(() => useVersionStream(VERSION_ID))

    await waitFor(() => expect(mocks.connectSSE).toHaveBeenCalledTimes(1))

    expect(callOrder).toEqual(["fetch", "connect"])
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(String(url)).toBe(resolveApiUrl(`/api/versions/${VERSION_ID}/events?limit=50`))
    expect(init.method).toBe("GET")
    expect((init.headers as Record<string, string>).Authorization).toBe("Bearer test-token")

    await waitFor(() => expect(result.current.events.map((event) => event.seq)).toEqual([1, 2]))
    const options = latestOptions()
    expect(options.url).toBe(resolveApiUrl(`/api/versions/${VERSION_ID}/stream`))
    expect(options.token).toBe("test-token")
    expect(options.lastEventId).toBe(2)
  })

  it("dedupes live events by seq and ignores frames without a persisted event", async () => {
    stubSnapshots(eventsPage([makeEvent(1), makeEvent(2)], 2))

    const { result } = renderHook(() => useVersionStream(VERSION_ID))
    await waitFor(() => expect(mocks.connectSSE).toHaveBeenCalledTimes(1))
    const options = latestOptions()

    act(() => {
      options.onEvent(toSseEvent(makeEvent(2)))
      options.onEvent(toSseEvent(makeEvent(3)))
      options.onEvent({ event: "requirement.added", data: "not-json" })
      options.onEvent({
        event: "requirement.added",
        data: JSON.stringify({ type: "requirement.added", seq: "4" }),
      })
    })

    await waitFor(() => expect(result.current.events.map((event) => event.seq)).toEqual([1, 2, 3]))
  })

  it("refetches the snapshot when the server resets the stream", async () => {
    const fetchMock = stubSnapshots(
      eventsPage([makeEvent(1)], 1),
      eventsPage([makeEvent(1), makeEvent(2)], 2),
    )

    const { result } = renderHook(() => useVersionStream(VERSION_ID))
    await waitFor(() => expect(mocks.connectSSE).toHaveBeenCalledTimes(1))
    const options = latestOptions()

    act(() => options.onReset?.({ channel: `version:${VERSION_ID}`, reason: "cursor_too_old" }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(result.current.events.map((event) => event.seq)).toEqual([1, 2]))
  })

  it("exposes live/reconnecting status and keeps events while reconnecting", async () => {
    stubSnapshots(eventsPage([makeEvent(1)], 1))

    const { result } = renderHook(() => useVersionStream(VERSION_ID))
    await waitFor(() => expect(mocks.connectSSE).toHaveBeenCalledTimes(1))
    expect(result.current.status).toBe("connecting")
    const options = latestOptions()

    act(() => options.onStatus("live"))
    expect(result.current.status).toBe("live")

    act(() => options.onStatus("reconnecting"))
    expect(result.current.status).toBe("reconnecting")
    expect(result.current.events.map((event) => event.seq)).toEqual([1])

    act(() => options.onStatus("closed"))
    expect(result.current.status).toBe("closed")
  })

  it("reconnects from a fresh snapshot and reports the connecting status", async () => {
    const fetchMock = stubSnapshots(
      eventsPage([makeEvent(1)], 1),
      eventsPage([makeEvent(1), makeEvent(2)], 2),
    )

    const { result } = renderHook(() => useVersionStream(VERSION_ID))
    await waitFor(() => expect(mocks.connectSSE).toHaveBeenCalledTimes(1))
    const first = latestOptions()
    act(() => first.onStatus("closed"))
    expect(result.current.status).toBe("closed")

    act(() => result.current.reconnect())

    expect(result.current.status).toBe("connecting")
    await waitFor(() => expect(mocks.stop).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(mocks.connectSSE).toHaveBeenCalledTimes(2))

    const second = latestOptions()
    expect(second).not.toBe(first)
    expect(second.lastEventId).toBe(2)
    await waitFor(() => expect(result.current.events.map((event) => event.seq)).toEqual([1, 2]))
  })

  it("stops the stream and resets events when the version changes", async () => {
    stubSnapshots(eventsPage([makeEvent(1)], 1), eventsPage([makeEvent(5)], 5))

    const { result, rerender } = renderHook(({ id }: { id: string }) => useVersionStream(id), {
      initialProps: { id: VERSION_ID },
    })
    await waitFor(() => expect(mocks.connectSSE).toHaveBeenCalledTimes(1))
    const first = latestOptions()

    rerender({ id: OTHER_VERSION_ID })

    await waitFor(() => expect(mocks.stop).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(mocks.connectSSE).toHaveBeenCalledTimes(2))
    expect(latestOptions().url).toBe(resolveApiUrl(`/api/versions/${OTHER_VERSION_ID}/stream`))
    expect(latestOptions().lastEventId).toBe(5)
    await waitFor(() => expect(result.current.events.map((event) => event.seq)).toEqual([5]))

    act(() => first.onEvent(toSseEvent(makeEvent(99))))
    expect(result.current.events.map((event) => event.seq)).not.toContain(99)
  })

  it("treats a channel without persisted events as an empty snapshot", async () => {
    const fetchMock = stubSnapshotError(404, "not_found")

    const { result } = renderHook(() => useVersionStream(VERSION_ID))

    await waitFor(() => expect(mocks.connectSSE).toHaveBeenCalledTimes(1))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(result.current.events).toEqual([])
    expect(latestOptions().lastEventId).toBe(0)
  })

  it("still opens the stream when the snapshot read fails", async () => {
    stubSnapshotError(500, "internal")

    const { result } = renderHook(() => useVersionStream(VERSION_ID))

    await waitFor(() => expect(mocks.connectSSE).toHaveBeenCalledTimes(1))
    expect(result.current.events).toEqual([])
    expect(latestOptions().lastEventId).toBe(0)
  })

  it("stays closed and never opens a stream without a token", async () => {
    sessionStorage.clear()
    const fetchMock = stubSnapshots(eventsPage([], 0))

    const { result } = renderHook(() => useVersionStream(VERSION_ID))

    await waitFor(() => expect(result.current.status).toBe("closed"))
    expect(fetchMock).not.toHaveBeenCalled()
    expect(mocks.connectSSE).not.toHaveBeenCalled()
  })

  it("stays closed for an empty version id", async () => {
    const fetchMock = stubSnapshots(eventsPage([], 0))

    const { result } = renderHook(() => useVersionStream(""))

    await waitFor(() => expect(result.current.status).toBe("closed"))
    expect(fetchMock).not.toHaveBeenCalled()
    expect(mocks.connectSSE).not.toHaveBeenCalled()
  })
})
