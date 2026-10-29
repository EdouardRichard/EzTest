import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { getToken, setSession } from "./auth-storage"
import { connectSSE, consumeSSEStream, type SSEEvent, type SSEStatus } from "./sse-client"

const encoder = new TextEncoder()

/** Builds an SSE response whose body emits the chunks and then ends. */
function streamResponse(chunks: string[]): Response {
  return new Response(
    new ReadableStream<Uint8Array>({
      start(controller) {
        for (const chunk of chunks) controller.enqueue(encoder.encode(chunk))
        controller.close()
      },
    }),
    { status: 200, headers: { "content-type": "text/event-stream" } },
  )
}

/** Builds an SSE response that stays open: a reconnect test can end it via abort. */
function openResponse(): Response {
  return new Response(new ReadableStream<Uint8Array>({ start() {} }), {
    status: 200,
    headers: { "content-type": "text/event-stream" },
  })
}

/** Drains pending microtasks (the reader loop is promise-driven). */
async function flushMicrotasks(): Promise<void> {
  for (let i = 0; i < 25; i++) await Promise.resolve()
}

function requestInit(fetchMock: ReturnType<typeof vi.fn>, call: number): RequestInit {
  return fetchMock.mock.calls[call]?.[1] as RequestInit
}

function requestHeaders(fetchMock: ReturnType<typeof vi.fn>, call: number): Record<string, string> {
  return requestInit(fetchMock, call).headers as Record<string, string>
}

describe("connectSSE", () => {
  beforeEach(() => {
    sessionStorage.clear()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it("parses events, dedupes seq, reconnects with Last-Event-ID", async () => {
    vi.useFakeTimers()
    vi.spyOn(Math, "random").mockReturnValue(0)
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        streamResponse([
          'id: 1\nevent: system.test\ndata: {"seq":1,"type":"system.test"}\n\n',
          ": ping\n\n",
          'id: 2\nevent: system.test\ndata: {"seq":2,"type":"system.test"}\n\n',
          'id: 2\nevent: system.test\ndata: {"seq":2,"type":"system.test"}\n\n',
        ]),
      )
      .mockResolvedValueOnce(openResponse())
    vi.stubGlobal("fetch", fetchMock)

    const events: SSEEvent[] = []
    const statuses: SSEStatus[] = []
    const stop = connectSSE({
      url: "http://localhost:13333/api/stream?channel=system",
      token: "ezt_test",
      onEvent: (event) => events.push(event),
      onStatus: (status) => statuses.push(status),
    })

    await flushMicrotasks()
    expect(events.map((event) => event.id)).toEqual(["1", "2"])
    expect(events[0]).toEqual({
      id: "1",
      event: "system.test",
      data: '{"seq":1,"type":"system.test"}',
    })
    expect(statuses).toEqual(["connecting", "live", "reconnecting"])
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(requestHeaders(fetchMock, 0)).toMatchObject({
      Authorization: "Bearer ezt_test",
      Accept: "text/event-stream",
    })
    expect(requestHeaders(fetchMock, 0)["Last-Event-ID"]).toBeUndefined()

    await vi.advanceTimersByTimeAsync(1000)
    await flushMicrotasks()
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(requestHeaders(fetchMock, 1)["Last-Event-ID"]).toBe("2")

    stop()
    expect(statuses.at(-1)).toBe("closed")
  })

  it("omits Cache-Control because the CORS allow-list is closed", async () => {
    const fetchMock = vi.fn().mockResolvedValue(openResponse())
    vi.stubGlobal("fetch", fetchMock)

    const stop = connectSSE({
      url: "http://localhost:13333/api/stream?channel=system",
      token: "ezt_test",
      lastEventId: 7,
      onEvent: vi.fn(),
      onStatus: vi.fn(),
    })

    await flushMicrotasks()
    const headers = requestHeaders(fetchMock, 0)
    const headerNames = Object.keys(headers).map((name) => name.toLowerCase())
    // The browser must not preflight on a header the API does not allow; the
    // closed allow-list only covers Authorization, Content-Type, X-Request-ID,
    // Idempotency-Key and Last-Event-ID.
    expect(headerNames).not.toContain("cache-control")
    expect(headers).toMatchObject({
      Accept: "text/event-stream",
      Authorization: "Bearer ezt_test",
    })
    expect(headers["Last-Event-ID"]).toBe("7")

    stop()
  })

  it("handles stream.reset without advancing lastSeq", async () => {
    vi.useFakeTimers()
    vi.spyOn(Math, "random").mockReturnValue(0)
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        streamResponse([
          'event: stream.reset\ndata: {"channel":"system","snapshot_cursor":9,"reason":"cursor_too_old"}\n\n',
        ]),
      )
      .mockResolvedValueOnce(openResponse())
    vi.stubGlobal("fetch", fetchMock)

    const onEvent = vi.fn()
    const onReset = vi.fn()
    const statuses: SSEStatus[] = []
    const stop = connectSSE({
      url: "http://localhost:13333/api/stream?channel=system",
      token: "ezt_test",
      lastEventId: 3,
      onEvent,
      onStatus: (status) => statuses.push(status),
      onReset,
    })

    await flushMicrotasks()
    expect(onEvent).not.toHaveBeenCalled()
    expect(onReset).toHaveBeenCalledWith({
      channel: "system",
      snapshot_cursor: 9,
      reason: "cursor_too_old",
    })
    expect(statuses).toEqual(["connecting", "live", "reconnecting"])
    expect(requestHeaders(fetchMock, 0)["Last-Event-ID"]).toBe("3")

    await vi.advanceTimersByTimeAsync(1000)
    await flushMicrotasks()
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(requestHeaders(fetchMock, 1)["Last-Event-ID"]).toBe("3")

    stop()
  })

  it("stops retrying and clears the session on 401", async () => {
    setSession("ezt_expired", {
      id: 1,
      username: "admin",
      role: "admin",
      status: "active",
    })
    vi.useFakeTimers()
    const response = new Response(
      JSON.stringify({
        error: {
          code: "unauthenticated",
          message: "authentication required",
          request_id: "req_401",
          retryable: false,
        },
      }),
      { status: 401, headers: { "content-type": "application/json" } },
    )
    const fetchMock = vi.fn().mockResolvedValue(response)
    vi.stubGlobal("fetch", fetchMock)

    const statuses: SSEStatus[] = []
    const onEvent = vi.fn()
    const stop = connectSSE({
      url: "http://localhost:13333/api/stream?channel=system",
      token: "ezt_expired",
      onEvent,
      onStatus: (status) => statuses.push(status),
    })

    await flushMicrotasks()
    expect(getToken()).toBeNull()
    expect(onEvent).not.toHaveBeenCalled()
    expect(statuses).toEqual(["connecting", "closed"])

    await vi.advanceTimersByTimeAsync(60000)
    await flushMicrotasks()
    expect(fetchMock).toHaveBeenCalledTimes(1)

    stop()
    expect(statuses).toEqual(["connecting", "closed"])
  })

  it("reconnects after 45 seconds without bytes", async () => {
    vi.useFakeTimers()
    vi.spyOn(Math, "random").mockReturnValue(0)
    const fetchMock = vi.fn().mockResolvedValue(openResponse())
    vi.stubGlobal("fetch", fetchMock)

    const statuses: SSEStatus[] = []
    const stop = connectSSE({
      url: "http://localhost:13333/api/stream?channel=system",
      token: "ezt_test",
      onEvent: vi.fn(),
      onStatus: (status) => statuses.push(status),
    })

    await flushMicrotasks()
    expect(statuses).toEqual(["connecting", "live"])

    await vi.advanceTimersByTimeAsync(44999)
    await flushMicrotasks()
    expect(fetchMock).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(1)
    await flushMicrotasks()
    expect(statuses).toContain("reconnecting")
    expect(fetchMock).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(1000)
    await flushMicrotasks()
    expect(fetchMock).toHaveBeenCalledTimes(2)

    stop()
  })

  it("backs off exponentially from 1s and caps at 30s", async () => {
    vi.useFakeTimers()
    vi.spyOn(Math, "random").mockReturnValue(0)
    const fetchMock = vi.fn().mockRejectedValue(new TypeError("network down"))
    vi.stubGlobal("fetch", fetchMock)

    const statuses: SSEStatus[] = []
    const stop = connectSSE({
      url: "http://localhost:13333/api/stream?channel=system",
      token: "ezt_test",
      onEvent: vi.fn(),
      onStatus: (status) => statuses.push(status),
    })

    await flushMicrotasks()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(statuses).toEqual(["connecting", "reconnecting"])

    const delays = [1000, 2000, 4000, 8000, 16000, 30000, 30000]
    for (const [index, delay] of delays.entries()) {
      await vi.advanceTimersByTimeAsync(delay - 1)
      await flushMicrotasks()
      expect(fetchMock).toHaveBeenCalledTimes(index + 1)
      await vi.advanceTimersByTimeAsync(1)
      await flushMicrotasks()
      expect(fetchMock).toHaveBeenCalledTimes(index + 2)
    }

    stop()
  })

  it("reads a one-shot SSE response and ignores comments", async () => {
    const events: SSEEvent[] = []
    await consumeSSEStream(
      streamResponse([
        ": ping\n\n",
        'event: llm.delta\ndata: {"delta":"你"}\n\n',
        'event: llm.done\ndata: {"latency_ms":5}\n\n',
      ]),
      { onEvent: (event) => events.push(event) },
    )

    expect(events.map((event) => event.event)).toEqual(["llm.delta", "llm.done"])
    expect(events[0]).toEqual({ id: undefined, event: "llm.delta", data: '{"delta":"你"}' })
    expect(events[1]?.data).toBe('{"latency_ms":5}')
  })

  it("aborts the request and never reconnects after cleanup", async () => {
    vi.useFakeTimers()
    const fetchMock = vi.fn().mockResolvedValue(openResponse())
    vi.stubGlobal("fetch", fetchMock)

    const statuses: SSEStatus[] = []
    const stop = connectSSE({
      url: "http://localhost:13333/api/stream?channel=system",
      token: "ezt_test",
      onEvent: vi.fn(),
      onStatus: (status) => statuses.push(status),
    })

    await flushMicrotasks()
    const signal = requestInit(fetchMock, 0).signal as AbortSignal
    expect(signal.aborted).toBe(false)

    stop()
    expect(signal.aborted).toBe(true)
    expect(statuses.at(-1)).toBe("closed")

    await vi.advanceTimersByTimeAsync(120000)
    await flushMicrotasks()
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})
