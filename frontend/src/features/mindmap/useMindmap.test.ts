import { act, cleanup, renderHook } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { VersionEvent } from "@/features/versions/types"
import { ApiError } from "@/lib/api-client"
import type { SSEStatus } from "@/lib/sse-client"

import { describeMindmapError } from "./error-messages"
import type { MindmapGraph } from "./types"

const mocks = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  apiPatch: vi.fn(),
  apiPut: vi.fn(),
  apiDelete: vi.fn(),
  apiStreamFetch: vi.fn(),
  useVersionStream: vi.fn(),
}))

vi.mock("@/lib/api-client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api-client")>()
  return {
    ...actual,
    apiGet: mocks.apiGet,
    apiPost: mocks.apiPost,
    apiPatch: mocks.apiPatch,
    apiPut: mocks.apiPut,
    apiDelete: mocks.apiDelete,
    apiStreamFetch: mocks.apiStreamFetch,
  }
})

vi.mock("@/features/versions/useVersionStream", () => ({
  useVersionStream: mocks.useVersionStream,
}))

import { useMindmap } from "./useMindmap"

const VERSION_ID = "1844674407370955161"
const OTHER_VERSION_ID = "1844674407370955162"

type StreamState = {
  events: VersionEvent[]
  status: SSEStatus
  reconnect: () => void
}

let streamState: StreamState

function graphFixture(overrides: Partial<MindmapGraph> = {}): MindmapGraph {
  return {
    versionId: VERSION_ID,
    versionStatus: "designing",
    editable: true,
    nodes: [],
    coverage: {
      requirementIds: [],
      uncoveredRequirementIds: [],
      staleRequirementIds: [],
      matrix: [],
    },
    baseline: null,
    latestRuns: {},
    impactCases: [],
    ...overrides,
  }
}

function event(seq: number, type = "case.created"): VersionEvent {
  return {
    seq,
    channel: `version:${VERSION_ID}`,
    type,
    version_id: VERSION_ID,
    batch_id: null,
    case_id: null,
    step_id: null,
    agent_role: null,
    session_id: null,
    ts: "2026-10-28T02:00:00Z",
    summary: `事件 ${seq}`,
    detail: {},
    refs: { evidence: [], case: [], requirement: [], code: [] },
  }
}

async function flushPromises(): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0)
  })
}

async function advance(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

function pushEvents(rendered: { rerender: () => void }, events: VersionEvent[]): void {
  streamState = { ...streamState, events }
  rendered.rerender()
}

describe("useMindmap", () => {
  beforeEach(() => {
    vi.useFakeTimers()
    mocks.apiGet.mockReset()
    mocks.apiPost.mockReset()
    mocks.apiPatch.mockReset()
    mocks.apiPut.mockReset()
    mocks.apiDelete.mockReset()
    mocks.apiStreamFetch.mockReset()
    mocks.useVersionStream.mockReset()
    streamState = { events: [], status: "live", reconnect: vi.fn() }
    mocks.useVersionStream.mockImplementation(() => streamState)
  })

  afterEach(() => {
    cleanup()
    vi.useRealTimers()
  })

  it("loads the graph and exposes the stream status", async () => {
    const graph = graphFixture()
    mocks.apiGet.mockResolvedValueOnce(graph)

    const rendered = renderHook(() => useMindmap(VERSION_ID))
    expect(rendered.result.current.loading).toBe(true)

    await flushPromises()

    expect(mocks.apiGet).toHaveBeenCalledWith(`/api/versions/${VERSION_ID}/mindmap`)
    expect(rendered.result.current.graph).toBe(graph)
    expect(rendered.result.current.loading).toBe(false)
    expect(rendered.result.current.error).toBe("")
    expect(rendered.result.current.streamStatus).toBe("live")
  })

  it("surfaces a described error when the initial load fails", async () => {
    const caught = new ApiError({
      status: 409,
      code: "conflict",
      message: "graph too large",
      details: { reason: "graph_too_large" },
    })
    mocks.apiGet.mockRejectedValueOnce(caught)

    const rendered = renderHook(() => useMindmap(VERSION_ID))
    await flushPromises()

    expect(rendered.result.current.graph).toBeNull()
    expect(rendered.result.current.loading).toBe(false)
    expect(rendered.result.current.error).toBe(describeMindmapError(caught))
    expect(rendered.result.current.error).not.toBe("")
  })

  it("refreshes on matching SSE events at most once per throttle window", async () => {
    const first = graphFixture()
    const second = graphFixture({ editable: false })
    const third = graphFixture()
    mocks.apiGet
      .mockResolvedValueOnce(first)
      .mockResolvedValueOnce(second)
      .mockResolvedValueOnce(third)

    const rendered = renderHook(() => useMindmap(VERSION_ID))
    await flushPromises()
    expect(mocks.apiGet).toHaveBeenCalledTimes(1)

    await advance(100)
    pushEvents(rendered, [event(1)])
    await flushPromises()
    // The throttle window opened by the initial load: no second read yet.
    expect(mocks.apiGet).toHaveBeenCalledTimes(1)

    await advance(100)
    pushEvents(rendered, [event(1), event(2)])
    await flushPromises()
    expect(mocks.apiGet).toHaveBeenCalledTimes(1)

    // One trailing read fires when the 500ms window closes.
    await advance(300)
    await flushPromises()
    expect(mocks.apiGet).toHaveBeenCalledTimes(2)
    expect(rendered.result.current.graph).toBe(second)

    pushEvents(rendered, [event(1), event(2), event(3)])
    await flushPromises()
    await advance(499)
    expect(mocks.apiGet).toHaveBeenCalledTimes(2)
    await advance(1)
    await flushPromises()
    expect(mocks.apiGet).toHaveBeenCalledTimes(3)
    expect(rendered.result.current.graph).toBe(third)
  })

  it("ignores events outside the case.*/version.status_changed/gate.* set", async () => {
    mocks.apiGet.mockResolvedValue(graphFixture())

    const rendered = renderHook(() => useMindmap(VERSION_ID))
    await flushPromises()

    pushEvents(rendered, [event(1, "requirement.updated")])
    await flushPromises()
    await advance(1000)
    await flushPromises()

    expect(mocks.apiGet).toHaveBeenCalledTimes(1)
  })

  it("refetches the whole graph when the stream resets with a cursor gap", async () => {
    const first = graphFixture()
    const second = graphFixture({ versionStatus: "reviewing" })
    mocks.apiGet.mockResolvedValueOnce(first).mockResolvedValueOnce(second)

    const rendered = renderHook(() => useMindmap(VERSION_ID))
    await flushPromises()
    expect(mocks.apiGet).toHaveBeenCalledTimes(1)

    await advance(100)
    pushEvents(rendered, [event(1)])
    await flushPromises()
    expect(mocks.apiGet).toHaveBeenCalledTimes(1)

    // A reset prunes the channel: the replacement snapshot starts at seq 6, so
    // seqs 2-5 were missed and only a full refetch can repair the graph.
    await advance(100)
    pushEvents(rendered, [event(6)])
    await flushPromises()

    expect(mocks.apiGet).toHaveBeenCalledTimes(2)
    expect(rendered.result.current.graph).toBe(second)
    // The full refresh cancels the pending throttled read.
    await advance(1000)
    await flushPromises()
    expect(mocks.apiGet).toHaveBeenCalledTimes(2)
  })

  it("refetches when the stream reconnects after a reset", async () => {
    mocks.apiGet.mockResolvedValueOnce(graphFixture()).mockResolvedValueOnce(graphFixture())

    const rendered = renderHook(() => useMindmap(VERSION_ID))
    await flushPromises()
    expect(mocks.apiGet).toHaveBeenCalledTimes(1)

    streamState = { ...streamState, status: "reconnecting" }
    rendered.rerender()
    await flushPromises()

    streamState = { ...streamState, status: "live" }
    rendered.rerender()
    await flushPromises()

    expect(mocks.apiGet).toHaveBeenCalledTimes(2)
  })

  it("replaces the graph in place with applyGraph", async () => {
    const first = graphFixture()
    const next = graphFixture({ editable: false })
    mocks.apiGet.mockResolvedValueOnce(first)

    const rendered = renderHook(() => useMindmap(VERSION_ID))
    await flushPromises()

    act(() => rendered.result.current.applyGraph(next))

    expect(rendered.result.current.graph).toBe(next)
    expect(mocks.apiGet).toHaveBeenCalledTimes(1)
  })

  it("refetches the graph through refresh()", async () => {
    const first = graphFixture()
    const second = graphFixture({ editable: false })
    mocks.apiGet.mockResolvedValueOnce(first).mockResolvedValueOnce(second)

    const rendered = renderHook(() => useMindmap(VERSION_ID))
    await flushPromises()

    await act(async () => {
      await rendered.result.current.refresh()
    })

    expect(mocks.apiGet).toHaveBeenCalledTimes(2)
    expect(rendered.result.current.graph).toBe(second)
  })

  it("reloads when the version changes", async () => {
    mocks.apiGet.mockResolvedValue(graphFixture())

    const rendered = renderHook(({ id }: { id: string }) => useMindmap(id), {
      initialProps: { id: VERSION_ID },
    })
    await flushPromises()
    expect(mocks.apiGet).toHaveBeenLastCalledWith(`/api/versions/${VERSION_ID}/mindmap`)

    rendered.rerender({ id: OTHER_VERSION_ID })
    await flushPromises()

    expect(mocks.apiGet).toHaveBeenCalledTimes(2)
    expect(mocks.apiGet).toHaveBeenLastCalledWith(`/api/versions/${OTHER_VERSION_ID}/mindmap`)
  })
})
