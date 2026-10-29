import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { VersionEvent } from "@/features/versions/types"
import type { VersionStreamState } from "@/features/versions/useVersionStream"
import type { SSEStatus } from "@/lib/sse-client"

import type { ChainSnapshot } from "./chain-types"

const mocks = vi.hoisted(() => ({
  apiGet: vi.fn(),
}))

vi.mock("@/lib/api-client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api-client")>()
  return { ...actual, apiGet: mocks.apiGet }
})

import { useExplorationChain } from "./use-exploration-chain"

const VERSION_ID = "1844674407370955161"
const BATCH_ID = "1844674407370955162"
const R1 = "1844674407370955101"
const C1 = "1844674407370955201"
const CR1 = "1844674407370955301"

function snapshot(overrides: Partial<ChainSnapshot> = {}): ChainSnapshot {
  return {
    versionId: VERSION_ID,
    batch: { id: BATCH_ID, batchNo: 1, kind: "baseline", status: "done" },
    nodes: [
      {
        id: `requirement:${R1}`,
        kind: "requirement",
        title: "登录需求",
        status: "clarified",
      },
      {
        id: `case:${C1}`,
        kind: "case",
        title: "登录成功",
        status: "executed",
        requirementIds: [R1],
        source: "user_input",
      },
    ],
    edges: [
      {
        id: `requirement_case:${R1}:${C1}`,
        source: `requirement:${R1}`,
        target: `case:${C1}`,
        kind: "requirement_case",
      },
      {
        id: `case_result:${C1}:${CR1}`,
        source: `case:${C1}`,
        target: `result:${CR1}`,
        kind: "case_result",
      },
    ],
    counts: {
      requirements: 1,
      cases: 1,
      results: 1,
      issues: 0,
      byResultStatus: {
        pending: 0,
        running: 0,
        passed: 1,
        failed: 0,
        blocked: 0,
        inconclusive: 0,
        skipped: 0,
        interrupted: 0,
      },
      byIssueSeverity: { blocker: 0, major: 0, minor: 0, info: 0 },
      uncoveredRequirements: 0,
      revalidationCases: 0,
      supplementCases: 0,
    },
    truncated: { requirements: false, cases: false, results: false, issues: false },
    generatedAt: "2026-10-29T10:00:00Z",
    ...overrides,
  }
}

function versionEvent(seq: number, type: string): VersionEvent {
  return {
    seq,
    channel: `version:${VERSION_ID}`,
    type,
    version_id: VERSION_ID,
    batch_id: BATCH_ID,
    case_id: null,
    step_id: null,
    agent_role: null,
    session_id: null,
    ts: "2026-10-29T10:00:00Z",
    summary: "",
    detail: {},
    refs: { evidence: [], case: [], requirement: [], code: [] },
  }
}

function stream(events: VersionEvent[] = [], status: SSEStatus = "live"): VersionStreamState {
  return { events, status, reconnect: vi.fn() }
}

function requestUrl(call: unknown[]): URL {
  const [path] = call as [string, { signal?: AbortSignal }?]
  return new URL(path, "http://localhost")
}

beforeEach(() => {
  mocks.apiGet.mockReset()
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

describe("useExplorationChain", () => {
  it("requests the read model with the default bounds and every server filter", async () => {
    mocks.apiGet.mockResolvedValue(snapshot())

    const { result } = renderHook(() =>
      useExplorationChain(
        VERSION_ID,
        {
          batchId: BATCH_ID,
          requirementId: R1,
          resultStatus: "failed",
          abnormalOnly: true,
          issueStatus: "open",
          issueSeverity: "major",
          q: "登录",
          includeSupplement: false,
          revalidationOnly: true,
        },
        stream(),
      ),
    )

    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(mocks.apiGet).toHaveBeenCalledTimes(1)
    const [path, init] = mocks.apiGet.mock.calls[0] as [
      string,
      { signal?: AbortSignal } | undefined,
    ]
    expect(path).toContain(`/api/versions/${VERSION_ID}/exploration-chain`)
    expect(init?.signal).toBeInstanceOf(AbortSignal)
    expect(init?.signal?.aborted).toBe(false)

    const url = requestUrl(mocks.apiGet.mock.calls[0] as unknown[])
    expect(url.searchParams.get("limit")).toBe("300")
    expect(url.searchParams.get("requirementLimit")).toBe("100")
    expect(Number(url.searchParams.get("limit"))).toBeLessThanOrEqual(2000)
    expect(url.searchParams.get("batchId")).toBe(BATCH_ID)
    expect(url.searchParams.get("requirementId")).toBe(R1)
    expect(url.searchParams.get("resultStatus")).toBe("failed")
    expect(url.searchParams.get("abnormalOnly")).toBe("true")
    expect(url.searchParams.get("issueStatus")).toBe("open")
    expect(url.searchParams.get("issueSeverity")).toBe("major")
    expect(url.searchParams.get("q")).toBe("登录")
    expect(url.searchParams.get("includeSupplement")).toBe("false")
    expect(url.searchParams.get("revalidationOnly")).toBe("true")
    expect(result.current.data?.versionId).toBe(VERSION_ID)
    expect(result.current.error).toBe("")
  })

  it("never sends a limit above the backend maximum", async () => {
    mocks.apiGet.mockResolvedValue(snapshot())

    const { result } = renderHook(() =>
      useExplorationChain(VERSION_ID, { limit: 5000, requirementLimit: 900 }, stream()),
    )
    await waitFor(() => expect(result.current.loading).toBe(false))

    const url = requestUrl(mocks.apiGet.mock.calls[0] as unknown[])
    expect(url.searchParams.get("limit")).toBe("2000")
    expect(url.searchParams.get("requirementLimit")).toBe("500")
  })

  it("coalesces a burst of live chain events into one refresh after 500ms", async () => {
    vi.useFakeTimers()
    mocks.apiGet.mockResolvedValue(snapshot())

    const { rerender } = renderHook(
      ({ currentStream }: { currentStream: VersionStreamState }) =>
        useExplorationChain(VERSION_ID, {}, currentStream),
      { initialProps: { currentStream: stream() } },
    )
    await act(async () => {})

    expect(mocks.apiGet).toHaveBeenCalledTimes(1)

    rerender({
      currentStream: stream([
        versionEvent(1, "case.status_changed"),
        versionEvent(2, "run.finished"),
        versionEvent(3, "issue.updated"),
      ]),
    })

    await act(async () => {
      vi.advanceTimersByTime(499)
    })
    expect(mocks.apiGet).toHaveBeenCalledTimes(1)

    await act(async () => {
      vi.advanceTimersByTime(1)
    })
    await act(async () => {})
    expect(mocks.apiGet).toHaveBeenCalledTimes(2)
  })

  it("treats the events present at mount as the baseline and only refreshes for new ones", async () => {
    vi.useFakeTimers()
    let initialSignal: AbortSignal | undefined
    let resolveInitial: ((value: ChainSnapshot) => void) | undefined
    mocks.apiGet
      .mockImplementationOnce(
        (_path: string, init?: { signal?: AbortSignal }) =>
          new Promise<ChainSnapshot>((resolve) => {
            initialSignal = init?.signal
            resolveInitial = resolve
          }),
      )
      .mockResolvedValue(snapshot())

    const historical = [versionEvent(4, "case.status_changed"), versionEvent(5, "run.finished")]
    const { rerender } = renderHook(
      ({ currentStream }: { currentStream: VersionStreamState }) =>
        useExplorationChain(VERSION_ID, {}, currentStream),
      { initialProps: { currentStream: stream(historical) } },
    )
    await act(async () => {})

    // Only the initial read goes out; the historical events must not schedule a
    // catch-up refresh that would abort it.
    expect(mocks.apiGet).toHaveBeenCalledTimes(1)
    expect(initialSignal?.aborted).toBe(false)
    await act(async () => {
      vi.advanceTimersByTime(500)
    })
    await act(async () => {})
    expect(mocks.apiGet).toHaveBeenCalledTimes(1)
    expect(initialSignal?.aborted).toBe(false)

    await act(async () => {
      resolveInitial?.(snapshot())
    })

    // A genuinely new event after mount coalesces into exactly one refresh.
    rerender({
      currentStream: stream([...historical, versionEvent(6, "issue.updated")]),
    })
    await act(async () => {
      vi.advanceTimersByTime(499)
    })
    expect(mocks.apiGet).toHaveBeenCalledTimes(1)
    await act(async () => {
      vi.advanceTimersByTime(1)
    })
    await act(async () => {})
    expect(mocks.apiGet).toHaveBeenCalledTimes(2)
  })

  it("keeps the previous snapshot and reports the error when a refresh fails", async () => {
    const first = snapshot()
    mocks.apiGet.mockResolvedValueOnce(first).mockRejectedValueOnce(new Error("读取链路失败"))

    const { result } = renderHook(() => useExplorationChain(VERSION_ID, {}, stream()))
    await waitFor(() => expect(result.current.data).not.toBeNull())

    await act(async () => {
      await result.current.refresh()
    })

    expect(result.current.data).toEqual(first)
    expect(result.current.error).toContain("读取链路失败")
    expect(result.current.loading).toBe(false)
  })

  it("returns the truncation flags and the real counts, including pending/running", async () => {
    mocks.apiGet.mockResolvedValue(
      snapshot({
        counts: {
          requirements: 4,
          cases: 42,
          results: 43,
          issues: 2,
          byResultStatus: {
            pending: 3,
            running: 4,
            passed: 5,
            failed: 6,
            blocked: 0,
            inconclusive: 0,
            skipped: 0,
            interrupted: 0,
          },
          byIssueSeverity: { blocker: 0, major: 1, minor: 1, info: 0 },
          uncoveredRequirements: 1,
          revalidationCases: 1,
          supplementCases: 2,
        },
        truncated: { requirements: false, cases: true, results: true, issues: false },
      }),
    )

    const { result } = renderHook(() => useExplorationChain(VERSION_ID, {}, stream()))
    await waitFor(() => expect(result.current.data).not.toBeNull())

    expect(result.current.data?.truncated).toEqual({
      requirements: false,
      cases: true,
      results: true,
      issues: false,
    })
    expect(result.current.data?.counts.cases).toBe(42)
    expect(result.current.data?.counts.byResultStatus.pending).toBe(3)
    expect(result.current.data?.counts.byResultStatus.running).toBe(4)
  })

  it("aborts the in-flight request on unmount instead of writing state", async () => {
    let captured: AbortSignal | undefined
    let resolveRequest: ((value: ChainSnapshot) => void) | undefined
    mocks.apiGet.mockImplementation(
      (_path: string, init?: { signal?: AbortSignal }) =>
        new Promise<ChainSnapshot>((resolve) => {
          captured = init?.signal
          resolveRequest = resolve
        }),
    )

    const { unmount } = renderHook(() => useExplorationChain(VERSION_ID, {}, stream()))
    await waitFor(() => expect(captured).toBeDefined())
    expect(captured?.aborted).toBe(false)

    unmount()

    expect(captured?.aborted).toBe(true)
    await act(async () => {
      resolveRequest?.(snapshot())
    })
  })
})
