import { act, cleanup, render, screen } from "@testing-library/react"
import { createElement } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { setToken } from "@/lib/auth-storage"

type ConnectOptions = {
  url: string
  token: string
  lastEventId?: number
  onEvent: (event: { id?: string; event: string; data: string }) => void
  onStatus: (status: string) => void
  onReset?: (data: unknown) => void
}

const sse = vi.hoisted(() => ({
  connectSSE: vi.fn<(options: ConnectOptions) => () => void>(),
}))

vi.mock("@/lib/sse-client", () => ({
  connectSSE: sse.connectSSE,
}))

import { useCodeEvents } from "../useCodeEvents"

function eventFrame(type: string, payload: Record<string, unknown> = {}): string {
  return JSON.stringify({
    seq: 11,
    channel: "version:2001",
    type,
    scope: { version_id: "2001" },
    payload,
    actor_id: null,
    request_id: "req_code_sse",
    created_at: "2026-11-01T10:00:00Z",
  })
}

function Harness(props: {
  handlers: Parameters<typeof useCodeEvents>[1]
}): ReturnType<typeof createElement> {
  const status = useCodeEvents("2001", props.handlers)
  return createElement("span", { "data-testid": "code-sse-status" }, status)
}

describe("useCodeEvents", () => {
  let connectOptions: ConnectOptions
  let cleanupSpy: ReturnType<typeof vi.fn<() => void>>

  beforeEach(() => {
    window.sessionStorage.clear()
    setToken("ezt_token")
    vi.stubEnv("NEXT_PUBLIC_API_BASE_URL", "")
    cleanupSpy = vi.fn<() => void>()
    sse.connectSSE.mockReset()
    sse.connectSSE.mockImplementation((options: ConnectOptions) => {
      connectOptions = options
      return cleanupSpy
    })
  })

  afterEach(() => {
    cleanup()
    vi.useRealTimers()
    vi.unstubAllEnvs()
    window.sessionStorage.clear()
  })

  it("subscribes to the version channel with the bearer token", () => {
    render(createElement(Harness, { handlers: {} }))

    expect(sse.connectSSE).toHaveBeenCalledTimes(1)
    expect(connectOptions.url).toBe("http://localhost:13333/api/stream?channel=version:2001")
    expect(connectOptions.token).toBe("ezt_token")
    expect(screen.getByTestId("code-sse-status")).toHaveTextContent("connecting")

    act(() => connectOptions.onStatus("live"))
    expect(screen.getByTestId("code-sse-status")).toHaveTextContent("live")
  })

  it("debounces a burst of code.* events into one refresh", () => {
    vi.useFakeTimers()
    const onCodeChanged = vi.fn()
    render(createElement(Harness, { handlers: { onCodeChanged } }))

    act(() => {
      connectOptions.onEvent({ event: "message", data: eventFrame("code.index_started") })
      connectOptions.onEvent({ event: "message", data: eventFrame("code.index_finished") })
      connectOptions.onEvent({ event: "message", data: eventFrame("visual.diff_created") })
      connectOptions.onEvent({ event: "message", data: eventFrame("code.analysis_finished") })
    })

    act(() => vi.advanceTimersByTime(349))
    expect(onCodeChanged).not.toHaveBeenCalled()

    act(() => vi.advanceTimersByTime(1))
    expect(onCodeChanged).toHaveBeenCalledTimes(1)
  })

  it("ignores malformed and non-code frames", () => {
    vi.useFakeTimers()
    const onCodeChanged = vi.fn()
    render(createElement(Harness, { handlers: { onCodeChanged } }))

    act(() => {
      connectOptions.onEvent({ event: "message", data: "not json" })
      connectOptions.onEvent({ event: "message", data: JSON.stringify({ type: 42 }) })
      connectOptions.onEvent({ event: "message", data: eventFrame("requirement.clarified") })
      connectOptions.onEvent({ event: "message", data: eventFrame("code.anchor_validated") })
      connectOptions.onEvent({ event: "message", data: eventFrame("code.fact_reviewed") })
    })
    act(() => vi.advanceTimersByTime(350))

    expect(onCodeChanged).toHaveBeenCalledTimes(1)
  })

  it("refreshes immediately on stream.reset instead of waiting for the debounce", () => {
    vi.useFakeTimers()
    const onCodeChanged = vi.fn()
    const onStreamReset = vi.fn()
    render(createElement(Harness, { handlers: { onCodeChanged, onStreamReset } }))

    act(() => {
      connectOptions.onReset?.({ reason: "cursor_too_old", snapshot_cursor: 99 })
    })

    expect(onStreamReset).toHaveBeenCalledWith({ reason: "cursor_too_old", snapshot_cursor: 99 })
    expect(onCodeChanged).not.toHaveBeenCalled()
  })

  it("disconnects on unmount", () => {
    const { unmount } = render(createElement(Harness, { handlers: {} }))
    unmount()

    expect(cleanupSpy).toHaveBeenCalledTimes(1)
  })
})
