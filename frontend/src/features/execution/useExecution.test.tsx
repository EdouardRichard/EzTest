import { act, cleanup, renderHook } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { VersionEvent } from "@/features/versions/types"
import { ApiError } from "@/lib/api-client"
import type { SSEStatus } from "@/lib/sse-client"

import type { Batch, CaseRun, ReportView } from "./types"

const mocks = vi.hoisted(() => ({
  listBatches: vi.fn(),
  getBatch: vi.fn(),
  pauseBatch: vi.fn(),
  resumeBatch: vi.fn(),
  cancelBatch: vi.fn(),
  listCaseRuns: vi.fn(),
  getCaseRun: vi.fn(),
  rerunCase: vi.fn(),
  createBatch: vi.fn(),
  getReport: vi.fn(),
  exportReport: vi.fn(),
  listExecutionSessions: vi.fn(),
  useVersionStream: vi.fn(),
}))

vi.mock("./api", () => ({
  listBatches: mocks.listBatches,
  getBatch: mocks.getBatch,
  pauseBatch: mocks.pauseBatch,
  resumeBatch: mocks.resumeBatch,
  cancelBatch: mocks.cancelBatch,
  listCaseRuns: mocks.listCaseRuns,
  getCaseRun: mocks.getCaseRun,
  rerunCase: mocks.rerunCase,
  createBatch: mocks.createBatch,
  getReport: mocks.getReport,
  exportReport: mocks.exportReport,
  listExecutionSessions: mocks.listExecutionSessions,
}))

vi.mock("@/features/versions/useVersionStream", () => ({
  useVersionStream: mocks.useVersionStream,
}))

import {
  useBatch,
  useBatches,
  useCaseRun,
  useCaseRuns,
  useExecution,
  useExecutionActions,
  useReport,
} from "./hooks/useExecution"

const VERSION_ID = "1844674407370955161"
const BATCH_ID = "1844674407370955162"
const OTHER_BATCH_ID = "1844674407370955163"
const CASE_RUN_ID = "1844674407370955164"
const CASE_ID = "1844674407370955165"

type StreamState = {
  events: VersionEvent[]
  status: SSEStatus
  reconnect: () => void
}

let streamState: StreamState

function batchFixture(overrides: Partial<Batch> = {}): Batch {
  return {
    id: BATCH_ID,
    version_id: VERSION_ID,
    batch_no: 1,
    status: "running",
    paused: false,
    pause_mode: "",
    pause_reason: "",
    baseline_hash: "a".repeat(64),
    code_baseline_commit: null,
    head_commit: null,
    counts: {
      total: 2,
      passed: 1,
      failed: 0,
      blocked: 0,
      inconclusive: 0,
      skipped: 0,
      interrupted: 0,
    },
    created_by: "1",
    started_at: "2026-10-28T00:00:00Z",
    finished_at: null,
    failure_reason: "",
    ...overrides,
  }
}

function caseRunFixture(overrides: Partial<CaseRun> = {}): CaseRun {
  return {
    id: CASE_RUN_ID,
    batch_id: BATCH_ID,
    case_node_id: CASE_ID,
    case_revision: 3,
    attempt: 1,
    status: "passed",
    failure_class: null,
    failure_reason: "",
    summary: "通过",
    duration_ms: 1200,
    title: "下单成功",
    session: { id: "1844674407370955166", account: "buyer", state: "valid" },
    checkpoint: { completed_seqs: [1, 2], cleanup_required: false, interrupted_step_seq: null },
    steps: [],
    assertions: [],
    evidence: [],
    pending_takeover: null,
    ...overrides,
  }
}

function reportFixture(): ReportView {
  return {
    batch: {
      id: BATCH_ID,
      batch_no: 1,
      status: "done",
      baseline_hash: "a".repeat(64),
      code_baseline_commit: null,
    },
    summary: {
      case_count: 2,
      by_status: { passed: 1, failed: 1 },
      by_failure_class: { application: 1 },
      flaky_case_ids: [],
      duration_ms: 12000,
      evidence_count: 4,
    },
    cases: [],
    environment: { system: "商城", environment: "测试", base_url: "https://example.test" },
    generated_at: "2026-10-28T01:00:00Z",
  }
}

function apiError(reason: string): ApiError {
  return new ApiError({
    status: 409,
    code: "conflict",
    message: "request rejected",
    details: { reason },
  })
}

function event(seq: number, type = "batch.started"): VersionEvent {
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
    ts: "2026-10-28T00:00:00Z",
    summary: `事件 ${seq}`,
    detail: {},
    refs: { evidence: [], case: [], requirement: [], code: [] },
  }
}

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => {
    resolve = done
  })
  return { promise, resolve }
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

describe("execution hooks", () => {
  beforeEach(() => {
    vi.useFakeTimers()
    for (const fn of Object.values(mocks)) fn.mockReset()
    streamState = { events: [], status: "live", reconnect: vi.fn() }
    mocks.useVersionStream.mockImplementation(() => streamState)
  })

  afterEach(() => {
    cleanup()
    vi.useRealTimers()
  })

  it("useBatches loads a page, exposes the stream status and refreshes", async () => {
    const first = batchFixture()
    const second = batchFixture({ status: "done" })
    mocks.listBatches
      .mockResolvedValueOnce({ items: [first], nextCursor: "c2", hasMore: true })
      .mockResolvedValueOnce({ items: [second], nextCursor: "", hasMore: false })

    const rendered = renderHook(() => useBatches(VERSION_ID))
    expect(rendered.result.current.loading).toBe(true)

    await flushPromises()

    expect(mocks.useVersionStream).toHaveBeenCalledWith(VERSION_ID)
    expect(mocks.listBatches).toHaveBeenCalledWith(
      VERSION_ID,
      {},
      {
        signal: expect.any(AbortSignal),
      },
    )
    expect(rendered.result.current.batches).toEqual([first])
    expect(rendered.result.current.nextCursor).toBe("c2")
    expect(rendered.result.current.hasMore).toBe(true)
    expect(rendered.result.current.loading).toBe(false)
    expect(rendered.result.current.error).toBe("")
    expect(rendered.result.current.streamStatus).toBe("live")

    await act(async () => {
      await rendered.result.current.refresh()
    })

    expect(mocks.listBatches).toHaveBeenCalledTimes(2)
    expect(rendered.result.current.batches).toEqual([second])
    expect(rendered.result.current.hasMore).toBe(false)
  })

  it("useBatches surfaces the mapped Chinese error", async () => {
    mocks.listBatches.mockRejectedValueOnce(apiError("batch_active"))

    const rendered = renderHook(() => useBatches(VERSION_ID))
    await flushPromises()

    expect(rendered.result.current.batches).toEqual([])
    expect(rendered.result.current.loading).toBe(false)
    expect(rendered.result.current.error).toContain("已有执行中的批次")
  })

  it("useBatches refreshes after a matching execution event at most once per window", async () => {
    mocks.listBatches
      .mockResolvedValueOnce({ items: [batchFixture()], nextCursor: "", hasMore: false })
      .mockResolvedValueOnce({
        items: [batchFixture({ paused: true })],
        nextCursor: "",
        hasMore: false,
      })

    const rendered = renderHook(() => useBatches(VERSION_ID))
    await flushPromises()
    expect(mocks.listBatches).toHaveBeenCalledTimes(1)

    pushEvents(rendered, [event(1, "batch.paused")])
    await flushPromises()
    await advance(500)
    await flushPromises()

    expect(mocks.listBatches).toHaveBeenCalledTimes(2)
    expect(rendered.result.current.batches[0]?.paused).toBe(true)
  })

  it("useBatch loads the detail and ignores a late response from the previous batch", async () => {
    const stale = deferred<Batch>()
    const current = batchFixture({ id: OTHER_BATCH_ID })
    mocks.getBatch.mockReturnValueOnce(stale.promise).mockResolvedValueOnce(current)

    const rendered = renderHook(
      ({ batchId }: { batchId: string }) => useBatch(VERSION_ID, batchId),
      {
        initialProps: { batchId: BATCH_ID },
      },
    )
    await flushPromises()

    rendered.rerender({ batchId: OTHER_BATCH_ID })
    await flushPromises()
    expect(rendered.result.current.batch).toEqual(current)

    stale.resolve(batchFixture())
    await flushPromises()

    expect(rendered.result.current.batch).toEqual(current)
    expect(mocks.getBatch).toHaveBeenLastCalledWith(
      VERSION_ID,
      OTHER_BATCH_ID,
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    )
  })

  it("useCaseRuns normalizes missing detail sections to empty arrays", async () => {
    const raw = {
      ...caseRunFixture(),
      steps: undefined,
      assertions: undefined,
      evidence: undefined,
      checkpoint: undefined,
    } as unknown as CaseRun
    mocks.listCaseRuns.mockResolvedValueOnce({ items: [raw], nextCursor: "", hasMore: false })

    const rendered = renderHook(() => useCaseRuns(VERSION_ID, BATCH_ID))
    await flushPromises()

    expect(mocks.listCaseRuns).toHaveBeenCalledWith(
      VERSION_ID,
      BATCH_ID,
      {},
      {
        signal: expect.any(AbortSignal),
      },
    )
    expect(rendered.result.current.caseRuns[0]?.steps).toEqual([])
    expect(rendered.result.current.caseRuns[0]?.assertions).toEqual([])
    expect(rendered.result.current.caseRuns[0]?.evidence).toEqual([])
    expect(rendered.result.current.caseRuns[0]?.checkpoint).toEqual({
      completed_seqs: [],
      cleanup_required: false,
      interrupted_step_seq: null,
    })
  })

  it("useCaseRuns refreshes the page and maps a failure", async () => {
    const failed = caseRunFixture({ status: "failed", failure_class: "application" })
    const passed = caseRunFixture({ status: "passed" })
    mocks.listCaseRuns
      .mockResolvedValueOnce({ items: [failed], nextCursor: "", hasMore: false })
      .mockRejectedValueOnce(apiError("batch_finished"))
      .mockResolvedValueOnce({ items: [passed], nextCursor: "", hasMore: false })

    const rendered = renderHook(() => useCaseRuns(VERSION_ID, BATCH_ID))
    await flushPromises()
    expect(rendered.result.current.caseRuns).toEqual([failed])

    await act(async () => {
      await rendered.result.current.refresh()
    })
    expect(rendered.result.current.caseRuns).toEqual([failed])
    expect(rendered.result.current.error).toContain("批次已结束")

    await act(async () => {
      await rendered.result.current.refresh()
    })
    expect(rendered.result.current.caseRuns).toEqual([passed])
    expect(rendered.result.current.error).toBe("")
  })

  it("useCaseRun loads the full detail, then recovers from a terminal conflict", async () => {
    const detail = caseRunFixture({
      steps: [
        {
          seq: 1,
          action: "点击下单",
          expected: "订单创建",
          actual: "订单创建",
          status: "passed",
          evidence_ids: ["1844674407370955167"],
        },
      ],
    })
    mocks.getCaseRun
      .mockRejectedValueOnce(apiError("case_run_terminal"))
      .mockResolvedValueOnce(detail)

    const rendered = renderHook(() => useCaseRun(VERSION_ID, BATCH_ID, CASE_RUN_ID))
    await flushPromises()

    expect(mocks.getCaseRun).toHaveBeenCalledWith(VERSION_ID, BATCH_ID, CASE_RUN_ID, {
      signal: expect.any(AbortSignal),
    })
    expect(rendered.result.current.caseRun).toBeNull()
    expect(rendered.result.current.error).toContain("该用例已处于终态")

    await act(async () => {
      await rendered.result.current.refresh()
    })

    expect(rendered.result.current.caseRun).toEqual(detail)
    expect(rendered.result.current.caseRun?.steps).toHaveLength(1)
    expect(rendered.result.current.error).toBe("")
  })

  it("useReport loads the report and exposes a retryable error state", async () => {
    const report = reportFixture()
    mocks.getReport.mockResolvedValueOnce(report).mockRejectedValueOnce(apiError("export_failed"))

    const rendered = renderHook(() => useReport(VERSION_ID, BATCH_ID))
    await flushPromises()

    expect(mocks.getReport).toHaveBeenCalledWith(VERSION_ID, BATCH_ID, {
      signal: expect.any(AbortSignal),
    })
    expect(rendered.result.current.report).toEqual(report)

    await act(async () => {
      await rendered.result.current.refresh()
    })

    // A failed refresh keeps the last good report; only the error state changes.
    expect(rendered.result.current.report).toEqual(report)
    expect(rendered.result.current.error).toContain("报告导出失败")
  })

  it("useExecutionActions pauses with the requested mode and refreshes, without optimistic writes", async () => {
    const onChanged = vi.fn()
    mocks.pauseBatch.mockResolvedValueOnce(batchFixture({ paused: true }))

    const rendered = renderHook(() => useExecutionActions(VERSION_ID, { onChanged }))

    let ok = false
    await act(async () => {
      ok = await rendered.result.current.pause(BATCH_ID, "immediate")
    })

    expect(ok).toBe(true)
    expect(mocks.pauseBatch).toHaveBeenCalledWith(VERSION_ID, BATCH_ID, "immediate")
    expect(onChanged).toHaveBeenCalledTimes(1)
    expect(rendered.result.current.pending).toBe("")
    expect(rendered.result.current.error).toBe("")
  })

  it("useExecutionActions covers create/resume/cancel/rerun/export and reports failures", async () => {
    const onChanged = vi.fn()
    const created = batchFixture({ id: OTHER_BATCH_ID })
    mocks.createBatch.mockResolvedValueOnce(created)
    mocks.resumeBatch.mockRejectedValueOnce(apiError("not_paused"))
    mocks.cancelBatch.mockResolvedValueOnce(batchFixture({ status: "cancelled" }))
    mocks.rerunCase.mockResolvedValueOnce(caseRunFixture({ attempt: 2 }))
    mocks.exportReport.mockResolvedValueOnce({ blob: new Blob(["md"]), filename: "report.md" })

    const rendered = renderHook(() => useExecutionActions(VERSION_ID, { onChanged }))
    let createdBatch: Batch | null = null
    let rerun: CaseRun | null = null
    let download: { blob: Blob; filename: string } | null = null

    await act(async () => {
      createdBatch = await rendered.result.current.createBatch({ caseIds: [CASE_ID] })
    })
    expect(createdBatch).toEqual(created)

    await act(async () => {
      expect(await rendered.result.current.resume(BATCH_ID)).toBe(false)
    })
    expect(rendered.result.current.error).toContain("未暂停")

    await act(async () => {
      expect(await rendered.result.current.cancel(BATCH_ID)).toBe(true)
    })
    await act(async () => {
      rerun = await rendered.result.current.rerun(BATCH_ID, CASE_RUN_ID)
    })
    await act(async () => {
      download = await rendered.result.current.exportReport(BATCH_ID)
    })

    expect(mocks.cancelBatch).toHaveBeenCalledWith(VERSION_ID, BATCH_ID)
    expect(mocks.rerunCase).toHaveBeenCalledWith(VERSION_ID, BATCH_ID, CASE_RUN_ID)
    expect(mocks.exportReport).toHaveBeenCalledWith(VERSION_ID, BATCH_ID, {})
    expect(rerun).toEqual(caseRunFixture({ attempt: 2 }))
    expect(download).toEqual({ blob: expect.any(Blob), filename: "report.md" })
    expect(onChanged).toHaveBeenCalledTimes(4)
  })

  it("useExecution composes one stream with the active batch and actions", async () => {
    const running = batchFixture()
    mocks.listBatches.mockResolvedValueOnce({
      items: [batchFixture({ id: OTHER_BATCH_ID, status: "done" }), running],
      nextCursor: "",
      hasMore: false,
    })

    const rendered = renderHook(() => useExecution(VERSION_ID))
    await flushPromises()

    expect(rendered.result.current.activeBatch).toEqual(running)
    expect(rendered.result.current.streamStatus).toBe("live")
    expect(rendered.result.current.batches).toHaveLength(2)
    expect(mocks.useVersionStream).toHaveBeenCalledWith(VERSION_ID)
  })
})
