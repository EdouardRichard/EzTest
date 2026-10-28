import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

import { mindmapApi } from "./api"
import { ExportMenu } from "./ExportMenu"
import { downloadBlob, svgToPngBlob } from "./png-export"
import { ReviewPanel } from "./ReviewPanel"
import { RUN_POLL_INTERVAL_MS, RunPanel } from "./RunPanel"
import type {
  BaselineView,
  CaseExportView,
  CaseFindingView,
  CaseNodeView,
  CaseReviewView,
  CaseRunView,
  LatestRuns,
  MindmapGraph,
  ReviewResult,
  SubmitReviewResult,
} from "./types"

vi.mock("./api", () => ({
  mindmapApi: {
    listFindings: vi.fn(),
    updateFinding: vi.fn(),
    startRun: vi.fn(),
    getRun: vi.fn(),
    decideReview: vi.fn(),
    submitReview: vi.fn(),
    createExport: vi.fn(),
    downloadExport: vi.fn(),
  },
}))

vi.mock("./png-export", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./png-export")>()
  return { ...actual, svgToPngBlob: vi.fn(), downloadBlob: vi.fn() }
})

const api = {
  listFindings: vi.mocked(mindmapApi.listFindings),
  updateFinding: vi.mocked(mindmapApi.updateFinding),
  startRun: vi.mocked(mindmapApi.startRun),
  getRun: vi.mocked(mindmapApi.getRun),
  decideReview: vi.mocked(mindmapApi.decideReview),
  submitReview: vi.mocked(mindmapApi.submitReview),
  createExport: vi.mocked(mindmapApi.createExport),
  downloadExport: vi.mocked(mindmapApi.downloadExport),
}

const png = {
  svgToPngBlob: vi.mocked(svgToPngBlob),
  downloadBlob: vi.mocked(downloadBlob),
}

const VERSION_ID = "1844674407370955161"
const NODE_ID = "1844674407370955103"
const BLOCKER_ID = "1844674407370955201"
const MAJOR_ID = "1844674407370955202"
const MINOR_ID = "1844674407370955203"
const INFO_ID = "1844674407370955204"
const REVIEW_ID = "1844674407370955301"
const RUN_ID = "1844674407370955401"
const EXPORT_ID = "1844674407370955501"
const TIMESTAMP = "2026-10-28T10:00:00Z"

function caseNode(overrides: Partial<CaseNodeView> = {}): CaseNodeView {
  return {
    id: NODE_ID,
    parentId: null,
    kind: "case",
    title: "取消订单",
    description: "",
    priority: "P1",
    preconditions: "",
    testData: null,
    source: "user_input",
    status: "draft",
    sortOrder: 0,
    revision: 1,
    steps: [],
    assertions: [],
    sources: [],
    anchors: [],
    openBlockerCount: 0,
    ...overrides,
  }
}

function mindmapGraph(overrides: Partial<MindmapGraph> = {}): MindmapGraph {
  return {
    versionId: VERSION_ID,
    versionStatus: "reviewing",
    editable: true,
    nodes: [caseNode()],
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

function graphWithLatest(latestRuns: LatestRuns): MindmapGraph {
  return mindmapGraph({ latestRuns })
}

function finding(
  id: string,
  severity: CaseFindingView["severity"],
  overrides: Partial<CaseFindingView> = {},
): CaseFindingView {
  return {
    id,
    reviewId: REVIEW_ID,
    nodeId: NODE_ID,
    category: "coverage",
    severity,
    detail: `${severity} 级别的评审发现`,
    status: "open",
    source: "ai",
    createdBy: 1,
    createdAt: TIMESTAMP,
    updatedAt: TIMESTAMP,
    ...overrides,
  }
}

function baselineView(overrides: Partial<BaselineView> = {}): BaselineView {
  return {
    id: "1844674407370955601",
    graphHash: "abc123",
    caseCount: 4,
    nodeCount: 6,
    frozenAt: TIMESTAMP,
    codeBaselineCommit: null,
    ...overrides,
  }
}

function reviewView(overrides: Partial<CaseReviewView> = {}): CaseReviewView {
  return {
    id: REVIEW_ID,
    versionId: VERSION_ID,
    runId: null,
    status: "draft",
    comment: "",
    reviewerId: 1,
    decidedBy: null,
    decidedAt: null,
    createdAt: TIMESTAMP,
    updatedAt: TIMESTAMP,
    ...overrides,
  }
}

function caseRun(overrides: Partial<CaseRunView> = {}): CaseRunView {
  return {
    id: RUN_ID,
    versionId: VERSION_ID,
    kind: "design",
    status: "queued",
    reason: "",
    stats: null,
    errors: [],
    agentSessionId: null,
    agentRunId: null,
    model: "",
    promptVersion: "",
    createdBy: 1,
    createdAt: TIMESTAMP,
    startedAt: null,
    finishedAt: null,
    ...overrides,
  }
}

function exportView(overrides: Partial<CaseExportView> = {}): CaseExportView {
  return {
    id: EXPORT_ID,
    versionId: VERSION_ID,
    format: "markdown",
    nodeCount: 6,
    sizeBytes: 1024,
    baselineId: null,
    createdAt: TIMESTAMP,
    ...overrides,
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  api.listFindings.mockResolvedValue([])
  api.updateFinding.mockResolvedValue(finding(BLOCKER_ID, "blocker", { status: "fixed" }))
  api.startRun.mockResolvedValue({ runId: RUN_ID })
  api.getRun.mockResolvedValue(caseRun())
  api.decideReview.mockResolvedValue({ review: reviewView() } as ReviewResult)
  api.submitReview.mockResolvedValue({} as SubmitReviewResult)
  api.createExport.mockResolvedValue(exportView())
  api.downloadExport.mockResolvedValue({
    blob: new Blob(["export"], { type: "text/markdown" }),
    filename: "mindmap-export.md",
  })
  png.svgToPngBlob.mockResolvedValue(new Blob(["png"], { type: "image/png" }))
  png.downloadBlob.mockImplementation(() => undefined)
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

describe("ReviewPanel", () => {
  it("lists the findings grouped by severity, one row per finding", async () => {
    api.listFindings.mockResolvedValue([
      finding(BLOCKER_ID, "blocker"),
      finding(MAJOR_ID, "major"),
      finding(MINOR_ID, "minor"),
      finding(INFO_ID, "info", { status: "fixed" }),
    ])

    render(<ReviewPanel versionId={VERSION_ID} graph={mindmapGraph()} />)

    await screen.findByTestId(`finding-row-${BLOCKER_ID}`)

    expect(screen.getByTestId("findings-group-blocker")).toBeTruthy()
    expect(screen.getByTestId("findings-group-major")).toBeTruthy()
    expect(screen.getByTestId("findings-group-minor")).toBeTruthy()
    expect(screen.getByTestId("findings-group-info")).toBeTruthy()
    expect(
      screen.getAllByTestId(/^findings-group-/).map((group) => group.getAttribute("data-testid")),
    ).toEqual([
      "findings-group-blocker",
      "findings-group-major",
      "findings-group-minor",
      "findings-group-info",
    ])
    expect(screen.getByTestId(`finding-row-${INFO_ID}`).textContent).toContain("已修复")
  })

  it("keeps approve disabled until an open blocker is acknowledged", async () => {
    api.listFindings.mockResolvedValue([finding(BLOCKER_ID, "blocker")])
    api.decideReview.mockResolvedValue({
      baseline: baselineView(),
      review: reviewView({ status: "approved" }),
    } as ReviewResult)
    const onChanged = vi.fn()

    render(<ReviewPanel versionId={VERSION_ID} graph={mindmapGraph()} onChanged={onChanged} />)

    await screen.findByTestId(`finding-row-${BLOCKER_ID}`)
    const approve = screen.getByTestId("btn-approve") as HTMLButtonElement
    expect(approve.disabled).toBe(true)

    fireEvent.click(screen.getByLabelText("已知悉 blocker 并继续"))
    expect(approve.disabled).toBe(false)

    fireEvent.click(approve)

    await waitFor(() =>
      expect(api.decideReview).toHaveBeenCalledWith(VERSION_ID, {
        decision: "approve",
        acknowledgeFindings: true,
      }),
    )
    expect((await screen.findByTestId("baseline-badge")).textContent).toContain("已绑定基线")
    expect(onChanged).toHaveBeenCalled()
  })

  it("requires a non-empty comment before rejecting", async () => {
    api.decideReview.mockResolvedValue({ review: reviewView() } as ReviewResult)

    render(<ReviewPanel versionId={VERSION_ID} graph={mindmapGraph()} />)

    await screen.findByText("暂无评审发现项")
    const reject = screen.getByTestId("btn-reject") as HTMLButtonElement
    const comment = screen.getByLabelText("评审评论")

    expect(reject.disabled).toBe(true)

    fireEvent.change(comment, { target: { value: "   " } })
    expect(reject.disabled).toBe(true)

    fireEvent.change(comment, { target: { value: "缺少边界用例" } })
    expect(reject.disabled).toBe(false)

    fireEvent.click(reject)

    await waitFor(() =>
      expect(api.decideReview).toHaveBeenCalledWith(VERSION_ID, {
        decision: "reject",
        comment: "缺少边界用例",
      }),
    )
  })

  it("surfaces a mapped 409 message when approval is rejected by the server", async () => {
    api.decideReview.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "conflict",
        message: "blocker findings are still open",
        details: { reason: "blocker_findings_open" },
      }),
    )

    render(<ReviewPanel versionId={VERSION_ID} graph={mindmapGraph()} />)

    await screen.findByText("暂无评审发现项")
    fireEvent.click(screen.getByTestId("btn-approve"))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("仍存在未处理的 blocker 评审意见。")
  })

  it("marks a finding fixed through the API and updates its row", async () => {
    api.listFindings.mockResolvedValue([finding(BLOCKER_ID, "blocker")])
    api.updateFinding.mockResolvedValue(finding(BLOCKER_ID, "blocker", { status: "fixed" }))
    const onChanged = vi.fn()

    render(<ReviewPanel versionId={VERSION_ID} graph={mindmapGraph()} onChanged={onChanged} />)

    const row = await screen.findByTestId(`finding-row-${BLOCKER_ID}`)
    fireEvent.click(within(row).getByRole("button", { name: "标记已修复" }))

    await waitFor(() =>
      expect(api.updateFinding).toHaveBeenCalledWith(VERSION_ID, BLOCKER_ID, { status: "fixed" }),
    )
    await waitFor(() =>
      expect(screen.getByTestId(`finding-row-${BLOCKER_ID}`).textContent).toContain("已修复"),
    )
    expect(onChanged).toHaveBeenCalled()
  })

  it("starts a review run from the panel", async () => {
    render(<ReviewPanel versionId={VERSION_ID} graph={mindmapGraph()} />)

    await screen.findByText("暂无评审发现项")
    fireEvent.click(screen.getByTestId("btn-run-review"))

    await waitFor(() => expect(api.startRun).toHaveBeenCalledWith(VERSION_ID, { kind: "review" }))
    expect((await screen.findByRole("status")).textContent).toContain("评审已启动")
  })

  it("submits a designing version into the review stage", async () => {
    render(
      <ReviewPanel versionId={VERSION_ID} graph={mindmapGraph({ versionStatus: "designing" })} />,
    )

    await screen.findByText("暂无评审发现项")
    fireEvent.click(screen.getByTestId("btn-submit-review"))

    await waitFor(() => expect(api.submitReview).toHaveBeenCalledWith(VERSION_ID))
  })
})

describe("RunPanel", () => {
  it("polls a running run every two seconds and stops once it is terminal", async () => {
    vi.useFakeTimers()
    api.getRun
      .mockResolvedValueOnce(caseRun({ status: "running" }))
      .mockResolvedValueOnce(caseRun({ status: "done", stats: { materialized: true } }))
    const onChanged = vi.fn()

    render(
      <RunPanel
        versionId={VERSION_ID}
        graph={graphWithLatest({ design: { id: RUN_ID, status: "running" } })}
        onChanged={onChanged}
      />,
    )

    await act(async () => {
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(api.getRun).toHaveBeenCalledTimes(1)
    expect(api.getRun).toHaveBeenCalledWith(VERSION_ID, RUN_ID)

    await act(async () => {
      vi.advanceTimersByTime(RUN_POLL_INTERVAL_MS)
      await Promise.resolve()
      await Promise.resolve()
    })

    expect(api.getRun).toHaveBeenCalledTimes(2)
    expect(screen.getByTestId("run-status-design").textContent).toContain("已完成")
    expect(onChanged).toHaveBeenCalled()

    await act(async () => {
      vi.advanceTimersByTime(RUN_POLL_INTERVAL_MS * 3)
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(api.getRun).toHaveBeenCalledTimes(2)
  })

  it("requires confirmation before starting a replace design run", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false)

    render(<RunPanel versionId={VERSION_ID} graph={mindmapGraph()} />)

    fireEvent.click(screen.getByLabelText("替换现有草稿"))
    fireEvent.click(screen.getByTestId("btn-generate"))

    expect(confirm).toHaveBeenCalled()
    expect(api.startRun).not.toHaveBeenCalled()

    confirm.mockReturnValue(true)
    fireEvent.click(screen.getByTestId("btn-generate"))

    await waitFor(() =>
      expect(api.startRun).toHaveBeenCalledWith(VERSION_ID, {
        kind: "design",
        mode: "replace",
        confirmReplace: true,
      }),
    )
  })

  it("triggers design, review and data_plan runs", async () => {
    render(<RunPanel versionId={VERSION_ID} graph={mindmapGraph()} />)

    fireEvent.click(screen.getByTestId("btn-generate"))
    await waitFor(() =>
      expect(api.startRun).toHaveBeenCalledWith(VERSION_ID, { kind: "design", mode: "append" }),
    )
    await waitFor(() =>
      expect((screen.getByTestId("btn-generate-review") as HTMLButtonElement).disabled).toBe(false),
    )

    fireEvent.click(screen.getByTestId("btn-generate-review"))
    await waitFor(() => expect(api.startRun).toHaveBeenCalledWith(VERSION_ID, { kind: "review" }))
    await waitFor(() =>
      expect((screen.getByTestId("btn-generate-data-plan") as HTMLButtonElement).disabled).toBe(
        false,
      ),
    )

    fireEvent.click(screen.getByTestId("btn-generate-data-plan"))
    await waitFor(() =>
      expect(api.startRun).toHaveBeenCalledWith(VERSION_ID, { kind: "data_plan" }),
    )
  })

  it("shows a degraded banner with the run reason, stats and manual-path hint", async () => {
    api.getRun.mockResolvedValue(
      caseRun({
        status: "degraded",
        reason: "ai_unavailable",
        stats: {
          contextTruncated: true,
          anchorsDropped: 2,
          skipped: 3,
          validationErrors: ["missing assertion", "missing step"],
        },
      }),
    )

    render(
      <RunPanel
        versionId={VERSION_ID}
        graph={graphWithLatest({ design: { id: RUN_ID, status: "degraded" } })}
      />,
    )

    const banner = await screen.findByTestId("run-banner-design")
    expect(banner.textContent).toContain("AI 服务暂不可用")
    expect(banner.textContent).toContain("手工")

    const stats = screen.getByTestId("run-stats-design")
    expect(stats.textContent).toContain("上下文已截断")
    expect(stats.textContent).toContain("锚点丢弃 2")
    expect(stats.textContent).toContain("跳过 3")
    expect(stats.textContent).toContain("校验错误 2")
  })
})

describe("ExportMenu", () => {
  it("creates and downloads each server-side format through the API", async () => {
    api.createExport.mockImplementation((_versionId, body) =>
      Promise.resolve(exportView({ id: `${EXPORT_ID}-${body.format}`, format: body.format })),
    )
    api.downloadExport.mockImplementation((_versionId, exportId, fallbackFilename) =>
      Promise.resolve({
        blob: new Blob(["export"]),
        filename: fallbackFilename === undefined ? `${exportId}.bin` : fallbackFilename,
      }),
    )

    render(<ExportMenu versionId={VERSION_ID} svgRef={{ current: null }} versionName="订单系统" />)

    fireEvent.click(screen.getByTestId("btn-export-markdown"))
    await waitFor(() =>
      expect(api.downloadExport).toHaveBeenCalledWith(
        VERSION_ID,
        `${EXPORT_ID}-markdown`,
        "mindmap-markdown.md",
      ),
    )
    expect(api.createExport).toHaveBeenCalledWith(VERSION_ID, { format: "markdown" })
    expect(png.downloadBlob).toHaveBeenCalledWith(expect.any(Blob), "mindmap-markdown.md")

    await waitFor(() =>
      expect((screen.getByTestId("btn-export-mermaid") as HTMLButtonElement).disabled).toBe(false),
    )
    fireEvent.click(screen.getByTestId("btn-export-mermaid"))
    await waitFor(() =>
      expect(api.createExport).toHaveBeenCalledWith(VERSION_ID, { format: "mermaid" }),
    )
    await waitFor(() =>
      expect((screen.getByTestId("btn-export-opml") as HTMLButtonElement).disabled).toBe(false),
    )
    fireEvent.click(screen.getByTestId("btn-export-opml"))
    await waitFor(() =>
      expect(api.createExport).toHaveBeenCalledWith(VERSION_ID, { format: "opml" }),
    )
  })

  it("exports the current canvas as a version-named PNG", async () => {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg")
    const blob = new Blob(["png"], { type: "image/png" })
    png.svgToPngBlob.mockResolvedValue(blob)

    render(
      <ExportMenu versionId={VERSION_ID} svgRef={{ current: svg }} versionName="订单系统 2.0" />,
    )

    fireEvent.click(screen.getByTestId("btn-export-png"))

    await waitFor(() => expect(png.svgToPngBlob).toHaveBeenCalledWith(svg))
    expect(png.downloadBlob).toHaveBeenCalledWith(blob, "订单系统 2.0-mindmap.png")
    expect(api.createExport).not.toHaveBeenCalled()
  })

  it("disables every export button while a download is in flight", async () => {
    let resolveExport: (value: CaseExportView) => void = () => undefined
    api.createExport.mockReturnValue(
      new Promise<CaseExportView>((resolve) => {
        resolveExport = resolve
      }),
    )

    render(<ExportMenu versionId={VERSION_ID} svgRef={{ current: null }} />)

    fireEvent.click(screen.getByTestId("btn-export-markdown"))

    await waitFor(() => expect(api.createExport).toHaveBeenCalled())
    expect((screen.getByTestId("btn-export-markdown") as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByTestId("btn-export-png") as HTMLButtonElement).disabled).toBe(true)

    resolveExport(exportView())
    await waitFor(() =>
      expect((screen.getByTestId("btn-export-markdown") as HTMLButtonElement).disabled).toBe(false),
    )
  })

  it("shows the baseline binding hint when the version is frozen", () => {
    render(
      <ExportMenu
        versionId={VERSION_ID}
        svgRef={{ current: null }}
        baseline={baselineView({ caseCount: 7 })}
      />,
    )

    const hint = screen.getByTestId("export-baseline-hint")
    expect(hint.textContent).toContain("已绑定基线")
    expect(hint.textContent).toContain("7 个用例")
  })
})
