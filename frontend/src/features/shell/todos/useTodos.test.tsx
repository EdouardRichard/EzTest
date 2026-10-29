import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const nav = vi.hoisted(() => ({ pathname: "/versions/11" }))

vi.mock("next/navigation", () => ({
  usePathname: () => nav.pathname,
}))

import {
  KNOWLEDGE_QUEUE_LIMIT,
  mergeKnowledgeTodos,
  normalizeKnowledgeTodos,
  TODOS_POLL_INTERVAL_MS,
  useTodos,
} from "./useTodos"

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

  it("merges the admin knowledge review queue into the review group and count", async () => {
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      if (String(url).includes("/api/knowledge")) {
        return Promise.resolve(
          jsonResponse({
            data: [
              {
                id: "1844674407370955161",
                versionId: "11",
                versionName: "版本甲",
                title: "支付超时",
                createdAt: "2026-10-29T01:00:00Z",
              },
            ],
            has_more: false,
          }),
        )
      }
      return Promise.resolve(jsonResponse({ data: SNAPSHOT }))
    })
    vi.stubGlobal("fetch", fetchMock)

    const { result } = renderHook(() => useTodos(true))
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(fetchMock).toHaveBeenCalledTimes(2)
    const urls = fetchMock.mock.calls.map((call) => String(call[0]))
    expect(urls[0]).toContain("/api/todos?limit=50")
    expect(urls[1]).toContain("/api/knowledge?status=draft&limit=50")
    expect(result.current.todos.counts.review).toBe(2)
    const knowledge = result.current.todos.items.find(
      (item) => item.kind === "review" && item.url.startsWith("/knowledge"),
    )
    expect(knowledge?.title).toBe("知识审核：支付超时")
    expect(knowledge?.url).toBe("/knowledge?review=1844674407370955161")
  })

  it("records the knowledge queue cap when the admin page has more drafts", async () => {
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      if (String(url).includes("/api/knowledge")) {
        return Promise.resolve(
          jsonResponse({
            data: [
              {
                id: "6001",
                versionId: "11",
                versionName: "版本甲",
                title: "支付超时",
                createdAt: "2026-10-29T01:00:00Z",
              },
            ],
            has_more: true,
          }),
        )
      }
      return Promise.resolve(jsonResponse({ data: SNAPSHOT }))
    })
    vi.stubGlobal("fetch", fetchMock)

    const { result } = renderHook(() => useTodos(true))
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(String(fetchMock.mock.calls[1]?.[0])).toContain(`limit=${KNOWLEDGE_QUEUE_LIMIT}`)
    expect(result.current.todos.knowledgeTruncated).toBe(true)
    expect(result.current.todos.counts.review).toBe(2)
  })

  it("dedupes merged knowledge drafts and keeps the truncated flag", () => {
    const snapshot = {
      items: normalizeKnowledgeTodos([
        { id: "6001", versionId: "11", versionName: "版本甲", title: "支付超时" },
      ]),
      counts: { review: 1, takeover: 0, gate: 0 },
    }
    const duplicate = normalizeKnowledgeTodos([
      { id: "6001", versionId: "11", versionName: "版本甲", title: "支付超时" },
      { id: "6002", versionId: "11", versionName: "版本甲", title: "幂等键" },
    ])

    const merged = mergeKnowledgeTodos(snapshot, duplicate, true)

    expect(merged.items).toHaveLength(2)
    expect(merged.counts.review).toBe(2)
    expect(merged.knowledgeTruncated).toBe(true)
    expect(mergeKnowledgeTodos(snapshot, duplicate, false).knowledgeTruncated).toBe(false)
  })

  it("does not request the knowledge queue for a regular user", async () => {
    const fetchMock = okFetch()
    vi.stubGlobal("fetch", fetchMock)

    const { result } = renderHook(() => useTodos(false))
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(String(fetchMock.mock.calls[0]?.[0])).not.toContain("/api/knowledge")
    expect(result.current.todos.counts.review).toBe(1)
  })

  it("keeps the version todos when the knowledge queue read fails", async () => {
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      if (String(url).includes("/api/knowledge")) {
        return Promise.reject(new TypeError("network failed"))
      }
      return Promise.resolve(jsonResponse({ data: SNAPSHOT }))
    })
    vi.stubGlobal("fetch", fetchMock)

    const { result } = renderHook(() => useTodos(true))
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.error).toBe("")
    expect(result.current.todos.items).toHaveLength(1)
    expect(result.current.todos.counts.review).toBe(1)
  })

  it("normalizes knowledge drafts into review todo items and ignores malformed rows", () => {
    const items = normalizeKnowledgeTodos([
      null,
      { id: 12, title: "numeric id" },
      {
        id: "6001",
        versionId: "11",
        versionName: "版本甲",
        title: "支付超时",
        createdAt: "2026-10-29T01:00:00Z",
      },
    ])
    expect(items).toHaveLength(1)
    expect(items[0]?.kind).toBe("review")
    expect(items[0]?.title).toBe("知识审核：支付超时")
    expect(items[0]?.versionId).toBe("11")
    expect(items[0]?.url).toBe("/knowledge?review=6001")
  })
})
