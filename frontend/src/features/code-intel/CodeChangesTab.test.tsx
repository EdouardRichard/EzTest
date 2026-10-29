import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { Requirement, VersionEvent } from "@/features/versions/types"
import { ApiError } from "@/lib/api-client"

import type {
  Analysis,
  AnalysisStats,
  Anchor,
  Candidate,
  ChangedFile,
  ChangeList,
  CodeEntity,
  FileDiff,
  IndexRun,
  RepoQuality,
  UnmappedChange,
  VersionChangeBatch,
} from "./types"

const mocks = vi.hoisted(() => ({
  getVersionChanges: vi.fn(),
  getLatestIndexRun: vi.fn(),
  getLatestAnalysis: vi.fn(),
  startAnalysis: vi.fn(),
  getFileDiff: vi.fn(),
  getFacts: vi.fn(),
  getFile: vi.fn(),
  patchCandidate: vi.fn(),
  listAnchors: vi.fn(),
  createAnchor: vi.fn(),
  deleteAnchor: vi.fn(),
  getRepoQuality: vi.fn(),
  listRequirements: vi.fn(),
  useVersionStream: vi.fn(),
}))

vi.mock("./api", () => ({
  getVersionChanges: mocks.getVersionChanges,
  getLatestIndexRun: mocks.getLatestIndexRun,
  getLatestAnalysis: mocks.getLatestAnalysis,
  startAnalysis: mocks.startAnalysis,
  getFileDiff: mocks.getFileDiff,
  getFacts: mocks.getFacts,
  getFile: mocks.getFile,
  patchCandidate: mocks.patchCandidate,
  listAnchors: mocks.listAnchors,
  createAnchor: mocks.createAnchor,
  deleteAnchor: mocks.deleteAnchor,
  getRepoQuality: mocks.getRepoQuality,
}))

vi.mock("@/features/versions/api", () => ({
  listRequirements: mocks.listRequirements,
}))

vi.mock("@/features/versions/useVersionStream", () => ({
  useVersionStream: mocks.useVersionStream,
}))

import { CodeChangesTab } from "./CodeChangesTab"
import { MAX_UNMAPPED_ENTRIES } from "./UnmappedChanges"

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

function capUnmappedEntries(): UnmappedChange[] {
  return Array.from({ length: MAX_UNMAPPED_ENTRIES }, (_value, index) => ({
    path: `src/generated/file-${index}.ts`,
    status: "M",
    reason: "no_fact_or_anchor",
    suggestion: "",
  }))
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

const INDEX_RUNNING: IndexRun = {
  ...INDEX_DONE,
  status: "running",
  stats: undefined,
  finishedAt: null,
}

const REPO_QUALITY: RepoQuality = {
  repoId: REPO_ID,
  generatedAt: "2026-10-29T10:00:00Z",
  evidence: { checks: 20, failures: 5, passRate: 0.75 },
  invalidFacts: 2,
  confidence: { high: 4, medium: 2, low: 1, total: 7 },
  reviews: { total: 3, correct: 2, incorrect: 1 },
  impact: {
    precision: 0.75,
    recall: 0.8,
    available: true,
    evalRunId: "9201",
    evaluatedAt: "2026-10-29T08:00:00Z",
  },
  cacheHitRate: 0.5,
  analysis: { id: ANALYSIS_ID, headCommit: HEAD, analyzedAt: "2026-10-29T09:00:00Z" },
  indexedCommit: HEAD,
}

const CANDIDATE: Candidate = {
  id: "7001",
  subjectKind: "requirement",
  subjectId: "9001",
  reason: "changed api POST /api/refund",
  confidence: "high",
  source: "direct",
  evidence: [{ file: "src/api/refund.ts", startLine: 80, endLine: 96 }],
  selected: true,
}

const FACT_ENTITY: CodeEntity = {
  id: "1844674407370955201",
  repoId: REPO_ID,
  commit: HEAD,
  kind: "route",
  path: "src/routes/orders.ts",
  name: "GET /orders",
  startLine: 1,
  endLine: 10,
  signature: "",
  blobHash: "git-sha1:1111111111111111111111111111111111111111",
  source: "ai",
  confidence: "high",
  evidence: [{ file: "src/routes/orders.ts", startLine: 2, endLine: 3 }],
}

const REQUIREMENT: Requirement = {
  id: "1844674407370956001",
  versionId: VERSION_ID,
  title: "退款需求",
  description: "",
  acceptanceCriteria: "",
  priority: "must",
  status: "draft",
  source: "user",
  lastUpdateSource: "user",
  revision: 1,
  confirmedAt: null,
  confirmedBy: null,
  openQuestionCount: 0,
  relationCount: 0,
  attachmentCount: 0,
  createdBy: 1,
  updatedBy: 1,
  createdAt: "2026-10-27T10:00:00Z",
  updatedAt: "2026-10-27T10:00:00Z",
}

const CREATED_ANCHOR: Anchor = {
  id: "1844674407370955301",
  versionId: VERSION_ID,
  subjectKind: "requirement",
  subjectId: REQUIREMENT.id,
  filePath: TEXT_FILE.path,
  startLine: null,
  endLine: null,
  commitSha: HEAD,
  confidence: "high",
  createdByKind: "user",
  createdBy: 1,
  createdAt: "2026-10-28T01:00:00Z",
}

/** Two batch snapshots: a mapped batch with cases plus an unmapped file, and an
 * empty later batch (the「本批无代码变更」state). */
function batchTimelineFixture(): VersionChangeBatch[] {
  return [
    {
      batchId: "701",
      batchNo: 1,
      batchStatus: "done",
      baseCommit: BASE,
      headCommit: HEAD,
      historyRewritten: false,
      changedFiles: 2,
      impactedCases: 2,
      unmappedCount: 1,
      computedAt: "2026-10-29T04:00:00Z",
      items: [
        {
          file: "src/api/refund.ts",
          changeKind: "modified",
          subjects: [{ kind: "api", name: "POST /api/refund" }],
          caseIds: ["501", "502"],
          unmapped: false,
        },
        {
          file: "src/legacy/old.ts",
          changeKind: "deleted",
          subjects: [],
          caseIds: [],
          unmapped: true,
        },
      ],
    },
    {
      batchId: "702",
      batchNo: 2,
      batchStatus: "done",
      baseCommit: HEAD,
      headCommit: "cccccccccccccccccccccccccccccccccccccccc",
      historyRewritten: true,
      changedFiles: 0,
      impactedCases: 0,
      unmappedCount: 0,
      computedAt: "2026-10-29T05:00:00Z",
      items: [],
    },
  ]
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
  // The panels mounted in the T11 slots fetch facts and requirements on mount.
  mocks.getFacts.mockResolvedValue({ items: [], nextCursor: "", hasMore: false })
  mocks.listRequirements.mockResolvedValue({ items: [REQUIREMENT], nextCursor: "", hasMore: false })
  mocks.listAnchors.mockResolvedValue([])
  mocks.getRepoQuality.mockResolvedValue(REPO_QUALITY)
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
    // The summary and the impact panel both state the degraded mode.
    expect((await screen.findAllByText("AI 不可用，仅文件级结果")).length).toBeGreaterThan(0)
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

  it("mounts the repository quality block for the bound repository", async () => {
    mockReady()

    renderTab()

    const panel = await screen.findByTestId("repo-quality-panel")
    expect(panel.textContent).toContain("代码质量")
    await waitFor(() => expect(mocks.getRepoQuality).toHaveBeenCalledWith(REPO_ID))

    // An unbound version has no repository to aggregate.
    cleanup()
    resetMocks()
    renderTab({ repoId: null })
    expect(await screen.findByRole("heading", { name: "未绑定代码仓库" })).toBeTruthy()
    expect(mocks.getRepoQuality).not.toHaveBeenCalled()
  })

  it("renders the batch timeline with commits, entities, cases, unmapped and history rewrite", async () => {
    mockReady({ changes: changesFixture({ batchTimeline: batchTimelineFixture() }) })

    renderTab()

    // Batch 1→N ordering with the commit pair and per-batch counters.
    const heading = await screen.findByRole("heading", { name: "批次时间线" })
    expect(heading).toBeTruthy()
    const timeline = screen.getByTestId("code-batch-timeline")
    expect(timeline.textContent).toContain("Batch 1")
    expect(timeline.textContent).toContain("Batch 2")
    expect(timeline.textContent).toContain("01234567")
    expect(timeline.textContent).toContain("变更文件 2 · 影响用例 2 · 未映射 1")
    // Change file → entity → affected case chain.
    expect(timeline.textContent).toContain("src/api/refund.ts")
    expect(timeline.textContent).toContain("POST /api/refund")
    expect(timeline.textContent).toContain("用例 501")
    expect(timeline.textContent).toContain("用例 502")
    // Unmapped change of the batch.
    const unmapped = screen.getByTestId("batch-unmapped-701")
    expect(unmapped.textContent).toContain("src/legacy/old.ts")
    // D49 history rewrite marker and the per-batch empty state.
    expect(timeline.textContent).toContain("历史重写")
    expect(timeline.textContent).toContain("本批无代码变更")
    // DOM order: Batch 1 is rendered before Batch 2 (spec §7.2 Batch 1→N).
    const renderedOrder = Array.from(
      timeline.querySelectorAll('[data-testid^="batch-timeline-"]'),
    ).map((node) => node.getAttribute("data-testid"))
    expect(renderedOrder).toEqual(["batch-timeline-701", "batch-timeline-702"])
  })

  it("marks a provisionally computed batch as mapping in progress, never as unmapped", async () => {
    const provisional: VersionChangeBatch = {
      batchId: "801",
      batchNo: 1,
      batchStatus: "done",
      baseCommit: BASE,
      headCommit: HEAD,
      historyRewritten: false,
      changedFiles: 2,
      impactedCases: 0,
      unmappedCount: 2,
      provisional: true,
      computedAt: "2026-10-29T06:00:00Z",
      items: [
        {
          file: "src/api/refund.ts",
          changeKind: "modified",
          subjects: [],
          caseIds: [],
          unmapped: true,
        },
        {
          file: "src/new/checkout.ts",
          changeKind: "added",
          subjects: [],
          caseIds: [],
          unmapped: true,
        },
      ],
    }
    mockReady({ changes: changesFixture({ batchTimeline: [provisional] }) })

    renderTab()

    const timeline = await screen.findByTestId("code-batch-timeline")
    const hint = await screen.findByTestId("batch-provisional-801")
    expect(hint.textContent).toContain("映射生成中")
    expect(timeline.textContent).toContain("映射生成中")
    // The pre-analysis coverage verdict is not a real unmapped verdict: do not
    // render the misleading per-item badge, counter or the unmapped section.
    expect(screen.queryByTestId("batch-unmapped-801")).toBeNull()
    expect(timeline.textContent).not.toContain("未映射 2")
    expect(timeline.querySelectorAll("[data-unmapped-file]").length).toBe(0)
    // The changed-file shell (path + kind) is still visible.
    expect(timeline.textContent).toContain("src/api/refund.ts")
    expect(timeline.textContent).toContain("src/new/checkout.ts")
  })

  it("renders a single unavailable batch without failing the whole timeline", async () => {
    const unavailable: VersionChangeBatch = {
      batchId: "802",
      batchNo: 2,
      batchStatus: "done",
      baseCommit: BASE,
      headCommit: HEAD,
      historyRewritten: false,
      changedFiles: 0,
      impactedCases: 0,
      unmappedCount: 0,
      unavailable: true,
      computedAt: "2026-10-29T06:30:00Z",
      items: [],
    }
    mockReady({ changes: changesFixture({ batchTimeline: [unavailable] }) })

    renderTab()

    const unavailableHint = await screen.findByTestId("batch-unavailable-802")
    expect(unavailableHint.textContent).toContain("暂不可用")
    // An unavailable batch must not masquerade as a real "no changes" batch.
    expect(screen.getByTestId("code-batch-timeline").textContent).not.toContain("本批无代码变更")
  })

  it("keeps the legacy change list when the backend omits batchTimeline", async () => {
    mockReady()

    renderTab()

    expect(await screen.findByRole("heading", { name: "变更文件" })).toBeTruthy()
    expect(screen.queryByRole("heading", { name: "批次时间线" })).toBeNull()
    expect(screen.getByText("src/api/refund.ts")).toBeTruthy()
  })

  it("retries the load and renders the batch timeline after a failure", async () => {
    mocks.getVersionChanges
      .mockRejectedValueOnce(new Error("network down"))
      .mockResolvedValueOnce(changesFixture({ batchTimeline: batchTimelineFixture() }))
    mocks.getLatestIndexRun.mockResolvedValue(INDEX_DONE)
    mocks.getLatestAnalysis.mockResolvedValue(analysisFixture())

    renderTab()

    expect(await screen.findByRole("heading", { name: "加载代码变更失败" })).toBeTruthy()
    expect(screen.getByText("network down")).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByRole("heading", { name: "批次时间线" })).toBeTruthy()
    expect(mocks.getVersionChanges).toHaveBeenCalledTimes(2)
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
    expect(screen.getAllByText("修改").length).toBeGreaterThan(0)
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

  it("mounts the facts, impact and unmapped panels in the T11 slots", async () => {
    mockReady({
      analysis: analysisFixture({ candidates: [CANDIDATE] }),
    })
    mocks.getFacts.mockImplementation((_repoId: number, params: { kind?: string }) =>
      Promise.resolve({
        items: params.kind === FACT_ENTITY.kind ? [FACT_ENTITY] : [],
        nextCursor: "",
        hasMore: false,
      }),
    )
    mocks.patchCandidate.mockResolvedValue({ ...CANDIDATE, selected: false })

    renderTab()

    expect(await screen.findByRole("heading", { name: "代码事实" })).toBeTruthy()
    expect(await screen.findByText("GET /orders")).toBeTruthy()
    expect(mocks.getFacts).toHaveBeenCalledWith(REPO_ID, {
      commit: HEAD,
      kind: "route",
      limit: 50,
    })
    expect(screen.getByRole("heading", { name: "影响候选" })).toBeTruthy()
    expect(screen.getByText("changed api POST /api/refund")).toBeTruthy()
    expect(screen.getByRole("heading", { name: "未映射变更" })).toBeTruthy()
    expect(screen.getByText("无事实或锚点覆盖")).toBeTruthy()
    expect(screen.queryByTestId("code-facts-slot")?.textContent).toContain("GET /orders")

    const checkbox = screen.getByRole("checkbox", { name: "选择需求 9001" })
    expect((checkbox as HTMLInputElement).checked).toBe(true)
    fireEvent.click(checkbox)

    await waitFor(() =>
      expect(mocks.patchCandidate).toHaveBeenCalledWith(
        VERSION_ID,
        ANALYSIS_ID,
        CANDIDATE.id,
        false,
      ),
    )
    await waitFor(() => expect((checkbox as HTMLInputElement).checked).toBe(false))
  })

  it("warns when the analysis stats mark the unmapped list truncated", async () => {
    mockReady({
      analysis: analysisFixture({
        stats: {
          ...ANALYSIS_STATS,
          unmapped: MAX_UNMAPPED_ENTRIES,
          unmappedTruncated: true,
        },
        unmapped: capUnmappedEntries(),
      }),
    })

    renderTab()

    expect(await screen.findByRole("heading", { name: "未映射变更" })).toBeTruthy()
    expect(
      screen.getByText(`未映射变更超过上限，仅展示前 ${MAX_UNMAPPED_ENTRIES} 条。`),
    ).toBeTruthy()
  })

  it("opens the anchor dialog from a changed file row and refreshes after creation", async () => {
    mockReady()
    mocks.createAnchor.mockResolvedValue(CREATED_ANCHOR)

    renderTab()

    const anchorAction = await screen.findByRole("button", {
      name: "为 src/api/refund.ts 关联需求",
    })
    fireEvent.click(anchorAction)

    expect(await screen.findByRole("dialog", { name: "关联需求锚点" })).toBeTruthy()
    expect(mocks.listRequirements).toHaveBeenCalledWith(VERSION_ID, { limit: 100 })
    await screen.findByRole("option", { name: /退款需求/ })

    fireEvent.change(screen.getByLabelText("需求"), {
      target: { value: REQUIREMENT.id },
    })
    const beforeRefresh = mocks.getLatestAnalysis.mock.calls.length
    fireEvent.click(screen.getByRole("button", { name: "创建锚点" }))

    await waitFor(() =>
      expect(mocks.createAnchor).toHaveBeenCalledWith(VERSION_ID, {
        subjectKind: "requirement",
        subjectId: REQUIREMENT.id,
        filePath: TEXT_FILE.path,
      }),
    )
    await waitFor(() =>
      expect(mocks.getLatestAnalysis.mock.calls.length).toBeGreaterThan(beforeRefresh),
    )

    fireEvent.keyDown(screen.getByRole("dialog", { name: "关联需求锚点" }), { key: "Escape" })
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  })

  it("stops index polling after 60 attempts and offers a manual refresh", async () => {
    vi.useFakeTimers()
    try {
      mockReady({ index: INDEX_RUNNING, analysis: notFound() })

      renderTab()
      await flushPromises()
      expect(screen.getByRole("heading", { name: "代码仓库正在索引" })).toBeTruthy()
      expect(mocks.getLatestIndexRun).toHaveBeenCalledTimes(1)

      await act(async () => {
        await vi.advanceTimersByTimeAsync(1500 * 61)
      })
      await flushPromises()

      expect(mocks.getLatestIndexRun).toHaveBeenCalledTimes(1 + 60)
      expect(screen.getByText(/已停止自动刷新/)).toBeTruthy()

      await act(async () => {
        await vi.advanceTimersByTimeAsync(6000)
      })
      expect(mocks.getLatestIndexRun).toHaveBeenCalledTimes(1 + 60)

      // The manual status refresh reads the terminal run and leaves the state.
      mocks.getLatestIndexRun.mockResolvedValue(INDEX_DONE)
      fireEvent.click(screen.getByRole("button", { name: "刷新索引状态" }))
      await flushPromises()
      expect(screen.getByRole("heading", { name: "变更文件" })).toBeTruthy()
      expect(screen.queryByText(/已停止自动刷新/)).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })
})
