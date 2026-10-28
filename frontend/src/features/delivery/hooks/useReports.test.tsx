import { cleanup, renderHook, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { VersionEvent } from "@/features/versions/types"

import type { ReportBatchSummary, ReportDetail, ReportSummary } from "../types"

const mocks = vi.hoisted(() => ({
  listReports: vi.fn(),
  getReport: vi.fn(),
  listReportBatches: vi.fn(),
}))

vi.mock("../api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api")>()
  return {
    ...actual,
    listReports: mocks.listReports,
    getReport: mocks.getReport,
    listReportBatches: mocks.listReportBatches,
  }
})

vi.mock("@/features/versions/useVersionStream", () => ({
  useVersionStream: vi.fn(() => ({ events: [], status: "closed", reconnect: vi.fn() })),
}))

import { useReport, useReportBatches, useReports } from "./useReports"

const VERSION_ID = "1844674407370955161"
const BATCH_ID = "1844674407370955162"
const REPORT_ID = "1844674407370955163"

type StreamState = { events: VersionEvent[]; status: "closed" | "live" | "reconnecting" }

function event(seq: number, type: string): VersionEvent {
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
    summary: "",
    detail: {},
    refs: { evidence: [], case: [], requirement: [], code: [] },
  }
}

function reportSummary(overrides: Partial<ReportSummary> = {}): ReportSummary {
  return {
    id: REPORT_ID,
    versionId: VERSION_ID,
    batchId: BATCH_ID,
    revision: 2,
    status: "published",
    title: "第 2 批报告",
    inputFingerprint: "a".repeat(64),
    markdown: {
      sha256: "b".repeat(64),
      sizeBytes: 2048,
      downloadPath: `/api/versions/${VERSION_ID}/reports/${REPORT_ID}/export?format=markdown`,
    },
    staleReason: "",
    publishedAt: "2026-10-28T00:01:00Z",
    createdAt: "2026-10-28T00:00:00Z",
    updatedAt: "2026-10-28T00:01:00Z",
    ...overrides,
  }
}

function reportDetail(overrides: Partial<ReportDetail> = {}): ReportDetail {
  return {
    ...reportSummary(),
    batchNo: 2,
    batchKind: "incremental",
    content: {
      schema: "eztest.report.v1",
      version: {
        id: VERSION_ID,
        name: "示例版本",
        label: "",
        status: "completed",
        codeBaselineCommit: "",
      },
      batch: {
        id: BATCH_ID,
        batchNo: 2,
        kind: "incremental",
        status: "done",
        baselineHash: "c".repeat(64),
        graphHash: "d".repeat(64),
        codeBaseCommit: "",
        headCommit: "",
        startedAt: "2026-10-28T00:00:00Z",
        finishedAt: "2026-10-28T00:00:30Z",
      },
      summary: {
        caseCount: 2,
        passed: 2,
        failed: 0,
        blocked: 0,
        inconclusive: 0,
        skipped: 0,
        interrupted: 0,
        flakyCount: 0,
      },
      cases: [],
      requirements: [],
      issues: [],
      risks: [],
      degraded: false,
      appendix: { environment: "dev", system: "示例", commit: "", generatedAt: "" },
    },
    ...overrides,
  }
}

const STREAM: StreamState = { events: [], status: "live" }

describe("useReports", () => {
  beforeEach(() => {
    mocks.listReports.mockReset()
    mocks.getReport.mockReset()
    mocks.listReportBatches.mockReset()
  })

  afterEach(() => {
    cleanup()
  })

  it("loads the reports of one batch", async () => {
    mocks.listReports.mockResolvedValueOnce({
      items: [reportSummary()],
      nextCursor: "",
      hasMore: false,
    })

    const { result } = renderHook(() => useReports(VERSION_ID, { batchId: BATCH_ID }, STREAM))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.reports[0]?.id).toBe(REPORT_ID)
    expect(mocks.listReports).toHaveBeenCalledWith(
      VERSION_ID,
      { batchId: BATCH_ID },
      { signal: expect.any(AbortSignal) },
    )
  })

  it("refreshes on report.* events and ignores issue events", async () => {
    mocks.listReports.mockResolvedValue({ items: [], nextCursor: "", hasMore: false })

    let stream: StreamState = { events: [], status: "live" }
    const { rerender } = renderHook(() => useReports(VERSION_ID, {}, stream))
    await waitFor(() => expect(mocks.listReports).toHaveBeenCalledTimes(1))

    stream = { events: [event(1, "report.published")], status: "live" }
    rerender()
    await waitFor(() => expect(mocks.listReports).toHaveBeenCalledTimes(2))

    stream = {
      events: [event(1, "report.published"), event(2, "issue.created")],
      status: "live",
    }
    rerender()
    expect(mocks.listReports).toHaveBeenCalledTimes(2)
  })

  it("loads a report detail and refreshes it when the report goes stale", async () => {
    mocks.getReport.mockResolvedValue(reportDetail())

    let stream: StreamState = { events: [], status: "live" }
    const { result, rerender } = renderHook(() => useReport(VERSION_ID, REPORT_ID, stream))
    await waitFor(() => expect(result.current.report?.revision).toBe(2))
    expect(mocks.getReport).toHaveBeenCalledWith(VERSION_ID, REPORT_ID, {
      signal: expect.any(AbortSignal),
    })

    stream = { events: [event(1, "report.stale")], status: "live" }
    rerender()
    await waitFor(() => expect(mocks.getReport).toHaveBeenCalledTimes(2))
  })

  it("lists the version batches for the report selector", async () => {
    const batches: ReportBatchSummary[] = [
      { id: BATCH_ID, batchNo: 2, status: "done", kind: "incremental" },
    ]
    mocks.listReportBatches.mockResolvedValueOnce({
      items: batches,
      nextCursor: "",
      hasMore: false,
    })

    const { result } = renderHook(() => useReportBatches(VERSION_ID))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.batches.map((batch) => batch.id)).toEqual([BATCH_ID])
    expect(mocks.listReportBatches).toHaveBeenCalledWith(VERSION_ID, {
      signal: expect.any(AbortSignal),
    })
  })
})
