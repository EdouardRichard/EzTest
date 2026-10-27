import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { VersionEvent } from "@/features/versions/types"
import { ApiError } from "@/lib/api-client"

import type { Analysis, AnalysisStats, ChangedFile, ChangeList, FileDiff, IndexRun } from "./types"

const mocks = vi.hoisted(() => ({
  getVersionChanges: vi.fn(),
  getLatestIndexRun: vi.fn(),
  getLatestAnalysis: vi.fn(),
  startAnalysis: vi.fn(),
  getFileDiff: vi.fn(),
  useVersionStream: vi.fn(),
}))

vi.mock("./api", () => ({
  getVersionChanges: mocks.getVersionChanges,
  getLatestIndexRun: mocks.getLatestIndexRun,
  getLatestAnalysis: mocks.getLatestAnalysis,
  startAnalysis: mocks.startAnalysis,
  getFileDiff: mocks.getFileDiff,
}))

vi.mock("@/features/versions/useVersionStream", () => ({
  useVersionStream: mocks.useVersionStream,
}))

import { CodeChangesTab } from "./CodeChangesTab"

/** A snowflake-shaped ID that must stay a string end to end (spec §7.1). */
const VERSION_ID = "1844674407370955161"
const ANALYSIS_ID = "1844674407370955162"
const REPO_ID = 3
const BASE = "0123456789abcdef0123456789abcdef01234567"
const HEAD = "89abcdef0123456789abcdef0123456789abcdef"

const TEXT_FILE: ChangedFile = {
  path: "src/api/refund.ts",
  oldPath: null,
  status: "M",
  additions: 40,
  deletions: 2,
  binary: false,
  headBlobHash: "git-sha1:1111111111111111111111111111111111111111",
}

const BINARY_FILE: ChangedFile = {
  path: "assets/logo.png",
  oldPath: null,
  status: "M",
  additions: 0,
  deletions: 0,
  binary: true,
  headBlobHash: "git-sha1:2222222222222222222222222222222222222222",
}

const ANALYSIS_STATS: AnalysisStats = {
  changedFiles: 1,
  analyzedFiles: 1,
  cacheHits: 0,
  facts: 4,
  edges: 1,
  contracts: 0,
  evidenceChecks: 2,
  evidenceFailures: 0,
  unmapped: 1,
  invalidFacts: 0,
  duplicateFacts: 0,
  unmatchedContracts: 0,
  truncated: false,
}

function notFound(message = "not found"): ApiError {
  return new ApiError({ status: 404, code: "not_found", message })
}

function analysisFixture(overrides: Partial<Analysis> = {}): Analysis {
  return {
    id: ANALYSIS_ID,
    versionId: VERSION_ID,
    repoId: REPO_ID,
    baseCommit: BASE,
    headCommit: HEAD,
    status: "done",
    degraded: false,
    analysisVersion: 1,
    model: "deepseek-chat",
    promptVersion: "p4.1",
    changeSetId: "1844674407370955163",
    changeSummary: "退款接口新增 reason 参数，订单页调用点同步调整",
    stats: ANALYSIS_STATS,
    factVersions: [{ factKey: "api:POST /api/refund", version: 3 }],
    candidates: [],
    unmapped: [
      {
        path: "src/utils/format.ts",
        status: "M",
        reason: "no_fact_or_anchor",
        suggestion: "人工确认影响",
      },
    ],
    errors: [],
    createdBy: 1,
    createdAt: "2026-10-28T01:00:00Z",
    startedAt: "2026-10-28T01:00:00Z",
    finishedAt: "2026-10-28T01:02:03Z",
    ...overrides,
  }
}

function changesFixture(overrides: Partial<ChangeList> = {}): ChangeList {
  return {
    changeSetId: "1844674407370955163",
    baseCommit: BASE,
    headCommit: HEAD,
    stats: { files: 1, additions: 40, deletions: 2, truncated: false },
    files: [TEXT_FILE],
    nextCursor: null,
    hasMore: false,
    ...overrides,
  }
}

const INDEX_DONE: IndexRun = {
  runId: "1844674407370955164",
  repoId: REPO_ID,
  commit: HEAD,
  status: "done",
  stats: { files: 1200, truncated: false },
  startedAt: "2026-10-28T00:50:00Z",
  finishedAt: "2026-10-28T00:52:03Z",
}

function codeEvent(seq: number, type = "code.changed"): VersionEvent {
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
    ts: "2026-10-28T01:00:00Z",
    summary: "code event",
    detail: {},
    refs: { evidence: [], case: [], requirement: [], code: ["src/api/refund.ts"] },
  }
}

function resetMocks(): void {
  for (const mock of Object.values(mocks)) mock.mockReset()
  mocks.useVersionStream.mockReturnValue({ events: [], status: "live", reconnect: vi.fn() })
}

/** Resolves the initial effect promises of the container. */
async function flushPromises(): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0)
  })
}

function mockReady(
  options: {
    changes?: ChangeList
    index?: IndexRun | ApiError
    analysis?: Analysis | ApiError
  } = {},
): void {
  mocks.getVersionChanges.mockResolvedValue(options.changes ?? changesFixture())
  const index = options.index ?? INDEX_DONE
  if (index instanceof Error) mocks.getLatestIndexRun.mockRejectedValue(index)
  else mocks.getLatestIndexRun.mockResolvedValue(index)
  const analysis = options.analysis ?? analysisFixture()
  if (analysis instanceof Error) mocks.getLatestAnalysis.mockRejectedValue(analysis)
  else mocks.getLatestAnalysis.mockResolvedValue(analysis)
}

function renderTab(props: { repoId?: number | null } = {}) {
  return render(
    <CodeChangesTab
      versionId={VERSION_ID}
      repoId={props.repoId === undefined ? REPO_ID : props.repoId}
    />,
  )
}

describe("CodeChangesTab", () => {
  beforeEach(() => {
    resetMocks()
  })

  afterEach(() => {
    cleanup()
  })

  it("renders the six code tab states", async () => {
    // 1. 未绑定代码仓: a null repo binding needs no API call at all.
    const unbound = renderTab({ repoId: null })
    expect(await screen.findByRole("heading", { name: "未绑定代码仓库" })).toBeTruthy()
    const bindLink = screen.getByRole("link", { name: "前往绑定代码仓库" })
    expect(bindLink.getAttribute("href")).toBe(`/versions/${VERSION_ID}?tab=requirements`)
    expect(mocks.getVersionChanges).not.toHaveBeenCalled()
    unbound.unmount()

    // 2. 未索引: no persisted index run yet (404) gates the change list.
    resetMocks()
    mockReady({ index: notFound("index run not found"), analysis: notFound() })
    const unindexed = renderTab()
    expect(await screen.findByRole("heading", { name: "代码仓库尚未索引" })).toBeTruthy()
    expect(screen.getByRole("button", { name: "刷新索引状态" })).toBeTruthy()
    expect(screen.getByRole("link", { name: "前往代码仓库" }).getAttribute("href")).toBe(
      "/code-repos",
    )
    unindexed.unmount()

    // 3. 无变更: the index is done but the base→head range has no files.
    resetMocks()
    mockReady({
      changes: changesFixture({
        files: [],
        stats: { files: 0, additions: 0, deletions: 0, truncated: false },
      }),
      analysis: notFound(),
    })
    const noChanges = renderTab()
    expect(await screen.findByRole("heading", { name: "无变更" })).toBeTruthy()
    expect(screen.getByRole("button", { name: "刷新变更" })).toBeTruthy()
    noChanges.unmount()

    // 4. 运行中: a queued/running analysis keeps the files visible and polls.
    resetMocks()
    mockReady({ analysis: analysisFixture({ status: "running", finishedAt: null }) })
    const running = renderTab()
    expect(await screen.findByText(/AI 影响分析运行中/)).toBeTruthy()
    expect(screen.getByText("src/api/refund.ts")).toBeTruthy()
    expect(screen.getByRole("button", { name: "刷新" })).toBeTruthy()
    running.unmount()

    // 5. 完成: done without degradation renders the summary and results region.
    resetMocks()
    mockReady({ analysis: analysisFixture() })
    const done = renderTab()
    expect(await screen.findByText("分析完成")).toBeTruthy()
    expect(screen.getByText("退款接口新增 reason 参数，订单页调用点同步调整")).toBeTruthy()
    expect(screen.getByRole("button", { name: "重新分析" })).toBeTruthy()
    done.unmount()

    // 6. 降级: done + degraded shows the T0-only banner instead of hiding data.
    resetMocks()
    mockReady({ analysis: analysisFixture({ degraded: true }) })
    const degraded = renderTab()
    expect(await screen.findByText("AI 不可用，仅文件级结果")).toBeTruthy()
    expect(screen.getByText("分析降级")).toBeTruthy()
    expect(screen.getByRole("button", { name: "重新分析" })).toBeTruthy()
    degraded.unmount()
  })

  it("renders a failed analysis as an error state with a retry action", async () => {
    mockReady({
      analysis: analysisFixture({
        status: "failed",
        errors: [{ code: "llm_unavailable", message: "AI 服务不可用" }],
      }),
    })

    renderTab()

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("分析失败")
    expect(alert.textContent).toContain("AI 服务不可用")
    expect(screen.getByRole("button", { name: "重试分析" })).toBeTruthy()
  })

  it("lists changed files and opens the unified diff drawer", async () => {
    mockReady({
      changes: changesFixture({
        files: [TEXT_FILE],
        stats: { files: 1, additions: 40, deletions: 2, truncated: false },
      }),
    })
    mocks.getFileDiff.mockResolvedValue({
      baseCommit: BASE,
      headCommit: HEAD,
      path: TEXT_FILE.path,
      status: "M",
      binary: false,
      truncated: false,
      patch:
        '@@ -80,6 +80,8 @@\n const client = createClient()\n-const body = { amount }\n+const body = { amount, reason }\n+const reason = ""\n',
    } satisfies FileDiff)

    renderTab()

    const changesHeading = await screen.findByRole("heading", { name: "变更文件" })
    expect(changesHeading).toBeTruthy()
    expect(screen.getByText("src/api/refund.ts")).toBeTruthy()
    expect(screen.getByText("修改")).toBeTruthy()
    expect(screen.getByText("+40")).toBeTruthy()
    expect(screen.getByText("-2")).toBeTruthy()

    const fileButton = screen.getByRole("button", { name: "查看 src/api/refund.ts 的 diff" })
    fileButton.focus()
    fireEvent.click(fileButton)

    await waitFor(() =>
      expect(mocks.getFileDiff).toHaveBeenCalledWith(REPO_ID, {
        base: BASE,
        head: HEAD,
        path: "src/api/refund.ts",
      }),
    )

    const dialog = await screen.findByRole("dialog", { name: /src\/api\/refund\.ts/ })
    expect(dialog.textContent).toContain('+const reason = ""')
    expect(dialog.textContent).toContain("-const body = { amount }")
    expect(dialog.querySelectorAll('[data-line-kind="add"]')).toHaveLength(2)
    expect(dialog.querySelectorAll('[data-line-kind="del"]')).toHaveLength(1)
    expect(dialog.querySelectorAll('[data-line-kind="hunk"]')).toHaveLength(1)

    fireEvent.keyDown(dialog, { key: "Escape" })
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
    expect(document.activeElement).toBe(fileButton)
  })

  it("shows binary and truncated notices in the diff drawer", async () => {
    mockReady({
      changes: changesFixture({
        files: [TEXT_FILE, BINARY_FILE],
        stats: { files: 2, additions: 40, deletions: 2, truncated: false },
      }),
    })
    mocks.getFileDiff.mockImplementation((_repoId: number, params: { path: string }) =>
      Promise.resolve(
        params.path === BINARY_FILE.path
          ? {
              baseCommit: BASE,
              headCommit: HEAD,
              path: BINARY_FILE.path,
              status: "M",
              binary: true,
              truncated: false,
              patch: "",
            }
          : {
              baseCommit: BASE,
              headCommit: HEAD,
              path: TEXT_FILE.path,
              status: "M",
              binary: false,
              truncated: true,
              patch: "@@ -1 +1 @@\n-old\n+new\n",
            },
      ),
    )

    renderTab()

    fireEvent.click(await screen.findByRole("button", { name: "查看 assets/logo.png 的 diff" }))
    expect(await screen.findByText("二进制文件，无法展示 diff。")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "关闭 diff 抽屉" }))
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())

    fireEvent.click(screen.getByRole("button", { name: "查看 src/api/refund.ts 的 diff" }))
    expect(await screen.findByText("diff 内容已截断，仅展示部分内容。")).toBeTruthy()
  })

  it("starts an analysis when none exists and refreshes the latest state", async () => {
    mockReady({ analysis: notFound("analysis not found") })
    mocks.getLatestAnalysis
      .mockRejectedValueOnce(notFound("analysis not found"))
      .mockResolvedValue(analysisFixture({ status: "running", finishedAt: null }))
    mocks.startAnalysis.mockResolvedValue({
      id: ANALYSIS_ID,
      status: "queued",
      analysisVersion: 1,
      reused: false,
    })

    renderTab()

    expect(await screen.findByText("尚未开始分析")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "开始分析" }))

    await waitFor(() => expect(mocks.startAnalysis).toHaveBeenCalledWith(VERSION_ID, {}))
    expect(await screen.findByText(/AI 影响分析运行中/)).toBeTruthy()
  })

  it("confirms a forced re-analysis from the completed state", async () => {
    mockReady({ analysis: analysisFixture() })
    mocks.getLatestAnalysis
      .mockResolvedValueOnce(analysisFixture())
      .mockResolvedValue(analysisFixture({ status: "running", finishedAt: null }))
    mocks.startAnalysis.mockResolvedValue({
      id: ANALYSIS_ID,
      status: "queued",
      analysisVersion: 2,
      reused: false,
    })

    renderTab()

    expect(await screen.findByText("分析完成")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "重新分析" }))

    const confirm = await screen.findByRole("alertdialog")
    expect(confirm.textContent).toContain("忽略缓存")
    fireEvent.click(screen.getByRole("button", { name: "确认重新分析" }))

    await waitFor(() =>
      expect(mocks.startAnalysis).toHaveBeenCalledWith(VERSION_ID, { force: true }),
    )
    expect(await screen.findByText(/AI 影响分析运行中/)).toBeTruthy()
  })

  it("stops polling after done/failed and on unmount", async () => {
    vi.useFakeTimers()
    try {
      // done stops the interval.
      mockReady({ analysis: analysisFixture({ status: "running", finishedAt: null }) })
      mocks.getLatestAnalysis
        .mockResolvedValueOnce(analysisFixture({ status: "running", finishedAt: null }))
        .mockResolvedValue(analysisFixture())
      const doneRun = renderTab()
      await flushPromises()
      expect(mocks.getLatestAnalysis).toHaveBeenCalledTimes(1)

      await act(async () => {
        await vi.advanceTimersByTimeAsync(1500)
      })
      expect(mocks.getLatestAnalysis).toHaveBeenCalledTimes(2)

      await act(async () => {
        await vi.advanceTimersByTimeAsync(6000)
      })
      expect(mocks.getLatestAnalysis).toHaveBeenCalledTimes(2)
      doneRun.unmount()

      // failed also stops the interval.
      resetMocks()
      mockReady({ analysis: analysisFixture({ status: "running", finishedAt: null }) })
      mocks.getLatestAnalysis
        .mockResolvedValueOnce(analysisFixture({ status: "running", finishedAt: null }))
        .mockResolvedValue(
          analysisFixture({
            status: "failed",
            errors: [{ code: "llm_unavailable", message: "AI 服务不可用" }],
          }),
        )
      const failedRun = renderTab()
      await flushPromises()
      expect(mocks.getLatestAnalysis).toHaveBeenCalledTimes(1)

      await act(async () => {
        await vi.advanceTimersByTimeAsync(1500)
      })
      expect(mocks.getLatestAnalysis).toHaveBeenCalledTimes(2)

      await act(async () => {
        await vi.advanceTimersByTimeAsync(6000)
      })
      expect(mocks.getLatestAnalysis).toHaveBeenCalledTimes(2)
      failedRun.unmount()

      // unmount stops the interval while the analysis is still running.
      resetMocks()
      mockReady({ analysis: analysisFixture({ status: "running", finishedAt: null }) })
      mocks.getLatestAnalysis.mockResolvedValue(
        analysisFixture({ status: "running", finishedAt: null }),
      )
      const unmounted = renderTab()
      await flushPromises()
      expect(mocks.getLatestAnalysis).toHaveBeenCalledTimes(1)
      unmounted.unmount()

      await act(async () => {
        await vi.advanceTimersByTimeAsync(6000)
      })
      expect(mocks.getLatestAnalysis).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it("pauses polling while the document is hidden and refreshes when it is visible", async () => {
    vi.useFakeTimers()
    const hiddenSpy = vi.spyOn(document, "hidden", "get").mockReturnValue(true)
    try {
      mockReady({ analysis: analysisFixture({ status: "running", finishedAt: null }) })
      mocks.getLatestAnalysis.mockResolvedValue(
        analysisFixture({ status: "running", finishedAt: null }),
      )
      renderTab()
      await flushPromises()
      expect(mocks.getLatestAnalysis).toHaveBeenCalledTimes(1)

      await act(async () => {
        await vi.advanceTimersByTimeAsync(3000)
      })
      expect(mocks.getLatestAnalysis).toHaveBeenCalledTimes(1)

      hiddenSpy.mockReturnValue(false)
      await act(async () => {
        document.dispatchEvent(new Event("visibilitychange"))
      })
      await flushPromises()
      expect(mocks.getLatestAnalysis).toHaveBeenCalledTimes(2)
    } finally {
      hiddenSpy.mockRestore()
      vi.useRealTimers()
    }
  })

  it("refreshes the latest analysis and change list after code.* SSE events", async () => {
    vi.useFakeTimers()
    try {
      mockReady({ analysis: analysisFixture() })
      const view = renderTab()
      await flushPromises()
      expect(mocks.getLatestAnalysis).toHaveBeenCalledTimes(1)
      expect(mocks.getVersionChanges).toHaveBeenCalledTimes(1)

      mocks.useVersionStream.mockReturnValue({
        events: [codeEvent(7)],
        status: "live",
        reconnect: vi.fn(),
      })
      view.rerender(<CodeChangesTab versionId={VERSION_ID} repoId={REPO_ID} />)
      await flushPromises()

      await act(async () => {
        await vi.advanceTimersByTimeAsync(299)
      })
      expect(mocks.getLatestAnalysis).toHaveBeenCalledTimes(1)
      expect(mocks.getVersionChanges).toHaveBeenCalledTimes(1)

      await act(async () => {
        await vi.advanceTimersByTimeAsync(1)
      })
      await flushPromises()
      expect(mocks.getLatestAnalysis).toHaveBeenCalledTimes(2)
      expect(mocks.getVersionChanges).toHaveBeenCalledTimes(2)
    } finally {
      vi.useRealTimers()
    }
  })

  it("keeps the string version id and number repo id in every code-intel call", async () => {
    mockReady()

    renderTab()
    await screen.findByRole("heading", { name: "变更文件" })

    expect(mocks.getVersionChanges).toHaveBeenCalledWith(VERSION_ID)
    expect(mocks.getVersionChanges.mock.calls[0]?.[0]).toBe(VERSION_ID)
    expect(mocks.getLatestIndexRun).toHaveBeenCalledWith(REPO_ID)
    expect(typeof mocks.getLatestIndexRun.mock.calls[0]?.[0]).toBe("number")
    expect(mocks.getLatestAnalysis).toHaveBeenCalledWith(VERSION_ID)
    expect(typeof mocks.getLatestAnalysis.mock.calls[0]?.[0]).toBe("string")
  })
})
