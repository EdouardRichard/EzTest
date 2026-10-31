import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { AUTH_CLEARED_EVENT, getToken, setToken } from "./auth-storage"
import { connectSSE, type SSEEvent, type SSEStatus } from "./sse-client"

type StreamHandle = {
  response: Response
  push: (chunk: string) => void
  close: () => void
}

function controlledStream(status = 200): StreamHandle {
  const encoder = new TextEncoder()
  let controller: ReadableStreamDefaultController<Uint8Array> | null = null
  const stream = new ReadableStream<Uint8Array>({
    start(inner) {
      controller = inner
    },
  })
  return {
    response: new Response(stream, {
      status,
      headers: { "Content-Type": "text/event-stream" },
    }),
    push(chunk) {
      controller?.enqueue(encoder.encode(chunk))
    },
    close() {
      controller?.close()
      controller = null
    },
  }
}

type Harness = {
  events: SSEEvent[]
  statuses: SSEStatus[]
  resets: unknown[]
  cleanup: () => void
}

function start(
  fetchMock: ReturnType<typeof vi.fn>,
  overrides: Partial<Parameters<typeof connectSSE>[0]> = {},
): Harness {
  const events: SSEEvent[] = []
  const statuses: SSEStatus[] = []
  const resets: unknown[] = []
  const cleanup = connectSSE({
    url: "http://localhost:13333/api/stream?channel=system",
    token: "ezt_token",
    onEvent: (event) => events.push(event),
    onStatus: (status) => statuses.push(status),
    onReset: (data) => resets.push(data),
    ...overrides,
  })
  expect(fetchMock).toHaveBeenCalledTimes(1)
  return { events, statuses, resets, cleanup }
}

const flush = async () => {
  await vi.advanceTimersByTimeAsync(0)
}

describe("connectSSE", () => {
  const fetchMock = vi.fn<typeof fetch>()

  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    vi.useFakeTimers()
    vi.spyOn(Math, "random").mockReturnValue(0)
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it("opens an authorized fetch stream and parses frames", async () => {
    const handle = controlledStream()
    fetchMock.mockResolvedValue(handle.response)

    const harness = start(fetchMock, { lastEventId: 3 })
    expect(harness.statuses).toEqual(["connecting"])
    await flush()

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/stream?channel=system")
    const headers = new Headers(init?.headers)
    expect(headers.get("Authorization")).toBe("Bearer ezt_token")
    expect(headers.get("Accept")).toBe("text/event-stream")
    expect(headers.get("Last-Event-ID")).toBe("3")
    expect(harness.statuses).toEqual(["connecting", "live"])

    handle.push(
      ": heartbeat\n" +
        'id: 4\nevent: version.created\ndata: {"seq":4,"title":"v4"}\n\n' +
        'id: 5\nevent: message\ndata: {"seq":5}\ndata: second-line\n\n',
    )
    await flush()

    expect(harness.events).toEqual([
      { id: "4", event: "version.created", data: '{"seq":4,"title":"v4"}' },
      { id: "5", event: "message", data: '{"seq":5}\nsecond-line' },
    ])

    harness.cleanup()
  })

  it("dedupes events by channel seq, including the initial cursor", async () => {
    const handle = controlledStream()
    fetchMock.mockResolvedValue(handle.response)

    const harness = start(fetchMock, { lastEventId: 3 })
    await flush()

    handle.push(
      'id: 3\nevent: version.created\ndata: {"seq":3}\n\n' +
        'id: 4\nevent: version.created\ndata: {"seq":4}\n\n' +
        'id: 4\nevent: version.created\ndata: {"seq":4}\n\n',
    )
    await flush()

    expect(harness.events.map((event) => event.id)).toEqual(["4"])
    harness.cleanup()
  })

  it("routes stream.reset to onReset without dispatching it as an event", async () => {
    const handle = controlledStream()
    fetchMock.mockResolvedValue(handle.response)

    const harness = start(fetchMock)
    await flush()

    handle.push('event: stream.reset\ndata: {"reason":"cursor_too_old"}\n\n')
    await flush()

    expect(harness.resets).toEqual([{ reason: "cursor_too_old" }])
    expect(harness.events).toEqual([])

    handle.push('id: 1\nevent: version.created\ndata: {"seq":1}\n\n')
    await flush()
    expect(harness.events).toEqual([{ id: "1", event: "version.created", data: '{"seq":1}' }])
    harness.cleanup()
  })

  it("reconnects with Last-Event-ID using 1s->2s exponential backoff", async () => {
    const first = controlledStream()
    const second = controlledStream()
    const third = controlledStream()
    fetchMock
      .mockResolvedValueOnce(first.response)
      .mockResolvedValueOnce(second.response)
      .mockResolvedValueOnce(third.response)

    const harness = start(fetchMock)
    await flush()
    first.push('id: 7\nevent: version.created\ndata: {"seq":7}\n\n')
    await flush()
    first.close()
    await flush()
    expect(harness.statuses).toContain("reconnecting")

    await vi.advanceTimersByTimeAsync(999)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    const reconnectHeaders = new Headers(fetchMock.mock.calls[1][1]?.headers)
    expect(reconnectHeaders.get("Last-Event-ID")).toBe("7")

    await flush()
    second.close()
    await flush()
    await vi.advanceTimersByTimeAsync(1_999)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(1)
    expect(fetchMock).toHaveBeenCalledTimes(3)

    harness.cleanup()
    expect(harness.statuses.at(-1)).toBe("closed")
  })

  it("drops the connection and clears the session on 401", async () => {
    fetchMock.mockResolvedValue(
      new Response(JSON.stringify({ error: { code: "unauthenticated" } }), {
        status: 401,
        headers: { "Content-Type": "application/json" },
      }),
    )
    setToken("ezt_expired")
    const cleared = vi.fn()
    window.addEventListener(AUTH_CLEARED_EVENT, cleared)

    const harness = start(fetchMock, { token: "ezt_expired" })
    await flush()

    expect(getToken()).toBeNull()
    expect(cleared).toHaveBeenCalledTimes(1)
    expect(harness.statuses.at(-1)).toBe("closed")
    await vi.advanceTimersByTimeAsync(60_000)
    expect(fetchMock).toHaveBeenCalledTimes(1)

    window.removeEventListener(AUTH_CLEARED_EVENT, cleared)
    harness.cleanup()
  })

  it("times out and reconnects after 45s without bytes", async () => {
    const first = controlledStream()
    const second = controlledStream()
    fetchMock.mockResolvedValueOnce(first.response).mockResolvedValueOnce(second.response)

    const harness = start(fetchMock)
    await flush()
    expect(harness.statuses).toContain("live")

    await vi.advanceTimersByTimeAsync(44_999)
    expect(harness.statuses).not.toContain("reconnecting")
    await vi.advanceTimersByTimeAsync(1)
    expect(harness.statuses).toContain("reconnecting")

    await vi.advanceTimersByTimeAsync(1_000)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    harness.cleanup()
  })

  it("stops cleanly and reports closed", async () => {
    const handle = controlledStream()
    fetchMock.mockResolvedValue(handle.response)

    const harness = start(fetchMock)
    await flush()
    const init = fetchMock.mock.calls[0][1]
    const signal = init?.signal as AbortSignal | null
    expect(signal?.aborted).toBe(false)

    harness.cleanup()
    harness.cleanup()

    expect(signal?.aborted).toBe(true)
    expect(harness.statuses.at(-1)).toBe("closed")
    handle.push('id: 9\nevent: version.created\ndata: {"seq":9}\n\n')
    await flush()
    expect(harness.events).toEqual([])
    await vi.advanceTimersByTimeAsync(60_000)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})
