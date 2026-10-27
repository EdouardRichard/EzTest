import { act, cleanup, renderHook } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { VersionEvent } from "@/features/versions/types"

import type { Analysis } from "./types"

const mocks = vi.hoisted(() => ({
  getLatestAnalysis: vi.fn(),
  useVersionStream: vi.fn(),
}))

vi.mock("./api", () => ({
  getLatestAnalysis: mocks.getLatestAnalysis,
}))

vi.mock("@/features/versions/useVersionStream", () => ({
  useVersionStream: mocks.useVersionStream,
}))

import {
  ANALYSIS_POLL_INTERVAL_MS,
  ANALYSIS_POLL_MAX_ATTEMPTS,
  useAnalysisPolling,
} from "./useAnalysisPolling"

const VERSION_ID = "1844674407370955161"
const ANALYSIS_ID = "1844674407370955162"

function analysisFixture(overrides: Partial<Analysis> = {}): Analysis {
  return {
    id: ANALYSIS_ID,
    versionId: VERSION_ID,
    repoId: 3,
    baseCommit: "0123456789abcdef0123456789abcdef01234567",
    headCommit: "89abcdef0123456789abcdef0123456789abcdef",
    status: "done",
    degraded: false,
    analysisVersion: 1,
    model: "deepseek-chat",
    promptVersion: "p4.1",
    changeSetId: null,
    changeSummary: "变更摘要",
    stats: null,
    factVersions: [],
    candidates: [],
    unmapped: [],
    errors: [],
    createdBy: 1,
    createdAt: "2026-10-28T01:00:00Z",
    startedAt: "2026-10-28T01:00:00Z",
    finishedAt: "2026-10-28T01:02:03Z",
    ...overrides,
  }
}

function runningAnalysis(): Analysis {
  return analysisFixture({ status: "running", finishedAt: null })
}

function codeEvent(seq: number): VersionEvent {
  return {
    seq,
    channel: `version:${VERSION_ID}`,
    type: "code.changed",
    version_id: VERSION_ID,
    batch_id: null,
    case_id: null,
    step_id: null,
    agent_role: null,
    session_id: null,
    ts: "2026-10-28T01:00:00Z",
    summary: "code event",
    detail: {},
    refs: { evidence: [], case: [], requirement: [], code: [] },
  }
}

async function flushPromises(): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0)
  })
}

async function advanceTimers(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

/** Drives the hook past its 60-poll cap while the analysis stays running. */
async function exhaustPolling() {
  mocks.getLatestAnalysis.mockResolvedValue(runningAnalysis())
  const rendered = renderHook(() => useAnalysisPolling(VERSION_ID))
  await flushPromises()
  await advanceTimers(ANALYSIS_POLL_INTERVAL_MS * (ANALYSIS_POLL_MAX_ATTEMPTS + 1))
  expect(rendered.result.current.exhausted).toBe(true)
  expect(rendered.result.current.analysis?.status).toBe("running")
  return rendered
}

describe("useAnalysisPolling exhausted hint", () => {
  beforeEach(() => {
    mocks.getLatestAnalysis.mockReset()
    mocks.useVersionStream.mockReset()
    mocks.useVersionStream.mockReturnValue({ events: [], status: "live", reconnect: vi.fn() })
  })

  afterEach(() => {
    cleanup()
  })

  it("clears exhausted when a manual refresh returns a terminal analysis", async () => {
    vi.useFakeTimers()
    try {
      const rendered = await exhaustPolling()

      // A refresh that still returns a running analysis keeps the hint.
      mocks.getLatestAnalysis.mockResolvedValue(runningAnalysis())
      await act(async () => {
        await rendered.result.current.refresh()
      })
      expect(rendered.result.current.exhausted).toBe(true)

      mocks.getLatestAnalysis.mockResolvedValue(analysisFixture())
      await act(async () => {
        await rendered.result.current.refresh()
      })
      expect(rendered.result.current.analysis?.status).toBe("done")
      expect(rendered.result.current.exhausted).toBe(false)
    } finally {
      vi.useRealTimers()
    }
  })

  it("clears exhausted when a code.* SSE refresh returns a terminal analysis", async () => {
    vi.useFakeTimers()
    try {
      const rendered = await exhaustPolling()

      mocks.getLatestAnalysis.mockResolvedValue(analysisFixture())
      mocks.useVersionStream.mockReturnValue({
        events: [codeEvent(11)],
        status: "live",
        reconnect: vi.fn(),
      })
      rendered.rerender()
      await flushPromises()
      await advanceTimers(300)
      await flushPromises()

      expect(rendered.result.current.analysis?.status).toBe("done")
      expect(rendered.result.current.exhausted).toBe(false)
    } finally {
      vi.useRealTimers()
    }
  })
})
