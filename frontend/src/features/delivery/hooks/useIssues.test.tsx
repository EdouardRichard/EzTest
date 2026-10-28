import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { VersionEvent } from "@/features/versions/types"
import { ApiError } from "@/lib/api-client"

import type { IssueDetail, IssueSummary } from "../types"

const mocks = vi.hoisted(() => ({
  listIssues: vi.fn(),
  getIssue: vi.fn(),
}))

vi.mock("../api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api")>()
  return { ...actual, listIssues: mocks.listIssues, getIssue: mocks.getIssue }
})

vi.mock("@/features/versions/useVersionStream", () => ({
  useVersionStream: vi.fn(() => ({ events: [], status: "closed", reconnect: vi.fn() })),
}))

import { useIssue, useIssues } from "./useIssues"

const VERSION_ID = "1844674407370955161"
const ISSUE_ID = "1844674407370955162"
const OTHER_ISSUE_ID = "1844674407370955163"

type StreamState = { events: VersionEvent[]; status: "closed" | "live" | "reconnecting" }

function event(seq: number, type: string, overrides: Partial<VersionEvent> = {}): VersionEvent {
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
    ts: "2026-10-28T00:00:00Z",
    summary: "",
    detail: {},
    refs: { evidence: [], case: [], requirement: [], code: [] },
    ...overrides,
  }
}

function issueSummary(overrides: Partial<IssueSummary> = {}): IssueSummary {
  return {
    id: ISSUE_ID,
    versionId: VERSION_ID,
    batchId: "1844674407370955164",
    caseRunId: null,
    caseNodeId: null,
    stepResultId: null,
    source: "ai",
    category: "defect",
    severity: "major",
    status: "open",
    confidence: "high",
    failureClass: "application",
    title: "登录失败",
    rerunSuggestion: "",
    recurrenceCount: 0,
    revision: 1,
    refs: [],
    codeRefs: [],
    dismissedReason: "",
    createdBy: "1",
    confirmedBy: null,
    confirmedAt: null,
    dismissedBy: null,
    dismissedAt: null,
    createdAt: "2026-10-28T00:00:00Z",
    updatedAt: "2026-10-28T00:00:00Z",
    ...overrides,
  }
}

function issueDetail(overrides: Partial<IssueDetail> = {}): IssueDetail {
  return {
    ...issueSummary(),
    description: "期望与实际的差异",
    reproSteps: "1. 打开登录页",
    expected: "登录成功",
    actual: "500",
    case: null,
    requirement: null,
    batch: null,
    ...overrides,
  }
}

const STREAM: StreamState = { events: [], status: "live" }

describe("useIssues", () => {
  beforeEach(() => {
    mocks.listIssues.mockReset()
    mocks.getIssue.mockReset()
  })

  afterEach(() => {
    cleanup()
  })

  it("loads one page with filters and keeps IDs as strings", async () => {
    mocks.listIssues.mockResolvedValueOnce({
      items: [issueSummary()],
      nextCursor: "c2",
      hasMore: true,
    })

    const { result } = renderHook(() =>
      useIssues(VERSION_ID, { status: "open", severity: "major" }, STREAM),
    )

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.issues).toHaveLength(1)
    expect(result.current.issues[0]?.id).toBe(ISSUE_ID)
    expect(result.current.hasMore).toBe(true)
    expect(mocks.listIssues).toHaveBeenCalledWith(
      VERSION_ID,
      { status: "open", severity: "major" },
      { signal: expect.any(AbortSignal) },
    )
  })

  it("refetches the first page when the filters change", async () => {
    mocks.listIssues.mockResolvedValue({ items: [], nextCursor: "", hasMore: false })

    const { rerender } = renderHook(
      ({ status }: { status: "open" | "confirmed" }) => useIssues(VERSION_ID, { status }, STREAM),
      { initialProps: { status: "open" } },
    )
    await waitFor(() => expect(mocks.listIssues).toHaveBeenCalledTimes(1))

    rerender({ status: "confirmed" })
    await waitFor(() => expect(mocks.listIssues).toHaveBeenCalledTimes(2))
    expect(mocks.listIssues).toHaveBeenLastCalledWith(
      VERSION_ID,
      { status: "confirmed" },
      { signal: expect.any(AbortSignal) },
    )
  })

  it("appends the next cursor page without replacing the loaded rows", async () => {
    mocks.listIssues
      .mockResolvedValueOnce({ items: [issueSummary()], nextCursor: "c2", hasMore: true })
      .mockResolvedValueOnce({
        items: [issueSummary({ id: "1844674407370955169" })],
        nextCursor: "",
        hasMore: false,
      })

    const { result } = renderHook(() => useIssues(VERSION_ID, {}, STREAM))
    await waitFor(() => expect(result.current.issues).toHaveLength(1))

    await act(async () => {
      await result.current.loadMore()
    })

    expect(result.current.issues.map((item) => item.id)).toEqual([ISSUE_ID, "1844674407370955169"])
    expect(result.current.hasMore).toBe(false)
    expect(mocks.listIssues).toHaveBeenLastCalledWith(
      VERSION_ID,
      { cursor: "c2" },
      { signal: expect.any(AbortSignal) },
    )
  })

  it("refreshes on issue.* events but ignores unrelated batch events", async () => {
    mocks.listIssues.mockResolvedValue({ items: [], nextCursor: "", hasMore: false })

    let stream: StreamState = { events: [], status: "live" }
    const { rerender } = renderHook(() => useIssues(VERSION_ID, {}, stream))
    await waitFor(() => expect(mocks.listIssues).toHaveBeenCalledTimes(1))

    stream = { events: [event(1, "issue.created")], status: "live" }
    rerender()
    await waitFor(() => expect(mocks.listIssues).toHaveBeenCalledTimes(2))

    stream = { events: [event(1, "issue.created"), event(2, "batch.created")], status: "live" }
    rerender()
    expect(mocks.listIssues).toHaveBeenCalledTimes(2)
  })

  it("surfaces an API error while stopping the spinner", async () => {
    mocks.listIssues.mockRejectedValueOnce(
      new ApiError({ status: 403, code: "forbidden", message: "forbidden" }),
    )

    const { result } = renderHook(() => useIssues(VERSION_ID, {}, STREAM))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toContain("无权限")
    expect(result.current.issues).toEqual([])
  })

  it("loads one issue detail and refreshes it on a matching issue update", async () => {
    mocks.getIssue.mockResolvedValue(issueDetail())

    let stream: StreamState = { events: [], status: "live" }
    const { result, rerender } = renderHook(() => useIssue(VERSION_ID, ISSUE_ID, stream))
    await waitFor(() => expect(result.current.issue?.id).toBe(ISSUE_ID))
    expect(mocks.getIssue).toHaveBeenCalledWith(VERSION_ID, ISSUE_ID, {
      signal: expect.any(AbortSignal),
    })

    stream = { events: [event(1, "issue.updated")], status: "live" }
    rerender()
    await waitFor(() => expect(mocks.getIssue).toHaveBeenCalledTimes(2))
  })

  it("does not refresh an unrelated issue detail on batch events", async () => {
    mocks.getIssue.mockResolvedValue(issueDetail({ id: OTHER_ISSUE_ID }))

    let stream: StreamState = { events: [], status: "live" }
    const { rerender } = renderHook(() => useIssue(VERSION_ID, OTHER_ISSUE_ID, stream))
    await waitFor(() => expect(mocks.getIssue).toHaveBeenCalledTimes(1))

    stream = { events: [event(1, "batch.finished")], status: "live" }
    rerender()
    expect(mocks.getIssue).toHaveBeenCalledTimes(1)
  })
})
