import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const nav = vi.hoisted(() => ({ pathname: "/versions/11" }))

vi.mock("next/navigation", () => ({
  usePathname: () => nav.pathname,
}))

import { TODOS_POLL_INTERVAL_MS, useTodos } from "./useTodos"

const SNAPSHOT = {
  items: [
    {
      kind: "review",
      title: "版本甲",
      versionId: "11",
      versionName: "版本甲",
      severity: "info",
      createdAt: "2026-10-29T00:00:00Z",
      url: "/versions/11?tab=mindmap",
    },
  ],
  counts: { review: 1, takeover: 0, gate: 0 },
}

function jsonResponse(body: unknown): Response {
  const text = JSON.stringify(body)
  return {
    ok: true,
    status: 200,
    headers: new Headers({ "content-type": "application/json" }),
    text: async () => text,
    json: async () => body,
  } as unknown as Response
}

function okFetch() {
  return vi.fn().mockResolvedValue(jsonResponse({ data: SNAPSHOT }))
}

beforeEach(() => {
  nav.pathname = "/versions/11"
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe("useTodos", () => {
  it("loads the aggregated todos and normalizes the payload", async () => {
    const fetchMock = okFetch()
    vi.stubGlobal("fetch", fetchMock)

    const { result } = renderHook(() => useTodos())
    expect(result.current.loading).toBe(true)
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.error).toBe("")
    expect(result.current.todos.items).toHaveLength(1)
    expect(result.current.todos.counts.review).toBe(1)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain("/api/todos?limit=50")
  })

  it("refreshes when the route changes", async () => {
    const fetchMock = okFetch()
    vi.stubGlobal("fetch", fetchMock)
    const { rerender } = renderHook(() => useTodos())
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))

    nav.pathname = "/versions/12"
    rerender()
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
  })

  it("polls every thirty seconds", async () => {
    vi.useFakeTimers()
    const fetchMock = okFetch()
    vi.stubGlobal("fetch", fetchMock)

    renderHook(() => useTodos())
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(fetchMock).toHaveBeenCalledTimes(1)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(TODOS_POLL_INTERVAL_MS)
    })
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it("keeps the failure visible and recovers through refresh", async () => {
    const fetchMock = vi
      .fn()
      .mockRejectedValueOnce(new TypeError("network failed"))
      .mockResolvedValueOnce(jsonResponse({ data: SNAPSHOT }))
    vi.stubGlobal("fetch", fetchMock)

    const { result } = renderHook(() => useTodos())
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).not.toBe("")
    expect(result.current.todos.items).toHaveLength(0)

    await act(async () => {
      await result.current.refresh()
    })
    expect(result.current.error).toBe("")
    expect(result.current.todos.items).toHaveLength(1)
  })
})
